package phpm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s *Store) importExtension(rt Runtime, name, version, file, packageDir, expectedSHA string, zend bool) (Extension, error) {
	return s.importExtensionFromSource(rt, name, version, file, packageDir, expectedSHA, zend, Source{})
}

func (s *Store) importExtensionFromSource(rt Runtime, name, version, file, packageDir, expectedSHA string, zend bool, source Source) (Extension, error) {
	var ext Extension
	if !namePattern.MatchString(name) || !versionPattern.MatchString(version) || strings.Count(version, ".") != 2 {
		return ext, errors.New("extension requires a valid name and an exact numeric version")
	}
	if _, exists := rt.PHP.Extensions[loadedExtensionName(name)]; exists {
		return ext, fmt.Errorf("%s is already compiled into this PHP runtime", name)
	}
	if err := s.verifyRuntime(rt); err != nil {
		return ext, err
	}
	absolute, err := filepath.Abs(file)
	if err != nil {
		return ext, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return ext, err
	}
	if !info.Mode().IsRegular() {
		return ext, errors.New("extension file must be a regular file")
	}
	checksum, err := fileSHA(absolute)
	if err != nil {
		return ext, err
	}
	if expectedSHA != "" && !strings.EqualFold(checksum, expectedSHA) {
		return ext, errors.New("extension SHA-256 mismatch")
	}
	temp, err := os.MkdirTemp(filepath.Join(s.Home, "tmp"), "extension-*")
	if err != nil {
		return ext, err
	}
	defer os.RemoveAll(temp)
	payload := filepath.Join(temp, "payload")
	module := filepath.Base(absolute)
	if packageDir == "" {
		if err := os.MkdirAll(payload, 0700); err != nil {
			return ext, err
		}
		if err := copyFile(absolute, filepath.Join(payload, module), info.Mode().Perm()); err != nil {
			return ext, err
		}
	} else {
		root, err := filepath.Abs(packageDir)
		if err != nil {
			return ext, err
		}
		module, err = filepath.Rel(root, absolute)
		if err != nil {
			return ext, err
		}
		if _, err := safeRelative(module); err != nil {
			return ext, err
		}
		if err := copyTree(root, payload); err != nil {
			return ext, err
		}
	}
	// Verify the copied file, rather than trusting a mutable import source.
	stagedSHA, err := fileSHA(filepath.Join(payload, module))
	if err != nil || stagedSHA != checksum {
		return ext, errors.New("extension changed while being imported")
	}
	digest, err := treeSHA(payload)
	if err != nil {
		return ext, err
	}
	if source.Kind == "" {
		source = Source{Kind: "local-import", SHA256: checksum}
	}
	ext = Extension{Schema: 1, Name: name, Version: version, RuntimeID: rt.ID, Module: filepath.ToSlash(module), Zend: zend, Digest: digest, Source: source}
	ext.ID = name + "-" + version + "-" + digestJSON(struct {
		Runtime, Digest string
		Zend            bool
	}{rt.ID, digest, zend})[:16]
	unlock, err := s.lock("extension:" + ext.ID)
	if err != nil {
		return ext, err
	}
	defer unlock()
	if _, err := os.Stat(s.extPath(ext.ID)); err == nil {
		existing, err := s.readExtension(ext.ID)
		if err != nil {
			return ext, err
		}
		if existing.Digest != ext.Digest || existing.RuntimeID != ext.RuntimeID || existing.Module != ext.Module || existing.Zend != ext.Zend {
			return ext, errors.New("extension ID collision or changed module selection; existing artifact was not overwritten")
		}
		return existing, s.verifyExtension(existing)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ext, err
	}
	if err := writeJSON(filepath.Join(temp, "manifest.json"), ext); err != nil {
		return ext, err
	}
	// Validate in the final location to catch dependent library lookup failures.
	if err := os.Rename(temp, s.extPath(ext.ID)); err != nil {
		return ext, err
	}
	if err := s.probeExtension(rt, ext); err != nil {
		_ = os.RemoveAll(s.extPath(ext.ID))
		return Extension{}, err
	}
	return ext, nil
}

func (s *Store) modulePath(ext Extension) string {
	return filepath.Join(s.extPath(ext.ID), "payload", filepath.FromSlash(ext.Module))
}

func (s *Store) probeExtension(rt Runtime, ext Extension) error {
	version, err := s.inspectExtension(rt, ext.Name, s.modulePath(ext), ext.Zend, filepath.Join(s.extPath(ext.ID), "payload"))
	if err != nil {
		return err
	}
	if version != ext.Version {
		return fmt.Errorf("extension probe expected %s %s, found %q", ext.Name, ext.Version, version)
	}
	return nil
}

func loadedExtensionName(name string) string {
	if name == "opcache" {
		return "zend opcache"
	}
	return name
}

func (s *Store) inspectExtension(rt Runtime, name, path string, zend bool, extraPaths ...string) (string, error) {
	key := "extension"
	if zend {
		key = "zend_extension"
	}
	loadedName := loadedExtensionName(name)
	code := fmt.Sprintf(`echo json_encode(['loaded'=>extension_loaded(%q),'version'=>(string)phpversion(%q)]);`, loadedName, loadedName)
	paths := []string{filepath.Dir(s.runtimeBinary(rt)), filepath.Dir(path)}
	paths = append(paths, extraPaths...)
	paths = append(paths, os.Getenv("PATH"))
	env := cleanEnv(map[string]string{"PATH": strings.Join(paths, string(os.PathListSeparator)), "PHPRC": filepath.Join(s.Home, "tmp"), "PHP_INI_SCAN_DIR": filepath.Join(s.Home, "tmp")})
	output, err := phpOutput(s.runtimeBinary(rt), []string{"-n", "-d", key + "=" + path, "-r", code}, env)
	if err != nil {
		return "", fmt.Errorf("extension %s is incompatible or has missing dependencies: %w", name, err)
	}
	var result struct {
		Loaded  bool   `json:"loaded"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("invalid extension probe output: %w", err)
	}
	if !result.Loaded || !versionPattern.MatchString(result.Version) || strings.Count(result.Version, ".") != 2 {
		return "", fmt.Errorf("extension %s did not load with a supported numeric version (loaded=%v version=%q)", name, result.Loaded, result.Version)
	}
	return result.Version, nil
}

func (s *Store) readExtension(id string) (Extension, error) {
	var ext Extension
	if !idPattern.MatchString(id) {
		return ext, errors.New("invalid extension ID")
	}
	if err := readJSON(filepath.Join(s.extPath(id), "manifest.json"), &ext); err != nil {
		return ext, err
	}
	if _, err := safeRelative(ext.Module); err != nil {
		return ext, err
	}
	if ext.Schema != 1 || ext.ID != id || !namePattern.MatchString(ext.Name) || !idPattern.MatchString(ext.RuntimeID) {
		return ext, errors.New("invalid extension manifest identity")
	}
	return ext, nil
}

func (s *Store) extensions() ([]Extension, error) {
	entries, err := os.ReadDir(filepath.Join(s.Home, "store", "extensions"))
	if err != nil {
		return nil, err
	}
	var results []Extension
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ext, err := s.readExtension(entry.Name())
		if err != nil {
			return nil, err
		}
		results = append(results, ext)
	}
	return results, nil
}

func (s *Store) resolveExtensions(cfg Config, rt Runtime) ([]Extension, error) {
	return s.resolveExtensionsWithPins(cfg, rt, nil)
}

func (s *Store) resolveExtensionsWithPins(cfg Config, rt Runtime, pins map[string]Extension) ([]Extension, error) {
	available, err := s.extensions()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cfg.Extensions))
	for name := range cfg.Extensions {
		names = append(names, name)
	}
	sort.Strings(names)
	var result []Extension
	for _, name := range names {
		version := cfg.Extensions[name]
		if builtin, ok := rt.PHP.Extensions[loadedExtensionName(name)]; ok {
			if builtin != version {
				return nil, fmt.Errorf("%s is compiled into PHP at version %q, cannot select %s", name, builtin, version)
			}
			continue
		}
		if pinned, ok := pins[name]; ok && pinned.RuntimeID == rt.ID && pinned.Version == version {
			result = append(result, pinned)
			continue
		}
		var matches []Extension
		for _, ext := range available {
			if ext.Name == name && ext.Version == version && ext.RuntimeID == rt.ID {
				matches = append(matches, ext)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s@%s is not imported for runtime %s; use phpm ext import", name, version, rt.ID)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("multiple builds of %s@%s match; retain an existing lock or use a separate PHPM_HOME", name, version)
		}
		result = append(result, matches[0])
	}
	return result, nil
}

func (s *Store) verifyExtension(ext Extension) error {
	sum, err := treeSHA(filepath.Join(s.extPath(ext.ID), "payload"))
	if err != nil {
		return err
	}
	if sum != ext.Digest {
		return fmt.Errorf("extension integrity check failed: %s", ext.ID)
	}
	return nil
}
