package phpm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const windowsExtensionBase = "https://downloads.php.net/~windows/pecl/releases/"

var indexLinkPattern = regexp.MustCompile(`(?i)\bhref\s*=\s*["']([^"']+)["']`)
var windowsCompilerPattern = regexp.MustCompile(`(?i)(?:^|,)(v[cs]\d+)(?:,|$)`)

// A third-party DLL placed into an imported PHP's ext directory (e.g. Redis)
// is not a runtime-bundled extension and must not shadow online version choice.
const runtimeBundledNames = " bcmath bz2 calendar com_dotnet ctype curl dba dom enchant exif ffi fileinfo filter ftp gd gettext gmp hash iconv intl ldap mbstring mysqli odbc opcache openssl pcntl pdo pdo_dblib pdo_firebird pdo_mysql pdo_odbc pdo_pgsql pdo_sqlite posix readline session shmop simplexml snmp soap sockets sodium sqlite3 sysvmsg sysvsem sysvshm tidy tokenizer xml xmlreader xmlwriter xsl zip zlib "

type windowsExtensionProvider struct {
	Store          *Store
	Client         *http.Client
	Progress       io.Writer
	FilterVersions func(string, []string) ([]string, error)
}

func (p windowsExtensionProvider) acquire(ctx context.Context, rt Runtime, request ExtensionRequest) (acquiredExtension, error) {
	if err := request.validate(); err != nil {
		return acquiredExtension{}, err
	}
	if version, exists := rt.PHP.Extensions[loadedExtensionName(request.Name)]; exists {
		if err := p.checkVersion(request.Name, version); err != nil {
			return acquiredExtension{}, err
		}
		if request.SHA256 != "" || request.Zend {
			return acquiredExtension{}, errors.New("a compiled-in extension has no separate archive or selectable loader")
		}
		if request.Version != "" && request.Version != version {
			return acquiredExtension{}, fmt.Errorf("%s is compiled into PHP at version %s", request.Name, version)
		}
		p.message("Using compiled-in %s %s\n", request.Name, version)
		return acquiredExtension{Version: version}, nil
	}
	zend := request.Zend || request.Name == "xdebug" || request.Name == "opcache"
	var bundled string
	var err error
	if strings.Contains(runtimeBundledNames, " "+request.Name+" ") {
		bundled, err = p.Store.findBundledModule(rt, request.Name)
		if err != nil {
			return acquiredExtension{}, err
		}
	}
	if bundled != "" {
		version, err := p.Store.inspectExtension(rt, request.Name, bundled, zend)
		if err != nil {
			return acquiredExtension{}, err
		}
		if err := p.checkVersion(request.Name, version); err != nil {
			return acquiredExtension{}, err
		}
		if request.Version != "" && version != request.Version {
			return acquiredExtension{}, fmt.Errorf("bundled %s has version %s, not %s; select a different PHP build", request.Name, version, request.Version)
		}
		p.message("Using bundled %s %s\n", request.Name, version)
		checksum, err := fileSHA(bundled)
		if err != nil {
			return acquiredExtension{}, err
		}
		ext, err := p.Store.importExtensionFromSource(rt, request.Name, version, bundled, "", request.SHA256, zend, Source{Kind: "runtime-bundled", SHA256: checksum})
		return acquiredExtension{Version: version, Artifact: &ext}, err
	}
	if rt.Platform != "windows-amd64" {
		if rt.Source.Kind == "signed-catalog" {
			return p.acquireCatalogExtension(ctx, rt, request)
		}
		return acquiredExtension{}, errors.New("online extension downloads currently support Windows x64; Linux/macOS support bundled modules and ext import, with source-build support still pending")
	}
	version, raw, err := p.resolveArchive(ctx, rt, request)
	if err != nil {
		return acquiredExtension{}, err
	}
	p.message("Downloading %s\n", raw)
	archive, checksum, err := p.downloadArchive(ctx, raw, request.SHA256)
	if err != nil {
		return acquiredExtension{}, err
	}
	temp, err := os.MkdirTemp(filepath.Join(p.Store.Home, "tmp"), "extension-zip-*")
	if err != nil {
		return acquiredExtension{}, err
	}
	defer os.RemoveAll(temp)
	if err := extractZIP(archive, temp); err != nil {
		return acquiredExtension{}, err
	}
	module, err := findModuleInTree(temp, "php_"+request.Name+".dll")
	if err != nil {
		return acquiredExtension{}, err
	}
	if module == "" {
		return acquiredExtension{}, fmt.Errorf("archive contains no php_%s.dll", request.Name)
	}
	ext, err := p.Store.importExtensionFromSource(rt, request.Name, version, module, temp, "", zend, Source{Kind: "official-windows-pecl", URL: raw, SHA256: checksum})
	if err != nil {
		return acquiredExtension{}, err
	}
	return acquiredExtension{Version: version, Artifact: &ext}, nil
}

func (p windowsExtensionProvider) resolveArchive(ctx context.Context, rt Runtime, request ExtensionRequest) (string, string, error) {
	if err := request.validate(); err != nil {
		return "", "", err
	}
	profile, err := windowsExtensionProfile(rt)
	if err != nil {
		return "", "", err
	}
	base := windowsExtensionBase + request.Name + "/"
	var versions []string
	if request.Version != "" {
		versions = []string{request.Version}
	} else {
		data, err := p.index(ctx, base)
		if err != nil {
			return "", "", fmt.Errorf("cannot list Windows releases for %s: %w", request.Name, err)
		}
		versions = indexVersions(data)
		if len(versions) == 0 {
			return "", "", fmt.Errorf("no numeric release versions found for %s", request.Name)
		}
	}
	if p.FilterVersions != nil {
		versions, err = p.FilterVersions(request.Name, versions)
		if err != nil {
			return "", "", err
		}
		if len(versions) == 0 {
			return "", "", fmt.Errorf("no published %s version satisfies all Composer requirements", request.Name)
		}
	}
	for _, version := range versions {
		name := fmt.Sprintf("php_%s-%s-%s.zip", request.Name, version, profile)
		directory := base + version + "/"
		data, err := p.index(ctx, directory)
		if err != nil {
			return "", "", fmt.Errorf("cannot inspect %s@%s: %w", request.Name, version, err)
		}
		if !indexHasFile(data, name) {
			continue
		}
		return version, directory + name, nil
	}
	return "", "", fmt.Errorf("no Windows archive for %s%s matches PHP build %s; choose another extension/PHP version or use ext import", request.Name, optionalVersion(request.Version), profile)
}

func (p windowsExtensionProvider) checkVersion(name, version string) error {
	if p.FilterVersions == nil {
		return nil
	}
	allowed, err := p.FilterVersions(name, []string{version})
	if err != nil {
		return err
	}
	if len(allowed) != 1 || allowed[0] != version {
		return fmt.Errorf("bundled/compiled %s %s does not satisfy all Composer requirements; select a compatible PHP build", name, version)
	}
	return nil
}

func optionalVersion(version string) string {
	if version != "" {
		return "@" + version
	}
	return ""
}

func (p windowsExtensionProvider) message(format string, args ...any) {
	if p.Progress != nil {
		fmt.Fprintf(p.Progress, format, args...)
	}
}

func windowsExtensionProfile(rt Runtime) (string, error) {
	if rt.Platform != "windows-amd64" || rt.PHP.IntSize != 8 {
		return "", errors.New("Windows extension downloads require an x64 PHP build")
	}
	if rt.PHP.Debug {
		return "", errors.New("official Windows extension downloads do not support debug PHP builds")
	}
	match := windowsCompilerPattern.FindStringSubmatch(rt.PHP.Build)
	if match == nil {
		return "", fmt.Errorf("cannot identify the Windows compiler from PHP build %q", rt.PHP.Build)
	}
	parts := strings.Split(rt.PHP.Version, ".")
	if len(parts) != 3 || !versionPattern.MatchString(rt.PHP.Version) {
		return "", errors.New("invalid PHP runtime version")
	}
	ts := "nts"
	if rt.PHP.ZTS {
		ts = "ts"
	}
	return strings.Join(parts[:2], ".") + "-" + ts + "-" + strings.ToLower(match[1]) + "-x64", nil
}

func (p windowsExtensionProvider) index(ctx context.Context, raw string) ([]byte, error) {
	if err := officialURL(raw); err != nil {
		return nil, err
	}
	response, err := getHTTP(ctx, p.Client, raw)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4*1024*1024 {
		return nil, errors.New("extension directory index exceeds 4 MiB")
	}
	return data, nil
}

func indexVersions(data []byte) []string {
	seen := map[string]bool{}
	var versions []string
	for _, match := range indexLinkPattern.FindAllSubmatch(data, -1) {
		version := strings.TrimSuffix(html.UnescapeString(string(match[1])), "/")
		if !versionPattern.MatchString(version) || strings.Count(version, ".") != 2 || seen[version] {
			continue
		}
		valid := true
		for _, part := range strings.Split(version, ".") {
			if _, err := strconv.Atoi(part); err != nil {
				valid = false
			}
		}
		if valid {
			versions = append(versions, version)
			seen[version] = true
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		a, b := strings.Split(versions[i], "."), strings.Split(versions[j], ".")
		for n := range a {
			av, _ := strconv.Atoi(a[n])
			bv, _ := strconv.Atoi(b[n])
			if av != bv {
				return av > bv
			}
		}
		return versions[i] < versions[j]
	})
	return versions
}

func indexHasFile(data []byte, name string) bool {
	for _, match := range indexLinkPattern.FindAllSubmatch(data, -1) {
		if html.UnescapeString(string(match[1])) == name {
			return true
		}
	}
	return false
}

func findModuleInTree(root, basename string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("module tree contains a symlink: %s", path)
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), basename) {
			if found != "" {
				return fmt.Errorf("multiple copies of %s found in package", basename)
			}
			found = path
		}
		return nil
	})
	return found, err
}

func (s *Store) findBundledModule(rt Runtime, name string) (string, error) {
	basename := name + ".so"
	if strings.HasPrefix(rt.Platform, "windows-") {
		basename = "php_" + name + ".dll"
	}
	return findModuleInTree(filepath.Join(s.runtimePath(rt.ID), "payload"), basename)
}

type extensionDownloadReceipt struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// PECL's Windows directory does not consistently publish archive checksums.
// Trust the official HTTPS origin on first retrieval, record the observed hash,
// and verify cached content or an optional independently supplied pin thereafter.
func (p windowsExtensionProvider) downloadArchive(ctx context.Context, raw, expected string) (string, string, error) {
	if err := officialURL(raw); err != nil {
		return "", "", err
	}
	u, _ := url.Parse(raw)
	if !strings.HasPrefix(u.Path, "/~windows/pecl/releases/") {
		return "", "", errors.New("extension archive is outside the official release directory")
	}
	unlock, err := p.Store.lock("extension-download:" + raw)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	receiptPath := filepath.Join(p.Store.Home, "cache", "extension-"+digestJSON(raw)+".json")
	var receipt extensionDownloadReceipt
	if err := readJSON(receiptPath, &receipt); err == nil {
		if receipt.URL != raw || !validSHA(receipt.SHA256) {
			return "", "", errors.New("invalid cached extension receipt")
		}
		if expected != "" && !strings.EqualFold(receipt.SHA256, expected) {
			return "", "", errors.New("cached extension archive differs from --sha256")
		}
		path := filepath.Join(p.Store.Home, "cache", receipt.SHA256+".zip")
		actual, err := fileSHA(path)
		if err != nil {
			return "", "", err
		}
		if actual != receipt.SHA256 {
			return "", "", errors.New("cached extension archive is corrupted")
		}
		return path, actual, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	response, err := getHTTP(ctx, p.Client, raw)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	temp, err := os.CreateTemp(filepath.Join(p.Store.Home, "cache"), ".extension-download-*")
	if err != nil {
		return "", "", err
	}
	name := temp.Name()
	defer os.Remove(name)
	const limit = int64(128 * 1024 * 1024)
	n, copyErr := io.Copy(temp, io.LimitReader(response.Body, limit+1))
	closeErr := temp.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", "", err
	}
	if n > limit {
		return "", "", errors.New("extension archive exceeds 128 MiB")
	}
	checksum, err := fileSHA(name)
	if err != nil {
		return "", "", err
	}
	if expected != "" && !strings.EqualFold(expected, checksum) {
		return "", "", errors.New("extension archive SHA-256 mismatch; nothing was installed")
	}
	// Share the content-addressed cache with runtime downloads, under its lock.
	cacheUnlock, err := p.Store.lock("download:" + checksum)
	if err != nil {
		return "", "", err
	}
	defer cacheUnlock()
	target := filepath.Join(p.Store.Home, "cache", checksum+".zip")
	if existing, err := fileSHA(target); err == nil {
		if existing != checksum {
			return "", "", errors.New("cached archive is corrupted")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(name, target); err != nil {
			return "", "", err
		}
	} else {
		return "", "", err
	}
	receipt = extensionDownloadReceipt{URL: raw, SHA256: checksum}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := atomicWrite(receiptPath, append(data, '\n'), 0600); err != nil {
		return "", "", err
	}
	return target, checksum, nil
}
