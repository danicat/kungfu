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
	"maps"
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
const DefaultRemoteURL = "https://skills.danicat.dev/.well-known/agent-skills/index.json"

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
	return raw + "/.well-known/agent-skills/index.json"
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

// WithOffline configures the catalog to use cached manifests only without making network requests.
func WithOffline(offline bool) Option {
	return func(c *Catalog) {
		c.offline = offline
	}
}

// Catalog implements the Store interface with deterministic in-memory lookup.
type Catalog struct {
	mu           sync.RWMutex
	items        map[string]Item
	manifest     *Manifest
	remoteURL    string
	httpClient   *http.Client
	enableRemote bool
	enableLocal  bool
	cacheDir     string
	noCache      bool
	forceRefresh bool
	offline      bool
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
			_ = resp.Body.Close()
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
		if err := c.fetchRemoteLocked(context.Background()); err != nil {
			return nil, err
		}
	}

	// 2. Discover local/installed skills
	c.mu.Lock()
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

	prevForce := c.forceRefresh
	c.forceRefresh = force
	defer func() { c.forceRefresh = prevForce }()

	if err := c.fetchRemoteLocked(ctx); err != nil {
		return err
	}
	if c.enableLocal {
		c.discoverLocalSkillsLocked()
	}
	return nil
}

func (c *Catalog) fetchRemoteLocked(ctx context.Context) error {
	var cachePath string
	if !c.noCache {
		if c.cacheDir != "" {
			cachePath = filepath.Join(c.cacheDir, "index.json")
		} else {
			cachePath = GetCatalogCachePath()
		}
	}

	var cached *CachedCatalog
	if !c.noCache && cachePath != "" {
		if loaded, err := LoadCachedCatalog(cachePath); err == nil && loaded != nil && loaded.URL == c.remoteURL {
			cached = loaded
		}
	}

	// If cache is fresh (or offline mode is active) and not forced refresh, use cached manifest immediately
	if cached != nil && !c.forceRefresh && (c.offline || time.Since(cached.FetchedAt) < DefaultCatalogTTL) {
		ResolveManifestURLs(&cached.Manifest, c.remoteURL)
		c.manifest = &cached.Manifest
		c.items = make(map[string]Item)
		for _, it := range cached.Manifest.Items {
			c.items[strings.ToLower(it.ID)] = it
			c.items[strings.ToLower(it.Name)] = it
		}
	} else if c.offline {
		c.loadNamedCatalogsLocked()
		return nil
	} else {
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		headers := make(map[string]string)
		if cached != nil && cached.ETag != "" && !c.forceRefresh {
			headers["If-None-Match"] = cached.ETag
		}

		resp, err := fetchWithRetryAndHeaders(reqCtx, c.httpClient, c.remoteURL, version.UserAgent(), headers, defaultMaxRetries, defaultInitialBackoff)
		if err != nil {
			return fmt.Errorf("catalog: fetching %s: %w", c.remoteURL, err)
		}
		if resp == nil {
			return fmt.Errorf("catalog: nil response from %s", c.remoteURL)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusNotModified && cached != nil {
			// 304 Not Modified: refresh fetched timestamp and use cache
			ResolveManifestURLs(&cached.Manifest, c.remoteURL)
			if !c.noCache && cachePath != "" {
				_ = SaveCachedCatalog(cachePath, c.remoteURL, cached.Manifest, cached.ETag)
			}
			c.manifest = &cached.Manifest
			c.items = make(map[string]Item)
			for _, it := range cached.Manifest.Items {
				c.items[strings.ToLower(it.ID)] = it
				c.items[strings.ToLower(it.Name)] = it
			}
		} else if resp.StatusCode == http.StatusOK {
			data, err := ReadBounded(resp.Body, 10<<20)
			if err != nil {
				return fmt.Errorf("catalog: reading %s body: %w", c.remoteURL, err)
			}
			var manifest Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return fmt.Errorf("catalog: parsing %s JSON: %w", c.remoteURL, err)
			}
			ResolveManifestURLs(&manifest, c.remoteURL)
			c.manifest = &manifest
			etag := resp.Header.Get("ETag")
			if !c.noCache && cachePath != "" {
				_ = SaveCachedCatalog(cachePath, c.remoteURL, manifest, etag)
			}
			c.items = make(map[string]Item)
			for _, it := range manifest.Items {
				c.items[strings.ToLower(it.ID)] = it
				c.items[strings.ToLower(it.Name)] = it
			}
		} else {
			return fmt.Errorf("catalog: unexpected HTTP %d from %s", resp.StatusCode, c.remoteURL)
		}
	}

	// Also load all registered/named external catalogs from cache
	c.loadNamedCatalogsLocked()
	return nil
}

func (c *Catalog) loadNamedCatalogsLocked() {
	if c.noCache {
		return
	}
	namedCaches, err := LoadAllNamedCatalogCachesIn(c.resolveCacheRoot())
	if err != nil || len(namedCaches) == 0 {
		return
	}
	var catNames []string
	for catName := range namedCaches {
		catNames = append(catNames, catName)
	}
	sort.Strings(catNames)

	for _, catName := range catNames {
		cached := namedCaches[catName]
		if cached == nil {
			continue
		}
		for _, it := range cached.Manifest.Items {
			it.Normalize()
			it.Source = catName
			lowID := strings.ToLower(it.ID)
			lowName := strings.ToLower(it.Name)
			if existing, exists := c.items[lowID]; !exists || shouldPreferCatalogItem(existing, it) {
				c.items[lowID] = it
			}
			if existing, exists := c.items[lowName]; !exists || shouldPreferCatalogItem(existing, it) {
				c.items[lowName] = it
			}
			c.items[fmt.Sprintf("%s@%s", lowID, strings.ToLower(catName))] = it
			c.items[fmt.Sprintf("%s@%s", lowName, strings.ToLower(catName))] = it
		}
	}
}

func shouldPreferCatalogItem(existing, candidate Item) bool {
	if candidate.Version != "" && existing.Version != "" {
		return CompareSemver(candidate.Version, existing.Version) > 0
	}
	return false
}

// AddCatalogManifest dynamically registers items from a newly added or updated catalog manifest.
func (c *Catalog) AddCatalogManifest(name string, manifest *Manifest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if manifest == nil {
		return
	}
	for _, it := range manifest.Items {
		it.Normalize()
		it.Source = name
		lowID := strings.ToLower(it.ID)
		lowName := strings.ToLower(it.Name)
		if existing, exists := c.items[lowID]; !exists || existing.Source == name || shouldPreferCatalogItem(existing, it) {
			c.items[lowID] = it
		}
		if existing, exists := c.items[lowName]; !exists || existing.Source == name || shouldPreferCatalogItem(existing, it) {
			c.items[lowName] = it
		}
		c.items[fmt.Sprintf("%s@%s", lowID, strings.ToLower(name))] = it
		c.items[fmt.Sprintf("%s@%s", lowName, strings.ToLower(name))] = it
	}
}

// RemoveCatalogItems removes items belonging to a specified catalog source.
func (c *Catalog) RemoveCatalogItems(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if cleanName == "" {
		return
	}
	safeName := SafeCatalogFileName(cleanName)
	suffix := "@" + cleanName
	for k, it := range c.items {
		src := strings.ToLower(strings.TrimSpace(it.Source))
		srcTrimmed := strings.TrimPrefix(src, "github:")
		if src == cleanName || srcTrimmed == cleanName ||
			(safeName != "" && (SafeCatalogFileName(src) == safeName || SafeCatalogFileName(srcTrimmed) == safeName)) ||
			strings.HasSuffix(strings.ToLower(k), suffix) {
			delete(c.items, k)
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

// ListByFilter filters unique catalog items by boolean tag query.
func (c *Catalog) ListByFilter(filter QueryFilter) []Item {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tagQuery := strings.TrimSpace(filter.TagQuery)
	seen := make(map[string]struct{})
	var result []Item

	for _, item := range c.items {
		lowID := strings.ToLower(item.ID)
		if primary, ok := c.items[lowID]; ok {
			item = primary
			lowID = strings.ToLower(item.ID)
		}
		if _, already := seen[lowID]; already {
			continue
		}
		seen[lowID] = struct{}{}

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

func (c *Catalog) discoverLocalSkillsLocked() {
	var candidateRoots []string

	// 1. Workspace directories
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidateRoots = append(candidateRoots, filepath.Join(wd, ".agents", "skills"))
		candidateRoots = append(candidateRoots, filepath.Join(wd, "skills"))
	}

	// 2. Global user directories and cache directory
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidateRoots = append(candidateRoots, filepath.Join(home, ".agents", "skills"))
	}
	var cacheSkillsDir string
	if cacheRoot := c.resolveCacheRoot(); cacheRoot != "" && !c.noCache {
		cacheSkillsDir = filepath.Join(cacheRoot, "skills")
		candidateRoots = append(candidateRoots, cacheSkillsDir)
	}

	for _, root := range candidateRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			skillDir := filepath.Join(root, entry.Name())
			if !entry.IsDir() {
				info, err := os.Stat(skillDir)
				if err != nil || !info.IsDir() {
					continue
				}
			}
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
			fm, _, err := ParseFrontmatter(data)
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
				// For remote catalog items, only populate tags if empty; keep remote version authoritative
				if len(existing.Tags) == 0 && fm.Metadata != nil && fm.Metadata["tags"] != "" {
					existing.Tags = ParseTags(fm.Metadata["tags"])
				}
				isCachePath := strings.Contains(filepath.ToSlash(skillFile), "/.cache/kungfu/skills/") ||
					(cacheSkillsDir != "" && IsPathContained(cacheSkillsDir, skillFile))
				if existing.Path == "" && !isCachePath {
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
			if len(data) > 0 && existing.Body == "" {
				existing.Body = string(data)
			}
			if fm.Metadata != nil {
				if existing.Metadata == nil {
					existing.Metadata = make(map[string]string)
				}
				maps.Copy(existing.Metadata, fm.Metadata)
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

func (c *Catalog) resolveCacheRoot() string {
	if c.cacheDir != "" {
		return c.cacheDir
	}
	if envDir := os.Getenv("KUNGFU_CACHE_DIR"); envDir != "" {
		return envDir
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, ".cache", "kungfu")
	}
	return ""
}

// LoadContent retrieves the full raw markdown content for an item.
// It loads in-memory bodies or streams live from remote URLs.
// Kungfu does not load skills from local filesystem paths; local paths are only used for installation via learn.
func (c *Catalog) LoadContent(item *Item) (string, error) {
	if item == nil {
		return "", errors.New("cannot load content for nil item")
	}

	// 1. Direct frontmatter body already in memory
	if strings.HasPrefix(item.Body, "---") {
		return item.Body, nil
	}

	// 2. Remote URL: Check digest-verified local cache first, otherwise stream live over HTTP
	remoteURL := item.URL
	if remoteURL == "" && item.Metadata != nil && item.Metadata["canonical"] != "" {
		remoteURL = item.Metadata["canonical"]
	}
	if remoteURL != "" {
		expectedDigest := item.Digest
		if expectedDigest == "" {
			expectedDigest = item.Sha256
		}
		if !c.noCache && !c.forceRefresh && item.Name != "" && expectedDigest != "" {
			if cacheRoot := c.resolveCacheRoot(); cacheRoot != "" {
				cleanSkillName := filepath.Base(filepath.Clean(item.Name))
				if cleanSkillName != "" && cleanSkillName != "." && cleanSkillName != ".." {
					cachedMD := filepath.Join(cacheRoot, "skills", cleanSkillName, "SKILL.md")
					if IsArchiveItem(*item) {
						if IsArchiveCachedWithDigest(cacheRoot, cleanSkillName, expectedDigest) {
							if cachedData, err := os.ReadFile(cachedMD); err == nil && len(cachedData) > 0 {
								EnrichItemFromSkillMD(item, cachedData)
								return string(cachedData), nil
							}
						}
					} else if cachedData, err := os.ReadFile(cachedMD); err == nil && len(cachedData) > 0 {
						if VerifyDigest(cachedData, expectedDigest) == nil {
							EnrichItemFromSkillMD(item, cachedData)
							return string(cachedData), nil
						}
					}
				}
			}
		}

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
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to download skill %q from %s: HTTP %d", item.Name, remoteURL, resp.StatusCode)
		}
		data, err := ReadBounded(resp.Body, DefaultMaxUnpackedBytes)
		if err != nil {
			return "", fmt.Errorf("failed to read response for %q: %w", item.Name, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("empty content downloaded for %q", item.Name)
		}
		if expectedDigest != "" {
			if err := VerifyDigest(data, expectedDigest); err != nil {
				return "", fmt.Errorf("%w: downloaded content for %q does not match expected digest %s", ErrDigestMismatch, item.Name, expectedDigest)
			}
		}

		// If item is an archive (.tar.gz, .zip or type == "archive"), extract SKILL.md and unpack to cache
		if IsArchiveItem(*item) || DetectArchiveFormat(data, remoteURL) != "" {
			extractedMD, err := ExtractSkillMDFromArchive(data)
			if err != nil {
				return "", fmt.Errorf("failed to extract SKILL.md from archive for %q: %w", item.Name, err)
			}
			EnrichItemFromSkillMD(item, []byte(extractedMD))
			if !c.noCache && item.Name != "" {
				if cacheRoot := c.resolveCacheRoot(); cacheRoot != "" {
					if _, err := UnpackArchiveToCache(data, cacheRoot, item.Name, expectedDigest); err != nil {
						return "", fmt.Errorf("failed to unpack archive for %q: %w", item.Name, err)
					}
				}
			}
			return extractedMD, nil
		}

		EnrichItemFromSkillMD(item, data)
		return string(data), nil
	}

	// 3. If item has raw body, return it
	if item.Body != "" {
		return item.Body, nil
	}

	return "", fmt.Errorf("content unavailable for skill %q: missing remote URL or body", item.Name)
}
