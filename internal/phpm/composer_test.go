package phpm

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposerResolutionUsesPayloadAndHonorsExplicitPath(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "composer.phar")
	if err := os.WriteFile(payload, []byte("#!/usr/bin/env php\n<?php echo 'test';"), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "composer.bat")
	if err := os.WriteFile(launcher, []byte("@php composer.phar %*"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "relative-tools"+string(os.PathListSeparator)+dir)
	t.Setenv("PHPM_COMPOSER", "")
	got, err := resolveComposer()
	if err != nil || got != payload {
		t.Fatalf("payload=%q err=%v", got, err)
	}
	t.Setenv("PHPM_COMPOSER", launcher)
	if _, err := resolveComposer(); err == nil {
		t.Fatal("batch launcher accepted as PHP payload")
	}
	t.Setenv("PHPM_COMPOSER", filepath.Join(dir, "missing.phar"))
	if _, err := resolveComposer(); err == nil {
		t.Fatal("invalid explicit path silently fell back to PATH")
	}
	t.Setenv("PHPM_COMPOSER", payload)
	if _, err := resolveComposer(); err != nil {
		t.Fatal(err)
	}
}

func TestComposerProjectLookupAndCustomLockNames(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "nested")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "composer.json")
	if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := findComposerFile(canonicalRoot, child, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "composer.json" || projectIdentity(filepath.Dir(got)) != projectIdentity(canonicalRoot) {
		t.Fatalf("wrong project file: %s", got)
	}
	custom := filepath.Join(child, "production.json")
	if err := os.WriteFile(custom, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = findComposerFile(canonicalRoot, child, "production.json")
	if err != nil || got != custom {
		t.Fatalf("custom file=%q err=%v", got, err)
	}
	if composerLockPath(custom) != filepath.Join(child, "production.lock") {
		t.Fatal("custom lock name incorrect")
	}
	if composerLockPath(filepath.Join(child, "manifest")) != filepath.Join(child, "manifest.lock") {
		t.Fatal("extensionless lock name incorrect")
	}
}

func TestComposerMetadataMustBeAnObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "composer.json")
	for _, invalid := range []string{"null", "[]", "{} {}", "{bad}"} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readComposerObject(path); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestComposerCheckReportsFailuresProvidersAndStaleLocks(t *testing.T) {
	fresh := false
	if composerCheckCode(ComposerCheckReport{LockFresh: &fresh}) == 0 {
		t.Fatal("stale lock reported success")
	}
	report := ComposerCheckReport{Scope: "root+lock", Results: []ComposerPlatformResult{
		{Name: "ext-json", Version: "8.4.27", Status: "success"},
		{Name: "ext-redis", Version: "6.1.0", Status: "failed", Requirements: []ComposerRequirement{{Source: "test/app", Type: "requires", Constraint: "^6.2"}}},
		{Name: "ext-mbstring", Version: "n/a", Status: "success", Provider: "test/polyfill"},
	}}
	if composerCheckCode(report) == 0 {
		t.Fatal("version mismatch reported success")
	}
	var output bytes.Buffer
	writeComposerCheck(&output, report)
	for _, expected := range []string{"failed", "ext-redis", "test/app", "^6.2", "provided by test/polyfill"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in %s", expected, &output)
		}
	}
}
