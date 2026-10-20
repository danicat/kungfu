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
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Common errors returned by the catalog package.
var (
	ErrItemNotFound       = errors.New("item not found")
	ErrInvalidFrontmatter = errors.New("invalid frontmatter")
	ErrDigestMismatch     = errors.New("digest mismatch")
	ErrEmptyManifest      = errors.New("empty catalog manifest")
)

// ItemType defines the supported catalog item types.
type ItemType string

const (
	// ItemTypeSkill represents an Agent Skill (SKILL.md package).
	ItemTypeSkill ItemType = "skill"
	// ItemTypeSkillMD represents an Agent Skill in the Cloudflare RFC v0.2.0 discovery schema.
	ItemTypeSkillMD ItemType = "skill-md"
)

// Item represents a discoverable Agent Skill in the catalog.
type Item struct {
	ID                 string            `json:"id" yaml:"id"`
	Name               string            `json:"name" yaml:"name"`
	Type               ItemType          `json:"type,omitempty" yaml:"type,omitempty"`
	Title              string            `json:"title,omitempty" yaml:"title,omitempty"`
	Description        string            `json:"description" yaml:"description"`
	Category           string            `json:"category,omitempty" yaml:"category,omitempty"`
	Tags               []string          `json:"tags,omitempty" yaml:"tags,omitempty"`
	Author             string            `json:"author,omitempty" yaml:"author,omitempty"`
	Version            string            `json:"version,omitempty" yaml:"version,omitempty"`
	License            string            `json:"license,omitempty" yaml:"license,omitempty"`
	URL                string            `json:"url,omitempty" yaml:"url,omitempty"`
	Entrypoint         string            `json:"entrypoint,omitempty" yaml:"entrypoint,omitempty"`
	RelativePath       string            `json:"relativePath,omitempty" yaml:"relativePath,omitempty"`
	Path               string            `json:"path,omitempty" yaml:"path,omitempty"`
	Digest             string            `json:"digest,omitempty" yaml:"digest,omitempty"`
	Sha256             string            `json:"sha256,omitempty" yaml:"sha256,omitempty"`
	Source             string            `json:"source,omitempty" yaml:"source,omitempty"` // "remote", "workspace", "global", "catalog"
	Status             string            `json:"status,omitempty" yaml:"status,omitempty"` // "installed", "online"
	InstallCmd         string            `json:"install_command,omitempty" yaml:"install_command,omitempty"`
	InstallCommand     string            `json:"installCommand,omitempty" yaml:"installCommand,omitempty"`
	NpxInstallCommand  string            `json:"npxInstallCommand,omitempty" yaml:"npxInstallCommand,omitempty"`
	KungfuLoadCommand  string            `json:"kungfuLoadCommand,omitempty" yaml:"kungfuLoadCommand,omitempty"`
	KungfuLearnCommand string            `json:"kungfuLearnCommand,omitempty" yaml:"kungfuLearnCommand,omitempty"`
	FreshnessScore     *int              `json:"freshnessScore,omitempty" yaml:"freshnessScore,omitempty"`
	FreshnessTier      string            `json:"freshnessTier,omitempty" yaml:"freshnessTier,omitempty"`
	FreshnessEmoji     string            `json:"freshnessEmoji,omitempty" yaml:"freshnessEmoji,omitempty"`
	LastCommitAt       string            `json:"lastCommitAt,omitempty" yaml:"lastCommitAt,omitempty"`
	AllowedTools       string            `json:"allowed_tools,omitempty" yaml:"allowed-tools,omitempty"`
	Body               string            `json:"body,omitempty" yaml:"body,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Normalize ensures uniform field population across legacy and new metadata conventions.
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
	if it.Digest == "" && it.Sha256 != "" {
		it.Digest = it.Sha256
	}
	if it.InstallCmd == "" && it.InstallCommand != "" {
		it.InstallCmd = it.InstallCommand
	}
	if it.InstallCommand == "" && it.InstallCmd != "" {
		it.InstallCommand = it.InstallCmd
	}
	if it.URL == "" && it.Entrypoint != "" {
		it.URL = it.Entrypoint
	}
	if (it.Entrypoint == "" || (!strings.HasPrefix(it.Entrypoint, "http://") && !strings.HasPrefix(it.Entrypoint, "https://"))) && (strings.HasPrefix(it.URL, "http://") || strings.HasPrefix(it.URL, "https://")) {
		it.Entrypoint = it.URL
	} else if it.Entrypoint == "" && it.URL != "" {
		it.Entrypoint = it.URL
	}
	if it.Metadata == nil {
		it.Metadata = make(map[string]string)
	}
	if (it.Metadata["canonical"] == "" || (!strings.HasPrefix(it.Metadata["canonical"], "http://") && !strings.HasPrefix(it.Metadata["canonical"], "https://"))) && (strings.HasPrefix(it.Entrypoint, "http://") || strings.HasPrefix(it.Entrypoint, "https://")) {
		it.Metadata["canonical"] = it.Entrypoint
	} else if it.Entrypoint != "" && it.Metadata["canonical"] == "" {
		it.Metadata["canonical"] = it.Entrypoint
	}
	if it.Category == "" {
		it.Category = it.Metadata["category"]
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
	if it.URL == "" && it.Metadata["canonical"] != "" {
		it.URL = it.Metadata["canonical"]
	}

	targetURL := it.Entrypoint
	if targetURL == "" {
		targetURL = it.URL
	}
	if targetURL != "" {
		if idx := strings.LastIndex(targetURL, "/skills/"); idx != -1 {
			rel := targetURL[idx+len("/skills/"):]
			parts := strings.Split(rel, "/")
			if len(parts) >= 3 {
				if it.Category == "" {
					it.Category = parts[0]
				}
				if it.RelativePath == "" {
					it.RelativePath = "skills/" + strings.Join(parts[:len(parts)-1], "/")
				}
			} else if len(parts) == 2 {
				if it.Category == "" {
					it.Category = "agents"
				}
				if it.RelativePath == "" {
					it.RelativePath = "skills/" + parts[0]
				}
			}
		} else if u, err := url.Parse(targetURL); err == nil {
			cleanPath := strings.Trim(u.Path, "/")
			parts := strings.Split(cleanPath, "/")
			if len(parts) >= 2 && strings.EqualFold(parts[len(parts)-1], "SKILL.md") {
				if len(parts) == 3 && it.Category == "" {
					it.Category = parts[0]
				} else if len(parts) >= 2 && it.Category == "" {
					it.Category = parts[len(parts)-2]
				}
			}
		}
	}
	if it.Category == "" {
		it.Category = "agents"
	}
}

// UnmarshalJSON normalizes item fields upon deserialization.
func (it *Item) UnmarshalJSON(data []byte) error {
	type Alias Item
	var aux Alias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*it = Item(aux)
	it.Normalize()
	return nil
}

// Category represents a grouping of catalog items.
type Category struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Emoji       string `json:"emoji,omitempty" yaml:"emoji,omitempty"`
	Description string `json:"description" yaml:"description"`
	Count       int    `json:"count,omitempty" yaml:"count,omitempty"`
}

// Gateway represents catalog gateway metadata and shortcuts.
type Gateway struct {
	Name           string `json:"name,omitempty" yaml:"name,omitempty"`
	Description    string `json:"description,omitempty" yaml:"description,omitempty"`
	URL            string `json:"url,omitempty" yaml:"url,omitempty"`
	InstallCommand string `json:"installCommand,omitempty" yaml:"installCommand,omitempty"`
	KungfuLoad     string `json:"kungfuLoad,omitempty" yaml:"kungfuLoad,omitempty"`
	KungfuLearn    string `json:"kungfuLearn,omitempty" yaml:"kungfuLearn,omitempty"`
}

// Frontmatter strictly reflects the official Agent Skills specification (agentskills.io).
// Top-level fields: name, description, license, compatibility, allowed-tools, metadata.
type Frontmatter struct {
	Name          string            `json:"name" yaml:"name"`
	Description   string            `json:"description" yaml:"description"`
	License       string            `json:"license,omitempty" yaml:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty" yaml:"compatibility,omitempty"`
	AllowedTools  string            `json:"allowed-tools,omitempty" yaml:"allowed-tools,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Manifest represents a remote or local discovery index (catalog.json / index.json).
type Manifest struct {
	Schema      string     `json:"$schema,omitempty" yaml:"$schema,omitempty"`
	Version     string     `json:"version,omitempty" yaml:"version,omitempty"`
	Name        string     `json:"name,omitempty" yaml:"name,omitempty"`
	Title       string     `json:"title,omitempty" yaml:"title,omitempty"`
	Generator   string     `json:"generator,omitempty" yaml:"generator,omitempty"`
	URL         string     `json:"url,omitempty" yaml:"url,omitempty"`
	Repository  string     `json:"repository,omitempty" yaml:"repository,omitempty"`
	TotalSkills int        `json:"totalSkills,omitempty" yaml:"totalSkills,omitempty"`
	UpdatedAt   any        `json:"updatedAt,omitempty" yaml:"updatedAt,omitempty"`
	Categories  []Category `json:"categories,omitempty" yaml:"categories,omitempty"`
	Gateway     *Gateway   `json:"gateway,omitempty" yaml:"gateway,omitempty"`
	Items       []Item     `json:"items,omitempty" yaml:"items,omitempty"`
	Skills      []Item     `json:"skills,omitempty" yaml:"skills,omitempty"`
}

// UnmarshalJSON handles dual support for "items" and "skills" fields in catalog JSON manifests.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	type Alias Manifest
	var aux Alias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*m = Manifest(aux)
	if len(m.Items) == 0 && len(m.Skills) > 0 {
		m.Items = m.Skills
	} else if len(m.Skills) == 0 && len(m.Items) > 0 {
		m.Skills = m.Items
	}
	for i := range m.Items {
		m.Items[i].Normalize()
	}
	for i := range m.Skills {
		m.Skills[i].Normalize()
	}
	return nil
}

// ResolveManifestURLs resolves any relative skill URLs in a manifest against the manifest endpoint URL.
func ResolveManifestURLs(m *Manifest, endpoint string) {
	if m == nil || endpoint == "" {
		return
	}
	baseURL, err := url.Parse(endpoint)
	if err != nil || baseURL == nil {
		return
	}
	for i := range m.Items {
		if m.Items[i].URL != "" && !strings.HasPrefix(m.Items[i].URL, "http://") && !strings.HasPrefix(m.Items[i].URL, "https://") {
			relPath := m.Items[i].URL
			if strings.Contains(endpoint, "/.well-known/agent-skills/") && !strings.HasPrefix(relPath, "/") && !strings.HasPrefix(relPath, "./") {
				relPath = "/" + relPath
			}
			if refURL, err := url.Parse(relPath); err == nil {
				m.Items[i].URL = baseURL.ResolveReference(refURL).String()
			}
		}
		m.Items[i].Normalize()
		if m.Items[i].Source == "" {
			m.Items[i].Source = endpoint
		}
	}
	m.Skills = m.Items
}

// CompareSemver compares two semantic version strings (v1 vs v2).
// Returns > 0 if v1 > v2, < 0 if v1 < v2, and 0 if equal.
func CompareSemver(v1, v2 string) int {
	p1 := parseSemverParts(v1)
	p2 := parseSemverParts(v2)
	for i := 0; i < len(p1) && i < len(p2); i++ {
		if p1[i] > p2[i] {
			return 1
		}
		if p1[i] < p2[i] {
			return -1
		}
	}
	if len(p1) > len(p2) {
		return 1
	}
	if len(p1) < len(p2) {
		return -1
	}
	return 0
}

func parseSemverParts(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	parts := strings.Split(v, ".")
	var res []int
	for _, p := range parts {
		var n int
		for _, r := range p {
			if r >= '0' && r <= '9' {
				n = n*10 + int(r-'0')
			} else {
				break
			}
		}
		res = append(res, n)
	}
	for len(res) < 3 {
		res = append(res, 0)
	}
	return res
}

// CacheMeta stores HTTP caching headers and timestamps for catalog persistence.
type CacheMeta struct {
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	FetchedAt    time.Time `json:"fetched_at"`
	LastChecked  time.Time `json:"last_checked,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Digest       string    `json:"digest,omitempty"`
	HTTPStatus   int       `json:"http_status,omitempty"`
}

// QueryFilter defines criteria for filtering catalog items.
type QueryFilter struct {
	Category string
	TagQuery string
}

// LoadedDoc represents the serialized content and metadata of a loaded skill.
type LoadedDoc struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Category string            `json:"category"`
	Tags     []string          `json:"tags,omitempty"`
	Path     string            `json:"path,omitempty"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Store defines the public catalog query interface.
type Store interface {
	Get(idOrName string) (*Item, error)
	List(category string) ([]Item, error)
	ListByFilter(filter QueryFilter) []Item
	All() []Item
	Categories() []Category
	LoadContent(item *Item) (string, error)
	AddCatalogManifest(name string, manifest *Manifest)
	RemoveCatalogItems(name string)
}
