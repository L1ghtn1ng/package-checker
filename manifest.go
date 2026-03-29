package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

var pythonRequirementPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+`)

type packageJSONManifest struct {
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

type pyprojectManifest struct {
	Project struct {
		Dependencies         []string            `toml:"dependencies"`
		OptionalDependencies map[string][]string `toml:"optional-dependencies"`
	} `toml:"project"`
	Tool struct {
		Poetry poetryManifest `toml:"poetry"`
	} `toml:"tool"`
}

type poetryManifest struct {
	Dependencies    map[string]any                 `toml:"dependencies"`
	DevDependencies map[string]any                 `toml:"dev-dependencies"`
	Group           map[string]poetryGroupManifest `toml:"group"`
}

type poetryGroupManifest struct {
	Dependencies map[string]any `toml:"dependencies"`
}

func parsePackageJSON(path string) ([]dependencyRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var manifest packageJSONManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	for _, group := range []map[string]string{
		manifest.Dependencies,
		manifest.DevDependencies,
		manifest.OptionalDependencies,
		manifest.PeerDependencies,
	} {
		for name := range group {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			deps = append(deps, dependencyRef{
				Name:      name,
				Source:    path,
				Ecosystem: ecosystemNPM,
			})
		}
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		return strings.Compare(a.Name, b.Name)
	})

	return deps, nil
}

func parsePyproject(path string) ([]dependencyRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var manifest pyprojectManifest
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		deps = append(deps, dependencyRef{
			Name:      name,
			Source:    path,
			Ecosystem: ecosystemPython,
		})
	}

	for _, requirement := range manifest.Project.Dependencies {
		add(extractPythonRequirementName(requirement))
	}

	for _, requirements := range manifest.Project.OptionalDependencies {
		for _, requirement := range requirements {
			add(extractPythonRequirementName(requirement))
		}
	}

	addPoetryDependencies(add, manifest.Tool.Poetry.Dependencies)
	addPoetryDependencies(add, manifest.Tool.Poetry.DevDependencies)

	for _, group := range manifest.Tool.Poetry.Group {
		addPoetryDependencies(add, group.Dependencies)
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		return strings.Compare(a.Name, b.Name)
	})

	return deps, nil
}

func parseRequirements(path string) ([]dependencyRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	seen := map[string]struct{}{}
	deps := make([]dependencyRef, 0)

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		deps = append(deps, dependencyRef{
			Name:      name,
			Source:    path,
			Ecosystem: ecosystemPython,
		})
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if name := extractEditableRequirementName(line); name != "" {
			add(name)
			continue
		}

		if strings.HasPrefix(line, "-") {
			continue
		}

		add(extractPythonRequirementName(line))
	}

	slices.SortFunc(deps, func(a, b dependencyRef) int {
		return strings.Compare(a.Name, b.Name)
	})

	return deps, nil
}

func extractEditableRequirementName(line string) string {
	for _, prefix := range []string{"-e ", "--editable "} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		parts := strings.SplitN(line, "#egg=", 2)
		if len(parts) != 2 {
			return ""
		}
		return extractPythonRequirementName(parts[1])
	}

	return ""
}

func extractPythonRequirementName(requirement string) string {
	candidate := strings.TrimSpace(requirement)
	if candidate == "" {
		return ""
	}

	if idx := strings.Index(candidate, ";"); idx >= 0 {
		candidate = candidate[:idx]
	}
	if idx := strings.Index(candidate, " @ "); idx >= 0 {
		candidate = candidate[:idx]
	}

	candidate = strings.TrimSpace(candidate)
	match := pythonRequirementPattern.FindString(candidate)
	return strings.ToLower(match)
}

func addPoetryDependencies(add func(string), deps map[string]any) {
	for name := range deps {
		if strings.EqualFold(name, "python") {
			continue
		}
		add(name)
	}
}
