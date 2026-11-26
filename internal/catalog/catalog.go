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
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

const (
	defaultMaxRetries     = 3
	defaultInitialBackoff = 100 * time.Millisecond
)

// ResolveCatalogURL resolves a catalog base origin or JSON endpoint URL.
func ResolveCatalogURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultRemoteURL
	}
	if strings.HasSuffix(strings.ToLower(raw), ".json") {
		return raw
	}
	raw = strings.TrimSuffix(raw, "/")
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw + "/.well-known/agent-skills/index.json"
}

// Option configures the Catalog instance.
type Option func(*Catalog)

// WithRemoteURL sets a custom remote catalog URL or base origin.
func WithRemoteURL(rawURL string) Option {
	return func(c *Catalog) {
		c.remoteURL = ResolveCatalogURL(rawURL)
	}
}

// WithHTTPClient sets a custom HTTP client for remote fetching.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Catalog) {
		c.httpClient = client
		c.customClient = true
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

// WithNamedCatalogs restricts cached named-catalog loading to the exact configured catalog names.
// When set, orphaned and legacy unhashed cache files in <cacheDir>/catalogs/ are ignored.
func WithNamedCatalogs(names []string) Option {
	return func(c *Catalog) {
		c.filterNamedCatalogs = true
		c.namedCatalogs = make([]string, 0, len(names))
		for _, n := range names {
			if trimmed := strings.TrimSpace(n); trimmed != "" {
				c.namedCatalogs = append(c.namedCatalogs, trimmed)
			}
		}
	}
}

type cachedCatalogMeta struct {
	name      string
	fetchedAt time.Time
}

// Catalog stores and resolves discovered Agent Skills with thread-safe access.
type Catalog struct {
	mu                  sync.RWMutex
	items               map[string]Item
	catalogMeta         map[string]cachedCatalogMeta
	manifest            *Manifest
	remoteURL           string
	httpClient          *http.Client
	customClient        bool
	enableRemote        bool
	enableLocal         bool
	cacheDir            string
	noCache             bool
	forceRefresh        bool
	offline             bool
	filterNamedCatalogs bool
	namedCatalogs       []string
}

// NewCatalog constructs a Catalog instance with option modifiers.
func NewCatalog(opts ...Option) (*Catalog, error) {
	remote := DefaultRemoteURL
	if envURL := strings.TrimSpace(os.Getenv("KUNGFU_URL")); envURL != "" {
		remote = ResolveCatalogURL(envURL)
	}

	c := &Catalog{
		items:        make(map[string]Item),
		catalogMeta:  make(map[string]cachedCatalogMeta),
		remoteURL:    remote,
		enableRemote: true,
		enableLocal:  true,
	}

	for _, opt := range opts {
		opt(c)
	}

	if !c.customClient || c.httpClient == nil {
		c.httpClient = NewSafeHTTPClientWithLoopback(15*time.Second, IsLoopbackURL(c.remoteURL))
	}

	if c.enableRemote && c.remoteURL != "" && c.manifest == nil {
		if err := c.fetchRemoteLocked(context.Background()); err != nil {
			return nil, err
		}
	}

	c.mu.Lock()
	c.loadNamedCatalogsLocked()
	if c.enableLocal {
		c.discoverLocalSkillsLocked()
	}
	c.mu.Unlock()
	return c, nil
}

func (c *Catalog) resolveCacheRoot() string {
	dir, err := ResolveCacheDir(c.cacheDir)
	if err != nil {
		return ""
	}
	return dir
}

func (c *Catalog) setDefaultItemsLocked(items []Item) {
	c.items = make(map[string]Item, len(items)*4)
	for _, it := range items {
		it.Source = "default"
		lowID := strings.ToLower(it.ID)
		lowName := strings.ToLower(it.Name)
		c.items[lowID] = it
		c.items[lowName] = it
		c.items[lowID+"@default"] = it
		c.items[lowName+"@default"] = it
	}
}

func (c *Catalog) fetchRemoteLocked(ctx context.Context) error {
	cacheRoot := ""
	if !c.noCache {
		cacheRoot = c.resolveCacheRoot()
	}

	allowLoopback := AllowLoopbackHTTP(c.httpClient)

	var cached *CachedCatalog
	if !c.noCache && cacheRoot != "" {
		if loaded, err := LoadNamedCatalogCache(cacheRoot, "default"); err == nil && loaded != nil && loaded.URL == c.remoteURL {
			cached = loaded
		}
	}

	if cached != nil && !c.forceRefresh && (c.offline || time.Since(cached.FetchedAt) < DefaultCatalogTTL) {
		if err := ResolveManifestURLs(&cached.Manifest, c.remoteURL, allowLoopback); err != nil {
			return err
		}
		c.manifest = &cached.Manifest
		c.setDefaultItemsLocked(cached.Manifest.Items)
	} else if c.offline {
		return nil
	} else {
		reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		headers := map[string]string{"Accept": "application/json"}
		if cached != nil && cached.ETag != "" {
			headers["If-None-Match"] = cached.ETag
		}

		resp, err := FetchWithRetryAndHeaders(reqCtx, c.httpClient, c.remoteURL, version.UserAgent(), headers, defaultMaxRetries, defaultInitialBackoff)
		if err != nil {
			return fmt.Errorf("catalog: fetching %s: %w", c.remoteURL, err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusNotModified && cached != nil {
			if err := ResolveManifestURLs(&cached.Manifest, c.remoteURL, allowLoopback); err != nil {
				return err
			}
			if !c.noCache && cacheRoot != "" {
				_ = SaveNamedCatalogCache(cacheRoot, "default", c.remoteURL, cached.Manifest, cached.ETag)
			}
			c.manifest = &cached.Manifest
			c.setDefaultItemsLocked(cached.Manifest.Items)
		} else if resp.StatusCode == http.StatusOK {
			data, err := ReadBounded(resp.Body, 10<<20)
			if err != nil {
				return fmt.Errorf("catalog: reading %s body: %w", c.remoteURL, err)
			}
			var manifest Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return fmt.Errorf("catalog: parsing %s JSON: %w", c.remoteURL, err)
			}
			if err := ResolveManifestURLs(&manifest, c.remoteURL, allowLoopback); err != nil {
				return err
			}
			c.manifest = &manifest
			etag := resp.Header.Get("ETag")
			if !c.noCache && cacheRoot != "" {
				_ = SaveNamedCatalogCache(cacheRoot, "default", c.remoteURL, manifest, etag)
			}
			c.setDefaultItemsLocked(manifest.Items)
		} else {
			return &HTTPStatusError{StatusCode: resp.StatusCode, URL: c.remoteURL}
		}
	}

	return nil
}

func (c *Catalog) loadNamedCatalogsLocked() {
	if c.noCache {
		return
	}
	cacheRoot := c.resolveCacheRoot()
	if cacheRoot == "" {
		return
	}

	var namedCaches map[string]*CachedCatalog
	if c.filterNamedCatalogs {
		namedCaches = make(map[string]*CachedCatalog, len(c.namedCatalogs))
		for _, catName := range c.namedCatalogs {
			if strings.EqualFold(catName, "default") {
				continue
			}
			cached, err := LoadNamedCatalogCache(cacheRoot, catName)
			if err != nil || cached == nil {
				continue
			}
			namedCaches[catName] = cached
		}
	} else {
		var err error
		namedCaches, err = LoadAllNamedCatalogCaches(cacheRoot)
		if err != nil {
			return
		}
	}
	if len(namedCaches) == 0 {
		return
	}
	catNames := make([]string, 0, len(namedCaches))
	for catName := range namedCaches {
		if strings.EqualFold(catName, "default") {
			continue
		}
		catNames = append(catNames, catName)
	}
	sort.Strings(catNames)

	for _, catName := range catNames {
		cached := namedCaches[catName]
		if cached == nil {
			continue
		}
		c.catalogMeta[strings.ToLower(catName)] = cachedCatalogMeta{
			name:      catName,
			fetchedAt: cached.FetchedAt,
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

// HasFreshCatalog reports whether name is loaded in memory and its cache timestamp is within DefaultCatalogTTL.
func (c *Catalog) HasFreshCatalog(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	meta, ok := c.catalogMeta[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return false
	}
	return !meta.fetchedAt.IsZero() && time.Since(meta.fetchedAt) < DefaultCatalogTTL
}

// IsCatalogCacheStale reports whether name is loaded from a cache older than DefaultCatalogTTL.
func (c *Catalog) IsCatalogCacheStale(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	meta, ok := c.catalogMeta[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return false
	}
	return meta.fetchedAt.IsZero() || time.Since(meta.fetchedAt) >= DefaultCatalogTTL
}

// StaleCatalogs returns sorted display names of all loaded named catalogs whose cache exceeds DefaultCatalogTTL.
func (c *Catalog) StaleCatalogs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var stale []string
	for _, meta := range c.catalogMeta {
		if strings.EqualFold(meta.name, "default") {
			continue
		}
		if meta.fetchedAt.IsZero() || time.Since(meta.fetchedAt) >= DefaultCatalogTTL {
			stale = append(stale, meta.name)
		}
	}
	sort.Strings(stale)
	return stale
}

func shouldPreferCatalogItem(existing, candidate Item) bool {
	if candidate.Version != "" && existing.Version != "" {
		return CompareSemver(candidate.Version, existing.Version) > 0
	}
	return false
}

func (c *Catalog) removeCatalogItemsLocked(name string) {
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if cleanName == "" {
		return
	}
	suffix := "@" + cleanName
	for k, it := range c.items {
		src := strings.ToLower(strings.TrimSpace(it.Source))
		srcTrimmed := strings.TrimPrefix(src, "github:")
		if src == cleanName || srcTrimmed == cleanName || strings.HasSuffix(strings.ToLower(k), suffix) {
			delete(c.items, k)
		}
	}
	delete(c.catalogMeta, cleanName)
}

// AddCatalogManifest replaces and registers items from an added or refreshed catalog manifest.
func (c *Catalog) AddCatalogManifest(name string, manifest *Manifest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if manifest == nil {
		return
	}
	c.removeCatalogItemsLocked(name)
	c.catalogMeta[strings.ToLower(strings.TrimSpace(name))] = cachedCatalogMeta{
		name:      name,
		fetchedAt: time.Now().UTC(),
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
	c.removeCatalogItemsLocked(name)
}

// Get retrieves a deep-copied Item by ID or Name (case-insensitive), supporting
// exact @catalog qualifiers as well as unambiguous short repo suffix matches
// (e.g. "go@go-skills" matching "go@spf13/go-skills").
func (c *Catalog) Get(idOrName string) (*Item, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := strings.ToLower(strings.TrimSpace(idOrName))
	if item, exists := c.items[key]; exists {
		itCopy := item.Clone()
		return &itCopy, nil
	}

	if skillPart, catPart, ok := strings.Cut(key, "@"); ok && skillPart != "" && catPart != "" && catPart != "default" {
		suffix := "/" + catPart
		prefix := skillPart + "@"
		matchedCats := make(map[string]Item)
		for k, it := range c.items {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			fullCat := k[len(prefix):]
			if strings.HasSuffix(fullCat, suffix) {
				displayCat := it.Source
				if displayCat == "" {
					displayCat = fullCat
				}
				matchedCats[displayCat] = it
			}
		}
		if len(matchedCats) == 1 {
			for _, it := range matchedCats {
				itCopy := it.Clone()
				return &itCopy, nil
			}
		}
		if len(matchedCats) > 1 {
			names := make([]string, 0, len(matchedCats))
			for name := range matchedCats {
				names = append(names, name)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("%w: %q matches multiple catalogs (%s); use the full catalog name", ErrAmbiguousCatalog, catPart, strings.Join(names, ", "))
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrItemNotFound, idOrName)
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

// All returns deep copies of all unique (ID, Source) items across all loaded catalogs, sorted by ID then Source.
func (c *Catalog) All() []Item {
	c.mu.RLock()
	defer c.mu.RUnlock()

	type itemKey struct {
		id     string
		source string
	}
	seen := make(map[itemKey]struct{}, len(c.items))
	result := make([]Item, 0, len(c.items))

	for _, item := range c.items {
		k := itemKey{
			id:     strings.ToLower(item.ID),
			source: strings.ToLower(strings.TrimSpace(item.Source)),
		}
		if _, already := seen[k]; already {
			continue
		}
		seen[k] = struct{}{}
		result = append(result, item.Clone())
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Source < result[j].Source
	})
	return result
}

func (c *Catalog) discoverLocalSkillsLocked() {
	var candidateRoots []string

	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidateRoots = append(candidateRoots,
			filepath.Join(wd, ".agents", "skills"),
			filepath.Join(wd, "skills"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidateRoots = append(candidateRoots, filepath.Join(home, ".agents", "skills"))
	}

	for _, root := range candidateRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			skillDir := filepath.Join(root, entry.Name())
			if !IsPathContained(root, skillDir) {
				continue
			}
			fi, err := os.Stat(skillDir)
			if err != nil || !fi.IsDir() {
				continue
			}

			skillFile := filepath.Join(skillDir, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				skillFile = filepath.Join(skillDir, "skill.md")
				if _, err := os.Stat(skillFile); err != nil {
					continue
				}
			}
			if !IsPathContained(root, skillFile) {
				continue
			}
			f, err := os.Open(skillFile)
			if err != nil {
				continue
			}
			data, readErr := ReadBounded(f, DefaultMaxSkillMDBytes)
			_ = f.Close()
			if readErr != nil || len(data) == 0 {
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
			if existing.Body == "" {
				existing.Body = string(data)
			}
			if existing.Path == "" {
				existing.Path = skillFile
			}
			ApplyFrontmatter(&existing, fm, true)
			existing.Normalize()

			c.items[key] = existing
			c.items[strings.ToLower(existing.ID)] = existing
			c.items[strings.ToLower(existing.Name)] = existing
		}
	}
}

// LoadContent retrieves the full raw markdown content for an item using context.Background().
func (c *Catalog) LoadContent(item *Item) (string, error) {
	return c.LoadContentContext(context.Background(), item, false)
}

// LoadContentContext retrieves the full raw markdown content for an item with context and optional digest bypass.
func (c *Catalog) LoadContentContext(ctx context.Context, item *Item, forceBypassDigest bool) (string, error) {
	if item == nil {
		return "", errors.New("cannot load content for nil item")
	}

	expectedDigest := item.EffectiveDigest()

	// 1. Direct frontmatter body already in memory (verify digest & respect forceRefresh).
	if strings.HasPrefix(item.Body, "---") && !c.forceRefresh {
		if !forceBypassDigest && expectedDigest != "" {
			if err := VerifyDigest([]byte(item.Body), expectedDigest); err != nil {
				return "", fmt.Errorf("%w: inline content for %q does not match expected digest %s", err, item.Name, expectedDigest)
			}
		}
		return item.Body, nil
	}

	// 2. Remote URL: Check digest-verified local cache first, otherwise stream live over HTTP.
	remoteURL := item.URL
	if remoteURL == "" && item.Metadata != nil {
		remoteURL = item.Metadata["canonical"]
	}
	if remoteURL != "" {
		cacheRoot := ""
		if !c.noCache {
			cacheRoot = c.resolveCacheRoot()
		}

		if cacheRoot != "" && !c.forceRefresh && item.Name != "" && expectedDigest != "" {
			if skillDir, err := SkillCacheDir(cacheRoot, item.Name, item.Source); err == nil {
				cachedMD := filepath.Join(skillDir, "SKILL.md")
				if IsArchiveItem(*item) {
					if isArchiveDirCachedWithDigest(skillDir, expectedDigest) {
						if f, err := os.Open(cachedMD); err == nil {
							cachedData, readErr := ReadBounded(f, DefaultMaxSkillMDBytes)
							_ = f.Close()
							if readErr == nil && len(cachedData) > 0 {
								EnrichItemFromSkillMD(item, cachedData)
								return string(cachedData), nil
							}
						}
					}
				} else if f, err := os.Open(cachedMD); err == nil {
					cachedData, readErr := ReadBounded(f, DefaultMaxSkillMDBytes)
					_ = f.Close()
					if readErr == nil && len(cachedData) > 0 && VerifyDigest(cachedData, expectedDigest) == nil {
						EnrichItemFromSkillMD(item, cachedData)
						return string(cachedData), nil
					}
				}
			}
		}

		reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()

		client := c.httpClient
		if client == nil {
			client = NewSafeHTTPClient(15 * time.Second)
		}

		// Archive path: unified download + required digest verification + unpack to cache.
		if IsArchiveItem(*item) {
			extractedMD, _, _, err := FetchAndUnpackArchive(reqCtx, client, remoteURL, expectedDigest, cacheRoot, item.Name, item.Source, forceBypassDigest)
			if err != nil {
				return "", err
			}
			EnrichItemFromSkillMD(item, []byte(extractedMD))
			return extractedMD, nil
		}

		resp, err := FetchWithRetryAndHeaders(reqCtx, client, remoteURL, version.UserAgent(), nil, defaultMaxRetries, defaultInitialBackoff)
		if err != nil {
			return "", fmt.Errorf("failed to download skill %q from %s: %w", item.Name, remoteURL, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", &HTTPStatusError{StatusCode: resp.StatusCode, URL: remoteURL}
		}

		data, err := ReadBounded(resp.Body, DefaultMaxSkillMDBytes)
		if err != nil {
			return "", fmt.Errorf("failed to read response for %q: %w", item.Name, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("empty content downloaded for %q", item.Name)
		}

		// Sniff archive payload even if manifest did not tag type: "archive".
		if DetectArchiveFormat(data, remoteURL) != "" {
			if !forceBypassDigest {
				if err := VerifyRequiredDigest(data, expectedDigest); err != nil {
					return "", fmt.Errorf("%w: archive %q", err, item.Name)
				}
			}
			extractedMD, err := ExtractSkillMDFromArchive(data)
			if err != nil {
				return "", fmt.Errorf("failed to extract SKILL.md from archive for %q: %w", item.Name, err)
			}
			EnrichItemFromSkillMD(item, []byte(extractedMD))
			if cacheRoot != "" && item.Name != "" {
				if _, _, err := UnpackArchiveToCache(data, cacheRoot, item.Name, item.Source, expectedDigest); err != nil {
					return "", fmt.Errorf("failed to unpack archive for %q: %w", item.Name, err)
				}
			}
			return extractedMD, nil
		}

		if !forceBypassDigest && expectedDigest != "" {
			if err := VerifyDigest(data, expectedDigest); err != nil {
				return "", fmt.Errorf("%w: downloaded content for %q does not match expected digest %s", err, item.Name, expectedDigest)
			}
		}

		EnrichItemFromSkillMD(item, data)
		return string(data), nil
	}

	// 3. Fallback to raw body when no remote URL is set.
	if item.Body != "" {
		if !forceBypassDigest && expectedDigest != "" {
			if err := VerifyDigest([]byte(item.Body), expectedDigest); err != nil {
				return "", fmt.Errorf("%w: body for %q does not match expected digest %s", err, item.Name, expectedDigest)
			}
		}
		return item.Body, nil
	}

	return "", fmt.Errorf("content unavailable for skill %q: missing remote URL or body", item.Name)
}
