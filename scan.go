package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func scanDirectory(dir string, entries []feedEntry) ([]finding, int, error) {
	manifests := []struct {
		filename string
		parser   func(string) ([]dependencyRef, error)
	}{
		{filename: "package.json", parser: parsePackageJSON},
		{filename: "pyproject.toml", parser: parsePyproject},
		{filename: "requirements.txt", parser: parseRequirements},
		{filename: "go.mod", parser: parseGoMod},
		{filename: "composer.json", parser: parseComposerJSON},
	}

	index := buildFeedIndex(entries)
	findings := make([]finding, 0)
	seen := map[string]struct{}{}
	scannedFiles := 0

	for _, manifest := range manifests {
		path := filepath.Join(dir, manifest.filename)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, 0, fmt.Errorf("stat %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}

		scannedFiles++
		deps, err := manifest.parser(path)
		if err != nil {
			return nil, 0, err
		}

		findings = appendMatchingFindings(findings, seen, index, deps)
	}

	nodeModulesFindings, nodeModulesScanned, err := scanNodeModules(filepath.Join(dir, "node_modules"), index, seen)
	if err != nil {
		return nil, 0, err
	}
	if nodeModulesScanned > 0 {
		scannedFiles += nodeModulesScanned
		findings = append(findings, nodeModulesFindings...)
	}

	slices.SortFunc(findings, func(a, b finding) int {
		if cmp := strings.Compare(a.Dependency.Source, b.Dependency.Source); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Dependency.Name, b.Dependency.Name)
	})

	return findings, scannedFiles, nil
}

func appendMatchingFindings(findings []finding, seen map[string]struct{}, index feedIndex, deps []dependencyRef) []finding {
	for _, dep := range deps {
		findings = appendMatchingFinding(findings, seen, index, dep)
	}

	return findings
}

func appendMatchingFinding(findings []finding, seen map[string]struct{}, index feedIndex, dep dependencyRef) []finding {
	match, ok := index.match(dep)
	if !ok {
		return findings
	}

	key := string(dep.Ecosystem) + "|" + dep.Source + "|" + normalizeDependencyName(dep.Ecosystem, dep.Name) + "|" + dep.Version
	if _, exists := seen[key]; exists {
		return findings
	}
	seen[key] = struct{}{}
	return append(findings, finding{
		Dependency: dep,
		Feed:       match,
	})
}

func scanNodeModules(nodeModulesPath string, index feedIndex, seen map[string]struct{}) ([]finding, int, error) {
	if !index.hasEcosystem(ecosystemNPM) {
		return nil, 0, nil
	}

	info, err := os.Stat(nodeModulesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("stat %s: %w", nodeModulesPath, err)
	}
	if !info.IsDir() {
		return nil, 0, nil
	}

	packageDirs, err := nodeModulesPackageDirs(nodeModulesPath)
	if err != nil {
		return nil, 0, err
	}

	findings := make([]finding, 0)
	scannedFiles := 0
	for _, packageDir := range packageDirs {
		packageJSONPath := filepath.Join(packageDir.path, "package.json")
		info, err := os.Stat(packageJSONPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, 0, fmt.Errorf("stat %s: %w", packageJSONPath, err)
		}
		if info.IsDir() {
			continue
		}

		dep, err := parseInstalledPackageJSON(packageJSONPath, packageDir.name)
		if err != nil {
			return nil, 0, err
		}
		if dep.Name == "" {
			continue
		}
		scannedFiles++
		findings = appendMatchingFinding(findings, seen, index, dep)
	}

	return findings, scannedFiles, nil
}

type nodeModulesPackageDir struct {
	path string
	name string
}

func nodeModulesPackageDirs(nodeModulesPath string) ([]nodeModulesPackageDir, error) {
	entries, err := os.ReadDir(nodeModulesPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", nodeModulesPath, err)
	}

	packageDirs := make([]nodeModulesPackageDir, 0, len(entries))
	for _, entry := range entries {
		if !isUsableNodeModulesDir(nodeModulesPath, entry) {
			continue
		}
		name := entry.Name()
		path := filepath.Join(nodeModulesPath, name)
		if strings.HasPrefix(name, "@") {
			scopedDirs, err := scopedNodeModulesPackageDirs(path, name)
			if err != nil {
				return nil, err
			}
			packageDirs = append(packageDirs, scopedDirs...)
			continue
		}
		packageDirs = append(packageDirs, nodeModulesPackageDir{path: path, name: name})
	}

	return packageDirs, nil
}

func scopedNodeModulesPackageDirs(scopePath, scopeName string) ([]nodeModulesPackageDir, error) {
	entries, err := os.ReadDir(scopePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", scopePath, err)
	}

	packageDirs := make([]nodeModulesPackageDir, 0, len(entries))
	for _, entry := range entries {
		if !isUsableNodeModulesDir(scopePath, entry) {
			continue
		}
		name := scopeName + "/" + entry.Name()
		packageDirs = append(packageDirs, nodeModulesPackageDir{
			path: filepath.Join(scopePath, entry.Name()),
			name: name,
		})
	}
	return packageDirs, nil
}

func isUsableNodeModulesDir(parent string, entry os.DirEntry) bool {
	name := entry.Name()
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parent, name))
	return err == nil && info.IsDir()
}

type feedIndex struct {
	entries map[ecosystem]map[string][]feedEntry
}

func buildFeedIndex(entries []feedEntry) feedIndex {
	index := feedIndex{
		entries: make(map[ecosystem]map[string][]feedEntry),
	}

	for _, entry := range entries {
		kind := feedEcosystem(entry)
		if kind == "" {
			continue
		}
		key := normalizeDependencyName(kind, feedPackageName(entry, kind))
		if key == "" {
			continue
		}
		if index.entries[kind] == nil {
			index.entries[kind] = make(map[string][]feedEntry)
		}
		index.entries[kind][key] = append(index.entries[kind][key], entry)
	}

	return index
}

func (f feedIndex) match(dep dependencyRef) (feedEntry, bool) {
	key := normalizeDependencyName(dep.Ecosystem, dep.Name)
	candidates := f.entries[dep.Ecosystem][key]
	if len(candidates) == 0 {
		return feedEntry{}, false
	}
	for _, entry := range candidates {
		if feedVersionMatches(dep.Version, entry) {
			return entry, true
		}
	}
	return feedEntry{}, false
}

func (f feedIndex) hasEcosystem(kind ecosystem) bool {
	return len(f.entries[kind]) > 0
}

func mapFeedPlatform(platform string) ecosystem {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "npm":
		return ecosystemNPM
	case "pypi", "pip", "python":
		return ecosystemPython
	case "go", "golang", "gomod", "go-mod", "go modules", "go_modules":
		return ecosystemGo
	case "php", "composer", "packagist":
		return ecosystemPHP
	default:
		return ""
	}
}

func feedEcosystem(entry feedEntry) ecosystem {
	for _, value := range []string{string(entry.Ecosystem), entry.Platform, entry.Type} {
		if kind := mapFeedPlatform(value); kind != "" {
			return kind
		}
	}
	return ""
}

func feedPackageName(entry feedEntry, kind ecosystem) string {
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = strings.TrimSpace(entry.Title)
	}
	namespace := strings.TrimSpace(entry.Namespace)
	if namespace == "" || name == "" {
		return name
	}
	switch kind {
	case ecosystemNPM:
		namespace = strings.TrimPrefix(namespace, "@")
		if strings.HasPrefix(name, "@") || strings.Contains(name, "/") {
			return name
		}
		return "@" + namespace + "/" + name
	case ecosystemGo, ecosystemPHP:
		if strings.Contains(name, "/") {
			return name
		}
		return strings.TrimRight(namespace, "/") + "/" + name
	default:
		return name
	}
}

func feedVersionMatches(dependencyVersion string, entry feedEntry) bool {
	feedVersion := normalizeVersion(entry.Version)
	if feedVersion == "" {
		if len(entry.Versions) == 0 {
			return true
		}
		for _, version := range entry.Versions {
			if normalizeVersion(dependencyVersion) == normalizeVersion(version) {
				return true
			}
		}
		return false
	}
	return normalizeVersion(dependencyVersion) == feedVersion
}

func normalizeDependencyName(kind ecosystem, name string) string {
	name = strings.TrimSpace(name)
	switch kind {
	case ecosystemNPM, ecosystemGo, ecosystemPHP:
		return strings.ToLower(name)
	case ecosystemPython:
		name = strings.ToLower(name)
		replacer := strings.NewReplacer("_", "-", ".", "-")
		name = replacer.Replace(name)
		for strings.Contains(name, "--") {
			name = strings.ReplaceAll(name, "--", "-")
		}
		return strings.Trim(name, "-")
	default:
		return name
	}
}
