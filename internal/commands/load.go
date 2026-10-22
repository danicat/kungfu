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

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/config"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/search"
	"github.com/danicat/kungfu/internal/state"
)

type loadTarget struct {
	item    catalog.Item
	subpath string
}

const DefaultPageSize = 6000
const MaxPageSize = 7000

func paginateContent(content string, pageSize int) []string {
	if pageSize <= 0 || len(content) <= pageSize {
		return []string{content}
	}

	lines := strings.Split(content, "\n")
	var pages []string
	var cur strings.Builder

	for i, line := range lines {
		toAdd := len(line)
		if i > 0 && cur.Len() > 0 {
			toAdd += 2 // account for "\r\n" in PTY line disciplines
		}

		if cur.Len() > 0 && cur.Len()+toAdd > pageSize {
			pages = append(pages, cur.String())
			cur.Reset()
		}

		// Handle lines longer than pageSize without severing UTF-8 runes
		for len(line) > pageSize {
			if cur.Len() > 0 {
				pages = append(pages, cur.String())
				cur.Reset()
			}
			splitIdx := pageSize
			for splitIdx > 0 && !utf8.RuneStart(line[splitIdx]) {
				splitIdx--
			}
			if splitIdx == 0 {
				_, size := utf8.DecodeRuneInString(line)
				splitIdx = size
			}
			pages = append(pages, line[:splitIdx])
			line = line[splitIdx:]
		}

		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(line)
	}

	if cur.Len() > 0 {
		pages = append(pages, cur.String())
	}

	if len(pages) == 0 {
		return []string{content}
	}

	return pages
}

func (a *App) RunLoad(args []string) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag bool
	var localFlag, updateFlag, forceFlag, fullFlag bool
	var pageFlag, pageSizeFlag int

	fs.BoolVar(&jsonFlag, "json", false, "Output loaded content as JSON")
	fs.BoolVar(&rawFlag, "raw", false, "Output raw verbatim markdown content without extra formatting")
	fs.BoolVar(&localFlag, "local", false, "Use locally cached or installed copy without checking remote upstream")
	fs.BoolVar(&updateFlag, "update", false, "Check remote upstream and update cached copy before loading")
	fs.BoolVar(&updateFlag, "u", false, "Check remote upstream and update cached copy before loading (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Force loading/updating even if digest verification fails")
	fs.BoolVar(&forceFlag, "f", false, "Force loading/updating even if digest verification fails (shorthand)")
	fs.IntVar(&pageFlag, "page", 1, "Page number to display (1-based)")
	fs.IntVar(&pageFlag, "p", 1, "Page number to display (1-based, shorthand)")
	fs.IntVar(&pageSizeFlag, "page-size", DefaultPageSize, "Maximum page size in bytes")
	fs.BoolVar(&fullFlag, "full", false, "Output complete unpaginated content")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: load requires a skill name or subpath (e.g. 'kungfu load git/reference')")
		return ExitUsage
	}
	if len(posArgs) > 1 {
		_, _ = fmt.Fprintln(a.Stderr, "error: loading multiple skills simultaneously is disabled to prevent truncation; load skills individually (e.g. 'kungfu load <skill>')")
		return ExitUsage
	}
	if pageFlag < 1 {
		_, _ = fmt.Fprintf(a.Stderr, "error: invalid page %d: page must be >= 1\n", pageFlag)
		return ExitUsage
	}
	if pageSizeFlag <= 0 || pageSizeFlag > MaxPageSize {
		_, _ = fmt.Fprintf(a.Stderr, "error: invalid page-size %d: page-size must be between 1 and %d bytes (agent buffer safety ceiling)\n", pageSizeFlag, MaxPageSize)
		return ExitUsage
	}
	if fullFlag && pageFlag > 1 {
		_, _ = fmt.Fprintln(a.Stderr, "error: cannot combine --full with -p/--page > 1")
		return ExitUsage
	}

	spec := posArgs[0]
	parts := strings.SplitN(spec, "/", 2)
	skillName := parts[0]
	subpath := ""
	if len(parts) == 2 {
		subpath = parts[1]
	}

	item, err := a.Store.Get(skillName)
	if err != nil {
		a.printNotFoundWithSuggestions("load", spec)
		return ExitNotFound
	}
	target := loadTarget{
		item:    *item,
		subpath: subpath,
	}

	var content string
	var loadErr error

	if target.subpath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		content, loadErr = runner.ReadFileContext(ctx, target.item, target.subpath, a.getHTTPClient())
		if loadErr != nil {
			if errors.Is(loadErr, runner.ErrInvalidPath) {
				_, _ = fmt.Fprintf(a.Stderr, "error: invalid path traversal in %q\n", target.subpath)
				return ExitUsage
			}
			if errors.Is(loadErr, runner.ErrFileNotFound) {
				_, _ = fmt.Fprintf(a.Stderr, "error: subpath %q not found in skill %q.\n", target.subpath, target.item.Name)
				var candidates []string
				for _, ref := range a.listReferences(target.item) {
					candidates = append(candidates, ref, "references/"+ref, "references/"+ref+".md")
				}
				for _, scr := range a.listScripts(target.item) {
					candidates = append(candidates, scr, "scripts/"+scr)
				}
				candidates = uniqueStrings(candidates)
				if len(candidates) > 0 {
					suggs := search.Suggest(target.subpath, candidates, 3)
					if len(suggs) > 0 {
						_, _ = fmt.Fprintln(a.Stderr, "\nDid you mean:")
						for _, s := range suggs {
							_, _ = fmt.Fprintf(a.Stderr, "  - kungfu load %s/%s\n", target.item.Name, s)
						}
					}
				}
				return ExitNotFound
			}
			_, _ = fmt.Fprintf(a.Stderr, "error: loading %s/%s: %v\n", target.item.Name, target.subpath, loadErr)
			return MapErrorToExitCode(loadErr)
		}
	} else {
		rawContent, err := a.loadItemContentWithOpts(&target.item, localFlag, updateFlag, forceFlag)
		if err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: loading skill %q: %v\n", target.item.Name, err)
			if errors.Is(err, catalog.ErrDigestMismatch) {
				_, _ = fmt.Fprintln(a.Stderr, "\nDid you mean:")
				_, _ = fmt.Fprintf(a.Stderr, "  - Load local cached copy:   kungfu load %s --local\n", target.item.Name)
				_, _ = fmt.Fprintf(a.Stderr, "  - Check upstream update:    kungfu load %s --update\n", target.item.Name)
				_, _ = fmt.Fprintf(a.Stderr, "  - Ignore digest check:      kungfu load %s --force\n", target.item.Name)
				_, _ = fmt.Fprintf(a.Stderr, "  - Synchronize catalogs:     kungfu update\n")
			}
			return MapErrorToExitCode(err)
		}
		if mode == OutputModeRaw {
			content = rawContent
		} else {
			content = stripFrontmatter(rawContent)
		}
	}

	var pages []string
	if fullFlag {
		pages = []string{content}
	} else {
		pages = paginateContent(content, pageSizeFlag)
	}

	totalPages := len(pages)
	if pageFlag > totalPages {
		_, _ = fmt.Fprintf(a.Stderr, "error: page %d out of bounds; %s has %d page(s) (valid pages: 1..%d)\n", pageFlag, spec, totalPages, totalPages)
		return ExitUsage
	}

	currentPageContent := pages[pageFlag-1]
	a.recordSkillLoad(target.item.Name, target.subpath, pageFlag)

	if mode == OutputModeJSON {
		ver := target.item.Version
		if ver == "" && target.item.Metadata != nil {
			ver = target.item.Metadata["version"]
		}

		pageNum := 0
		totPages := 0
		if totalPages > 1 && !fullFlag {
			pageNum = pageFlag
			totPages = totalPages
		}

		type LoadResultItem struct {
			Name        string `json:"name"`
			Subpath     string `json:"subpath,omitempty"`
			Category    string `json:"category"`
			Version     string `json:"version,omitempty"`
			Description string `json:"description"`
			Page        int    `json:"page,omitempty"`
			TotalPages  int    `json:"total_pages,omitempty"`
			Content     string `json:"content"`
			Digest      string `json:"digest,omitempty"`
			Source      string `json:"source,omitempty"`
		}

		res := LoadResultItem{
			Name:        target.item.Name,
			Subpath:     target.subpath,
			Category:    target.item.Category,
			Version:     ver,
			Description: target.item.Description,
			Page:        pageNum,
			TotalPages:  totPages,
			Content:     currentPageContent,
			Digest:      target.item.Digest,
			Source:      target.item.Source,
		}

		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return ExitSuccess
	}

	var sb strings.Builder
	if totalPages > 1 && !fullFlag && pageFlag > 1 && mode != OutputModeRaw {
		sb.WriteString(fmt.Sprintf("[Page %d of %d (continued)]\n\n", pageFlag, totalPages))
	}

	sb.WriteString(currentPageContent)
	if !strings.HasSuffix(currentPageContent, "\n") {
		sb.WriteString("\n")
	}

	if totalPages > 1 && !fullFlag && mode != OutputModeRaw {
		sb.WriteString("\n---\n")
		if pageFlag < totalPages {
			sb.WriteString(fmt.Sprintf("[Page %d of %d • Next: kungfu load %s -p %d • Use --full to display all]\n", pageFlag, totalPages, spec, pageFlag+1))
		} else {
			sb.WriteString(fmt.Sprintf("[Page %d of %d • End of document]\n", pageFlag, totalPages))
		}
	}

	_, _ = fmt.Fprint(a.Stdout, sb.String())
	return ExitSuccess
}

func uniqueStrings(slice []string) []string {
	seen := make(map[string]bool)
	var res []string
	for _, s := range slice {
		if !seen[s] {
			seen[s] = true
			res = append(res, s)
		}
	}
	return res
}

func (a *App) loadItemContentWithOpts(item *catalog.Item, localOnly, forceUpdate, forceDigest bool) (string, error) {
	// 1. If --local is specified, attempt to load from local cache or workspace immediately
	if localOnly {
		// Check workspace installation
		if item.Path != "" {
			skillMD := filepath.Join(item.Path, "SKILL.md")
			if data, err := os.ReadFile(skillMD); err == nil && len(data) > 0 {
				return string(data), nil
			}
			if fi, err := os.Stat(item.Path); err == nil && !fi.IsDir() {
				if data, err := os.ReadFile(item.Path); err == nil && len(data) > 0 {
					return string(data), nil
				}
			}
		}

		// Check local disk cache (~/.cache/kungfu/skills/<name>/SKILL.md)
		homeDir, err := os.UserHomeDir()
		if err != nil || homeDir == "" {
			return "", fmt.Errorf("load --local: unable to determine user home directory: %w", err)
		}
		cachedMD := filepath.Join(homeDir, ".cache", "kungfu", "skills", item.Name, "SKILL.md")
		if data, err := os.ReadFile(cachedMD); err == nil && len(data) > 0 {
			return string(data), nil
		}
		return "", fmt.Errorf("skill %q not found in local cache. Run 'kungfu load %s' to fetch from remote", item.Name, item.Name)
	}

	// 2. Normal load or update load
	autoUpdate := true
	if a.Config != nil {
		autoUpdate = a.Config.IsAutoUpdate()
	}

	resolveDefaultCatalogURL := func() string {
		catURL := config.DefaultCatalogURL
		if envURL := os.Getenv("KUNGFU_URL"); envURL != "" {
			catURL = envURL
		} else if envURL := os.Getenv("KUNGFU_REMOTE_URL"); envURL != "" {
			catURL = envURL
		} else if a.Config != nil && a.Config.GetCatalogURL("default") != "" {
			catURL = a.Config.GetCatalogURL("default")
		}
		return catalog.ResolveCatalogURL(catURL)
	}

	if forceUpdate {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		freshItem := a.refreshCatalogForItem(ctx, item, resolveDefaultCatalogURL())
		cancel()
		if freshItem != nil {
			tempItem := *freshItem
			if tempItem.URL == "" && item.URL != "" {
				tempItem.URL = item.URL
			}
			*item = tempItem
		}
	}

	// Try standard loadContent
	content, err := a.loadItemContent(item)
	if err == nil {
		// Save to cache for offline/local access
		if item.Name != "" {
			runner.CacheSkillFile(item.Name, "SKILL.md", []byte(content))
		}
		return content, nil
	}

	// If error is not digest mismatch or remote is not reachable, check if forceDigest is set
	if errors.Is(err, catalog.ErrDigestMismatch) {
		// Case A: Upstream may have updated! Check remote catalog index to see if catalog has a new digest
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		freshItem := a.refreshCatalogForItem(ctx, item, resolveDefaultCatalogURL())
		if freshItem != nil {
			// Re-fetch content using the fresh digest
			tempItem := *freshItem
			if tempItem.URL == "" && item.URL != "" {
				tempItem.URL = item.URL
			}
			freshContent, freshContentErr := a.loadItemContent(&tempItem)
			if freshContentErr == nil {
				// The new catalog manifest acknowledges this content!
				if autoUpdate || forceUpdate {
					runner.CacheSkillFile(freshItem.Name, "SKILL.md", []byte(freshContent))
					*item = tempItem

					if !a.Options.NoColor && a.Options.Mode != OutputModeJSON && a.Options.Mode != OutputModeRaw {
						_, _ = fmt.Fprintf(a.Stderr, "✓ Detected upstream update for %q; cache updated.\n", item.Name)
					}
					return freshContent, nil
				}
				return "", fmt.Errorf("%w: upstream content for %q has changed (new digest %s); run 'kungfu load %s --update' to update", catalog.ErrDigestMismatch, item.Name, freshItem.Digest, item.Name)
			}
		}

		// If forceDigest was passed, bypass verification
		if forceDigest {
			_, _ = fmt.Fprintf(a.Stderr, "warning: bypassing integrity verification for %q (--force)\n", item.Name)
			// Fetch raw bytes without verify
			remoteURL := item.URL
			if remoteURL == "" && item.Metadata != nil {
				remoteURL = item.Metadata["canonical"]
			}
			if remoteURL != "" {
				reqCtx, reqCancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer reqCancel()
				req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodGet, remoteURL, nil)
				if reqErr != nil {
					return "", fmt.Errorf("creating force-refresh request for %s: %w", remoteURL, reqErr)
				}
				resp, rErr := a.getHTTPClient().Do(req)
				if rErr != nil {
					return "", fmt.Errorf("fetching %s (--force): %w", remoteURL, rErr)
				}
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusOK {
					return "", fmt.Errorf("fetching %s (--force): HTTP %d", remoteURL, resp.StatusCode)
				}
				b, readErr := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
				if readErr != nil {
					return "", fmt.Errorf("reading %s (--force): %w", remoteURL, readErr)
				}
				if len(b) > 0 {
					runner.CacheSkillFile(item.Name, "SKILL.md", b)
					return string(b), nil
				}
			}
		}
	}

	return "", err
}

func (a *App) refreshCatalogForItem(ctx context.Context, item *catalog.Item, defaultCatURL string) *catalog.Item {
	if item == nil {
		return nil
	}

	findInManifest := func(m *catalog.Manifest) *catalog.Item {
		if m == nil {
			return nil
		}
		for _, mItem := range m.Items {
			if strings.EqualFold(mItem.Name, item.Name) || strings.EqualFold(mItem.ID, item.ID) {
				it := mItem
				return &it
			}
		}
		return nil
	}

	// 1. Check if item comes from a registered named catalog (e.g. "kungfu" -> github:danicat/kungfu)
	st := a.loadState()
	candidateCats := []string{
		item.Source,
		strings.TrimPrefix(item.Source, "github:"),
	}
	if os.Getenv("KUNGFU_URL") == "" && os.Getenv("KUNGFU_REMOTE_URL") == "" {
		candidateCats = append(candidateCats, item.Name)
	}
	seenCats := make(map[string]bool)
	for _, cand := range candidateCats {
		cand = strings.TrimSpace(cand)
		if cand == "" || cand == "default" || seenCats[cand] {
			continue
		}
		seenCats[cand] = true
		catRec, ok := st.GetCatalog(cand)
		if !ok || catRec.Type == state.CatalogTypeDefault {
			continue
		}
		switch catRec.Type {
		case state.CatalogTypeGitHub:
			if target, okTarget := catalog.ParseGitHubTarget(catRec.Source); okTarget {
				if catRec.Ref != "" {
					target.Ref = catRec.Ref
				}
				if freshManifest, err := catalog.FetchGitHubCatalog(ctx, target, a.getHTTPClient()); err == nil && freshManifest != nil {
					_ = catalog.SaveNamedCatalogCache(catRec.Name, catRec.URL, *freshManifest, "")
					if cat, ok := a.Store.(*catalog.Catalog); ok {
						cat.AddCatalogManifest(catRec.Name, freshManifest)
					}
					if found := findInManifest(freshManifest); found != nil {
						return found
					}
				}
			}
		case state.CatalogTypeHTTP:
			if freshManifest, freshETag, err := catalog.FetchHTTPCatalog(ctx, catRec.Source, a.getHTTPClient()); err == nil && freshManifest != nil {
				_ = catalog.SaveNamedCatalogCache(catRec.Name, catRec.URL, *freshManifest, freshETag)
				if cat, ok := a.Store.(*catalog.Catalog); ok {
					cat.AddCatalogManifest(catRec.Name, freshManifest)
				}
				if found := findInManifest(freshManifest); found != nil {
					return found
				}
			}
		}
	}

	// 2. Refresh default catalog
	if freshManifest, freshETag, fetchErr := catalog.FetchHTTPCatalog(ctx, defaultCatURL, a.getHTTPClient()); fetchErr == nil && freshManifest != nil {
		cachePath := catalog.GetCatalogCachePath()
		if a.Options.CacheDir != "" {
			cachePath = filepath.Join(a.Options.CacheDir, "index.json")
		}
		_ = catalog.SaveCachedCatalog(cachePath, defaultCatURL, *freshManifest, freshETag)
		if found := findInManifest(freshManifest); found != nil {
			return found
		}
	}

	return nil
}
