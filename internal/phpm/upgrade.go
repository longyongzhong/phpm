package phpm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type UpgradePlan struct {
	Schema       int               `json:"schema"`
	ID           string            `json:"id"`
	Project      string            `json:"project"`
	Platform     string            `json:"platform"`
	CreatedAt    time.Time         `json:"created_at"`
	BeforeConfig fileSnapshot      `json:"before_config"`
	BeforeLock   fileSnapshot      `json:"before_lock"`
	AfterConfig  []byte            `json:"after_config"`
	AfterLock    []byte            `json:"after_lock"`
	Environment  LockedEnvironment `json:"environment"`
}

type runtimeUpgradeRequest struct{ Version, RuntimeID string }
type runtimeAcquire func(context.Context, Runtime, runtimeUpgradeRequest) (Runtime, error)

func projectIdentity(root string) string {
	root = filepath.Clean(root)
	if runtime.GOOS == "windows" {
		return strings.ToLower(root)
	}
	return root
}

func (s *Store) projectStatePath(root string) string {
	return filepath.Join(s.Home, "projects", digestJSON(projectIdentity(root)))
}

func upgradePlanID(p UpgradePlan) string {
	return "upgrade-" + digestJSON(struct {
		Project, Platform, Environment, Config, Lock string
		BeforeConfig, BeforeLock                     fileSnapshot
		CreatedAt                                    time.Time
	}{projectIdentity(p.Project), p.Platform, p.Environment.ID, digestJSON(p.AfterConfig), digestJSON(p.AfterLock), p.BeforeConfig, p.BeforeLock, p.CreatedAt})
}

func (s *Store) acquireUpgradeRuntime(ctx context.Context, current Runtime, request runtimeUpgradeRequest) (Runtime, error) {
	if request.RuntimeID != "" {
		rt, err := s.readRuntime(request.RuntimeID)
		if err != nil {
			return rt, err
		}
		if request.Version != "" && !matchesVersion(rt.PHP.Version, request.Version) {
			return rt, errors.New("requested runtime does not match the target PHP version")
		}
		return rt, nil
	}
	version := request.Version
	if version == "" {
		parts := strings.Split(current.PHP.Version, ".")
		version = strings.Join(parts[:2], ".")
	}
	return s.installRuntime(version, current.PHP.ZTS, nil)
}

func comparePHPVersions(a, b string) (int, error) {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	if len(left) != 3 || len(right) != 3 {
		return 0, errors.New("PHP versions must have three numeric components")
	}
	for i := range left {
		x, err := strconv.Atoi(left[i])
		if err != nil || x < 0 {
			return 0, errors.New("invalid PHP version")
		}
		y, err := strconv.Atoi(right[i])
		if err != nil || y < 0 {
			return 0, errors.New("invalid PHP version")
		}
		if x < y {
			return -1, nil
		}
		if x > y {
			return 1, nil
		}
	}
	return 0, nil
}

// Preparation never writes project files. Providers populate only the shared
// immutable store, then the complete proposed environment is validated.
func (s *Store) prepareUpgrade(root string, request runtimeUpgradeRequest, acquireRuntime runtimeAcquire, acquireExtension extensionAcquire) (UpgradePlan, error) {
	var plan UpgradePlan
	if request.Version != "" && !versionPattern.MatchString(request.Version) {
		return plan, errors.New("target PHP must be major.minor or major.minor.patch")
	}
	if request.RuntimeID != "" && !idPattern.MatchString(request.RuntimeID) {
		return plan, errors.New("invalid target runtime ID")
	}
	unlock, err := s.lock("project:" + root)
	if err != nil {
		return plan, err
	}
	defer unlock()
	if err := checkProjectTransaction(root); err != nil {
		return plan, err
	}
	beforeConfig, err := snapshotFile(filepath.Join(root, "phpm.yaml"))
	if err != nil {
		return plan, err
	}
	beforeLock, err := snapshotFile(filepath.Join(root, "phpm.lock"))
	if err != nil {
		return plan, err
	}
	current, err := s.current(root)
	if err != nil {
		return plan, fmt.Errorf("a verified current environment is required before upgrading: %w", err)
	}
	cfg, err := decodeConfig(beforeConfig.Data)
	if err != nil {
		return plan, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	target, err := acquireRuntime(ctx, current.Runtime, request)
	if err != nil {
		return plan, err
	}
	if target.Platform != current.Runtime.Platform || target.PHP.ZTS != current.Runtime.PHP.ZTS || target.PHP.Debug != current.Runtime.PHP.Debug || target.PHP.IntSize != current.Runtime.PHP.IntSize {
		return plan, errors.New("upgrade must preserve the platform, architecture size, TS/NTS and debug mode")
	}
	if request.RuntimeID != "" && target.ID != request.RuntimeID {
		return plan, errors.New("runtime provider returned a different build than requested")
	}
	expectedVersion := request.Version
	if expectedVersion == "" && request.RuntimeID == "" {
		parts := strings.Split(current.Runtime.PHP.Version, ".")
		expectedVersion = strings.Join(parts[:2], ".")
	}
	if expectedVersion != "" && !matchesVersion(target.PHP.Version, expectedVersion) {
		return plan, errors.New("downloaded runtime does not match the requested PHP version")
	}
	order, err := comparePHPVersions(target.PHP.Version, current.Runtime.PHP.Version)
	if err != nil {
		return plan, err
	}
	if order < 0 {
		return plan, errors.New("php upgrade cannot downgrade PHP; use rollback for a retained environment")
	}
	if target.ID == current.Runtime.ID {
		return plan, errors.New("the selected PHP build is already current; no candidate was created")
	}
	if err := s.verifyRuntime(target); err != nil {
		return plan, err
	}
	if !matchesVersion(target.PHP.Version, cfg.PHP) {
		cfg.PHP = target.PHP.Version
		original, err := decodeConfig(beforeConfig.Data)
		if err != nil {
			return plan, err
		}
		if strings.Count(original.PHP, ".") == 1 {
			parts := strings.Split(target.PHP.Version, ".")
			cfg.PHP = strings.Join(parts[:2], ".")
		}
	}
	if cfg.Runtime != "" {
		cfg.Runtime = target.ID
	}
	pins := map[string]Extension{}
	oldExtensions := map[string]Extension{}
	for _, ext := range current.Extensions {
		oldExtensions[ext.Name] = ext
	}
	names := make([]string, 0, len(cfg.Extensions))
	for name := range cfg.Extensions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		old := oldExtensions[name]
		_, compiled := current.Runtime.PHP.Extensions[loadedExtensionName(name)]
		coupled := compiled || old.Source.Kind == "runtime-bundled" || (strings.Contains(runtimeBundledNames, " "+name+" ") && cfg.Extensions[name] == current.Runtime.PHP.Version)
		req := ExtensionRequest{Name: name, Version: cfg.Extensions[name], Zend: old.Zend}
		if coupled {
			req.Version, req.Zend = "", false
		}
		got, err := s.acquireUpgradeExtension(ctx, target, req, acquireExtension)
		if err != nil {
			return plan, fmt.Errorf("cannot prepare %s for PHP %s: %w", name, target.PHP.Version, err)
		}
		if coupled && got.Artifact != nil && got.Artifact.Source.Kind != "runtime-bundled" {
			return plan, fmt.Errorf("%s is no longer bundled with target PHP; explicit extension migration is required", name)
		}
		if !coupled && got.Version != cfg.Extensions[name] {
			return plan, fmt.Errorf("upgrade must preserve the declared version of %s", name)
		}
		if got.Artifact != nil {
			if got.Artifact.Name != name || got.Artifact.Version != got.Version || got.Artifact.RuntimeID != target.ID || (!coupled && got.Artifact.Zend != old.Zend) {
				return plan, errors.New("extension provider returned an unexpected identity or loader")
			}
			pins[name] = *got.Artifact
		} else if target.PHP.Extensions[loadedExtensionName(name)] != got.Version {
			return plan, fmt.Errorf("extension provider did not supply %s", name)
		}
		cfg.Extensions[name] = got.Version
	}
	if err := cfg.Validate(); err != nil {
		return plan, err
	}
	exts, err := s.resolveExtensionsWithPins(cfg, target, pins)
	if err != nil {
		return plan, err
	}
	env := LockedEnvironment{ConfigHash: digestJSON(cfg), Runtime: target, Extensions: exts, INI: cfg.INI}
	env.ID = environmentID(env)
	if err := s.validateLocked(env, cfg); err != nil {
		return plan, err
	}
	if err := s.materialize(env); err != nil {
		return plan, err
	}
	afterConfig, err := upgradeConfigYAML(beforeConfig.Data, cfg)
	if err != nil {
		return plan, err
	}
	lock, err := decodeLock(beforeLock.Data)
	if err != nil {
		return plan, err
	}
	lock.Targets[platformKey()] = env
	afterLock, err := jsonBytes(lock)
	if err != nil {
		return plan, err
	}
	if err := requireProjectSnapshots(root, beforeConfig, beforeLock); err != nil {
		return plan, err
	}
	plan = UpgradePlan{Schema: 1, Project: root, Platform: platformKey(), CreatedAt: time.Now().UTC(), BeforeConfig: beforeConfig, BeforeLock: beforeLock, AfterConfig: afterConfig, AfterLock: afterLock, Environment: env}
	plan.ID = upgradePlanID(plan)
	state := s.projectStatePath(root)
	if err := os.MkdirAll(state, 0700); err != nil {
		return plan, err
	}
	if err := writeJSON(filepath.Join(state, "candidate.json"), plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func (s *Store) acquireUpgradeExtension(ctx context.Context, rt Runtime, req ExtensionRequest, acquire extensionAcquire) (acquiredExtension, error) {
	if req.Version != "" {
		available, err := s.extensions()
		if err != nil {
			return acquiredExtension{}, err
		}
		var matches []Extension
		for _, ext := range available {
			if ext.RuntimeID == rt.ID && ext.Name == req.Name && ext.Version == req.Version && ext.Zend == req.Zend {
				matches = append(matches, ext)
			}
		}
		if len(matches) == 1 {
			return acquiredExtension{Version: req.Version, Artifact: &matches[0]}, nil
		}
		if len(matches) > 1 {
			return acquiredExtension{}, fmt.Errorf("multiple cached builds match %s@%s for target PHP", req.Name, req.Version)
		}
	}
	return acquire(ctx, rt, req)
}

func jsonBytes(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}

func decodeLock(data []byte) (Lockfile, error) {
	var lock Lockfile
	if err := decodeJSON(data, &lock); err != nil {
		return lock, err
	}
	if lock.Schema != 1 || lock.Targets == nil {
		return lock, errors.New("invalid lock schema")
	}
	return lock, nil
}

func requireProjectSnapshots(root string, config, lock fileSnapshot) error {
	actualConfig, err := snapshotFile(filepath.Join(root, "phpm.yaml"))
	if err != nil {
		return err
	}
	actualLock, err := snapshotFile(filepath.Join(root, "phpm.lock"))
	if err != nil {
		return err
	}
	if !sameSnapshot(actualConfig, config) || !sameSnapshot(actualLock, lock) {
		return errors.New("project files changed since preparation; prepare a new candidate before switching")
	}
	return nil
}

func upgradeConfigYAML(data []byte, cfg Config) ([]byte, error) {
	var err error
	data, err = setConfigScalar(data, "php", cfg.PHP)
	if err != nil {
		return nil, err
	}
	if cfg.Runtime != "" {
		data, err = setConfigScalar(data, "runtime", cfg.Runtime)
		if err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(cfg.Extensions))
	for name := range cfg.Extensions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data, err = setExtensionInYAML(data, name, cfg.Extensions[name])
		if err != nil {
			return nil, err
		}
	}
	actual, err := decodeConfig(data)
	if err != nil {
		return nil, err
	}
	if digestJSON(actual) != digestJSON(cfg) {
		return nil, errors.New("YAML aliases would change unrelated settings; expand the aliases before upgrading")
	}
	return data, nil
}

func setConfigScalar(data []byte, key, value string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration must be a mapping")
	}
	root := doc.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		node := root.Content[i+1]
		if node.Kind == yaml.ScalarNode && node.Value == value {
			return data, nil
		}
		if node.Kind == yaml.AliasNode {
			return nil, fmt.Errorf("expand the YAML alias for %s before upgrading", key)
		}
		node.Kind, node.Tag, node.Value, node.Content, node.Style = yaml.ScalarNode, "!!str", value, nil, yaml.DoubleQuotedStyle
		return encodeProjectYAML(&doc)
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle})
	return encodeProjectYAML(&doc)
}

func (s *Store) validateUpgradePlan(root string, p UpgradePlan) error {
	if p.Schema != 1 || p.Platform != platformKey() || projectIdentity(root) != projectIdentity(p.Project) || p.ID != upgradePlanID(p) || !p.BeforeConfig.Exists || !p.BeforeLock.Exists {
		return errors.New("invalid upgrade plan identity or project")
	}
	cfg, err := decodeConfig(p.AfterConfig)
	if err != nil {
		return err
	}
	lock, err := decodeLock(p.AfterLock)
	if err != nil {
		return err
	}
	if digestJSON(lock.Targets[platformKey()]) != digestJSON(p.Environment) {
		return errors.New("upgrade plan differs from its target lock")
	}
	if err := s.validateLocked(p.Environment, cfg); err != nil {
		return err
	}
	return s.verifyEnvironment(p.Environment)
}

func (s *Store) candidate(root string) (UpgradePlan, error) {
	var plan UpgradePlan
	if err := checkProjectTransaction(root); err != nil {
		return plan, err
	}
	if err := readJSON(filepath.Join(s.projectStatePath(root), "candidate.json"), &plan); err != nil {
		return plan, fmt.Errorf("no readable candidate; run phpm php upgrade first: %w", err)
	}
	if err := s.validateUpgradePlan(root, plan); err != nil {
		return plan, err
	}
	if err := requireProjectSnapshots(root, plan.BeforeConfig, plan.BeforeLock); err != nil {
		if !projectHasBytes(root, plan.AfterConfig, plan.AfterLock) {
			return plan, err
		}
	}
	return plan, nil
}

func projectHasBytes(root string, config, lock []byte) bool {
	a, err := snapshotFile(filepath.Join(root, "phpm.yaml"))
	if err != nil {
		return false
	}
	b, err := snapshotFile(filepath.Join(root, "phpm.lock"))
	if err != nil {
		return false
	}
	return a.Exists && b.Exists && bytes.Equal(a.Data, config) && bytes.Equal(b.Data, lock)
}

// A checkpoint is saved before switching, so even interruption after the
// project-file commit leaves a restore point. It is not proof of deployment.
func (s *Store) activateUpgrade(root, id string) (UpgradePlan, error) {
	unlock, err := s.lock("project:" + root)
	if err != nil {
		return UpgradePlan{}, err
	}
	defer unlock()
	if err := checkProjectTransaction(root); err != nil {
		return UpgradePlan{}, err
	}
	plan, err := s.candidate(root)
	if err != nil {
		return plan, err
	}
	if id != plan.ID {
		return plan, errors.New("candidate ID does not match; review the current candidate before activating")
	}
	if projectHasBytes(root, plan.AfterConfig, plan.AfterLock) {
		return plan, nil
	}
	if err := requireProjectSnapshots(root, plan.BeforeConfig, plan.BeforeLock); err != nil {
		return plan, err
	}
	if _, err := s.current(root); err != nil {
		return plan, fmt.Errorf("current environment is not a valid rollback target: %w", err)
	}
	history := filepath.Join(s.projectStatePath(root), "history")
	if err := os.MkdirAll(history, 0700); err != nil {
		return plan, err
	}
	if err := writeJSON(filepath.Join(history, plan.ID+".json"), plan); err != nil {
		return plan, err
	}
	if err := commitProjectFiles(root, plan.BeforeConfig, plan.BeforeLock, plan.AfterConfig, plan.AfterLock); err != nil {
		return plan, err
	}
	return plan, nil
}

func (s *Store) upgradeHistory(root string) ([]UpgradePlan, error) {
	entries, err := os.ReadDir(filepath.Join(s.projectStatePath(root), "history"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var plans []UpgradePlan
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var plan UpgradePlan
		if err := readJSON(filepath.Join(s.projectStatePath(root), "history", entry.Name()), &plan); err != nil {
			return nil, err
		}
		if plan.Schema != 1 || plan.ID != upgradePlanID(plan) || projectIdentity(root) != projectIdentity(plan.Project) || plan.Platform != platformKey() || entry.Name() != plan.ID+".json" {
			return nil, errors.New("invalid upgrade history entry")
		}
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].CreatedAt.After(plans[j].CreatedAt) })
	return plans, nil
}

func (s *Store) rollbackUpgrade(root, id string) (LockedEnvironment, error) {
	var env LockedEnvironment
	unlock, err := s.lock("project:" + root)
	if err != nil {
		return env, err
	}
	defer unlock()
	if err := checkProjectTransaction(root); err != nil {
		return env, err
	}
	plans, err := s.upgradeHistory(root)
	if err != nil {
		return env, err
	}
	for _, plan := range plans {
		if id != "" && id != plan.ID {
			continue
		}
		if !projectHasBytes(root, plan.AfterConfig, plan.AfterLock) {
			if id != "" {
				return env, errors.New("project files differ from the activated checkpoint; rollback would overwrite later edits")
			}
			continue
		}
		cfg, err := decodeConfig(plan.BeforeConfig.Data)
		if err != nil {
			return env, err
		}
		lock, err := decodeLock(plan.BeforeLock.Data)
		if err != nil {
			return env, err
		}
		var ok bool
		env, ok = lock.Targets[platformKey()]
		if !ok {
			return env, errors.New("checkpoint has no previous environment for this platform")
		}
		if err := s.validateLocked(env, cfg); err != nil {
			return env, err
		}
		if err := s.verifyEnvironment(env); err != nil {
			return env, err
		}
		currentConfig, err := snapshotFile(filepath.Join(root, "phpm.yaml"))
		if err != nil {
			return env, err
		}
		currentLock, err := snapshotFile(filepath.Join(root, "phpm.lock"))
		if err != nil {
			return env, err
		}
		if !bytes.Equal(currentConfig.Data, plan.AfterConfig) || !bytes.Equal(currentLock.Data, plan.AfterLock) {
			return env, errors.New("project files changed while validating rollback")
		}
		if err := commitProjectFiles(root, currentConfig, currentLock, plan.BeforeConfig.Data, plan.BeforeLock.Data); err != nil {
			return env, err
		}
		return env, nil
	}
	return env, errors.New("no retained checkpoint matches the current project; later edits will not be overwritten")
}
