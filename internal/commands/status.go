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

	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, jsonFlag, rawFlag bool

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
		if len(targetNames) == 0 {
			return skills
		}
		res := make(map[string]state.SkillRecord)
		for name, rec := range skills {
			if targetNames[name] {
				res[name] = rec
			}
		}
		return res
	}

	wsInstalled = filterSkills(wsInstalled)
	wsForgotten = filterSkills(wsForgotten)
	globalInstalled = filterSkills(globalInstalled)
	globalForgotten = filterSkills(globalForgotten)

	filteredHistory := st.History
	if len(targetNames) > 0 && st.History != nil {
		filteredHistory = make(map[string]*state.HistoryRecord)
		for name, rec := range st.History {
			if targetNames[name] {
				filteredHistory[name] = rec
			}
		}
	}

	var missingTargets []string
	if len(posArgs) > 0 {
		for _, name := range posArgs {
			_, inGInst := globalInstalled[name]
			_, inGForg := globalForgotten[name]
			_, inHist := filteredHistory[name]
			inWSInst := false
			inWSForg := false
			if !globalFlag {
				_, inWSInst = wsInstalled[name]
				_, inWSForg = wsForgotten[name]
			}
			if !inGInst && !inGForg && !inHist && !inWSInst && !inWSForg {
				missingTargets = append(missingTargets, name)
			}
		}
	}

	combinedForgotten := make(map[string]state.SkillRecord)
	for k, v := range globalForgotten {
		combinedForgotten[k] = v
	}
	if !globalFlag {
		for k, v := range wsForgotten {
			combinedForgotten[k] = v
		}
	}

	type StatusOutput struct {
		Workspace       string                          `json:"workspace,omitempty"`
		WorkspaceSkills map[string]state.SkillRecord    `json:"workspace_skills,omitempty"`
		GlobalSkills    map[string]state.SkillRecord    `json:"global_skills"`
		ForgottenSkills map[string]state.SkillRecord    `json:"forgotten_skills,omitempty"`
		History         map[string]*state.HistoryRecord `json:"history,omitempty"`
	}

	out := StatusOutput{
		GlobalSkills:    globalInstalled,
		ForgottenSkills: combinedForgotten,
		History:         filteredHistory,
	}
	if !globalFlag {
		out.Workspace = wsDir
		out.WorkspaceSkills = wsInstalled
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		if len(missingTargets) > 0 {
			return ExitNotFound
		}
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
				_, _ = fmt.Fprintf(a.Stdout, "workspace\t%s\t%s\tinstalled\t%s\n",
					sanitizeTSVField(rec.Name),
					sanitizeTSVField(rec.Version),
					sanitizeTSVField(rec.Origin),
				)
			}
			var wsForgNames []string
			for n := range wsForgotten {
				wsForgNames = append(wsForgNames, n)
			}
			sort.Strings(wsForgNames)
			for _, n := range wsForgNames {
				rec := wsForgotten[n]
				_, _ = fmt.Fprintf(a.Stdout, "workspace\t%s\t%s\tforgotten\t%s\n",
					sanitizeTSVField(rec.Name),
					sanitizeTSVField(rec.Version),
					sanitizeTSVField(rec.Origin),
				)
			}
		}
		var gNames []string
		for n := range globalInstalled {
			gNames = append(gNames, n)
		}
		sort.Strings(gNames)
		for _, n := range gNames {
			rec := globalInstalled[n]
			_, _ = fmt.Fprintf(a.Stdout, "global\t%s\t%s\tinstalled\t%s\n",
				sanitizeTSVField(rec.Name),
				sanitizeTSVField(rec.Version),
				sanitizeTSVField(rec.Origin),
			)
		}
		var gForgNames []string
		for n := range globalForgotten {
			gForgNames = append(gForgNames, n)
		}
		sort.Strings(gForgNames)
		for _, n := range gForgNames {
			rec := globalForgotten[n]
			_, _ = fmt.Fprintf(a.Stdout, "global\t%s\t%s\tforgotten\t%s\n",
				sanitizeTSVField(rec.Name),
				sanitizeTSVField(rec.Version),
				sanitizeTSVField(rec.Origin),
			)
		}
		if len(missingTargets) > 0 {
			return ExitNotFound
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
				var wsForgNames []string
				for n := range wsForgotten {
					wsForgNames = append(wsForgNames, n)
				}
				sort.Strings(wsForgNames)
				for _, n := range wsForgNames {
					rec := wsForgotten[n]
					uninst := ""
					if rec.UninstalledAt != nil {
						uninst = rec.UninstalledAt.Format("2006-01-02")
					}
					_, _ = fmt.Fprintf(a.Stdout, "  ✗ %-20s (workspace, uninstalled %s)\n", rec.Name, uninst)
				}
			}
			var gForgNames []string
			for n := range globalForgotten {
				gForgNames = append(gForgNames, n)
			}
			sort.Strings(gForgNames)
			for _, n := range gForgNames {
				rec := globalForgotten[n]
				uninst := ""
				if rec.UninstalledAt != nil {
					uninst = rec.UninstalledAt.Format("2006-01-02")
				}
				_, _ = fmt.Fprintf(a.Stdout, "  ✗ %-20s (global, uninstalled %s)\n", rec.Name, uninst)
			}
		}

		// Skill Usage History
		if len(filteredHistory) > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "\nSkill Usage History:\n")
			var histNames []string
			for n := range filteredHistory {
				histNames = append(histNames, n)
			}
			sort.Strings(histNames)
			for _, n := range histNames {
				h := filteredHistory[n]
				details := fmt.Sprintf("%d loads", h.LoadCount)
				if h.PageLoadCount > 0 {
					details += fmt.Sprintf(", +%d pages", h.PageLoadCount)
				}
				if h.SubpathLoadCount > 0 {
					details += fmt.Sprintf(", +%d subpaths", h.SubpathLoadCount)
				}
				if h.RunCount > 0 {
					details += fmt.Sprintf(", %d runs", h.RunCount)
				}
				if h.LearnCount > 0 {
					details += fmt.Sprintf(", %d learns", h.LearnCount)
				}
				_, _ = fmt.Fprintf(a.Stdout, "  • %-20s (%s, %s)\n", n, h.LastSeenAt.Format("2006-01-02 15:04"), details)
			}
		}

		if len(missingTargets) > 0 {
			for _, name := range missingTargets {
				_, _ = fmt.Fprintf(a.Stderr, "⚠ Skill %q has no installation or usage history.\n", name)
			}
			return ExitNotFound
		}

		return ExitSuccess
	}
}
