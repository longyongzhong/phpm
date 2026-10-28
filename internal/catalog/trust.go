package catalog

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const RootMode = "root-v1"
const RoleTrust = "trust"
const RoleCatalog = "catalog"
const RoleClients = "clients"

type SigningKey struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
}

type Trust struct {
	Schema     int          `json:"schema"`
	Version    uint64       `json:"version"`
	RootSHA256 string       `json:"root_sha256"`
	IssuedAt   time.Time    `json:"issued_at"`
	ExpiresAt  time.Time    `json:"expires_at"`
	Keys       []SigningKey `json:"keys"`
	Revoked    []string     `json:"revoked"`
}

type Signature struct {
	Schema       int    `json:"schema"`
	Role         string `json:"role"`
	KeyID        string `json:"key_id"`
	TrustVersion uint64 `json:"trust_version"`
	Signature    string `json:"signature"`
}

func signatureMessage(role, keyID string, version uint64, data []byte) []byte {
	return append([]byte("PHPM-SIGNATURE-v1\n"+role+"\n"+keyID+"\n"+strconv.FormatUint(version, 10)+"\n"), data...)
}

func Sign(data, seed []byte, role string, version uint64) ([]byte, error) {
	if len(seed) != ed25519.SeedSize || version == 0 || (role != RoleTrust && role != RoleCatalog && role != RoleClients) {
		return nil, errors.New("invalid signing role/key/trust version")
	}
	key := ed25519.NewKeyFromSeed(seed)
	id := Fingerprint(key.Public().(ed25519.PublicKey))
	sig := Signature{Schema: 1, Role: role, KeyID: id, TrustVersion: version, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, signatureMessage(role, id, version, data)))}
	result, err := json.MarshalIndent(sig, "", "  ")
	return append(result, '\n'), err
}

func verifySignature(data, encoded []byte, role string, key ed25519.PublicKey, version uint64) error {
	var sig Signature
	if len(key) != ed25519.PublicKeySize || len(data) > MaxSize || len(encoded) > 4096 {
		return errors.New("signed metadata exceeds size limit")
	}
	if err := Decode(encoded, &sig); err != nil {
		return err
	}
	if sig.Schema != 1 || sig.Role != role || sig.KeyID != Fingerprint(key) || sig.TrustVersion != version {
		return errors.New("signature role/key/trust-version mismatch")
	}
	value, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil || !ed25519.Verify(key, signatureMessage(role, sig.KeyID, version, data), value) {
		return errors.New("metadata signature verification failed")
	}
	return nil
}

func (t Trust) Validate(root ed25519.PublicKey, now time.Time) error {
	if len(root) != ed25519.PublicKeySize || t.Schema != 1 || t.Version == 0 || t.RootSHA256 != Fingerprint(root) || len(t.Keys) > 16 || len(t.Revoked) > 256 || (len(t.Keys) == 0 && len(t.Revoked) == 0) {
		return errors.New("invalid root authorization identity/keys")
	}
	if t.IssuedAt.IsZero() || t.IssuedAt.After(now.Add(5*time.Minute)) || !t.ExpiresAt.After(now) || !t.ExpiresAt.After(t.IssuedAt) || t.ExpiresAt.Sub(t.IssuedAt) > 365*24*time.Hour {
		return errors.New("root authorization expired or has invalid dates")
	}
	ids := map[string]bool{}
	for _, k := range t.Keys {
		pub, err := PublicKey([]byte(k.PublicKey))
		if err != nil {
			return err
		}
		if k.ID != Fingerprint(pub) || k.ID == t.RootSHA256 || ids[k.ID] {
			return errors.New("duplicate/invalid publishing key or root reused as publishing key")
		}
		ids[k.ID] = true
	}
	ids = map[string]bool{}
	for _, id := range t.Revoked {
		if !ValidSHA(id) || ids[id] || id == t.RootSHA256 {
			return errors.New("invalid/duplicate revoked publishing key")
		}
		ids[id] = true
	}
	return nil
}

func VerifyTrust(data, signature []byte, root ed25519.PublicKey, now time.Time) (Trust, error) {
	var t Trust
	if err := Decode(data, &t); err != nil {
		return t, err
	}
	if err := t.Validate(root, now); err != nil {
		return t, err
	}
	return t, verifySignature(data, signature, RoleTrust, root, t.Version)
}

func (t Trust) PublishingKey(id string) (ed25519.PublicKey, error) {
	for _, revoked := range t.Revoked {
		if revoked == id {
			return nil, errors.New("publishing key is revoked")
		}
	}
	for _, k := range t.Keys {
		if k.ID == id {
			return PublicKey([]byte(k.PublicKey))
		}
	}
	return nil, errors.New("publishing key is not authorized by the pinned root")
}

func (t Trust) VerifyDocument(data, signature []byte, role string, now time.Time) error {
	if !t.ExpiresAt.After(now) {
		return errors.New("root authorization expired")
	}
	if role != RoleCatalog && role != RoleClients {
		return errors.New("invalid delegated document role")
	}
	var sig Signature
	if len(signature) > 4096 {
		return errors.New("signature exceeds size limit")
	}
	if err := Decode(signature, &sig); err != nil {
		return err
	}
	key, err := t.PublishingKey(sig.KeyID)
	if err != nil {
		return err
	}
	return verifySignature(data, signature, role, key, t.Version)
}

func VerifyDelegatedCatalog(data, signature []byte, t Trust, now time.Time) (Catalog, error) {
	var c Catalog
	if err := t.VerifyDocument(data, signature, RoleCatalog, now); err != nil {
		return c, err
	}
	if err := Decode(data, &c); err != nil {
		return c, err
	}
	if err := c.Validate(now); err != nil {
		return c, err
	}
	if c.ExpiresAt.After(t.ExpiresAt) {
		return c, errors.New("catalog outlives root authorization")
	}
	return c, nil
}

func NewTrust(seed []byte, keys []ed25519.PublicKey, revoked []string, version uint64, now time.Time, validity time.Duration) (Trust, []byte, []byte, error) {
	var t Trust
	if len(seed) != ed25519.SeedSize || validity <= 0 || validity > 365*24*time.Hour {
		return t, nil, nil, errors.New("invalid root key or authorization validity")
	}
	root := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	t = Trust{Schema: 1, Version: version, RootSHA256: Fingerprint(root), IssuedAt: now.UTC().Truncate(time.Second), ExpiresAt: now.UTC().Truncate(time.Second).Add(validity), Keys: []SigningKey{}, Revoked: append([]string{}, revoked...)}
	for _, key := range keys {
		if len(key) != ed25519.PublicKeySize {
			return t, nil, nil, fmt.Errorf("invalid publishing public key")
		}
		t.Keys = append(t.Keys, SigningKey{ID: Fingerprint(key), PublicKey: base64.StdEncoding.EncodeToString(key)})
	}
	if err := t.Validate(root, now); err != nil {
		return t, nil, nil, err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return t, nil, nil, err
	}
	data = append(data, '\n')
	signature, err := Sign(data, seed, RoleTrust, t.Version)
	return t, data, signature, err
}
