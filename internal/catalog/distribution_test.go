package catalog

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Unit-only bytes exercise release integrity. They are deliberately not stage-A
// receipts, executable clients, or evidence of native runtime acceptance.
func releaseFixture(t *testing.T) (string, ed25519.PublicKey, []byte, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	root, private, _ := ed25519.GenerateKey(rand.Reader)
	pub, publisher, _ := ed25519.GenerateKey(rand.Reader)
	_, raw, sig, err := NewTrust(private.Seed(), []ed25519.PublicKey{pub}, nil, 1, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("trust.json", raw)
	write("trust.json.sig", sig)
	write("root-key.pub", []byte(base64.StdEncoding.EncodeToString(root)))
	write("signing-key.pub", []byte(base64.StdEncoding.EncodeToString(pub)))
	c := fixture(now)
	packages := []*Package{&c.Builds[0].Runtime, &c.Builds[0].SDK, &c.Builds[0].Extensions[0].Package}
	for _, p := range packages {
		data := []byte("unit fixture: " + p.Filename)
		sum := sha256.Sum256(data)
		p.SHA256 = hex.EncodeToString(sum[:])
		p.Size = int64(len(data))
		write(p.Filename, data)
	}
	for _, suffix := range []string{"-build-result.json", "-sdk-verification.json", "-runtime-verification.json"} {
		write(c.Builds[0].ID+suffix, []byte(`{"unit_fixture":true}`))
	}
	raw, _ = json.Marshal(c)
	sig, err = Sign(raw, publisher.Seed(), RoleCatalog, 1)
	if err != nil {
		t.Fatal(err)
	}
	write("catalog.json", raw)
	write("catalog.json.sig", sig)
	return dir, root, publisher.Seed(), now
}

func TestClientReleaseIntegrityAndBootstrapInterop(t *testing.T) {
	bundle, root, seed, now := releaseFixture(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "unit-client")
	if err := os.WriteFile(binary, []byte("unit client bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "release")
	if err := CreateClients(bundle, out, root, seed, "0.1.0-test", strings.Repeat("a", 40), binary, binary, repo, filepath.Join(repo, "scripts/install-phpm.py"), now, time.Hour); err != nil {
		t.Fatal(err)
	}
	m, err := VerifyRelease(out, root, now)
	if err != nil || len(m.Clients) != 2 {
		t.Fatalf("verify: %+v %v", m, err)
	}
	t.Run("python-openssl-interop", func(t *testing.T) {
		if runtime.GOOS != "linux" && os.Getenv("PHPM_TEST_PYTHON") == "" {
			t.Skip("set PHPM_TEST_PYTHON and PHPM_TEST_OPENSSL for non-Linux interoperability checks")
		}
		python := os.Getenv("PHPM_TEST_PYTHON")
		if python == "" {
			python, _ = exec.LookPath("python3")
		}
		openssl := os.Getenv("PHPM_TEST_OPENSSL")
		if openssl == "" {
			openssl, _ = exec.LookPath("openssl")
		}
		if python == "" || openssl == "" {
			t.Skip("Python 3 and OpenSSL required for independent bootstrap verification")
		}
		for _, platform := range []string{"linux-amd64", "windows-amd64"} {
			dest := filepath.Join(t.TempDir(), "client")
			args := []string{filepath.Join(repo, "scripts/install-phpm.py"), "--source", out, "--root", filepath.Join(out, "root-key.pub"), "--fingerprint", Fingerprint(root), "--dest", dest, "--platform", platform, "--version", "0.1.0-test", "--openssl", openssl}
			if data, err := exec.Command(python, args...).CombinedOutput(); err != nil {
				t.Fatalf("%s bootstrap: %s %v", platform, data, err)
			}
			if _, err := os.Stat(filepath.Join(dest, "installation.json")); err != nil {
				t.Fatal(err)
			}
			if data, err := exec.Command(python, args...).CombinedOutput(); err == nil {
				t.Fatalf("overwrote installation: %s", data)
			}
			args[6] = strings.Repeat("0", 64) // fingerprint value
			args[8] = filepath.Join(t.TempDir(), "wrong-root")
			if data, err := exec.Command(python, args...).CombinedOutput(); err == nil {
				t.Fatalf("accepted wrong root: %s", data)
			}
		}
	})
	for _, name := range []string{m.Clients[0].Package.Filename, "install-phpm.py", "php-fixture-sdk-verification.json"} {
		path := filepath.Join(out, name)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(original, '!'), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyRelease(out, root, now); err == nil {
			t.Fatalf("accepted modified %s", name)
		}
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundle, "private.seed"), []byte("must never escape"), 0600); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(t.TempDir(), "refused")
	if err := CreateClients(bundle, partial, root, seed, "0.1.0-test", strings.Repeat("a", 40), binary, "", repo, filepath.Join(repo, "scripts/install-phpm.py"), now, time.Hour); err == nil {
		t.Fatal("accepted unlisted potentially private file")
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("partial destination exposed")
	}
}
