package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"phpm/internal/catalog"
)

func TestGitHubSetupKeepsPrivateKeysSeparateAndPreservesExistingKeys(t *testing.T) {
	base := t.TempDir()
	keys := filepath.Join(base, "private")
	out := filepath.Join(base, "public")
	root, publisher, err := setupGitHub(keys, out, 1, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	trust, _, _, err := catalog.ReadTrustDirectory(out, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trust.PublishingKey(catalog.Fingerprint(publisher)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatal("unexpected public files")
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".seed" {
			t.Fatal("private seed copied to public output")
		}
	}
	before, err := os.ReadFile(filepath.Join(keys, "publisher.seed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := setupGitHub(keys, out, 2, time.Hour); err == nil {
		t.Fatal("overwrote public authorization")
	}
	nextRoot, nextPublisher, err := setupGitHub(keys, filepath.Join(base, "renewed"), 2, 24*time.Hour)
	if err != nil || !bytes.Equal(nextRoot, root) || !bytes.Equal(nextPublisher, publisher) {
		t.Fatalf("rotated keys unexpectedly: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(keys, "publisher.seed"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("modified private key")
	}
}

func TestGitHubSetupRejectsIncompleteKeysAndPrivatePublicOverlap(t *testing.T) {
	base := t.TempDir()
	keys := filepath.Join(base, "keys")
	if err := os.Mkdir(keys, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := setupGitHub(keys, filepath.Join(keys, "public"), 1, time.Hour); err == nil {
		t.Fatal("accepted overlapping directories")
	}
	path := filepath.Join(keys, "root.seed")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := setupGitHub(keys, filepath.Join(base, "public"), 1, time.Hour); err == nil {
		t.Fatal("replaced incomplete key pair")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve" {
		t.Fatal("existing key changed")
	}
}
