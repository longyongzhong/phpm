package phpm

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"phpm/internal/catalog"
)

func TestDefaultAndExplicitProviderSelection(t *testing.T) {
	for _, tc := range []struct {
		os, arch, source, provider, want string
		static, invalid                  bool
	}{
		{"linux", "amd64", "", "", defaultSourceName, false, false},
		{"windows", "amd64", "", "", "", false, false},
		{"linux", "amd64", "", "staticphp", "", true, false},
		{"linux", "amd64", "custom", "", "custom", false, false},
		{"linux", "amd64", "custom", "staticphp", "", false, true},
		{"linux", "amd64", "", "unknown", "", false, true},
		{"windows", "amd64", "", "staticphp", "", false, true},
		{"linux", "arm64", "", "", "", false, true},
		{"darwin", "arm64", "", "", "", false, true},
	} {
		name, static, err := sourceSelection(tc.os, tc.arch, tc.source, tc.provider)
		if (err != nil) != tc.invalid || name != tc.want || static != tc.static {
			t.Fatalf("selection %+v: %s %v %v", tc, name, static, err)
		}
	}
}

func TestUpgradeKeepsExistingProviderUnlessExplicitlySelected(t *testing.T) {
	for _, tc := range []struct {
		kind, current, source, provider, wantSource, wantProvider string
	}{
		{"signed-catalog", "custom", "", "", "custom", ""},
		{"signed-catalog", defaultSourceName, "", "", defaultSourceName, ""},
		{"staticphp-linux", "", "", "", "", "staticphp"},
		{"local-import", "", "", "", "", ""},
		{"staticphp-linux", "", defaultSourceName, "", defaultSourceName, ""},
		{"signed-catalog", "custom", "", "staticphp", "", "staticphp"},
	} {
		source, provider := upgradeSource(Runtime{Source: Source{Kind: tc.kind, Catalog: tc.current}}, runtimeUpgradeRequest{Source: tc.source, Provider: tc.provider})
		if source != tc.wantSource || provider != tc.wantProvider {
			t.Fatalf("upgrade changed provider: %+v -> %s/%s", tc, source, provider)
		}
	}
}

func TestBuiltinRootMatchesReviewedReleaseTrust(t *testing.T) {
	data, err := os.ReadFile("../../release/trust/root-key.pub")
	if err != nil {
		t.Fatal(err)
	}
	key, err := catalog.PublicKey(data)
	if err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodeToString(key) != defaultRootPublicKey || catalog.Fingerprint(key) != defaultRootFingerprint {
		t.Fatal("built-in root differs from the reviewed public release root")
	}
}

func TestBuiltinBootstrapAuthenticatesBeforeRegistrationAndRetainsHistory(t *testing.T) {
	index, publisher := signedFixture(t)
	index.Sequence = 10
	pub, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	trustFixture(t, dir, rootKey, publisher, 1, nil)
	delegatedFixture(t, dir, index, publisher, 1)
	expected := catalogSource{Schema: 1, Name: "builtin-test", Location: filepath.Join(dir, "catalog.json"),
		PublicKey: base64.StdEncoding.EncodeToString(pub), TrustMode: catalog.RootMode, Sequence: 10}
	s := testStore(t)
	fetch := func() error {
		_, _, err := s.fetchCatalog(context.Background(), expected.Name, catalogClient(), &expected)
		return err
	}
	// A downloaded wrong signature cannot establish even the first source.
	sigPath := filepath.Join(dir, "catalog.json.sig")
	goodSig, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := fetch(); err == nil {
		t.Fatal("bootstrapped unsigned data")
	}
	if _, err := os.Stat(s.sourcePath(expected.Name)); !os.IsNotExist(err) {
		t.Fatal("persisted an unverified initial source")
	}
	if err := os.WriteFile(sigPath, goodSig, 0600); err != nil {
		t.Fatal(err)
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	stored, err := s.loadSource(expected.Name)
	if err != nil || stored.Sequence != 10 || stored.TrustVersion != 1 {
		t.Fatalf("bootstrap state: %+v %v", stored, err)
	}
	// A newer client's catalog floor must not erase the existing accepted digest
	// when the server still supplies the previous, correctly signed catalog.
	expected.Sequence = 11
	if err := fetch(); err == nil || !strings.Contains(err.Error(), "minimum sequence") {
		t.Fatalf("minimum sequence: %v", err)
	}
	still, err := s.loadSource(expected.Name)
	if err != nil || still.Sequence != stored.Sequence || still.Digest != stored.Digest {
		t.Fatalf("destroyed source history: %+v %v", still, err)
	}
	index.Sequence = 11
	delegatedFixture(t, dir, index, publisher, 1)
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	index.Sequence = 10
	delegatedFixture(t, dir, index, publisher, 1)
	if err := fetch(); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("accepted replay: %v", err)
	}
	// Even a well-formed user-created source cannot shadow the built-in key.
	stored, err = s.loadSource(expected.Name)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	stored.PublicKey = base64.StdEncoding.EncodeToString(other)
	if err := writeJSON(s.sourcePath(expected.Name), stored); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.sourcePath(expected.Name))
	if err != nil {
		t.Fatal(err)
	}
	if err := fetch(); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("accepted shadowed root: %v", err)
	}
	after, err := os.ReadFile(s.sourcePath(expected.Name))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("overwrote conflicting source")
	}
}

func TestBuiltinNameAndInvalidProviderFlagsFailBeforeNetwork(t *testing.T) {
	c := CLI{Store: testStore(t), Out: &bytes.Buffer{}}
	if err := c.sourceCommand([]string{"add", defaultSourceName, "--catalog", defaultCatalogURL, "--key", "not-read.pub"}); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("built-in source was replaceable: %v", err)
	}
	for _, args := range [][]string{{"install", "8.4", "--source", "other", "--provider", "staticphp"}, {"available", "--provider", "unknown"}} {
		if err := c.php(args); err == nil {
			t.Fatalf("accepted conflicting provider args: %v", args)
		}
	}
	if err := c.preparePHPUpgrade([]string{"8.5", "--runtime", "fixture", "--provider", "staticphp"}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("accepted conflicting upgrade args: %v", err)
	}
}
