package phpm

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"phpm/internal/catalog"
)

func signedFixture(t *testing.T) (catalog.Catalog, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := func(n string) catalog.Package {
		return catalog.Package{Filename: n + ".tar.gz", SHA256: strings.Repeat("a", 64), Size: 1}
	}
	return catalog.Catalog{Schema: 1, Sequence: 1, IssuedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour), Builds: []catalog.Build{{ID: "fixture", Version: "8.4.27", Revision: 1, Target: catalog.Target, PHPAPI: "20240924", PHPBinarySHA256: strings.Repeat("b", 64), Runtime: p("runtime"), SDK: p("sdk")}}}, key
}

func writeSignedFixture(t *testing.T, dir string, c catalog.Catalog, key ed25519.PrivateKey) {
	t.Helper()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json.sig"), []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSignedSourcePinsKeyAndPreventsCatalogReplay(t *testing.T) {
	index, key := signedFixture(t)
	dir := t.TempDir()
	writeSignedFixture(t, dir, index, key)
	keyPath := filepath.Join(dir, "trusted.pub")
	os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))), 0600)
	s := testStore(t)
	cli := CLI{Store: s, Out: &bytes.Buffer{}}
	add := []string{"add", "verified", "--catalog", filepath.Join(dir, "catalog.json"), "--key", keyPath}
	if err := cli.sourceCommand(add); err != nil {
		t.Fatal(err)
	}
	if err := cli.sourceCommand(add); err == nil {
		t.Fatal("silently replaced pinned source")
	}
	index.Sequence = 2
	writeSignedFixture(t, dir, index, key)
	if _, _, err := s.fetchSourceCatalog(context.Background(), "verified", catalogClient()); err != nil {
		t.Fatal(err)
	}
	index.Sequence = 1
	writeSignedFixture(t, dir, index, key)
	if _, _, err := s.fetchSourceCatalog(context.Background(), "verified", catalogClient()); err == nil {
		t.Fatal("accepted old signed catalog")
	}
	index.Sequence = 2
	index.Builds[0].Revision++
	writeSignedFixture(t, dir, index, key)
	if _, _, err := s.fetchSourceCatalog(context.Background(), "verified", catalogClient()); err == nil {
		t.Fatal("accepted changed bytes at same sequence")
	}
	index.Sequence = 3
	index.ExpiresAt = time.Now().Add(-time.Second)
	writeSignedFixture(t, dir, index, key)
	if _, _, err := s.fetchSourceCatalog(context.Background(), "verified", catalogClient()); err == nil {
		t.Fatal("accepted expired catalog")
	}
	stored, err := s.loadSource("verified")
	if err != nil || stored.Sequence != 2 {
		t.Fatalf("rejected catalog changed trusted state: %+v %v", stored, err)
	}
}

func TestCatalogPackageVerifiesTLSSizeHashAndCache(t *testing.T) {
	data := []byte("verified archive bytes")
	sum := sha256.Sum256(data)
	p := catalog.Package{Filename: "runtime.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.Write(data) }))
	defer server.Close()
	s := testStore(t)
	source := catalogSource{Location: server.URL + "/catalog.json"}
	path, err := s.catalogPackage(context.Background(), source, p, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.catalogPackage(context.Background(), source, p, server.Client()); err != nil || requests != 1 {
		t.Fatalf("verified cache not reused: %v %d", err, requests)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.catalogPackage(context.Background(), source, p, server.Client()); err == nil {
		t.Fatal("accepted corrupt cache")
	}
	for _, body := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), 'x'), bytes.Repeat([]byte("z"), len(data))} {
		data = body
		s = testStore(t)
		if _, err := s.catalogPackage(context.Background(), source, p, server.Client()); err == nil {
			t.Fatal("accepted truncated/oversized/hash-mismatched package")
		}
		entries, _ := os.ReadDir(filepath.Join(s.Home, "cache"))
		if len(entries) != 0 {
			t.Fatal("invalid download committed to cache")
		}
	}
}

func TestCatalogOriginHostAndSelection(t *testing.T) {
	for _, location := range []string{"http://example.test/catalog.json", "https://user@example.test/catalog.json", "https://example.test/catalog.json?token=secret", "https://example.test/../catalog.json", "https://example.test/wrong.json"} {
		if _, err := normalizeCatalogLocation(location); err == nil {
			t.Fatalf("accepted %s", location)
		}
	}
	if _, err := normalizeCatalogLocation("https://github.com/owner/repo/releases/download/tag/catalog.json"); err != nil {
		t.Fatal(err)
	}
	client := catalogClient()
	request, _ := http.NewRequest("GET", "http://insecure.test/file", nil)
	if err := client.CheckRedirect(request, []*http.Request{{}}); err == nil {
		t.Fatal("accepted HTTP redirect")
	}
	if err := requireDebianHost("linux", "amd64", []byte("ID=debian\nVERSION_ID=\"13\"\n"), "glibc 2.41\n"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []struct{ goos, arch, release, libc string }{{"linux", "amd64", "ID=openwrt\nVERSION_ID=24", "musl"}, {"linux", "amd64", "ID=debian\nVERSION_ID=12", "glibc 2.36"}, {"linux", "arm64", "ID=debian\nVERSION_ID=13", "glibc 2.41"}, {"windows", "amd64", "", ""}} {
		if err := requireDebianHost(host.goos, host.arch, []byte(host.release), host.libc); err == nil {
			t.Fatal("accepted unsupported host")
		}
	}
	index, _ := signedFixture(t)
	b := index.Builds[0]
	b.Revision = 2
	b.ID = "newer"
	index.Builds = append(index.Builds, b)
	selected, err := selectCatalogBuild(index, "8.4")
	if err != nil || selected.ID != "newer" {
		t.Fatalf("selection: %+v %v", selected, err)
	}
}
