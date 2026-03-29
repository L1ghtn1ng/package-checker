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

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
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

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
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
`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write requirements.txt: %v", err)
	}

	deps, err := parseRequirements(path)
	if err != nil {
		t.Fatalf("parse requirements.txt: %v", err)
	}

	got := dependencyNames(deps)
	want := []string{"editable_pkg", "flask", "pkgname", "requests"}
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
