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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Common errors returned by the catalog package.
var (
	ErrItemNotFound       = errors.New("item not found")
	ErrInvalidFrontmatter = errors.New("invalid frontmatter")
	ErrDigestMismatch     = errors.New("digest mismatch")
	ErrMissingDigest      = errors.New("missing required digest")
	ErrInsecureURL        = errors.New("insecure or unsupported URL scheme (https required)")
	ErrInvalidName        = errors.New("invalid skill or catalog name")
	ErrAmbiguousCatalog   = errors.New("ambiguous catalog name")
)

// HTTPStatusError represents an unexpected HTTP status returned by a remote endpoint.
type HTTPStatusError struct {
	StatusCode int
	URL        string
	Detail     string
}

func (e *HTTPStatusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("HTTP %d from %s: %s", e.StatusCode, e.URL, e.Detail)
	}
	return fmt.Sprintf("HTTP %d from %s", e.StatusCode, e.URL)
}

// ItemType defines the supported catalog item types.
type ItemType string

const (
	// ItemTypeSkill represents an Agent Skill (SKILL.md package).
	ItemTypeSkill ItemType = "skill"
	// ItemTypeSkillMD represents an Agent Skill in the Cloudflare RFC v0.2.0 discovery schema.
	ItemTypeSkillMD ItemType = "skill-md"
	// ItemTypeArchive represents a packaged Agent Skill archive (.tar.gz or .zip).
	ItemTypeArchive ItemType = "archive"
)

// Item represents a discoverable Agent Skill in the catalog.
type Item struct {
	ID           string            `json:"id" yaml:"id"`
	Name         string            `json:"name" yaml:"name"`
	Type         ItemType          `json:"type,omitempty" yaml:"type,omitempty"`
	Title        string            `json:"title,omitempty" yaml:"title,omitempty"`
	Description  string            `json:"description" yaml:"description"`
	Tags         []string          `json:"tags,omitempty" yaml:"tags,omitempty"`
	Author       string            `json:"author,omitempty" yaml:"author,omitempty"`
	Version      string            `json:"version,omitempty" yaml:"version,omitempty"`
	License      string            `json:"license,omitempty" yaml:"license,omitempty"`
	URL          string            `json:"url,omitempty" yaml:"url,omitempty"`
	RelativePath string            `json:"relativePath,omitempty" yaml:"relativePath,omitempty"`
	Path         string            `json:"path,omitempty" yaml:"path,omitempty"`
	Digest       string            `json:"digest,omitempty" yaml:"digest,omitempty"`
	Source       string            `json:"source,omitempty" yaml:"source,omitempty"`
	Status       string            `json:"status,omitempty" yaml:"status,omitempty"`
	AllowedTools string            `json:"allowed_tools,omitempty" yaml:"allowed-tools,omitempty"`
	Body         string            `json:"body,omitempty" yaml:"body,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Clone returns a deep copy of Item so callers cannot race on Metadata or Tags.
func (it Item) Clone() Item {
	out := it
	if it.Tags != nil {
		out.Tags = slices.Clone(it.Tags)
	}
	if it.Metadata != nil {
		out.Metadata = maps.Clone(it.Metadata)
	}
	return out
}

// EffectiveDigest returns the item's normalized digest if present.
func (it Item) EffectiveDigest() string {
	return it.Digest
}

// ValidateSkillName checks that name is a safe single path component with no traversal or control chars.
func ValidateSkillName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != name {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if trimmed == "." || trimmed == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	for _, r := range trimmed {
		if r == '/' || r == '\\' || r == ':' || r == '@' || r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("%w: %q contains forbidden character", ErrInvalidName, name)
		}
	}
	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("%w: %q contains traversal sequence", ErrInvalidName, name)
	}
	return nil
}

// Normalize ensures uniform field population across catalog metadata conventions.
func (it *Item) Normalize() {
	if it.ID == "" && it.Name != "" {
		it.ID = it.Name
	}
	if it.Name == "" && it.ID != "" {
		it.Name = it.ID
	}
	if it.Type == "" {
		it.Type = ItemTypeSkill
	}
	if it.Metadata == nil {
		it.Metadata = make(map[string]string)
	}
	if it.URL != "" && (it.Metadata["canonical"] == "" || (!strings.HasPrefix(it.Metadata["canonical"], "http://") && !strings.HasPrefix(it.Metadata["canonical"], "https://"))) {
		if strings.HasPrefix(it.URL, "http://") || strings.HasPrefix(it.URL, "https://") {
			it.Metadata["canonical"] = it.URL
		}
	} else if it.URL == "" && it.Metadata["canonical"] != "" {
		it.URL = it.Metadata["canonical"]
	}
	if it.Author == "" {
		it.Author = it.Metadata["author"]
	}
	if it.Version == "" {
		it.Version = it.Metadata["version"]
	}
	if len(it.Tags) == 0 && it.Metadata["tags"] != "" {
		it.Tags = ParseTags(it.Metadata["tags"])
	}

	if it.URL != "" && it.RelativePath == "" {
		if idx := strings.LastIndex(it.URL, "/skills/"); idx != -1 {
			rel := it.URL[idx+len("/skills/"):]
			parts := strings.Split(rel, "/")
			if len(parts) >= 3 {
				it.RelativePath = "skills/" + strings.Join(parts[:len(parts)-1], "/")
			} else if len(parts) == 2 {
				it.RelativePath = "skills/" + parts[0]
			}
		}
	}
}

// UnmarshalJSON normalizes item fields upon deserialization, mapping wire aliases
// such as google/skills index.json "entrypoint" onto URL.
func (it *Item) UnmarshalJSON(data []byte) error {
	type Alias Item
	var aux struct {
		Alias
		Entrypoint string `json:"entrypoint,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*it = Item(aux.Alias)
	if it.URL == "" && aux.Entrypoint != "" {
		it.URL = aux.Entrypoint
	}
	it.Normalize()
	return nil
}

// Frontmatter strictly reflects the official Agent Skills specification (agentskills.io).
type Frontmatter struct {
	Name          string            `json:"name" yaml:"name"`
	Description   string            `json:"description" yaml:"description"`
	License       string            `json:"license,omitempty" yaml:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty" yaml:"compatibility,omitempty"`
	AllowedTools  string            `json:"allowed-tools,omitempty" yaml:"allowed-tools,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Manifest represents a remote or local discovery index (.well-known/agent-skills/index.json or index.json).
type Manifest struct {
	Schema      string `json:"$schema,omitempty" yaml:"$schema,omitempty"`
	Version     string `json:"version,omitempty" yaml:"version,omitempty"`
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Generator   string `json:"generator,omitempty" yaml:"generator,omitempty"`
	URL         string `json:"url,omitempty" yaml:"url,omitempty"`
	Repository  string `json:"repository,omitempty" yaml:"repository,omitempty"`
	TotalSkills int    `json:"totalSkills,omitempty" yaml:"totalSkills,omitempty"`
	UpdatedAt   any    `json:"updatedAt,omitempty" yaml:"updatedAt,omitempty"`
	Items       []Item `json:"skills,omitempty" yaml:"skills,omitempty"`
}

// AgentSkillsDiscoverySchemaV020 is the Cloudflare RFC v0.2.0 schema URI.
const AgentSkillsDiscoverySchemaV020 = "https://schemas.agentskills.io/discovery/0.2.0/schema.json"

// UnmarshalJSON parses a discovery manifest ("skills" per RFC v0.2.0, or "items" for google/skills index.json)
// and filters out entries with unrecognized type values per RFC v0.2.0.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	type manifestWire struct {
		Schema      string `json:"$schema,omitempty"`
		Version     string `json:"version,omitempty"`
		Name        string `json:"name,omitempty"`
		Title       string `json:"title,omitempty"`
		Generator   string `json:"generator,omitempty"`
		URL         string `json:"url,omitempty"`
		Repository  string `json:"repository,omitempty"`
		TotalSkills int    `json:"totalSkills,omitempty"`
		UpdatedAt   any    `json:"updatedAt,omitempty"`
		Skills      []Item `json:"skills,omitempty"`
		Items       []Item `json:"items,omitempty"`
	}
	var aux manifestWire
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.Skills) > 0 && len(aux.Items) > 0 {
		return errors.New("manifest cannot specify both \"skills\" and \"items\"")
	}
	rawItems := aux.Skills
	if len(rawItems) == 0 {
		rawItems = aux.Items
	}

	isRecognizedType := func(t ItemType) bool {
		switch t {
		case "", ItemTypeSkill, ItemTypeSkillMD, ItemTypeArchive:
			return true
		default:
			return false
		}
	}

	valid := make([]Item, 0, len(rawItems))
	for _, it := range rawItems {
		if isRecognizedType(it.Type) {
			it.Normalize()
			valid = append(valid, it)
		}
	}

	*m = Manifest{
		Schema:      aux.Schema,
		Version:     aux.Version,
		Name:        aux.Name,
		Title:       aux.Title,
		Generator:   aux.Generator,
		URL:         aux.URL,
		Repository:  aux.Repository,
		TotalSkills: aux.TotalSkills,
		UpdatedAt:   aux.UpdatedAt,
		Items:       valid,
	}
	return nil
}

// ResolveManifestURLs resolves relative skill URLs in a manifest against the manifest endpoint URL
// and rejects unsafe schemes (anything other than https, or loopback http when allowLoopbackHTTP is true).
func ResolveManifestURLs(m *Manifest, endpoint string, allowLoopbackHTTP bool) error {
	if m == nil || endpoint == "" {
		return nil
	}
	baseURL, err := ValidateRemoteURL(endpoint, allowLoopbackHTTP)
	if err != nil {
		return err
	}
	for i := range m.Items {
		if m.Items[i].URL != "" {
			refURL, err := url.Parse(m.Items[i].URL)
			if err != nil {
				return fmt.Errorf("invalid skill URL %q: %w", m.Items[i].URL, err)
			}
			resolved := baseURL.ResolveReference(refURL).String()
			if _, err := ValidateRemoteURL(resolved, allowLoopbackHTTP); err != nil {
				return fmt.Errorf("skill %q URL %q rejected: %w", m.Items[i].Name, resolved, err)
			}
			m.Items[i].URL = resolved
		}
		m.Items[i].Normalize()
		if m.Items[i].Source == "" {
			m.Items[i].Source = endpoint
		}
	}
	return nil
}

type parsedSemver struct {
	core       [3]uint64
	prerelease []string
}

func parseSemver(v string) parsedSemver {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if idx := strings.IndexByte(v, '+'); idx != -1 {
		v = v[:idx]
	}

	var ps parsedSemver
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		pre := v[idx+1:]
		v = v[:idx]
		if pre != "" {
			ps.prerelease = strings.Split(pre, ".")
		}
	}

	parts := strings.Split(v, ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		if n, err := strconv.ParseUint(parts[i], 10, 64); err == nil {
			ps.core[i] = n
		}
	}
	return ps
}

// CompareSemver compares two SemVer 2.0 version strings (v1 vs v2), including prerelease precedence.
// Returns > 0 if v1 > v2, < 0 if v1 < v2, and 0 if equal.
func CompareSemver(v1, v2 string) int {
	p1 := parseSemver(v1)
	p2 := parseSemver(v2)

	for i := range 3 {
		if p1.core[i] > p2.core[i] {
			return 1
		}
		if p1.core[i] < p2.core[i] {
			return -1
		}
	}

	// Prerelease versions have lower precedence than normal versions (1.0.0-alpha < 1.0.0).
	if len(p1.prerelease) == 0 && len(p2.prerelease) == 0 {
		return 0
	}
	if len(p1.prerelease) == 0 {
		return 1
	}
	if len(p2.prerelease) == 0 {
		return -1
	}

	for i := 0; i < len(p1.prerelease) && i < len(p2.prerelease); i++ {
		a, b := p1.prerelease[i], p2.prerelease[i]
		if a == b {
			continue
		}
		na, errA := strconv.ParseUint(a, 10, 64)
		nb, errB := strconv.ParseUint(b, 10, 64)
		switch {
		case errA == nil && errB == nil:
			if na > nb {
				return 1
			}
			return -1
		case errA == nil:
			return -1 // numeric identifiers have lower precedence than non-numeric
		case errB == nil:
			return 1
		default:
			if a > b {
				return 1
			}
			return -1
		}
	}

	if len(p1.prerelease) > len(p2.prerelease) {
		return 1
	}
	if len(p1.prerelease) < len(p2.prerelease) {
		return -1
	}
	return 0
}
