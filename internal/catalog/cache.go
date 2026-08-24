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
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultCatalogTTL is the default cache duration for the catalog manifest (1 hour).
const DefaultCatalogTTL = 1 * time.Hour

// CachedCatalog represents the cached index.json discovery envelope.
type CachedCatalog struct {
	URL       string    `json:"url,omitempty"`
	ETag      string    `json:"etag,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
	Manifest  Manifest  `json:"manifest"`
}

// DefaultCacheDir returns ~/.cache/kungfu.
func DefaultCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".cache", "kungfu")
}

// GetCatalogCachePath returns ~/.cache/kungfu/index.json.
func GetCatalogCachePath() string {
	return filepath.Join(DefaultCacheDir(), "index.json")
}

// GetSkillCacheDir returns ~/.cache/kungfu/skills/<skillName>.
func GetSkillCacheDir(skillName string) string {
	return filepath.Join(DefaultCacheDir(), "skills", skillName)
}

// LoadCachedCatalog reads and parses the cached catalog from cachePath.
func LoadCachedCatalog(cachePath string) (*CachedCatalog, error) {
	if cachePath == "" {
		cachePath = GetCatalogCachePath()
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}

	var cached CachedCatalog
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("catalog: parsing cache %s: %w", cachePath, err)
	}

	return &cached, nil
}

// SaveCachedCatalog writes the catalog manifest and optional ETag to cachePath.
func SaveCachedCatalog(cachePath string, remoteURL string, manifest Manifest, etag string) error {
	if cachePath == "" {
		cachePath = GetCatalogCachePath()
	}

	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("catalog: creating cache directory %s: %w", dir, err)
	}

	cached := CachedCatalog{
		URL:       remoteURL,
		ETag:      etag,
		FetchedAt: time.Now().UTC(),
		Manifest:  manifest,
	}

	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return fmt.Errorf("catalog: encoding cached catalog: %w", err)
	}

	tmpFile := cachePath + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("catalog: writing temp cache %s: %w", tmpFile, err)
	}

	if err := os.Rename(tmpFile, cachePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("catalog: renaming cache file %s -> %s: %w", tmpFile, cachePath, err)
	}

	return nil
}
