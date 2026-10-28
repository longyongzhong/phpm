package phpm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvironmentFileIdentityRejectsSubstitutes(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "php.ini")
	substitute := filepath.Join(root, "other.ini")
	for _, path := range []string{original, substitute} {
		if err := os.WriteFile(path, []byte("memory_limit=128M\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !sameExistingFile(original, original) {
		t.Fatal("the same existing file was rejected")
	}
	for _, other := range []string{substitute, filepath.Join(root, "missing.ini"), root, "php.ini", ""} {
		if sameExistingFile(original, other) {
			t.Fatalf("a different/missing/non-absolute file was accepted: %q", other)
		}
	}
	if sameExistingFile(filepath.Join(root, "missing.ini"), filepath.Join(root, "missing.ini")) {
		t.Fatal("two missing paths were accepted")
	}
	t.Run("directory-alias", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(root, alias); err != nil {
			t.Skipf("directory symlink creation is unavailable: %v", err)
		}
		if !sameExistingFile(original, filepath.Join(alias, "php.ini")) {
			t.Fatal("an existing file reached through a directory alias was rejected")
		}
	})
}
