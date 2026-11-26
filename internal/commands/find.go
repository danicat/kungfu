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
	"flag"
	"fmt"
	"strings"

	"github.com/danicat/kungfu/internal/search"
)

func (a *App) RunFind(args []string) int {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var limit int
	var jsonFlag bool

	fs.IntVar(&limit, "limit", 10, "Maximum number of search results to return")
	fs.IntVar(&limit, "n", 10, "Maximum number of search results (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: find requires search terms")
		return ExitUsage
	}
	if limit < 1 {
		_, _ = fmt.Fprintf(a.Stderr, "error: invalid limit %d: limit must be >= 1\n", limit)
		return ExitUsage
	}

	query := strings.Join(posArgs, " ")
	mode := a.resolveMode(jsonFlag)

	// If query scopes to @catalog, fetch or refresh that configured catalog on demand.
	for tok := range strings.FieldsSeq(query) {
		if idx := strings.LastIndex(tok, "@"); idx != -1 && idx < len(tok)-1 {
			catQual := strings.TrimSpace(tok[idx+1:])
			if catQual != "" {
				if err := a.ensureCatalogFresh(catQual); err != nil {
					_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", err)
					return MapErrorToExitCode(err)
				}
			}
		}
	}
	a.warnStaleCatalogs()

	allItems := a.Store.All()

	if len(allItems) == 0 {
		if mode == OutputModeJSON {
			_, _ = fmt.Fprintln(a.Stdout, "[]")
		} else {
			_, _ = fmt.Fprintln(a.Stderr, "error: no skills loaded in catalog to search")
		}
		return ExitNotFound
	}

	if a.Searcher == nil {
		a.Searcher = search.NewEngine()
	}
	if err := a.Searcher.Index(allItems); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: indexing catalog: %v\n", err)
		return ExitGeneral
	}

	const strategy = "hybrid"
	results := a.Searcher.Search(query, strategy, limit, 0.0)

	if len(results) == 0 {
		if mode == OutputModeJSON {
			_, _ = fmt.Fprintln(a.Stdout, "[]")
		} else {
			a.printNotFoundWithSuggestions("find", query)
		}
		return ExitNotFound
	}

	type FindResultItem struct {
		Name        string   `json:"name"`
		Source      string   `json:"source,omitempty"`
		Version     string   `json:"version,omitempty"`
		Description string   `json:"description"`
		Score       float32  `json:"score"`
		Strategy    string   `json:"strategy"`
		MatchFields []string `json:"match_fields,omitempty"`
		Tags        []string `json:"tags,omitempty"`
		Status      string   `json:"status"`
	}

	nameCounts := make(map[string]int, len(results))
	for _, r := range results {
		nameCounts[strings.ToLower(r.Item.Name)]++
	}

	outputItems := make([]FindResultItem, 0, len(results))
	lookup := a.newSkillStatusLookup(a.loadStateReadOnly())
	for _, r := range results {
		outputItems = append(outputItems, FindResultItem{
			Name:        r.Item.Name,
			Source:      r.Item.Source,
			Version:     itemVersion(r.Item),
			Description: r.Item.Description,
			Score:       r.Score,
			Strategy:    r.Strategy,
			MatchFields: r.MatchFields,
			Tags:        r.Item.Tags,
			Status:      lookup.status(&r.Item),
		})
	}

	if mode == OutputModeJSON {
		if err := writeJSON(a.Stdout, outputItems); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
		return ExitSuccess
	}

	_, _ = fmt.Fprintf(a.Stdout, "SEARCH RESULTS FOR %q (%s, %d matches):\n\n", query, strategy, len(outputItems))
	_, _ = fmt.Fprintf(a.Stdout, "%-24s %-8s %-12s %s\n", "NAME", "SCORE", "STATUS", "DESCRIPTION")
	_, _ = fmt.Fprintf(a.Stdout, "%-24s %-8s %-12s %s\n", "------------------------", "--------", "------------", "--------------------------------------------------")
	for _, o := range outputItems {
		displayName := o.Name
		if nameCounts[strings.ToLower(o.Name)] > 1 && o.Source != "" {
			displayName = o.Name + "@" + o.Source
		}
		statusBadge := "[" + o.Status + "]"
		desc := truncateRunes(singleLineDesc(o.Description), 55)
		_, _ = fmt.Fprintf(a.Stdout, "%-24s %-8.3f %-12s %s\n", displayName, o.Score, statusBadge, desc)
	}
	return ExitSuccess
}
