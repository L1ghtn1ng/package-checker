package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExitCodeMatch(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	cacheFile := filepath.Join(t.TempDir(), "cache.json")

	manifest := `{"dependencies":{"badpkg":"1.0.0","safe":"1.0.0"}}`
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/16/packages") {
				return jsonResponse(req, http.StatusOK, `[{"name":"badpkg","version":"1.0.0","type":"npm","date_published":"2026-03-29"}]`), nil
			}
			return jsonResponse(req, http.StatusNotFound, `{"error":"not found"}`), nil
		}),
	}

	var stdout strings.Builder
	var stderr strings.Builder
	code := runWithClient(context.Background(), &stdout, &stderr, []string{
		"--dir", projectDir,
		"--cache-file", cacheFile,
		"--feed-url", "https://example.test/feed.json",
	}, client)

	if code != 1 {
		t.Fatalf("expected exit code 1, got %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "badpkg") {
		t.Fatalf("expected finding in stdout, got %s", stdout.String())
	}
}

func TestRunExitCodeClean(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	cacheFile := filepath.Join(t.TempDir(), "cache.json")

	manifest := "[project]\ndependencies = [\"requests>=2.0\"]\n"
	if err := os.WriteFile(filepath.Join(projectDir, "pyproject.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/16/packages") {
				return jsonResponse(req, http.StatusOK, `[{"name":"badpkg","type":"pypi","date_published":"2026-03-29"}]`), nil
			}
			return jsonResponse(req, http.StatusNotFound, `{"error":"not found"}`), nil
		}),
	}

	var stdout strings.Builder
	var stderr strings.Builder
	code := runWithClient(context.Background(), &stdout, &stderr, []string{
		"--dir", projectDir,
		"--cache-file", cacheFile,
		"--feed-url", "https://example.test/feed.json",
	}, client)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No malicious packages detected") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}
