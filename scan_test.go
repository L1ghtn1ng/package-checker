package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestScanDirectoryMatchesSocketPackagesByEcosystemNameAndVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	files := map[string]string{
		"package.json":     `{"dependencies":{"@scope/bad":"1.2.3","plain-bad":"4.5.6","range-only":"^1.0.0"}}`,
		"pyproject.toml":   "[project]\ndependencies = [\"Bad_Py==2.0.0\"]\n",
		"go.mod":           "module example.test/app\n\nrequire github.com/bad/module v0.1.0\n",
		"composer.json":    `{"require":{"vendor/bad":"3.2.1"}}`,
		"requirements.txt": "safe==1.0.0\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	entries := []feedEntry{
		{Namespace: "scope", Name: "bad", Version: "1.2.3", Type: "npm"},
		{Name: "plain-bad", Versions: []string{"4.5.6"}, Type: "npm"},
		{Name: "range-only", Version: "1.0.0", Type: "npm"},
		{Name: "bad-py", Version: "2.0.0", Type: "pypi"},
		{Name: "github.com/bad/module", Version: "0.1.0", Type: "golang"},
		{Name: "vendor/bad", Version: "3.2.1", Type: "composer"},
	}

	findings, scannedFiles, err := scanDirectory(dir, entries)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scannedFiles != len(files) {
		t.Fatalf("unexpected scanned files: got %d want %d", scannedFiles, len(files))
	}

	got := make([]string, 0, len(findings))
	for _, finding := range findings {
		got = append(got, string(finding.Dependency.Ecosystem)+":"+finding.Dependency.Name+"@"+finding.Dependency.Version)
	}
	want := []string{
		"php:vendor/bad@3.2.1",
		"golang:github.com/bad/module@0.1.0",
		"npm:@scope/bad@1.2.3",
		"npm:plain-bad@4.5.6",
		"python:bad_py@2.0.0",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected findings: got %v want %v", got, want)
	}
}

func TestScanDirectoryMatchesInstalledNodeModulesPackages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{}}`), 0o644); err != nil {
		t.Fatalf("write root package.json: %v", err)
	}

	nodeModulesFiles := map[string]string{
		"node_modules/plain/package.json":                     `{"name":"plain","version":"1.2.3"}`,
		"node_modules/@scope/pkg/package.json":                `{"name":"@scope/pkg","version":"4.5.6"}`,
		"node_modules/no-name/package.json":                   `{"version":"7.8.9"}`,
		"node_modules/range-mismatch/package.json":            `{"name":"range-mismatch","version":"2.0.0"}`,
		"node_modules/plain/node_modules/nested/package.json": `{"name":"nested","version":"9.9.9"}`,
	}
	for name, data := range nodeModulesFiles {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s parent: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	entries := []feedEntry{
		{Name: "plain", Version: "1.2.3", Type: "npm"},
		{Namespace: "scope", Name: "pkg", Version: "4.5.6", Type: "npm"},
		{Name: "no-name", Version: "7.8.9", Type: "npm"},
		{Name: "range-mismatch", Version: "1.0.0", Type: "npm"},
		{Name: "nested", Version: "9.9.9", Type: "npm"},
	}

	findings, scannedFiles, err := scanDirectory(dir, entries)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scannedFiles != 5 {
		t.Fatalf("unexpected scanned files: got %d want 5", scannedFiles)
	}

	got := make([]string, 0, len(findings))
	for _, finding := range findings {
		got = append(got, finding.Dependency.Name+"@"+finding.Dependency.Version)
	}
	want := []string{"@scope/pkg@4.5.6", "no-name@7.8.9", "plain@1.2.3"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected findings: got %v want %v", got, want)
	}
}

func TestScanDirectoryScansNodeModulesWithoutRootManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	packageJSONPath := filepath.Join(dir, "node_modules", "plain", "package.json")
	if err := os.MkdirAll(filepath.Dir(packageJSONPath), 0o755); err != nil {
		t.Fatalf("create node_modules package dir: %v", err)
	}
	if err := os.WriteFile(packageJSONPath, []byte(`{"name":"plain","version":"1.2.3"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	findings, scannedFiles, err := scanDirectory(dir, []feedEntry{
		{Name: "plain", Version: "1.2.3", Type: "npm"},
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scannedFiles != 1 {
		t.Fatalf("unexpected scanned files: got %d want 1", scannedFiles)
	}
	if len(findings) != 1 || findings[0].Dependency.Name != "plain" {
		t.Fatalf("unexpected findings: %+v", findings)
	}
}
