package phpm

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed composer_check.php
var composerCheckSource string

var composerCheckPHP = strings.TrimPrefix(composerCheckSource, "<?php")

// Resolve the PHP payload, never execute a batch wrapper that might select a
// different PHP. Explicit PHPM_COMPOSER takes precedence over absolute PATH dirs.
func resolveComposer() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("PHPM_COMPOSER")); configured != "" {
		path, err := filepath.Abs(configured)
		if err != nil {
			return "", err
		}
		if err := composerPHPFile(path); err != nil {
			return "", fmt.Errorf("PHPM_COMPOSER: %w", err)
		}
		return path, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, name := range []string{"composer.phar", "composer"} {
			path := filepath.Join(dir, name)
			if composerPHPFile(path) == nil {
				return path, nil
			}
		}
	}
	return "", errors.New("Composer PHP payload not found; install Composer or set PHPM_COMPOSER to a trusted composer.phar (Windows composer.bat installations usually have an adjacent composer.phar)")
}

func composerPHPFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("Composer must be a regular PHP/PHAR file")
	}
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".bat" || ext == ".cmd" || ext == ".exe" {
		return errors.New("point to composer.phar, not a batch/native launcher")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	header, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return err
	}
	if !bytes.Contains(header, []byte("<?php")) {
		return errors.New("Composer payload has no PHP opening tag")
	}
	return nil
}

func (c *CLI) projectEnvironment(root string, candidate bool) (LockedEnvironment, error) {
	if candidate {
		plan, err := c.Store.candidate(root)
		return plan.Environment, err
	}
	return c.Store.current(root)
}

func (c *CLI) composer(args []string) (int, error) {
	candidate := len(args) > 0 && args[0] == "--candidate"
	if candidate {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	root, err := findProject(".")
	if err != nil {
		return 0, err
	}
	env, err := c.projectEnvironment(root, candidate)
	if err != nil {
		return 0, err
	}
	return c.runComposer(env, args)
}

func (c *CLI) runComposer(env LockedEnvironment, args []string) (int, error) {
	payload, err := resolveComposer()
	if err != nil {
		return 0, err
	}
	phpArgs := []string{"-c", filepath.Join(c.Store.environmentPath(env), "php.ini"), payload}
	phpArgs = append(phpArgs, args...)
	return execProcess(c.Store.runtimeBinary(env.Runtime), phpArgs, c.Store.environmentVars(env, c.Store.environmentPath(env)), c.In, c.Out, c.Err)
}

type ComposerRequirement struct {
	Source     string `json:"source"`
	Constraint string `json:"constraint"`
	Type       string `json:"type"`
}

type ComposerPlatformResult struct {
	Name         string                `json:"name"`
	Version      string                `json:"version"`
	Status       string                `json:"status"`
	Provider     string                `json:"provider,omitempty"`
	Requirements []ComposerRequirement `json:"requirements"`
}

type ComposerCheckReport struct {
	Scope            string                   `json:"scope"`
	ComposerFile     string                   `json:"composer_file"`
	RuntimeID        string                   `json:"runtime_id"`
	LockFresh        *bool                    `json:"lock_fresh,omitempty"`
	Warnings         []string                 `json:"warnings"`
	Diagnostics      []ComposerDiagnostic     `json:"diagnostics,omitempty"`
	Results          []ComposerPlatformResult `json:"results"`
	MatchingVersions map[string][]string      `json:"matching_versions,omitempty"`
	inputHash        string
}

type ComposerDiagnostic struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

type composerCheckInput struct {
	Root              json.RawMessage     `json:"root"`
	RootJSON          string              `json:"root_json"`
	Lock              json.RawMessage     `json:"lock"`
	NoDev             bool                `json:"no_dev"`
	CandidateVersions map[string][]string `json:"candidate_versions,omitempty"`
}

func findComposerFile(projectRoot, cwd, configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		if !filepath.IsAbs(configured) {
			configured = filepath.Join(cwd, configured)
		}
		if _, err := os.Stat(configured); err != nil {
			return "", err
		}
		return filepath.Abs(configured)
	}
	dir, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, "composer.json")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if projectIdentity(dir) == projectIdentity(projectRoot) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("no composer.json found inside this PHPM project")
}

func composerLockPath(path string) string {
	if strings.HasSuffix(path, ".json") {
		return strings.TrimSuffix(path, ".json") + ".lock"
	}
	return path + ".lock"
}

func readComposerObject(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", filepath.Base(path), err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", filepath.Base(path))
	}
	return data, nil
}

func (s *Store) checkComposer(root string, env LockedEnvironment, noDev, requireLock bool) (ComposerCheckReport, error) {
	return s.checkComposerVersions(root, env, noDev, requireLock, nil)
}

func (s *Store) checkComposerVersions(root string, env LockedEnvironment, noDev, requireLock bool, candidates map[string][]string) (ComposerCheckReport, error) {
	var report ComposerCheckReport
	cwd, err := os.Getwd()
	if err != nil {
		return report, err
	}
	path, err := findComposerFile(root, cwd, os.Getenv("COMPOSER"))
	if err != nil {
		return report, err
	}
	data, err := readComposerObject(path)
	if err != nil {
		return report, err
	}
	lockPath := composerLockPath(path)
	lock, err := readComposerObject(lockPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	if errors.Is(err, os.ErrNotExist) {
		lock = nil
	}
	if requireLock && lock == nil {
		return report, errors.New("--lock requires an existing Composer lock file")
	}
	payload, err := resolveComposer()
	if err != nil {
		return report, err
	}
	checkInput := composerCheckInput{Root: data, RootJSON: string(data), Lock: lock, NoDev: noDev}
	inputHash := digestJSON(struct {
		Path       string
		Root, Lock []byte
		NoDev      bool
	}{path, data, lock, noDev})
	checkInput.CandidateVersions = candidates
	input, err := json.Marshal(checkInput)
	if err != nil {
		return report, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.runtimeBinary(env.Runtime), "-c", filepath.Join(s.environmentPath(env), "php.ini"), "-d", "display_errors=stderr", "-r", composerCheckPHP, "--", payload)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = s.environmentVars(env, s.environmentPath(env))
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return report, fmt.Errorf("Composer requirement check timed out: %w", ctx.Err())
	}
	if runErr != nil {
		return report, fmt.Errorf("Composer requirement checker failed: %w\n%s", runErr, strings.TrimSpace(stderr.String()))
	}
	if stderr.Len() != 0 {
		return report, fmt.Errorf("Composer requirement checker emitted diagnostics; use a Composer 2 PHAR compatible with the project PHP:\n%s", strings.TrimSpace(stderr.String()))
	}
	var response struct {
		ComposerCheckReport
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return report, fmt.Errorf("invalid Composer checker output: %w", err)
	}
	if response.Error != "" {
		return report, errors.New(response.Error)
	}
	report = response.ComposerCheckReport
	if report.Scope != "root" && report.Scope != "root+lock" {
		return report, errors.New("Composer checker did not report its inspection scope")
	}
	if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, data) {
		return report, errors.New("composer.json changed during inspection; rerun the check")
	}
	actualLock, err := os.ReadFile(lockPath)
	if lock == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return report, errors.New("Composer lock appeared during inspection; rerun the check")
		}
	} else if err != nil || !bytes.Equal(actualLock, lock) {
		return report, errors.New("Composer lock changed during inspection; rerun the check")
	}
	report.ComposerFile, report.RuntimeID = path, env.Runtime.ID
	report.inputHash = inputHash
	return report, nil
}

func composerCheckCode(report ComposerCheckReport) int {
	for _, result := range report.Results {
		if result.Status != "success" {
			return 1
		}
	}
	if report.LockFresh != nil && !*report.LockFresh {
		return 1
	}
	return 0
}

func writeComposerCheck(out io.Writer, report ComposerCheckReport) {
	w := bufio.NewWriter(out)
	defer w.Flush()
	fmt.Fprintf(w, "Composer requirements: %s (%s)\nPHP build: %s\n", report.ComposerFile, report.Scope, report.RuntimeID)
	for _, warning := range report.Warnings {
		fmt.Fprintln(w, "Note:", warning)
	}
	for _, result := range report.Results {
		fmt.Fprintf(w, "%-9s %-24s %s", result.Status, result.Name, result.Version)
		if result.Provider != "" {
			fmt.Fprintf(w, " (provided by %s)", result.Provider)
		}
		fmt.Fprintln(w)
		if result.Status != "success" {
			for _, requirement := range result.Requirements {
				fmt.Fprintf(w, "  %s %s %s\n", requirement.Source, requirement.Type, requirement.Constraint)
			}
		}
	}
	if composerCheckCode(report) == 0 {
		fmt.Fprintln(w, "All inspected platform requirements are satisfied.")
	} else {
		fmt.Fprintln(w, "Resolve missing/version-mismatched requirements and any stale lock before installing dependencies.")
	}
}
