// Package catalog defines the signed, versioned PHPM distribution index.
package catalog

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const Target = "debian13-x86_64-nts"
const Debian12Target = "debian12-x86_64-nts"
const MaxSize = 4 * 1024 * 1024

func SupportedTarget(target string) bool {
	return target == Target || target == Debian12Target
}

type Package struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

type Extension struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Module  string  `json:"module"`
	Zend    bool    `json:"zend"`
	Package Package `json:"package"`
}

type Build struct {
	ID              string      `json:"id"`
	Target          string      `json:"target"`
	Version         string      `json:"version"`
	Revision        int         `json:"revision"`
	PHPBinarySHA256 string      `json:"php_binary_sha256"`
	PHPAPI          string      `json:"php_api"`
	Runtime         Package     `json:"runtime"`
	SDK             Package     `json:"sdk"`
	Extensions      []Extension `json:"extensions"`
}

type Catalog struct {
	Schema    int       `json:"schema"`
	Sequence  uint64    `json:"sequence"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Builds    []Build   `json:"builds"`
}

var Version = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var filename = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,199}$`)
var name = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var api = regexp.MustCompile(`^[0-9]{8}$`)

func ValidSHA(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

func Filename(s string) bool { return filename.MatchString(s) && !strings.Contains(s, "..") }

func (p Package) Validate() error {
	if !Filename(p.Filename) || !strings.HasSuffix(p.Filename, ".tar.gz") || !ValidSHA(p.SHA256) || p.Size < 1 || p.Size > 512*1024*1024 {
		return errors.New("invalid catalog package name/hash/size")
	}
	return nil
}

func (c Catalog) Validate(now time.Time) error {
	if c.Schema != 1 || c.Sequence == 0 || len(c.Builds) == 0 || len(c.Builds) > 500 {
		return errors.New("unsupported or empty signed catalog")
	}
	if c.IssuedAt.IsZero() || c.IssuedAt.After(now.Add(5*time.Minute)) || !c.ExpiresAt.After(now) || !c.ExpiresAt.After(c.IssuedAt) || c.ExpiresAt.Sub(c.IssuedAt) > 90*24*time.Hour {
		return errors.New("catalog is expired or has invalid validity dates (maximum 90 days)")
	}
	ids, revisions, files := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, b := range c.Builds {
		key := fmt.Sprintf("%s/%s/%d", b.Target, b.Version, b.Revision)
		if !Filename(b.ID) || !Version.MatchString(b.Version) || !SupportedTarget(b.Target) || b.Revision < 1 || !ValidSHA(b.PHPBinarySHA256) || !api.MatchString(b.PHPAPI) || ids[b.ID] || revisions[key] {
			return errors.New("invalid or ambiguous catalog build identity/target/revision")
		}
		ids[b.ID], revisions[key] = true, true
		packages := []Package{b.Runtime, b.SDK}
		names := map[string]bool{}
		if len(b.Extensions) > 100 {
			return errors.New("too many catalog extensions")
		}
		for _, e := range b.Extensions {
			if !name.MatchString(e.Name) || !Version.MatchString(e.Version) || e.Module != e.Name+".so" || names[e.Name] {
				return errors.New("invalid or duplicate catalog extension")
			}
			names[e.Name] = true
			packages = append(packages, e.Package)
		}
		for _, p := range packages {
			if err := p.Validate(); err != nil {
				return err
			}
			if files[p.Filename] {
				return errors.New("duplicate catalog package filename")
			}
			files[p.Filename] = true
		}
	}
	return nil
}

// Decode rejects ambiguous duplicate object keys as well as unknown schema fields.
func Decode(data []byte, value any) error {
	if len(data) > MaxSize {
		return errors.New("catalog JSON exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var scan func(int) error
	scan = func(depth int) error {
		if depth > 32 {
			return errors.New("JSON nesting exceeds limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if t == json.Delim('{') {
			keys := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || keys[key] {
					return errors.New("duplicate JSON object key")
				}
				keys[key] = true
				if err := scan(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		if t == json.Delim('[') {
			for d.More() {
				if err := scan(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := scan(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

func PublicKey(data []byte) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("expected a base64 Ed25519 public key (32 bytes)")
	}
	return ed25519.PublicKey(b), nil
}

func Fingerprint(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])
}

func Verify(data, signature []byte, key ed25519.PublicKey, now time.Time) (Catalog, error) {
	var c Catalog
	if len(data) > MaxSize {
		return c, errors.New("catalog exceeds size limit")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, data, sig) {
		return c, errors.New("catalog signature verification failed")
	}
	if err := Decode(data, &c); err != nil {
		return c, err
	}
	return c, c.Validate(now)
}
