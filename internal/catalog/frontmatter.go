// Copyright 2026 Daniela Petruzalek
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
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxFrontmatterYAMLBytes = 64 * 1024 // 64 KiB

// ExtractFrontmatter extracts the raw YAML frontmatter and body from markdown content.
// Requires an opening "---" followed immediately by a newline and a column-0 closing "---" or "...".
func ExtractFrontmatter(data []byte) ([]byte, []byte, bool, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil, data, false, nil
	}

	before, after, ok := bytes.Cut(trimmed, []byte{'\n'})
	if !ok {
		return nil, data, false, nil
	}
	openLine := bytes.TrimRight(before, " \t\r")
	if !bytes.Equal(openLine, []byte("---")) {
		return nil, data, false, nil
	}

	rest := after
	offset := 0
	for offset <= len(rest) {
		lineEnd := bytes.IndexByte(rest[offset:], '\n')
		var line []byte
		var nextOffset int
		if lineEnd == -1 {
			line = rest[offset:]
			nextOffset = len(rest)
		} else {
			line = rest[offset : offset+lineEnd]
			nextOffset = offset + lineEnd + 1
		}

		cleanLine := bytes.TrimRight(line, " \t\r")
		if bytes.Equal(cleanLine, []byte("---")) || bytes.Equal(cleanLine, []byte("...")) {
			rawYAML := rest[:offset]
			if len(rawYAML) > maxFrontmatterYAMLBytes {
				return nil, data, false, fmt.Errorf("%w: frontmatter exceeds %d bytes", ErrInvalidFrontmatter, maxFrontmatterYAMLBytes)
			}
			body := rest[nextOffset:]
			return rawYAML, body, true, nil
		}

		if lineEnd == -1 {
			break
		}
		offset = nextOffset
	}

	return nil, data, false, fmt.Errorf("%w: unclosed frontmatter delimiter", ErrInvalidFrontmatter)
}

// ComputeDigest returns the standard sha256:hex digest for byte content.
func ComputeDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

// NormalizeDigest normalizes a sha256 digest string to canonical lowercase sha256:<hex>.
// Unsupported algorithm prefixes (e.g. "sha512:...") are preserved unchanged so VerifyDigest rejects them.
func NormalizeDigest(d string) string {
	clean := strings.TrimSpace(d)
	if clean == "" {
		return ""
	}
	if algo, hexPart, hasColon := strings.Cut(clean, ":"); hasColon {
		if strings.EqualFold(algo, "sha256") {
			return "sha256:" + strings.ToLower(strings.TrimSpace(hexPart))
		}
		return strings.ToLower(algo) + ":" + strings.ToLower(strings.TrimSpace(hexPart))
	}
	return "sha256:" + strings.ToLower(clean)
}

// VerifyDigest verifies whether data matches the expected sha256 digest when expected is non-empty.
// Rejects unsupported digest algorithms explicitly.
func VerifyDigest(data []byte, expected string) error {
	cleanExpected := NormalizeDigest(expected)
	if cleanExpected == "" {
		return nil
	}
	if !strings.HasPrefix(cleanExpected, "sha256:") {
		return fmt.Errorf("%w: unsupported digest algorithm in %q (only sha256 is supported)", ErrDigestMismatch, expected)
	}
	actual := ComputeDigest(data)
	if actual != cleanExpected {
		return fmt.Errorf("%w: expected %s, computed %s", ErrDigestMismatch, cleanExpected, actual)
	}
	return nil
}

// VerifyRequiredDigest verifies data against expected and fails closed with ErrMissingDigest if expected is empty.
func VerifyRequiredDigest(data []byte, expected string) error {
	if strings.TrimSpace(expected) == "" {
		return fmt.Errorf("%w: archive requires a sha256 digest", ErrMissingDigest)
	}
	return VerifyDigest(data, expected)
}

// ApplyFrontmatter populates fields on item from fm.
// When overwrite is false, only empty fields are filled.
func ApplyFrontmatter(item *Item, fm *Frontmatter, overwrite bool) {
	if item == nil || fm == nil {
		return
	}
	if fm.Description != "" && (overwrite || item.Description == "") {
		item.Description = fm.Description
	}
	if fm.License != "" && (overwrite || item.License == "") {
		item.License = fm.License
	}
	if fm.AllowedTools != "" && (overwrite || item.AllowedTools == "") {
		item.AllowedTools = fm.AllowedTools
	}
	if fm.Metadata != nil {
		if item.Metadata == nil {
			item.Metadata = make(map[string]string, len(fm.Metadata))
		}
		for k, v := range fm.Metadata {
			if overwrite || item.Metadata[k] == "" {
				item.Metadata[k] = v
			}
		}
		if v := fm.Metadata["version"]; v != "" && (overwrite || item.Version == "") {
			item.Version = v
		}
		if v := fm.Metadata["author"]; v != "" && (overwrite || item.Author == "") {
			item.Author = v
		}
		if v := fm.Metadata["tags"]; v != "" && (overwrite || len(item.Tags) == 0) {
			item.Tags = ParseTags(v)
		}
	}
}

// EnrichItemFromSkillMD populates missing metadata fields on item from a SKILL.md document's frontmatter.
func EnrichItemFromSkillMD(item *Item, data []byte) {
	if item == nil || len(data) == 0 {
		return
	}
	fm, _, err := ParseFrontmatter(data)
	if err != nil || fm == nil {
		return
	}
	ApplyFrontmatter(item, fm, false)
}

type rawFrontmatter struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	License       string         `yaml:"license"`
	Compatibility string         `yaml:"compatibility"`
	AllowedTools  any            `yaml:"allowed-tools"`
	Version       any            `yaml:"version"`
	Metadata      map[string]any `yaml:"metadata"`
}

func stringifyYAMLValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []any:
		parts := make([]string, 0, len(val))
		for _, elem := range val {
			if elem != nil {
				parts = append(parts, stringifyYAMLValue(elem))
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any, map[any]any:
		if b, err := json.Marshal(val); err == nil {
			return string(b)
		}
		return fmt.Sprint(val)
	default:
		return fmt.Sprint(val)
	}
}

func parseYAMLFrontmatter(data []byte) (*Frontmatter, error) {
	if len(data) > maxFrontmatterYAMLBytes {
		return nil, fmt.Errorf("frontmatter exceeds %d bytes", maxFrontmatterYAMLBytes)
	}
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

	if raw.AllowedTools != nil {
		fm.AllowedTools = strings.TrimSpace(stringifyYAMLValue(raw.AllowedTools))
	}

	for k, v := range raw.Metadata {
		if v == nil {
			continue
		}
		fm.Metadata[k] = stringifyYAMLValue(v)
	}
	if raw.Version != nil && fm.Metadata["version"] == "" {
		if v := strings.TrimSpace(stringifyYAMLValue(raw.Version)); v != "" {
			fm.Metadata["version"] = v
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
