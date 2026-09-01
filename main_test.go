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
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(manifest), 0o600); err != nil {
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
	if err := os.WriteFile(filepath.Join(projectDir, "pyproject.toml"), []byte(manifest), 0o600); err != nil {
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

func TestRunReportsOutputWriteFailure(t *testing.T) {
	t.Parallel()

	var stderr strings.Builder
	code := runWithClient(context.Background(), failingWriter{}, &stderr, []string{"--help"}, &http.Client{})

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "write output") {
		t.Fatalf("expected output error in stderr, got %s", stderr.String())
	}
}

func TestParseFlagsRejectsConflictingSelfUpdateOptions(t *testing.T) {
	t.Parallel()

	var stderr strings.Builder
	_, err := parseFlags(&stderr, []string{"--self-update", "--no-self-update"})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("parseFlags error = %v", err)
	}
	if !strings.Contains(stderr.String(), "--self-update") || !strings.Contains(stderr.String(), "--no-self-update") {
		t.Fatalf("usage does not list self-update flags: %s", stderr.String())
	}
}

func TestForcedSelfUpdateRejectsDevelopmentBuild(t *testing.T) {
	var stdout strings.Builder
	var stderr strings.Builder
	code := runWithClient(context.Background(), &stdout, &stderr, []string{"--self-update"}, &http.Client{})

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unavailable for development builds") {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, os.ErrClosed
}
