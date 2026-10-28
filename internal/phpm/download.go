package phpm

import (
	"archive/zip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const windowsCatalogURL = "https://downloads.php.net/~windows/releases/releases.json"
const windowsDownloadBase = "https://downloads.php.net/~windows/releases/"

type WindowsArchive struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type WindowsRelease struct {
	Version        string
	PackageVersion string
	Variants       map[string]WindowsArchive
}

func officialURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "downloads.php.net" && u.Hostname() != "windows.php.net") {
		return errors.New("downloads must use HTTPS on an official PHP Windows host")
	}
	return nil
}

func officialClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many download redirects")
		}
		return officialURL(req.URL.String())
	}}
}

func getHTTP(ctx context.Context, client *http.Client, raw string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PHPM/"+Version)
	// Checksums apply to published archive bytes, without transparent HTTP
	// content-encoding transformations by the transport or a reverse proxy.
	req.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func fetchWindowsCatalog(ctx context.Context) (map[string]WindowsRelease, error) {
	response, err := getHTTP(ctx, officialClient(), windowsCatalogURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4*1024*1024 {
		return nil, errors.New("release catalog exceeds size limit")
	}
	return parseWindowsCatalog(data)
}

func parseWindowsCatalog(data []byte) (map[string]WindowsRelease, error) {
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	result := map[string]WindowsRelease{}
	for branch, fields := range raw {
		var release WindowsRelease
		if err := json.Unmarshal(fields["version"], &release.Version); err != nil {
			return nil, err
		}
		// Official Windows packages can carry a packaging revision (e.g.
		// 8.4.27-1), distinct from the PHP_VERSION reported by the binary.
		match := regexp.MustCompile(`^(\d+\.\d+\.\d+)(?:-\d+)?$`).FindStringSubmatch(release.Version)
		if match == nil {
			return nil, fmt.Errorf("catalog contains an unsupported PHP package version %q", release.Version)
		}
		release.PackageVersion, release.Version = release.Version, match[1]
		release.Variants = map[string]WindowsArchive{}
		for variant, value := range fields {
			if !strings.HasPrefix(variant, "nts-") && !strings.HasPrefix(variant, "ts-") {
				continue
			}
			var entry struct {
				ZIP WindowsArchive `json:"zip"`
			}
			if err := json.Unmarshal(value, &entry); err != nil {
				return nil, err
			}
			if entry.ZIP.Path != "" {
				release.Variants[variant] = entry.ZIP
			}
		}
		result[branch] = release
	}
	return result, nil
}

func selectWindowsArchive(catalog map[string]WindowsRelease, requested string, ts bool) (WindowsRelease, WindowsArchive, error) {
	if !versionPattern.MatchString(requested) {
		return WindowsRelease{}, WindowsArchive{}, errors.New("expected PHP major.minor or major.minor.patch")
	}
	parts := strings.Split(requested, ".")
	branch := strings.Join(parts[:2], ".")
	release, ok := catalog[branch]
	if !ok || !matchesVersion(release.Version, requested) {
		return release, WindowsArchive{}, fmt.Errorf("PHP %s is not in the current official catalog; import an independently verified archive for older patches", requested)
	}
	prefix := "nts-"
	if ts {
		prefix = "ts-"
	}
	var keys []string
	for key := range release.Variants {
		if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, "-x64") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) != 1 {
		return release, WindowsArchive{}, errors.New("official catalog has no unique matching x64 TS/NTS build")
	}
	archive := release.Variants[keys[0]]
	if _, err := safeRelative(archive.Path); err != nil || strings.ContainsAny(archive.Path, `/\`) {
		return release, archive, errors.New("invalid archive path in official catalog")
	}
	if !validSHA(archive.SHA256) {
		return release, archive, errors.New("official catalog has no valid SHA-256")
	}
	return release, archive, nil
}

func validSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func (s *Store) download(ctx context.Context, client *http.Client, raw, checksum string) (string, error) {
	if !validSHA(checksum) {
		return "", errors.New("download requires a SHA-256 checksum")
	}
	checksum = strings.ToLower(checksum)
	unlock, err := s.lock("download:" + checksum)
	if err != nil {
		return "", err
	}
	defer unlock()
	target := filepath.Join(s.Home, "cache", checksum+".zip")
	if existing, err := fileSHA(target); err == nil {
		if existing != checksum {
			return "", errors.New("cached download is corrupted; remove the cached file and retry")
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	response, err := getHTTP(ctx, client, raw)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	temp, err := os.CreateTemp(filepath.Join(s.Home, "cache"), ".download-*")
	if err != nil {
		return "", err
	}
	name := temp.Name()
	defer os.Remove(name)
	const maxDownload = int64(512 * 1024 * 1024)
	n, copyErr := io.Copy(temp, io.LimitReader(response.Body, maxDownload+1))
	closeErr := temp.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	if n > maxDownload {
		return "", errors.New("download exceeds 512 MiB limit")
	}
	sum, err := fileSHA(name)
	if err != nil {
		return "", err
	}
	if sum != checksum {
		return "", errors.New("download SHA-256 mismatch; archive was not installed")
	}
	if err := os.Rename(name, target); err != nil {
		return "", err
	}
	return target, nil
}

func extractZIP(archive, target string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) > 20000 {
		return errors.New("archive contains too many entries")
	}
	var total uint64
	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, `\`, "/")
		rel, err := safeRelative(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		if file.Mode()&os.ModeSymlink != 0 || (!file.FileInfo().IsDir() && !file.Mode().IsRegular()) {
			return errors.New("archive contains a link or special file")
		}
		total += file.UncompressedSize64
		if total > 1024*1024*1024 {
			return errors.New("expanded archive exceeds 1 GiB limit")
		}
		dest := filepath.Join(target, rel)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600|file.Mode().Perm()&0111)
		if err != nil {
			src.Close()
			return err
		}
		n, copyErr := io.Copy(dst, io.LimitReader(src, int64(file.UncompressedSize64)+1))
		closeErr := errors.Join(src.Close(), dst.Close())
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		if uint64(n) != file.UncompressedSize64 {
			return errors.New("archive entry size mismatch")
		}
	}
	return nil
}

func (s *Store) installWindows(requested string, ts bool) (Runtime, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return Runtime{}, errors.New("automatic PHP downloads currently support Windows x64; use phpm php import on Linux/macOS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	catalog, err := fetchWindowsCatalog(ctx)
	if err != nil {
		return Runtime{}, err
	}
	release, archive, err := selectWindowsArchive(catalog, requested, ts)
	if err != nil {
		return Runtime{}, err
	}
	raw := windowsDownloadBase + archive.Path
	if err := officialURL(raw); err != nil {
		return Runtime{}, err
	}
	path, err := s.download(ctx, officialClient(), raw, archive.SHA256)
	if err != nil {
		return Runtime{}, err
	}
	temp, err := os.MkdirTemp(filepath.Join(s.Home, "tmp"), "unzip-*")
	if err != nil {
		return Runtime{}, err
	}
	defer os.RemoveAll(temp)
	if err := extractZIP(path, temp); err != nil {
		return Runtime{}, err
	}
	info, err := probePHP(filepath.Join(temp, "php.exe"))
	if err != nil {
		return Runtime{}, err
	}
	if info.Version != release.Version || info.ZTS != ts {
		return Runtime{}, errors.New("downloaded PHP identity differs from the official catalog")
	}
	return s.importRuntime(temp, "php.exe", Source{Kind: "official-windows", URL: raw, SHA256: archive.SHA256})
}
