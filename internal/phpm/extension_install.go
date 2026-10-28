package phpm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type ExtensionRequest struct {
	Name, Version, SHA256 string
	Zend                  bool
}

type acquiredExtension struct {
	Version  string
	Artifact *Extension // nil for an extension statically included in the runtime
}

type extensionAcquire func(context.Context, Runtime, ExtensionRequest) (acquiredExtension, error)

func (r ExtensionRequest) validate() error {
	if !namePattern.MatchString(r.Name) {
		return errors.New("extension name must contain lowercase letters, digits or underscores")
	}
	if r.Version != "" && (!versionPattern.MatchString(r.Version) || strings.Count(r.Version, ".") != 2) {
		return errors.New("extension version must be an exact major.minor.patch version")
	}
	if r.SHA256 != "" && !validSHA(r.SHA256) {
		return errors.New("invalid --sha256")
	}
	return nil
}

// Download/build first, verify the entire prospective environment, then change
// the manifest and lock together using a recoverable project transaction.
func (s *Store) installProjectExtension(root string, request ExtensionRequest, acquire extensionAcquire) (LockedEnvironment, string, error) {
	env, versions, err := s.installProjectExtensions(root, []ExtensionRequest{request}, acquire, nil, nil)
	return env, versions[request.Name], err
}

// Batch installations share one writer lock and commit. Expected pins the
// environment inspected by Composer; verify checks it again before committing.
func (s *Store) installProjectExtensions(root string, requests []ExtensionRequest, acquire extensionAcquire, expected *LockedEnvironment, verify func(LockedEnvironment) error) (LockedEnvironment, map[string]string, error) {
	var result LockedEnvironment
	versions := map[string]string{}
	for _, request := range requests {
		if err := request.validate(); err != nil {
			return result, nil, err
		}
		if _, exists := versions[request.Name]; exists {
			return result, nil, errors.New("duplicate extension in installation batch")
		}
		versions[request.Name] = ""
	}
	unlock, err := s.lock("project:" + root)
	if err != nil {
		return result, nil, err
	}
	defer unlock()
	if err := recoverProjectTransaction(root); err != nil {
		return result, nil, err
	}
	beforeConfig, err := snapshotFile(filepath.Join(root, "phpm.yaml"))
	if err != nil {
		return result, nil, err
	}
	if !beforeConfig.Exists {
		return result, nil, errors.New("no phpm.yaml; run phpm init first")
	}
	cfg, err := decodeConfig(beforeConfig.Data)
	if err != nil {
		return result, nil, err
	}
	beforeLock, err := snapshotFile(filepath.Join(root, "phpm.lock"))
	if err != nil {
		return result, nil, err
	}
	lock := Lockfile{Schema: 1, Targets: map[string]LockedEnvironment{}}
	if beforeLock.Exists {
		lock, err = loadLock(root)
		if err != nil {
			return result, nil, err
		}
	}
	previous, hasPrevious := lock.Targets[platformKey()]
	if hasPrevious && previous.ID != environmentID(previous) {
		return result, nil, errors.New("existing lock has an invalid environment identity")
	}
	if expected != nil && (!hasPrevious || previous.ID != expected.ID || digestJSON(cfg) != expected.ConfigHash) {
		return result, nil, errors.New("PHPM environment changed after Composer inspection; rerun --missing")
	}
	var rt Runtime
	if hasPrevious && matchesVersion(previous.Runtime.PHP.Version, cfg.PHP) && (cfg.Runtime == "" || cfg.Runtime == previous.Runtime.ID) {
		rt, err = s.readRuntime(previous.Runtime.ID)
		if err == nil {
			cached, pinned := rt, previous.Runtime
			cached.Source, pinned.Source = Source{}, Source{}
			if digestJSON(cached) != digestJSON(pinned) {
				err = errors.New("locked runtime metadata differs from the store")
			}
		}
	} else {
		rt, err = s.resolveRuntime(cfg)
	}
	if err != nil {
		return result, nil, err
	}
	if err := s.verifyRuntime(rt); err != nil {
		return result, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if cfg.Extensions == nil {
		cfg.Extensions = map[string]string{}
	}
	pins := map[string]Extension{}
	if hasPrevious {
		for _, ext := range previous.Extensions {
			if ext.RuntimeID == rt.ID && cfg.Extensions[ext.Name] == ext.Version {
				pins[ext.Name] = ext
			}
		}
	}
	afterConfig := beforeConfig.Data
	for _, request := range requests {
		got, err := acquire(ctx, rt, request)
		if err != nil {
			return result, nil, err
		}
		if request.Version != "" && request.Version != got.Version {
			return result, nil, errors.New("installer returned a different extension version")
		}
		if expected != nil && cfg.Extensions[request.Name] != "" && cfg.Extensions[request.Name] != got.Version {
			return result, nil, errors.New("--missing cannot change an already declared extension version; use ext install NAME@VERSION explicitly")
		}
		cfg.Extensions[request.Name] = got.Version
		versions[request.Name] = got.Version
		delete(pins, request.Name)
		if got.Artifact != nil {
			if got.Artifact.Name != request.Name || got.Artifact.Version != got.Version || got.Artifact.RuntimeID != rt.ID {
				return result, nil, errors.New("installer returned an unexpected extension identity")
			}
			pins[request.Name] = *got.Artifact
		} else if rt.PHP.Extensions[loadedExtensionName(request.Name)] != got.Version {
			return result, nil, errors.New("installer did not provide the requested module")
		}
		afterConfig, err = setExtensionInYAML(afterConfig, request.Name, got.Version)
		if err != nil {
			return result, nil, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return result, nil, err
	}
	exts, err := s.resolveExtensionsWithPins(cfg, rt, pins)
	if err != nil {
		return result, nil, err
	}
	result = LockedEnvironment{ConfigHash: digestJSON(cfg), Runtime: rt, Extensions: exts, INI: cfg.INI}
	result.ID = environmentID(result)
	if err := s.validateLocked(result, cfg); err != nil {
		return result, nil, err
	}
	if err := s.materialize(result); err != nil {
		return result, nil, err
	}
	roundTrip, err := decodeConfig(afterConfig)
	if err != nil {
		return result, nil, err
	}
	if digestJSON(roundTrip) != digestJSON(cfg) {
		return result, nil, errors.New("updating this YAML would also change aliased settings; expand the aliases before installing an extension")
	}
	lock.Targets[platformKey()] = result
	afterLock, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return result, nil, err
	}
	afterLock = append(afterLock, '\n')
	if verify != nil {
		if err := verify(result); err != nil {
			return result, nil, err
		}
	}
	if err := commitProjectFiles(root, beforeConfig, beforeLock, afterConfig, afterLock); err != nil {
		return result, nil, err
	}
	return result, versions, nil
}

func setExtensionInYAML(data []byte, name, version string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("project configuration must be a mapping")
	}
	root := doc.Content[0]
	var extensions *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "extensions" {
			extensions = root.Content[i+1]
			break
		}
	}
	if extensions == nil {
		extensions = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "extensions"}, extensions)
	}
	if extensions.Kind == yaml.ScalarNode && extensions.Tag == "!!null" {
		extensions.Kind, extensions.Tag, extensions.Value = yaml.MappingNode, "!!map", ""
	}
	if extensions.Kind != yaml.MappingNode {
		return nil, errors.New("automatic installation requires an extensions mapping without aliases")
	}
	for i := 0; i < len(extensions.Content); i += 2 {
		if extensions.Content[i].Value != name {
			continue
		}
		node := extensions.Content[i+1]
		if node.Kind == yaml.ScalarNode && node.Value == version {
			return data, nil
		}
		node.Kind, node.Tag, node.Value, node.Content = yaml.ScalarNode, "!!str", version, nil
		node.Style = yaml.DoubleQuotedStyle
		return encodeProjectYAML(&doc)
	}
	extensions.Content = append(extensions.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: version, Style: yaml.DoubleQuotedStyle})
	return encodeProjectYAML(&doc)
}

func encodeProjectYAML(node *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
