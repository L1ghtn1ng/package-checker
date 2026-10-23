package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
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

type cargoManifest struct {
	cargoDependencyTables
	Package struct {
		Workspace string `toml:"workspace"`
	} `toml:"package"`
	Target    map[string]cargoDependencyTables `toml:"target"`
	Workspace *cargoWorkspace                  `toml:"workspace"`
}

type cargoWorkspace struct {
	Members      []string       `toml:"members"`
	Exclude      []string       `toml:"exclude"`
	Dependencies map[string]any `toml:"dependencies"`
}

type cargoDependencyTables struct {
	Dependencies            map[string]any `toml:"dependencies"`
	DevDependencies         map[string]any `toml:"dev-dependencies"`
	LegacyDevDependencies   map[string]any `toml:"dev_dependencies"`
	BuildDependencies       map[string]any `toml:"build-dependencies"`
	LegacyBuildDependencies map[string]any `toml:"build_dependencies"`
}

type cargoLockManifest struct {
	Packages []cargoLockPackage `toml:"package"`
}

type cargoLockPackage struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Source  string `toml:"source"`
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

func parseCargoToml(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	var manifest cargoManifest
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	workspace := manifest.workspace()
	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	addTable := func(table map[string]any) {
		for key, spec := range table {
			if inherited, ok := workspace.Dependencies[key]; ok && cargoInheritsWorkspace(spec) {
				spec = inherited
			}
			dep, ok := cargoDependency(key, spec)
			if !ok {
				continue
			}
			seenKey := normalizeDependencyName(ecosystemRust, dep.Name) + "@" + dep.VersionSpec
			if _, exists := seen[seenKey]; exists {
				continue
			}
			seen[seenKey] = struct{}{}
			dep.Source = path
			deps = append(deps, dep)
		}
	}
	for _, table := range manifest.dependencyTables() {
		addTable(table)
	}

	// [workspace.dependencies] only declares crates for members to opt into, so
	// report an entry only when a workspace member inherits it.
	memberInherited, err := cargoMemberInheritedDependencies(filepath.Dir(path), manifest)
	if err != nil {
		return nil, err
	}
	for key, spec := range workspace.Dependencies {
		if _, ok := memberInherited[key]; ok {
			addTable(map[string]any{key: spec})
		}
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.VersionSpec, b.VersionSpec)
	})

	return deps, nil
}

// cargoMemberInheritedDependencies returns the dependency keys that workspace
// members inherit with workspace = true. Members are the crates listed in
// [workspace] members plus, as in Cargo, every path dependency reachable from
// the root or another member that lives inside the workspace directory.
func cargoMemberInheritedDependencies(rootDir string, root cargoManifest) (map[string]struct{}, error) {
	inherited := map[string]struct{}{}
	workspace := root.workspace()
	if len(workspace.Dependencies) == 0 {
		return inherited, nil
	}

	canonicalRoot, err := canonicalDir(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root %s: %w", rootDir, err)
	}
	layout := cargoWorkspaceLayout{
		root:    canonicalRoot,
		members: cleanRelativePaths(workspace.Members),
		exclude: cleanRelativePaths(workspace.Exclude),
	}

	queue := cargoPathDependencyDirs(canonicalRoot, root)
	// Glob within the root directory so its own name is never read as a pattern.
	rootFS := os.DirFS(canonicalRoot)
	for _, member := range workspace.Members {
		member = strings.TrimSpace(member)
		if member == "" || filepath.IsAbs(member) {
			continue
		}
		// io/fs patterns are slash-separated; on Windows a backslash in a member
		// pattern is a directory separator rather than a glob escape.
		pattern := filepath.ToSlash(member)
		if path.IsAbs(pattern) {
			continue
		}
		matches, err := fs.Glob(rootFS, path.Join(path.Clean(pattern), "Cargo.toml"))
		if err != nil {
			return nil, fmt.Errorf("expand workspace member %q: %w", member, err)
		}
		for _, match := range matches {
			queue = append(queue, filepath.Join(canonicalRoot, filepath.FromSlash(path.Dir(match))))
		}
	}

	visited := map[string]struct{}{canonicalRoot: {}}
	for len(queue) > 0 {
		candidate := queue[0]
		queue = queue[1:]
		// Resolve symlinks so a link inside the workspace cannot pull in an outside crate.
		memberDir, err := canonicalDir(candidate)
		if err != nil {
			continue
		}
		if _, ok := visited[memberDir]; ok {
			continue
		}
		visited[memberDir] = struct{}{}
		if !layout.includes(memberDir) {
			continue
		}

		memberPath := filepath.Join(memberDir, "Cargo.toml")
		data, err := readManifestFile(memberPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var memberManifest cargoManifest
		if err := toml.Unmarshal(data, &memberManifest); err != nil {
			return nil, fmt.Errorf("parse %s: %w", memberPath, err)
		}
		if !layout.owns(memberDir, memberManifest) {
			continue
		}
		for _, table := range memberManifest.dependencyTables() {
			for key, spec := range table {
				if cargoInheritsWorkspace(spec) {
					inherited[key] = struct{}{}
				}
			}
		}
		queue = append(queue, cargoPathDependencyDirs(memberDir, memberManifest)...)
	}

	return inherited, nil
}

// cargoPathDependencyDirs returns the directories of a manifest's path dependencies.
func cargoPathDependencyDirs(manifestDir string, manifest cargoManifest) []string {
	dirs := make([]string, 0)
	for _, table := range manifest.dependencyTables() {
		for _, spec := range table {
			fields, ok := spec.(map[string]any)
			if !ok {
				continue
			}
			depPath, ok := fields["path"].(string)
			if !ok || strings.TrimSpace(depPath) == "" {
				continue
			}
			dirs = append(dirs, resolveManifestPath(manifestDir, depPath))
		}
	}
	return dirs
}

type cargoWorkspaceLayout struct {
	root    string
	members []string
	exclude []string
}

// includes reports whether a canonical directory is inside the workspace and
// not excluded. As in Cargo, a path under a literally listed member stays a
// member even when it is also excluded.
func (l cargoWorkspaceLayout) includes(dir string) bool {
	relDir, err := filepath.Rel(l.root, dir)
	if err != nil || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) {
		return false
	}
	for _, member := range l.members {
		if hasPathPrefix(relDir, member) {
			return true
		}
	}
	for _, exclusion := range l.exclude {
		if hasPathPrefix(relDir, exclusion) {
			return false
		}
	}
	return true
}

// owns reports whether a crate belongs to this workspace rather than one named
// by its [package] workspace key or rooted at its own [workspace] table.
func (l cargoWorkspaceLayout) owns(dir string, manifest cargoManifest) bool {
	if workspacePath := strings.TrimSpace(manifest.Package.Workspace); workspacePath != "" {
		target, err := canonicalDir(resolveManifestPath(dir, workspacePath))
		return err == nil && target == l.root
	}
	return manifest.Workspace == nil
}

func (m cargoManifest) workspace() cargoWorkspace {
	if m.Workspace == nil {
		return cargoWorkspace{}
	}
	return *m.Workspace
}

func canonicalDir(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func resolveManifestPath(manifestDir, value string) string {
	value = filepath.FromSlash(strings.TrimSpace(value))
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(manifestDir, value)
}

func cleanRelativePaths(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, filepath.Clean(filepath.FromSlash(value)))
	}
	return cleaned
}

func hasPathPrefix(relPath, prefix string) bool {
	return prefix == "." || relPath == prefix || strings.HasPrefix(relPath, prefix+string(filepath.Separator))
}

func (m cargoManifest) dependencyTables() []map[string]any {
	tables := m.list()
	for _, target := range m.Target {
		tables = append(tables, target.list()...)
	}
	return tables
}

func (t cargoDependencyTables) list() []map[string]any {
	return []map[string]any{
		t.Dependencies,
		t.DevDependencies,
		t.LegacyDevDependencies,
		t.BuildDependencies,
		t.LegacyBuildDependencies,
	}
}

func parseCargoLock(path string) ([]dependencyRef, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return nil, err
	}

	var manifest cargoLockManifest
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	for _, pkg := range manifest.Packages {
		name := strings.TrimSpace(pkg.Name)
		// Packages without a source are workspace members or path dependencies.
		if name == "" || strings.TrimSpace(pkg.Source) == "" {
			continue
		}
		version := normalizeVersion(pkg.Version)
		key := normalizeDependencyName(ecosystemRust, name) + "@" + version
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deps = append(deps, dependencyRef{
			Name:      name,
			Version:   version,
			Source:    path,
			Ecosystem: ecosystemRust,
		})
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

// cargoDependency resolves a Cargo.toml dependency entry to the crate it pulls
// from a registry or git source. Path-only dependencies are local code and skipped.
func cargoDependency(key string, spec any) (dependencyRef, bool) {
	name := strings.TrimSpace(key)
	versionSpec := ""
	switch value := spec.(type) {
	case string:
		versionSpec = strings.TrimSpace(value)
	case map[string]any:
		if _, ok := value["path"]; ok {
			return dependencyRef{}, false
		}
		if pkg, ok := value["package"].(string); ok && strings.TrimSpace(pkg) != "" {
			name = strings.TrimSpace(pkg)
		}
		if version, ok := value["version"].(string); ok {
			versionSpec = strings.TrimSpace(version)
		}
	default:
		return dependencyRef{}, false
	}
	if name == "" {
		return dependencyRef{}, false
	}
	// Git and unresolved workspace = true dependencies have no declared version,
	// so any flagged version of the crate could be in use.
	if versionSpec == "" {
		versionSpec = "*"
	}

	version := ""
	if exact, ok := strings.CutPrefix(versionSpec, "="); ok {
		version = extractExactVersion(exact)
	}
	return dependencyRef{
		Name:        name,
		Version:     version,
		VersionSpec: versionSpec,
		Ecosystem:   ecosystemRust,
	}, true
}

func cargoInheritsWorkspace(spec any) bool {
	table, ok := spec.(map[string]any)
	if !ok {
		return false
	}
	inherits, ok := table["workspace"].(bool)
	return ok && inherits
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
