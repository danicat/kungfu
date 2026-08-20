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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultRemoteURL is the default endpoint for the skills catalog.
	DefaultRemoteURL = "https://skills.danicat.dev/catalog.json"
	// CatalogFileName is the cache file name for the catalog manifest.
	CatalogFileName = "catalog.json"
	// MetaFileName is the cache file name for caching metadata.
	MetaFileName = "meta.json"
)

// Cache manages remote catalog fetching, HTTP conditional caching, and disk persistence.
type Cache struct {
	httpClient  *http.Client
	remoteURL   string
	primaryDir  string
	fallbackDir string
}

// NewCache constructs a Cache with the specified settings.
func NewCache(remoteURL, primaryDir, fallbackDir string, client *http.Client) *Cache {
	if remoteURL == "" {
		remoteURL = DefaultRemoteURL
	}
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	if primaryDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			primaryDir = filepath.Join(home, ".cache", "kungfu")
		}
	}
	if fallbackDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			fallbackDir = filepath.Join(home, ".kungfu")
		}
	}
	return &Cache{
		httpClient:  client,
		remoteURL:   remoteURL,
		primaryDir:  primaryDir,
		fallbackDir: fallbackDir,
	}
}

// Fetch retrieves the catalog manifest, utilizing conditional HTTP headers and disk cache.
func (c *Cache) Fetch(ctx context.Context, force bool) (*Manifest, *CacheMeta, error) {
	cachedManifest, cachedMeta, _ := c.LoadDisk()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.remoteURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	if !force && cachedMeta != nil {
		if cachedMeta.ETag != "" {
			req.Header.Set("If-None-Match", cachedMeta.ETag)
		}
		if cachedMeta.LastModified != "" {
			req.Header.Set("If-Modified-Since", cachedMeta.LastModified)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Offline fallback: if disk cache is available, return it.
		if cachedManifest != nil {
			return cachedManifest, cachedMeta, nil
		}
		return nil, nil, fmt.Errorf("fetching remote catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if cachedManifest != nil {
			if cachedMeta == nil {
				cachedMeta = &CacheMeta{}
			}
			cachedMeta.FetchedAt = time.Now().UTC()
			_ = c.saveMetaOnly(cachedMeta)
			return cachedManifest, cachedMeta, nil
		}
		if !force {
			return c.Fetch(ctx, true)
		}
	}

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < 300 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB limit
		if err != nil {
			if cachedManifest != nil {
				return cachedManifest, cachedMeta, nil
			}
			return nil, nil, fmt.Errorf("reading response body: %w", err)
		}

		var manifest Manifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			if cachedManifest != nil {
				return cachedManifest, cachedMeta, nil
			}
			return nil, nil, fmt.Errorf("parsing catalog manifest JSON: %w", err)
		}

		meta := &CacheMeta{
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			FetchedAt:    time.Now().UTC(),
			URL:          c.remoteURL,
		}

		_ = c.SaveDisk(&manifest, meta)
		return &manifest, meta, nil
	}

	// If server returned 5xx or other status, attempt offline fallback.
	if cachedManifest != nil {
		return cachedManifest, cachedMeta, nil
	}

	return nil, nil, fmt.Errorf("remote catalog returned HTTP status %d: %s", resp.StatusCode, resp.Status)
}

// LoadDisk attempts to load the cached catalog and metadata from primary or fallback directories.
func (c *Cache) LoadDisk() (*Manifest, *CacheMeta, error) {
	dirs := []string{c.primaryDir, c.fallbackDir}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		catPath := filepath.Join(dir, CatalogFileName)
		data, err := os.ReadFile(catPath)
		if err != nil {
			continue
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			continue
		}

		var meta CacheMeta
		metaPath := filepath.Join(dir, MetaFileName)
		if metaData, err := os.ReadFile(metaPath); err == nil {
			_ = json.Unmarshal(metaData, &meta)
		}
		return &manifest, &meta, nil
	}
	return nil, nil, ErrCacheNotFound
}

// SaveDisk saves the manifest and metadata to disk, trying primaryDir first and falling back to fallbackDir.
func (c *Cache) SaveDisk(manifest *Manifest, meta *CacheMeta) error {
	if manifest == nil {
		return ErrEmptyManifest
	}
	catData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling catalog manifest: %w", err)
	}

	var metaData []byte
	if meta != nil {
		metaData, _ = json.MarshalIndent(meta, "", "  ")
	}

	targetDirs := []string{c.primaryDir, c.fallbackDir}
	var lastErr error
	for _, dir := range targetDirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			lastErr = err
			continue
		}
		catFile := filepath.Join(dir, CatalogFileName)
		if err := writeAtomic(catFile, catData, 0644); err != nil {
			lastErr = err
			continue
		}
		if len(metaData) > 0 {
			metaFile := filepath.Join(dir, MetaFileName)
			_ = writeAtomic(metaFile, metaData, 0644)
		}
		return nil
	}
	if lastErr != nil {
		return fmt.Errorf("saving catalog cache: %w", lastErr)
	}
	return fmt.Errorf("no valid cache directory available")
}

func (c *Cache) saveMetaOnly(meta *CacheMeta) error {
	if meta == nil {
		return nil
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	dirs := []string{c.primaryDir, c.fallbackDir}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		metaFile := filepath.Join(dir, MetaFileName)
		if _, err := os.Stat(filepath.Join(dir, CatalogFileName)); err == nil {
			return writeAtomic(metaFile, metaData, 0644)
		}
	}
	return nil
}

// writeAtomic writes data to a temporary file before renaming to target path.
func writeAtomic(targetPath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(targetPath)
	tmpFile, err := os.CreateTemp(dir, ".catalog-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("writing to temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("renaming temp file to target: %w", err)
	}
	return nil
}
