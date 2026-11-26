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
	"path/filepath"
	"sort"
	"strings"

	"github.com/danicat/kungfu/internal/search"
	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunForget(args []string) int {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, forceFlag, allFlag, jsonFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Forget skill from global store (~/.agents/skills/)")
	fs.BoolVar(&globalFlag, "g", false, "Forget skill from global store (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Also remove custom/external skills not installed by kungfu")
	fs.BoolVar(&forceFlag, "f", false, "Also remove custom/external skills (shorthand)")
	fs.BoolVar(&allFlag, "all", false, "Forget all installed skills in scope")
	fs.BoolVar(&allFlag, "a", false, "Forget all installed skills in scope (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	if allFlag && len(posArgs) > 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: cannot combine --all with explicit skill names")
		return ExitUsage
	}

	scope, wsDir, scopeErr := resolveScopeAndDir(globalFlag)
	if scopeErr != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", scopeErr)
		return ExitGeneral
	}

	st := a.loadStateReconciled()
	installed := st.GetInstalledSkills(scope, wsDir)

	var targets []string
	if allFlag {
		for name, rec := range installed {
			if rec.Origin != state.OriginCustom || forceFlag {
				targets = append(targets, name)
			}
		}
		sort.Strings(targets)
	} else if len(posArgs) > 0 {
		targets = posArgs
	} else {
		_, _ = fmt.Fprintln(a.Stderr, "error: forget requires skill name(s) or --all")
		return ExitUsage
	}

	type ForgetResult struct {
		Name    string   `json:"name"`
		Status  string   `json:"status"`
		Path    string   `json:"path,omitempty"`
		Files   []string `json:"files,omitempty"`
		Message string   `json:"message,omitempty"`
	}

	results := make([]ForgetResult, 0, len(targets))
	var forgottenNames []string

	installedNames := make([]string, 0, len(installed))
	for k := range installed {
		installedNames = append(installedNames, k)
	}
	sort.Strings(installedNames)

	for _, name := range targets {
		rec, isTracked := installed[name]
		if isTracked && rec.Origin == state.OriginCustom && !forceFlag {
			results = append(results, ForgetResult{
				Name:    name,
				Status:  "error",
				Message: fmt.Sprintf("skill %q is a custom/external skill (not learned by kungfu). Kungfu only uninstalls skills it learned; pass -f/--force to remove it anyway.", name),
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
			if !isSafeSkillDirectory(rec.Path, scope, wsDir) {
				results = append(results, ForgetResult{
					Name:    name,
					Status:  "error",
					Message: fmt.Sprintf("unsafe skill path %q outside authorized skills directory", rec.Path),
				})
				continue
			}
			if err := os.RemoveAll(rec.Path); err != nil {
				results = append(results, ForgetResult{
					Name:    name,
					Status:  "error",
					Message: fmt.Sprintf("failed to remove skill directory %q: %v", rec.Path, err),
				})
				continue
			}
		}

		forgottenNames = append(forgottenNames, name)
		results = append(results, ForgetResult{
			Name:   name,
			Status: "forgotten",
			Path:   rec.Path,
			Files:  rec.Files,
		})
	}

	saveFailed := false
	if len(forgottenNames) > 0 {
		if err := state.WithLockedState("", func(disk *state.State) error {
			for _, name := range forgottenNames {
				disk.RecordForget(scope, wsDir, name)
			}
			return nil
		}); err != nil {
			saveFailed = true
			path, _ := state.DefaultStateFilePath()
			_, _ = fmt.Fprintf(a.Stderr, "error: failed to persist state to %s: %v\n", path, err)
		}
	}

	mode := a.resolveMode(jsonFlag)
	if mode == OutputModeJSON {
		if err := writeJSON(a.Stdout, results); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
	} else {
		globalHint := ""
		if globalFlag {
			globalHint = "-g "
		}
		for _, r := range results {
			switch r.Status {
			case "forgotten":
				if len(r.Files) > 0 {
					_, _ = fmt.Fprintf(a.Stdout, "✓ Forgot skill %q (removed %d files from %s)\n", r.Name, len(r.Files), r.Path)
				} else {
					_, _ = fmt.Fprintf(a.Stdout, "✓ Forgot skill %q\n", r.Name)
				}
			case "not_found":
				_, _ = fmt.Fprintf(a.Stderr, "⚠ Skill %q is not installed in %s scope.\n", r.Name, scope)
				if len(installedNames) > 0 {
					suggs := search.Suggest(r.Name, installedNames, 3)
					if len(suggs) > 0 {
						_, _ = fmt.Fprintln(a.Stderr, "\nDid you mean:")
						for _, s := range suggs {
							_, _ = fmt.Fprintf(a.Stderr, "  - kungfu forget %s%s\n", globalHint, s)
						}
					}
				}
			default:
				_, _ = fmt.Fprintf(a.Stderr, "✗ Failed to forget skill %q: %s\n", r.Name, r.Message)
			}
		}
	}

	if saveFailed {
		return ExitGeneral
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

	if hasGeneralError {
		return ExitGeneral
	}
	if hasNotFound {
		return ExitNotFound
	}
	return ExitSuccess
}

func resolveSymlinkPrefix(p string) string {
	clean := filepath.Clean(p)
	if realP, err := filepath.EvalSymlinks(clean); err == nil {
		return realP
	}
	parent := filepath.Dir(clean)
	relParts := []string{filepath.Base(clean)}
	for {
		if evalParent, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(append([]string{evalParent}, relParts...)...)
		}
		relParts = append([]string{filepath.Base(parent)}, relParts...)
		next := filepath.Dir(parent)
		if next == parent {
			return clean
		}
		parent = next
	}
}

func isSafeSkillDirectory(targetPath, scope, wsDir string) bool {
	if targetPath == "" {
		return false
	}
	cleanTarget := filepath.Clean(targetPath)

	var allowedBase string
	if scope == state.ScopeGlobal {
		homeDir, err := os.UserHomeDir()
		if err != nil || homeDir == "" {
			return false
		}
		allowedBase = filepath.Join(homeDir, ".agents", "skills")
	} else {
		if wsDir == "" {
			return false
		}
		allowedBase = filepath.Join(wsDir, ".agents", "skills")
	}
	allowedBase = filepath.Clean(allowedBase)

	rel, err := filepath.Rel(allowedBase, cleanTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if strings.Contains(rel, string(filepath.Separator)) {
		return false
	}

	realBase := resolveSymlinkPrefix(allowedBase)
	realTarget := resolveSymlinkPrefix(cleanTarget)

	realRel, err := filepath.Rel(realBase, realTarget)
	if err != nil || realRel == "." || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return false
	}
	if strings.Contains(realRel, string(filepath.Separator)) {
		return false
	}
	return true
}
