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
	"sort"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
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
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag, noStripFlag bool

	fs.StringVar(&category, "category", "", "Filter skills by category")
	fs.StringVar(&category, "c", "", "Filter skills by category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Filter skills by boolean tag query")
	fs.StringVar(&tagQuery, "t", "", "Filter skills by boolean tag query (shorthand)")
	fs.StringVar(&tagQuery, "tag", "", "Filter skills by tag (alias)")
	fs.BoolVar(&allFlag, "all", false, "Load all skills across categories")
	fs.BoolVar(&allFlag, "a", false, "Load all skills across categories (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output loaded content as JSON")
	fs.BoolVar(&rawFlag, "raw", false, "Output raw verbatim markdown content without extra formatting")
	fs.BoolVar(&noStripFlag, "no-strip", false, "Preserve YAML frontmatter in instructions")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	var targets []loadTarget
	var notFounds []string

	if allFlag || category != "" || tagQuery != "" {
		items := a.Store.ListByFilter(catalog.QueryFilter{
			Category: category,
			TagQuery: tagQuery,
		})
		if len(items) == 0 {
			if mode == OutputModeJSON {
				fmt.Fprintln(a.Stdout, "[]")
				return ExitSuccess
			}
			if mode == OutputModeRaw {
				return ExitSuccess
			}
			a.printFilterNotFoundWithSuggestions("load", category, tagQuery)
			return ExitNotFound
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].Name < items[j].Name
		})
		for _, it := range items {
			targets = append(targets, loadTarget{item: it})
		}
	} else {
		if len(posArgs) == 0 {
			fmt.Fprintln(a.Stderr, "error: load requires at least one skill name or subpath (e.g. 'kungfu load git/reference'), or use --all / -c / -t")
			return ExitUsage
		}

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
					fmt.Fprintf(a.Stderr, "error: invalid path traversal in %q\n", target.subpath)
					return ExitUsage
				}
				if errors.Is(loadErr, runner.ErrFileNotFound) {
					fmt.Fprintf(a.Stderr, "error: subpath %q not found in skill %q.\n", target.subpath, target.item.Name)
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
							fmt.Fprintln(a.Stderr, "\nDid you mean:")
							for _, s := range suggs {
								fmt.Fprintf(a.Stderr, "  - kungfu load %s/%s\n", target.item.Name, s)
							}
						}
					}
					return ExitNotFound
				}
				fmt.Fprintf(a.Stderr, "error: loading %s/%s: %v\n", target.item.Name, target.subpath, loadErr)
				return MapErrorToExitCode(loadErr)
			}
		} else {
			rawContent, err := a.loadItemContent(&target.item)
			if err != nil {
				fmt.Fprintf(a.Stderr, "error: loading skill %q: %v\n", target.item.Name, err)
				return MapErrorToExitCode(err)
			}
			if noStripFlag {
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

		// Track JIT load count in state history ONLY for explicitly targeted single loads
		if !allFlag && category == "" && tagQuery == "" {
			st := a.loadState()
			st.RecordLoad(target.item.Name)
			a.saveState(st)
		}
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
			fmt.Fprint(a.Stdout, r.Content)
			if !strings.HasSuffix(r.Content, "\n") {
				fmt.Fprintln(a.Stdout)
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
				fmt.Fprintln(a.Stdout, header)
				fmt.Fprintln(a.Stdout)
			}
			fmt.Fprint(a.Stdout, r.Content)
			if !strings.HasSuffix(r.Content, "\n") {
				fmt.Fprintln(a.Stdout)
			}
			if i < len(results)-1 {
				fmt.Fprintln(a.Stdout, "\n---")
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
