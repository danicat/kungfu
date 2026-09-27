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
	"strings"
)

func (a *App) RunFind(args []string) int {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var limit int
	var jsonFlag, rawFlag bool

	fs.IntVar(&limit, "limit", 10, "Maximum number of search results to return")
	fs.IntVar(&limit, "n", 10, "Maximum number of search results (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: find requires search terms")
		return ExitUsage
	}

	query := strings.Join(posArgs, " ")
	allItems := a.Store.All()

	if len(allItems) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: no skills loaded in catalog to search")
		return ExitNotFound
	}

	_ = a.Searcher.Index(allItems)
	const strategy = "hybrid"
	results := a.Searcher.Search(query, strategy, limit, 0.0)

	mode := a.resolveMode(jsonFlag, rawFlag)

	if len(results) == 0 {
		if mode == OutputModeJSON {
			_, _ = fmt.Fprintln(a.Stdout, "[]")
			return ExitSuccess
		}
		if mode == OutputModeRaw {
			return ExitSuccess
		}
		a.printNotFoundWithSuggestions("find", query)
		return ExitNotFound
	}

	type FindResultItem struct {
		Name        string   `json:"name"`
		Category    string   `json:"category"`
		Description string   `json:"description"`
		Score       float32  `json:"score"`
		Strategy    string   `json:"strategy"`
		MatchFields []string `json:"match_fields,omitempty"`
		Tags        []string `json:"tags,omitempty"`
		Status      string   `json:"status"`
	}

	var outputItems []FindResultItem
	for _, r := range results {
		outputItems = append(outputItems, FindResultItem{
			Name:        r.Item.Name,
			Category:    r.Item.Category,
			Description: r.Item.Description,
			Score:       r.Score,
			Strategy:    r.Strategy,
			MatchFields: r.MatchFields,
			Tags:        r.Item.Tags,
			Status:      a.getSkillStatus(&r.Item),
		})
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(outputItems)
		return ExitSuccess
	case OutputModeRaw:
		for _, o := range outputItems {
			_, _ = fmt.Fprintf(a.Stdout, "%s\t%s\t%.4f\t%s\t%s\n", o.Name, o.Category, o.Score, o.Status, o.Description)
		}
		return ExitSuccess
	default: // OutputModeHuman
		_, _ = fmt.Fprintf(a.Stdout, "SEARCH RESULTS FOR %q (%s, %d matches):\n\n", query, strategy, len(outputItems))
		_, _ = fmt.Fprintf(a.Stdout, "%-22s %-14s %-8s %-12s %s\n", "NAME", "CATEGORY", "SCORE", "STATUS", "DESCRIPTION")
		_, _ = fmt.Fprintf(a.Stdout, "%-22s %-14s %-8s %-12s %s\n", "----------------------", "--------------", "--------", "------------", "--------------------------------------------------")
		for _, o := range outputItems {
			statusBadge := "[" + o.Status + "]"
			desc := o.Description
			if len(desc) > 50 {
				desc = desc[:47] + "..."
			}
			_, _ = fmt.Fprintf(a.Stdout, "%-22s %-14s %-8.3f %-12s %s\n", o.Name, o.Category, o.Score, statusBadge, desc)
		}
		return ExitSuccess
	}
}
