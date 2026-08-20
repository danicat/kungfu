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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultRemoteURL = DefaultRemoteURL

// Option configures the Catalog instance.
type Option func(*Catalog)

// WithRemoteURL sets a custom remote catalog URL.
func WithRemoteURL(url string) Option {
	return func(c *Catalog) {
		c.remoteURL = url
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Catalog) {
		c.httpClient = client
	}
}

// WithCacheDir sets the cache directory for manifest and remote metadata.
func WithCacheDir(dir string) Option {
	return func(c *Catalog) {
		c.cacheDir = dir
	}
}

// WithFallbackCacheDir sets a secondary directory to read cached catalogs from if cacheDir has nothing.
func WithFallbackCacheDir(dir string) Option {
	return func(c *Catalog) {
		c.fallbackCacheDir = dir
	}
}

// WithWorkspaceDir sets the workspace directory to scan.
func WithWorkspaceDir(dir string) Option {
	return func(c *Catalog) {
		c.workspaceDir = dir
	}
}

// WithHomeDir sets the global user home directory.
func WithHomeDir(dir string) Option {
	return func(c *Catalog) {
		c.homeDir = dir
	}
}

// WithRemoteSync enables or disables remote catalog syncing.
func WithRemoteSync(enable bool) Option {
	return func(c *Catalog) {
		c.enableRemote = enable
	}
}

// WithRemoteDiscovery enables or disables remote catalog syncing (alias for WithRemoteSync).
func WithRemoteDiscovery(enable bool) Option {
	return WithRemoteSync(enable)
}

// WithLocalDiscovery is kept for backward compatibility (no-op).
func WithLocalDiscovery(enable bool) Option {
	return func(c *Catalog) {
	}
}

// Catalog implements the Store interface with deterministic in-memory lookup.
type Catalog struct {
	mu               sync.RWMutex
	items            map[string]Item
	manifest         *Manifest
	cache            *Cache
	remoteURL        string
	httpClient       *http.Client
	cacheDir         string
	fallbackCacheDir string
	workspaceDir     string
	homeDir          string
	enableRemote     bool
}

// NewCatalog constructs a Catalog instance with option modifiers.
func NewCatalog(opts ...Option) (*Catalog, error) {
	c := &Catalog{
		items:        make(map[string]Item),
		remoteURL:    defaultRemoteURL,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		enableRemote: true,
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.cacheDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			c.cacheDir = filepath.Join(home, ".cache", "kungfu")
		}
	}

	c.cache = NewCache(c.remoteURL, c.cacheDir, c.fallbackCacheDir, c.httpClient)

	// 1. Load from disk cache if available
	if manifest, _, err := c.cache.LoadDisk(); err == nil && manifest != nil && len(manifest.Items) > 0 {
		c.manifest = manifest
		for _, it := range manifest.Items {
			c.items[strings.ToLower(it.ID)] = it
			c.items[strings.ToLower(it.Name)] = it
		}
	} else if c.enableRemote {
		// 2. Fetch directly from remote registry on startup if cache is empty
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		manifest, _, err := c.cache.Fetch(ctx, false)
		cancel()
		if err == nil && manifest != nil {
			c.manifest = manifest
			for _, it := range manifest.Items {
				c.items[strings.ToLower(it.ID)] = it
				c.items[strings.ToLower(it.Name)] = it
			}
		}
	}

	// 3. Register built-in embedded kungfu item
	c.mu.Lock()
	c.discoverEmbeddedLocked()
	c.mu.Unlock()
	return c, nil
}

// Sync refreshes the catalog from the remote registry.
func (c *Catalog) Sync(force bool) error {
	var remoteErr error
	if c.enableRemote && c.cache != nil {
		manifest, _, err := c.cache.Fetch(context.Background(), force)
		if err != nil {
			remoteErr = err
		} else if manifest != nil {
			c.mu.Lock()
			c.manifest = manifest
			c.items = make(map[string]Item)
			for _, it := range manifest.Items {
				c.items[strings.ToLower(it.ID)] = it
				c.items[strings.ToLower(it.Name)] = it
			}
			c.discoverEmbeddedLocked()
			c.mu.Unlock()
			return nil
		}
	}
	c.mu.Lock()
	c.discoverEmbeddedLocked()
	c.mu.Unlock()
	return remoteErr
}

// Get retrieves an item by ID or Name (case-insensitive).
func (c *Catalog) Get(idOrName string) (*Item, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := strings.ToLower(strings.TrimSpace(idOrName))
	if item, exists := c.items[key]; exists {
		itCopy := item
		return &itCopy, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrItemNotFound, idOrName)
}

// List returns items filtered by category.
func (c *Catalog) List(category string) ([]Item, error) {
	return c.ListByFilter(QueryFilter{Category: category}), nil
}

// ListByFilter filters unique catalog items by category and/or boolean tag query.
func (c *Catalog) ListByFilter(filter QueryFilter) []Item {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cat := strings.ToLower(strings.TrimSpace(filter.Category))
	tagQuery := strings.TrimSpace(filter.TagQuery)
	seen := make(map[string]struct{})
	var result []Item

	for _, item := range c.items {
		if _, already := seen[item.ID]; already {
			continue
		}
		seen[item.ID] = struct{}{}

		// Check Category filter
		if cat != "" && cat != "all" {
			itemCat := strings.ToLower(item.Category)
			if itemCat != cat {
				// Check against manifest category Name / ID aliases
				matched := false
				if c.manifest != nil {
					for _, mc := range c.manifest.Categories {
						mcID := strings.ToLower(mc.ID)
						mcName := strings.ToLower(mc.Name)
						if (cat == mcID || cat == mcName) && (itemCat == mcID || itemCat == mcName) {
							matched = true
							break
						}
					}
				}
				if !matched {
					continue
				}
			}
		}

		// Check Tag Query filter
		if tagQuery != "" && !MatchTags(item.Tags, tagQuery) {
			continue
		}

		result = append(result, item)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

// AddItem registers an Item in the catalog (useful for testing and extensions).
func (c *Catalog) AddItem(item Item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item.Normalize()
	if item.ID != "" {
		c.items[strings.ToLower(item.ID)] = item
	}
	if item.Name != "" {
		c.items[strings.ToLower(item.Name)] = item
	}
}

// All returns all unique items in the catalog.
func (c *Catalog) All() []Item {
	return c.ListByFilter(QueryFilter{})
}

// Categories returns all official categories with item counts.
func (c *Catalog) Categories() []Category {
	c.mu.RLock()
	defer c.mu.RUnlock()

	catCount := make(map[string]int)
	seen := make(map[string]struct{})
	for _, item := range c.items {
		if _, already := seen[item.ID]; already {
			continue
		}
		seen[item.ID] = struct{}{}
		if item.Category != "" {
			catCount[strings.ToLower(item.Category)]++
		}
	}

	var result []Category
	seenCats := make(map[string]struct{})

	// 1. Include categories from manifest if available
	if c.manifest != nil {
		for _, mc := range c.manifest.Categories {
			idLower := strings.ToLower(mc.ID)
			if idLower == "" {
				idLower = strings.ToLower(mc.Name)
			}
			seenCats[idLower] = struct{}{}
			result = append(result, Category{
				ID:          mc.ID,
				Name:        mc.Name,
				Emoji:       mc.Emoji,
				Description: mc.Description,
				Count:       catCount[idLower],
			})
		}
	}

	// 2. Include any remaining categories from items (e.g. gateway for kungfu)
	for catKey, count := range catCount {
		if _, exists := seenCats[catKey]; !exists {
			result = append(result, Category{
				ID:          catKey,
				Name:        catKey,
				Description: fmt.Sprintf("%s skills", catKey),
				Count:       count,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

func (c *Catalog) discoverEmbeddedLocked() {
	if item, err := GetEmbeddedKungFuItem(); err == nil {
		c.items[strings.ToLower(item.ID)] = item
		c.items[strings.ToLower(item.Name)] = item
	}
}

// LoadContent retrieves the full raw markdown content for an item.
// It checks local paths, embedded skills, cached copies, or fetches from remote URLs.
// If an item is remote and uncached while offline, it returns an explicit network error instead of a fake stub.
func (c *Catalog) LoadContent(item *Item) (string, error) {
	if item == nil {
		return "", errors.New("cannot load content for nil item")
	}

	// 1. Local path
	if item.Path != "" {
		path := item.Path
		fi, err := os.Stat(path)
		if err == nil && fi.IsDir() {
			skillPath := filepath.Join(path, "SKILL.md")
			if data, err := os.ReadFile(skillPath); err == nil {
				return string(data), nil
			}
		}
		if data, err := os.ReadFile(path); err == nil {
			return string(data), nil
		}
	}

	// 2. Embedded self-skill (kungfu)
	if item.Source == "embedded" && item.ID == "kungfu" {
		return string(EmbeddedKungFuSkill), nil
	}

	// 3. Direct frontmatter body already in memory
	if strings.HasPrefix(item.Body, "---") {
		return item.Body, nil
	}

	// 4. Remote URL or cached copy
	if item.URL != "" {
		cleanName := filepath.Base(filepath.Clean(item.Name))
		if cleanName == "." || cleanName == ".." || strings.Contains(item.Name, "..") {
			cleanName = "skill"
		}
		var cacheFile string
		if c.cacheDir != "" {
			cacheFile = filepath.Join(c.cacheDir, "skills", cleanName, "SKILL.md")
			if data, err := os.ReadFile(cacheFile); err == nil && len(data) > 0 {
				return string(data), nil
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create request for %s: %w", item.URL, err)
		}
		req.Header.Set("User-Agent", "kungfu/0.1.0")
		client := c.httpClient
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to download skill %q from %s: network unreachable: %w", item.Name, item.URL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to download skill %q from %s: HTTP %d", item.Name, item.URL, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
		if err != nil {
			return "", fmt.Errorf("failed to read response for %q: %w", item.Name, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("empty content downloaded for %q", item.Name)
		}
		if cacheFile != "" {
			_ = os.MkdirAll(filepath.Dir(cacheFile), 0755)
			_ = os.WriteFile(cacheFile, data, 0644)
		}
		return string(data), nil
	}

	// 5. If item has raw body, return it
	if item.Body != "" {
		return item.Body, nil
	}

	return "", fmt.Errorf("content unavailable for skill %q (no local path, cache, or remote URL)", item.Name)
}
