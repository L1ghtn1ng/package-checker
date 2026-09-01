package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

var pythonRequirementPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+`)
var goRequirePattern = regexp.MustCompile(`^\s*([^\s]+)\s+(v[^\s]+)`)

type packageJSONManifest struct {
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

type installedPackageJSONManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type packageLockManifest struct {
	Packages     map[string]packageLockPackage    `json:"packages"`
	Dependencies map[string]packageLockDependency `json:"dependencies"`
}

type packageLockPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type packageLockDependency struct {
	Version      string                           `json:"version"`
	Dependencies map[string]packageLockDependency `json:"dependencies"`
}

type composerManifest struct {
	Require    map[string]string `json:"require"`
	RequireDev map[string]string `json:"require-dev"`
}

type composerLockManifest struct {
	Packages    []composerLockPackage `json:"packages"`
	PackagesDev []composerLockPackage `json:"packages-dev"`
}

type composerLockPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type pyprojectManifest struct {
	Project struct {
		Dependencies         []string            `toml:"dependencies"`
		OptionalDependencies map[string][]string `toml:"optional-dependencies"`
	} `toml:"project"`
	Tool struct {
		Poetry poetryManifest `toml:"poetry"`
	} `toml:"tool"`
}

type poetryManifest struct {
	Dependencies    map[string]any                 `toml:"dependencies"`
	DevDependencies map[string]any                 `toml:"dev-dependencies"`
	Group           map[string]poetryGroupManifest `toml:"group"`
}

type poetryGroupManifest struct {
	Dependencies map[string]any `toml:"dependencies"`
}

func parsePackageJSON(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	var manifest packageJSONManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	for _, group := range []map[string]string{
		manifest.Dependencies,
		manifest.DevDependencies,
		manifest.OptionalDependencies,
		manifest.PeerDependencies,
	} {
		for name, spec := range group {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			deps = append(deps, dependencyRef{
				Name:        name,
				Version:     extractExactVersion(spec),
				VersionSpec: strings.TrimSpace(spec),
				Source:      path,
				Ecosystem:   ecosystemNPM,
			})
		}
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		return strings.Compare(a.Name, b.Name)
	})

	return deps, nil
}

func parseInstalledPackageJSON(path, fallbackName string) (dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return dependencyRef{}, err
	}

	var manifest installedPackageJSONManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return dependencyRef{}, fmt.Errorf("parse %s: %w", path, err)
	}

	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = fallbackName
	}
	if name == "" {
		return dependencyRef{}, nil
	}

	return dependencyRef{
		Name:      name,
		Version:   normalizeVersion(manifest.Version),
		Source:    path,
		Ecosystem: ecosystemNPM,
	}, nil
}

func parsePackageLock(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	var manifest packageLockManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	add := func(name, version string) {
		name = strings.TrimSpace(name)
		version = normalizeVersion(version)
		if name == "" {
			return
		}
		key := strings.ToLower(name) + "@" + version
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		deps = append(deps, dependencyRef{
			Name:      name,
			Version:   version,
			Source:    path,
			Ecosystem: ecosystemNPM,
		})
	}

	for packagePath, pkg := range manifest.Packages {
		if packagePath == "" {
			continue
		}
		name := strings.TrimSpace(pkg.Name)
		if name == "" {
			name = packageLockPackageName(packagePath)
		}
		add(name, pkg.Version)
	}
	for name, dep := range manifest.Dependencies {
		addPackageLockDependency(add, name, dep)
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Version, b.Version)
	})

	return deps, nil
}

func parsePyproject(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	var manifest pyprojectManifest
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	add := func(value string) {
		name, version := splitNameVersion(value)
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		deps = append(deps, dependencyRef{
			Name:      name,
			Version:   version,
			Source:    path,
			Ecosystem: ecosystemPython,
		})
	}

	for _, requirement := range manifest.Project.Dependencies {
		addPythonRequirement(add, requirement)
	}

	for _, requirements := range manifest.Project.OptionalDependencies {
		for _, requirement := range requirements {
			addPythonRequirement(add, requirement)
		}
	}

	addPoetryDependencies(add, manifest.Tool.Poetry.Dependencies)
	addPoetryDependencies(add, manifest.Tool.Poetry.DevDependencies)

	for _, group := range manifest.Tool.Poetry.Group {
		addPoetryDependencies(add, group.Dependencies)
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		return strings.Compare(a.Name, b.Name)
	})

	return deps, nil
}

func parseRequirements(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)

	add := func(value string) {
		name, version := splitNameVersion(value)
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		deps = append(deps, dependencyRef{
			Name:      name,
			Version:   version,
			Source:    path,
			Ecosystem: ecosystemPython,
		})
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if name := extractEditableRequirementName(line); name != "" {
			add(name)
			continue
		}

		if strings.HasPrefix(line, "-") {
			continue
		}

		addPythonRequirement(add, line)
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		return strings.Compare(a.Name, b.Name)
	})

	return deps, nil
}

func parseGoMod(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	inRequireBlock := false
	for rawLine := range strings.SplitSeq(string(data), "\n") {
		line := stripInlineComment(rawLine)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "require (") {
			inRequireBlock = true
			continue
		}
		if inRequireBlock && trimmed == ")" {
			inRequireBlock = false
			continue
		}
		if after, ok := strings.CutPrefix(trimmed, "require "); ok {
			trimmed = strings.TrimSpace(after)
		} else if !inRequireBlock {
			continue
		}

		match := goRequirePattern.FindStringSubmatch(trimmed)
		if len(match) != 3 {
			continue
		}
		name := match[1]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		deps = append(deps, dependencyRef{
			Name:      name,
			Version:   strings.TrimPrefix(match[2], "v"),
			Source:    path,
			Ecosystem: ecosystemGo,
		})
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		return strings.Compare(a.Name, b.Name)
	})

	return deps, nil
}

func parseComposerJSON(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	var manifest composerManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	for _, group := range []map[string]string{manifest.Require, manifest.RequireDev} {
		for name, spec := range group {
			if strings.EqualFold(name, "php") || strings.HasPrefix(strings.ToLower(name), "ext-") {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			deps = append(deps, dependencyRef{
				Name:        name,
				Version:     extractExactVersion(spec),
				VersionSpec: strings.TrimSpace(spec),
				Source:      path,
				Ecosystem:   ecosystemPHP,
			})
		}
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		return strings.Compare(a.Name, b.Name)
	})

	return deps, nil
}

func parseComposerLock(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	var manifest composerLockManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	for _, group := range [][]composerLockPackage{manifest.Packages, manifest.PackagesDev} {
		for _, pkg := range group {
			name := strings.TrimSpace(pkg.Name)
			if name == "" || strings.EqualFold(name, "php") || strings.HasPrefix(strings.ToLower(name), "ext-") {
				continue
			}
			version := normalizeVersion(pkg.Version)
			key := strings.ToLower(name) + "@" + version
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			deps = append(deps, dependencyRef{
				Name:      name,
				Version:   version,
				Source:    path,
				Ecosystem: ecosystemPHP,
			})
		}
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Version, b.Version)
	})

	return deps, nil
}

func readManifestFile(path string) ([]byte, error) {
	// Reading caller-selected manifest paths is the scanner's intended operation.
	return os.ReadFile(path) //nolint:gosec
}

func packageLockPackageName(packagePath string) string {
	parts := strings.Split(filepath.ToSlash(packagePath), "/")
	for i, part := range parts {
		if part != "node_modules" || i+1 >= len(parts) {
			continue
		}
		if strings.HasPrefix(parts[i+1], "@") && i+2 < len(parts) {
			return parts[i+1] + "/" + parts[i+2]
		}
		return parts[i+1]
	}
	return ""
}

func addPackageLockDependency(add func(string, string), name string, dep packageLockDependency) {
	add(name, dep.Version)
	for childName, child := range dep.Dependencies {
		addPackageLockDependency(add, childName, child)
	}
}

func extractEditableRequirementName(line string) string {
	for _, prefix := range []string{"-e ", "--editable "} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		parts := strings.SplitN(line, "#egg=", 2)
		if len(parts) != 2 {
			return ""
		}
		return extractPythonRequirementName(parts[1])
	}

	return ""
}

func extractPythonRequirementName(requirement string) string {
	candidate := strings.TrimSpace(requirement)
	if candidate == "" {
		return ""
	}

	if idx := strings.Index(candidate, ";"); idx >= 0 {
		candidate = candidate[:idx]
	}
	if idx := strings.Index(candidate, " @ "); idx >= 0 {
		candidate = candidate[:idx]
	}

	candidate = strings.TrimSpace(candidate)
	match := pythonRequirementPattern.FindString(candidate)
	return strings.ToLower(match)
}

func addPythonRequirement(add func(string), requirement string) {
	name, version := extractPythonRequirement(requirement)
	if name == "" {
		return
	}
	if version != "" {
		add(name + "@" + version)
		return
	}
	add(name)
}

func extractPythonRequirement(requirement string) (string, string) {
	name := extractPythonRequirementName(requirement)
	if name == "" {
		return "", ""
	}

	candidate := strings.TrimSpace(requirement)
	if idx := strings.Index(candidate, ";"); idx >= 0 {
		candidate = candidate[:idx]
	}
	exactToken := "=="
	idx := strings.Index(candidate, exactToken)
	if idx < 0 {
		return name, ""
	}
	version := strings.TrimSpace(candidate[idx+len(exactToken):])
	if comma := strings.Index(version, ","); comma >= 0 {
		version = version[:comma]
	}
	if hash := strings.Index(version, "#"); hash >= 0 {
		version = version[:hash]
	}
	if fields := strings.Fields(version); len(fields) > 0 {
		version = fields[0]
	}
	version = strings.TrimSuffix(version, "\\")
	return name, normalizeVersion(version)
}

func splitNameVersion(value string) (string, string) {
	name, version, ok := strings.Cut(value, "@")
	if !ok {
		return value, ""
	}
	return name, version
}

func addPoetryDependencies(add func(string), deps map[string]any) {
	for name, spec := range deps {
		if strings.EqualFold(name, "python") {
			continue
		}
		if version := poetryExactVersion(spec); version != "" {
			add(name + "@" + version)
			continue
		}
		add(name)
	}
}

func poetryExactVersion(spec any) string {
	switch value := spec.(type) {
	case string:
		return extractExactVersion(value)
	case map[string]any:
		if version, ok := value["version"].(string); ok {
			return extractExactVersion(version)
		}
	default:
		return ""
	}
	return ""
}

func stripInlineComment(line string) string {
	if before, _, ok := strings.Cut(line, "//"); ok {
		return before
	}
	return line
}

func extractExactVersion(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	spec = strings.TrimPrefix(spec, "npm:")
	spec = strings.TrimPrefix(spec, "=")
	spec = strings.TrimSpace(spec)
	if strings.ContainsAny(spec, "<>^~*|, ") {
		return ""
	}
	return normalizeVersion(spec)
}

func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if idx := strings.Index(version, "?"); idx >= 0 {
		version = version[:idx]
	}
	version = strings.Trim(version, `"'`)
	return strings.TrimPrefix(version, "v")
}
