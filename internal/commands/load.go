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

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/config"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/search"
)

type loadTarget struct {
	item    catalog.Item
	subpath string
}

func (a *App) RunLoad(args []string) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag bool
	var localFlag, updateFlag, forceFlag bool

	fs.BoolVar(&jsonFlag, "json", false, "Output loaded content as JSON")
	fs.BoolVar(&rawFlag, "raw", false, "Output raw verbatim markdown content without extra formatting")
	fs.BoolVar(&localFlag, "local", false, "Use locally cached or installed copy without checking remote upstream")
	fs.BoolVar(&updateFlag, "update", false, "Check remote upstream and update cached copy before loading")
	fs.BoolVar(&updateFlag, "u", false, "Check remote upstream and update cached copy before loading (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Force loading/updating even if digest verification fails")
	fs.BoolVar(&forceFlag, "f", false, "Force loading/updating even if digest verification fails (shorthand)")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: load requires at least one skill name or subpath (e.g. 'kungfu load git/reference')")
		return ExitUsage
	}

	var targets []loadTarget
	var notFounds []string

	for _, spec := range posArgs {
		parts := strings.SplitN(spec, "/", 2)
		skillName := parts[0]
		subpath := ""
		if len(parts) == 2 {
			subpath = parts[1]
		}

		item, err := a.Store.Get(skillName)
		if err != nil {
			notFounds = append(notFounds, spec)
			continue
		}
		targets = append(targets, loadTarget{
			item:    *item,
			subpath: subpath,
		})
	}

	if len(notFounds) > 0 {
		for _, name := range notFounds {
			a.printNotFoundWithSuggestions("load", name)
		}
		return ExitNotFound
	}

	type LoadResultItem struct {
		Name        string `json:"name"`
		Subpath     string `json:"subpath,omitempty"`
		Category    string `json:"category"`
		Version     string `json:"version,omitempty"`
		Description string `json:"description"`
		Content     string `json:"content"`
		Digest      string `json:"digest,omitempty"`
		Source      string `json:"source,omitempty"`
	}

	var results []LoadResultItem

	for _, target := range targets {
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
					_, _ = fmt.Fprintf(a.Stderr, "  - Synchronize catalog:      kungfu catalog sync\n")
				}
				return MapErrorToExitCode(err)
			}
			if mode == OutputModeRaw {
				content = rawContent
			} else {
				content = stripFrontmatter(rawContent)
			}
		}

		ver := target.item.Version
		if ver == "" && target.item.Metadata != nil {
			ver = target.item.Metadata["version"]
		}

		results = append(results, LoadResultItem{
			Name:        target.item.Name,
			Subpath:     target.subpath,
			Category:    target.item.Category,
			Version:     ver,
			Description: target.item.Description,
			Content:     content,
			Digest:      target.item.Digest,
			Source:      target.item.Source,
		})

		st := a.loadState()
		st.RecordLoad(target.item.Name)
		a.saveState(st)
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		if len(results) == 1 {
			_ = enc.Encode(results[0])
		} else {
			_ = enc.Encode(results)
		}
		return ExitSuccess
	case OutputModeRaw:
		for _, r := range results {
			_, _ = fmt.Fprint(a.Stdout, r.Content)
			if !strings.HasSuffix(r.Content, "\n") {
				_, _ = fmt.Fprintln(a.Stdout)
			}
		}
		return ExitSuccess
	default: // OutputModeHuman
		for i, r := range results {
			if len(results) > 1 {
				header := fmt.Sprintf("# SKILL: %s", r.Name)
				if r.Subpath != "" {
					header += fmt.Sprintf(" (%s)", r.Subpath)
				}
				_, _ = fmt.Fprintln(a.Stdout, header)
				_, _ = fmt.Fprintln(a.Stdout)
			}
			_, _ = fmt.Fprint(a.Stdout, r.Content)
			if !strings.HasSuffix(r.Content, "\n") {
				_, _ = fmt.Fprintln(a.Stdout)
			}
			if i < len(results)-1 {
				_, _ = fmt.Fprintln(a.Stdout, "\n---")
			}
		}
		return ExitSuccess
	}
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
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			cachedMD := filepath.Join(homeDir, ".cache", "kungfu", "skills", item.Name, "SKILL.md")
			if data, err := os.ReadFile(cachedMD); err == nil && len(data) > 0 {
				return string(data), nil
			}
		}

		// If embedded, return embedded data
		if item.Source == "embedded" {
			return a.loadItemContent(item)
		}

		return "", fmt.Errorf("skill %q not found in local cache. Run 'kungfu load %s' to fetch from remote", item.Name, item.Name)
	}

	// 2. Normal load or update load
	autoUpdate := true
	if a.Config != nil {
		autoUpdate = a.Config.IsAutoUpdate()
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
		// Case A: Upstream may have updated! Check remote catalog.json to see if catalog has a new digest
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		catURL := config.DefaultCatalogURL
		if a.Config != nil && a.Config.GetCatalogURL("default") != "" {
			catURL = a.Config.GetCatalogURL("default")
		}

		freshManifest, freshETag, fetchErr := catalog.FetchHTTPCatalog(ctx, catURL, a.getHTTPClient())
		if fetchErr == nil && freshManifest != nil {
			// Find this item in fresh manifest
			var freshItem *catalog.Item
			for _, mItem := range freshManifest.Items {
				if strings.EqualFold(mItem.Name, item.Name) || strings.EqualFold(mItem.ID, item.ID) {
					it := mItem
					freshItem = &it
					break
				}
			}

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
						// Save fresh catalog cache
						cachePath := catalog.GetCatalogCachePath()
						if a.Options.CacheDir != "" {
							cachePath = filepath.Join(a.Options.CacheDir, "index.json")
						}
						_ = catalog.SaveCachedCatalog(cachePath, catURL, *freshManifest, freshETag)
						runner.CacheSkillFile(freshItem.Name, "SKILL.md", []byte(freshContent))
						*item = tempItem

						if !a.Options.NoColor && a.Options.Mode != OutputModeJSON && a.Options.Mode != OutputModeRaw {
							_, _ = fmt.Fprintf(a.Stderr, "✓ Detected upstream update for %q; cache updated.\n", item.Name)
						}
						return freshContent, nil
					} else {
						// autoUpdate is false and not forced: warn and use cached copy
						homeDir, _ := os.UserHomeDir()
						if homeDir != "" {
							cachedMD := filepath.Join(homeDir, ".cache", "kungfu", "skills", item.Name, "SKILL.md")
							if cachedData, cErr := os.ReadFile(cachedMD); cErr == nil && len(cachedData) > 0 {
								_, _ = fmt.Fprintf(a.Stderr, "warning: skill %q has an update available upstream; loading cached copy. Use 'kungfu load %s --update' to update.\n", item.Name, item.Name)
								return string(cachedData), nil
							}
						}
					}
				}
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
				resp, rErr := a.getHTTPClient().Get(remoteURL)
				if rErr == nil && resp != nil {
					defer func() { _ = resp.Body.Close() }()
					if resp.StatusCode == http.StatusOK {
						b, readErr := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
						if readErr == nil && len(b) > 0 {
							runner.CacheSkillFile(item.Name, "SKILL.md", b)
							return string(b), nil
						}
					}
				}
				_ = reqCtx
			}
		}
	}

	return "", err
}
