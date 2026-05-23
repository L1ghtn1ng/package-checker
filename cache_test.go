package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadFeedUsesFreshCache(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	cachePath := filepath.Join(t.TempDir(), "feed.json")
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			hits.Add(1)
			if strings.HasSuffix(req.URL.Path, "/16/packages") {
				return jsonResponse(req, http.StatusOK, `{"packages":[{"name":"badpkg","version":"1.0.0","type":"npm","date_published":"2026-03-29"}]}`), nil
			}
			return jsonResponse(req, http.StatusNotFound, `{"error":"not found"}`), nil
		}),
	}
	now := time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC)

	entries, source, err := loadFeed(context.Background(), client, "https://example.test/feed.json", cachePath, now)
	if err != nil {
		t.Fatalf("load feed first call: %v", err)
	}
	if len(entries) != 1 || source != "remote" {
		t.Fatalf("unexpected first result: entries=%d source=%s", len(entries), source)
	}

	entries, source, err = loadFeed(context.Background(), client, "https://example.test/feed.json", cachePath, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("load feed second call: %v", err)
	}
	if len(entries) != 1 || source != "cache" {
		t.Fatalf("unexpected second result: entries=%d source=%s", len(entries), source)
	}
	if hits.Load() != 10 {
		t.Fatalf("expected ten upstream hits, got %d", hits.Load())
	}
}

func TestFetchSocketFeedSkipsSparse404sUntilMinimumStopID(t *testing.T) {
	t.Parallel()

	seenPaths := make([]string, 0)
	seenUserAgents := make([]string, 0)
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPaths = append(seenPaths, req.URL.Path)
			seenUserAgents = append(seenUserAgents, req.Header.Get("User-Agent"))
			if req.Header.Get("Accept") != "application/json" {
				t.Fatalf("unexpected accept header: %q", req.Header.Get("Accept"))
			}
			switch req.URL.Path {
			case "/attacks/16/packages":
				return jsonResponse(req, http.StatusOK, `[{"namespace":"scope","name":"pkg","version":"1.2.3","type":"npm"}]`), nil
			case "/attacks/21/packages":
				return jsonResponse(req, http.StatusOK, `{"data":[{"name":"requests","version":"2.31.0","type":"pypi"}]}`), nil
			case "/attacks/25/packages":
				return jsonResponse(req, http.StatusOK, `{"packages":[{"namespace":"laravel-lang","name":"lang","version":"1.0.2","type":"composer"}]}`), nil
			default:
				return jsonResponse(req, http.StatusNotFound, `{"error":"not found"}`), nil
			}
		}),
	}

	entries, err := fetchSocketFeed(context.Background(), client, "https://example.test/attacks/%d/packages", 16)
	if err != nil {
		t.Fatalf("fetch feed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	wantPaths := []string{
		"/attacks/16/packages",
		"/attacks/17/packages",
		"/attacks/18/packages",
		"/attacks/19/packages",
		"/attacks/20/packages",
		"/attacks/21/packages",
		"/attacks/22/packages",
		"/attacks/23/packages",
		"/attacks/24/packages",
		"/attacks/25/packages",
		"/attacks/26/packages",
	}
	if strings.Join(seenPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("unexpected request paths: got %v want %v", seenPaths, wantPaths)
	}
	for _, userAgent := range seenUserAgents {
		if userAgent != socketUserAgent {
			t.Fatalf("unexpected user agent: %q", userAgent)
		}
	}
}

func TestLoadFeedFallsBackToStaleCache(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "feed.json")
	cache := cachedFeed{
		FetchedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Entries: []feedEntry{
			{Title: "cached-bad", Platform: "npm", Type: "malicious", DatePublished: "2026-03-28"},
		},
	}
	if err := writeCachedFeed(cachePath, cache); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(req, http.StatusBadGateway, `{"error":"boom"}`), nil
		}),
	}
	entries, source, err := loadFeed(context.Background(), client, "https://example.test/feed.json", cachePath, time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("load feed: %v", err)
	}
	if len(entries) != 1 || entries[0].Title != "cached-bad" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if source != "stale-cache" {
		t.Fatalf("unexpected source: %s", source)
	}
}

func TestLoadFeedErrorsWithoutCacheOnFetchFailure(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "feed.json")
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(req, http.StatusBadGateway, `{"error":"boom"}`), nil
		}),
	}
	_, _, err := loadFeed(context.Background(), client, "https://example.test/feed.json", cachePath, time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestWriteCachedFeedCreatesUsableJSON(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "nested", "feed.json")
	cache := cachedFeed{
		FetchedAt: time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC),
		Entries: []feedEntry{
			{Title: "pkg", Platform: "pip", Type: "malicious", DatePublished: "2026-03-29"},
		},
	}

	if err := writeCachedFeed(cachePath, cache); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("stat cache: %v", err)
	}

	loaded, err := readCachedFeed(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if got := fmt.Sprintf("%s/%s", loaded.Entries[0].Title, loaded.Entries[0].Platform); got != "pkg/pip" {
		t.Fatalf("unexpected cache contents: %s", got)
	}
}

func TestWriteCachedFeedReplacesExistingFile(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "feed.json")
	first := cachedFeed{
		FetchedAt: time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC),
		Entries: []feedEntry{
			{Title: "first", Platform: "npm", Type: "malicious", DatePublished: "2026-03-29"},
		},
	}
	second := cachedFeed{
		FetchedAt: time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC),
		Entries: []feedEntry{
			{Title: "second", Platform: "pip", Type: "malicious", DatePublished: "2026-03-30"},
		},
	}

	if err := writeCachedFeed(cachePath, first); err != nil {
		t.Fatalf("write first cache: %v", err)
	}
	if err := writeCachedFeed(cachePath, second); err != nil {
		t.Fatalf("replace cache: %v", err)
	}

	loaded, err := readCachedFeed(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if got := loaded.Entries[0].Title; got != "second" {
		t.Fatalf("unexpected cached title after replace: %s", got)
	}
}

func TestLoadFeedUsesFreshEntriesWhenCacheWriteFails(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "cache-dir")
	if err := os.MkdirAll(cachePath, 0o755); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/16/packages") {
				return jsonResponse(req, http.StatusOK, `[{"name":"remote-bad","type":"npm","date_published":"2026-03-29"}]`), nil
			}
			return jsonResponse(req, http.StatusNotFound, `{"error":"not found"}`), nil
		}),
	}

	entries, source, err := loadFeed(context.Background(), client, "https://example.test/feed.json", cachePath, time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("load feed: %v", err)
	}
	if source != "remote" {
		t.Fatalf("unexpected source: %s", source)
	}
	if len(entries) != 1 || entries[0].Name != "remote-bad" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
