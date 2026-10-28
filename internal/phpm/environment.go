package phpm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type LockedEnvironment struct {
	ConfigHash string            `json:"config_hash"`
	ID         string            `json:"id"`
	Runtime    Runtime           `json:"runtime"`
	Extensions []Extension       `json:"extensions"`
	INI        map[string]string `json:"ini,omitempty"`
}

type Lockfile struct {
	Schema  int                          `json:"schema"`
	Targets map[string]LockedEnvironment `json:"targets"`
}

func environmentID(env LockedEnvironment) string {
	// Build identity excludes download location but includes binary hashes,
	// loader mode, resolved settings and the environment format version.
	type module struct {
		ID, Digest string
		Zend       bool
	}
	modules := make([]module, 0, len(env.Extensions))
	for _, ext := range env.Extensions {
		modules = append(modules, module{ext.ID, ext.Digest, ext.Zend})
	}
	return "env-" + digestJSON(struct {
		Schema                            int
		Platform, Runtime, Digest, Config string
		Extensions                        []module
		INI                               map[string]string
	}{1, env.Runtime.Platform, env.Runtime.ID, env.Runtime.Digest, env.ConfigHash, modules, env.INI})
}

func loadLock(root string) (Lockfile, error) {
	lock := Lockfile{Schema: 1, Targets: map[string]LockedEnvironment{}}
	err := readJSON(filepath.Join(root, "phpm.lock"), &lock)
	if err != nil {
		return lock, err
	}
	if lock.Schema != 1 || lock.Targets == nil {
		return lock, errors.New("unsupported or invalid phpm.lock")
	}
	return lock, nil
}

func (s *Store) install(root string, frozen, update bool) (LockedEnvironment, error) {
	var selected LockedEnvironment
	if frozen && update {
		return selected, errors.New("--frozen and --update cannot be combined")
	}
	unlock, err := s.lock("project:" + root)
	if err != nil {
		return selected, err
	}
	defer unlock()
	if err := recoverProjectTransaction(root); err != nil {
		return selected, err
	}
	cfg, err := readConfig(root)
	if err != nil {
		return selected, err
	}
	hash := digestJSON(cfg)
	lock, err := loadLock(root)
	if errors.Is(err, os.ErrNotExist) {
		if frozen {
			return selected, errors.New("--frozen requires an existing phpm.lock")
		}
		lock = Lockfile{Schema: 1, Targets: map[string]LockedEnvironment{}}
	} else if err != nil {
		return selected, err
	}
	previous, exists := lock.Targets[platformKey()]
	if exists && previous.ConfigHash == hash && !update {
		selected = previous
	} else {
		if frozen {
			return selected, errors.New("lock has no matching configuration for this platform; run phpm install to resolve it")
		}
		rt, err := s.resolveRuntime(cfg)
		if err != nil {
			return selected, err
		}
		exts, err := s.resolveExtensions(cfg, rt)
		if err != nil {
			return selected, err
		}
		selected = LockedEnvironment{ConfigHash: hash, Runtime: rt, Extensions: exts, INI: cfg.INI}
		selected.ID = environmentID(selected)
	}
	if err := s.validateLocked(selected, cfg); err != nil {
		return selected, err
	}
	if err := s.materialize(selected); err != nil {
		return selected, err
	}
	// Detect edits made by an editor while preparing the environment.
	current, err := readConfig(root)
	if err != nil {
		return selected, err
	}
	if digestJSON(current) != hash {
		return selected, errors.New("phpm.yaml changed during installation; lock was not updated")
	}
	if exists && previous.ConfigHash == hash && !update {
		return selected, nil
	}
	lock.Targets[platformKey()] = selected
	if err := writeJSON(filepath.Join(root, "phpm.lock"), lock); err != nil {
		return selected, err
	}
	return selected, nil
}

func (s *Store) validateLocked(env LockedEnvironment, cfg Config) error {
	if env.ConfigHash != digestJSON(cfg) || env.ID != environmentID(env) || env.Runtime.Platform != platformKey() {
		return errors.New("lock identity/configuration/platform mismatch")
	}
	if !matchesVersion(env.Runtime.PHP.Version, cfg.PHP) || (cfg.Runtime != "" && cfg.Runtime != env.Runtime.ID) || digestJSON(cfg.INI) != digestJSON(env.INI) {
		return errors.New("locked runtime or settings do not satisfy phpm.yaml")
	}
	stored, err := s.readRuntime(env.Runtime.ID)
	if err != nil {
		return fmt.Errorf("locked runtime is unavailable; import/install the same build: %w", err)
	}
	// Sources are provenance, not binary identity. A local reimport of the
	// exact official package may legitimately have different source metadata.
	stored.Source, env.Runtime.Source = Source{}, Source{}
	if digestJSON(stored) != digestJSON(env.Runtime) {
		return errors.New("runtime manifest differs from the lock")
	}
	if err := s.verifyRuntime(stored); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ext := range env.Extensions {
		if seen[ext.Name] || cfg.Extensions[ext.Name] != ext.Version || ext.RuntimeID != stored.ID {
			return errors.New("locked extensions do not satisfy phpm.yaml")
		}
		seen[ext.Name] = true
		cached, err := s.readExtension(ext.ID)
		if err != nil {
			return fmt.Errorf("locked extension %s unavailable: %w", ext.Name, err)
		}
		cached.Source, ext.Source = Source{}, Source{}
		if digestJSON(cached) != digestJSON(ext) {
			return errors.New("extension manifest differs from the lock")
		}
		if err := s.verifyExtension(cached); err != nil {
			return err
		}
	}
	for name, version := range cfg.Extensions {
		if !seen[name] && stored.PHP.Extensions[loadedExtensionName(name)] != version {
			return fmt.Errorf("extension %s is missing from the lock", name)
		}
	}
	return nil
}

func iniQuote(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00\"$") {
		return "", errors.New("INI values/paths cannot contain quotes, dollar signs or control characters")
	}
	return `"` + value + `"`, nil
}

func (s *Store) renderINI(env LockedEnvironment) ([]byte, error) {
	var b strings.Builder
	b.WriteString("; Generated by PHPM. Changes invalidate the environment.\n")
	keys := make([]string, 0, len(env.INI))
	for key := range env.INI {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !iniPattern.MatchString(key) {
			return nil, errors.New("invalid locked INI key")
		}
		quoted, err := iniQuote(env.INI[key])
		if err != nil {
			return nil, fmt.Errorf("INI %s: %w", key, err)
		}
		fmt.Fprintf(&b, "%s=%s\n", key, quoted)
	}
	for _, ext := range env.Extensions {
		key := "extension"
		if ext.Zend {
			key = "zend_extension"
		}
		quoted, err := iniQuote(filepath.ToSlash(s.modulePath(ext)))
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s=%s\n", key, quoted)
	}
	return []byte(b.String()), nil
}

func (s *Store) environmentPath(env LockedEnvironment) string {
	return filepath.Join(s.Home, "environments", env.ID)
}

func (s *Store) environmentVars(env LockedEnvironment, root string) []string {
	paths := []string{filepath.Dir(s.runtimeBinary(env.Runtime))}
	for _, ext := range env.Extensions {
		paths = append(paths, filepath.Dir(s.modulePath(ext)), filepath.Join(s.extPath(ext.ID), "payload"))
	}
	paths = append(paths, os.Getenv("PATH"))
	return cleanEnv(map[string]string{"PATH": strings.Join(paths, string(os.PathListSeparator)), "PHPRC": filepath.Join(root, "php.ini"), "PHP_INI_SCAN_DIR": filepath.Join(root, "conf.d"), "PHPM_ENVIRONMENT": env.ID})
}

func (s *Store) materialize(env LockedEnvironment) error {
	unlock, err := s.lock("environment:" + env.ID)
	if err != nil {
		return err
	}
	defer unlock()
	root := s.environmentPath(env)
	if _, err := os.Stat(root); err == nil {
		return s.verifyEnvironment(env)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Join(s.Home, "tmp"), "env-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err := os.Mkdir(filepath.Join(temp, "conf.d"), 0700); err != nil {
		return err
	}
	ini, err := s.renderINI(env)
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(temp, "php.ini"), ini, 0600); err != nil {
		return err
	}
	if err := s.probeEnvironment(env, temp); err != nil {
		return err
	}
	if err := os.Rename(temp, root); err != nil {
		return err
	}
	return nil
}

func (s *Store) verifyEnvironment(env LockedEnvironment) error {
	root := s.environmentPath(env)
	expected, err := s.renderINI(env)
	if err != nil {
		return err
	}
	actual, err := os.ReadFile(filepath.Join(root, "php.ini"))
	if err != nil {
		return err
	}
	if string(actual) != string(expected) {
		return errors.New("environment php.ini was modified; restore it or reinstall into a clean PHPM_HOME")
	}
	entries, err := os.ReadDir(filepath.Join(root, "conf.d"))
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("environment conf.d must be empty; unexpected configuration detected")
	}
	return s.probeEnvironment(env, root)
}

func (s *Store) probeEnvironment(env LockedEnvironment, root string) error {
	code := `$e=[];foreach(get_loaded_extensions() as $n){$e[strtolower($n)]=(string)phpversion($n);}echo json_encode(['version'=>PHP_VERSION,'extensions'=>$e,'ini'=>php_ini_loaded_file(),'scanned'=>php_ini_scanned_files()]);`
	output, err := phpOutput(s.runtimeBinary(env.Runtime), []string{"-c", filepath.Join(root, "php.ini"), "-r", code}, s.environmentVars(env, root))
	if err != nil {
		return err
	}
	var result struct {
		Version    string            `json:"version"`
		Extensions map[string]string `json:"extensions"`
		INI        string            `json:"ini"`
		Scanned    any               `json:"scanned"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("invalid environment probe output: %w", err)
	}
	if result.Version != env.Runtime.PHP.Version {
		return fmt.Errorf("PHP runtime version differs: expected %q, got %q", env.Runtime.PHP.Version, result.Version)
	}
	expectedINI := filepath.Join(root, "php.ini")
	if !sameExistingFile(result.INI, expectedINI) {
		return fmt.Errorf("PHP loaded an unexpected configuration: expected %q, got %q", expectedINI, result.INI)
	}
	if scanned, ok := result.Scanned.(string); ok && strings.TrimSpace(scanned) != "" {
		return errors.New("PHP unexpectedly scanned extra INI files")
	}
	expected := make(map[string]string, len(env.Runtime.PHP.Extensions)+len(env.Extensions))
	for name, version := range env.Runtime.PHP.Extensions {
		expected[name] = version
	}
	for _, ext := range env.Extensions {
		expected[loadedExtensionName(ext.Name)] = ext.Version
	}
	if digestJSON(expected) != digestJSON(result.Extensions) {
		return fmt.Errorf("loaded extensions differ from the environment: expected %v, got %v", expected, result.Extensions)
	}
	return nil
}

// PHP reports canonical paths, which can differ from Go's spelling for Windows
// short names/case or macOS /var aliases. Require the exact same existing file,
// rather than accepting matching contents or loosely normalizing path strings.
func sameExistingFile(left, right string) bool {
	if !filepath.IsAbs(left) || !filepath.IsAbs(right) {
		return false
	}
	a, err := os.Stat(left)
	if err != nil || !a.Mode().IsRegular() {
		return false
	}
	b, err := os.Stat(right)
	return err == nil && b.Mode().IsRegular() && os.SameFile(a, b)
}

func (s *Store) current(root string) (LockedEnvironment, error) {
	if err := checkProjectTransaction(root); err != nil {
		return LockedEnvironment{}, err
	}
	cfg, err := readConfig(root)
	if err != nil {
		return LockedEnvironment{}, err
	}
	lock, err := loadLock(root)
	if err != nil {
		return LockedEnvironment{}, fmt.Errorf("run phpm install first: %w", err)
	}
	env, ok := lock.Targets[platformKey()]
	if !ok {
		return env, errors.New("lock has no entry for this platform; run phpm install")
	}
	if err := s.validateLocked(env, cfg); err != nil {
		return env, err
	}
	if err := s.verifyEnvironment(env); err != nil {
		return env, err
	}
	return env, nil
}

// Keep environment variable values textual; for example memory_limit=256M
// and display_errors=0 must not be inferred as YAML booleans/numbers.
func formatRuntime(rt Runtime) string {
	return rt.ID + "  PHP " + rt.PHP.Version + "  zts=" + strconv.FormatBool(rt.PHP.ZTS)
}
