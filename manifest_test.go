package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParsePackageJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "package.json")
	data := `{
		"dependencies": {"alpha":"1.0.0"},
		"devDependencies": {"beta":"1.0.0"},
		"optionalDependencies": {"gamma":"1.0.0"},
		"peerDependencies": {"@scope/delta":"1.0.0"}
	}`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	deps, err := parsePackageJSON(path)
	if err != nil {
		t.Fatalf("parse package.json: %v", err)
	}

	got := dependencyNames(deps)
	want := []string{"@scope/delta", "alpha", "beta", "gamma"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}
	if deps[0].Version != "1.0.0" {
		t.Fatalf("expected exact package.json version, got %q", deps[0].Version)
	}
}

func TestParsePackageLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "package-lock.json")
	data := `{
		"name": "app",
		"lockfileVersion": 3,
		"packages": {
			"": {"name": "app", "version": "1.0.0"},
			"node_modules/plain": {"version": "1.2.3"},
			"node_modules/@scope/pkg": {"version": "4.5.6"},
			"node_modules/named": {"name": "override-name", "version": "7.8.9"}
		}
	}`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write package-lock.json: %v", err)
	}

	deps, err := parsePackageLock(path)
	if err != nil {
		t.Fatalf("parse package-lock.json: %v", err)
	}

	got := dependencyNameVersions(deps)
	want := []string{"@scope/pkg@4.5.6", "override-name@7.8.9", "plain@1.2.3"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}
}

func TestParsePackageLockV1Dependencies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "package-lock.json")
	data := `{
		"lockfileVersion": 1,
		"dependencies": {
			"plain": {
				"version": "1.2.3",
				"dependencies": {
					"nested": {"version": "9.9.9"}
				}
			},
			"@scope/pkg": {"version": "4.5.6"}
		}
	}`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write package-lock.json: %v", err)
	}

	deps, err := parsePackageLock(path)
	if err != nil {
		t.Fatalf("parse package-lock.json: %v", err)
	}

	got := dependencyNameVersions(deps)
	want := []string{"@scope/pkg@4.5.6", "nested@9.9.9", "plain@1.2.3"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}
}

func TestParsePyproject(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "pyproject.toml")
	data := `
[project]
dependencies = ["Requests[socks]>=2.0", "example @ https://example.com/example.whl"]

[project.optional-dependencies]
dev = ["Flask>=3.0"]

[tool.poetry.dependencies]
python = "^3.12"
numpy = "^2.0"

[tool.poetry.dev-dependencies]
black = "^24.0"

[tool.poetry.group.test.dependencies]
pytest = "^8.0"
`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write pyproject.toml: %v", err)
	}

	deps, err := parsePyproject(path)
	if err != nil {
		t.Fatalf("parse pyproject.toml: %v", err)
	}

	got := dependencyNames(deps)
	want := []string{"black", "example", "flask", "numpy", "pytest", "requests"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}
}

func TestParseRequirements(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "requirements.txt")
	data := `
# comment
requests[socks]>=2.31
Flask==3.0
--index-url https://example.com/simple
-r other.txt
-e git+https://github.com/example/project.git#egg=Editable_Pkg
pkgname @ https://example.com/pkg.whl
commented==1.2.3  # via pip-compile
continued==2.3.4 \
hashed==3.4.5 --hash=sha256:abc
`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write requirements.txt: %v", err)
	}

	deps, err := parseRequirements(path)
	if err != nil {
		t.Fatalf("parse requirements.txt: %v", err)
	}

	got := dependencyNames(deps)
	want := []string{"commented", "continued", "editable_pkg", "flask", "hashed", "pkgname", "requests"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}

	versions := dependencyVersions(deps)
	for name, wantVersion := range map[string]string{
		"commented": "1.2.3",
		"continued": "2.3.4",
		"hashed":    "3.4.5",
	} {
		if versions[name] != wantVersion {
			t.Fatalf("unexpected %s version: got %q want %q", name, versions[name], wantVersion)
		}
	}
}

func TestParseGoMod(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	data := `
module example.test/app

go 1.26

require github.com/example/direct v1.2.3

require (
	golang.org/x/mod v0.25.0
	github.com/example/indirect v0.1.0 // indirect
)
`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	deps, err := parseGoMod(path)
	if err != nil {
		t.Fatalf("parse go.mod: %v", err)
	}

	got := dependencyNames(deps)
	want := []string{"github.com/example/direct", "github.com/example/indirect", "golang.org/x/mod"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}
	if deps[0].Version != "1.2.3" {
		t.Fatalf("unexpected Go version: %q", deps[0].Version)
	}
}

func TestParseComposerJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "composer.json")
	data := `{
		"require": {
			"php": "^8.3",
			"ext-json": "*",
			"vendor/package": "1.2.3"
		},
		"require-dev": {
			"phpunit/phpunit": "^11.0"
		}
	}`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write composer.json: %v", err)
	}

	deps, err := parseComposerJSON(path)
	if err != nil {
		t.Fatalf("parse composer.json: %v", err)
	}

	got := dependencyNames(deps)
	want := []string{"phpunit/phpunit", "vendor/package"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}
	if deps[1].Version != "1.2.3" {
		t.Fatalf("unexpected Composer exact version: %q", deps[1].Version)
	}
}

func TestParseComposerJSONWithProjectMetadataAndConstraints(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "composer.json")
	data := `{
		"name": "laravel/boost",
		"description": "Laravel Boost",
		"require": {
			"php": "^8.2",
			"laravel-lang/lang": "^1.0.2",
			"illuminate/support": "^11.45.3|^12.41.1|^13.0"
		},
		"require-dev": {
			"pestphp/pest": "^2.36.0|^3.8.4|^4.1.5"
		},
		"autoload": {
			"psr-4": {"Laravel\\Boost\\": "src/"}
		}
	}`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write composer.json: %v", err)
	}

	deps, err := parseComposerJSON(path)
	if err != nil {
		t.Fatalf("parse composer.json: %v", err)
	}

	got := dependencyNames(deps)
	want := []string{"illuminate/support", "laravel-lang/lang", "pestphp/pest"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}
	versions := dependencyVersionSpecs(deps)
	if versions["laravel-lang/lang"] != "^1.0.2" {
		t.Fatalf("unexpected version spec: %q", versions["laravel-lang/lang"])
	}
}

func TestParseComposerLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "composer.lock")
	data := `{
		"packages": [
			{"name": "laravel-lang/lang", "version": "1.0.2"},
			{"name": "vendor/package", "version": "v2.3.4"}
		],
		"packages-dev": [
			{"name": "pestphp/pest", "version": "3.8.4"}
		]
	}`

	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write composer.lock: %v", err)
	}

	deps, err := parseComposerLock(path)
	if err != nil {
		t.Fatalf("parse composer.lock: %v", err)
	}

	got := dependencyNameVersions(deps)
	want := []string{"laravel-lang/lang@1.0.2", "pestphp/pest@3.8.4", "vendor/package@2.3.4"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected deps: got %v want %v", got, want)
	}
}

func dependencyNames(deps []dependencyRef) []string {
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		names = append(names, dep.Name)
	}
	return names
}

func dependencyNameVersions(deps []dependencyRef) []string {
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		names = append(names, dep.Name+"@"+dep.Version)
	}
	return names
}

func dependencyVersions(deps []dependencyRef) map[string]string {
	versions := make(map[string]string, len(deps))
	for _, dep := range deps {
		versions[dep.Name] = dep.Version
	}
	return versions
}

func dependencyVersionSpecs(deps []dependencyRef) map[string]string {
	versions := make(map[string]string, len(deps))
	for _, dep := range deps {
		versions[dep.Name] = dep.VersionSpec
	}
	return versions
}
