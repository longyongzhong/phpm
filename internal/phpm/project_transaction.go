package phpm

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const projectTransactionName = ".phpm-transaction.json"

type fileSnapshot struct {
	Exists bool        `json:"exists"`
	Data   []byte      `json:"data"`
	Mode   os.FileMode `json:"mode"`
}

type projectTransaction struct {
	Schema       int          `json:"schema"`
	BeforeConfig fileSnapshot `json:"before_config"`
	BeforeLock   fileSnapshot `json:"before_lock"`
	AfterConfig  []byte       `json:"after_config"`
	AfterLock    []byte       `json:"after_lock"`
}

func snapshotFile(path string) (fileSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{Mode: 0600}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("project file must be a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	return fileSnapshot{Exists: true, Data: data, Mode: info.Mode().Perm()}, err
}

func sameSnapshot(a, b fileSnapshot) bool {
	return a.Exists == b.Exists && a.Mode == b.Mode && bytes.Equal(a.Data, b.Data)
}

func checkProjectTransaction(root string) error {
	_, err := os.Lstat(filepath.Join(root, projectTransactionName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unfinished project environment update; run phpm install to recover the previous project files")
}

func commitProjectFiles(root string, config, lock fileSnapshot, afterConfig, afterLock []byte) error {
	return commitProjectFilesWithWriter(root, config, lock, afterConfig, afterLock, atomicWrite)
}

func commitProjectFilesWithWriter(root string, config, lock fileSnapshot, afterConfig, afterLock []byte, write func(string, []byte, os.FileMode) error) error {
	configPath, lockPath := filepath.Join(root, "phpm.yaml"), filepath.Join(root, "phpm.lock")
	currentConfig, err := snapshotFile(configPath)
	if err != nil {
		return err
	}
	currentLock, err := snapshotFile(lockPath)
	if err != nil {
		return err
	}
	if !sameSnapshot(config, currentConfig) || !sameSnapshot(lock, currentLock) {
		return errors.New("project files changed during environment preparation; no project files were updated")
	}
	if bytes.Equal(config.Data, afterConfig) && lock.Exists && bytes.Equal(lock.Data, afterLock) {
		return nil
	}
	journal := projectTransaction{Schema: 1, BeforeConfig: config, BeforeLock: lock, AfterConfig: afterConfig, AfterLock: afterLock}
	path := filepath.Join(root, projectTransactionName)
	if err := checkProjectTransaction(root); err != nil {
		return err
	}
	if err := writeJSON(path, journal); err != nil {
		return err
	}
	commitErr := write(configPath, afterConfig, config.Mode)
	if commitErr == nil {
		commitErr = write(lockPath, afterLock, lock.Mode)
	}
	if commitErr != nil {
		if recoveryErr := recoverProjectTransaction(root); recoveryErr != nil {
			return fmt.Errorf("project update failed: %v; recovery requires attention: %w", commitErr, recoveryErr)
		}
		return fmt.Errorf("project update failed; previous files restored: %w", commitErr)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("project files committed but transaction cleanup failed; run phpm install before executing: %w", err)
	}
	return nil
}

// Called only while holding the project writer lock. If either file was edited
// outside PHPM, keep the journal and stop instead of overwriting that edit.
func recoverProjectTransaction(root string) error {
	path := filepath.Join(root, projectTransactionName)
	var journal projectTransaction
	if err := readJSON(path, &journal); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if journal.Schema != 1 || !journal.BeforeConfig.Exists || len(journal.BeforeConfig.Data) == 0 || len(journal.AfterConfig) == 0 || len(journal.AfterLock) == 0 {
		return errors.New("invalid project transaction; preserve the journal and restore project files manually")
	}
	files := []struct {
		Name   string
		Before fileSnapshot
		After  []byte
	}{{"phpm.yaml", journal.BeforeConfig, journal.AfterConfig}, {"phpm.lock", journal.BeforeLock, journal.AfterLock}}
	for _, file := range files {
		current, err := snapshotFile(filepath.Join(root, file.Name))
		if err != nil {
			return err
		}
		isBefore := current.Exists == file.Before.Exists && bytes.Equal(current.Data, file.Before.Data)
		isAfter := current.Exists && bytes.Equal(current.Data, file.After)
		if !isBefore && !isAfter {
			return fmt.Errorf("%s changed after the interrupted install; refusing to overwrite it (backup: %s)", file.Name, path)
		}
	}
	for _, file := range files {
		target := filepath.Join(root, file.Name)
		if file.Before.Exists {
			if err := atomicWrite(target, file.Before.Data, file.Before.Mode.Perm()); err != nil {
				return err
			}
		} else if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Remove(path)
}
