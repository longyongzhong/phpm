package phpm

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRejectsAmbiguousAndInjectedInput(t *testing.T) {
	cases := map[string]string{
		"unknown field":       "schema: 1\nphp: '8.4'\nphpp: '8.2'\n",
		"duplicate key":       "schema: 1\nphp: '8.4'\nphp: '8.2'\n",
		"multiple documents":  "schema: 1\nphp: '8.4'\n---\nphp: '8.2'\n",
		"unsupported schema":  "schema: 2\nphp: '8.4'\n",
		"version expression":  "schema: 1\nphp: '^8.4'\n",
		"extension not exact": "schema: 1\nphp: '8.4'\nextensions:\n  redis: '6.2'\n",
		"loader override":     "schema: 1\nphp: '8.4'\nini:\n  EXTENSION: '/tmp/other.so'\n",
		"INI injection":       "schema: 1\nphp: '8.4'\nini:\n  memory_limit: \"256M\\nextension=bad.so\"\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "phpm.yaml"), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readConfig(root); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestProjectDiscoveryAndCanonicalConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "phpm.yaml"), []byte("schema: 1\nphp: '8.4'\nextensions:\n  redis: '6.2.0'\nini:\n  memory_limit: '256M'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	found, err := findProject(child)
	canonicalRoot, canonicalErr := filepath.EvalSymlinks(root)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if err != nil || found != canonicalRoot {
		t.Fatalf("found=%q err=%v", found, err)
	}
	cfg, err := readConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Extensions["redis"] != "6.2.0" || cfg.INI["memory_limit"] != "256M" {
		t.Fatalf("unexpected configuration: %+v", cfg)
	}
}

func TestSafeRelativeRejectsEscapes(t *testing.T) {
	for _, path := range []string{"", ".", "..", "../secret", "C:\\secret", "/secret", "file:stream", "bad\x00name"} {
		if _, err := safeRelative(path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	if _, err := safeRelative(filepath.Join("ext", "redis.so")); err != nil {
		t.Fatal(err)
	}
}

func TestFlagsAfterPositionals(t *testing.T) {
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	file := set.String("file", "", "")
	zend := set.Bool("zend", false, "")
	pos, err := parseFlags(set, []string{"xdebug@3.4.0", "--file", "a path/xdebug.so", "--zend"})
	if err != nil || len(pos) != 1 || *file != "a path/xdebug.so" || !*zend {
		t.Fatalf("pos=%v err=%v", pos, err)
	}
	if _, err := parseFlags(set, []string{"--typo"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestAtomicReplacementPreservesCompleteFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.json")
	for _, value := range []string{"first", "replacement"} {
		if err := atomicWrite(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != value {
			t.Fatalf("got=%q err=%v", got, err)
		}
	}
}

func TestHelpDoesNotCreateHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "unused")
	t.Setenv("PHPM_HOME", home)
	var output, stderr bytes.Buffer
	if code := Main([]string{"--help"}, nil, &output, &stderr); code != 0 || !strings.Contains(output.String(), "PHPM") {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("help created home: %v", err)
	}
}

func TestEmptyOptionalMapsRoundTripLikeOmittedMaps(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "phpm.yaml"), []byte("schema: 1\nphp: '8.4'\nextensions: {}\nini: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.INI != nil || cfg.Extensions != nil {
		t.Fatal("empty optional maps were not normalized")
	}
	if digestJSON(cfg) != digestJSON(Config{Schema: 1, PHP: "8.4"}) {
		t.Fatal("equivalent configuration changed lock identity")
	}
}
