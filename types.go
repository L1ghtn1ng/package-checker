package main

import "time"

type ecosystem string

const (
	ecosystemNPM    ecosystem = "npm"
	ecosystemPython ecosystem = "python"
)

type feedEntry struct {
	Title         string `json:"title"`
	Description   string `json:"description"`
	DatePublished string `json:"date_published"`
	Platform      string `json:"platform"`
	DownloadsText string `json:"downloads_text"`
	Type          string `json:"type"`
}

type cachedFeed struct {
	FetchedAt time.Time   `json:"fetched_at"`
	Entries   []feedEntry `json:"entries"`
}

type dependencyRef struct {
	Name      string
	Source    string
	Ecosystem ecosystem
}

type finding struct {
	Dependency dependencyRef
	Feed       feedEntry
}
