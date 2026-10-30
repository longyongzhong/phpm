package phpm

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"phpm/internal/catalog"
)

// A source is an explicit user trust decision, never inferred from a download.
type catalogSource struct {
	Schema       int      `json:"schema"`
	Name         string   `json:"name"`
	Location     string   `json:"location"`
	PublicKey    string   `json:"public_key"`
	Sequence     uint64   `json:"sequence"`
	Digest       string   `json:"digest"`
	TrustMode    string   `json:"trust_mode,omitempty"`
	TrustVersion uint64   `json:"trust_version,omitempty"`
	TrustDigest  string   `json:"trust_digest,omitempty"`
	Revoked      []string `json:"revoked,omitempty"`
}

var sourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

func catalogClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	return &http.Client{Transport: transport, Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many catalog download redirects")
		}
		// Signed CDN URLs may carry queries; no credentials are sent to any host.
		if req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("catalog redirects require HTTPS without embedded credentials")
		}
		return nil
	}}
}

func normalizeCatalogLocation(raw string) (string, error) {
	if strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/catalog.json") || strings.Contains(u.Path, "..") || strings.ContainsAny(u.Path, `\%`) {
			return "", errors.New("source URL must be an HTTPS catalog.json URL without credentials/query/fragment")
		}
		return u.String(), nil
	}
	if strings.Contains(raw, "://") || raw == "" {
		return "", errors.New("source must be a local catalog.json path or HTTPS URL")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	if filepath.Base(abs) != "catalog.json" {
		return "", errors.New("source path must name catalog.json")
	}
	return abs, nil
}

func sourceResource(source catalogSource, name string) (string, error) {
	if !catalog.Filename(name) {
		return "", errors.New("invalid source resource filename")
	}
	if strings.HasPrefix(source.Location, "https://") {
		u, err := url.Parse(source.Location)
		if err != nil {
			return "", err
		}
		return u.ResolveReference(&url.URL{Path: name}).String(), nil
	}
	return filepath.Join(filepath.Dir(source.Location), name), nil
}

func readSourceResource(ctx context.Context, client *http.Client, location string, limit int64) ([]byte, error) {
	var reader io.ReadCloser
	if strings.HasPrefix(location, "https://") {
		response, err := getHTTP(ctx, client, location)
		if err != nil {
			return nil, err
		}
		reader = response.Body
	} else {
		f, err := os.Open(location)
		if err != nil {
			return nil, err
		}
		reader = f
	}
	defer reader.Close()
	b, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("source metadata exceeds size limit")
	}
	return b, nil
}

func (s *Store) sourcePath(name string) string { return filepath.Join(s.Home, "sources", name+".json") }

func (s *Store) loadSource(name string) (catalogSource, error) {
	var source catalogSource
	if !sourceName.MatchString(name) {
		return source, errors.New("invalid source name")
	}
	if err := readJSON(s.sourcePath(name), &source); err != nil {
		return source, err
	}
	location, err := normalizeCatalogLocation(source.Location)
	if err != nil || location != source.Location || source.Name != name || source.Schema != 1 || source.Sequence == 0 || !validSHA(source.Digest) {
		return source, errors.New("invalid stored source configuration")
	}
	if _, err := catalog.PublicKey([]byte(source.PublicKey)); err != nil {
		return source, err
	}
	if source.TrustMode != "" && source.TrustMode != catalog.RootMode {
		return source, errors.New("unsupported source trust mode")
	}
	if source.TrustMode == catalog.RootMode && (source.TrustVersion == 0 || !validSHA(source.TrustDigest)) {
		return source, errors.New("invalid stored root authorization state")
	}
	if source.TrustMode == "" && (source.TrustVersion != 0 || source.TrustDigest != "" || len(source.Revoked) != 0) {
		return source, errors.New("inconsistent direct-key source state")
	}
	return source, nil
}

// Keep the highest accepted sequence and its exact bytes. Expiry is checked on
// every fetch; network/signature errors never fall back to a different provider.
func verifiedCatalog(ctx context.Context, source catalogSource, client *http.Client) (catalog.Catalog, string, error) {
	return verifiedSourceCatalog(ctx, source, client, nil)
}

func verifiedSourceCatalog(ctx context.Context, source catalogSource, client *http.Client, trust *catalog.Trust) (catalog.Catalog, string, error) {
	var empty catalog.Catalog
	data, err := readSourceResource(ctx, client, source.Location, catalog.MaxSize)
	if err != nil {
		return empty, "", err
	}
	sig, err := readSourceResource(ctx, client, source.Location+".sig", 4096)
	if err != nil {
		return empty, "", err
	}
	key, err := catalog.PublicKey([]byte(source.PublicKey))
	if err != nil {
		return empty, "", err
	}
	var c catalog.Catalog
	if source.TrustMode == catalog.RootMode {
		if trust == nil {
			return empty, "", errors.New("root authorization is required; direct-key fallback is forbidden")
		}
		c, err = catalog.VerifyDelegatedCatalog(data, sig, *trust, time.Now())
	} else {
		c, err = catalog.Verify(data, sig, key, time.Now())
	}
	if err != nil {
		return c, "", err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if c.Sequence < source.Sequence || (c.Sequence == source.Sequence && source.Digest != "" && source.Digest != digest) {
		return empty, "", errors.New("catalog rollback or changed bytes at an accepted sequence; publish a higher sequence")
	}
	return c, digest, nil
}

func (s *Store) fetchSourceCatalog(ctx context.Context, name string, client *http.Client) (catalogSource, catalog.Catalog, error) {
	var c catalog.Catalog
	if !sourceName.MatchString(name) {
		return catalogSource{}, c, errors.New("invalid source name")
	}
	unlock, err := s.lock("source:" + name)
	if err != nil {
		return catalogSource{}, c, err
	}
	defer unlock()
	source, err := s.loadSource(name)
	if err != nil {
		return source, c, err
	}
	var trust *catalog.Trust
	if source.TrustMode == catalog.RootMode {
		updated, authorization, err := verifySourceTrust(ctx, source, client)
		if err != nil {
			return source, c, err
		}
		source = updated
		trust = &authorization
		// Remember revocations even if the accompanying catalog is stale,
		// unavailable or still signed by the newly revoked publisher.
		if err := writeJSON(s.sourcePath(name), source); err != nil {
			return source, c, err
		}
	}
	c, digest, err := verifiedSourceCatalog(ctx, source, client, trust)
	if err != nil {
		return source, c, err
	}
	source.Sequence, source.Digest = c.Sequence, digest
	return source, c, writeJSON(s.sourcePath(name), source)
}

func (c *CLI) sourceCommand(args []string) error {
	if len(args) > 0 && args[0] == "replace-root" {
		return c.replaceSourceRoot(args[1:])
	}
	if len(args) == 2 && args[0] == "verify" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		source, index, err := c.Store.fetchSourceCatalog(ctx, args[1], catalogClient())
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Verified source %s, catalog sequence %d, authorization version %d.\n", source.Name, index.Sequence, source.TrustVersion)
		return nil
	}
	if len(args) == 1 && args[0] == "list" {
		entries, err := os.ReadDir(filepath.Join(c.Store.Home, "sources"))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			source, err := c.Store.loadSource(strings.TrimSuffix(entry.Name(), ".json"))
			if err != nil {
				return err
			}
			key, _ := catalog.PublicKey([]byte(source.PublicKey))
			mode := source.TrustMode
			if mode == "" {
				mode = "direct-key"
			}
			fmt.Fprintf(c.Out, "%s  %s  key-sha256:%s  sequence:%d  trust:%s/%d\n", source.Name, source.Location, catalog.Fingerprint(key), source.Sequence, mode, source.TrustVersion)
		}
		return nil
	}
	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: phpm source add NAME --catalog LOCATION (--key FILE | --root FILE --fingerprint SHA256) | source list | source verify NAME | source replace-root NAME ...")
	}
	set := flag.NewFlagSet("source add", flag.ContinueOnError)
	location := set.String("catalog", "", "local catalog.json path or HTTPS URL")
	keyFile := set.String("key", "", "independently trusted Ed25519 public key file")
	rootFile := set.String("root", "", "offline root public key file")
	fingerprint := set.String("fingerprint", "", "independently checked root SHA-256 fingerprint")
	pos, err := parseFlags(set, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || !sourceName.MatchString(pos[0]) || (*keyFile == "") == (*rootFile == "") {
		return errors.New("source add requires NAME, --catalog and exactly one of --key or --root")
	}
	name := pos[0]
	locationValue, err := normalizeCatalogLocation(*location)
	if err != nil {
		return err
	}
	if *rootFile != "" {
		*keyFile = *rootFile
	}
	if strings.Contains(*keyFile, "://") {
		return errors.New("--key requires a local independently trusted public key file")
	}
	keyBytes, err := readSourceResource(context.Background(), nil, *keyFile, 1024)
	if err != nil {
		return err
	}
	key, err := catalog.PublicKey(keyBytes)
	if err != nil {
		return err
	}
	if *rootFile != "" && (!validSHA(*fingerprint) || catalog.Fingerprint(key) != *fingerprint) {
		return errors.New("--root requires the independently checked --fingerprint SHA-256")
	}
	if *rootFile == "" && *fingerprint != "" {
		return errors.New("--fingerprint is used with --root")
	}
	unlock, err := c.Store.lock("source:" + name)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(c.Store.sourcePath(name)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("source already exists or cannot be inspected; its pinned key/location was not replaced")
	}
	source := catalogSource{Schema: 1, Name: name, Location: locationValue, PublicKey: base64.StdEncoding.EncodeToString(key)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var trust *catalog.Trust
	client := catalogClient()
	if *rootFile != "" {
		source.TrustMode = catalog.RootMode
		updated, authorization, err := verifySourceTrust(ctx, source, client)
		if err != nil {
			return err
		}
		source = updated
		trust = &authorization
	}
	index, digest, err := verifiedSourceCatalog(ctx, source, client, trust)
	if err != nil {
		return err
	}
	source.Sequence, source.Digest = index.Sequence, digest
	if err := writeJSON(c.Store.sourcePath(name), source); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Trusted source %s (Ed25519 key SHA-256: %s), sequence %d.\n", name, catalog.Fingerprint(key), index.Sequence)
	return nil
}

func requireDebianHost(goos, arch string, release []byte, libc string) error {
	_, err := catalogHostTarget(goos, arch, release, libc)
	return err
}

func catalogHostTarget(goos, arch string, release []byte, libc string) (string, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(string(release), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			fields[k] = strings.Trim(v, `"'`)
		}
	}
	if goos == "linux" && arch == "amd64" && fields["ID"] == "debian" {
		switch fields["VERSION_ID"] + "/" + strings.TrimSpace(libc) {
		case "12/glibc 2.36":
			return catalog.Debian12Target, nil
		case "13/glibc 2.41":
			return catalog.Target, nil
		}
	}
	return "", errors.New("signed PHP packages require Debian 12 x86_64 (glibc 2.36) or Debian 13 x86_64 (glibc 2.41); OpenWrt/iStoreOS and other distributions are not supported")
}

func checkDebianHost() error {
	_, err := debianHostTarget()
	return err
}

func debianHostTarget() (string, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return catalogHostTarget(runtime.GOOS, runtime.GOARCH, nil, "")
	}
	release, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	libc, err := exec.CommandContext(ctx, "getconf", "GNU_LIBC_VERSION").Output()
	if err != nil {
		return "", fmt.Errorf("cannot check required glibc version: %w", err)
	}
	return catalogHostTarget(runtime.GOOS, runtime.GOARCH, release, string(libc))
}

func selectCatalogBuild(index catalog.Catalog, requested, target string) (catalog.Build, error) {
	if !versionPattern.MatchString(requested) {
		return catalog.Build{}, errors.New("expected PHP major.minor or major.minor.patch")
	}
	builds := append([]catalog.Build(nil), index.Builds...)
	sort.Slice(builds, func(i, j int) bool {
		n, _ := comparePHPVersions(builds[i].Version, builds[j].Version)
		if n == 0 {
			return builds[i].Revision > builds[j].Revision
		}
		return n > 0
	})
	for _, b := range builds {
		if b.Target == target && matchesVersion(b.Version, requested) {
			return b, nil
		}
	}
	return catalog.Build{}, fmt.Errorf("no signed catalog build for PHP %s on %s", requested, target)
}

func (s *Store) catalogPackage(ctx context.Context, source catalogSource, p catalog.Package, client *http.Client) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	unlock, err := s.lock("download:" + p.SHA256)
	if err != nil {
		return "", err
	}
	defer unlock()
	target := filepath.Join(s.Home, "cache", p.SHA256+".tar.gz")
	if info, err := os.Stat(target); err == nil {
		sum, err := fileSHA(target)
		if err != nil {
			return "", err
		}
		if info.Size() != p.Size || sum != p.SHA256 {
			return "", errors.New("cached catalog package hash/size mismatch")
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	location, err := sourceResource(source, p.Filename)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Join(s.Home, "cache"), ".catalog-*")
	if err != nil {
		return "", err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	h := sha256.New()
	var n int64
	writer := io.MultiWriter(f, h)
	if strings.HasPrefix(location, "https://") {
		noProgress := 0
		for n < p.Size {
			data, _, readErr, protocolErr := readPackageRange(ctx, client, location, n, p.Size)
			if protocolErr != nil {
				return "", protocolErr
			}
			if len(data) == 0 {
				noProgress++
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if noProgress >= 3 {
					return "", fmt.Errorf("catalog package transfer made no progress at %d/%d bytes: %w", n, p.Size, readErr)
				}
				continue
			}
			noProgress = 0
			written, err := writer.Write(data)
			if err != nil {
				return "", err
			}
			n += int64(written)
		}
	} else {
		reader, err := os.Open(location)
		if err != nil {
			return "", err
		}
		var copyErr error
		n, copyErr = io.Copy(writer, io.LimitReader(reader, p.Size+1))
		if err := errors.Join(copyErr, reader.Close()); err != nil {
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return "", errors.New("catalog package hash/size mismatch; nothing was installed")
	}
	if err := os.Rename(f.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

func (s *Store) installCatalogRuntime(ctx context.Context, sourceName, requested string, ts bool, progress io.Writer) (Runtime, error) {
	if ts {
		return Runtime{}, errors.New("the signed Debian catalog supplies NTS builds only")
	}
	target, err := debianHostTarget()
	if err != nil {
		return Runtime{}, err
	}
	client := catalogClient()
	source, index, err := s.fetchSourceCatalog(ctx, sourceName, client)
	if err != nil {
		return Runtime{}, err
	}
	b, err := selectCatalogBuild(index, requested, target)
	if err != nil {
		return Runtime{}, err
	}
	if progress != nil {
		fmt.Fprintf(progress, "Verified signed catalog %s sequence %d; acquiring %s\n", source.Name, index.Sequence, b.ID)
	}
	return s.importCatalogBuild(ctx, source, b, client)
}

func (s *Store) importCatalogBuild(ctx context.Context, source catalogSource, b catalog.Build, client *http.Client) (Runtime, error) {
	target, err := debianHostTarget()
	if err != nil {
		return Runtime{}, err
	}
	if b.Target != target {
		return Runtime{}, fmt.Errorf("catalog package target %s does not match host %s", b.Target, target)
	}
	archive, err := s.catalogPackage(ctx, source, b.Runtime, client)
	if err != nil {
		return Runtime{}, err
	}
	temp, err := os.MkdirTemp(filepath.Join(s.Home, "tmp"), "catalog-runtime-*")
	if err != nil {
		return Runtime{}, err
	}
	defer os.RemoveAll(temp)
	if err := extractTarGZIP(archive, temp); err != nil {
		return Runtime{}, err
	}
	sum, err := fileSHA(filepath.Join(temp, "bin", "php"))
	if err != nil || sum != b.PHPBinarySHA256 {
		return Runtime{}, errors.New("catalog PHP binary hash mismatch")
	}
	// Check identity in staging before registering any runtime in the store.
	info, err := probePHP(filepath.Join(temp, "bin", "php"))
	if err != nil {
		return Runtime{}, fmt.Errorf("PHP failed its dependency/identity probe; install the documented Debian runtime libraries: %w", err)
	}
	if info.Version != b.Version || info.API != b.PHPAPI || info.ZTS || info.Debug || info.IntSize != 8 {
		return Runtime{}, errors.New("catalog PHP ABI/version mismatch")
	}
	location, _ := sourceResource(source, b.Runtime.Filename)
	return s.importRuntime(temp, "bin/php", Source{Kind: "signed-catalog", URL: location, SHA256: b.Runtime.SHA256, Catalog: source.Name, Build: b.ID})
}

// Restore the exact locked payloads, never resolve a newer version. Project
// files remain read-only; normal frozen install rechecks the entire lock after
// hydration. Successfully verified immutable downloads may remain after failure.
func (s *Store) restoreCatalogLock(root, name string) error {
	if err := checkDebianHost(); err != nil {
		return err
	}
	unlock, err := s.lock("project:" + root)
	if err != nil {
		return err
	}
	defer unlock()
	if err := checkProjectTransaction(root); err != nil {
		return err
	}
	cfg, err := readConfig(root)
	if err != nil {
		return err
	}
	lock, err := loadLock(root)
	if err != nil {
		return err
	}
	env, ok := lock.Targets[platformKey()]
	if !ok || env.ID != environmentID(env) || env.ConfigHash != digestJSON(cfg) || !matchesVersion(env.Runtime.PHP.Version, cfg.PHP) || (cfg.Runtime != "" && cfg.Runtime != env.Runtime.ID) || digestJSON(env.INI) != digestJSON(cfg.INI) {
		return errors.New("cannot restore a stale or invalid platform lock")
	}
	if env.Runtime.Source.Kind != "signed-catalog" {
		return errors.New("locked runtime has no signed catalog provenance; use an explicit local import")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := catalogClient()
	source, index, err := s.fetchSourceCatalog(ctx, name, client)
	if err != nil {
		return err
	}
	var build catalog.Build
	for _, b := range index.Builds {
		if b.ID == env.Runtime.Source.Build {
			build = b
			break
		}
	}
	if build.ID == "" || build.Version != env.Runtime.PHP.Version || build.Runtime.SHA256 != env.Runtime.Source.SHA256 {
		return errors.New("exact locked runtime is absent from the signed catalog")
	}
	rt, err := s.importCatalogBuild(ctx, source, build, client)
	if err != nil {
		return err
	}
	if rt.ID != env.Runtime.ID || rt.Digest != env.Runtime.Digest {
		return errors.New("downloaded runtime does not match the exact locked payload")
	}
	provider := windowsExtensionProvider{Store: s}
	for _, ext := range env.Extensions {
		if ext.RuntimeID != rt.ID || cfg.Extensions[ext.Name] != ext.Version {
			return errors.New("invalid locked extension binding")
		}
		if ext.Source.Kind != "signed-catalog" && ext.Source.Kind != "runtime-bundled" {
			return fmt.Errorf("locked %s has no supported catalog/bundled provenance", ext.Name)
		}
		got, err := provider.acquire(ctx, rt, ExtensionRequest{Name: ext.Name, Version: ext.Version, Zend: ext.Zend})
		if err != nil {
			return err
		}
		if got.Artifact == nil || got.Artifact.ID != ext.ID || got.Artifact.Digest != ext.Digest {
			return fmt.Errorf("downloaded %s does not match the exact locked payload", ext.Name)
		}
	}
	return s.validateLocked(env, cfg)
}

func (p windowsExtensionProvider) acquireCatalogExtension(ctx context.Context, rt Runtime, request ExtensionRequest) (acquiredExtension, error) {
	target, err := debianHostTarget()
	if err != nil {
		return acquiredExtension{}, err
	}
	client := catalogClient()
	source, index, err := p.Store.fetchSourceCatalog(ctx, rt.Source.Catalog, client)
	if err != nil {
		return acquiredExtension{}, err
	}
	for _, b := range index.Builds {
		if b.ID != rt.Source.Build {
			continue
		}
		if b.Target != target || b.Version != rt.PHP.Version || b.PHPAPI != rt.PHP.API || b.Runtime.SHA256 != rt.Source.SHA256 {
			return acquiredExtension{}, errors.New("catalog runtime binding changed")
		}
		for _, e := range b.Extensions {
			if e.Name != request.Name || (request.Version != "" && request.Version != e.Version) {
				continue
			}
			if request.Zend && !e.Zend {
				return acquiredExtension{}, errors.New("catalog extension uses the ordinary loader")
			}
			if request.SHA256 != "" && !strings.EqualFold(request.SHA256, e.Package.SHA256) {
				return acquiredExtension{}, errors.New("catalog extension archive differs from --sha256")
			}
			if err := p.checkVersion(e.Name, e.Version); err != nil {
				return acquiredExtension{}, err
			}
			p.message("Acquiring signed %s@%s for %s\n", e.Name, e.Version, b.ID)
			archive, err := p.Store.catalogPackage(ctx, source, e.Package, client)
			if err != nil {
				return acquiredExtension{}, err
			}
			temp, err := os.MkdirTemp(filepath.Join(p.Store.Home, "tmp"), "catalog-extension-*")
			if err != nil {
				return acquiredExtension{}, err
			}
			defer os.RemoveAll(temp)
			if err := extractTarGZIP(archive, temp); err != nil {
				return acquiredExtension{}, err
			}
			location, _ := sourceResource(source, e.Package.Filename)
			ext, err := p.Store.importExtensionFromSource(rt, e.Name, e.Version, filepath.Join(temp, e.Module), temp, "", e.Zend, Source{Kind: "signed-catalog", URL: location, SHA256: e.Package.SHA256, Catalog: source.Name, Build: b.ID})
			return acquiredExtension{Version: e.Version, Artifact: &ext}, err
		}
		return acquiredExtension{}, fmt.Errorf("no signed %s%s package for this exact PHP build", request.Name, optionalVersion(request.Version))
	}
	return acquiredExtension{}, errors.New("runtime build is absent from its signed catalog; no provider fallback was attempted")
}
