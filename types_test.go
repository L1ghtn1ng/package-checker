package main

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestFeedEntryUnmarshalSocketFieldVariants(t *testing.T) {
	t.Parallel()

	var entry feedEntry
	data := []byte(`{
		"packageName": "pkg",
		"packageNamespace": "scope",
		"packageVersions": ["1.2.3", "1.2.4"],
		"packageType": "npm",
		"detectedAt": "2026-05-23T10:00:00Z"
	}`)
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal feed entry: %v", err)
	}

	if got := feedPackageName(entry, feedEcosystem(entry)); got != "@scope/pkg" {
		t.Fatalf("unexpected package name: %s", got)
	}
	if !slices.Equal(entry.Versions, []string{"1.2.3", "1.2.4"}) {
		t.Fatalf("unexpected versions: %v", entry.Versions)
	}
	if feedEcosystem(entry) != ecosystemNPM {
		t.Fatalf("unexpected ecosystem: %s", feedEcosystem(entry))
	}
	if entry.DatePublished != "2026-05-23T10:00:00Z" {
		t.Fatalf("unexpected detected date: %s", entry.DatePublished)
	}
}

func TestFeedEntryUnmarshalPURL(t *testing.T) {
	t.Parallel()

	var entry feedEntry
	if err := json.Unmarshal([]byte(`{"purl":"pkg:npm/%40scope/pkg@1.2.3"}`), &entry); err != nil {
		t.Fatalf("unmarshal feed entry: %v", err)
	}

	if entry.Name != "@scope/pkg" {
		t.Fatalf("unexpected purl name: %s", entry.Name)
	}
	if entry.Version != "1.2.3" {
		t.Fatalf("unexpected purl version: %s", entry.Version)
	}
	if entry.Ecosystem != ecosystemNPM {
		t.Fatalf("unexpected purl ecosystem: %s", entry.Ecosystem)
	}
}

func TestFeedEntryUnmarshalKeepsVersionFromPURLWhenNameIsPresent(t *testing.T) {
	t.Parallel()

	var entry feedEntry
	data := []byte(`{
		"name": "pkg",
		"type": "npm",
		"purl": "pkg:npm/pkg@1.2.3"
	}`)
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal feed entry: %v", err)
	}

	if entry.Name != "pkg" {
		t.Fatalf("unexpected name: %s", entry.Name)
	}
	if entry.Version != "1.2.3" {
		t.Fatalf("expected purl version, got %q", entry.Version)
	}
	if feedVersionMatches(dependencyRef{Name: "pkg", Version: "2.0.0", Ecosystem: ecosystemNPM}, entry) {
		t.Fatal("versioned purl entry unexpectedly matched a different dependency version")
	}
}
