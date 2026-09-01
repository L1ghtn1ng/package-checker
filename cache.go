package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const cacheTTL = 24 * time.Hour
const socketFeedStartID = 16

// Socket campaign IDs are sparse before the requested campaign 25 endpoint.
const socketFeedMinimumStopID = 25
const socketFeedConsecutiveNotFoundLimit = 3
const socketUserAgent = "package-checker/1.0"

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
			// Fresh remote entries remain usable even when the optional cache cannot be updated.
			return entries, "remote", nil //nolint:nilerr
		}
		return entries, "remote", nil
	}

	if cacheErr == nil {
		return cache.Entries, "stale-cache", nil
	}

	return nil, "", fmt.Errorf("fetch feed: %w", err)
}

func fetchFeed(ctx context.Context, client *http.Client, feedURL string) ([]feedEntry, error) {
	return fetchSocketFeed(ctx, client, feedURL, socketFeedStartID)
}

func fetchSocketFeed(ctx context.Context, client *http.Client, feedURL string, startID int) ([]feedEntry, error) {
	entries := make([]feedEntry, 0)
	consecutiveNotFound := 0
	for id := startID; ; id++ {
		pageEntries, err := fetchSocketFeedPage(ctx, client, socketFeedPageURL(feedURL, id))
		if err != nil {
			if errors.Is(err, errFeedPageNotFound) {
				if id < socketFeedMinimumStopID {
					continue
				}
				consecutiveNotFound++
				if consecutiveNotFound >= socketFeedConsecutiveNotFoundLimit {
					return entries, nil
				}
				continue
			}
			return nil, err
		}
		consecutiveNotFound = 0
		entries = append(entries, pageEntries...)
	}
}

var errFeedPageNotFound = errors.New("feed page not found")

func socketFeedPageURL(feedURL string, id int) string {
	if strings.Contains(feedURL, "%d") {
		return fmt.Sprintf(feedURL, id)
	}
	return strings.TrimRight(feedURL, "/") + "/" + strconv.Itoa(id) + "/packages"
}

func fetchSocketFeedPage(ctx context.Context, client *http.Client, feedURL string) ([]feedEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", socketUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errFeedPageNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}

	var payload socketFeedPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return payload.entries(), nil
}

type socketFeedPayload struct {
	Entries  []feedEntry `json:"entries"`
	Packages []feedEntry `json:"packages"`
	Data     []feedEntry `json:"data"`
	Results  []feedEntry `json:"results"`
}

func (p *socketFeedPayload) UnmarshalJSON(data []byte) error {
	var entries []feedEntry
	if err := json.Unmarshal(data, &entries); err == nil {
		p.Entries = entries
		return nil
	}

	type payload socketFeedPayload
	var obj payload
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	*p = socketFeedPayload(obj)
	return nil
}

func (p socketFeedPayload) entries() []feedEntry {
	switch {
	case len(p.Entries) > 0:
		return p.Entries
	case len(p.Packages) > 0:
		return p.Packages
	case len(p.Data) > 0:
		return p.Data
	default:
		return p.Results
	}
}

func readCachedFeed(cachePath string) (cachedFeed, error) {
	var cache cachedFeed

	// cachePath is either user-selected or resolved inside the user's cache directory.
	data, err := os.ReadFile(cachePath) //nolint:gosec
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
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}

	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
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
