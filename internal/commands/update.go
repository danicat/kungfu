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
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/search"
	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, forceFlag, allFlag, jsonFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Update global skills (~/.agents/skills/)")
	fs.BoolVar(&globalFlag, "g", false, "Update global skills (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Force update even for unversioned, drifted, or locally modified skills")
	fs.BoolVar(&forceFlag, "f", false, "Force update (shorthand)")
	fs.BoolVar(&allFlag, "all", false, "Apply updates to all installed skills")
	fs.BoolVar(&allFlag, "a", false, "Apply updates to all installed skills (shorthand)")
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
	mode := a.resolveMode(jsonFlag)

	syncCtx, syncCancel := context.WithTimeout(context.Background(), 120*time.Second)
	syncExitCode := a.syncRegisteredCatalogs(syncCtx, mode)
	syncCancel()

	installed := st.GetInstalledSkills(scope, wsDir)

	if len(installed) == 0 && len(posArgs) == 0 {
		if mode == OutputModeJSON {
			_ = writeJSON(a.Stdout, []any{})
		} else {
			_, _ = fmt.Fprintf(a.Stdout, "No installed skills found in %s scope.\n", scope)
		}
		return syncExitCode
	}

	type SkillUpdateCheck struct {
		Name            string   `json:"name"`
		InstalledVer    string   `json:"installed_version,omitempty"`
		RemoteVer       string   `json:"remote_version,omitempty"`
		InstalledDigest string   `json:"installed_digest,omitempty"`
		RemoteDigest    string   `json:"remote_digest,omitempty"`
		HasUpdate       bool     `json:"has_update"`
		RequiresForce   bool     `json:"requires_force"`
		IsDirty         bool     `json:"is_dirty"`
		Reason          string   `json:"reason"`
		Status          string   `json:"status"`
		Files           []string `json:"files,omitempty"`
		Message         string   `json:"message,omitempty"`
	}

	checks := make([]SkillUpdateCheck, 0, len(installed))
	dryRun := len(posArgs) == 0 && !allFlag

	installedNames := make([]string, 0, len(installed))
	for k := range installed {
		installedNames = append(installedNames, k)
	}
	sort.Strings(installedNames)

	targetMap := make(map[string]bool)
	if len(posArgs) > 0 {
		for _, name := range posArgs {
			targetMap[name] = true
			if _, ok := installed[name]; !ok {
				checks = append(checks, SkillUpdateCheck{
					Name:   name,
					Status: "not_found",
					Reason: fmt.Sprintf("skill %q is not installed in %s scope", name, scope),
				})
			}
		}
	} else {
		for _, name := range installedNames {
			targetMap[name] = true
		}
	}

	errExitCode := ExitSuccess
	var updatedRecords []state.SkillRecord

	for _, name := range installedNames {
		rec := installed[name]
		if !targetMap[name] {
			continue
		}

		if rec.Origin == state.OriginCustom {
			if !dryRun && len(posArgs) > 0 {
				if errExitCode == ExitSuccess {
					errExitCode = ExitUsage
				}
				checks = append(checks, SkillUpdateCheck{
					Name:         name,
					InstalledVer: rec.Version,
					Status:       "error",
					Reason:       "custom skill (not learned from catalog)",
					Message:      fmt.Sprintf("skill %q is a custom/external skill; kungfu cannot update custom skills", name),
				})
				continue
			}
			checks = append(checks, SkillUpdateCheck{
				Name:         name,
				InstalledVer: rec.Version,
				Status:       "custom",
				Reason:       "custom skill (not in catalog, skipped)",
			})
			continue
		}

		var item *catalog.Item
		var fetchErr error

		if rec.Origin == state.OriginGitHub || (rec.RawURL != "" && isGitHubSkillSpec(rec.RawURL)) {
			var target *catalog.GitHubTarget
			var ok bool
			if rec.RawURL != "" {
				target, ok = catalog.ParseGitHubTarget(rec.RawURL)
			}
			if !ok && rec.Repo != "" {
				target, ok = catalog.ParseGitHubTarget(rec.Repo)
			}
			if ok {
				if rec.Ref != "" {
					target.Ref = rec.Ref
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				item, fetchErr = catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
				cancel()
			} else {
				fetchErr = fmt.Errorf("invalid github repository: %s", rec.Repo)
			}
		} else {
			lookupKey := name
			if rec.Catalog != "" && !strings.EqualFold(rec.Catalog, "default") && !strings.HasPrefix(rec.Catalog, "http") {
				if qualified, qErr := a.Store.Get(name + "@" + strings.TrimPrefix(rec.Catalog, "github:")); qErr == nil && qualified != nil && qualified.Source != "local" {
					item = qualified
				}
			}
			if item == nil {
				item, fetchErr = a.Store.Get(lookupKey)
				if item != nil && item.Source == "local" {
					item = nil
					fetchErr = catalog.ErrItemNotFound
				}
			}
		}

		if fetchErr != nil || item == nil {
			if fetchErr != nil {
				if code := MapErrorToExitCode(fetchErr); code != ExitNotFound && (errExitCode == ExitSuccess || errExitCode == ExitGeneral) {
					errExitCode = code
				}
			}
			checks = append(checks, SkillUpdateCheck{
				Name:         name,
				InstalledVer: rec.Version,
				Status:       "error",
				Reason:       fmt.Sprintf("not found in upstream source: %v", fetchErr),
			})
			continue
		}

		remoteVer := itemVersion(*item)
		if remoteVer == "" && item.URL != "" && item.Body == "" {
			if content, err := a.Store.LoadContent(item); err == nil {
				if fm, _, err := catalog.ParseFrontmatter([]byte(content)); err == nil && fm != nil {
					catalog.ApplyFrontmatter(item, fm, false)
					remoteVer = itemVersion(*item)
				}
			}
		}

		hasUpdate := false
		requiresForce := false
		reason := ""

		if remoteVer != "" && rec.Version != "" {
			cmp := catalog.CompareSemver(remoteVer, rec.Version)
			switch {
			case cmp > 0:
				hasUpdate = true
				reason = fmt.Sprintf("new version available: %s -> %s", rec.Version, remoteVer)
			case cmp == 0:
				hasUpdate = false
				reason = fmt.Sprintf("up to date (%s)", rec.Version)
			default:
				reason = fmt.Sprintf("installed version (%s) is newer than catalog (%s)", rec.Version, remoteVer)
			}
		} else if item.EffectiveDigest() != "" && rec.Digest != "" {
			if catalog.NormalizeDigest(item.EffectiveDigest()) == catalog.NormalizeDigest(rec.Digest) {
				hasUpdate = false
				reason = "up to date (digest match)"
			} else {
				hasUpdate = true
				reason = fmt.Sprintf("digest updated: %s -> %s", rec.Digest, item.EffectiveDigest())
			}
		} else {
			requiresForce = true
			hasUpdate = true
			reason = "unversioned skill in catalog (cannot verify if newer or different)"
		}

		isDirty := rec.InstalledDigest != "" && rec.Digest != "" && rec.Digest != rec.InstalledDigest
		if isDirty && hasUpdate {
			requiresForce = true
			reason = fmt.Sprintf("local files modified since install; %s", reason)
		}

		if forceFlag && !hasUpdate {
			reason = fmt.Sprintf("force update requested (%s)", rec.Version)
		}

		check := SkillUpdateCheck{
			Name:            name,
			InstalledVer:    rec.Version,
			RemoteVer:       remoteVer,
			InstalledDigest: rec.Digest,
			RemoteDigest:    item.EffectiveDigest(),
			HasUpdate:       hasUpdate,
			RequiresForce:   requiresForce,
			IsDirty:         isDirty,
			Reason:          reason,
			Status:          "up_to_date",
		}
		if hasUpdate {
			check.Status = "update_available"
		}

		if !dryRun && (hasUpdate || forceFlag) {
			if !isSafeSkillDirectory(rec.Path, scope, wsDir) {
				check.Status = "error"
				check.Message = fmt.Sprintf("unsafe skill path %q outside authorized skills directory", rec.Path)
			} else if isDirty && !forceFlag {
				check.Status = "conflict"
				check.Message = "Local skill has been modified since install; pass -f/--force to overwrite local edits"
			} else if requiresForce && !forceFlag {
				check.Status = "blocked"
				check.Message = "Pass -f/--force to accept unversioned or drifted update"
			} else {
				content, err := a.Store.LoadContent(item)
				if err != nil {
					if code := MapErrorToExitCode(err); errExitCode == ExitSuccess || errExitCode == ExitGeneral {
						errExitCode = code
					}
					check.Status = "error"
					check.Message = err.Error()
				} else {
					var extraCandidates []string
					for _, refFile := range a.collectReferences(*item, false) {
						extraCandidates = append(extraCandidates, "references/"+refFile)
					}
					for _, scr := range a.listScripts(*item) {
						extraCandidates = append(extraCandidates, "scripts/"+scr)
					}

					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					files, err := a.getRunner().InstallSkillContext(ctx, *item, rec.Path, content, extraCandidates, a.getHTTPClient())
					cancel()
					if err != nil {
						if code := MapErrorToExitCode(err); errExitCode == ExitSuccess || errExitCode == ExitGeneral {
							errExitCode = code
						}
						check.Status = "error"
						check.Message = err.Error()
					} else {
						newDigest, digestErr := state.ComputePackageDigest(rec.Path, files)
						if digestErr != nil {
							if errExitCode == ExitSuccess {
								errExitCode = ExitIntegrity
							}
							check.Status = "error"
							check.Message = fmt.Sprintf("computing updated digest: %v", digestErr)
						} else {
							rec.Version = remoteVer
							rec.Digest = newDigest
							rec.Files = files
							rec.UpdatedAt = time.Now().UTC()
							updatedRecords = append(updatedRecords, rec)

							check.Status = "updated"
							check.Files = files
						}
					}
				}
			}
		}

		checks = append(checks, check)
	}

	saveFailed := false
	if !dryRun && len(updatedRecords) > 0 {
		if err := state.WithLockedState("", func(disk *state.State) error {
			for _, rec := range updatedRecords {
				disk.RecordInstall(scope, wsDir, rec)
			}
			return nil
		}); err != nil {
			saveFailed = true
			path, _ := state.DefaultStateFilePath()
			_, _ = fmt.Fprintf(a.Stderr, "error: failed to persist state to %s: %v\n", path, err)
		}
	}

	if mode == OutputModeJSON {
		if err := writeJSON(a.Stdout, checks); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
	} else if dryRun {
		hasAnyUpdates := false
		hasAnyErrors := false
		_, _ = fmt.Fprintf(a.Stdout, "Checking for skill updates (%s scope)...\n\n", scope)
		for _, c := range checks {
			switch {
			case c.Status == "custom":
				_, _ = fmt.Fprintf(a.Stdout, "  • %-20s [Custom skill] %s\n", c.Name, c.Reason)
			case c.Status == "not_found":
				hasAnyErrors = true
				_, _ = fmt.Fprintf(a.Stderr, "  ⚠ %-20s %s\n", c.Name, c.Reason)
			case c.Status == "error":
				hasAnyErrors = true
				msg := c.Reason
				if c.Message != "" {
					msg = c.Message
				}
				_, _ = fmt.Fprintf(a.Stderr, "  ✗ %-20s [Error] %s\n", c.Name, msg)
			case c.HasUpdate:
				hasAnyUpdates = true
				if c.IsDirty || c.RequiresForce {
					_, _ = fmt.Fprintf(a.Stdout, "  ⚠ %-20s [Update available with -f] %s\n", c.Name, c.Reason)
				} else {
					_, _ = fmt.Fprintf(a.Stdout, "  ↑ %-20s [Update available] %s\n", c.Name, c.Reason)
				}
			default:
				_, _ = fmt.Fprintf(a.Stdout, "  ✓ %-20s %s\n", c.Name, c.Reason)
			}
		}

		if hasAnyUpdates {
			if globalFlag {
				_, _ = fmt.Fprintln(a.Stdout, "\nRun 'kungfu update -g <skill>' or 'kungfu update -g --all' to apply updates.")
			} else {
				_, _ = fmt.Fprintln(a.Stdout, "\nRun 'kungfu update <skill>' or 'kungfu update --all' to apply updates.")
			}
		} else if !hasAnyErrors {
			_, _ = fmt.Fprintln(a.Stdout, "\nAll installed skills are up to date.")
		}
	} else {
		globalHint := ""
		if globalFlag {
			globalHint = "-g "
		}
		for _, c := range checks {
			switch c.Status {
			case "updated":
				_, _ = fmt.Fprintf(a.Stdout, "✓ Updated skill %q (%s, %d files)\n", c.Name, c.Reason, len(c.Files))
			case "up_to_date":
				_, _ = fmt.Fprintf(a.Stdout, "✓ Skill %q is up to date (%s)\n", c.Name, c.Reason)
			case "custom":
				_, _ = fmt.Fprintf(a.Stdout, "• Skipped custom skill %q (%s)\n", c.Name, c.Reason)
			case "not_found":
				_, _ = fmt.Fprintf(a.Stderr, "⚠ Skill %q is not installed in %s scope.\n", c.Name, scope)
				if len(installedNames) > 0 {
					suggs := search.Suggest(c.Name, installedNames, 3)
					if len(suggs) > 0 {
						_, _ = fmt.Fprintln(a.Stderr, "\nDid you mean:")
						for _, s := range suggs {
							_, _ = fmt.Fprintf(a.Stderr, "  - kungfu update %s%s\n", globalHint, s)
						}
					}
				}
			case "conflict", "blocked":
				_, _ = fmt.Fprintf(a.Stderr, "⚠ Skill %q was not updated: %s\n", c.Name, c.Message)
			case "error":
				msg := c.Message
				if msg == "" {
					msg = c.Reason
				}
				_, _ = fmt.Fprintf(a.Stderr, "✗ Failed to update skill %q: %s\n", c.Name, msg)
			}
		}
	}

	if saveFailed {
		return ExitGeneral
	}

	hasNotFound := false
	hasGeneralError := false
	for _, c := range checks {
		switch c.Status {
		case "not_found":
			hasNotFound = true
		case "error", "conflict", "blocked":
			hasGeneralError = true
		}
	}

	if hasNotFound {
		return ExitNotFound
	}
	if errExitCode != ExitSuccess {
		return errExitCode
	}
	if hasGeneralError {
		return ExitGeneral
	}
	return syncExitCode
}
