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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danicat/kungfu/internal/version"
)

// DefaultRemoteURL is the default endpoint for the skills catalog manifest.
const DefaultRemoteURL = "https://skills.danicat.dev/catalog.json"

const defaultRemoteURL = DefaultRemoteURL

// ResolveCatalogURL resolves a catalog base origin or endpoint URL.
func ResolveCatalogURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultRemoteURL
	}
	if strings.HasSuffix(raw, ".json") {
		return raw
	}
	raw = strings.TrimSuffix(raw, "/")
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	return raw + "/catalog.json"
}

// Option configures the Catalog instance.
type Option func(*Catalog)

// WithRemoteURL sets a custom remote catalog URL or base origin.
func WithRemoteURL(url string) Option {
	return func(c *Catalog) {
		c.remoteURL = ResolveCatalogURL(url)
	}
}

// WithHTTPClient sets a custom HTTP client for remote fetching.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Catalog) {
		c.httpClient = client
	}
}

// WithRemoteDisabled disables remote fetching.
func WithRemoteDisabled() Option {
	return func(c *Catalog) {
		c.enableRemote = false
	}
}

// WithEmbeddedFS sets an embedded filesystem to discover built-in skills from.
func WithEmbeddedFS(fsys fs.FS) Option {
	return func(c *Catalog) {
		c.embeddedFS = fsys
	}
}

// WithManifest injects a pre-parsed manifest (useful for testing).
func WithManifest(m *Manifest) Option {
	return func(c *Catalog) {
		c.manifest = m
		if m != nil {
			for i := range m.Items {
				m.Items[i].Normalize()
				c.items[strings.ToLower(m.Items[i].ID)] = m.Items[i]
				c.items[strings.ToLower(m.Items[i].Name)] = m.Items[i]
			}
		}
	}
}

// WithRemoteSync enables or disables remote catalog fetching.
func WithRemoteSync(enable bool) Option {
	return func(c *Catalog) {
		c.enableRemote = enable
	}
}

// WithRemoteDiscovery enables or disables remote catalog fetching (alias for WithRemoteSync).
func WithRemoteDiscovery(enable bool) Option {
	return WithRemoteSync(enable)
}

// WithLocalDiscovery enables or disables local filesystem skill discovery.
func WithLocalDiscovery(enable bool) Option {
	return func(c *Catalog) {
		c.enableLocal = enable
	}
}

// WithCacheDir configures the catalog cache directory.
func WithCacheDir(dir string) Option {
	return func(c *Catalog) {
		c.cacheDir = dir
	}
}

// WithNoCache disables local catalog caching.
func WithNoCache() Option {
	return func(c *Catalog) {
		c.noCache = true
	}
}

// WithForceRefresh forces fetching fresh catalog bypassing cache TTL.
func WithForceRefresh() Option {
	return func(c *Catalog) {
		c.forceRefresh = true
	}
}

// WithFallbackCacheDir is kept for backward compatibility (no-op).
func WithFallbackCacheDir(dir string) Option {
	return func(c *Catalog) {
	}
}

// WithWorkspaceDir is kept for backward compatibility (no-op).
func WithWorkspaceDir(dir string) Option {
	return func(c *Catalog) {
	}
}

// WithHomeDir is kept for backward compatibility (no-op).
func WithHomeDir(dir string) Option {
	return func(c *Catalog) {
	}
}

// Catalog implements the Store interface with deterministic in-memory lookup.
type Catalog struct {
	mu           sync.RWMutex
	items        map[string]Item
	embeddedData map[string]string
	embeddedFS   fs.FS
	manifest     *Manifest
	remoteURL    string
	httpClient   *http.Client
	enableRemote bool
	enableLocal  bool
	cacheDir     string
	noCache      bool
	forceRefresh bool
}

const (
	defaultMaxRetries     = 3
	defaultInitialBackoff = 100 * time.Millisecond
)

// fetchWithRetry executes an HTTP GET request with up to maxRetries retries using exponential backoff.
func fetchWithRetry(ctx context.Context, client *http.Client, url string, userAgent string, maxRetries int, initialBackoff time.Duration) (*http.Response, error) {
	return fetchWithRetryAndHeaders(ctx, client, url, userAgent, nil, maxRetries, initialBackoff)
}

// fetchWithRetryAndHeaders executes an HTTP GET request with custom request headers and exponential backoff.
func fetchWithRetryAndHeaders(ctx context.Context, client *http.Client, url string, userAgent string, headers map[string]string, maxRetries int, initialBackoff time.Duration) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	if initialBackoff <= 0 {
		initialBackoff = 100 * time.Millisecond
	}

	backoff := initialBackoff
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		// If successful (200 OK or 304 Not Modified), return response immediately
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotModified {
			return resp, nil
		}

		// On retryable HTTP status (429, 5xx), close body and retry
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		// Non-retryable client error (e.g. 404, 403, 400)
		return resp, nil
	}

	return nil, lastErr
}

// NewCatalog constructs a Catalog instance with option modifiers.
func NewCatalog(opts ...Option) (*Catalog, error) {
	remote := defaultRemoteURL
	if envURL := os.Getenv("KUNGFU_URL"); envURL != "" {
		remote = ResolveCatalogURL(envURL)
	} else if envURL := os.Getenv("KUNGFU_REMOTE_URL"); envURL != "" {
		remote = ResolveCatalogURL(envURL)
	}

	c := &Catalog{
		items:        make(map[string]Item),
		embeddedData: make(map[string]string),
		remoteURL:    remote,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		enableRemote: true,
		enableLocal:  true,
	}

	for _, opt := range opts {
		opt(c)
	}

	// 1. Fetch live remote catalog manifest if enabled with 3 exponential retries and caching
	if c.enableRemote && c.remoteURL != "" && c.manifest == nil {
		c.fetchRemoteLocked(context.Background())
	}

	// 2. Register built-in embedded kungfu item and discover local/installed skills
	c.mu.Lock()
	c.discoverEmbeddedLocked()
	if c.enableLocal {
		c.discoverLocalSkillsLocked()
	}
	c.mu.Unlock()
	return c, nil
}

// Sync synchronizes the remote catalog manifest with the local cache.
func (c *Catalog) Sync(ctx context.Context, force bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.enableRemote || c.remoteURL == "" {
		return nil
	}

	c.forceRefresh = force
	c.fetchRemoteLocked(ctx)
	c.discoverEmbeddedLocked()
	c.discoverLocalSkillsLocked()
	return nil
}

func (c *Catalog) fetchRemoteLocked(ctx context.Context) {
	cachePath := GetCatalogCachePath()
	if c.cacheDir != "" {
		cachePath = filepath.Join(c.cacheDir, "index.json")
	}

	var cached *CachedCatalog
	if !c.noCache {
		if loaded, err := LoadCachedCatalog(cachePath); err == nil && loaded != nil && loaded.URL == c.remoteURL {
			cached = loaded
		}
	}

	// If cache is fresh and not forced refresh, use cached manifest immediately
	if cached != nil && !c.forceRefresh && time.Since(cached.FetchedAt) < DefaultCatalogTTL {
		c.manifest = &cached.Manifest
		for _, it := range cached.Manifest.Items {
			c.items[strings.ToLower(it.ID)] = it
			c.items[strings.ToLower(it.Name)] = it
		}
	} else {
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		headers := make(map[string]string)
		if cached != nil && cached.ETag != "" && !c.forceRefresh {
			headers["If-None-Match"] = cached.ETag
		}

		resp, err := fetchWithRetryAndHeaders(reqCtx, c.httpClient, c.remoteURL, version.UserAgent(), headers, defaultMaxRetries, defaultInitialBackoff)
		if err == nil && resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotModified && cached != nil {
				// 304 Not Modified: refresh fetched timestamp and use cache
				_ = SaveCachedCatalog(cachePath, c.remoteURL, cached.Manifest, cached.ETag)
				c.manifest = &cached.Manifest
				for _, it := range cached.Manifest.Items {
					c.items[strings.ToLower(it.ID)] = it
					c.items[strings.ToLower(it.Name)] = it
				}
			} else if resp.StatusCode == http.StatusOK {
				data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
				if err == nil {
					var manifest Manifest
					if err := json.Unmarshal(data, &manifest); err == nil {
						c.manifest = &manifest
						etag := resp.Header.Get("ETag")
						if !c.noCache {
							_ = SaveCachedCatalog(cachePath, c.remoteURL, manifest, etag)
						}
						for _, it := range manifest.Items {
							c.items[strings.ToLower(it.ID)] = it
							c.items[strings.ToLower(it.Name)] = it
						}
					}
				}
			}
		}

		if c.manifest == nil && cached != nil {
			// Offline / non-200 HTTP fallback to cache
			c.manifest = &cached.Manifest
			for _, it := range cached.Manifest.Items {
				c.items[strings.ToLower(it.ID)] = it
				c.items[strings.ToLower(it.Name)] = it
			}
		}
	}
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

// Categories returns the distinct categories in the catalog with item counts.
// Categories are dynamically discovered from skills and manifest declarations; never hardcoded.
func (c *Catalog) Categories() []Category {
	c.mu.RLock()
	defer c.mu.RUnlock()

	catCount := make(map[string]int)
	catNames := make(map[string]string)
	catEmojis := make(map[string]string)
	catDescs := make(map[string]string)
	seen := make(map[string]struct{})

	for _, item := range c.items {
		if _, already := seen[item.ID]; already {
			continue
		}
		seen[item.ID] = struct{}{}
		if item.Category != "" {
			catKey := strings.ToLower(item.Category)
			catCount[catKey]++
			if catNames[catKey] == "" {
				if item.CategoryName != "" {
					catNames[catKey] = item.CategoryName
				} else {
					catNames[catKey] = item.Category
				}
			}
			if catEmojis[catKey] == "" && item.CategoryEmoji != "" {
				catEmojis[catKey] = item.CategoryEmoji
			}
		}
	}

	var result []Category
	seenCats := make(map[string]struct{})

	// 1. Include categories from manifest if explicitly defined
	if c.manifest != nil {
		for _, mc := range c.manifest.Categories {
			idLower := strings.ToLower(mc.ID)
			if idLower == "" {
				idLower = strings.ToLower(mc.Name)
			}
			if idLower == "" {
				continue
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

	// 2. Include dynamic categories discovered from items
	for catKey, count := range catCount {
		if _, exists := seenCats[catKey]; !exists {
			name := catNames[catKey]
			if name == "" {
				name = catKey
			}
			result = append(result, Category{
				ID:          catKey,
				Name:        name,
				Emoji:       catEmojis[catKey],
				Description: catDescs[catKey],
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
	if c.embeddedFS == nil {
		return
	}
	items, contents, err := DiscoverSkillsInFS(c.embeddedFS)
	if err == nil {
		for _, item := range items {
			c.items[strings.ToLower(item.ID)] = item
			c.items[strings.ToLower(item.Name)] = item
		}
		for k, v := range contents {
			c.embeddedData[k] = v
		}
	}
}

func (c *Catalog) discoverLocalSkillsLocked() {
	var candidateRoots []string

	// 1. Workspace directories
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidateRoots = append(candidateRoots, filepath.Join(wd, ".agents", "skills"))
		candidateRoots = append(candidateRoots, filepath.Join(wd, ".gemini", "skills"))
		candidateRoots = append(candidateRoots, filepath.Join(wd, "skills"))
	}

	// 2. Global user directories
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidateRoots = append(candidateRoots, filepath.Join(home, ".agents", "skills"))
		candidateRoots = append(candidateRoots, filepath.Join(home, ".gemini", "config", "skills"))
		candidateRoots = append(candidateRoots, filepath.Join(home, ".cache", "kungfu", "skills"))
	}

	for _, root := range candidateRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillDir := filepath.Join(root, entry.Name())
			skillFile := filepath.Join(skillDir, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				skillFile = filepath.Join(skillDir, "skill.md")
				if _, err := os.Stat(skillFile); err != nil {
					continue
				}
			}
			data, err := os.ReadFile(skillFile)
			if err != nil || len(data) == 0 {
				continue
			}
			fm, body, err := ParseFrontmatter(data)
			if err != nil || fm == nil {
				continue
			}
			name := strings.TrimSpace(fm.Name)
			if name == "" {
				name = entry.Name()
			}
			key := strings.ToLower(name)

			existing, exists := c.items[key]
			if exists && existing.Source != "local" {
				// For remote catalog items, only populate category/tags if empty; keep remote version authoritative
				if existing.Category == "" && fm.Metadata != nil && fm.Metadata["category"] != "" {
					existing.Category = fm.Metadata["category"]
				}
				if len(existing.Tags) == 0 && fm.Metadata != nil && fm.Metadata["tags"] != "" {
					existing.Tags = ParseTags(fm.Metadata["tags"])
				}
				if existing.Path == "" {
					existing.Path = skillFile
				}
				c.items[key] = existing
				c.items[strings.ToLower(existing.ID)] = existing
				c.items[strings.ToLower(existing.Name)] = existing
				continue
			}

			if !exists {
				existing = Item{
					ID:     name,
					Name:   name,
					Type:   ItemTypeSkill,
					Source: "local",
					Path:   skillFile,
				}
			}

			if fm.Description != "" {
				existing.Description = fm.Description
			}
			if fm.License != "" {
				existing.License = fm.License
			}
			if fm.AllowedTools != "" {
				existing.AllowedTools = fm.AllowedTools
			}
			if body != "" && existing.Body == "" {
				existing.Body = body
			}
			if fm.Metadata != nil {
				if existing.Metadata == nil {
					existing.Metadata = make(map[string]string)
				}
				for mk, mv := range fm.Metadata {
					existing.Metadata[mk] = mv
				}
				if cat, ok := fm.Metadata["category"]; ok && cat != "" {
					existing.Category = cat
				}
				if ver, ok := fm.Metadata["version"]; ok && ver != "" {
					existing.Version = ver
				}
				if auth, ok := fm.Metadata["author"]; ok && auth != "" {
					existing.Author = auth
				}
				if tags, ok := fm.Metadata["tags"]; ok && tags != "" {
					existing.Tags = ParseTags(tags)
				}
			}
			if existing.Path == "" {
				existing.Path = skillFile
			}

			existing.Normalize()

			c.items[key] = existing
			c.items[strings.ToLower(existing.ID)] = existing
			c.items[strings.ToLower(existing.Name)] = existing
		}
	}
}

// LoadContent retrieves the full raw markdown content for an item.
// It loads embedded skills, in-memory bodies, or streams live from remote URLs.
// Kungfu does not load skills from local filesystem paths; local paths are only used for installation via learn.
func (c *Catalog) LoadContent(item *Item) (string, error) {
	if item == nil {
		return "", errors.New("cannot load content for nil item")
	}

	// 1. Embedded skills
	if item.Source == "embedded" {
		if content, ok := c.embeddedData[strings.ToLower(item.ID)]; ok && content != "" {
			return content, nil
		}
		if content, ok := c.embeddedData[strings.ToLower(item.Name)]; ok && content != "" {
			return content, nil
		}
	}

	// 2. Direct frontmatter body already in memory
	if strings.HasPrefix(item.Body, "---") {
		return item.Body, nil
	}

	// 3. Remote URL: Stream live over HTTP into memory
	remoteURL := item.URL
	if remoteURL == "" && item.Metadata != nil && item.Metadata["canonical"] != "" {
		remoteURL = item.Metadata["canonical"]
	}
	if remoteURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		client := c.httpClient
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		resp, err := fetchWithRetry(ctx, client, remoteURL, version.UserAgent(), defaultMaxRetries, defaultInitialBackoff)
		if err != nil {
			return "", fmt.Errorf("failed to download skill %q from %s: network unreachable: %w", item.Name, remoteURL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to download skill %q from %s: HTTP %d", item.Name, remoteURL, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
		if err != nil {
			return "", fmt.Errorf("failed to read response for %q: %w", item.Name, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("empty content downloaded for %q", item.Name)
		}
		expectedDigest := item.Digest
		if expectedDigest == "" {
			expectedDigest = item.Sha256
		}
		if expectedDigest != "" {
			if err := VerifyDigest(data, expectedDigest); err != nil {
				return "", fmt.Errorf("%w: downloaded content for %q does not match expected digest %s", ErrDigestMismatch, item.Name, expectedDigest)
			}
		}

		return string(data), nil
	}

	// 4. If item has raw body, return it
	if item.Body != "" {
		return item.Body, nil
	}

	return "", fmt.Errorf("content unavailable for skill %q (remote URL or embedded content required)", item.Name)
}
