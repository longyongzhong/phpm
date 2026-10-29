package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"phpm/internal/catalog"
)

func insideDirectory(path, directory string) bool {
	rel, err := filepath.Rel(directory, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func setupGitHubCommand(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	set := flag.NewFlagSet("setup-github", flag.ContinueOnError)
	keys := set.String("keys", filepath.Join(home, "phpm-signing"), "local private key directory outside the repository")
	output := set.String("public-output", filepath.Join("release", "trust"), "NEW directory containing public authorization only")
	version := set.Uint64("version", 1, "root authorization version; increase when renewing")
	validity := set.Duration("validity", 365*24*time.Hour, "root authorization lifetime, maximum 8760h")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("setup-github takes flags only")
	}
	keyDir, err := filepath.Abs(*keys)
	if err != nil {
		return err
	}
	// Do not place formal private keys inside any existing Git checkout.
	for parent := keyDir; ; parent = filepath.Dir(parent) {
		if _, err := os.Lstat(filepath.Join(parent, ".git")); err == nil {
			return errors.New("private key directory must be outside a Git checkout")
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	root, publisher, err := setupGitHub(keyDir, *output, *version, *validity)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "github-signing-initialized", "public_directory": *output,
		"root_sha256": catalog.Fingerprint(root), "publisher_sha256": catalog.Fingerprint(publisher),
		"github_environment": "phpm-release", "secret_name": "PHPM_RELEASE_SEED", "secret_file": filepath.Join(keyDir, "publisher.seed"),
		"private_key_directory": keyDir})
}

func setupGitHub(keyDir, output string, version uint64, validity time.Duration) (ed25519.PublicKey, ed25519.PublicKey, error) {
	var empty ed25519.PublicKey
	keyDir, err := filepath.Abs(keyDir)
	if err != nil {
		return empty, empty, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return empty, empty, err
	}
	if insideDirectory(keyDir, output) || insideDirectory(output, keyDir) {
		return empty, empty, errors.New("public output and private key directories must be separate")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return empty, empty, errors.New("public authorization destination already exists or cannot be inspected; nothing overwritten")
	}
	if version == 0 || validity <= 0 || validity > 365*24*time.Hour {
		return empty, empty, errors.New("invalid authorization version/lifetime")
	}
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return empty, empty, err
	}
	rootSeed, root, err := existingOrNewKeyPair(keyDir, "root")
	if err != nil {
		return empty, empty, err
	}
	_, publisher, err := existingOrNewKeyPair(keyDir, "publisher")
	if err != nil {
		return empty, empty, err
	}
	t, data, sig, err := catalog.NewTrust(rootSeed, []ed25519.PublicKey{publisher}, nil, version, time.Now(), validity)
	if err != nil {
		return empty, empty, err
	}
	if err := catalog.NewTrustDirectory(output, t, data, sig, root); err != nil {
		return empty, empty, err
	}
	if err := os.WriteFile(filepath.Join(output, "fingerprint.txt"), []byte(catalog.Fingerprint(root)+"\n"), 0644); err != nil {
		return empty, empty, err
	}
	return root, publisher, nil
}

func existingOrNewKeyPair(dir, name string) ([]byte, ed25519.PublicKey, error) {
	private, public := filepath.Join(dir, name+".seed"), filepath.Join(dir, name+".pub")
	privateInfo, privateErr := os.Lstat(private)
	publicInfo, publicErr := os.Lstat(public)
	if privateErr == nil && publicErr == nil {
		if !privateInfo.Mode().IsRegular() || !publicInfo.Mode().IsRegular() {
			return nil, nil, errors.New("key pair must be regular files")
		}
		seed, err := readSeed(private)
		if err != nil {
			return nil, nil, err
		}
		pub, err := readPublic(public)
		if err != nil {
			return nil, nil, err
		}
		derived := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		if !bytes.Equal(pub, derived) {
			return nil, nil, fmt.Errorf("%s private/public keys do not match; existing files preserved", name)
		}
		return seed, pub, nil
	}
	if !errors.Is(privateErr, os.ErrNotExist) || !errors.Is(publicErr, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("%s key pair is incomplete or inaccessible; existing files preserved", name)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	write := func(path string, data []byte) error {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return errors.Join(err, f.Close())
	}
	if err := write(private, []byte(base64.StdEncoding.EncodeToString(key.Seed())+"\n")); err != nil {
		return nil, nil, err
	}
	if err := write(public, []byte(base64.StdEncoding.EncodeToString(pub)+"\n")); err != nil {
		return nil, nil, err
	}
	return key.Seed(), pub, nil
}
