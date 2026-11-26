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
	"os"
	"sort"

	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, jsonFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Show global skills status only")
	fs.BoolVar(&globalFlag, "g", false, "Show global skills status only (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	wsDir, wdErr := os.Getwd()
	if !globalFlag && wdErr != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: unable to determine working directory: %v\n", wdErr)
		return ExitGeneral
	}

	st := a.loadStateReconciled()

	wsInstalled := st.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	globalInstalled := st.GetInstalledSkills(state.ScopeGlobal, "")

	targetNames := make(map[string]bool, len(posArgs))
	for _, name := range posArgs {
		targetNames[name] = true
	}

	filterSkills := func(skills map[string]state.SkillRecord) map[string]state.SkillRecord {
		if len(targetNames) == 0 {
			if skills == nil {
				return make(map[string]state.SkillRecord)
			}
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
	globalInstalled = filterSkills(globalInstalled)

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
			_, inHist := filteredHistory[name]
			inWSInst := false
			if !globalFlag {
				_, inWSInst = wsInstalled[name]
			}
			if !inGInst && !inHist && !inWSInst {
				missingTargets = append(missingTargets, name)
			}
		}
	}

	type StatusOutput struct {
		Workspace       string                          `json:"workspace,omitempty"`
		WorkspaceSkills map[string]state.SkillRecord    `json:"workspace_skills,omitempty"`
		GlobalSkills    map[string]state.SkillRecord    `json:"global_skills"`
		History         map[string]*state.HistoryRecord `json:"history,omitempty"`
	}

	out := StatusOutput{
		GlobalSkills: globalInstalled,
		History:      filteredHistory,
	}
	if !globalFlag {
		out.Workspace = wsDir
		out.WorkspaceSkills = wsInstalled
	}

	mode := a.resolveMode(jsonFlag)
	if mode == OutputModeJSON {
		if err := writeJSON(a.Stdout, out); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
		if len(missingTargets) > 0 {
			return ExitNotFound
		}
		return ExitSuccess
	}

	_, _ = fmt.Fprintf(a.Stdout, "KungFu Status\n")
	if !globalFlag {
		_, _ = fmt.Fprintf(a.Stdout, "Workspace: %s\n\n", wsDir)
		_, _ = fmt.Fprintf(a.Stdout, "Workspace Skills (%d):\n", len(wsInstalled))
		printInstalledScope(a.Stdout, wsInstalled)
	}

	_, _ = fmt.Fprintf(a.Stdout, "\nGlobal Skills (%d):\n", len(globalInstalled))
	printInstalledScope(a.Stdout, globalInstalled)

	if len(filteredHistory) > 0 {
		_, _ = fmt.Fprintf(a.Stdout, "\nSkill Usage History:\n")
		histNames := make([]string, 0, len(filteredHistory))
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

func printInstalledScope(w interface{ Write([]byte) (int, error) }, skills map[string]state.SkillRecord) {
	if len(skills) == 0 {
		_, _ = fmt.Fprintf(w, "  (none)\n")
		return
	}
	names := make([]string, 0, len(skills))
	for n := range skills {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rec := skills[n]
		ver := rec.Version
		if ver == "" {
			ver = "unversioned"
		}
		if rec.Origin == state.OriginCustom {
			_, _ = fmt.Fprintf(w, "  • %-20s %-12s (%d files, custom - read-only to kungfu)\n", rec.Name, ver, len(rec.Files))
		} else {
			_, _ = fmt.Fprintf(w, "  ✓ %-20s %-12s (%d files, installed %s)\n", rec.Name, ver, len(rec.Files), rec.InstalledAt.Format("2006-01-02"))
		}
	}
}
