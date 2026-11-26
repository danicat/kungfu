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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultCatalogTTL is the default cache duration for catalog manifests (24 hours).
const DefaultCatalogTTL = 24 * time.Hour

const maxCachedCatalogBytes = 10 << 20 // 10 MiB

// CachedCatalog represents a cached catalog discovery envelope.
type CachedCatalog struct {
	Name      string    `json:"name,omitempty"`
	URL       string    `json:"url,omitempty"`
	ETag      string    `json:"etag,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
	Manifest  Manifest  `json:"manifest"`
}

// ResolveCacheDir resolves the root cache directory.
// Precedence: override -> KUNGFU_CACHE_DIR -> XDG_CACHE_HOME/kungfu -> OS default (~/.cache/kungfu on Unix, %LOCALAPPDATA%/kungfu on Windows).
func ResolveCacheDir(override string) (string, error) {
	if dir := strings.TrimSpace(override); dir != "" {
		return dir, nil
	}
	if envDir := strings.TrimSpace(os.Getenv("KUNGFU_CACHE_DIR")); envDir != "" {
		return envDir, nil
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); xdg != "" {
		return filepath.Join(xdg, "kungfu"), nil
	}
	if runtime.GOOS == "windows" {
		dir, err := os.UserCacheDir()
		if err != nil || dir == "" {
			return "", fmt.Errorf("catalog: unable to resolve user cache directory: %w", err)
		}
		return filepath.Join(dir, "kungfu"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("catalog: unable to resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "kungfu"), nil
}

// GetCatalogsCacheDir returns <cacheDir>/catalogs (resolving default cache dir when cacheDir is empty).
func GetCatalogsCacheDir(cacheDir string) (string, error) {
	root, err := ResolveCacheDir(cacheDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "catalogs"), nil
}

// SafeCatalogFileName converts a catalog name into a collision-free, filesystem-safe JSON filename.
func SafeCatalogFileName(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		trimmed = "default"
	}

	var b strings.Builder
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "catalog"
	}
	if len(slug) > 48 {
		slug = slug[:48]
	}

	sum := sha256.Sum256([]byte(trimmed))
	return fmt.Sprintf("%s-%s.json", slug, hex.EncodeToString(sum[:4]))
}

// SkillCacheKey returns a collision-free, filesystem-safe cache directory name for a skill.
// When source is non-empty, appends an 8-hex hash of source so identically named skills from
// different catalogs do not overwrite each other.
func SkillCacheKey(skillName, source string) (string, error) {
	if err := ValidateSkillName(skillName); err != nil {
		return "", err
	}
	cleanSrc := strings.ToLower(strings.TrimSpace(source))
	if cleanSrc == "" || cleanSrc == "default" || cleanSrc == "local" {
		return skillName, nil
	}
	sum := sha256.Sum256([]byte(cleanSrc))
	return fmt.Sprintf("%s-%s", skillName, hex.EncodeToString(sum[:4])), nil
}

// SkillCacheDir returns the contained directory path <cacheRoot>/skills/<key>.
func SkillCacheDir(cacheRoot, skillName, source string) (string, error) {
	if cacheRoot == "" {
		return "", fmt.Errorf("cacheRoot is empty")
	}
	key, err := SkillCacheKey(skillName, source)
	if err != nil {
		return "", err
	}
	skillsRoot := filepath.Join(cacheRoot, "skills")
	dir := filepath.Join(skillsRoot, key)
	if !IsPathContained(skillsRoot, dir) {
		return "", fmt.Errorf("%w: %q escapes cache root", ErrInvalidName, skillName)
	}
	return dir, nil
}

// FindCachedSkillDir locates a cached skill directory for offline/local reads.
// Checks the exact catalog-scoped dir first, then the unscoped dir, then any <skillName>-* dir.
func FindCachedSkillDir(cacheRoot, skillName, source string) (string, error) {
	if err := ValidateSkillName(skillName); err != nil {
		return "", err
	}
	if cacheRoot == "" {
		return "", os.ErrNotExist
	}
	skillsRoot := filepath.Join(cacheRoot, "skills")

	var candidates []string
	if source != "" {
		if dir, err := SkillCacheDir(cacheRoot, skillName, source); err == nil {
			candidates = append(candidates, dir)
		}
	}
	if dir, err := SkillCacheDir(cacheRoot, skillName, ""); err == nil {
		candidates = append(candidates, dir)
	}

	for _, c := range candidates {
		if fi, err := os.Stat(filepath.Join(c, "SKILL.md")); err == nil && !fi.IsDir() {
			return c, nil
		}
	}

	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return "", err
	}
	prefix := skillName + "-"
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		dir := filepath.Join(skillsRoot, e.Name())
		if !IsPathContained(skillsRoot, dir) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !fi.IsDir() {
			return dir, nil
		}
	}
	return "", os.ErrNotExist
}

// LoadCachedCatalog reads and parses a cached catalog JSON file with bounded reads.
func LoadCachedCatalog(cachePath string) (*CachedCatalog, error) {
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, err
	}
	data, readErr := ReadBounded(f, maxCachedCatalogBytes)
	_ = f.Close()
	if readErr != nil {
		return nil, fmt.Errorf("catalog: reading cache %s: %w", cachePath, readErr)
	}

	var cached CachedCatalog
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("catalog: parsing cache %s: %w", cachePath, err)
	}

	return &cached, nil
}

// SaveCachedCatalog atomically writes the catalog manifest and ETag to cachePath with 0700/0600 modes and fsync.
func SaveCachedCatalog(cachePath, name, remoteURL string, manifest Manifest, etag string) error {
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("catalog: creating cache directory %s: %w", dir, err)
	}

	cached := CachedCatalog{
		Name:      name,
		URL:       remoteURL,
		ETag:      etag,
		FetchedAt: time.Now().UTC(),
		Manifest:  manifest,
	}

	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return fmt.Errorf("catalog: encoding cached catalog: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return fmt.Errorf("catalog: creating temp cache file in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmpFile.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("catalog: writing temp cache %s: %w", tmpPath, err)
	}
	if err := tmpFile.Chmod(0600); err != nil {
		cleanup()
		return fmt.Errorf("catalog: setting cache permissions %s: %w", tmpPath, err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("catalog: syncing temp cache %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("catalog: closing temp cache %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, cachePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("catalog: renaming cache file %s -> %s: %w", tmpPath, cachePath, err)
	}

	return nil
}

// SaveNamedCatalogCache saves a catalog manifest to <cacheDir>/catalogs/<safe-name>.json.
func SaveNamedCatalogCache(cacheDir, name, sourceURL string, manifest Manifest, etag string) error {
	dir, err := GetCatalogsCacheDir(cacheDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("catalog: creating catalogs cache dir: %w", err)
	}
	cachePath := filepath.Join(dir, SafeCatalogFileName(name))
	return SaveCachedCatalog(cachePath, name, sourceURL, manifest, etag)
}

// LoadNamedCatalogCache loads a named catalog manifest from <cacheDir>/catalogs/<safe-name>.json.
func LoadNamedCatalogCache(cacheDir, name string) (*CachedCatalog, error) {
	dir, err := GetCatalogsCacheDir(cacheDir)
	if err != nil {
		return nil, err
	}
	cachePath := filepath.Join(dir, SafeCatalogFileName(name))
	return LoadCachedCatalog(cachePath)
}

// DeleteNamedCatalogCache removes a named catalog cache file in cacheDir.
func DeleteNamedCatalogCache(cacheDir, name string) error {
	dir, err := GetCatalogsCacheDir(cacheDir)
	if err != nil {
		return err
	}
	cachePath := filepath.Join(dir, SafeCatalogFileName(name))
	if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// PruneNamedCatalogCaches removes any .json files in <cacheDir>/catalogs/ that do not
// match the hashed cache filename of a configured catalog in keepNames.
// This removes both legacy unhashed cache files and caches for removed catalogs.
func PruneNamedCatalogCaches(cacheDir string, keepNames []string) error {
	dir, err := GetCatalogsCacheDir(cacheDir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	keep := make(map[string]struct{}, len(keepNames))
	for _, name := range keepNames {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			keep[SafeCatalogFileName(trimmed)] = struct{}{}
		}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if _, ok := keep[entry.Name()]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// LoadAllNamedCatalogCaches loads all cached catalogs from <cacheDir>/catalogs/.
func LoadAllNamedCatalogCaches(cacheDir string) (map[string]*CachedCatalog, error) {
	dir, err := GetCatalogsCacheDir(cacheDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*CachedCatalog), nil
		}
		return nil, err
	}

	results := make(map[string]*CachedCatalog)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		cached, err := LoadCachedCatalog(path)
		if err != nil || cached == nil {
			continue
		}
		catName := strings.TrimSpace(cached.Name)
		if catName == "" {
			catName = strings.TrimSuffix(entry.Name(), ".json")
		}
		results[catName] = cached
	}
	return results, nil
}
