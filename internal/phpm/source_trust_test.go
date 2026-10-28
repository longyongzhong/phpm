package phpm

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"phpm/internal/catalog"
)

func trustFixture(t *testing.T, dir string, root, publisher ed25519.PrivateKey, version uint64, revoked []string) {
	t.Helper()
	_, data, sig, err := catalog.NewTrust(root.Seed(), []ed25519.PublicKey{publisher.Public().(ed25519.PublicKey)}, revoked, version, time.Now().Add(-time.Minute), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{"trust.json": data, "trust.json.sig": sig} {
		if err := os.WriteFile(filepath.Join(dir, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func delegatedFixture(t *testing.T, dir string, index catalog.Catalog, publisher ed25519.PrivateKey, version uint64) {
	t.Helper()
	data, _ := json.Marshal(index)
	sig, err := catalog.Sign(data, publisher.Seed(), catalog.RoleCatalog, version)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{"catalog.json": data, "catalog.json.sig": sig} {
		if err := os.WriteFile(filepath.Join(dir, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourceRecordsRevocationBeforeRejectingCatalogAndSupportsRotation(t *testing.T) {
	index, publisher := signedFixture(t)
	root, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	next, nextKey, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	rootFile := filepath.Join(dir, "root.pub")
	if err := os.WriteFile(rootFile, []byte(base64.StdEncoding.EncodeToString(root)), 0600); err != nil {
		t.Fatal(err)
	}
	trustFixture(t, dir, rootKey, publisher, 1, nil)
	delegatedFixture(t, dir, index, publisher, 1)
	s := testStore(t)
	cli := CLI{Store: s, Out: &bytes.Buffer{}}
	if err := cli.sourceCommand([]string{"add", "production", "--catalog", filepath.Join(dir, "catalog.json"), "--root", rootFile, "--fingerprint", catalog.Fingerprint(root)}); err != nil {
		t.Fatal(err)
	}
	trustFixture(t, dir, rootKey, nextKey, 2, []string{catalog.Fingerprint(publisher.Public().(ed25519.PublicKey))})
	// The catalog is still signed by the revoked key. Its failure must not
	// discard the already verified authorization/revocation update.
	if _, _, err := s.fetchSourceCatalog(context.Background(), "production", catalogClient()); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked catalog: %v", err)
	}
	stored, err := s.loadSource("production")
	if err != nil || stored.TrustVersion != 2 || len(stored.Revoked) != 1 {
		t.Fatalf("revocation lost: %+v %v", stored, err)
	}
	trustFixture(t, dir, rootKey, publisher, 1, nil)
	if _, _, err := s.fetchSourceCatalog(context.Background(), "production", catalogClient()); err == nil {
		t.Fatal("accepted old authorization after failed catalog")
	}
	trustFixture(t, dir, rootKey, nextKey, 3, nil)
	if _, _, err := s.fetchSourceCatalog(context.Background(), "production", catalogClient()); err == nil {
		t.Fatal("forgot previously observed revocation")
	}
	trustFixture(t, dir, rootKey, nextKey, 3, stored.Revoked)
	index.Sequence = 2
	delegatedFixture(t, dir, index, nextKey, 3)
	if _, _, err := s.fetchSourceCatalog(context.Background(), "production", catalogClient()); err != nil {
		t.Fatal(err)
	}
	if catalog.Fingerprint(next) == catalog.Fingerprint(root) {
		t.Fatal("invalid fixture")
	}
	newRoot, newRootKey, _ := ed25519.GenerateKey(rand.Reader)
	newFile := filepath.Join(dir, "new-root.pub")
	if err := os.WriteFile(newFile, []byte(base64.StdEncoding.EncodeToString(newRoot)), 0600); err != nil {
		t.Fatal(err)
	}
	trustFixture(t, dir, newRootKey, nextKey, 4, stored.Revoked)
	delegatedFixture(t, dir, index, nextKey, 4)
	if _, _, err := s.fetchSourceCatalog(context.Background(), "production", catalogClient()); err == nil {
		t.Fatal("automatically accepted replacement root")
	}
	if err := cli.sourceCommand([]string{"replace-root", "production", "--root", newFile, "--old-fingerprint", catalog.Fingerprint(root), "--new-fingerprint", catalog.Fingerprint(newRoot)}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.loadSource("production")
	if err != nil || updated.Sequence != 2 || updated.TrustVersion != 4 || len(updated.Revoked) != 1 {
		t.Fatalf("replacement reset rollback/revocation state: %+v %v", updated, err)
	}
}
