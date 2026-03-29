package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func scanDirectory(dir string, entries []feedEntry) ([]finding, int, error) {
	manifests := []struct {
		filename string
		parser   func(string) ([]dependencyRef, error)
	}{
		{filename: "package.json", parser: parsePackageJSON},
		{filename: "pyproject.toml", parser: parsePyproject},
		{filename: "requirements.txt", parser: parseRequirements},
	}

	index := buildFeedIndex(entries)
	findings := make([]finding, 0)
	seen := map[string]struct{}{}
	scannedFiles := 0

	for _, manifest := range manifests {
		path := filepath.Join(dir, manifest.filename)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, 0, fmt.Errorf("stat %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}

		scannedFiles++
		deps, err := manifest.parser(path)
		if err != nil {
			return nil, 0, err
		}

		for _, dep := range deps {
			match, ok := index.match(dep)
			if !ok {
				continue
			}

			key := string(dep.Ecosystem) + "|" + dep.Source + "|" + normalizeDependencyName(dep.Ecosystem, dep.Name)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			findings = append(findings, finding{
				Dependency: dep,
				Feed:       match,
			})
		}
	}

	slices.SortFunc(findings, func(a, b finding) int {
		if cmp := strings.Compare(a.Dependency.Source, b.Dependency.Source); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Dependency.Name, b.Dependency.Name)
	})

	return findings, scannedFiles, nil
}

type feedIndex struct {
	npm    map[string]feedEntry
	python map[string]feedEntry
}

func buildFeedIndex(entries []feedEntry) feedIndex {
	index := feedIndex{
		npm:    make(map[string]feedEntry),
		python: make(map[string]feedEntry),
	}

	for _, entry := range entries {
		switch mapFeedPlatform(entry.Platform) {
		case ecosystemNPM:
			key := normalizeDependencyName(ecosystemNPM, entry.Title)
			if key != "" {
				index.npm[key] = entry
			}
		case ecosystemPython:
			key := normalizeDependencyName(ecosystemPython, entry.Title)
			if key != "" {
				index.python[key] = entry
			}
		}
	}

	return index
}

func (f feedIndex) match(dep dependencyRef) (feedEntry, bool) {
	key := normalizeDependencyName(dep.Ecosystem, dep.Name)
	switch dep.Ecosystem {
	case ecosystemNPM:
		entry, ok := f.npm[key]
		return entry, ok
	case ecosystemPython:
		entry, ok := f.python[key]
		return entry, ok
	default:
		return feedEntry{}, false
	}
}

func mapFeedPlatform(platform string) ecosystem {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "npm":
		return ecosystemNPM
	case "pypi", "pip", "python":
		return ecosystemPython
	default:
		return ""
	}
}

func normalizeDependencyName(kind ecosystem, name string) string {
	name = strings.TrimSpace(name)
	switch kind {
	case ecosystemNPM:
		return strings.ToLower(name)
	case ecosystemPython:
		name = strings.ToLower(name)
		replacer := strings.NewReplacer("_", "-", ".", "-")
		name = replacer.Replace(name)
		for strings.Contains(name, "--") {
			name = strings.ReplaceAll(name, "--", "-")
		}
		return strings.Trim(name, "-")
	default:
		return name
	}
}
