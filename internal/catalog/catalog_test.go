package catalog

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(now time.Time) Catalog {
	p := func(n string) Package {
		return Package{Filename: n + ".tar.gz", SHA256: strings.Repeat("a", 64), Size: 1}
	}
	return Catalog{Schema: 1, Sequence: 1, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Builds: []Build{{ID: "php-fixture", Target: Target, Version: "8.4.27", Revision: 1, PHPBinarySHA256: strings.Repeat("b", 64), PHPAPI: "20240924", Runtime: p("runtime"), SDK: p("sdk"), Extensions: []Extension{{Name: "redis", Version: "6.3.0", Module: "redis.so", Package: p("redis")}}}}}
}

func TestVerifyRejectsTamperingWrongKeysAndAmbiguousJSON(t *testing.T) {
	now := time.Now()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	wrong, _, _ := ed25519.GenerateKey(rand.Reader)
	data, _ := json.Marshal(fixture(now))
	sign := func(b []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, b))) }
	if _, err := Verify(data, sign(data), pub, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(data, sign(data), wrong, now); err == nil {
		t.Fatal("accepted wrong signing key")
	}
	changed := append(append([]byte{}, data...), '\n')
	if _, err := Verify(changed, sign(data), pub, now); err == nil {
		t.Fatal("accepted modified signed bytes")
	}
	for _, bad := range []string{strings.Replace(string(data), `"schema":1`, `"schema":1,"schema":1`, 1), strings.Replace(string(data), `"schema":1`, `"schema":1,"unknown":true`, 1), string(data) + ` {}`} {
		if _, err := Verify([]byte(bad), sign([]byte(bad)), pub, now); err == nil {
			t.Fatal("accepted ambiguous/unknown/trailing JSON")
		}
	}
}

func TestCatalogRejectsExpiryUnsafeArtifactsAndBuildAmbiguity(t *testing.T) {
	now := time.Now()
	for name, mutate := range map[string]func(*Catalog){
		"expired":       func(c *Catalog) { c.ExpiresAt = now },
		"future":        func(c *Catalog) { c.IssuedAt = now.Add(time.Hour) },
		"too-long":      func(c *Catalog) { c.ExpiresAt = now.Add(91 * 24 * time.Hour) },
		"rollback-zero": func(c *Catalog) { c.Sequence = 0 },
		"escape":        func(c *Catalog) { c.Builds[0].Runtime.Filename = "../runtime.tar.gz" },
		"URL":           func(c *Catalog) { c.Builds[0].Runtime.Filename = "https://elsewhere/runtime.tar.gz" },
		"oversize":      func(c *Catalog) { c.Builds[0].Runtime.Size = 513 * 1024 * 1024 },
		"distro":        func(c *Catalog) { c.Builds[0].Target = "openwrt-x64" },
		"ambiguous":     func(c *Catalog) { b := c.Builds[0]; b.ID = "another-build"; c.Builds = append(c.Builds, b) },
		"module":        func(c *Catalog) { c.Builds[0].Extensions[0].Module = "../redis.so" },
	} {
		t.Run(name, func(t *testing.T) {
			c := fixture(now)
			mutate(&c)
			if err := c.Validate(now); err == nil {
				t.Fatal("accepted invalid catalog")
			}
		})
	}
}

func TestReleaseRequiresAllGatesAndLeavesNoPartialDestination(t *testing.T) {
	now := time.Now()
	input := t.TempDir()
	out := filepath.Join(t.TempDir(), "bundle")
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	// A valid-looking receipt cannot replace missing native verification reports.
	if err := os.WriteFile(filepath.Join(input, "build-result.json"), []byte(`{"schema":1,"build_id":"fixture","target":"debian13-x86_64-nts","artifacts":[{},{},{}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CreateBundle([]string{input}, out, key.Seed(), 1, now, time.Hour); err == nil {
		t.Fatal("signed an unverified build")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("partial release destination was exposed")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CreateBundle([]string{input}, out, key.Seed(), 1, now, time.Hour); err == nil {
		t.Fatal("overwrote existing release")
	}
}
