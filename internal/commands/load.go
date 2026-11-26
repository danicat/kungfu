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

package commands

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/search"
)

type loadTarget struct {
	item    catalog.Item
	subpath string
}

const (
	DefaultPageSize = 6000
	MaxPageSize     = 7000
)

func detectFenceMarker(line string) (marker string, fullLine string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) < 3 {
		return "", "", false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return "", "", false
	}
	count := 0
	for count < len(trimmed) && trimmed[count] == ch {
		count++
	}
	if count < 3 {
		return "", "", false
	}
	return trimmed[:count], trimmed, true
}

// paginateContent splits content into byte-bounded pages without dropping blank lines,
// severing UTF-8 runes, or leaving unclosed Markdown code fences across page breaks.
func paginateContent(content string, pageSize int) []string {
	if pageSize <= 0 || len(content) <= pageSize {
		return []string{content}
	}

	lines := strings.Split(content, "\n")
	var pages []string
	var cur strings.Builder
	hasLines := false

	var openFenceMarker string
	var openFenceHeader string

	flushPage := func() {
		if !hasLines {
			return
		}
		if openFenceMarker != "" {
			cur.WriteString("\n")
			cur.WriteString(openFenceMarker)
		}
		pages = append(pages, cur.String())
		cur.Reset()
		hasLines = false
		if openFenceMarker != "" {
			cur.WriteString(openFenceHeader)
			hasLines = true
		}
	}

	for _, line := range lines {
		// Reserve space for closing fence if currently inside a code block.
		closeReserve := 0
		if openFenceMarker != "" {
			closeReserve = len(openFenceMarker) + 2
		}

		toAdd := len(line)
		if hasLines {
			toAdd += 2 // account for "\r\n" in PTY line disciplines
		}

		if hasLines && cur.Len()+toAdd+closeReserve > pageSize {
			flushPage()
		}

		// Handle single lines longer than pageSize without severing UTF-8 runes.
		maxChunk := pageSize - closeReserve
		if maxChunk < 1 {
			maxChunk = pageSize
		}
		for len(line) > maxChunk {
			if hasLines {
				flushPage()
			}
			splitIdx := maxChunk
			for splitIdx > 0 && !utf8.RuneStart(line[splitIdx]) {
				splitIdx--
			}
			if splitIdx == 0 {
				_, size := utf8.DecodeRuneInString(line)
				splitIdx = size
			}
			chunk := line[:splitIdx]
			if hasLines {
				cur.WriteString("\n")
			}
			cur.WriteString(chunk)
			hasLines = true
			flushPage()
			line = line[splitIdx:]
		}

		if hasLines {
			cur.WriteString("\n")
		}
		cur.WriteString(line)
		hasLines = true

		// Update open Markdown code fence state after appending line.
		if marker, full, ok := detectFenceMarker(line); ok {
			if openFenceMarker == "" {
				openFenceMarker = marker
				openFenceHeader = full
			} else if marker[0] == openFenceMarker[0] && len(marker) >= len(openFenceMarker) && strings.TrimSpace(full[len(marker):]) == "" {
				openFenceMarker = ""
				openFenceHeader = ""
			}
		}
	}

	if hasLines {
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
	fs.BoolVar(&rawFlag, "raw", false, "Output raw verbatim markdown content including frontmatter")
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

	pageExplicit := false
	pageSizeExplicit := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "page", "p":
			pageExplicit = true
		case "page-size":
			pageSizeExplicit = true
		}
	})

	if (jsonFlag || a.Options.Mode == OutputModeJSON) && rawFlag {
		_, _ = fmt.Fprintln(a.Stderr, "error: cannot combine --json and --raw")
		return ExitUsage
	}
	if localFlag && updateFlag {
		_, _ = fmt.Fprintln(a.Stderr, "error: cannot combine --local and --update")
		return ExitUsage
	}
	if localFlag && forceFlag {
		_, _ = fmt.Fprintln(a.Stderr, "error: cannot combine --local and --force")
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag)
	if rawFlag {
		mode = OutputModeRaw
	}

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

	// Default --raw to unpaginated so raw output never silently truncates unless caller asks for pages.
	effectiveFull := fullFlag || (mode == OutputModeRaw && !pageExplicit && !pageSizeExplicit)

	spec := posArgs[0]
	lookupSpec, baseSkill, subpath, catQual := parseLoadSpec(spec)

	item, err := a.resolveSkill(lookupSpec)
	if err != nil && localFlag && baseSkill != "" {
		cacheCat := catQual
		if cacheCat != "" {
			if resolvedCat, _, ok, rErr := a.resolveConfiguredCatalog(cacheCat); rErr != nil {
				_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", rErr)
				return MapErrorToExitCode(rErr)
			} else if ok {
				cacheCat = resolvedCat
			}
		}
		if cacheRoot := a.cacheRootDir(); cacheRoot != "" {
			if skillDir, findErr := catalog.FindCachedSkillDir(cacheRoot, baseSkill, cacheCat); findErr == nil {
				cachedMD := filepath.Join(skillDir, "SKILL.md")
				if f, openErr := os.Open(cachedMD); openErr == nil {
					data, readErr := catalog.ReadBounded(f, catalog.DefaultMaxSkillMDBytes)
					_ = f.Close()
					if readErr == nil && len(data) > 0 {
						synth := catalog.Item{
							ID:     baseSkill,
							Name:   baseSkill,
							Source: cacheCat,
							Path:   skillDir,
						}
						if synth.Source == "" {
							synth.Source = "cache"
						}
						catalog.EnrichItemFromSkillMD(&synth, data)
						item = &synth
						err = nil
					}
				}
			}
		}
	}
	if err != nil && errors.Is(err, catalog.ErrItemNotFound) && !localFlag && shouldProbeGitHubOnLoad(spec, subpath) {
		if ghTarget, ok := catalog.ParseGitHubTarget(spec); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			ghItem, ghErr := catalog.FetchGitHubSkill(ctx, ghTarget, a.getHTTPClient())
			cancel()
			if ghErr == nil && ghItem != nil {
				item = ghItem
				subpath = ""
				err = nil
			} else if ghErr != nil && !errors.Is(ghErr, catalog.ErrItemNotFound) {
				_, _ = fmt.Fprintf(a.Stderr, "error: fetching skill from github %q: %v\n", spec, ghErr)
				return MapErrorToExitCode(ghErr)
			}
		}
	}
	if err != nil {
		if !errors.Is(err, catalog.ErrItemNotFound) {
			_, _ = fmt.Fprintf(a.Stderr, "error: resolving skill %q: %v\n", spec, err)
			return MapErrorToExitCode(err)
		}
		suggestQuery := spec
		if isBundledSubpath(subpath) {
			suggestQuery = baseSkill
		}
		a.printNotFoundWithSuggestions("load", suggestQuery)
		return ExitNotFound
	}

	target := loadTarget{
		item:    *item,
		subpath: subpath,
	}

	var content string
	var loadErr error

	if target.subpath != "" {
		a.warnIfLocalCopyOutdated(target.item, true)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		subpathItem := target.item
		if localFlag {
			subpathItem.URL = ""
			subpathItem.Metadata = nil
		}

		content, loadErr = a.getRunner().ReadFileContext(ctx, subpathItem, target.subpath, a.getHTTPClient())
		if loadErr != nil {
			if errors.Is(loadErr, runner.ErrInvalidPath) {
				_, _ = fmt.Fprintf(a.Stderr, "error: invalid path traversal in %q\n", target.subpath)
				return ExitUsage
			}
			if errors.Is(loadErr, runner.ErrFileNotFound) {
				if localFlag {
					_, _ = fmt.Fprintf(a.Stderr, "error: subpath %q not found in local cache for skill %q. Run 'kungfu load %s/%s' to fetch from remote.\n", target.subpath, target.item.Name, target.item.Name, target.subpath)
					return ExitNotFound
				}
				_, _ = fmt.Fprintf(a.Stderr, "error: subpath %q not found in skill %q.\n", target.subpath, target.item.Name)
				var candidates []string
				seen := make(map[string]struct{})
				addCand := func(c string) {
					if _, ok := seen[c]; !ok {
						seen[c] = struct{}{}
						candidates = append(candidates, c)
					}
				}
				for _, ref := range a.collectReferences(target.item, true) {
					addCand(ref)
					addCand("references/" + ref)
				}
				for _, refFile := range a.collectReferences(target.item, false) {
					addCand(refFile)
					addCand("references/" + refFile)
				}
				for _, scr := range a.listScripts(target.item) {
					addCand(scr)
					addCand("scripts/" + scr)
				}
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
		rawContent, err := a.loadItemContentWithOpts(&target.item, localFlag, updateFlag, forceFlag, mode)
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
		a.warnIfLocalCopyOutdated(target.item, localFlag)
	}

	var pages []string
	if effectiveFull {
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
	a.recordSkillLoad(target.item, target.subpath, pageFlag)

	if mode == OutputModeJSON {
		type LoadResultItem struct {
			Name        string `json:"name"`
			Subpath     string `json:"subpath,omitempty"`
			Version     string `json:"version,omitempty"`
			Description string `json:"description"`
			Page        int    `json:"page"`
			TotalPages  int    `json:"total_pages"`
			Content     string `json:"content"`
			Digest      string `json:"digest,omitempty"`
			Source      string `json:"source,omitempty"`
		}

		res := LoadResultItem{
			Name:        target.item.Name,
			Subpath:     target.subpath,
			Version:     itemVersion(target.item),
			Description: target.item.Description,
			Page:        pageFlag,
			TotalPages:  totalPages,
			Content:     currentPageContent,
			Digest:      target.item.Digest,
			Source:      target.item.Source,
		}
		if err := writeJSON(a.Stdout, res); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
		return ExitSuccess
	}

	var sb strings.Builder
	if totalPages > 1 && !effectiveFull && pageFlag > 1 && mode != OutputModeRaw {
		_, _ = fmt.Fprintf(&sb, "[Page %d of %d (continued)]\n\n", pageFlag, totalPages)
	}

	sb.WriteString(currentPageContent)
	if !strings.HasSuffix(currentPageContent, "\n") {
		sb.WriteString("\n")
	}

	if totalPages > 1 && !effectiveFull && mode != OutputModeRaw {
		sb.WriteString("\n---\n")
		if pageFlag < totalPages {
			_, _ = fmt.Fprintf(&sb, "[Page %d of %d • Next: kungfu load %s -p %d]\n", pageFlag, totalPages, spec, pageFlag+1)
		} else {
			_, _ = fmt.Fprintf(&sb, "[Page %d of %d • End of document]\n", pageFlag, totalPages)
		}
	}

	_, _ = fmt.Fprint(a.Stdout, sb.String())
	return ExitSuccess
}

func (a *App) cacheSkillMD(item catalog.Item, content string) {
	if item.Name == "" || content == "" {
		return
	}
	cacheRoot := a.cacheRootDir()
	if cacheRoot == "" {
		return
	}
	skillDir, err := catalog.SkillCacheDir(cacheRoot, item.Name, item.Source)
	if err != nil {
		return
	}
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		return
	}
	targetPath := filepath.Join(skillDir, "SKILL.md")
	if !catalog.IsPathContained(skillDir, targetPath) {
		return
	}
	tmpFile, err := os.CreateTemp(skillDir, ".SKILL.md-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.WriteString(content); err == nil && tmpFile.Chmod(0600) == nil && tmpFile.Sync() == nil && tmpFile.Close() == nil {
		_ = os.Rename(tmpPath, targetPath)
		return
	}
	_ = tmpFile.Close()
	_ = os.Remove(tmpPath)
}

func (a *App) loadItemContentWithOpts(item *catalog.Item, localOnly, forceUpdate, forceDigest bool, mode OutputMode) (string, error) {
	if localOnly {
		if item.Path != "" {
			var skillMD string
			if fi, err := os.Stat(item.Path); err == nil {
				if fi.IsDir() {
					skillMD = filepath.Join(item.Path, "SKILL.md")
				} else {
					skillMD = item.Path
				}
			}
			if skillMD != "" {
				if f, err := os.Open(skillMD); err == nil {
					data, readErr := catalog.ReadBounded(f, catalog.DefaultMaxSkillMDBytes)
					_ = f.Close()
					if readErr == nil && len(data) > 0 {
						return string(data), nil
					}
				}
			}
		}

		cacheRoot := a.cacheRootDir()
		if cacheRoot == "" {
			return "", fmt.Errorf("load --local: unable to determine cache directory")
		}
		if skillDir, err := catalog.FindCachedSkillDir(cacheRoot, item.Name, item.Source); err == nil {
			if f, err := os.Open(filepath.Join(skillDir, "SKILL.md")); err == nil {
				data, readErr := catalog.ReadBounded(f, catalog.DefaultMaxSkillMDBytes)
				_ = f.Close()
				if readErr == nil && len(data) > 0 {
					return string(data), nil
				}
			}
		}
		return "", fmt.Errorf("%w: skill %q not found in local cache; run 'kungfu load %s' to fetch from remote", catalog.ErrItemNotFound, item.Name, item.Name)
	}

	if forceUpdate {
		if cacheRoot := a.cacheRootDir(); cacheRoot != "" && item.Name != "" {
			if skillDir, err := catalog.SkillCacheDir(cacheRoot, item.Name, item.Source); err == nil {
				_ = os.Remove(filepath.Join(skillDir, ".digest"))
				_ = os.Remove(filepath.Join(skillDir, ".skillmd.digest"))
				_ = os.Remove(filepath.Join(skillDir, "SKILL.md"))
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		freshItem := a.refreshCatalogForItem(ctx, item, a.resolveDefaultCatalogURL())
		cancel()
		if freshItem != nil {
			tempItem := *freshItem
			if tempItem.URL == "" && item.URL != "" {
				tempItem.URL = item.URL
			}
			*item = tempItem
		}
	}

	content, err := a.Store.LoadContent(item)
	if err == nil {
		if !catalog.IsArchiveItem(*item) {
			a.cacheSkillMD(*item, content)
		}
		return content, nil
	}

	if errors.Is(err, catalog.ErrDigestMismatch) || errors.Is(err, catalog.ErrMissingDigest) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		freshItem := a.refreshCatalogForItem(ctx, item, a.resolveDefaultCatalogURL())
		cancel()
		if freshItem != nil {
			tempItem := *freshItem
			if tempItem.URL == "" && item.URL != "" {
				tempItem.URL = item.URL
			}
			freshContent, freshContentErr := a.Store.LoadContent(&tempItem)
			if freshContentErr == nil {
				if !catalog.IsArchiveItem(tempItem) {
					a.cacheSkillMD(tempItem, freshContent)
				}
				*item = tempItem
				if mode == OutputModeHuman {
					_, _ = fmt.Fprintf(a.Stderr, "✓ Detected upstream update for %q; cache updated.\n", item.Name)
				}
				return freshContent, nil
			}
		}

		if forceDigest {
			forceCtx, forceCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer forceCancel()
			forcedContent, forceErr := a.Store.LoadContentContext(forceCtx, item, true)
			if forceErr == nil {
				_, _ = fmt.Fprintf(a.Stderr, "warning: bypassed integrity verification for %q (--force)\n", item.Name)
				return forcedContent, nil
			}
			return "", forceErr
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

	candidateCats := []string{
		item.Source,
		strings.TrimPrefix(item.Source, "github:"),
	}
	if strings.TrimSpace(os.Getenv("KUNGFU_URL")) == "" {
		candidateCats = append(candidateCats, item.Name)
	}
	seenCats := make(map[string]bool)
	for _, cand := range candidateCats {
		cand = strings.TrimSpace(cand)
		if cand == "" || strings.EqualFold(cand, "default") || seenCats[cand] {
			continue
		}
		seenCats[cand] = true
		catName, src, ok, rErr := a.resolveConfiguredCatalog(cand)
		if rErr != nil || !ok || strings.EqualFold(catName, "default") {
			continue
		}
		freshManifest, freshETag, _, err := catalog.FetchSource(ctx, src, a.getHTTPClient())
		if err == nil && freshManifest != nil {
			if cacheRoot := a.cacheRootDir(); cacheRoot != "" {
				_ = catalog.SaveNamedCatalogCache(cacheRoot, catName, src, *freshManifest, freshETag)
			}
			a.Store.AddCatalogManifest(catName, freshManifest)
			if found := findInManifest(freshManifest); found != nil {
				found.Source = catName
				return found
			}
		}
	}

	if defaultCatURL != "" {
		if freshManifest, freshETag, err := catalog.FetchHTTPCatalog(ctx, defaultCatURL, a.getHTTPClient()); err == nil && freshManifest != nil {
			if cacheRoot := a.cacheRootDir(); cacheRoot != "" {
				_ = catalog.SaveNamedCatalogCache(cacheRoot, "default", defaultCatURL, *freshManifest, freshETag)
			}
			if found := findInManifest(freshManifest); found != nil {
				return found
			}
		}
	}

	return nil
}
