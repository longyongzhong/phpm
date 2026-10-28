package catalog

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Asset struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}
type Client struct {
	Platform string `json:"platform"`
	Package  Asset  `json:"package"`
}
type Clients struct {
	Schema    int       `json:"schema"`
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Clients   []Client  `json:"clients"`
	Files     []Asset   `json:"files"`
}

var clientVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9]+(?:[.-][A-Za-z0-9]+)*)?$`)
var gitCommit = regexp.MustCompile(`^[a-f0-9]{40}$`)

func (a Asset) Validate() error {
	if !Filename(a.Filename) || !ValidSHA(a.SHA256) || a.Size < 1 || a.Size > 512*1024*1024 {
		return errors.New("invalid release asset name/hash/size")
	}
	return nil
}

func fileBytes(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release file exceeds limit")
	}
	return data, nil
}

func ReadTrustDirectory(dir string, root ed25519.PublicKey, now time.Time) (Trust, []byte, []byte, error) {
	data, err := fileBytes(filepath.Join(dir, "trust.json"), MaxSize)
	if err != nil {
		return Trust{}, nil, nil, err
	}
	sig, err := fileBytes(filepath.Join(dir, "trust.json.sig"), 4096)
	if err != nil {
		return Trust{}, nil, nil, err
	}
	t, err := VerifyTrust(data, sig, root, now)
	return t, data, sig, err
}

func NewTrustDirectory(destination string, t Trust, data, sig []byte, root ed25519.PublicKey) error {
	if _, err := VerifyTrust(data, sig, root, time.Now()); err != nil {
		return err
	}
	return newDirectory(destination, func(stage string) error {
		for name, value := range map[string][]byte{"trust.json": data, "trust.json.sig": sig, "root-key.pub": []byte(base64.StdEncoding.EncodeToString(root) + "\n")} {
			if err := writeNew(filepath.Join(stage, name), value); err != nil {
				return err
			}
		}
		return nil
	})
}

func newDirectory(destination string, build func(string) error) error {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("destination must not already exist")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".phpm-distribution-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := build(stage); err != nil {
		return err
	}
	return os.Rename(stage, destination)
}

func CreateDelegatedBundle(inputs []string, destination string, seed []byte, sequence uint64, now time.Time, validity time.Duration, trustDir string, root ed25519.PublicKey) error {
	t, data, sig, err := ReadTrustDirectory(trustDir, root, now)
	if err != nil {
		return err
	}
	if len(seed) != ed25519.SeedSize {
		return errors.New("invalid publishing seed")
	}
	key := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if _, err := t.PublishingKey(Fingerprint(key)); err != nil {
		return err
	}
	if now.Add(validity).After(t.ExpiresAt) {
		return errors.New("catalog validity exceeds root authorization")
	}
	return newDirectory(destination, func(stage string) error {
		bundle := filepath.Join(stage, "bundle")
		if err := CreateBundle(inputs, bundle, seed, sequence, now, validity); err != nil {
			return err
		}
		catalogData, err := fileBytes(filepath.Join(bundle, "catalog.json"), MaxSize)
		if err != nil {
			return err
		}
		encoded, err := Sign(catalogData, seed, RoleCatalog, t.Version)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(bundle, "catalog.json.sig"), encoded, 0600); err != nil {
			return err
		}
		for name, value := range map[string][]byte{"trust.json": data, "trust.json.sig": sig, "root-key.pub": []byte(base64.StdEncoding.EncodeToString(root) + "\n")} {
			if err := writeNew(filepath.Join(bundle, name), value); err != nil {
				return err
			}
		}
		entries, err := os.ReadDir(bundle)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := os.Rename(filepath.Join(bundle, entry.Name()), filepath.Join(stage, entry.Name())); err != nil {
				return err
			}
		}
		return os.Remove(bundle)
	})
}

func assetFor(path string) (Asset, error) {
	var a Asset
	info, err := os.Lstat(path)
	if err != nil {
		return a, err
	}
	if !info.Mode().IsRegular() {
		return a, errors.New("release assets must be regular files")
	}
	f, err := os.Open(path)
	if err != nil {
		return a, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, 512*1024*1024+1))
	if err != nil {
		return a, err
	}
	a = Asset{Filename: filepath.Base(path), SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
	return a, a.Validate()
}

func copyAsset(source, directory string, a Asset) error {
	if err := a.Validate(); err != nil {
		return err
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.OpenFile(filepath.Join(directory, a.Filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(f, a.Size+1))
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("release asset changed while copying")
	}
	return nil
}

func verifyBundle(dir string, root ed25519.PublicKey, now time.Time) (Catalog, Trust, map[string]bool, error) {
	var empty Catalog
	t, _, _, err := ReadTrustDirectory(dir, root, now)
	if err != nil {
		return empty, t, nil, err
	}
	data, err := fileBytes(filepath.Join(dir, "catalog.json"), MaxSize)
	if err != nil {
		return empty, t, nil, err
	}
	sig, err := fileBytes(filepath.Join(dir, "catalog.json.sig"), 4096)
	if err != nil {
		return empty, t, nil, err
	}
	c, err := VerifyDelegatedCatalog(data, sig, t, now)
	if err != nil {
		return c, t, nil, err
	}
	allowed := map[string]bool{"catalog.json": true, "catalog.json.sig": true, "trust.json": true, "trust.json.sig": true, "root-key.pub": true, "signing-key.pub": true}
	rootData, err := fileBytes(filepath.Join(dir, "root-key.pub"), 1024)
	if err != nil {
		return c, t, nil, err
	}
	pinned, err := PublicKey(rootData)
	if err != nil || Fingerprint(pinned) != Fingerprint(root) {
		return c, t, nil, errors.New("bundle root public key mismatch")
	}
	for _, b := range c.Builds {
		packages := []Package{b.Runtime, b.SDK}
		for _, e := range b.Extensions {
			packages = append(packages, e.Package)
		}
		for _, p := range packages {
			a, err := assetFor(filepath.Join(dir, p.Filename))
			if err != nil {
				return c, t, nil, err
			}
			if a.SHA256 != p.SHA256 || a.Size != p.Size {
				return c, t, nil, errors.New("catalog package hash/size mismatch")
			}
			allowed[p.Filename] = true
		}
		for _, suffix := range []string{"-build-result.json", "-sdk-verification.json", "-runtime-verification.json"} {
			allowed[b.ID+suffix] = true
		}
	}
	return c, t, allowed, nil
}

func packClient(path, platform, binary, notices string) error {
	files := []struct{ name, path string }{{"LICENSE", filepath.Join(notices, "LICENSE")}, {"THIRD_PARTY_NOTICES.md", filepath.Join(notices, "THIRD_PARTY_NOTICES.md")}, {"third_party/go/LICENSE", filepath.Join(notices, "third_party/go/LICENSE")}, {"third_party/yaml.v3/LICENSE", filepath.Join(notices, "third_party/yaml.v3/LICENSE")}}
	files = append(files, struct{ name, path string }{"third_party/yaml.v3/Apache-2.0.txt", filepath.Join(notices, "third_party/yaml.v3/Apache-2.0.txt")})
	name := "phpm"
	if platform == "windows-amd64" {
		name = "phpm.exe"
	}
	files = append([]struct{ name, path string }{{name, binary}}, files...)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	var tw *tar.Writer
	var gz *gzip.Writer
	var zw *zip.Writer
	if platform == "windows-amd64" {
		zw = zip.NewWriter(f)
	} else {
		gz = gzip.NewWriter(f)
		tw = tar.NewWriter(gz)
	}
	for _, item := range files {
		info, err := os.Lstat(item.path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 64*1024*1024 {
			return errors.New("invalid client binary/notice file")
		}
		data, err := fileBytes(item.path, 64*1024*1024)
		if err != nil {
			return err
		}
		if tw != nil {
			mode := int64(0644)
			if item.name == name {
				mode = 0755
			}
			if err := tw.WriteHeader(&tar.Header{Name: item.name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
		} else {
			w, err := zw.Create(item.name)
			if err != nil {
				return err
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
		}
	}
	if tw != nil {
		return errors.Join(tw.Close(), gz.Close(), f.Close())
	}
	return errors.Join(zw.Close(), f.Close())
}

func (m Clients) Validate(now time.Time) error {
	if m.Schema != 1 || !clientVersion.MatchString(m.Version) || len(m.Version) > 64 || !gitCommit.MatchString(m.Commit) || len(m.Clients) < 1 || len(m.Clients) > 2 || len(m.Files) < 1 || len(m.Files) > 4000 {
		return errors.New("invalid client release identity/files")
	}
	if m.IssuedAt.IsZero() || m.IssuedAt.After(now.Add(5*time.Minute)) || !m.ExpiresAt.After(now) || !m.ExpiresAt.After(m.IssuedAt) || m.ExpiresAt.Sub(m.IssuedAt) > 90*24*time.Hour {
		return errors.New("client release expired or has invalid dates")
	}
	files := map[string]Asset{}
	for _, a := range m.Files {
		if err := a.Validate(); err != nil {
			return err
		}
		if _, ok := files[a.Filename]; ok {
			return errors.New("duplicate release asset")
		}
		files[a.Filename] = a
	}
	platforms := map[string]bool{}
	for _, c := range m.Clients {
		suffix := ".tar.gz"
		if c.Platform == "windows-amd64" {
			suffix = ".zip"
		} else if c.Platform != "linux-amd64" {
			return errors.New("unsupported client platform")
		}
		if err := c.Package.Validate(); err != nil {
			return err
		}
		if platforms[c.Platform] || !strings.HasSuffix(c.Package.Filename, suffix) || files[c.Package.Filename] != c.Package {
			return errors.New("invalid client platform/package binding")
		}
		platforms[c.Platform] = true
	}
	return nil
}

func CreateClients(bundle, destination string, root ed25519.PublicKey, seed []byte, version, commit, linux, windows, notices, installer string, now time.Time, validity time.Duration) error {
	_, trust, allowed, err := verifyBundle(bundle, root, now)
	if err != nil {
		return err
	}
	if len(seed) != ed25519.SeedSize {
		return errors.New("invalid publishing key")
	}
	if _, err := trust.PublishingKey(Fingerprint(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))); err != nil {
		return err
	}
	if !clientVersion.MatchString(version) || !gitCommit.MatchString(commit) || now.Add(validity).After(trust.ExpiresAt) {
		return errors.New("invalid client version/commit/authorization validity")
	}
	return newDirectory(destination, func(stage string) error {
		entries, err := os.ReadDir(bundle)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !allowed[entry.Name()] {
				return fmt.Errorf("unexpected file in bundle: %s", entry.Name())
			}
			a, err := assetFor(filepath.Join(bundle, entry.Name()))
			if err != nil {
				return err
			}
			if err := copyAsset(filepath.Join(bundle, entry.Name()), stage, a); err != nil {
				return err
			}
		}
		for name := range allowed {
			if _, err := os.Stat(filepath.Join(stage, name)); err != nil {
				return err
			}
		}
		m := Clients{Schema: 1, Version: version, Commit: commit, IssuedAt: now.UTC().Truncate(time.Second), ExpiresAt: now.UTC().Truncate(time.Second).Add(validity)}
		for _, item := range []struct{ platform, binary, suffix string }{{"linux-amd64", linux, ".tar.gz"}, {"windows-amd64", windows, ".zip"}} {
			if item.binary == "" {
				continue
			}
			filename := "phpm-" + version + "-" + item.platform + item.suffix
			if err := packClient(filepath.Join(stage, filename), item.platform, item.binary, notices); err != nil {
				return err
			}
			a, err := assetFor(filepath.Join(stage, filename))
			if err != nil {
				return err
			}
			m.Clients = append(m.Clients, Client{Platform: item.platform, Package: a})
		}
		installerAsset, err := assetFor(installer)
		if err != nil {
			return err
		}
		installerAsset.Filename = "install-phpm.py"
		if err := copyAsset(installer, stage, installerAsset); err != nil {
			return err
		}
		entries, err = os.ReadDir(stage)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			a, err := assetFor(filepath.Join(stage, entry.Name()))
			if err != nil {
				return err
			}
			m.Files = append(m.Files, a)
		}
		sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Filename < m.Files[j].Filename })
		if err := m.Validate(now); err != nil {
			return err
		}
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		sig, err := Sign(data, seed, RoleClients, trust.Version)
		if err != nil {
			return err
		}
		if err := writeNew(filepath.Join(stage, "clients.json"), data); err != nil {
			return err
		}
		return writeNew(filepath.Join(stage, "clients.json.sig"), sig)
	})
}

func VerifyRelease(dir string, root ed25519.PublicKey, now time.Time) (Clients, error) {
	var m Clients
	_, trust, allowed, err := verifyBundle(dir, root, now)
	if err != nil {
		return m, err
	}
	data, err := fileBytes(filepath.Join(dir, "clients.json"), MaxSize)
	if err != nil {
		return m, err
	}
	sig, err := fileBytes(filepath.Join(dir, "clients.json.sig"), 4096)
	if err != nil {
		return m, err
	}
	if err := trust.VerifyDocument(data, sig, RoleClients, now); err != nil {
		return m, err
	}
	if err := Decode(data, &m); err != nil {
		return m, err
	}
	if err := m.Validate(now); err != nil {
		return m, err
	}
	if m.ExpiresAt.After(trust.ExpiresAt) {
		return m, errors.New("client release outlives authorization")
	}
	allowed["install-phpm.py"] = true
	for _, c := range m.Clients {
		allowed[c.Package.Filename] = true
	}
	if len(allowed) != len(m.Files) {
		return m, errors.New("signed release is missing or adds unexpected files")
	}
	for _, a := range m.Files {
		if !allowed[a.Filename] {
			return m, errors.New("unexpected signed release file")
		}
		actual, err := assetFor(filepath.Join(dir, a.Filename))
		if err != nil {
			return m, err
		}
		if actual != a {
			return m, fmt.Errorf("release asset hash/size mismatch: %s", a.Filename)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m, err
	}
	if len(entries) != len(m.Files)+2 {
		return m, errors.New("release directory contains missing or unlisted files")
	}
	return m, nil
}
