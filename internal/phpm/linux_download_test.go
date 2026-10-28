package phpm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func linuxCatalogFixture(names ...string) []byte {
	var entries []map[string]any
	for _, name := range names {
		entries = append(entries, map[string]any{"name": name, "full_path": staticPHPPath + name, "is_dir": false})
	}
	data, _ := json.Marshal(entries)
	return data
}

func TestLinuxCatalogRequiresChecksumsAndSortsNumericVersions(t *testing.T) {
	name := func(v string) string { return "php-" + v + "-cli-linux-x86_64.tar.gz" }
	data := linuxCatalogFixture(name("8.4.9"), name("8.4.9")+".sha256", name("8.4.27"), name("8.4.27")+".sha256", name("8.5.12"), "php-8.4.99-cli-linux-aarch64.tar.gz", "../escape")
	releases, err := parseLinuxCatalog(data)
	if err != nil || len(releases) != 2 || releases[0].Version != "8.4.27" {
		t.Fatalf("catalog: %+v %v", releases, err)
	}
	selected, err := selectLinuxRelease(releases, "8.4")
	if err != nil || selected.Version != "8.4.27" {
		t.Fatalf("selection: %+v %v", selected, err)
	}
	for _, requested := range []string{"8.5", "8.4.26", "^8.4", "8"} {
		if _, err := selectLinuxRelease(releases, requested); err == nil {
			t.Fatalf("accepted unavailable/invalid %s", requested)
		}
	}
	if _, err := parseLinuxCatalog([]byte(`[{"name":"php-8.4.27-cli-linux-x86_64.tar.gz","full_path":"/elsewhere/php"}]`)); err == nil {
		t.Fatal("untrusted index path accepted")
	}
}

func TestStaticPHPOriginAndChecksumValidation(t *testing.T) {
	for _, raw := range []string{staticPHPBase + "?format=json", "https://static-php-cli.fra1.digitaloceanspaces.com" + staticPHPPath + "php.tar.gz", "https://dl.static-php.dev/down?path=v3%2Fphp-bin%2Fcommon%2Fphp.tar.gz.sha256"} {
		if err := staticPHPURL(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"https://dl.static-php.dev/down?path=elsewhere/php", "https://dl.static-php.dev/down?path=v3/php-bin/common/../escape", "https://dl.static-php.dev/down?path=v3/php-bin/common/php&extra=1", "https://dl.static-php.dev/down?path=v3/php-bin/common/php&path=v3/php-bin/common/other"} {
		if err := staticPHPURL(raw); err == nil {
			t.Fatalf("invalid frontend route accepted: %s", raw)
		}
	}
	for _, raw := range []string{"http://dl.static-php.dev" + staticPHPPath, "https://evil.test" + staticPHPPath, "https://dl.static-php.dev:443" + staticPHPPath, "https://dl.static-php.dev/other/", "https://dl.static-php.dev" + staticPHPPath + "../escape", "https://dl.static-php.dev" + staticPHPPath + "%2e%2e/escape", "https://user@dl.static-php.dev" + staticPHPPath} {
		if err := staticPHPURL(raw); err == nil {
			t.Fatalf("untrusted URL accepted: %s", raw)
		}
		client := staticPHPClient()
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, raw, nil)
		if request != nil && client.CheckRedirect(request, nil) == nil {
			t.Fatalf("untrusted redirect accepted: %s", raw)
		}
	}
	name := "php-8.4.27-cli-linux-x86_64.tar.gz"
	hash := strings.Repeat("a", 64)
	if got, err := parseLinuxChecksum([]byte(hash+"  "+name+"\n"), name); err != nil || got != hash {
		t.Fatalf("checksum: %s %v", got, err)
	}
	for _, data := range []string{hash, hash + "  wrong.tar.gz", hash + "  " + name + "\n" + hash + "  " + name, "invalid  " + name} {
		if _, err := parseLinuxChecksum([]byte(data), name); err == nil {
			t.Fatalf("invalid checksum accepted: %q", data)
		}
	}
}

func TestLinuxDownloadRejectsBadChecksumBeforeExecutingOrRegistering(t *testing.T) {
	s := testStore(t)
	name := "php-8.4.27-cli-linux-x86_64.tar.gz"
	p := linuxRuntimeProvider{Store: s, Client: &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		var content []byte
		switch req.URL.String() {
		case staticPHPBase + "?format=json":
			content = linuxCatalogFixture(name, name+".sha256")
		case staticPHPBase + name + ".sha256":
			content = []byte(strings.Repeat("0", 64) + "  " + name)
		case staticPHPDownloadBase + name:
			content = []byte("modified download")
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": []string{"bytes 0-16/17"}}, ContentLength: 17, Body: io.NopCloser(bytes.NewReader(content)), Request: req}, nil
		default:
			t.Fatalf("unexpected URL: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(content)), Request: req}, nil
	})}}
	if _, err := p.install("8.4"); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("bad download accepted: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Home, "store", "runtimes"))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed download registered runtime")
	}
}

func tarGZIPFixture(t *testing.T, headers []*tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	w := tar.NewWriter(gz)
	for i, header := range headers {
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(bodies[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestTarExtractionRejectsTraversalLinksDuplicatesAndCorruption(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/escape", `..\escape`} {
		t.Run(name, func(t *testing.T) {
			data := tarGZIPFixture(t, []*tar.Header{{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: 1}}, [][]byte{[]byte("x")})
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			if err := os.WriteFile(archive, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := extractTarGZIP(archive, t.TempDir()); err == nil {
				t.Fatal("path escape accepted")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar} {
		data := tarGZIPFixture(t, []*tar.Header{{Name: "php", Linkname: "target", Typeflag: kind, Mode: 0755}}, [][]byte{nil})
		archive := filepath.Join(t.TempDir(), "link.tar.gz")
		if err := os.WriteFile(archive, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := extractTarGZIP(archive, t.TempDir()); err == nil {
			t.Fatalf("special entry %d accepted", kind)
		}
	}
	duplicate := tarGZIPFixture(t, []*tar.Header{{Name: "php", Typeflag: tar.TypeReg, Mode: 0755, Size: 1}, {Name: "php", Typeflag: tar.TypeReg, Mode: 0755, Size: 1}}, [][]byte{[]byte("x"), []byte("y")})
	valid := tarGZIPFixture(t, []*tar.Header{{Name: "php", Typeflag: tar.TypeReg, Mode: 0755, Size: 1}}, [][]byte{[]byte("x")})
	corrupted := append([]byte{}, valid...)
	corrupted[len(corrupted)-8] ^= 1
	for _, data := range [][]byte{duplicate, corrupted, valid[:len(valid)-4]} {
		archive := filepath.Join(t.TempDir(), "invalid.tar.gz")
		if err := os.WriteFile(archive, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := extractTarGZIP(archive, t.TempDir()); err == nil {
			t.Fatal("duplicate or gzip corruption accepted")
		}
	}
	archive := filepath.Join(t.TempDir(), "valid.tar.gz")
	if err := os.WriteFile(archive, valid, 0600); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := extractTarGZIP(archive, target); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(target, "php"))
		if err != nil || info.Mode().Perm()&0100 == 0 {
			t.Fatal("executable bit was lost")
		}
	}
}

func TestLinuxELFRequiresStaticX64Executable(t *testing.T) {
	valid := make([]byte, 120)
	copy(valid, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(valid[16:], 2)
	binary.LittleEndian.PutUint16(valid[18:], 62)
	binary.LittleEndian.PutUint32(valid[20:], 1)
	binary.LittleEndian.PutUint64(valid[32:], 64)
	binary.LittleEndian.PutUint16(valid[52:], 64)
	binary.LittleEndian.PutUint16(valid[54:], 56)
	binary.LittleEndian.PutUint16(valid[56:], 1)
	binary.LittleEndian.PutUint16(valid[58:], 64)
	binary.LittleEndian.PutUint32(valid[64:], 1)
	path := filepath.Join(t.TempDir(), "php")
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(valid)
	if err := validateStaticLinuxPHP(path); err != nil {
		t.Fatal(err)
	}
	for _, program := range []uint32{2, 3} {
		bad := append([]byte{}, valid...)
		binary.LittleEndian.PutUint32(bad[64:], program)
		write(bad)
		if err := validateStaticLinuxPHP(path); err == nil {
			t.Fatal("dynamic ELF accepted")
		}
	}
	bad := append([]byte{}, valid...)
	binary.LittleEndian.PutUint16(bad[18:], 183)
	write(bad)
	if err := validateStaticLinuxPHP(path); err == nil {
		t.Fatal("wrong architecture accepted")
	}
}

func TestOptionalPublishedStaticPHPArchive(t *testing.T) {
	path := os.Getenv("PHPM_TEST_STATICPHP_ARCHIVE")
	if path == "" {
		t.Skip("set PHPM_TEST_STATICPHP_ARCHIVE to validate a downloaded static Linux archive")
	}
	target := t.TempDir()
	if err := extractTarGZIP(path, target); err != nil {
		t.Fatal(err)
	}
	if err := validateStaticLinuxPHP(filepath.Join(target, "php")); err != nil {
		t.Fatal(err)
	}
}

func TestOptionalStaticPHPNetwork(t *testing.T) {
	if os.Getenv("PHPM_TEST_STATICPHP_NETWORK") != "1" {
		t.Skip("set PHPM_TEST_STATICPHP_NETWORK=1 for the live provider download check")
	}
	s := testStore(t)
	p := linuxRuntimeProvider{Store: s, Client: staticPHPClient(), Progress: os.Stdout}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	releases, err := p.catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release, err := selectLinuxRelease(releases, "8.4")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Catalog selected PHP %s after %s", release.Version, time.Since(started))
	data, err := p.fetch(ctx, staticPHPBase+release.Path+".sha256", 4096)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := parseLinuxChecksum(data, release.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Published checksum verified after %s", time.Since(started))
	archive, err := p.download(ctx, staticPHPDownloadBase+release.Path, checksum)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := extractTarGZIP(archive, target); err != nil {
		t.Fatal(err)
	}
	if err := validateStaticLinuxPHP(filepath.Join(target, "php")); err != nil {
		t.Fatal(err)
	}
	t.Logf("Live catalog/download/checksum/static ELF passed for PHP %s; PHP execution is not tested by this check", release.Version)
}
