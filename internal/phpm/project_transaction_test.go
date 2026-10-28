package phpm

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func transactionFixture(t *testing.T, hasLock bool) (string, projectTransaction) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "phpm.yaml"), []byte("original config"), 0600); err != nil {
		t.Fatal(err)
	}
	if hasLock {
		if err := os.WriteFile(filepath.Join(root, "phpm.lock"), []byte("original lock"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config, err := snapshotFile(filepath.Join(root, "phpm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := snapshotFile(filepath.Join(root, "phpm.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return root, projectTransaction{Schema: 1, BeforeConfig: config, BeforeLock: lock, AfterConfig: []byte("new config"), AfterLock: []byte("new lock")}
}

func TestProjectCommitAndFailureRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		root, journal := transactionFixture(t, true)
		write := atomicWrite
		if fail {
			write = func(path string, data []byte, mode os.FileMode) error {
				if filepath.Base(path) == "phpm.lock" {
					return errors.New("simulated lock-file write failure")
				}
				return atomicWrite(path, data, mode)
			}
		}
		err := commitProjectFilesWithWriter(root, journal.BeforeConfig, journal.BeforeLock, journal.AfterConfig, journal.AfterLock, write)
		if (err != nil) != fail {
			t.Fatalf("fail=%v result=%v", fail, err)
		}
		config, _ := os.ReadFile(filepath.Join(root, "phpm.yaml"))
		lock, _ := os.ReadFile(filepath.Join(root, "phpm.lock"))
		wantConfig, wantLock := journal.AfterConfig, journal.AfterLock
		if fail {
			wantConfig, wantLock = journal.BeforeConfig.Data, journal.BeforeLock.Data
		}
		if !bytes.Equal(config, wantConfig) || !bytes.Equal(lock, wantLock) {
			t.Fatalf("partially committed files: %q %q", config, lock)
		}
		if err := checkProjectTransaction(root); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInterruptedProjectTransactionRestoresOldState(t *testing.T) {
	for _, hadLock := range []bool{false, true} {
		root, journal := transactionFixture(t, hadLock)
		if err := writeJSON(filepath.Join(root, projectTransactionName), journal); err != nil {
			t.Fatal(err)
		}
		if err := atomicWrite(filepath.Join(root, "phpm.yaml"), journal.AfterConfig, 0600); err != nil {
			t.Fatal(err)
		}
		if err := atomicWrite(filepath.Join(root, "phpm.lock"), journal.AfterLock, 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkProjectTransaction(root); err == nil {
			t.Fatal("execution allowed with unfinished transaction")
		}
		if err := recoverProjectTransaction(root); err != nil {
			t.Fatal(err)
		}
		config, _ := os.ReadFile(filepath.Join(root, "phpm.yaml"))
		lock, err := os.ReadFile(filepath.Join(root, "phpm.lock"))
		if !bytes.Equal(config, journal.BeforeConfig.Data) {
			t.Fatal("old config not restored")
		}
		if hadLock {
			if err != nil || !bytes.Equal(lock, journal.BeforeLock.Data) {
				t.Fatal("old lock not restored")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal("new lock was not removed")
		}
	}
}

func TestProjectTransactionDoesNotOverwriteConcurrentEdits(t *testing.T) {
	root, journal := transactionFixture(t, true)
	if err := os.WriteFile(filepath.Join(root, "phpm.yaml"), []byte("editor change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := commitProjectFiles(root, journal.BeforeConfig, journal.BeforeLock, journal.AfterConfig, journal.AfterLock); err == nil {
		t.Fatal("commit overwrote a concurrent edit")
	}
	if err := writeJSON(filepath.Join(root, projectTransactionName), journal); err != nil {
		t.Fatal(err)
	}
	if err := recoverProjectTransaction(root); err == nil {
		t.Fatal("recovery overwrote a concurrent edit")
	}
	data, _ := os.ReadFile(filepath.Join(root, "phpm.yaml"))
	if string(data) != "editor change" {
		t.Fatal("editor data lost")
	}
	if _, err := os.Stat(filepath.Join(root, projectTransactionName)); err != nil {
		t.Fatal("recovery backup was removed")
	}
}
