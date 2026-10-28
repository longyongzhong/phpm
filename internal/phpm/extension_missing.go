package phpm

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Refuse issues that installing missing extensions cannot fix before downloading.
// In particular, this command never upgrades PHP or existing extension versions.
func missingExtensionRequests(report ComposerCheckReport) ([]ExtensionRequest, map[string]string, error) {
	if report.Scope == "root+lock" && (report.LockFresh == nil || !*report.LockFresh) {
		return nil, nil, errors.New("Composer lock is stale or its freshness cannot be verified; regenerate it with Composer before --missing")
	}
	var requests []ExtensionRequest
	packages := map[string]string{}
	var issues []string
	for _, row := range report.Results {
		if row.Status == "success" {
			continue
		}
		var requirements []string
		for _, requirement := range row.Requirements {
			requirements = append(requirements, requirement.Source+" "+requirement.Constraint)
		}
		detail := row.Name + " (" + strings.Join(requirements, "; ") + ")"
		if row.Status != "missing" || !strings.HasPrefix(row.Name, "ext-") {
			issues = append(issues, detail+": installed version "+row.Version+"; resolve explicitly before installing missing extensions")
			continue
		}
		name := strings.TrimPrefix(row.Name, "ext-")
		if name == "zend-opcache" {
			name = "opcache"
		}
		request := ExtensionRequest{Name: name}
		if err := request.validate(); err != nil {
			issues = append(issues, detail+": extension name cannot be mapped automatically; use ext import/install explicitly")
			continue
		}
		if _, exists := packages[name]; exists {
			issues = append(issues, detail+": ambiguous extension package mapping")
			continue
		}
		packages[name] = row.Name
		requests = append(requests, request)
	}
	if len(issues) != 0 {
		return nil, nil, fmt.Errorf("--missing cannot resolve these platform requirements:\n  %s", strings.Join(issues, "\n  "))
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].Name < requests[j].Name })
	return requests, packages, nil
}

func (s *Store) installMissingExtensions(root string, noDev, requireLock bool, provider windowsExtensionProvider) (LockedEnvironment, map[string]string, ComposerCheckReport, error) {
	env, err := s.current(root)
	if err != nil {
		return env, nil, ComposerCheckReport{}, err
	}
	report, err := s.checkComposer(root, env, noDev, requireLock)
	if err != nil {
		return env, nil, report, err
	}
	requests, packages, err := missingExtensionRequests(report)
	if err != nil || len(requests) == 0 {
		return env, nil, report, err
	}
	provider.Store = s
	provider.FilterVersions = func(name string, versions []string) ([]string, error) {
		packageName := packages[name]
		if packageName == "" {
			return nil, errors.New("installer requested an extension outside the Composer plan")
		}
		checked, err := s.checkComposerVersions(root, env, noDev, requireLock, map[string][]string{packageName: versions})
		if err != nil {
			return nil, err
		}
		if checked.inputHash != report.inputHash {
			return nil, errors.New("Composer inputs changed during installation; no project files were updated, rerun --missing")
		}
		return checked.MatchingVersions[packageName], nil
	}
	verify := func(candidate LockedEnvironment) error {
		checked, err := s.checkComposer(root, candidate, noDev, requireLock)
		if err != nil {
			return err
		}
		if checked.inputHash != report.inputHash {
			return errors.New("Composer inputs changed during installation; no project files were updated, rerun --missing")
		}
		if composerCheckCode(checked) != 0 {
			var output bytes.Buffer
			writeComposerCheck(&output, checked)
			return fmt.Errorf("prospective environment does not satisfy Composer; no project files were updated:\n%s", &output)
		}
		return nil
	}
	installed, versions, err := s.installProjectExtensions(root, requests, provider.acquire, &env, verify)
	return installed, versions, report, err
}
