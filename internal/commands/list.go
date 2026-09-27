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
)

func (a *App) RunList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag bool

	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	_, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	items := a.Store.All()

	if len(items) == 0 {
		if mode == OutputModeJSON {
			_, _ = fmt.Fprintln(a.Stdout, "[]")
			return ExitSuccess
		}
		if mode == OutputModeRaw {
			return ExitSuccess
		}
		_, _ = fmt.Fprintln(a.Stderr, "error: no skills found in catalog")
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
			_, _ = fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%s\t%s\n", l.Name, l.Category, l.Status, tagsStr, l.Description)
		}
		return ExitSuccess
	default: // OutputModeHuman
		_, _ = fmt.Fprintf(a.Stdout, "AVAILABLE SKILLS (%d total):\n\n", len(listings))
		_, _ = fmt.Fprintf(a.Stdout, "%-22s %-14s %-12s %s\n", "NAME", "CATEGORY", "STATUS", "DESCRIPTION")
		_, _ = fmt.Fprintf(a.Stdout, "%-22s %-14s %-12s %s\n", "----------------------", "--------------", "------------", "--------------------------------------------------")
		for _, l := range listings {
			statusBadge := "[" + l.Status + "]"
			desc := l.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			_, _ = fmt.Fprintf(a.Stdout, "%-22s %-14s %-12s %s\n", l.Name, l.Category, statusBadge, desc)
		}
		return ExitSuccess
	}
}
