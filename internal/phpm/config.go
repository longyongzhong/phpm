package phpm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Release builds set Version with -ldflags -X; ordinary development builds retain this value.
var Version = "0.1.0-dev"

var (
	versionPattern = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	idPattern      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,159}$`)
	iniPattern     = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.]*$`)
)

type Config struct {
	Schema     int               `yaml:"schema" json:"schema"`
	PHP        string            `yaml:"php" json:"php"`
	Runtime    string            `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Extensions map[string]string `yaml:"extensions,omitempty" json:"extensions,omitempty"`
	INI        map[string]string `yaml:"ini,omitempty" json:"ini,omitempty"`
}

func (c Config) Validate() error {
	if c.Schema != 1 {
		return fmt.Errorf("unsupported configuration schema %d (expected 1)", c.Schema)
	}
	if !versionPattern.MatchString(c.PHP) {
		return errors.New("php must be a major.minor or exact major.minor.patch version")
	}
	if c.Runtime != "" && !idPattern.MatchString(c.Runtime) {
		return errors.New("invalid runtime ID")
	}
	for name, version := range c.Extensions {
		if !namePattern.MatchString(name) || !versionPattern.MatchString(version) || strings.Count(version, ".") != 2 {
			return fmt.Errorf("extension %q must have a valid module name and exact numeric version", name)
		}
	}
	for key, value := range c.INI {
		if !iniPattern.MatchString(key) || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("invalid INI setting %q", key)
		}
		switch strings.ToLower(key) {
		case "extension", "zend_extension", "extension_dir":
			return fmt.Errorf("INI setting %q is managed by PHPM", key)
		}
	}
	return nil
}

func findProject(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "phpm.yaml")); err == nil && !info.IsDir() {
			return filepath.EvalSymlinks(dir)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no phpm.yaml found; run phpm init first")
		}
		dir = parent
	}
}

func readConfig(root string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(root, "phpm.yaml"))
	if err != nil {
		return Config{}, err
	}
	return decodeConfig(data)
}

func decodeConfig(data []byte) (Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid phpm.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, errors.New("phpm.yaml must contain exactly one YAML document")
	}
	// Empty YAML maps and omitted optional maps have the same semantics and
	// must round-trip identically through JSON's omitempty lock encoding.
	if len(cfg.INI) == 0 {
		cfg.INI = nil
	}
	if len(cfg.Extensions) == 0 {
		cfg.Extensions = nil
	}
	return cfg, cfg.Validate()
}

func digestJSON(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := decodeJSON(data, value); err != nil {
		return fmt.Errorf("invalid %s: %w", filepath.Base(path), err)
	}
	return nil
}

func decodeJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0600)
}

// Atomic replacement is performed in the destination directory; a failed write
// never truncates the previously committed file.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".phpm-write-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceFile(temp, path)
}

func matchesVersion(actual, requested string) bool {
	return actual == requested || (strings.Count(requested, ".") == 1 && strings.HasPrefix(actual, requested+"."))
}

func safeRelative(path string) (string, error) {
	normalized := strings.ReplaceAll(path, `\`, "/")
	clean := filepath.Clean(filepath.FromSlash(normalized))
	if path == "" || strings.HasPrefix(normalized, "/") || clean == "." || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.ContainsAny(clean, "\x00\r\n:") {
		return "", fmt.Errorf("expected a relative path inside the package, got %q", path)
	}
	return clean, nil
}
