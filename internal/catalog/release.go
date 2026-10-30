package catalog

import (
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
	"sort"
	"time"
)

var SDKChecks = []string{"relocated-prefix", "relocated-headers", "php-identity", "phpize", "redis-rebuild", "redis-actual-loading"}
var RuntimeChecks = []string{"archive-integrity", "non-root-relocated-cli", "php-identity", "runtime-library-resolution", "phpm-import", "bundled-extension-install", "separate-redis-import", "frozen-install", "pdo-sqlite", "localhost-dns", "two-project-extension-and-ini-isolation", "redis-server-roundtrip", "curl-https-with-ca-verification", "openssl-https-with-ca-verification", "composer-platform-check", "non-root-fpm-request-with-project-extensions", "same-version-payload-activation-rollback"}

type buildReport struct {
	Schema          int               `json:"schema"`
	ID              string            `json:"build_id"`
	Target          string            `json:"target"`
	Version         string            `json:"php_version"`
	PHPBinarySHA256 string            `json:"php_binary_sha256"`
	PHPAPI          map[string]string `json:"php_api"`
	Recipe          struct {
		Revision int `json:"build_revision"`
		Redis    struct {
			Version string `json:"version"`
		} `json:"redis"`
		Xlswriter struct {
			Version string `json:"version"`
		} `json:"xlswriter"`
	} `json:"recipe"`
	Artifacts []struct {
		Kind string `json:"kind"`
		Package
	} `json:"artifacts"`
}

func readReport(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return err
	}
	if len(b) > MaxSize {
		return errors.New("build report exceeds size limit")
	}
	// Build receipts contain additional compiler/input data, preserved alongside
	// the signed catalog. Only the catalog's schema uses unknown-field rejection.
	return json.Unmarshal(b, value)
}

func verifyReport(path, id string, required []string) error {
	var report struct {
		Schema int      `json:"schema"`
		ID     string   `json:"build_id"`
		Checks []string `json:"checks"`
	}
	if err := readReport(path, &report); err != nil {
		return err
	}
	if report.Schema != 1 || report.ID != id {
		return errors.New("verification report identity mismatch")
	}
	seen := map[string]bool{}
	for _, c := range report.Checks {
		if seen[c] {
			return errors.New("duplicate verification check")
		}
		seen[c] = true
	}
	for _, c := range required {
		if !seen[c] {
			return fmt.Errorf("verification report is missing %s", c)
		}
	}
	return nil
}

func copyVerifiedPackage(input, output string, p Package) error {
	if err := p.Validate(); err != nil {
		return err
	}
	source := filepath.Join(input, p.Filename)
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != p.Size {
		return errors.New("release package size/type mismatch")
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	dest, err := os.OpenFile(filepath.Join(output, p.Filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(dest, h), io.LimitReader(f, p.Size+1))
	if err := errors.Join(copyErr, dest.Close()); err != nil {
		return err
	}
	if n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return errors.New("release package hash mismatch")
	}
	return nil
}

// CreateBundle stages everything in a new sibling directory; a failure never
// exposes a partial catalog or overwrites an existing release. Signing keys are
// kept outside the result. The caller must independently trust build provenance.
func CreateBundle(inputs []string, destination string, seed []byte, sequence uint64, now time.Time, validity time.Duration) error {
	if len(seed) != ed25519.SeedSize || len(inputs) == 0 {
		return errors.New("release needs an Ed25519 seed and build directories")
	}
	if validity <= 0 || validity > 90*24*time.Hour {
		return errors.New("catalog validity must be between zero and 90 days")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("release destination must not already exist")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".phpm-release-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	c := Catalog{Schema: 1, Sequence: sequence, IssuedAt: now.UTC().Truncate(time.Second), ExpiresAt: now.UTC().Truncate(time.Second).Add(validity)}
	for _, input := range inputs {
		var report buildReport
		if err := readReport(filepath.Join(input, "build-result.json"), &report); err != nil {
			return err
		}
		expectedArtifacts := 3
		if report.Recipe.Xlswriter.Version != "" {
			expectedArtifacts++
		}
		if report.Schema != 1 || !SupportedTarget(report.Target) || !Filename(report.ID) || len(report.Artifacts) != expectedArtifacts {
			return errors.New("unsupported build report")
		}
		if err := verifyReport(filepath.Join(input, "sdk-verification.json"), report.ID, SDKChecks); err != nil {
			return err
		}
		if err := verifyReport(filepath.Join(input, "verification", "runtime-verification.json"), report.ID, RuntimeChecks); err != nil {
			return err
		}
		if report.Recipe.Xlswriter.Version != "" {
			if err := verifyReport(filepath.Join(input, "sdk-verification.json"), report.ID, []string{"xlswriter-rebuild", "xlswriter-actual-loading"}); err != nil {
				return err
			}
			if err := verifyReport(filepath.Join(input, "verification", "runtime-verification.json"), report.ID, []string{"separate-xlswriter-import", "xlswriter-write-read-roundtrip", "xlswriter-fpm-loading"}); err != nil {
				return err
			}
		}
		b := Build{ID: report.ID, Target: report.Target, Version: report.Version, Revision: report.Recipe.Revision, PHPBinarySHA256: report.PHPBinarySHA256, PHPAPI: report.PHPAPI["PHP API"]}
		kinds := map[string]bool{}
		for _, p := range report.Artifacts {
			if kinds[p.Kind] {
				return errors.New("duplicate build artifact kind")
			}
			kinds[p.Kind] = true
			switch p.Kind {
			case "runtime":
				b.Runtime = p.Package
			case "sdk":
				b.SDK = p.Package
			case "redis":
				b.Extensions = append(b.Extensions, Extension{Name: "redis", Version: report.Recipe.Redis.Version, Module: "redis.so", Package: p.Package})
			case "xlswriter":
				b.Extensions = append(b.Extensions, Extension{Name: "xlswriter", Version: report.Recipe.Xlswriter.Version, Module: "xlswriter.so", Package: p.Package})
			default:
				return errors.New("unexpected build artifact kind")
			}
			if err := copyVerifiedPackage(input, stage, p.Package); err != nil {
				return err
			}
		}
		for _, p := range []struct{ from, to string }{{"build-result.json", report.ID + "-build-result.json"}, {"sdk-verification.json", report.ID + "-sdk-verification.json"}, {"verification/runtime-verification.json", report.ID + "-runtime-verification.json"}} {
			data, err := os.ReadFile(filepath.Join(input, filepath.FromSlash(p.from)))
			if err != nil {
				return err
			}
			if err := writeNew(filepath.Join(stage, p.to), data); err != nil {
				return err
			}
		}
		c.Builds = append(c.Builds, b)
	}
	if err := c.Validate(now); err != nil {
		return err
	}
	sort.Slice(c.Builds, func(i, j int) bool { return c.Builds[i].ID < c.Builds[j].ID })
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxSize {
		return errors.New("release catalog exceeds size limit")
	}
	key := ed25519.NewKeyFromSeed(seed)
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(key, data)) + "\n"
	for _, item := range []struct {
		name string
		data []byte
	}{{"catalog.json", data}, {"catalog.json.sig", []byte(signature)}, {"signing-key.pub", []byte(base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)) + "\n")}} {
		if err := writeNew(filepath.Join(stage, item.name), item.data); err != nil {
			return err
		}
	}
	return os.Rename(stage, destination)
}

func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}
