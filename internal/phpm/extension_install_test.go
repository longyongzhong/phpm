package phpm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtensionArchiveSelectionMatchesWholeBuildProfile(t *testing.T) {
	rt := Runtime{Platform: "windows-amd64", PHP: PHPInfo{Version: "8.4.27", IntSize: 8, Build: "API20240924,NTS,VS17"}}
	pages := map[string]string{
		windowsExtensionBase + "redis/":        `<a href="6.9.0/">old</a><a href="6.10.0/">new</a><a href="7.0.0RC1/">rc</a><a href="../evil/">escape</a>`,
		windowsExtensionBase + "redis/6.10.0/": `<a href="php_redis-6.10.0-8.4-ts-vs17-x64.zip">TS</a><a href="php_redis-6.10.0-8.4-nts-vs16-x64.zip">wrong compiler</a><a href="php_redis-6.10.0-8.4-nts-vs17-x86.zip">x86</a>`,
		windowsExtensionBase + "redis/6.9.0/":  `<a href="php_redis-6.9.0-8.4-nts-vs17-x64.zip">match</a>`,
	}
	provider := windowsExtensionProvider{Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		body, exists := pages[req.URL.String()]
		if !exists {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}}
	version, raw, err := provider.resolveArchive(context.Background(), rt, ExtensionRequest{Name: "redis"})
	if err != nil || version != "6.9.0" || !strings.HasSuffix(raw, "php_redis-6.9.0-8.4-nts-vs17-x64.zip") {
		t.Fatalf("version=%q url=%q err=%v", version, raw, err)
	}
	if _, _, err := provider.resolveArchive(context.Background(), rt, ExtensionRequest{Name: "redis", Version: "6.10.0"}); err == nil {
		t.Fatal("exact version silently downgraded to a matching older release")
	}
	rt.PHP.ZTS = true
	version, _, err = provider.resolveArchive(context.Background(), rt, ExtensionRequest{Name: "redis"})
	if err != nil || version != "6.10.0" {
		t.Fatalf("TS build not selected: %s %v", version, err)
	}
	rt.PHP.Debug = true
	if _, _, err := provider.resolveArchive(context.Background(), rt, ExtensionRequest{Name: "redis"}); err == nil {
		t.Fatal("debug runtime selected a release archive")
	}
}

func TestExtensionDownloadPinsObservedContentAndRejectsTampering(t *testing.T) {
	s := testStore(t)
	requests := 0
	provider := windowsExtensionProvider{Store: s, Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("archive bytes")), Request: req}, nil
	})}}
	raw := windowsExtensionBase + "redis/6.2.0/php_redis-6.2.0-8.4-nts-vs17-x64.zip"
	path, checksum, err := provider.downloadArchive(context.Background(), raw, "")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("archive bytes"))
	if checksum != hex.EncodeToString(sum[:]) {
		t.Fatal("observed archive digest not recorded")
	}
	if _, _, err := provider.downloadArchive(context.Background(), raw, checksum); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("archive cache was not reused: %d requests", requests)
	}
	if _, _, err := provider.downloadArchive(context.Background(), raw, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong checksum pin accepted for cached archive")
	}
	if err := os.WriteFile(path, []byte("modified cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.downloadArchive(context.Background(), raw, ""); err == nil {
		t.Fatal("modified extension archive accepted")
	}
	if _, _, err := provider.downloadArchive(context.Background(), raw+"?fresh", strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong checksum pin accepted on first download")
	}
}

func TestExtensionDownloadHTTPFailureDoesNotCommitCache(t *testing.T) {
	s := testStore(t)
	provider := windowsExtensionProvider{Store: s, Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Request: req}, nil
	})}}
	if _, _, err := provider.downloadArchive(context.Background(), windowsExtensionBase+"redis/6.2.0/package.zip", ""); err == nil {
		t.Fatal("HTTP failure reported success")
	}
	entries, err := os.ReadDir(filepath.Join(s.Home, "cache"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed download committed cache: %v %v", entries, err)
	}
}

func TestExtensionInstallFailurePreservesFilesAndPinnedRuntime(t *testing.T) {
	s := testStore(t)
	rt := fakeRuntime(t, s, "pinned", "8.4.9")
	fakeRuntime(t, s, "newer", "8.4.10")
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
	wantErr := errors.New("download failed")
	_, _, err := s.installProjectExtension(root, ExtensionRequest{Name: "redis"}, func(ctx context.Context, selected Runtime, request ExtensionRequest) (acquiredExtension, error) {
		if selected.ID != rt.ID {
			t.Fatalf("extension install unexpectedly upgraded PHP to %s", selected.ID)
		}
		return acquiredExtension{}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected result: %v", err)
	}
	configAfter, _ := os.ReadFile(filepath.Join(root, "phpm.yaml"))
	lockAfter, _ := os.ReadFile(filepath.Join(root, "phpm.lock"))
	if !bytes.Equal(configBefore, configAfter) || !bytes.Equal(lockBefore, lockAfter) {
		t.Fatal("failed download changed project files")
	}
}

func TestYAMLExtensionUpdatePreservesCommentsAndOtherSettings(t *testing.T) {
	before := []byte("# project comment\nschema: 1\nphp: '8.4'\nextensions:\n  redis: '6.1.0' # retain this comment\n  mongodb: '2.1.0'\nini:\n  memory_limit: '512M'\n")
	after, err := setExtensionInYAML(before, "redis", "6.2.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"# project comment", "# retain this comment", "mongodb: '2.1.0'", "memory_limit: '512M'", `redis: "6.2.0"`} {
		if !bytes.Contains(after, []byte(expected)) {
			t.Fatalf("lost %q:\n%s", expected, after)
		}
	}
	again, err := setExtensionInYAML(after, "redis", "6.2.0")
	if err != nil || !bytes.Equal(after, again) {
		t.Fatal("reinstall reformatted an unchanged manifest")
	}
}
