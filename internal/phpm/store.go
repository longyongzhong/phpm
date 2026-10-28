package phpm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type PHPInfo struct {
	Version    string            `json:"version"`
	ZTS        bool              `json:"zts"`
	Debug      bool              `json:"debug"`
	IntSize    int               `json:"int_size"`
	Machine    string            `json:"machine"`
	API        string            `json:"api"`
	ZendAPI    string            `json:"zend_api"`
	Build      string            `json:"build"`
	ZendBuild  string            `json:"zend_build"`
	Compiler   string            `json:"compiler"`
	Configure  string            `json:"configure"`
	Extensions map[string]string `json:"extensions"`
}

type Source struct {
	Kind    string `json:"kind"`
	URL     string `json:"url,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Catalog string `json:"catalog,omitempty"`
	Build   string `json:"build,omitempty"`
}

type Runtime struct {
	Schema   int     `json:"schema"`
	ID       string  `json:"id"`
	Platform string  `json:"platform"`
	Binary   string  `json:"binary"`
	Digest   string  `json:"digest"`
	PHP      PHPInfo `json:"php"`
	Source   Source  `json:"source"`
}

type Extension struct {
	Schema    int    `json:"schema"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	RuntimeID string `json:"runtime_id"`
	Module    string `json:"module"`
	Zend      bool   `json:"zend"`
	Digest    string `json:"digest"`
	Source    Source `json:"source"`
}

type Store struct{ Home string }

func platformKey() string { return runtime.GOOS + "-" + runtime.GOARCH }

func openStore(home string) (*Store, error) {
	if home == "" {
		home = os.Getenv("PHPM_HOME")
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".phpm")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{"store/runtimes", "store/extensions", "environments", "tmp", "locks", "cache", "projects", "sources"} {
		if err := os.MkdirAll(filepath.Join(abs, dir), 0700); err != nil {
			return nil, err
		}
	}
	return &Store{Home: abs}, nil
}

func (s *Store) runtimePath(id string) string { return filepath.Join(s.Home, "store", "runtimes", id) }
func (s *Store) extPath(id string) string     { return filepath.Join(s.Home, "store", "extensions", id) }

func (s *Store) lock(key string) (func(), error) {
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	path := filepath.Join(s.Home, "locks", digestJSON(key)+".lock")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("another PHPM operation holds %s; if it crashed, confirm it has exited before removing the lock", path)
	}
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintf(f, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		os.Remove(path)
		return nil, errors.Join(err, closeErr)
	}
	return func() { _ = os.Remove(path) }, nil
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Directory identity includes names, executable bits and content. Symlinks are
// rejected: imported packages must be self-contained trees of regular files.
func treeSHA(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported in imported packages: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular package file: %s", path)
		}
		rel, _ := filepath.Rel(root, path)
		sum, err := fileSHA(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%o\x00%s\n", filepath.ToSlash(rel), info.Mode().Perm()&0111, sum)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

func copyTree(source, target string) error {
	src, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	dst, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(src, dst)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return errors.New("package destination must not be inside the source directory")
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported in imported packages: %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s", path)
		}
		return copyFile(path, dest, info.Mode().Perm())
	})
}

func copyFile(source, target string, mode os.FileMode) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return errors.Join(err, dst.Close())
}

func cleanEnv(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		found := false
		for override := range overrides {
			if strings.EqualFold(key, override) {
				found = true
				break
			}
		}
		if !found {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func phpOutput(binary string, args, env []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("PHP probe timed out: %w", ctx.Err())
	}
	if err == nil && strings.TrimSpace(stderr.String()) != "" {
		return nil, fmt.Errorf("PHP emitted startup diagnostics:\n%s", strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		return nil, fmt.Errorf("PHP probe failed: %v\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

const phpProbe = `$e=[]; foreach(get_loaded_extensions() as $n){$e[strtolower($n)]=(string)phpversion($n);} echo json_encode(['version'=>PHP_VERSION,'zts'=>(bool)PHP_ZTS,'debug'=>(bool)PHP_DEBUG,'int_size'=>PHP_INT_SIZE,'machine'=>php_uname('m'),'extensions'=>$e]);`

func probePHP(binary string) (PHPInfo, error) {
	var info PHPInfo
	env := cleanEnv(map[string]string{"PHP_INI_SCAN_DIR": filepath.Dir(binary), "PHPRC": filepath.Dir(binary)})
	output, err := phpOutput(binary, []string{"-n", "-r", phpProbe}, env)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return info, fmt.Errorf("invalid PHP probe output: %w", err)
	}
	if !versionPattern.MatchString(info.Version) || strings.Count(info.Version, ".") != 2 {
		return info, errors.New("only stable numeric PHP releases are supported")
	}
	if (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") && info.IntSize != 8 {
		return info, errors.New("this PHPM build requires a native 64-bit PHP runtime")
	}
	output, err = phpOutput(binary, []string{"-n", "-i"}, env)
	if err != nil {
		return info, err
	}
	fields := map[string]*string{"PHP API": &info.API, "PHP Extension Build": &info.Build, "Zend Extension Build": &info.ZendBuild, "Zend Extension": &info.ZendAPI, "Compiler": &info.Compiler, "Configure Command": &info.Configure}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, " => ")
		if field := fields[strings.TrimSpace(key)]; ok && field != nil {
			*field = strings.TrimSpace(value)
		}
	}
	if info.API == "" || info.Build == "" || info.ZendBuild == "" {
		return info, errors.New("PHP did not report its API/build identity")
	}
	return info, nil
}

func (s *Store) importRuntime(dir, binary string, source Source) (Runtime, error) {
	var result Runtime
	if binary == "" {
		binary = "bin/php"
		if runtime.GOOS == "windows" {
			binary = "php.exe"
		}
	}
	rel, err := safeRelative(binary)
	if err != nil {
		return result, err
	}
	temp, err := os.MkdirTemp(filepath.Join(s.Home, "tmp"), "runtime-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(temp)
	payload := filepath.Join(temp, "payload")
	if err := copyTree(dir, payload); err != nil {
		return result, err
	}
	info, err := probePHP(filepath.Join(payload, rel))
	if err != nil {
		return result, err
	}
	digest, err := treeSHA(payload)
	if err != nil {
		return result, err
	}
	id := "php-" + info.Version + "-" + platformKey() + "-" + digest[:16]
	result = Runtime{Schema: 1, ID: id, Platform: platformKey(), Binary: filepath.ToSlash(rel), Digest: digest, PHP: info, Source: source}
	unlock, err := s.lock("runtime:" + id)
	if err != nil {
		return result, err
	}
	defer unlock()
	if _, err := os.Stat(s.runtimePath(id)); err == nil {
		existing, err := s.readRuntime(id)
		if err != nil {
			return result, err
		}
		if existing.Digest != result.Digest || existing.Binary != result.Binary || digestJSON(existing.PHP) != digestJSON(result.PHP) {
			return result, errors.New("runtime ID collision or changed build metadata; existing artifact was not overwritten")
		}
		if err := s.verifyRuntime(existing); err != nil {
			return existing, err
		}
		// Provenance is not payload identity. An explicit verified catalog install
		// can adopt an identical local import without changing its files or ID.
		if source.Kind == "signed-catalog" && existing.Source != source {
			existing.Source = source
			if err := writeJSON(filepath.Join(s.runtimePath(id), "manifest.json"), existing); err != nil {
				return Runtime{}, err
			}
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if err := writeJSON(filepath.Join(temp, "manifest.json"), result); err != nil {
		return result, err
	}
	if err := os.Rename(temp, s.runtimePath(id)); err != nil {
		return result, err
	}
	// A copied runtime may contain absolute library paths. Probe its final
	// location as well; never register a runtime that only works while staging.
	if _, err := probePHP(s.runtimeBinary(result)); err != nil {
		_ = os.RemoveAll(s.runtimePath(id))
		return Runtime{}, fmt.Errorf("runtime failed after relocation: %w", err)
	}
	return result, nil
}

func (s *Store) runtimeBinary(rt Runtime) string {
	return filepath.Join(s.runtimePath(rt.ID), "payload", filepath.FromSlash(rt.Binary))
}

func (s *Store) readRuntime(id string) (Runtime, error) {
	var rt Runtime
	if !idPattern.MatchString(id) {
		return rt, errors.New("invalid runtime ID")
	}
	if err := readJSON(filepath.Join(s.runtimePath(id), "manifest.json"), &rt); err != nil {
		return rt, err
	}
	if _, err := safeRelative(rt.Binary); err != nil {
		return rt, err
	}
	if rt.Schema != 1 || rt.ID != id || rt.Platform != platformKey() {
		return rt, errors.New("runtime manifest identity/platform mismatch")
	}
	return rt, nil
}

func (s *Store) runtimes() ([]Runtime, error) {
	entries, err := os.ReadDir(filepath.Join(s.Home, "store", "runtimes"))
	if err != nil {
		return nil, err
	}
	var results []Runtime
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rt, err := s.readRuntime(entry.Name())
		if err != nil {
			return nil, err
		}
		results = append(results, rt)
	}
	sort.Slice(results, func(i, j int) bool {
		var a, b [3]int
		fmt.Sscanf(results[i].PHP.Version, "%d.%d.%d", &a[0], &a[1], &a[2])
		fmt.Sscanf(results[j].PHP.Version, "%d.%d.%d", &b[0], &b[1], &b[2])
		for k := range a {
			if a[k] != b[k] {
				return a[k] > b[k]
			}
		}
		return results[i].ID < results[j].ID
	})
	return results, nil
}

func (s *Store) resolveRuntime(cfg Config) (Runtime, error) {
	if cfg.Runtime != "" {
		rt, err := s.readRuntime(cfg.Runtime)
		if err != nil {
			return rt, err
		}
		if !matchesVersion(rt.PHP.Version, cfg.PHP) {
			return rt, errors.New("runtime ID does not satisfy the configured PHP version")
		}
		return rt, nil
	}
	runtimes, err := s.runtimes()
	if err != nil {
		return Runtime{}, err
	}
	var matches []Runtime
	for _, rt := range runtimes {
		if matchesVersion(rt.PHP.Version, cfg.PHP) {
			matches = append(matches, rt)
		}
	}
	if len(matches) == 0 {
		return Runtime{}, fmt.Errorf("PHP %s is not installed; use phpm php import or phpm php install", cfg.PHP)
	}
	if len(matches) > 1 && matches[0].PHP.Version == matches[1].PHP.Version {
		return Runtime{}, errors.New("multiple builds match this PHP version; set runtime in phpm.yaml to an explicit ID")
	}
	return matches[0], nil
}

func (s *Store) verifyRuntime(rt Runtime) error {
	sum, err := treeSHA(filepath.Join(s.runtimePath(rt.ID), "payload"))
	if err != nil {
		return err
	}
	if sum != rt.Digest {
		return fmt.Errorf("runtime integrity check failed: %s", rt.ID)
	}
	return nil
}
