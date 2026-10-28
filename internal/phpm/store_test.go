package phpm

import (
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fakeRuntime(t *testing.T, s *Store, id, version string) Runtime {
	t.Helper()
	payload := filepath.Join(s.runtimePath(id), "payload")
	if err := os.MkdirAll(payload, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "php"), []byte(id), 0700); err != nil {
		t.Fatal(err)
	}
	digest, err := treeSHA(payload)
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Schema: 1, ID: id, Platform: platformKey(), Binary: "php", Digest: digest, PHP: PHPInfo{Version: version, Extensions: map[string]string{"core": version}}}
	if err := writeJSON(filepath.Join(s.runtimePath(id), "manifest.json"), rt); err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestInstallLockExcludesConcurrentWriters(t *testing.T) {
	s := testStore(t)
	unlock, err := s.lock("same-project")
	if err != nil {
		t.Fatal(err)
	}
	if next, err := s.lock("same-project"); err == nil {
		next()
		t.Fatal("concurrent writer acquired lock")
	}
	other, err := s.lock("other-project")
	if err != nil {
		t.Fatal(err)
	}
	other()
	unlock()
	last, err := s.lock("same-project")
	if err != nil {
		t.Fatal(err)
	}
	last()
}

func TestRuntimeResolutionUsesNumericVersionsAndRejectsAmbiguity(t *testing.T) {
	s := testStore(t)
	fakeRuntime(t, s, "older", "8.4.9")
	newer := fakeRuntime(t, s, "newer", "8.4.10")
	cfg := Config{Schema: 1, PHP: "8.4"}
	rt, err := s.resolveRuntime(cfg)
	if err != nil || rt.ID != newer.ID {
		t.Fatalf("runtime=%+v err=%v", rt, err)
	}
	fakeRuntime(t, s, "alternate-build", "8.4.10")
	if _, err := s.resolveRuntime(cfg); err == nil {
		t.Fatal("ambiguous build silently chosen")
	}
	cfg.Runtime = "newer"
	if _, err := s.resolveRuntime(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestLockedRuntimeDoesNotFloatAndTamperingIsDetected(t *testing.T) {
	s := testStore(t)
	rt := fakeRuntime(t, s, "pinned", "8.4.9")
	fakeRuntime(t, s, "newer", "8.4.10")
	cfg := Config{Schema: 1, PHP: "8.4"}
	env := LockedEnvironment{ConfigHash: digestJSON(cfg), Runtime: rt}
	env.ID = environmentID(env)
	if err := s.validateLocked(env, cfg); err != nil {
		t.Fatal(err)
	}
	if env.Runtime.PHP.Version != "8.4.9" {
		t.Fatal("lock floated to a newer patch")
	}
	if err := os.WriteFile(filepath.Join(s.runtimePath(rt.ID), "payload", "php"), []byte("corrupted"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.validateLocked(env, cfg); err == nil {
		t.Fatal("modified runtime accepted")
	}
}

func TestLockedExtensionCannotEscapeStore(t *testing.T) {
	s := testStore(t)
	if _, err := s.readExtension("../../elsewhere"); err == nil {
		t.Fatal("extension ID escaped store")
	}
	if _, err := s.readRuntime("../../elsewhere"); err == nil {
		t.Fatal("runtime ID escaped store")
	}
}

func TestCopyRejectsRecursiveDestination(t *testing.T) {
	root := t.TempDir()
	if err := copyTree(root, filepath.Join(root, "copy")); err == nil {
		t.Fatal("recursive copy accepted")
	}
}

func TestEnvironmentHashSeparatesLoaderModesAndSettings(t *testing.T) {
	env := LockedEnvironment{ConfigHash: "config", Runtime: Runtime{ID: "runtime", Platform: platformKey(), Digest: "digest"}, Extensions: []Extension{{ID: "xdebug", Digest: "extension"}}}
	first := environmentID(env)
	env.Extensions[0].Zend = true
	if first == environmentID(env) {
		t.Fatal("loader mode missing from identity")
	}
	first = environmentID(env)
	env.INI = map[string]string{"memory_limit": "256M"}
	if first == environmentID(env) {
		t.Fatal("INI settings missing from identity")
	}
}
