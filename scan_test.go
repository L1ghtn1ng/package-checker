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
