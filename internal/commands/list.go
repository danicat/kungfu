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
	"sort"
	"strings"
)

func (a *App) RunList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag bool

	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(posArgs) > 0 {
		_, _ = fmt.Fprintf(a.Stderr, "error: list does not accept positional arguments (got %q)\n", posArgs[0])
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag)
	a.warnStaleCatalogs()
	items := a.Store.All()

	if len(items) == 0 {
		if mode == OutputModeJSON {
			_, _ = fmt.Fprintln(a.Stdout, "[]")
			return ExitSuccess
		}
		_, _ = fmt.Fprintln(a.Stdout, "No skills available.")
		return ExitSuccess
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].Source < items[j].Source
	})

	type SkillListing struct {
		Name         string   `json:"name"`
		Source       string   `json:"source,omitempty"`
		Description  string   `json:"description"`
		Tags         []string `json:"tags,omitempty"`
		Version      string   `json:"version,omitempty"`
		Status       string   `json:"status"`
		AllowedTools string   `json:"allowed_tools,omitempty"`
	}

	nameCounts := make(map[string]int, len(items))
	for _, it := range items {
		nameCounts[strings.ToLower(it.Name)]++
	}

	listings := make([]SkillListing, 0, len(items))
	lookup := a.newSkillStatusLookup(a.loadStateReadOnly())
	for _, it := range items {
		listings = append(listings, SkillListing{
			Name:         it.Name,
			Source:       it.Source,
			Description:  it.Description,
			Tags:         it.Tags,
			Version:      itemVersion(it),
			Status:       lookup.status(&it),
			AllowedTools: it.AllowedTools,
		})
	}

	if mode == OutputModeJSON {
		if err := writeJSON(a.Stdout, listings); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
		return ExitSuccess
	}

	_, _ = fmt.Fprintf(a.Stdout, "AVAILABLE SKILLS (%d total):\n\n", len(listings))
	_, _ = fmt.Fprintf(a.Stdout, "%-24s %-12s %s\n", "NAME", "STATUS", "DESCRIPTION")
	_, _ = fmt.Fprintf(a.Stdout, "%-24s %-12s %s\n", "------------------------", "------------", "--------------------------------------------------")
	for _, l := range listings {
		displayName := l.Name
		if nameCounts[strings.ToLower(l.Name)] > 1 && l.Source != "" {
			displayName = l.Name + "@" + l.Source
		}
		statusBadge := "[" + l.Status + "]"
		desc := truncateRunes(singleLineDesc(l.Description), 65)
		_, _ = fmt.Fprintf(a.Stdout, "%-24s %-12s %s\n", displayName, statusBadge, desc)
	}
	return ExitSuccess
}
