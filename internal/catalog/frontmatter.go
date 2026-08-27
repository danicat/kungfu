// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package catalog

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExtractFrontmatter extracts the raw YAML frontmatter and body from markdown content.
func ExtractFrontmatter(data []byte) ([]byte, []byte, bool, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil, data, false, nil
	}
	rest := bytes.TrimPrefix(trimmed, []byte("---"))
	delims := [][]byte{
		[]byte("\n---\n"),
		[]byte("\r\n---\r\n"),
		[]byte("\n---\r\n"),
		[]byte("\r\n---\n"),
		[]byte("\n...\n"),
		[]byte("\r\n...\r\n"),
	}
	bestIdx := -1
	delimLen := 0
	for _, delim := range delims {
		idx := bytes.Index(rest, delim)
		if idx != -1 && (bestIdx == -1 || idx < bestIdx) {
			bestIdx = idx
			delimLen = len(delim)
		}
	}
	if bestIdx == -1 {
		if bytes.HasSuffix(rest, []byte("\n---")) {
			bestIdx = len(rest) - len([]byte("\n---"))
			delimLen = len([]byte("\n---"))
		} else if bytes.HasSuffix(rest, []byte("\r\n---")) {
			bestIdx = len(rest) - len([]byte("\r\n---"))
			delimLen = len([]byte("\r\n---"))
		} else if bytes.HasSuffix(rest, []byte("\n...")) {
			bestIdx = len(rest) - len([]byte("\n..."))
			delimLen = len([]byte("\n..."))
		} else if bytes.HasSuffix(rest, []byte("\r\n...")) {
			bestIdx = len(rest) - len([]byte("\r\n..."))
			delimLen = len([]byte("\r\n..."))
		}
	}
	if bestIdx == -1 {
		return nil, data, false, fmt.Errorf("%w: unclosed frontmatter delimiter", ErrInvalidFrontmatter)
	}
	rawYAML := rest[:bestIdx]
	body := rest[bestIdx+delimLen:]
	return rawYAML, body, true, nil
}

// ComputeDigest returns the standard sha256:hex digest for byte content.
func ComputeDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

// VerifyDigest verifies whether the content matches the expected sha256 digest.
func VerifyDigest(data []byte, expected string) error {
	if expected == "" {
		return nil
	}
	actual := ComputeDigest(data)
	cleanExpected := strings.TrimSpace(expected)
	if !strings.HasPrefix(cleanExpected, "sha256:") {
		cleanExpected = "sha256:" + cleanExpected
	}
	if actual != cleanExpected {
		return fmt.Errorf("%w: expected %s, computed %s", ErrDigestMismatch, cleanExpected, actual)
	}
	return nil
}

// ValidateFrontmatter verifies that required fields per the Agent Skills spec are populated.
func ValidateFrontmatter(fm *Frontmatter) error {
	if fm == nil {
		return fmt.Errorf("%w: frontmatter is nil", ErrInvalidFrontmatter)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return fmt.Errorf("%w: missing name", ErrInvalidFrontmatter)
	}
	if strings.TrimSpace(fm.Description) == "" {
		return fmt.Errorf("%w: missing description", ErrInvalidFrontmatter)
	}
	return nil
}

type rawFrontmatter struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license"`
	Compatibility string         `yaml:"compatibility"`
	AllowedTools  any            `yaml:"allowed-tools"`
	Metadata      map[string]any `yaml:"metadata"`
}

func parseYAMLFrontmatter(data []byte) (*Frontmatter, error) {
	var raw rawFrontmatter
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	fm := &Frontmatter{
		Name:          strings.TrimSpace(raw.Name),
		Description:   strings.TrimSpace(raw.Description),
		License:       strings.TrimSpace(raw.License),
		Compatibility: strings.TrimSpace(raw.Compatibility),
		Metadata:      make(map[string]string),
	}

	switch at := raw.AllowedTools.(type) {
	case string:
		fm.AllowedTools = strings.TrimSpace(at)
	case []any:
		var tools []string
		for _, t := range at {
			if t != nil {
				tools = append(tools, strings.TrimSpace(fmt.Sprint(t)))
			}
		}
		fm.AllowedTools = strings.Join(tools, ", ")
	default:
		if raw.AllowedTools != nil {
			fm.AllowedTools = strings.TrimSpace(fmt.Sprint(raw.AllowedTools))
		}
	}

	for k, v := range raw.Metadata {
		if v == nil {
			continue
		}
		switch val := v.(type) {
		case string:
			fm.Metadata[k] = val
		case []any:
			var strParts []string
			for _, item := range val {
				if item != nil {
					strParts = append(strParts, fmt.Sprint(item))
				}
			}
			fm.Metadata[k] = strings.Join(strParts, ", ")
		default:
			fm.Metadata[k] = fmt.Sprint(val)
		}
	}

	return fm, nil
}

// ParseFrontmatter parses YAML frontmatter and returns the Frontmatter struct and document body.
func ParseFrontmatter(data []byte) (*Frontmatter, string, error) {
	rawYAML, body, found, err := ExtractFrontmatter(data)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, string(data), fmt.Errorf("%w: no frontmatter found", ErrInvalidFrontmatter)
	}
	fm, err := parseYAMLFrontmatter(rawYAML)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrInvalidFrontmatter, err.Error())
	}
	return fm, string(body), nil
}
