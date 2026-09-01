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
	"path/filepath"
	"sort"

	"github.com/danicat/kungfu/internal/search"
	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunForget(args []string) int {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, forceFlag, allFlag, jsonFlag, rawFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Forget skill from global store (~/.agents/skills/)")
	fs.BoolVar(&globalFlag, "g", false, "Forget skill from global store (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Force forget without confirmation")
	fs.BoolVar(&forceFlag, "f", false, "Force forget without confirmation (shorthand)")
	fs.BoolVar(&allFlag, "all", false, "Forget all installed skills in scope")
	fs.BoolVar(&allFlag, "a", false, "Forget all installed skills in scope (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	scope := state.ScopeWorkspace
	wsDir, _ := os.Getwd()
	if globalFlag {
		scope = state.ScopeGlobal
		wsDir = ""
	}

	st := a.loadState()
	installed := st.GetInstalledSkills(scope, wsDir)

	var targets []string
	if allFlag {
		for name, rec := range installed {
			if rec.Origin != state.OriginCustom {
				targets = append(targets, name)
			}
		}
		sort.Strings(targets)
	} else if len(posArgs) > 0 {
		targets = posArgs
	} else {
		fmt.Fprintln(a.Stderr, "error: forget requires skill name(s) or --all")
		return ExitUsage
	}

	type ForgetResult struct {
		Name    string   `json:"name"`
		Status  string   `json:"status"`
		Path    string   `json:"path,omitempty"`
		Files   []string `json:"files,omitempty"`
		Message string   `json:"message,omitempty"`
	}

	var results []ForgetResult

	var installedNames []string
	for k := range installed {
		installedNames = append(installedNames, k)
	}

	for _, name := range targets {
		rec, isTracked := installed[name]
		if isTracked && rec.Origin == state.OriginCustom {
			results = append(results, ForgetResult{
				Name:    name,
				Status:  "error",
				Message: fmt.Sprintf("skill %q is a custom/external skill (not learned by kungfu). Kungfu only uninstalls skills it learned.", name),
			})
			continue
		}

		if !isTracked {
			results = append(results, ForgetResult{
				Name:    name,
				Status:  "not_found",
				Message: fmt.Sprintf("skill %q is not installed in %s scope", name, scope),
			})
			continue
		}

		if rec.Path != "" {
			for _, relFile := range rec.Files {
				filePath := filepath.Join(rec.Path, filepath.FromSlash(relFile))
				_ = os.Remove(filePath)
			}
			_ = os.RemoveAll(rec.Path)
		}

		st.RecordForget(scope, wsDir, name)
		results = append(results, ForgetResult{
			Name:   name,
			Status: "forgotten",
			Path:   rec.Path,
			Files:  rec.Files,
		})
	}

	a.saveState(st)

	mode := a.resolveMode(jsonFlag, rawFlag)
	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
	case OutputModeRaw:
		for _, r := range results {
			if r.Status == "forgotten" {
				fmt.Fprintf(a.Stdout, "forgot\t%s\t%s\n", r.Name, r.Path)
			} else {
				fmt.Fprintf(a.Stdout, "%s\t%s\t%s\n", r.Status, r.Name, r.Message)
			}
		}
	default: // OutputModeHuman
		for _, r := range results {
			switch r.Status {
			case "forgotten":
				if len(r.Files) > 0 {
					fmt.Fprintf(a.Stdout, "✓ Forgot skill %q (removed %d files from %s)\n", r.Name, len(r.Files), r.Path)
				} else {
					fmt.Fprintf(a.Stdout, "✓ Forgot skill %q\n", r.Name)
				}
			case "not_found":
				fmt.Fprintf(a.Stderr, "⚠ Skill %q is not installed in %s scope.\n", r.Name, scope)
				if len(installedNames) > 0 {
					suggs := search.Suggest(r.Name, installedNames, 3)
					if len(suggs) > 0 {
						fmt.Fprintln(a.Stderr, "\nDid you mean:")
						for _, s := range suggs {
							fmt.Fprintf(a.Stderr, "  - kungfu forget %s\n", s)
						}
					}
				}
			default:
				fmt.Fprintf(a.Stderr, "✗ Failed to forget skill %q: %s\n", r.Name, r.Message)
			}
		}
	}

	hasNotFound := false
	hasGeneralError := false
	for _, r := range results {
		switch r.Status {
		case "not_found":
			hasNotFound = true
		case "error":
			hasGeneralError = true
		}
	}

	if hasNotFound {
		return ExitNotFound
	}
	if hasGeneralError {
		return ExitGeneral
	}
	return ExitSuccess
}
