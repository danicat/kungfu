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
	"os"
	"sort"
	"strings"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, jsonFlag, rawFlag bool
	var categoryFlag, tagsFlag string

	fs.StringVar(&categoryFlag, "category", "", "Filter status by category")
	fs.StringVar(&categoryFlag, "c", "", "Filter status by category (shorthand)")
	fs.StringVar(&tagsFlag, "tags", "", "Filter status by boolean tag expression")
	fs.StringVar(&tagsFlag, "t", "", "Filter status by boolean tag expression (shorthand)")
	fs.StringVar(&tagsFlag, "tag", "", "Filter status by tag (alias)")
	fs.BoolVar(&globalFlag, "global", false, "Show global skills status only")
	fs.BoolVar(&globalFlag, "g", false, "Show global skills status only (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	st := a.loadState()
	wsDir, _ := os.Getwd()

	wsInstalled := st.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	wsForgotten := st.GetForgottenSkills(state.ScopeWorkspace, wsDir)
	globalInstalled := st.GetInstalledSkills(state.ScopeGlobal, "")
	globalForgotten := st.GetForgottenSkills(state.ScopeGlobal, "")

	targetNames := make(map[string]bool)
	for _, name := range posArgs {
		targetNames[name] = true
	}

	filterSkills := func(skills map[string]state.SkillRecord) map[string]state.SkillRecord {
		if len(targetNames) == 0 && categoryFlag == "" && tagsFlag == "" {
			return skills
		}
		res := make(map[string]state.SkillRecord)
		for name, rec := range skills {
			if len(targetNames) > 0 && !targetNames[name] {
				continue
			}
			if categoryFlag != "" || tagsFlag != "" {
				item, err := a.Store.Get(name)
				if err != nil {
					if categoryFlag != "" {
						continue
					}
				} else {
					if categoryFlag != "" && !strings.EqualFold(item.Category, categoryFlag) {
						continue
					}
					if tagsFlag != "" && !catalog.MatchTags(item.Tags, tagsFlag) {
						continue
					}
				}
			}
			res[name] = rec
		}
		return res
	}

	wsInstalled = filterSkills(wsInstalled)
	wsForgotten = filterSkills(wsForgotten)
	globalInstalled = filterSkills(globalInstalled)
	globalForgotten = filterSkills(globalForgotten)

	type StatusOutput struct {
		Workspace       string                          `json:"workspace,omitempty"`
		WorkspaceSkills map[string]state.SkillRecord    `json:"workspace_skills,omitempty"`
		GlobalSkills    map[string]state.SkillRecord    `json:"global_skills"`
		ForgottenSkills map[string]state.SkillRecord    `json:"forgotten_skills,omitempty"`
		History         map[string]*state.HistoryRecord `json:"history,omitempty"`
	}

	out := StatusOutput{
		GlobalSkills: globalInstalled,
		History:      st.History,
	}
	if !globalFlag {
		out.Workspace = wsDir
		out.WorkspaceSkills = wsInstalled
		out.ForgottenSkills = wsForgotten
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return ExitSuccess
	case OutputModeRaw:
		if !globalFlag {
			var wsNames []string
			for n := range wsInstalled {
				wsNames = append(wsNames, n)
			}
			sort.Strings(wsNames)
			for _, n := range wsNames {
				rec := wsInstalled[n]
				_, _ = fmt.Fprintf(a.Stdout, "workspace\t%s\t%s\tinstalled\t%s\n", rec.Name, rec.Version, rec.Origin)
			}
			for _, rec := range wsForgotten {
				_, _ = fmt.Fprintf(a.Stdout, "workspace\t%s\t%s\tforgotten\t%s\n", rec.Name, rec.Version, rec.Origin)
			}
		}
		var gNames []string
		for n := range globalInstalled {
			gNames = append(gNames, n)
		}
		sort.Strings(gNames)
		for _, n := range gNames {
			rec := globalInstalled[n]
			_, _ = fmt.Fprintf(a.Stdout, "global\t%s\t%s\tinstalled\t%s\n", rec.Name, rec.Version, rec.Origin)
		}
		for _, rec := range globalForgotten {
			_, _ = fmt.Fprintf(a.Stdout, "global\t%s\t%s\tforgotten\t%s\n", rec.Name, rec.Version, rec.Origin)
		}
		return ExitSuccess
	default: // OutputModeHuman
		_, _ = fmt.Fprintf(a.Stdout, "KungFu Status\n")
		if !globalFlag {
			_, _ = fmt.Fprintf(a.Stdout, "Workspace: %s\n\n", wsDir)

			// Workspace Installed
			_, _ = fmt.Fprintf(a.Stdout, "Workspace Skills (%d):\n", len(wsInstalled))
			if len(wsInstalled) == 0 {
				_, _ = fmt.Fprintf(a.Stdout, "  (none)\n")
			} else {
				var names []string
				for n := range wsInstalled {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					rec := wsInstalled[n]
					ver := rec.Version
					if ver == "" {
						ver = "unversioned"
					}
					if rec.Origin == state.OriginCustom {
						_, _ = fmt.Fprintf(a.Stdout, "  • %-20s %-12s (%d files, custom - read-only to kungfu)\n", rec.Name, ver, len(rec.Files))
					} else {
						_, _ = fmt.Fprintf(a.Stdout, "  ✓ %-20s %-12s (%d files, installed %s)\n", rec.Name, ver, len(rec.Files), rec.InstalledAt.Format("2006-01-02"))
					}
				}
			}
		}

		// Global Installed
		_, _ = fmt.Fprintf(a.Stdout, "\nGlobal Skills (%d):\n", len(globalInstalled))
		if len(globalInstalled) == 0 {
			_, _ = fmt.Fprintf(a.Stdout, "  (none)\n")
		} else {
			var names []string
			for n := range globalInstalled {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				rec := globalInstalled[n]
				ver := rec.Version
				if ver == "" {
					ver = "unversioned"
				}
				if rec.Origin == state.OriginCustom {
					_, _ = fmt.Fprintf(a.Stdout, "  • %-20s %-12s (%d files, custom - read-only to kungfu)\n", rec.Name, ver, len(rec.Files))
				} else {
					_, _ = fmt.Fprintf(a.Stdout, "  ✓ %-20s %-12s (%d files, installed %s)\n", rec.Name, ver, len(rec.Files), rec.InstalledAt.Format("2006-01-02"))
				}
			}
		}

		// Forgotten
		forgottenCount := len(globalForgotten)
		if !globalFlag {
			forgottenCount += len(wsForgotten)
		}
		if forgottenCount > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "\nForgotten Skills:\n")
			if !globalFlag {
				for _, rec := range wsForgotten {
					uninst := ""
					if rec.UninstalledAt != nil {
						uninst = rec.UninstalledAt.Format("2006-01-02")
					}
					_, _ = fmt.Fprintf(a.Stdout, "  ✗ %-20s (workspace, uninstalled %s)\n", rec.Name, uninst)
				}
			}
			for _, rec := range globalForgotten {
				uninst := ""
				if rec.UninstalledAt != nil {
					uninst = rec.UninstalledAt.Format("2006-01-02")
				}
				_, _ = fmt.Fprintf(a.Stdout, "  ✗ %-20s (global, uninstalled %s)\n", rec.Name, uninst)
			}
		}

		// Recent JIT Loads
		if len(st.History) > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "\nRecent JIT Loads:\n")
			var histNames []string
			for n := range st.History {
				histNames = append(histNames, n)
			}
			sort.Strings(histNames)
			for _, n := range histNames {
				h := st.History[n]
				_, _ = fmt.Fprintf(a.Stdout, "  • %-20s (last seen %s, loaded %d times)\n", n, h.LastSeenAt.Format("2006-01-02 15:04"), h.LoadCount)
			}
		}

		return ExitSuccess
	}
}
