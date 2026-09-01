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

func (a *App) RunList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var category, tagQuery string
	var all, jsonFlag, rawFlag bool

	fs.StringVar(&category, "category", "", "Filter skills by category")
	fs.StringVar(&category, "c", "", "Filter skills by category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Filter skills by boolean tag query (e.g. 'search AND NOT python')")
	fs.StringVar(&tagQuery, "t", "", "Filter skills by boolean tag query (shorthand)")
	fs.StringVar(&tagQuery, "tag", "", "Filter skills by tag (alias)")
	fs.BoolVar(&all, "all", false, "Show all skills across categories")
	fs.BoolVar(&all, "a", false, "Show all skills across categories (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	if len(posArgs) > 0 && category == "" {
		category = posArgs[0]
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	// Mode 1: No filters and not --all -> show category overview
	if category == "" && tagQuery == "" && !all {
		categories := a.Store.Categories()
		sort.Slice(categories, func(i, j int) bool {
			return categories[i].ID < categories[j].ID
		})

		switch mode {
		case OutputModeJSON:
			enc := json.NewEncoder(a.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(categories)
			return ExitSuccess
		case OutputModeRaw:
			for _, cat := range categories {
				fmt.Fprintf(a.Stdout, "%s\t%d\t%s\n", cat.ID, cat.Count, cat.Description)
			}
			return ExitSuccess
		default: // OutputModeHuman
			fmt.Fprintln(a.Stdout, "AVAILABLE SKILL CATEGORIES:")
			fmt.Fprintln(a.Stdout)
			fmt.Fprintf(a.Stdout, "%-16s %-8s %s\n", "CATEGORY", "COUNT", "DESCRIPTION")
			fmt.Fprintf(a.Stdout, "%-16s %-8s %s\n", "----------------", "--------", "--------------------------------------------------")
			for _, cat := range categories {
				fmt.Fprintf(a.Stdout, "%-16s %-8d %s\n", cat.ID, cat.Count, cat.Description)
			}
			fmt.Fprintln(a.Stdout)
			fmt.Fprintln(a.Stdout, "Use 'kungfu list <category>' or 'kungfu list --all' to inspect skills.")
			return ExitSuccess
		}
	}

	// Mode 2: Filtered items or --all
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
		a.printFilterNotFoundWithSuggestions("list", category, tagQuery)
		return ExitNotFound
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})

	type SkillListing struct {
		Name         string   `json:"name"`
		Category     string   `json:"category"`
		Description  string   `json:"description"`
		Tags         []string `json:"tags,omitempty"`
		Version      string   `json:"version,omitempty"`
		Status       string   `json:"status"`
		AllowedTools string   `json:"allowed_tools,omitempty"`
	}

	var listings []SkillListing
	for _, it := range items {
		listings = append(listings, SkillListing{
			Name:         it.Name,
			Category:     it.Category,
			Description:  it.Description,
			Tags:         it.Tags,
			Version:      it.Version,
			Status:       a.getSkillStatus(&it),
			AllowedTools: it.AllowedTools,
		})
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(listings)
		return ExitSuccess
	case OutputModeRaw:
		for _, l := range listings {
			tagsStr := strings.Join(l.Tags, ",")
			fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%s\t%s\n", l.Name, l.Category, l.Status, tagsStr, l.Description)
		}
		return ExitSuccess
	default: // OutputModeHuman
		title := "AVAILABLE SKILLS"
		if category != "" {
			title = fmt.Sprintf("SKILLS IN CATEGORY %q", category)
		}
		if tagQuery != "" {
			title += fmt.Sprintf(" (tags: %s)", tagQuery)
		}
		fmt.Fprintf(a.Stdout, "%s (%d total):\n\n", title, len(listings))
		fmt.Fprintf(a.Stdout, "%-22s %-14s %-12s %s\n", "NAME", "CATEGORY", "STATUS", "DESCRIPTION")
		fmt.Fprintf(a.Stdout, "%-22s %-14s %-12s %s\n", "----------------------", "--------------", "------------", "--------------------------------------------------")
		for _, l := range listings {
			statusBadge := "[" + l.Status + "]"
			desc := l.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			fmt.Fprintf(a.Stdout, "%-22s %-14s %-12s %s\n", l.Name, l.Category, statusBadge, desc)
		}
		return ExitSuccess
	}
}
