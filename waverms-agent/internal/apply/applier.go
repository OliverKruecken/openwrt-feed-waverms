// Package apply implements two-phase atomic UCI config apply for the WaveRMS agent.
package apply

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/OliverKruecken/waverms-agent/internal/uci"
)

// Applier stages and commits a desired UCI config payload using a UCIRunner.
type Applier struct {
	runner uci.UCIRunner
}

// New creates an Applier backed by the given UCIRunner.
func New(runner uci.UCIRunner) *Applier {
	return &Applier{runner: runner}
}

// Apply applies the desired config payload in two phases:
//  1. Stage: run all UCI set/add/delete operations without committing.
//     On any staging error, revert all staged packages and return an error.
//  2. Commit: commit each staged package in order.
//
// Returns the list of committed package names so the caller can run service reloads.
func (a *Applier) Apply(payload json.RawMessage) ([]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal config payload: %w", err)
	}

	// Collect UCI package names; skip meta-keys.
	var pkgNames []string
	pkgConfigs := make(map[string]map[string]json.RawMessage)
	for key, val := range raw {
		if key == "packages" || key == "reboot" {
			continue
		}
		var pkgCfg map[string]json.RawMessage
		if err := json.Unmarshal(val, &pkgCfg); err != nil {
			return nil, fmt.Errorf("unmarshal package config %q: %w", key, err)
		}
		pkgNames = append(pkgNames, key)
		pkgConfigs[key] = pkgCfg
	}
	sort.Strings(pkgNames)

	// Phase 1: stage all packages.
	staged := make([]string, 0, len(pkgNames))
	for _, pkg := range pkgNames {
		if err := a.stagePackage(pkg, pkgConfigs[pkg]); err != nil {
			// Revert everything staged so far (plus the partially-staged current package).
			for _, s := range staged {
				_ = a.runner.Revert(s)
			}
			_ = a.runner.Revert(pkg)
			return nil, fmt.Errorf("stage %s: %w", pkg, err)
		}
		staged = append(staged, pkg)
	}

	// Phase 2: commit each staged package.
	committed := make([]string, 0, len(staged))
	for _, pkg := range staged {
		if err := a.runner.Commit(pkg); err != nil {
			return committed, fmt.Errorf("commit %s: %w", pkg, err)
		}
		committed = append(committed, pkg)
	}
	return committed, nil
}

// stagePackage stages all UCI changes for one package without committing.
func (a *Applier) stagePackage(pkgName string, pkgCfg map[string]json.RawMessage) error {
	mode := "merge"
	if v, ok := pkgCfg[".mode"]; ok {
		_ = json.Unmarshal(v, &mode)
	}

	for sectionType, sectionsJSON := range pkgCfg {
		if strings.HasPrefix(sectionType, ".") {
			continue
		}
		sections, err := parseSections(sectionsJSON)
		if err != nil {
			return fmt.Errorf("parse sections for type %q: %w", sectionType, err)
		}

		if mode == "replace" {
			// Build the set of section names that we want to keep.
			keepNames := make(map[string]bool)
			for _, s := range sections {
				if name, ok := s[".name"].(string); ok {
					keepNames[name] = true
				}
			}
			if err := a.deleteUnwantedSections(pkgName, sectionType, keepNames); err != nil {
				return err
			}
		}

		for _, section := range sections {
			if err := a.applySection(pkgName, sectionType, section, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteUnwantedSections deletes all current sections of sectionType in pkgName that are
// not listed in keepNames. Uses `uci show` to enumerate existing sections.
func (a *Applier) deleteUnwantedSections(pkgName, sectionType string, keepNames map[string]bool) error {
	out, err := a.runner.Show(pkgName)
	if err != nil {
		// Package doesn't exist yet — nothing to delete.
		return nil
	}
	prefix := pkgName + "."
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		eqIdx := strings.Index(rest, "=")
		if eqIdx < 0 {
			continue
		}
		sectionID := rest[:eqIdx]
		typeVal := strings.TrimSpace(rest[eqIdx+1:])
		// Skip option lines — they contain a "." in sectionID (e.g. "wan.proto").
		if strings.Contains(sectionID, ".") {
			continue
		}
		if typeVal != sectionType {
			continue
		}
		if !keepNames[sectionID] {
			if err := a.runner.Delete(pkgName, sectionID); err != nil {
				return fmt.Errorf("delete %s.%s: %w", pkgName, sectionID, err)
			}
		}
	}
	return nil
}

// applySection stages UCI changes for a single section instance.
func (a *Applier) applySection(pkgName, sectionType string, section map[string]interface{}, mode string) error {
	name, hasName := section[".name"].(string)

	var sectionID string
	if hasName {
		sectionID = name
		// SetType creates the section if it doesn't exist, or updates its type.
		// In replace mode the section was already deleted by deleteUnwantedSections.
		if err := a.runner.SetType(pkgName, sectionID, sectionType); err != nil {
			return fmt.Errorf("set type %s.%s=%s: %w", pkgName, sectionID, sectionType, err)
		}
	} else {
		id, err := a.runner.Add(pkgName, sectionType)
		if err != nil {
			return fmt.Errorf("add %s %s: %w", pkgName, sectionType, err)
		}
		sectionID = id
	}

	for key, val := range section {
		if strings.HasPrefix(key, ".") {
			continue
		}
		switch v := val.(type) {
		case []interface{}:
			// List option: clear existing entries, then add each new value.
			_ = a.runner.DeleteOption(pkgName, sectionID, key)
			for _, item := range v {
				if err := a.runner.AddList(pkgName, sectionID, key, fmt.Sprintf("%v", item)); err != nil {
					return fmt.Errorf("add_list %s.%s.%s: %w", pkgName, sectionID, key, err)
				}
			}
		default:
			if err := a.runner.Set(pkgName, sectionID, key, fmt.Sprintf("%v", val)); err != nil {
				return fmt.Errorf("set %s.%s.%s: %w", pkgName, sectionID, key, err)
			}
		}
	}
	return nil
}

// parseSections unmarshals a JSON value that is either a single object or an array of objects.
func parseSections(raw json.RawMessage) ([]map[string]interface{}, error) {
	var arr []map[string]interface{}
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	var single map[string]interface{}
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, err
	}
	return []map[string]interface{}{single}, nil
}

// serviceReloadCmds maps UCI package names to their service reload shell commands.
var serviceReloadCmds = map[string]string{
	"wireless": "wifi reload",
	"network":  "/etc/init.d/network reload",
	"system":   "/etc/init.d/system reload",
	"firewall": "/etc/init.d/firewall reload",
	"dhcp":     "/etc/init.d/dnsmasq reload",
}

// ServiceReloads returns the ordered list of reload commands for the given package names.
// Duplicates are suppressed.
func ServiceReloads(pkgNames []string) []string {
	seen := make(map[string]bool)
	var cmds []string
	for _, pkg := range pkgNames {
		if cmd, ok := serviceReloadCmds[pkg]; ok && !seen[cmd] {
			cmds = append(cmds, cmd)
			seen[cmd] = true
		}
	}
	return cmds
}

// RunReloads executes the service reload commands for the given packages.
// Returns a slice of error strings for any reload that failed; other reloads continue.
func RunReloads(pkgNames []string) []string {
	var errs []string
	for _, cmd := range ServiceReloads(pkgNames) {
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			continue
		}
		out, err := exec.Command(parts[0], parts[1:]...).CombinedOutput() //nolint:gosec
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v: %s", cmd, err, strings.TrimSpace(string(out))))
		}
	}
	return errs
}
