package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const cacheTTL = 24 * time.Hour

func loadFeed(ctx context.Context, client *http.Client, feedURL, cachePath string, now time.Time) ([]feedEntry, string, error) {
	cache, cacheErr := readCachedFeed(cachePath)
	if cacheErr == nil && now.Sub(cache.FetchedAt) < cacheTTL {
		return cache.Entries, "cache", nil
	}

	entries, err := fetchFeed(ctx, client, feedURL)
	if err == nil {
		cache = cachedFeed{
			FetchedAt: now.UTC(),
			Entries:   entries,
		}
		if writeErr := writeCachedFeed(cachePath, cache); writeErr != nil {
			return entries, "remote", nil
		}
		return entries, "remote", nil
	}

	if cacheErr == nil {
		return cache.Entries, "stale-cache", nil
	}

	return nil, "", fmt.Errorf("fetch feed: %w", err)
}

func fetchFeed(ctx context.Context, client *http.Client, feedURL string) ([]feedEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}

	var entries []feedEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return entries, nil
}

func readCachedFeed(cachePath string) (cachedFeed, error) {
	var cache cachedFeed

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return cache, err
	}

	if err := json.Unmarshal(data, &cache); err != nil {
		return cache, fmt.Errorf("parse cache file: %w", err)
	}

	if cache.FetchedAt.IsZero() {
		return cache, fmt.Errorf("cache file missing fetched_at timestamp")
	}

	return cache, nil
}

func writeCachedFeed(cachePath string, cache cachedFeed) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}

	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write temp cache: %w", err)
	}

	if err := os.Rename(tmpPath, cachePath); err != nil {
		if removeErr := os.Remove(cachePath); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("remove existing cache file: %w", removeErr)
		}
		if renameErr := os.Rename(tmpPath, cachePath); renameErr != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("replace cache file: %w", renameErr)
		}
	}

	return nil
}
