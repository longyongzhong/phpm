package phpm

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testNativeComposerIntegration(t *testing.T, s *Store, root string, env LockedEnvironment) {
	t.Helper()
	if _, ok := env.Runtime.PHP.Extensions["phar"]; !ok {
		t.Skip("Composer integration requires phar in the imported native test runtime")
	}
	phar := os.Getenv("PHPM_TEST_COMPOSER")
	if phar == "" {
		var err error
		phar, err = resolveComposer()
		if err != nil {
			t.Skip("Composer integration requires a Composer 2 PHP payload")
		}
	}
	t.Setenv("PHPM_COMPOSER", phar)
	file := filepath.Join(root, "composer.json")
	t.Setenv("COMPOSER", file)
	data := []byte(`{
  "name":"phpm/native-test",
  "require":{"php":">=7.4","ext-json":"^0.0","ext-phpm_polyfill":"*"},
  "require-dev":{"ext-phpm_dev_missing":"*"},
  "suggest":{"ext-phpm_optional":"optional"},
  "config":{"platform":{"ext-json":"0.0.1"}},
  "autoload":{"files":["danger.php"]},
  "scripts":{"post-install-cmd":"@php danger.php"}
}`)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "executed")
	if err := os.WriteFile(filepath.Join(root, "danger.php"), []byte("<?php file_put_contents(__DIR__.'/executed','unexpected');"), 0600); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(filepath.Join(root, "phpm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	beforeLock, err := os.ReadFile(filepath.Join(root, "phpm.lock"))
	if err != nil {
		t.Fatal(err)
	}
	check := func(noDev bool) ComposerCheckReport {
		t.Helper()
		report, err := s.checkComposer(root, env, noDev, false)
		if err != nil {
			t.Fatal(err)
		}
		if report.RuntimeID != env.Runtime.ID {
			t.Fatal("wrong PHP checked")
		}
		return report
	}
	rows := func(report ComposerCheckReport) map[string]ComposerPlatformResult {
		result := make(map[string]ComposerPlatformResult)
		for _, row := range report.Results {
			result[row.Name] = row
		}
		return result
	}
	report := check(false)
	got := rows(report)
	if report.Scope != "root" || len(report.Warnings) == 0 || got["ext-json"].Status != "failed" || got["ext-phpm_dev_missing"].Status != "missing" || got["ext-phpm_optional"].Name != "" {
		t.Fatalf("root diagnostics wrong: %+v", report)
	}
	if _, err := s.checkComposer(root, env, false, true); err == nil {
		t.Fatal("--lock accepted no lock")
	}
	if rows(check(true))["ext-phpm_dev_missing"].Name != "" {
		t.Fatal("--no-dev checked root dev requirements")
	}
	sum := md5.Sum(data) // Exercise Composer's supported legacy lock freshness hash.
	lock := map[string]any{
		"hash": hex.EncodeToString(sum[:]),
		"packages": []any{
			map[string]any{"name": "test/library", "version": "1.0.0", "require": map[string]string{"ext-phpm_dep_missing": "*"}, "require-dev": map[string]string{"ext-phpm_indirect_dev": "*"}},
			map[string]any{"name": "test/polyfill", "version": "1.0.0", "provide": map[string]string{"ext-phpm_polyfill": "*"}},
		},
		"packages-dev": []any{map[string]any{"name": "test/devlib", "version": "1.0.0", "require": map[string]string{"ext-phpm_lock_dev": "*"}}},
	}
	writeLock := func() []byte {
		t.Helper()
		encoded, err := json.Marshal(lock)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(composerLockPath(file), encoded, 0600); err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	writeLock()
	report = check(false)
	got = rows(report)
	if report.Scope != "root+lock" || report.LockFresh == nil || !*report.LockFresh || got["ext-phpm_dep_missing"].Status != "missing" || got["ext-phpm_lock_dev"].Status != "missing" || got["ext-phpm_indirect_dev"].Name != "" || got["ext-phpm_polyfill"].Provider != "test/polyfill" || got["ext-phpm_polyfill"].Status != "success" {
		t.Fatalf("locked diagnostics wrong: %+v", report)
	}
	if rows(check(true))["ext-phpm_lock_dev"].Name != "" {
		t.Fatal("--no-dev checked locked dev packages")
	}
	lock["content-hash"] = "00000000000000000000000000000000"
	encodedLock := writeLock()
	report = check(false)
	if report.LockFresh == nil || *report.LockFresh || composerCheckCode(report) == 0 {
		t.Fatal("stale lock accepted")
	}
	for path, expected := range map[string][]byte{file: data, composerLockPath(file): encodedLock, filepath.Join(root, "phpm.yaml"): beforeConfig, filepath.Join(root, "phpm.lock"): beforeLock} {
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("check changed %s: %v", path, err)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("check ran project autoload/scripts")
	}
	t.Run("DiagnosticHandling", func(t *testing.T) {
		testComposerDiagnosticHandling(t, s, root, env, phar)
	})
	t.Run("MissingExtensions", func(t *testing.T) {
		testNativeMissingExtensions(t, s, env.Runtime)
	})
	if err := os.WriteFile(file, []byte(`{"require":{"ext-json":"this is not a version"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.checkComposer(root, env, true, false); err == nil {
		t.Fatal("invalid constraint accepted")
	}

	// A payload fixture isolates argument, PHP-selection and exit-code forwarding
	// from Composer's normal scripts, plugins and network behavior.
	cli := os.Getenv("PHPM_TEST_CLI_BINARY")
	if cli == "" {
		return
	}
	cli, err = filepath.Abs(cli)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "forwarding.php")
	if err := os.WriteFile(payload, []byte(`<?php if (($argv[1]??'')==='exit23') {exit(23);} echo json_encode(['php'=>PHP_VERSION,'binary'=>PHP_BINARY,'args'=>array_slice($argv,1)]);`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range [][]string{{"composer"}, {"exec", "--", "composer"}} {
		args := append([]string{"--home", s.Home}, prefix...)
		args = append(args, "show", "--locked", "argument with spaces")
		command := exec.Command(cli, args...)
		command.Dir = root
		command.Env = cleanEnv(map[string]string{"PHPM_COMPOSER": payload})
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("Composer forwarding: %v %s", err, output)
		}
		var result struct {
			PHP    string
			Binary string
			Args   []string
		}
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		if result.PHP != env.Runtime.PHP.Version || !sameExistingFile(result.Binary, s.runtimeBinary(env.Runtime)) || len(result.Args) != 3 || result.Args[2] != "argument with spaces" {
			t.Fatalf("wrong forwarded context: %+v", result)
		}
	}
	command := exec.Command(cli, "--home", s.Home, "composer", "exit23")
	command.Dir = root
	command.Env = cleanEnv(map[string]string{"PHPM_COMPOSER": payload})
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("Composer exit code lost: %v %s", err, output)
	}
}

func testComposerDiagnosticHandling(t *testing.T, s *Store, root string, env LockedEnvironment, phar string) {
	t.Helper()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "composer.json")
	if err := os.WriteFile(manifest, []byte(`{"require":{"php":">=7.4","ext-json":"*"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPOSER", manifest)
	// Copy the trusted PHAR and wrap its autoloader inside that same archive.
	// Actual Composer classes still perform the check; fixtures only inject a
	// diagnostic, so a warning cannot be mistaken for a valid checker result.
	build := `if (!copy($argv[1], $argv[2])) {throw new RuntimeException('copy failed');} $p = new Phar($argv[2]); $p['vendor/phpm-original-autoload.php'] = $p['vendor/autoload.php']->getContent(); $p['vendor/autoload.php'] = '<?php '.$argv[3].'; return require __DIR__."/phpm-original-autoload.php";';`
	for _, fixture := range []struct {
		name      string
		code      string
		wantError bool
	}{
		{"deprecated", `trigger_error('fixture deprecation', E_USER_DEPRECATED)`, false},
		{"warning", `trigger_error('fixture warning', E_USER_WARNING)`, true},
		{"exception", `throw new RuntimeException('fixture exception')`, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			payload := filepath.Join(dir, fixture.name+".phar")
			command := exec.Command(s.runtimeBinary(env.Runtime), "-n", "-d", "phar.readonly=0", "-r", build, "--", phar, payload, fixture.code)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("build diagnostic PHAR: %v %s", err, output)
			}
			t.Setenv("PHPM_COMPOSER", payload)
			report, err := s.checkComposer(root, env, false, false)
			if fixture.wantError {
				if err == nil || !strings.Contains(err.Error(), "fixture "+fixture.name) {
					t.Fatalf("expected injected warning/exception to fail: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if composerCheckCode(report) != 0 || len(report.Diagnostics) == 0 || len(report.Warnings) == 0 {
				t.Fatalf("deprecation did not preserve successful checks and diagnostics: %+v", report)
			}
			found := false
			for _, diagnostic := range report.Diagnostics {
				if diagnostic.Message == "fixture deprecation" && diagnostic.Level == "deprecated" && diagnostic.File != "" && diagnostic.Line > 0 {
					found = true
				}
			}
			if !found {
				t.Fatal("injected deprecation details were lost")
			}
		})
	}
}
