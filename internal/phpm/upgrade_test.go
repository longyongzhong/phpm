package phpm

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPHPUpgradeVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		A, B string
		Want int
	}{{"8.4.9", "8.4.10", -1}, {"8.5.0", "8.4.99", 1}, {"8.4.27", "8.4.27", 0}} {
		got, err := comparePHPVersions(tc.A, tc.B)
		if err != nil || got != tc.Want {
			t.Fatalf("%s vs %s: %d %v", tc.A, tc.B, got, err)
		}
	}
	if _, err := comparePHPVersions("8.4", "8.4.1"); err == nil {
		t.Fatal("incomplete version accepted")
	}
}

func TestUpgradeYAMLPreservesSettingsAndComments(t *testing.T) {
	before := []byte("# original comment\nschema: 1\nphp: '8.4.9'\nruntime: old-build\nextensions:\n  redis: '6.2.0' # keep redis version\n  curl: '8.4.9'\nini:\n  memory_limit: '512M'\n")
	cfg, err := decodeConfig(before)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PHP, cfg.Runtime, cfg.Extensions["curl"] = "8.4.10", "new-build", "8.4.10"
	after, err := upgradeConfigYAML(before, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"# original comment", "# keep redis version", "redis: '6.2.0'", "memory_limit: '512M'"} {
		if !strings.Contains(string(after), part) {
			t.Fatalf("lost %q:\n%s", part, after)
		}
	}
}

func TestUpgradePlanIdentityDetectsChangedSnapshots(t *testing.T) {
	p := UpgradePlan{Project: t.TempDir(), Platform: platformKey(), Environment: LockedEnvironment{ID: "env-test"}, BeforeConfig: fileSnapshot{Exists: true, Data: []byte("original")}}
	p.ID = upgradePlanID(p)
	if p.ID != upgradePlanID(p) {
		t.Fatal("unstable plan identity")
	}
	p.BeforeConfig.Data = []byte("edited")
	if p.ID == upgradePlanID(p) {
		t.Fatal("snapshot edit did not invalidate the plan")
	}
}

// Invoked from the existing opt-in native fixture, avoiding host installations
// or network dependencies. Different payloads model same-version package revisions.
func testNativeUpgradeLifecycle(t *testing.T, s *Store, base Runtime, fixture, binary string) {
	t.Helper()
	targetFixture := t.TempDir()
	if err := copyTree(fixture, filepath.Join(targetFixture, "php")); err != nil {
		t.Fatal(err)
	}
	targetFixture = filepath.Join(targetFixture, "php")
	if err := os.WriteFile(filepath.Join(targetFixture, "package-revision.txt"), []byte("new package revision"), 0600); err != nil {
		t.Fatal(err)
	}
	target, err := s.importRuntime(targetFixture, binary, Source{Kind: "local-import"})
	if err != nil {
		t.Fatal(err)
	}
	if target.ID == base.ID {
		t.Fatal("target runtime build did not change")
	}
	root := t.TempDir()
	cfg := Config{Schema: 1, PHP: base.PHP.Version, Runtime: base.ID, Extensions: map[string]string{"json": base.PHP.Extensions["json"]}, INI: map[string]string{"memory_limit": "192M"}}
	available, err := s.extensions()
	if err != nil {
		t.Fatal(err)
	}
	var redis *Extension
	for i := range available {
		if available[i].RuntimeID == base.ID && available[i].Name == "redis" {
			redis = &available[i]
			cfg.Extensions["redis"] = redis.Version
			break
		}
	}
	writeTestConfig(t, root, cfg)
	old, err := s.install(root, false, false)
	if err != nil {
		t.Fatal(err)
	}
	originalConfig, err := snapshotFile(filepath.Join(root, "phpm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	originalLock, err := snapshotFile(filepath.Join(root, "phpm.lock"))
	if err != nil {
		t.Fatal(err)
	}
	rtAcquire := func(ctx context.Context, previous Runtime, request runtimeUpgradeRequest) (Runtime, error) {
		return target, nil
	}
	provider := windowsExtensionProvider{Store: s}
	extAcquire := func(ctx context.Context, rt Runtime, req ExtensionRequest) (acquiredExtension, error) {
		if req.Name != "redis" {
			return provider.acquire(ctx, rt, req)
		}
		if redis == nil || req.Version != redis.Version {
			t.Fatal("upgrade changed third-party extension version")
		}
		ext, err := s.importExtension(rt, "redis", redis.Version, s.modulePath(*redis), "", "", redis.Zend)
		return acquiredExtension{Version: redis.Version, Artifact: &ext}, err
	}
	plan, err := s.prepareUpgrade(root, runtimeUpgradeRequest{RuntimeID: target.ID}, rtAcquire, extAcquire)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireProjectSnapshots(root, originalConfig, originalLock); err != nil {
		t.Fatal("preparation changed active project files:", err)
	}
	for _, kind := range []string{"same-build", "downgrade", "thread-mode", "requested-build", "unrequested-branch"} {
		badTarget := target
		badRequest := runtimeUpgradeRequest{}
		switch kind {
		case "same-build":
			badTarget = base
		case "downgrade":
			badTarget.PHP.Version = "1.0.0"
		case "thread-mode":
			badTarget.PHP.ZTS = !base.PHP.ZTS
		case "requested-build":
			badRequest.RuntimeID = base.ID
		case "unrequested-branch":
			badTarget.PHP.Version = "99.0.0"
		}
		if _, err := s.prepareUpgrade(root, badRequest, func(context.Context, Runtime, runtimeUpgradeRequest) (Runtime, error) { return badTarget, nil }, extAcquire); err == nil {
			t.Fatalf("invalid %s upgrade accepted", kind)
		}
		if err := requireProjectSnapshots(root, originalConfig, originalLock); err != nil {
			t.Fatal(err)
		}
	}
	current, err := s.current(root)
	if err != nil || current.ID != old.ID {
		t.Fatalf("preparation switched the active environment: %v", err)
	}
	if _, err := s.activateUpgrade(root, "wrong-candidate"); err == nil {
		t.Fatal("activation accepted the wrong reviewed ID")
	}
	if err := requireProjectSnapshots(root, originalConfig, originalLock); err != nil {
		t.Fatal(err)
	}
	if _, err := s.candidate(root); err != nil {
		t.Fatal(err)
	}
	// Failure during a later preparation must retain the usable candidate.
	wantErr := errors.New("runtime download failed")
	_, err = s.prepareUpgrade(root, runtimeUpgradeRequest{}, func(context.Context, Runtime, runtimeUpgradeRequest) (Runtime, error) { return Runtime{}, wantErr }, extAcquire)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected acquisition failure: %v", err)
	}
	retained, err := s.candidate(root)
	if err != nil || retained.ID != plan.ID {
		t.Fatal("failed preparation replaced the previous candidate")
	}
	_, err = s.prepareUpgrade(root, runtimeUpgradeRequest{RuntimeID: target.ID}, rtAcquire, func(context.Context, Runtime, ExtensionRequest) (acquiredExtension, error) {
		return acquiredExtension{}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected extension acquisition failure: %v", err)
	}
	if err := requireProjectSnapshots(root, originalConfig, originalLock); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.runtimePath(target.ID), "payload", "package-revision.txt")
	if err := os.WriteFile(marker, []byte("corrupted candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.activateUpgrade(root, plan.ID); err == nil {
		t.Fatal("activation accepted a corrupted target runtime")
	}
	if _, err := s.current(root); err != nil {
		t.Fatal("candidate corruption affected current PHP:", err)
	}
	if err := os.WriteFile(marker, []byte("new package revision"), 0600); err != nil {
		t.Fatal(err)
	}
	staleEdit := append(append([]byte{}, originalConfig.Data...), []byte("\n# edited after preparation\n")...)
	if err := os.WriteFile(filepath.Join(root, "phpm.yaml"), staleEdit, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.activateUpgrade(root, plan.ID); err == nil {
		t.Fatal("activation overwrote a project edit after preparation")
	}
	if err := atomicWrite(filepath.Join(root, "phpm.yaml"), originalConfig.Data, originalConfig.Mode); err != nil {
		t.Fatal(err)
	}
	// A process launched on the old PHP continues using it after activation.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, s.runtimeBinary(base), "-r", `echo "ready\n";fgets(STDIN);echo PHP_BINARY;`)
	process.Env = s.environmentVars(old, s.environmentPath(old))
	input, err := process.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(output)
	if ready, err := reader.ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatalf("old process did not start: %q %v", ready, err)
	}
	if _, err := s.activateUpgrade(root, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	input.Close()
	path, err := reader.ReadString('\n')
	if err != nil && path == "" {
		t.Fatal(err)
	}
	if filepath.Clean(path) != s.runtimeBinary(base) {
		t.Fatalf("running process unexpectedly changed PHP: %q", path)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	current, err = s.current(root)
	if err != nil || current.ID != plan.Environment.ID {
		t.Fatalf("activation did not select candidate: %v", err)
	}
	history, err := s.upgradeHistory(root)
	if err != nil || len(history) != 1 {
		t.Fatalf("missing restore point: %v", err)
	}
	// A newer user edit must never be overwritten by rollback.
	edited := append(append([]byte{}, plan.AfterConfig...), []byte("\n# later editor change\n")...)
	if err := os.WriteFile(filepath.Join(root, "phpm.yaml"), edited, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rollbackUpgrade(root, plan.ID); err == nil {
		t.Fatal("rollback overwrote a later edit")
	}
	if _, err := s.candidate(root); err == nil {
		t.Fatal("stale candidate accepted after project edit")
	}
	if err := atomicWrite(filepath.Join(root, "phpm.yaml"), plan.AfterConfig, originalConfig.Mode); err != nil {
		t.Fatal(err)
	}
	restored, err := s.rollbackUpgrade(root, "")
	if err != nil || restored.ID != old.ID {
		t.Fatalf("rollback failed: %v", err)
	}
	if err := requireProjectSnapshots(root, originalConfig, originalLock); err != nil {
		t.Fatal("rollback did not restore exact files:", err)
	}
	if _, err := s.current(root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.candidate(root); err != nil {
		t.Fatal("rollback lost the reusable candidate:", err)
	}
}
