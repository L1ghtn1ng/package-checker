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
			return jsonResponse(req, http.StatusOK, `[{"title":"badpkg","platform":"npm","type":"malicious","date_published":"2026-03-29"}]`), nil
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
	if hits.Load() != 1 {
		t.Fatalf("expected one upstream hit, got %d", hits.Load())
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
			return jsonResponse(req, http.StatusOK, `[{"title":"remote-bad","platform":"npm","type":"malicious","date_published":"2026-03-29"}]`), nil
		}),
	}

	entries, source, err := loadFeed(context.Background(), client, "https://example.test/feed.json", cachePath, time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("load feed: %v", err)
	}
	if source != "remote" {
		t.Fatalf("unexpected source: %s", source)
	}
	if len(entries) != 1 || entries[0].Title != "remote-bad" {
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
