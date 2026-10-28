package phpm

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testNativeMissingExtensions(t *testing.T, s *Store, rt Runtime) {
	t.Helper()
	module := os.Getenv("PHPM_TEST_REDIS_MODULE")
	if rt.Platform != "windows-amd64" || module == "" {
		t.Skip("missing-extension native fixture requires Windows x64 and PHPM_TEST_REDIS_MODULE")
	}
	version := probeRedisVersion(t, s.runtimeBinary(rt), module)
	if version != "6.1.0" {
		t.Skip("constraint fixture requires Redis 6.1.0")
	}
	root := t.TempDir()
	writeTestConfig(t, root, Config{Schema: 1, PHP: rt.PHP.Version, Runtime: rt.ID, INI: map[string]string{"memory_limit": "128M"}})
	initial, err := s.install(root, false, false)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "composer.json")
	t.Setenv("COMPOSER", file)
	data := []byte(`{"name":"test/missing","require":{"php":">=7.4","ext-redis":"^6.0 || ^7.0"},"require-dev":{"ext-zz_phpm_missing":"*"},"scripts":{"post-install-cmd":"@php danger.php"},"autoload":{"files":["danger.php"]}}`)
	writeManifest := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(data)
	if err := os.WriteFile(filepath.Join(root, "danger.php"), []byte(`<?php file_put_contents(__DIR__.'/executed','unexpected');`), 0600); err != nil {
		t.Fatal(err)
	}
	hash := md5.Sum(data)
	locked := map[string]any{"hash": hex.EncodeToString(hash[:]), "packages": []any{map[string]any{"name": "test/library", "version": "1.0.0", "require": map[string]string{"ext-redis": "~6.1.0 !=6.1.1"}}}, "packages-dev": []any{}}
	lockData, err := json.Marshal(locked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composerLockPath(file), lockData, 0600); err != nil {
		t.Fatal(err)
	}
	// Exercise Composer's AND/OR, tilde and exclusion rules using real classes.
	filtered, err := s.checkComposerVersions(root, initial, true, true, map[string][]string{"ext-redis": {"7.0.0", "6.2.0", "6.1.2", "6.1.1", "6.1.0"}})
	if err != nil || strings.Join(filtered.MatchingVersions["ext-redis"], ",") != "6.1.2,6.1.0" {
		t.Fatalf("constraint intersection: %+v %v", filtered.MatchingVersions, err)
	}
	dll, err := os.ReadFile(module)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	entry, err := w.Create("php_redis.dll")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(dll); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	profile, err := windowsExtensionProfile(rt)
	if err != nil {
		t.Fatal(err)
	}
	archiveName := "php_redis-6.1.0-" + profile + ".zip"
	requests, downloads := 0, 0
	changeManifest := false
	provider := windowsExtensionProvider{Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		var content []byte
		switch req.URL.String() {
		case windowsExtensionBase + "redis/":
			content = []byte(`<a href="7.0.0/">7</a><a href="6.2.0/">6.2</a><a href="6.1.0/">6.1</a>`)
		case windowsExtensionBase + "redis/6.1.0/":
			content = []byte(`<a href="` + archiveName + `">archive</a>`)
		case windowsExtensionBase + "redis/6.1.0/" + archiveName:
			downloads++
			if changeManifest {
				writeManifest(append(append([]byte{}, data...), '\n'))
			}
			content = archive.Bytes()
		case windowsExtensionBase + "zz_phpm_missing/":
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("fixture unavailable")), Request: req}, nil
		default:
			t.Fatalf("unexpected or constraint-rejected archive request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(content)), Request: req}, nil
	})}}
	beforeConfig, _ := os.ReadFile(filepath.Join(root, "phpm.yaml"))
	beforeLock, _ := os.ReadFile(filepath.Join(root, "phpm.lock"))
	assertOriginal := func() {
		t.Helper()
		for path, before := range map[string][]byte{filepath.Join(root, "phpm.yaml"): beforeConfig, filepath.Join(root, "phpm.lock"): beforeLock} {
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed batch changed %s", path)
			}
		}
		if current, err := s.current(root); err != nil || current.ID != initial.ID {
			t.Fatalf("original environment no longer usable: %v", err)
		}
	}
	// An editor change after version selection is detected by the final check.
	changeManifest = true
	if _, _, _, err := s.installMissingExtensions(root, true, true, provider); err == nil || !strings.Contains(err.Error(), "Composer inputs changed") {
		t.Fatalf("concurrent Composer edit accepted: %v", err)
	}
	assertOriginal()
	writeManifest(data)
	changeManifest = false
	// The first archive is now cached; failure of the second extension must
	// retain the entire old environment, rather than committing Redis alone.
	if _, _, _, err := s.installMissingExtensions(root, false, true, provider); err == nil || !strings.Contains(err.Error(), "zz_phpm_missing") {
		t.Fatalf("second extension failure lost: %v", err)
	}
	assertOriginal()
	if downloads != 1 {
		t.Fatalf("expected one acquisition/cache reuse, got %d", downloads)
	}
	installed, versions, _, err := s.installMissingExtensions(root, true, true, provider)
	if err != nil {
		t.Fatal(err)
	}
	if versions["redis"] != "6.1.0" || installed.Runtime.ID != initial.Runtime.ID || installed.INI["memory_limit"] != "128M" {
		t.Fatalf("wrong installed environment: %+v %+v", installed, versions)
	}
	if checked, err := s.checkComposer(root, installed, true, true); err != nil || composerCheckCode(checked) != 0 {
		t.Fatalf("installed environment fails Composer: %+v %v", checked, err)
	}
	for path, before := range map[string][]byte{file: data, composerLockPath(file): lockData} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("installer changed %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("installer executed project autoload/scripts")
	}
	beforeRequests := requests
	stableConfig, _ := os.ReadFile(filepath.Join(root, "phpm.yaml"))
	stableLock, _ := os.ReadFile(filepath.Join(root, "phpm.lock"))
	if _, versions, _, err := s.installMissingExtensions(root, true, true, provider); err != nil || len(versions) != 0 || requests != beforeRequests {
		t.Fatalf("satisfied project not a no-op: %+v %v", versions, err)
	}
	// A mismatched installed version and stale lock fail before any HTTP request.
	locked["hash"] = strings.Repeat("0", 32)
	stale, _ := json.Marshal(locked)
	if err := os.WriteFile(composerLockPath(file), stale, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.installMissingExtensions(root, true, true, provider); err == nil {
		t.Fatal("stale lock accepted")
	}
	if err := os.Remove(composerLockPath(file)); err != nil {
		t.Fatal(err)
	}
	writeManifest([]byte(`{"require":{"ext-redis":"^7.0"}}`))
	if _, _, _, err := s.installMissingExtensions(root, true, false, provider); err == nil || !strings.Contains(err.Error(), "resolve explicitly") {
		t.Fatalf("installed extension implicitly upgraded: %v", err)
	}
	if requests != beforeRequests {
		t.Fatal("preflight errors made HTTP requests")
	}
	for path, before := range map[string][]byte{filepath.Join(root, "phpm.yaml"): stableConfig, filepath.Join(root, "phpm.lock"): stableLock} {
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatalf("no-op/preflight changed %s", path)
		}
	}
}
