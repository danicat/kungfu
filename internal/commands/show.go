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
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/danicat/kungfu/internal/catalog"
)

func (a *App) RunShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag bool

	fs.StringVar(&category, "category", "", "Filter skills by category")
	fs.StringVar(&category, "c", "", "Filter skills by category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Filter skills by boolean tag query")
	fs.StringVar(&tagQuery, "t", "", "Filter skills by boolean tag query (shorthand)")
	fs.StringVar(&tagQuery, "tag", "", "Filter skills by tag (alias)")
	fs.BoolVar(&allFlag, "all", false, "Show all skills across categories")
	fs.BoolVar(&allFlag, "a", false, "Show all skills across categories (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	if allFlag || (category != "" || tagQuery != "") && len(posArgs) == 0 {
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
			a.printFilterNotFoundWithSuggestions("show", category, tagQuery)
			return ExitNotFound
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].Name < items[j].Name
		})
		return a.renderShowItems(items, mode)
	}

	if len(posArgs) == 0 {
		fmt.Fprintln(a.Stderr, "error: show requires at least one skill name, or use --all / -c / -t")
		return ExitUsage
	}

	var items []catalog.Item
	var notFounds []string

	for _, name := range posArgs {
		item, err := a.Store.Get(name)
		if err != nil {
			notFounds = append(notFounds, name)
			continue
		}
		items = append(items, *item)
	}

	if len(notFounds) > 0 {
		for _, name := range notFounds {
			a.printNotFoundWithSuggestions("show", name)
		}
		return ExitNotFound
	}

	return a.renderShowItems(items, mode)
}

func (a *App) renderShowItems(items []catalog.Item, mode OutputMode) int {
	type ShowDetail struct {
		Name         string            `json:"name"`
		Title        string            `json:"title,omitempty"`
		Category     string            `json:"category"`
		Version      string            `json:"version,omitempty"`
		Description  string            `json:"description"`
		Author       string            `json:"author,omitempty"`
		License      string            `json:"license,omitempty"`
		Status       string            `json:"status"`
		Tags         []string          `json:"tags,omitempty"`
		AllowedTools string            `json:"allowed_tools,omitempty"`
		References   []string          `json:"references,omitempty"`
		Scripts      []string          `json:"scripts,omitempty"`
		Digest       string            `json:"digest,omitempty"`
		Sha256       string            `json:"sha256,omitempty"`
		URL          string            `json:"url,omitempty"`
		Source       string            `json:"source,omitempty"`
		Metadata     map[string]string `json:"metadata,omitempty"`
	}

	var details []ShowDetail
	for _, it := range items {
		refs := a.listReferences(it)
		scripts := a.listScripts(it)

		ver := it.Version
		if ver == "" && it.Metadata != nil {
			ver = it.Metadata["version"]
		}

		author := it.Author
		if author == "" && it.Metadata != nil {
			author = it.Metadata["author"]
		}

		details = append(details, ShowDetail{
			Name:         it.Name,
			Title:        it.Title,
			Category:     it.Category,
			Version:      ver,
			Description:  it.Description,
			Author:       author,
			License:      it.License,
			Status:       a.getSkillStatus(&it),
			Tags:         it.Tags,
			AllowedTools: it.AllowedTools,
			References:   refs,
			Scripts:      scripts,
			Digest:       it.Digest,
			Sha256:       it.Sha256,
			URL:          it.URL,
			Source:       it.Source,
			Metadata:     it.Metadata,
		})
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		if len(details) == 1 {
			_ = enc.Encode(details[0])
		} else {
			_ = enc.Encode(details)
		}
		return ExitSuccess
	case OutputModeRaw:
		for _, d := range details {
			tagsStr := strings.Join(d.Tags, ",")
			refsStr := strings.Join(d.References, ",")
			scriptsStr := strings.Join(d.Scripts, ",")
			fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Name, d.Category, d.Version, d.Status, tagsStr, d.AllowedTools, refsStr, scriptsStr, d.Description)
		}
		return ExitSuccess
	default: // OutputModeHuman
		for i, d := range details {
			if i > 0 {
				fmt.Fprintln(a.Stdout)
				fmt.Fprintln(a.Stdout, "--------------------------------------------------------------------------------")
				fmt.Fprintln(a.Stdout)
			}
			fmt.Fprintf(a.Stdout, "SKILL: %s\n", d.Name)
			if d.Title != "" && d.Title != d.Name {
				fmt.Fprintf(a.Stdout, "Title:        %s\n", d.Title)
			}
			fmt.Fprintf(a.Stdout, "Category:     %s\n", d.Category)
			if d.Version != "" {
				fmt.Fprintf(a.Stdout, "Version:      %s\n", d.Version)
			}
			fmt.Fprintf(a.Stdout, "Status:       [%s]\n", d.Status)
			if d.Author != "" {
				fmt.Fprintf(a.Stdout, "Author:       %s\n", d.Author)
			}
			if d.License != "" {
				fmt.Fprintf(a.Stdout, "License:      %s\n", d.License)
			}
			if len(d.Tags) > 0 {
				fmt.Fprintf(a.Stdout, "Tags:         %s\n", strings.Join(d.Tags, ", "))
			}
			if d.AllowedTools != "" {
				fmt.Fprintf(a.Stdout, "Tools:        %s\n", d.AllowedTools)
			}
			if len(d.References) > 0 {
				fmt.Fprintf(a.Stdout, "References:   %s\n", strings.Join(d.References, ", "))
			}
			if len(d.Scripts) > 0 {
				fmt.Fprintf(a.Stdout, "Scripts:      %s\n", strings.Join(d.Scripts, ", "))
			}
			if d.URL != "" {
				fmt.Fprintf(a.Stdout, "URL:          %s\n", d.URL)
			}
			if d.Digest != "" {
				fmt.Fprintf(a.Stdout, "Digest:       %s\n", d.Digest)
			}
			fmt.Fprintf(a.Stdout, "Description:\n  %s\n", d.Description)
		}
		return ExitSuccess
	}
}
