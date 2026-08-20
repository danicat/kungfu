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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	wikilinkRegex     = regexp.MustCompile("\\[\\[([^\\]\\|]+)(?:\\|[^\\]]+)?\\]\\]")
	markdownLinkRegex = regexp.MustCompile("\\[[^\\]]*\\]\\(([^\\)#]+)(?:#[^\\)]+)?\\)")
)

// OKFDocument represents an individual Open Knowledge Format markdown document.
type OKFDocument struct {
	Path        string       `json:"path"`
	RelPath     string       `json:"rel_path,omitempty"`
	Frontmatter *Frontmatter `json:"frontmatter"`
	Body        string       `json:"body"`
	Digest      string       `json:"digest"`
	Interlinks  []string     `json:"interlinks,omitempty"`
}

// OKFBundle represents a bundle of related OKF documents in a directory.
type OKFBundle struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Title       string        `json:"title,omitempty"`
	Description string        `json:"description,omitempty"`
	Category    string        `json:"category,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	Author      string        `json:"author,omitempty"`
	Version     string        `json:"version,omitempty"`
	Path        string        `json:"path"`
	Documents   []OKFDocument `json:"documents"`
	Manifest    *Frontmatter  `json:"manifest,omitempty"`
	Digest      string        `json:"digest,omitempty"`
}

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

// ExtractInterlinks scans markdown body for wiki links and relative markdown links.
func ExtractInterlinks(content string) []string {
	seen := make(map[string]struct{})
	var result []string
	wikiMatches := wikilinkRegex.FindAllStringSubmatch(content, -1)
	for _, match := range wikiMatches {
		if len(match) > 1 {
			target := strings.TrimSpace(match[1])
			if target != "" && !strings.HasPrefix(target, "#") {
				target = strings.TrimSuffix(target, ".md")
				if _, exists := seen[target]; !exists {
					seen[target] = struct{}{}
					result = append(result, target)
				}
			}
		}
	}
	mdMatches := markdownLinkRegex.FindAllStringSubmatch(content, -1)
	for _, match := range mdMatches {
		if len(match) > 1 {
			target := strings.TrimSpace(match[1])
			if target != "" && !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "mailto:") && !strings.HasPrefix(target, "#") {
				cleanTarget := strings.TrimSuffix(filepath.Base(target), ".md")
				if cleanTarget != "" && cleanTarget != "." {
					if _, exists := seen[cleanTarget]; !exists {
						seen[cleanTarget] = struct{}{}
						result = append(result, cleanTarget)
					}
				}
			}
		}
	}
	sort.Strings(result)
	return result
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
	AllowedTools  string         `yaml:"allowed-tools"`
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
		AllowedTools:  strings.TrimSpace(raw.AllowedTools),
		Metadata:      make(map[string]string),
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

// ParseOKFDocument reads a single markdown file, parses and validates frontmatter, computes digest and extracts interlinks.
func ParseOKFDocument(filePath string) (*OKFDocument, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading OKF document %s: %w", filePath, err)
	}
	fm, body, err := ParseFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("parsing OKF document %s: %w", filePath, err)
	}
	if err := ValidateFrontmatter(fm); err != nil {
		return nil, fmt.Errorf("validating OKF document %s: %w", filePath, err)
	}
	digest := ComputeDigest(data)
	bodyLinks := ExtractInterlinks(body)
	linkMap := make(map[string]struct{})
	for _, l := range bodyLinks {
		linkMap[l] = struct{}{}
	}
	var allLinks []string
	for l := range linkMap {
		allLinks = append(allLinks, l)
	}
	sort.Strings(allLinks)
	return &OKFDocument{
		Path:        filePath,
		RelPath:     filepath.Base(filePath),
		Frontmatter: fm,
		Body:        body,
		Digest:      digest,
		Interlinks:  allLinks,
	}, nil
}

// ParseOKFBundle parses an entire directory of OKF markdown files into an OKFBundle.
func ParseOKFBundle(dirPath string) (*OKFBundle, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("stat OKF bundle dir %s: %w", dirPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path %s is not a directory", dirPath)
	}
	bundleName := filepath.Base(dirPath)
	var docs []OKFDocument
	tagMap := make(map[string]struct{})
	var bundleManifest *Frontmatter
	manifestFiles := []string{"okf.yaml", "okf.yml", "bundle.yaml", "bundle.yml"}
	for _, mf := range manifestFiles {
		mfPath := filepath.Join(dirPath, mf)
		if mData, err := os.ReadFile(mfPath); err == nil {
			if fm, err := parseYAMLFrontmatter(mData); err == nil {
				bundleManifest = fm
				break
			}
		}
	}
	err = filepath.Walk(dirPath, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fi.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}
		doc, err := ParseOKFDocument(path)
		if err != nil {
			return nil
		}
		docs = append(docs, *doc)
		if doc.Frontmatter != nil && doc.Frontmatter.Metadata != nil {
			if tagsStr := doc.Frontmatter.Metadata["tags"]; tagsStr != "" {
				for _, t := range ParseTags(tagsStr) {
					tagMap[t] = struct{}{}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning OKF bundle %s: %w", dirPath, err)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no valid OKF documents found in bundle %s", dirPath)
	}
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Path < docs[j].Path
	})
	bundleID := bundleName
	bundleTitle := bundleName
	bundleDesc := fmt.Sprintf("OKF bundle with %d concept documents", len(docs))
	bundleCategory := "knowledge"
	bundleAuthor := ""
	bundleVersion := "1.0.0"
	if bundleManifest != nil {
		if bundleManifest.Name != "" {
			bundleID = bundleManifest.Name
			bundleTitle = bundleManifest.Name
		}
		if bundleManifest.Description != "" {
			bundleDesc = bundleManifest.Description
		}
		if cat := bundleManifest.Metadata["category"]; cat != "" {
			bundleCategory = cat
		}
		if tagsStr := bundleManifest.Metadata["tags"]; tagsStr != "" {
			for _, t := range ParseTags(tagsStr) {
				tagMap[t] = struct{}{}
			}
		}
		if author := bundleManifest.Metadata["author"]; author != "" {
			bundleAuthor = author
		}
		if ver := bundleManifest.Metadata["version"]; ver != "" {
			bundleVersion = ver
		}
	}
	var tags []string
	for t := range tagMap {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	var digests []string
	for _, d := range docs {
		digests = append(digests, d.Digest)
	}
	sort.Strings(digests)
	combinedDigest := ComputeDigest([]byte(strings.Join(digests, ";")))
	return &OKFBundle{
		ID:          bundleID,
		Name:        bundleName,
		Title:       bundleTitle,
		Description: bundleDesc,
		Category:    bundleCategory,
		Tags:        tags,
		Author:      bundleAuthor,
		Version:     bundleVersion,
		Path:        dirPath,
		Documents:   docs,
		Manifest:    bundleManifest,
		Digest:      combinedDigest,
	}, nil
}

// ToItem converts an OKFBundle to a standard catalog Item.
func (b *OKFBundle) ToItem(source string) Item {
	if source == "" {
		source = "local"
	}
	name := b.Name
	if b.Manifest != nil && b.Manifest.Name != "" {
		name = b.Manifest.Name
	}
	return Item{
		ID:          b.ID,
		Name:        name,
		Type:        ItemTypeOKF,
		Title:       b.Title,
		Description: b.Description,
		Category:    b.Category,
		Tags:        b.Tags,
		Author:      b.Author,
		Version:     b.Version,
		Path:        b.Path,
		Digest:      b.Digest,
		Source:      source,
	}
}
