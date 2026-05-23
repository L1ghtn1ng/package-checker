package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

type ecosystem string

const (
	ecosystemNPM    ecosystem = "npm"
	ecosystemPython ecosystem = "python"
	ecosystemGo     ecosystem = "golang"
	ecosystemPHP    ecosystem = "php"
)

type feedEntry struct {
	Name          string    `json:"name"`
	Namespace     string    `json:"namespace"`
	Version       string    `json:"version"`
	Versions      []string  `json:"versions"`
	Ecosystem     ecosystem `json:"ecosystem"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	DatePublished string    `json:"date_published"`
	Platform      string    `json:"platform"`
	DownloadsText string    `json:"downloads_text"`
	Type          string    `json:"type"`
}

func (e *feedEntry) UnmarshalJSON(data []byte) error {
	type rawFeedEntry feedEntry
	var raw rawFeedEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	entry := feedEntry(raw)
	entry.Name = firstString(entry.Name, fields, "package", "package_name", "packageName")
	entry.Namespace = firstString(entry.Namespace, fields, "scope", "package_namespace", "packageNamespace")
	entry.Version = firstString(entry.Version, fields, "package_version", "packageVersion")
	if len(entry.Versions) == 0 {
		entry.Versions = firstStringSlice(fields, "versions", "package_versions", "packageVersions")
	}
	if entry.Platform == "" {
		entry.Platform = firstString("", fields, "registry", "package_type", "packageType")
	}
	if entry.Ecosystem == "" {
		entry.Ecosystem = ecosystem(firstString("", fields, "ecosystem", "platform"))
	}
	if entry.DatePublished == "" {
		entry.DatePublished = firstString("", fields, "detected_at", "detectedAt", "created_at", "createdAt")
	}
	if entry.Name == "" {
		entry.Name, entry.Version, entry.Ecosystem = parsePURL(firstString("", fields, "purl", "package_url", "packageURL"))
	}

	*e = entry
	return nil
}

func firstString(current string, fields map[string]any, names ...string) string {
	if current != "" {
		return current
	}
	for _, name := range names {
		if value, ok := fields[name].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstStringSlice(fields map[string]any, names ...string) []string {
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		values, ok := raw.([]any)
		if !ok {
			continue
		}
		result := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
		return result
	}
	return nil
}

func parsePURL(purl string) (string, string, ecosystem) {
	if !strings.HasPrefix(purl, "pkg:") {
		return "", "", ""
	}
	value := strings.TrimPrefix(purl, "pkg:")
	ecosystemName, remainder, ok := strings.Cut(value, "/")
	if !ok {
		return "", "", ""
	}
	name := remainder
	version := ""
	if at := strings.LastIndex(remainder, "@"); at > 0 {
		name = remainder[:at]
		version = remainder[at+1:]
	}
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	return name, normalizeVersion(version), mapFeedPlatform(ecosystemName)
}

type cachedFeed struct {
	FetchedAt time.Time   `json:"fetched_at"`
	Entries   []feedEntry `json:"entries"`
}

type dependencyRef struct {
	Name      string
	Version   string
	Source    string
	Ecosystem ecosystem
}

type finding struct {
	Dependency dependencyRef
	Feed       feedEntry
}
