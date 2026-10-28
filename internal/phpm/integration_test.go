package phpm

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Opt-in native integration tests. CI supplies PHP; no test downloads binaries,
// installs dependencies, or changes the host PHP configuration.
func TestNativeProjectIsolation(t *testing.T) {
	binary := os.Getenv("PHPM_TEST_PHP_BINARY")
	if binary == "" {
		t.Skip("set PHPM_TEST_PHP_BINARY to run native PHP integration tests")
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	rel := filepath.Join("bin", "php")
	if runtime.GOOS == "windows" {
		rel = "php.exe"
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(fixture, rel)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(resolved, filepath.Join(fixture, rel), 0700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		dlls, err := filepath.Glob(filepath.Join(filepath.Dir(resolved), "*.dll"))
		if err != nil {
			t.Fatal(err)
		}
		for _, dll := range dlls {
			if err := copyFile(dll, filepath.Join(fixture, filepath.Base(dll)), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	sourceBefore, err := treeSHA(fixture)
	if err != nil {
		t.Fatal(err)
	}
	s := testStore(t)
	rt, err := s.importRuntime(fixture, rel, Source{Kind: "local-import"})
	if err != nil {
		t.Fatal(err)
	}
	// A valid user configuration that would change PHP must not leak in.
	poison := t.TempDir()
	if err := os.WriteFile(filepath.Join(poison, "poison.ini"), []byte("memory_limit=64M\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PHP_INI_SCAN_DIR", poison)
	t.Setenv("PHPRC", poison)
	configs := []Config{
		{Schema: 1, PHP: rt.PHP.Version, INI: map[string]string{"memory_limit": "128M"}},
		{Schema: 1, PHP: rt.PHP.Version, INI: map[string]string{"memory_limit": "256M"}},
	}
	module := os.Getenv("PHPM_TEST_REDIS_MODULE")
	if module != "" {
		version := probeRedisVersion(t, s.runtimeBinary(rt), module)
		if _, err := s.importExtension(rt, "redis", version, module, "", "", false); err != nil {
			t.Fatal(err)
		}
		configs[0].Extensions = map[string]string{"redis": version}
		if alternate := os.Getenv("PHPM_TEST_REDIS_ALTERNATE"); alternate != "" {
			alternateVersion := probeRedisVersion(t, s.runtimeBinary(rt), alternate)
			if alternateVersion == version {
				t.Fatal("alternate Redis module must have a different version")
			}
			if _, err := s.importExtension(rt, "redis", alternateVersion, alternate, "", "", false); err != nil {
				t.Fatal(err)
			}
			configs[1].Extensions = map[string]string{"redis": alternateVersion}
		}
	}
	roots := []string{t.TempDir(), t.TempDir()}
	envs := make([]LockedEnvironment, 2)
	for i, cfg := range configs {
		writeTestConfig(t, roots[i], cfg)
		envs[i], err = s.install(roots[i], false, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.install(roots[i], true, false); err != nil {
			t.Fatalf("frozen reinstall: %v", err)
		}
		if _, err := s.current(roots[i]); err != nil {
			t.Fatal(err)
		}
	}
	if envs[0].ID == envs[1].ID || envs[0].Runtime.ID != envs[1].Runtime.ID {
		t.Fatal("projects must share PHP and have separate environments")
	}
	// Start both PHP processes before collecting results; they overlap in time.
	type process struct {
		cmd            *exec.Cmd
		output, stderr strings.Builder
	}
	processes := make([]*process, 2)
	for i, env := range envs {
		p := &process{}
		code := `usleep(250000);echo json_encode(['php'=>PHP_BINARY,'redis'=>(string)phpversion('redis'),'memory'=>ini_get('memory_limit')]);`
		p.cmd = exec.Command(s.runtimeBinary(rt), "-r", code)
		p.cmd.Env = s.environmentVars(env, s.environmentPath(env))
		p.cmd.Stdout, p.cmd.Stderr = &p.output, &p.stderr
		if err := p.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		processes[i] = p
	}
	for i, p := range processes {
		if err := p.cmd.Wait(); err != nil {
			t.Fatalf("PHP: %v %s", err, &p.stderr)
		}
		if p.stderr.Len() != 0 {
			t.Fatalf("PHP warning: %s", &p.stderr)
		}
		var result struct{ PHP, Redis, Memory string }
		if err := json.Unmarshal([]byte(p.output.String()), &result); err != nil {
			t.Fatal(err)
		}
		expectedRedis := configs[i].Extensions["redis"]
		if expectedRedis == "" {
			expectedRedis = rt.PHP.Extensions["redis"]
		}
		if result.Redis != expectedRedis || result.Memory != configs[i].INI["memory_limit"] || !sameExistingFile(result.PHP, s.runtimeBinary(rt)) {
			t.Fatalf("project %d leaked state: %+v", i, result)
		}
	}
	// Installing a compiled-in module needs no network and must update both
	// project files only after the prospective environment has been probed.
	builtinRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(builtinRoot, "phpm.yaml"), []byte("# keep this project comment\nschema: 1\nphp: \""+rt.PHP.Version+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := windowsExtensionProvider{Store: s}
	builtinEnv, installedVersion, err := s.installProjectExtension(builtinRoot, ExtensionRequest{Name: "json"}, provider.acquire)
	if err != nil {
		t.Fatal(err)
	}
	if installedVersion != rt.PHP.Extensions["json"] || builtinEnv.Runtime.ID != rt.ID {
		t.Fatal("compiled-in extension installation selected the wrong runtime/version")
	}
	installedConfig, err := readConfig(builtinRoot)
	if err != nil || installedConfig.Extensions["json"] != installedVersion {
		t.Fatalf("extension was not declared: %+v %v", installedConfig, err)
	}
	installedYAML, _ := os.ReadFile(filepath.Join(builtinRoot, "phpm.yaml"))
	if !strings.Contains(string(installedYAML), "# keep this project comment") {
		t.Fatal("extension installation dropped project comments")
	}
	if _, err := s.current(builtinRoot); err != nil {
		t.Fatal(err)
	}
	aliasRoot := t.TempDir()
	aliasConfig := []byte("schema: 1\nphp: \"" + rt.PHP.Version + "\"\nextensions: &shared\n  json: \"0.0.0\"\nini: *shared\n")
	if err := os.WriteFile(filepath.Join(aliasRoot, "phpm.yaml"), aliasConfig, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.installProjectExtension(aliasRoot, ExtensionRequest{Name: "json"}, provider.acquire); err == nil {
		t.Fatal("shared YAML anchor changed unrelated INI settings")
	}
	aliasAfter, _ := os.ReadFile(filepath.Join(aliasRoot, "phpm.yaml"))
	if string(aliasAfter) != string(aliasConfig) {
		t.Fatal("rejected YAML alias update changed the manifest")
	}
	if _, err := os.Stat(filepath.Join(aliasRoot, "phpm.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected YAML alias update wrote a lock")
	}
	if cli := os.Getenv("PHPM_TEST_CLI_BINARY"); cli != "" {
		cli, err = filepath.Abs(cli)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(cli, "--home", s.Home, "install", "--frozen")
		command.Dir = roots[1]
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("CLI frozen install: %v %s", err, output)
		}
		command = exec.Command(cli, "--home", s.Home, "ext", "install", "json")
		command.Dir = builtinRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("CLI extension installation: %v %s", err, output)
		}
		command = exec.Command(cli, "--home", s.Home, "exec", "--", "php", "-r", "exit(23);")
		command.Dir = roots[1]
		output, err := command.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 23 {
			t.Fatalf("CLI exit code lost: %v %s", err, output)
		}
		// Exercise a nested bare `php`, as used by Composer scripts. The
		// nested process must inherit the selected version and INI settings.
		code := `$p=proc_open(['php','-r','echo PHP_VERSION,"|",ini_get("memory_limit");'],[1=>['pipe','w'],2=>['pipe','w']],$pipes);if(!is_resource($p)){exit(9);}echo stream_get_contents($pipes[1]);$e=stream_get_contents($pipes[2]);fclose($pipes[1]);fclose($pipes[2]);if($e!==''){fwrite(STDERR,$e);}exit(proc_close($p));`
		command = exec.Command(cli, "--home", s.Home, "exec", "--", "php", "-r", code)
		command.Dir = roots[1]
		output, err = command.CombinedOutput()
		if err != nil || string(output) != rt.PHP.Version+"|256M" {
			t.Fatalf("nested PHP inherited the wrong environment: %v %q", err, output)
		}
	}
	// Existing locks survive failed resolution; the other project still runs.
	before, err := os.ReadFile(filepath.Join(roots[0], "phpm.lock"))
	if err != nil {
		t.Fatal(err)
	}
	bad := configs[0]
	bad.Extensions = map[string]string{"nonexistent": "1.0.0"}
	writeTestConfig(t, roots[0], bad)
	if _, err := s.install(roots[0], false, false); err == nil {
		t.Fatal("missing extension accepted")
	}
	after, err := os.ReadFile(filepath.Join(roots[0], "phpm.lock"))
	if err != nil || string(before) != string(after) {
		t.Fatal("failed install replaced the lock")
	}
	if _, err := s.current(roots[1]); err != nil {
		t.Fatal(err)
	}
	// Additional INI files cannot silently add configuration to a locked env.
	if err := os.WriteFile(filepath.Join(s.environmentPath(envs[1]), "conf.d", "unexpected.ini"), []byte("memory_limit=32M"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.current(roots[1]); err == nil {
		t.Fatal("unexpected INI file accepted")
	}
	sourceAfter, err := treeSHA(fixture)
	if err != nil || sourceAfter != sourceBefore {
		t.Fatal("source PHP distribution changed")
	}
	t.Run("Composer", func(t *testing.T) {
		testNativeComposerIntegration(t, s, builtinRoot, builtinEnv)
	})
	testNativeUpgradeLifecycle(t, s, rt, fixture, rel)
}

func probeRedisVersion(t *testing.T, binary, module string) string {
	t.Helper()
	output, err := phpOutput(binary, []string{"-n", "-d", "extension=" + module, "-r", `echo (string)phpversion('redis');`}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	version := string(output)
	if !versionPattern.MatchString(version) {
		t.Fatalf("invalid Redis version %q", version)
	}
	return version
}

func writeTestConfig(t *testing.T, root string, cfg Config) {
	t.Helper()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "phpm.yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
