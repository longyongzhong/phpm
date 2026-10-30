package phpm

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"phpm/internal/catalog"
)

func verifySourceTrust(ctx context.Context, source catalogSource, client *http.Client) (catalogSource, catalog.Trust, error) {
	var empty catalog.Trust
	root, err := catalog.PublicKey([]byte(source.PublicKey))
	if err != nil {
		return source, empty, err
	}
	location, err := sourceResource(source, "trust.json")
	if err != nil {
		return source, empty, err
	}
	data, err := readSourceResource(ctx, client, location, catalog.MaxSize)
	if err != nil {
		return source, empty, err
	}
	sig, err := readSourceResource(ctx, client, location+".sig", 4096)
	if err != nil {
		return source, empty, err
	}
	trust, err := catalog.VerifyTrust(data, sig, root, time.Now())
	if err != nil {
		return source, empty, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if trust.Version < source.TrustVersion || (trust.Version == source.TrustVersion && source.TrustDigest != "" && digest != source.TrustDigest) {
		return source, empty, errors.New("root authorization rollback or changed bytes at accepted version")
	}
	revoked := map[string]bool{}
	for _, id := range trust.Revoked {
		revoked[id] = true
	}
	for _, id := range source.Revoked {
		if !revoked[id] {
			return source, empty, errors.New("root authorization dropped previously recorded revocations")
		}
	}
	source.TrustVersion, source.TrustDigest, source.Revoked = trust.Version, digest, trust.Revoked
	return source, trust, nil
}

// Root replacement is an explicit out-of-band trust decision. It preserves both
// anti-rollback counters and the revocation history; no remote root is auto-pinned.
func (c *CLI) replaceSourceRoot(args []string) error {
	set := flag.NewFlagSet("source replace-root", flag.ContinueOnError)
	rootFile := set.String("root", "", "independently trusted replacement root public key")
	oldFingerprint := set.String("old-fingerprint", "", "expected current root SHA-256")
	newFingerprint := set.String("new-fingerprint", "", "independently verified new root SHA-256")
	pos, err := parseFlags(set, args)
	if err != nil {
		return err
	}
	if len(pos) == 1 && pos[0] == defaultSourceName {
		return errors.New("the built-in PHPM root cannot be replaced with this command; register a separately named source for an explicit root change")
	}
	if len(pos) != 1 || !sourceName.MatchString(pos[0]) || *rootFile == "" || !validSHA(*oldFingerprint) || !validSHA(*newFingerprint) || *oldFingerprint == *newFingerprint {
		return errors.New("source replace-root requires NAME, --root, --old-fingerprint and --new-fingerprint")
	}
	if strings.Contains(*rootFile, "://") {
		return errors.New("replacement root must be a local public key file")
	}
	data, err := readSourceResource(context.Background(), nil, *rootFile, 1024)
	if err != nil {
		return err
	}
	root, err := catalog.PublicKey(data)
	if err != nil {
		return err
	}
	if catalog.Fingerprint(root) != *newFingerprint {
		return errors.New("replacement root fingerprint mismatch")
	}
	unlock, err := c.Store.lock("source:" + pos[0])
	if err != nil {
		return err
	}
	defer unlock()
	source, err := c.Store.loadSource(pos[0])
	if err != nil {
		return err
	}
	oldRoot, _ := catalog.PublicKey([]byte(source.PublicKey))
	if source.TrustMode != catalog.RootMode || catalog.Fingerprint(oldRoot) != *oldFingerprint {
		return errors.New("current root/mode does not match the explicit replacement request")
	}
	source.PublicKey = base64.StdEncoding.EncodeToString(root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := catalogClient()
	updated, trust, err := verifySourceTrust(ctx, source, client)
	if err != nil {
		return err
	}
	if updated.TrustVersion <= source.TrustVersion {
		return errors.New("replacement root requires a higher authorization version")
	}
	index, digest, err := verifiedSourceCatalog(ctx, updated, client, &trust)
	if err != nil {
		return err
	}
	updated.Sequence, updated.Digest = index.Sequence, digest
	if err := writeJSON(c.Store.sourcePath(pos[0]), updated); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Replaced root for %s: %s -> %s; catalog sequence %d retained.\n", pos[0], *oldFingerprint, *newFingerprint, updated.Sequence)
	return nil
}
