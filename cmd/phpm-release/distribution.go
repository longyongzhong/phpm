package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"phpm/internal/catalog"
)

type values []string

func (v *values) String() string         { return strings.Join(*v, ",") }
func (v *values) Set(value string) error { *v = append(*v, value); return nil }

func smallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return nil, err
	}
	if len(data) > 1024 {
		return nil, errors.New("key file exceeds limit")
	}
	return data, nil
}

func readPublic(path string) (ed25519.PublicKey, error) {
	data, err := smallFile(path)
	if err != nil {
		return nil, err
	}
	return catalog.PublicKey(data)
}
func readSeed(path string) ([]byte, error) {
	data, err := smallFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("invalid private seed file")
	}
	return seed, nil
}

func trustCommand(args []string) error {
	set := flag.NewFlagSet("trust", flag.ContinueOnError)
	rootFile := set.String("root-key", "", "offline root private seed file")
	version := set.Uint64("version", 0, "monotonically increasing authorization version")
	output := set.String("output", "", "new authorization directory")
	validity := set.Duration("validity", 90*24*time.Hour, "authorization lifetime, maximum 8760h")
	var keys, revoked values
	set.Var(&keys, "key", "publishing public key file (repeatable)")
	set.Var(&revoked, "revoke", "permanently revoked publishing key SHA-256 (repeatable)")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *rootFile == "" || *output == "" || set.NArg() != 0 {
		return errors.New("trust requires --root-key, --version, --output and --key/--revoke")
	}
	seed, err := readSeed(*rootFile)
	if err != nil {
		return err
	}
	var pubs []ed25519.PublicKey
	for _, path := range keys {
		pub, err := readPublic(path)
		if err != nil {
			return err
		}
		pubs = append(pubs, pub)
	}
	t, data, sig, err := catalog.NewTrust(seed, pubs, revoked, *version, time.Now(), *validity)
	if err != nil {
		return err
	}
	root := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if err := catalog.NewTrustDirectory(*output, t, data, sig, root); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"authorization_version": t.Version, "root_sha256": t.RootSHA256, "output": *output})
}

func clientsCommand(args []string) error {
	set := flag.NewFlagSet("clients", flag.ContinueOnError)
	bundle := set.String("bundle", "", "verified delegated PHP bundle")
	rootFile := set.String("root", "", "trusted root public key file")
	keyFile := set.String("key", "", "offline publishing private seed file")
	version := set.String("version", "", "client release version")
	commit := set.String("commit", "", "exact client source commit")
	linux := set.String("linux", "", "Linux amd64 CLI binary")
	windows := set.String("windows", "", "Windows amd64 CLI binary")
	notices := set.String("notices", ".", "project directory containing license notices")
	installer := set.String("installer", "scripts/install-phpm.py", "reviewed bootstrap installer source")
	output := set.String("output", "", "new release directory")
	validity := set.Duration("validity", 30*24*time.Hour, "client manifest lifetime, maximum 2160h")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *bundle == "" || *rootFile == "" || *keyFile == "" || *output == "" || set.NArg() != 0 {
		return errors.New("clients requires --bundle --root --key --version --commit --output and a platform binary")
	}
	root, err := readPublic(*rootFile)
	if err != nil {
		return err
	}
	seed, err := readSeed(*keyFile)
	if err != nil {
		return err
	}
	return catalog.CreateClients(*bundle, *output, root, seed, *version, *commit, *linux, *windows, *notices, *installer, time.Now(), *validity)
}

func verifyCommand(args []string) error {
	set := flag.NewFlagSet("verify", flag.ContinueOnError)
	bundle := set.String("bundle", "", "complete release directory")
	rootFile := set.String("root", "", "trusted root public key file")
	fingerprint := set.String("fingerprint", "", "independently checked root SHA-256")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *bundle == "" || *rootFile == "" || !catalog.ValidSHA(*fingerprint) || set.NArg() != 0 {
		return errors.New("verify requires --bundle --root --fingerprint")
	}
	root, err := readPublic(*rootFile)
	if err != nil {
		return err
	}
	if catalog.Fingerprint(root) != *fingerprint {
		return errors.New("root fingerprint mismatch")
	}
	m, err := catalog.VerifyRelease(*bundle, root, time.Now())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "verified", "version": m.Version, "commit": m.Commit, "files": len(m.Files), "root_sha256": *fingerprint})
}
