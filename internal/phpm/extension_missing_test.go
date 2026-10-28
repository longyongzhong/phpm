package phpm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingExtensionPlanRefusesUnrelatedChanges(t *testing.T) {
	report := ComposerCheckReport{Scope: "root", Results: []ComposerPlatformResult{
		{Name: "ext-redis", Status: "missing"},
		{Name: "ext-mbstring", Status: "success", Provider: "test/polyfill"},
		{Name: "ext-zend-opcache", Status: "missing"},
	}}
	requests, packages, err := missingExtensionRequests(report)
	if err != nil || len(requests) != 2 || requests[0].Name != "opcache" || requests[1].Name != "redis" || packages["opcache"] != "ext-zend-opcache" {
		t.Fatalf("wrong plan: %+v %+v %v", requests, packages, err)
	}
	for _, row := range []ComposerPlatformResult{
		{Name: "php", Status: "failed", Version: "8.2.9"},
		{Name: "lib-curl", Status: "missing"},
		{Name: "ext-redis", Status: "failed", Version: "6.1.0"},
		{Name: "ext-unsupported-name", Status: "missing"},
	} {
		report.Results = []ComposerPlatformResult{row}
		if _, _, err := missingExtensionRequests(report); err == nil {
			t.Fatalf("accepted %+v", row)
		}
	}
	for _, fresh := range []*bool{nil, new(bool)} {
		if _, _, err := missingExtensionRequests(ComposerCheckReport{Scope: "root+lock", LockFresh: fresh}); err == nil {
			t.Fatal("unverified/stale lock accepted")
		}
	}
}

func TestExtensionArchiveFiltersBeforeFetchingVersionDirectories(t *testing.T) {
	rt := Runtime{Platform: "windows-amd64", PHP: PHPInfo{Version: "8.4.27", IntSize: 8, Build: "API20240924,NTS,VS17"}}
	provider := windowsExtensionProvider{FilterVersions: func(name string, versions []string) ([]string, error) {
		if name != "redis" || strings.Join(versions, ",") != "7.0.0,6.2.0,6.1.0" {
			t.Fatalf("wrong candidates: %s %+v", name, versions)
		}
		return []string{"6.1.0"}, nil
	}, Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.String() {
		case windowsExtensionBase + "redis/":
			body = `<a href="7.0.0/">7</a><a href="6.2.0/">6.2</a><a href="6.1.0/">6.1</a>`
		case windowsExtensionBase + "redis/6.1.0/":
			body = `<a href="php_redis-6.1.0-8.4-nts-vs17-x64.zip">match</a>`
		default:
			t.Fatalf("queried rejected version: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}}
	version, _, err := provider.resolveArchive(context.Background(), rt, ExtensionRequest{Name: "redis"})
	if err != nil || version != "6.1.0" {
		t.Fatalf("selected %s: %v", version, err)
	}
	provider.FilterVersions = func(string, []string) ([]string, error) { return nil, nil }
	if _, _, err := provider.resolveArchive(context.Background(), rt, ExtensionRequest{Name: "redis"}); err == nil {
		t.Fatal("empty constraint intersection accepted")
	}
	if err := provider.checkVersion("curl", "8.4.27"); err == nil {
		t.Fatal("bundled version bypassed constraints")
	}
}

func TestExtensionBatchFailureAndStaleBaseKeepProjectFiles(t *testing.T) {
	s := testStore(t)
	rt := fakeRuntime(t, s, "pinned-batch", "8.4.9")
	root := t.TempDir()
	cfg := Config{Schema: 1, PHP: "8.4"}
	writeTestConfig(t, root, cfg)
	previous := LockedEnvironment{ConfigHash: digestJSON(cfg), Runtime: rt}
	previous.ID = environmentID(previous)
	if err := writeJSON(filepath.Join(root, "phpm.lock"), Lockfile{Schema: 1, Targets: map[string]LockedEnvironment{platformKey(): previous}}); err != nil {
		t.Fatal(err)
	}
	configBefore, _ := os.ReadFile(filepath.Join(root, "phpm.yaml"))
	lockBefore, _ := os.ReadFile(filepath.Join(root, "phpm.lock"))
	calls := 0
	wantErr := errors.New("second download failed")
	acquire := func(_ context.Context, selected Runtime, req ExtensionRequest) (acquiredExtension, error) {
		calls++
		if selected.ID != rt.ID {
			t.Fatal("batch changed PHP")
		}
		if req.Name == "core" {
			return acquiredExtension{Version: rt.PHP.Version}, nil
		}
		return acquiredExtension{}, wantErr
	}
	_, _, err := s.installProjectExtensions(root, []ExtensionRequest{{Name: "core"}, {Name: "redis"}}, acquire, &previous, nil)
	if !errors.Is(err, wantErr) || calls != 2 {
		t.Fatalf("batch did not fail after first acquisition: %v (%d)", err, calls)
	}
	stale := previous
	stale.ID = "different"
	if _, _, err := s.installProjectExtensions(root, []ExtensionRequest{{Name: "core"}}, acquire, &stale, nil); err == nil || calls != 2 {
		t.Fatal("stale inspected environment accepted")
	}
	for path, before := range map[string][]byte{filepath.Join(root, "phpm.yaml"): configBefore, filepath.Join(root, "phpm.lock"): lockBefore} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("failed batch modified %s", path)
		}
	}
}

func TestMissingCLIRejectsAmbiguousOptions(t *testing.T) {
	c := CLI{}
	for _, args := range [][]string{{"install", "--missing", "redis"}, {"install", "--missing", "--zend"}, {"install", "--missing", "--sha256", "hash"}, {"install", "redis", "--no-dev"}, {"install", "redis", "--lock"}} {
		if err := c.ext(args); err == nil {
			t.Fatalf("accepted %+v", args)
		}
	}
}
