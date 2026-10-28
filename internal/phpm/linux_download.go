package phpm

import (
	"context"
	"crypto/tls"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// StaticPHP is a community distributor, not the PHP project's Windows provider.
// Its common preset is static musl PHP with a fixed set of built-in extensions.
const staticPHPBase = "https://dl.static-php.dev/v3/php-bin/common/"
const staticPHPPath = "/v3/php-bin/common/"
const staticPHPDownloadBase = "https://static-php-cli.fra1.digitaloceanspaces.com/v3/php-bin/common/"

var staticPHPArchiveName = regexp.MustCompile(`^php-(\d+\.\d+\.\d+)-cli-linux-x86_64\.tar\.gz$`)

type linuxRelease struct{ Version, Path string }
type linuxRuntimeProvider struct {
	Store    *Store
	Client   *http.Client
	Progress io.Writer
}

func staticPHPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" ||
		(u.Host != "dl.static-php.dev" && u.Host != "static-php-cli.fra1.digitaloceanspaces.com") {
		return errors.New("StaticPHP downloads must use HTTPS on the provider's common-preset hosts and path")
	}
	resourcePath := u.Path
	// The provider's frontend routes files through /down?path=... before
	// redirecting to object storage. Permit only that exact common-preset path.
	if u.Host == "dl.static-php.dev" && u.Path == "/down" {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query) != 1 || len(query["path"]) != 1 {
			return errors.New("invalid StaticPHP download route")
		}
		resourcePath = "/" + strings.TrimPrefix(query.Get("path"), "/")
	}
	if !strings.HasPrefix(resourcePath, staticPHPPath) || strings.Contains(resourcePath, "%") {
		return errors.New("StaticPHP download is outside the common-preset path")
	}
	// Refuse path escapes even when a redirect uses an allowed origin.
	for _, component := range strings.Split(resourcePath, "/") {
		if component == ".." || component == "." || strings.Contains(component, `\`) {
			return errors.New("invalid StaticPHP download path")
		}
	}
	return nil
}

func staticPHPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	transport.DisableCompression = true
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return &http.Client{Transport: transport, Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many StaticPHP redirects")
		}
		return staticPHPURL(req.URL.String())
	}}
}

func (p linuxRuntimeProvider) fetch(ctx context.Context, raw string, limit int64) ([]byte, error) {
	if err := staticPHPURL(raw); err != nil {
		return nil, err
	}
	response, err := getHTTP(ctx, p.Client, raw)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("StaticPHP metadata exceeds size limit")
	}
	return data, nil
}

func parseLinuxCatalog(data []byte) ([]linuxRelease, error) {
	var entries []struct {
		Name      string `json:"name"`
		FullPath  string `json:"full_path"`
		Directory bool   `json:"is_dir"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	if len(entries) > 20000 {
		return nil, errors.New("StaticPHP catalog contains too many entries")
	}
	files := map[string]bool{}
	for _, entry := range entries {
		if !entry.Directory && entry.FullPath == staticPHPPath+entry.Name {
			files[entry.Name] = true
		}
	}
	var releases []linuxRelease
	for name := range files {
		match := staticPHPArchiveName.FindStringSubmatch(name)
		if match == nil || !files[name+".sha256"] {
			continue
		}
		if _, err := comparePHPVersions(match[1], match[1]); err != nil {
			return nil, err
		}
		releases = append(releases, linuxRelease{Version: match[1], Path: name})
	}
	sort.Slice(releases, func(i, j int) bool {
		order, _ := comparePHPVersions(releases[i].Version, releases[j].Version)
		return order > 0
	})
	if len(releases) == 0 {
		return nil, errors.New("StaticPHP catalog has no Linux x64 CLI builds with published SHA-256 files")
	}
	return releases, nil
}

func (p linuxRuntimeProvider) catalog(ctx context.Context) ([]linuxRelease, error) {
	data, err := p.fetch(ctx, staticPHPBase+"?format=json", 8*1024*1024)
	if err != nil {
		return nil, err
	}
	return parseLinuxCatalog(data)
}

func selectLinuxRelease(releases []linuxRelease, requested string) (linuxRelease, error) {
	if !versionPattern.MatchString(requested) {
		return linuxRelease{}, errors.New("expected PHP major.minor or major.minor.patch")
	}
	for _, release := range releases {
		if matchesVersion(release.Version, requested) {
			return release, nil
		}
	}
	return linuxRelease{}, fmt.Errorf("PHP %s has no checksummed Linux x64 CLI build in the StaticPHP common catalog; use php available or import a verified local build", requested)
}

func parseLinuxChecksum(data []byte, name string) (string, error) {
	fields := strings.Fields(string(data))
	if len(fields) != 2 || !validSHA(fields[0]) || strings.TrimPrefix(fields[1], "*") != name {
		return "", errors.New("StaticPHP SHA-256 file is invalid or names a different archive")
	}
	return strings.ToLower(fields[0]), nil
}

func validateStaticLinuxPHP(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("invalid Linux PHP ELF: %w", err)
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != elf.EM_X86_64 || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return errors.New("downloaded PHP is not a Linux x86_64 executable")
	}
	for _, program := range f.Progs {
		if program.Type == elf.PT_INTERP || program.Type == elf.PT_DYNAMIC {
			return errors.New("downloaded PHP is dynamically linked; the static provider cannot guarantee its system dependencies")
		}
	}
	return nil
}

func (p linuxRuntimeProvider) install(requested string) (Runtime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	releases, err := p.catalog(ctx)
	if err != nil {
		return Runtime{}, fmt.Errorf("fetch StaticPHP catalog: %w", err)
	}
	release, err := selectLinuxRelease(releases, requested)
	if err != nil {
		return Runtime{}, err
	}
	checksumData, err := p.fetch(ctx, staticPHPBase+release.Path+".sha256", 4096)
	if err != nil {
		return Runtime{}, fmt.Errorf("fetch published PHP checksum: %w", err)
	}
	checksum, err := parseLinuxChecksum(checksumData, release.Path)
	if err != nil {
		return Runtime{}, err
	}
	raw := staticPHPDownloadBase + release.Path
	archive, err := p.download(ctx, raw, checksum)
	if err != nil {
		return Runtime{}, err
	}
	temp, err := os.MkdirTemp(filepath.Join(p.Store.Home, "tmp"), "staticphp-*")
	if err != nil {
		return Runtime{}, err
	}
	defer os.RemoveAll(temp)
	if err := extractTarGZIP(archive, temp); err != nil {
		return Runtime{}, err
	}
	php := filepath.Join(temp, "php")
	if err := validateStaticLinuxPHP(php); err != nil {
		return Runtime{}, err
	}
	if err := os.Chmod(php, 0700); err != nil {
		return Runtime{}, err
	}
	info, err := probePHP(php)
	if err != nil {
		return Runtime{}, fmt.Errorf("downloaded static PHP could not run on this host; check Linux x64 and executable filesystem permissions: %w", err)
	}
	if info.Version != release.Version || info.ZTS || info.Debug || info.IntSize != 8 {
		return Runtime{}, errors.New("downloaded PHP identity differs from its Linux x64 NTS release")
	}
	return p.Store.importRuntime(temp, "php", Source{Kind: "staticphp-linux", URL: raw, SHA256: checksum})
}
