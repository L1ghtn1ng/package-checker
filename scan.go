package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func scanDirectory(dir string, entries []feedEntry) ([]finding, int, error) {
	manifests := []struct {
		filename string
		parser   func(string) ([]dependencyRef, error)
	}{
		{filename: "package.json", parser: parsePackageJSON},
		{filename: "package-lock.json", parser: parsePackageLock},
		{filename: "pyproject.toml", parser: parsePyproject},
		{filename: "requirements.txt", parser: parseRequirements},
		{filename: "go.mod", parser: parseGoMod},
		{filename: "composer.json", parser: parseComposerJSON},
		{filename: "composer.lock", parser: parseComposerLock},
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

	key := string(dep.Ecosystem) + "|" + dep.Source + "|" + normalizeDependencyName(dep.Ecosystem, dep.Name) + "|" + dep.Version + "|" + dep.VersionSpec
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
		if feedVersionMatches(dep, entry) {
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

func feedVersionMatches(dep dependencyRef, entry feedEntry) bool {
	feedVersion := normalizeVersion(entry.Version)
	if feedVersion == "" {
		if len(entry.Versions) == 0 {
			return true
		}
		for _, version := range entry.Versions {
			if dependencyVersionMatches(dep, normalizeVersion(version)) {
				return true
			}
		}
		return false
	}
	return dependencyVersionMatches(dep, feedVersion)
}

func dependencyVersionMatches(dep dependencyRef, feedVersion string) bool {
	if normalizeVersion(dep.Version) == feedVersion {
		return true
	}
	if dep.VersionSpec == "" {
		return false
	}
	switch dep.Ecosystem {
	case ecosystemPHP:
		return composerConstraintAllowsVersion(dep.VersionSpec, feedVersion)
	default:
		return false
	}
}

func composerConstraintAllowsVersion(spec, version string) bool {
	for _, alternative := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == '|'
	}) {
		alternative = strings.TrimSpace(alternative)
		if alternative == "" {
			continue
		}
		if composerAlternativeAllowsVersion(alternative, version) {
			return true
		}
	}
	return false
}

func composerAlternativeAllowsVersion(spec, version string) bool {
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ' '
	}) {
		part = strings.TrimSpace(part)
		if part == "" || part == "*" {
			continue
		}
		if !composerConstraintPartAllowsVersion(part, version) {
			return false
		}
	}
	return true
}

func composerConstraintPartAllowsVersion(part, version string) bool {
	switch {
	case strings.HasPrefix(part, "^"):
		return caretConstraintAllowsVersion(strings.TrimPrefix(part, "^"), version)
	case strings.HasPrefix(part, "~"):
		return tildeConstraintAllowsVersion(strings.TrimPrefix(part, "~"), version)
	case strings.HasPrefix(part, ">="):
		return compareVersions(version, strings.TrimPrefix(part, ">=")) >= 0
	case strings.HasPrefix(part, ">"):
		return compareVersions(version, strings.TrimPrefix(part, ">")) > 0
	case strings.HasPrefix(part, "<="):
		return compareVersions(version, strings.TrimPrefix(part, "<=")) <= 0
	case strings.HasPrefix(part, "<"):
		return compareVersions(version, strings.TrimPrefix(part, "<")) < 0
	case strings.HasPrefix(part, "="):
		return compareVersions(version, strings.TrimPrefix(part, "=")) == 0
	default:
		return compareVersions(version, part) == 0
	}
}

func caretConstraintAllowsVersion(base, version string) bool {
	lower := parseVersionParts(base)
	upper := lower
	switch {
	case lower.major > 0:
		upper.major++
		upper.minor = 0
		upper.patch = 0
	case lower.minor > 0:
		upper.minor++
		upper.patch = 0
	default:
		upper.patch++
	}
	return compareParsedVersions(parseVersionParts(version), lower) >= 0 && compareParsedVersions(parseVersionParts(version), upper) < 0
}

func tildeConstraintAllowsVersion(base, version string) bool {
	lower := parseVersionParts(base)
	upper := lower
	if lower.segments >= 3 {
		upper.minor++
		upper.patch = 0
	} else {
		upper.major++
		upper.minor = 0
		upper.patch = 0
	}
	return compareParsedVersions(parseVersionParts(version), lower) >= 0 && compareParsedVersions(parseVersionParts(version), upper) < 0
}

type versionParts struct {
	major      int
	minor      int
	patch      int
	segments   int
	prerelease string
}

func compareVersions(a, b string) int {
	return compareParsedVersions(parseVersionParts(a), parseVersionParts(b))
}

func compareParsedVersions(a, b versionParts) int {
	switch {
	case a.major != b.major:
		return compareInts(a.major, b.major)
	case a.minor != b.minor:
		return compareInts(a.minor, b.minor)
	case a.patch != b.patch:
		return compareInts(a.patch, b.patch)
	case a.prerelease == b.prerelease:
		return 0
	case a.prerelease == "":
		return 1
	case b.prerelease == "":
		return -1
	default:
		return strings.Compare(a.prerelease, b.prerelease)
	}
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func parseVersionParts(version string) versionParts {
	version = normalizeVersion(version)
	if idx := strings.Index(version, "+"); idx >= 0 {
		version = version[:idx]
	}
	base, prerelease, _ := strings.Cut(version, "-")
	fields := strings.Split(base, ".")
	parts := versionParts{segments: len(fields), prerelease: prerelease}
	if len(fields) > 0 {
		parts.major = parseLeadingInt(fields[0])
	}
	if len(fields) > 1 {
		parts.minor = parseLeadingInt(fields[1])
	}
	if len(fields) > 2 {
		parts.patch = parseLeadingInt(fields[2])
	}
	return parts
}

func parseLeadingInt(value string) int {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(value[:end])
	if err != nil {
		return 0
	}
	return n
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
