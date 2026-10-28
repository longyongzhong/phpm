package catalog

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func TestRootDelegationRotationRevocationAndRoleIsolation(t *testing.T) {
	now := time.Now()
	root, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	next, nextKey, _ := ed25519.GenerateKey(rand.Reader)
	t1, data, sig, err := NewTrust(rootKey.Seed(), []ed25519.PublicKey{pub}, nil, 1, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTrust(data, sig, root, now); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(fixture(now))
	signed, err := Sign(payload, key.Seed(), RoleCatalog, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDelegatedCatalog(payload, signed, t1, now); err != nil {
		t.Fatal(err)
	}
	if err := t1.VerifyDocument(payload, signed, RoleClients, now); err == nil {
		t.Fatal("accepted a signature for another role")
	}
	if _, err := VerifyDelegatedCatalog(payload, append(signed, ' '), t1, now); err != nil {
		t.Fatal("valid signature-envelope whitespace rejected", err)
	}
	changed := append(append([]byte{}, payload...), ' ')
	if _, err := VerifyDelegatedCatalog(changed, signed, t1, now); err == nil {
		t.Fatal("accepted changed signed bytes")
	}
	t2, _, _, err := NewTrust(rootKey.Seed(), []ed25519.PublicKey{next}, []string{Fingerprint(pub)}, 2, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	oldSig, _ := Sign(payload, key.Seed(), RoleCatalog, 2)
	if _, err := VerifyDelegatedCatalog(payload, oldSig, t2, now); err == nil {
		t.Fatal("accepted revoked publisher")
	}
	nextSig, _ := Sign(payload, nextKey.Seed(), RoleCatalog, 2)
	if _, err := VerifyDelegatedCatalog(payload, nextSig, t2, now); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDelegatedCatalog(payload, signed, t2, now); err == nil {
		t.Fatal("accepted old authorization version")
	}
	if _, _, _, err := NewTrust(rootKey.Seed(), []ed25519.PublicKey{root}, nil, 1, now, time.Hour); err == nil {
		t.Fatal("root reused as publishing key")
	}
}

func TestRootTrustRejectsWrongRootExpiryAndUnsignedLegacyEnvelope(t *testing.T) {
	now := time.Now()
	root, key, _ := ed25519.GenerateKey(rand.Reader)
	pub, publisher, _ := ed25519.GenerateKey(rand.Reader)
	wrong, _, _ := ed25519.GenerateKey(rand.Reader)
	trust, data, sig, err := NewTrust(key.Seed(), []ed25519.PublicKey{pub}, nil, 1, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTrust(data, sig, wrong, now); err == nil {
		t.Fatal("accepted wrong root")
	}
	if _, err := VerifyTrust(data, sig, root, now.Add(2*time.Hour)); err == nil {
		t.Fatal("accepted expired authorization")
	}
	payload, _ := json.Marshal(fixture(now))
	legacy := ed25519.Sign(publisher, payload)
	if _, err := VerifyDelegatedCatalog(payload, legacy, trust, now); err == nil {
		t.Fatal("downgraded to a legacy signature")
	}
	if _, _, _, err := NewTrust(key.Seed(), nil, []string{Fingerprint(pub)}, 2, now, time.Hour); err != nil {
		t.Fatal("all-publishers revocation should be possible", err)
	}
}
