package phpm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"phpm/internal/catalog"
)

// This test uses real accepted stage-A packages. The second revision is an
// explicitly synthetic payload fixture, not an upstream PHP version upgrade.
func TestNativeSignedCatalogLifecycle(t *testing.T) {
	packages := os.Getenv("PHPM_TEST_DEBIAN_PACKAGES")
	if packages == "" {
		t.Skip("set PHPM_TEST_DEBIAN_PACKAGES to verify real signed Debian distribution")
	}
	if err := checkDebianHost(); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Fatal("distribution fixture must run without root privileges")
	}
	for _, tool := range []string{"php", "cc", "make"} {
		if path, err := exec.LookPath(tool); err == nil {
			t.Fatalf("unexpected host %s: %s", tool, path)
		}
	}
	cliPath, err := filepath.Abs(os.Getenv("PHPM_TEST_CLI_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PHPM_TEST_CLI_BINARY") == "" || os.Getenv("PHPM_TEST_COMPOSER") == "" {
		t.Fatal("native distribution fixture requires CLI and Composer PHAR")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	if err := catalog.CreateBundle([]string{packages}, bundle, key.Seed(), 1, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	var original catalog.Catalog
	if err := readJSON(filepath.Join(bundle, "catalog.json"), &original); err != nil {
		t.Fatal(err)
	}
	checks := []string{"verified-stage-a-inputs", "offline-ed25519-signing"}
	// Preserve the original, non-fixture release for the workflow artifact.
	if destination := os.Getenv("PHPM_TEST_DISTRIBUTION_OUTPUT"); destination != "" {
		if err := copyTree(bundle, filepath.Join(destination, "bundle")); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewTLSServer(http.FileServer(http.Dir(bundle)))
	defer server.Close()
	certFile := filepath.Join(root, "fixture-ca.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	env := cleanEnv(map[string]string{"PHPM_HOME": filepath.Join(root, "home"), "PHPM_COMPOSER": os.Getenv("PHPM_TEST_COMPOSER"), "SSL_CERT_FILE": certFile, "SSL_CERT_DIR": root, "COMPOSER_HOME": filepath.Join(root, "composer-home")})
	command := func(success bool, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, cliPath, args...)
		cmd.Env = env
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if success && err != nil {
			t.Fatalf("phpm %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		if !success && err == nil {
			t.Fatalf("unexpected success: phpm %s\n%s", strings.Join(args, " "), out)
		}
		return string(out)
	}
	command(true, "source", "add", "verified", "--catalog", server.URL+"/catalog.json", "--key", filepath.Join(bundle, "signing-key.pub"))
	command(false, "source", "add", "verified", "--catalog", server.URL+"/catalog.json", "--key", filepath.Join(bundle, "signing-key.pub"))
	command(true, "source", "list")
	command(true, "php", "available", "--source", "verified")
	command(true, "php", "install", original.Builds[0].Version, "--source", "verified")
	command(true, "init", "--php", "8.4")
	command(true, "install")
	composer := `{"name":"phpm/distribution-test","version":"1.0.0","require":{"php":"^8.4","ext-redis":"^6.3","ext-curl":"*"}}`
	if err := os.WriteFile(filepath.Join(project, "composer.json"), []byte(composer), 0600); err != nil {
		t.Fatal(err)
	}
	command(true, "ext", "install", "--missing")
	command(true, "ext", "check")
	command(true, "install", "--frozen")
	command(true, "verify")
	if got := strings.TrimSpace(command(true, "exec", "--", "php", "-r", "echo phpversion('redis');")); got != "6.3.0" {
		t.Fatalf("Redis not actually loaded: %s", got)
	}
	checks = append(checks, "https-catalog-and-package-download", "explicit-public-key-pin", "php-install-from-source", "bundled-and-signed-redis-via-composer-missing", "frozen-install-and-real-loading")
	var current LockedEnvironment
	if err := json.Unmarshal([]byte(command(true, "info")), &current); err != nil {
		t.Fatal(err)
	}
	if current.Runtime.Source.Kind != "signed-catalog" || current.Runtime.Source.Build != original.Builds[0].ID {
		t.Fatal("signed runtime binding was lost")
	}
	before := map[string][]byte{}
	for _, name := range []string{"phpm.yaml", "phpm.lock"} {
		data, err := os.ReadFile(filepath.Join(project, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	unchanged := func() {
		t.Helper()
		for name, data := range before {
			now, err := os.ReadFile(filepath.Join(project, name))
			if err != nil || string(now) != string(data) {
				t.Fatalf("active %s changed", name)
			}
		}
	}
	// Tampered metadata and cached packages must fail before a project commit.
	data, err := os.ReadFile(filepath.Join(bundle, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "catalog.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if out := command(false, "ext", "install", "redis"); !strings.Contains(out, "signature verification failed") {
		t.Fatalf("unexpected signature failure: %s", out)
	}
	unchanged()
	if err := os.WriteFile(filepath.Join(bundle, "catalog.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	redis := original.Builds[0].Extensions[0].Package
	cache := filepath.Join(root, "home", "cache", redis.SHA256+".tar.gz")
	if err := os.WriteFile(cache, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := command(false, "ext", "install", "redis"); !strings.Contains(out, "cached catalog package hash/size mismatch") {
		t.Fatalf("unexpected cache failure: %s", out)
	}
	unchanged()
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	command(true, "ext", "install", "redis")
	// An idempotent reinstall may serialize unchanged files; capture its exact pair.
	for name := range before {
		before[name], err = os.ReadFile(filepath.Join(project, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	checks = append(checks, "tampered-signature-rejected-with-project-intact", "corrupt-cache-rejected-with-project-intact")
	restoredHome := filepath.Join(root, "restored-home")
	command(true, "--home", restoredHome, "source", "add", "verified", "--catalog", server.URL+"/catalog.json", "--key", filepath.Join(bundle, "signing-key.pub"))
	command(true, "--home", restoredHome, "install", "--frozen", "--source", "verified")
	command(true, "--home", restoredHome, "verify")
	var restored LockedEnvironment
	if err := json.Unmarshal([]byte(command(true, "--home", restoredHome, "info")), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ID != current.ID {
		t.Fatal("fresh-home hydration changed locked identity")
	}
	unchanged()
	checks = append(checks, "exact-lock-restored-into-empty-home-without-project-writes")
	// Add a marker to the actual runtime package. No build verification reports
	// are fabricated: only this test's signed catalog contains the fixture entry.
	updated := original
	updated.Builds = append([]catalog.Build(nil), original.Builds...)
	updated.Sequence = 2
	alt := original.Builds[0]
	alt.ID += "-fixture2"
	alt.Revision++
	payload := filepath.Join(root, "alternate-payload")
	os.Mkdir(payload, 0700)
	if err := extractTarGZIP(filepath.Join(bundle, alt.Runtime.Filename), payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "test-revision.txt"), []byte("synthetic distribution lifecycle fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	alt.Runtime.Filename = "fixture2-runtime.tar.gz"
	packNativeCatalogFixture(t, payload, filepath.Join(bundle, alt.Runtime.Filename))
	alt.Runtime.SHA256, err = fileSHA(filepath.Join(bundle, alt.Runtime.Filename))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(bundle, alt.Runtime.Filename))
	if err != nil {
		t.Fatal(err)
	}
	alt.Runtime.Size = info.Size()
	alt.SDK.Filename = "fixture2-sdk.tar.gz"
	if err := copyFile(filepath.Join(bundle, original.Builds[0].SDK.Filename), filepath.Join(bundle, alt.SDK.Filename), 0600); err != nil {
		t.Fatal(err)
	}
	alt.Extensions = append([]catalog.Extension(nil), alt.Extensions...)
	alt.Extensions[0].Package.Filename = "fixture2-redis.tar.gz"
	if err := copyFile(filepath.Join(bundle, redis.Filename), filepath.Join(bundle, alt.Extensions[0].Package.Filename), 0600); err != nil {
		t.Fatal(err)
	}
	updated.Builds = append(updated.Builds, alt)
	if err := updated.Validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	writeSignedFixture(t, bundle, updated, key)
	command(true, "php", "upgrade") // inherits the current signed source
	command(true, "verify", "--candidate")
	command(true, "ext", "check", "--candidate")
	unchanged()
	var candidate struct {
		ID          string            `json:"candidate"`
		Environment LockedEnvironment `json:"environment"`
	}
	if err := json.Unmarshal([]byte(command(true, "info", "--candidate")), &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.Environment.Runtime.Source.Build != alt.ID {
		t.Fatal("upgrade changed provider or selected wrong revision")
	}
	command(true, "activate", candidate.ID)
	command(true, "verify")
	command(true, "rollback")
	command(true, "verify")
	unchanged()
	checks = append(checks, "source-inherited-by-upgrade", "signed-extension-rebound-to-candidate", "candidate-keeps-current-files", "same-version-fixture-activation-and-exact-rollback")
	writeSignedFixture(t, bundle, original, key)
	if out := command(false, "php", "available", "--source", "verified"); !strings.Contains(out, "catalog rollback") {
		t.Fatalf("unexpected replay failure: %s", out)
	}
	unchanged()
	updated.Sequence = 3
	updated.ExpiresAt = time.Now().Add(-time.Minute)
	writeSignedFixture(t, bundle, updated, key)
	if out := command(false, "php", "available", "--source", "verified"); !strings.Contains(out, "expired") {
		t.Fatalf("unexpected expiry failure: %s", out)
	}
	unchanged()
	checks = append(checks, "signed-catalog-replay-rejected", "expired-catalog-rejected")
	if destination := os.Getenv("PHPM_TEST_DISTRIBUTION_OUTPUT"); destination != "" {
		if err := writeJSON(filepath.Join(destination, "distribution-verification.json"), map[string]any{"schema": 1, "build_id": original.Builds[0].ID, "checks": checks, "key_fingerprint": catalog.Fingerprint(key.Public().(ed25519.PublicKey)), "signing_mode": "ephemeral-test-key", "limitations": []string{"test signing key is not a production trust root", "same PHP version payload revision fixture", "container shares host kernel", "no production service switch"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func packNativeCatalogFixture(t *testing.T, root, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, src)
		return errors.Join(copyErr, src.Close())
	})
	if err := errors.Join(err, tw.Close(), gz.Close(), f.Close()); err != nil {
		t.Fatal(err)
	}
}
