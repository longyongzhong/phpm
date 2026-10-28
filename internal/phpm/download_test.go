package phpm

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestCatalogSelectsTSNTSArchitectureAndExactPatch(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	data := `{"8.4":{"version":"8.4.27","nts-vs17-x64":{"zip":{"path":"nts.zip","sha256":"` + checksum + `"}},"ts-vs17-x64":{"zip":{"path":"ts.zip","sha256":"` + checksum + `"}},"nts-vs17-x86":{"zip":{"path":"x86.zip","sha256":"` + checksum + `"}}}}`
	catalog, err := parseWindowsCatalog([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range []bool{false, true} {
		_, artifact, err := selectWindowsArchive(catalog, "8.4", ts)
		want := "nts.zip"
		if ts {
			want = "ts.zip"
		}
		if err != nil || artifact.Path != want {
			t.Fatalf("got=%+v err=%v", artifact, err)
		}
	}
	if _, _, err := selectWindowsArchive(catalog, "8.4.2", false); err == nil {
		t.Fatal("unavailable exact patch silently upgraded")
	}
	if _, _, err := selectWindowsArchive(catalog, "invalid", false); err == nil {
		t.Fatal("invalid version accepted")
	}
}

func TestDownloadChecksumAndCacheValidation(t *testing.T) {
	s := testStore(t)
	body := []byte("verified archive")
	sum := sha256.Sum256(body)
	checksum := hex.EncodeToString(sum[:])
	requests := 0
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header), Request: req}, nil
	})}
	path, err := s.download(context.Background(), client, "https://example.test/package.zip", checksum)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.download(context.Background(), client, "https://example.test/package.zip", checksum); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("cache missed: requests=%d", requests)
	}
	if err := os.WriteFile(path, []byte("modified cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.download(context.Background(), client, "https://example.test/package.zip", checksum); err == nil {
		t.Fatal("corrupt cache accepted")
	}
	bad := strings.Repeat("0", 64)
	if _, err := s.download(context.Background(), client, "https://example.test/package.zip", bad); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if _, err := os.Stat(filepath.Join(s.Home, "cache", bad+".zip")); !os.IsNotExist(err) {
		t.Fatal("bad download was committed")
	}
}

func TestArchiveRejectsTraversalAndDuplicateFiles(t *testing.T) {
	for _, names := range [][]string{{"../escaped"}, {`..\escaped`}, {"/absolute"}, {"C:/absolute"}, {"same", "same"}} {
		t.Run(strings.Join(names, "_"), func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "package.zip")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			for _, name := range names {
				entry, err := writer.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte("data")); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := extractZIP(archive, t.TempDir()); err == nil {
				t.Fatalf("unsafe archive accepted: %v", names)
			}
		})
	}
}

func TestOfficialDownloadHosts(t *testing.T) {
	for _, raw := range []string{"http://downloads.php.net/file", "https://downloads.php.net.evil.test/file", "https://user@downloads.php.net/file", "https://downloads.php.net:444/file"} {
		if err := officialURL(raw); err == nil {
			t.Fatalf("untrusted URL accepted: %s", raw)
		}
	}
	if err := officialURL(windowsCatalogURL); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogSeparatesPackageRevisionFromPHPVersion(t *testing.T) {
	data := `{"8.4":{"version":"8.4.27-1","nts-vs17-x64":{"zip":{"path":"php-8.4.27-1.zip","sha256":"` + strings.Repeat("a", 64) + `"}}}}`
	catalog, err := parseWindowsCatalog([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	release, _, err := selectWindowsArchive(catalog, "8.4.27", false)
	if err != nil || release.Version != "8.4.27" || release.PackageVersion != "8.4.27-1" {
		t.Fatalf("release=%+v err=%v", release, err)
	}
}
