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
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/search"
	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, yesFlag, forceFlag, allFlag, refreshFlag, jsonFlag, rawFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Update global skills (~/.agents/skills/)")
	fs.BoolVar(&globalFlag, "g", false, "Update global skills (shorthand)")
	fs.BoolVar(&yesFlag, "yes", false, "Accept unversioned or drifted upstream updates")
	fs.BoolVar(&yesFlag, "y", false, "Accept unversioned or drifted upstream updates (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Force update regardless of version/digest status")
	fs.BoolVar(&forceFlag, "f", false, "Force update regardless of status (shorthand)")
	fs.BoolVar(&allFlag, "all", false, "Apply updates to all installed skills")
	fs.BoolVar(&allFlag, "a", false, "Apply updates to all installed skills (shorthand)")
	fs.BoolVar(&refreshFlag, "refresh", false, "Bypass cache and force refresh remote catalog")
	fs.BoolVar(&refreshFlag, "r", false, "Bypass cache and force refresh remote catalog (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	if refreshFlag {
		if store, err := catalog.NewCatalog(
			catalog.WithRemoteSync(true),
			catalog.WithForceRefresh(),
		); err == nil {
			a.Store = store
		}
	}

	scope := state.ScopeWorkspace
	wsDir, _ := os.Getwd()
	if globalFlag {
		scope = state.ScopeGlobal
		wsDir = ""
	}

	st := a.loadState()
	installed := st.GetInstalledSkills(scope, wsDir)

	mode := a.resolveMode(jsonFlag, rawFlag)

	if len(installed) == 0 && len(posArgs) == 0 {
		switch mode {
		case OutputModeJSON:
			_ = json.NewEncoder(a.Stdout).Encode([]any{})
		case OutputModeRaw:
			// empty output
		default:
			_, _ = fmt.Fprintf(a.Stdout, "No installed skills found in %s scope.\n", scope)
		}
		return ExitSuccess
	}

	type SkillUpdateCheck struct {
		Name            string   `json:"name"`
		InstalledVer    string   `json:"installed_version,omitempty"`
		RemoteVer       string   `json:"remote_version,omitempty"`
		InstalledDigest string   `json:"installed_digest,omitempty"`
		RemoteDigest    string   `json:"remote_digest,omitempty"`
		HasUpdate       bool     `json:"has_update"`
		RequiresYes     bool     `json:"requires_yes"`
		IsDirty         bool     `json:"is_dirty"`
		Reason          string   `json:"reason"`
		Status          string   `json:"status"`
		Files           []string `json:"files,omitempty"`
		Message         string   `json:"message,omitempty"`
	}

	var checks []SkillUpdateCheck
	dryRun := len(posArgs) == 0 && !allFlag

	var installedNames []string
	for k := range installed {
		installedNames = append(installedNames, k)
	}

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
		for name := range installed {
			targetMap[name] = true
		}
	}

	for name, rec := range installed {
		if !targetMap[name] {
			continue
		}

		if rec.Origin == state.OriginCustom {
			if !dryRun && len(posArgs) > 0 {
				_, _ = fmt.Fprintf(a.Stderr, "error: skill %q is a custom/external skill (not learned from catalog). Kungfu cannot update custom skills.\n", name)
				return ExitUsage
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
		var err error

		if rec.Origin == state.OriginGitHub || (rec.RawURL != "" && (strings.Contains(rec.RawURL, "github.com") || strings.Contains(rec.RawURL, "raw.githubusercontent.com"))) {
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
				item, err = catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
				cancel()
			} else {
				err = fmt.Errorf("invalid github repository: %s", rec.Repo)
			}
		} else {
			item, err = a.Store.Get(name)
			if err != nil || item == nil || (item.URL == "" && item.Body == "") {
				lookupCatalogs := []string{rec.Catalog, name, "kungfu"}
				for _, catName := range lookupCatalogs {
					if catName == "" {
						continue
					}
					if catRec, ok := st.GetCatalog(catName); ok && catRec.Type == state.CatalogTypeGitHub {
						target, okTarget := catalog.ParseGitHubTarget(catRec.Source)
						if okTarget {
							if catRec.Ref != "" {
								target.Ref = catRec.Ref
							}
							if target.Subpath == "" {
								target.Subpath = "skills/" + name
							}
							ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
							ghItem, ghErr := catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
							cancel()
							if ghErr == nil && ghItem != nil {
								item = ghItem
								err = nil
								rec.Origin = state.OriginGitHub
								rec.Repo = catRec.Source
								rec.Ref = catRec.Ref
								if ghItem.URL != "" {
									rec.RawURL = ghItem.URL
								}
								break
							}
						}
					}
				}
			}
		}

		if err != nil || item == nil {
			checks = append(checks, SkillUpdateCheck{
				Name:         name,
				InstalledVer: rec.Version,
				Status:       "error",
				Reason:       fmt.Sprintf("not found in upstream source: %v", err),
			})
			continue
		}

		remoteVer := item.Version
		if remoteVer == "" && item.Metadata != nil {
			remoteVer = item.Metadata["version"]
		}
		if remoteVer == "" && item.URL != "" && item.Body == "" {
			if content, err := a.Store.LoadContent(item); err == nil {
				if fm, _, err := catalog.ParseFrontmatter([]byte(content)); err == nil && fm != nil {
					if fm.Metadata != nil && fm.Metadata["version"] != "" {
						remoteVer = fm.Metadata["version"]
						item.Version = remoteVer
						if item.Metadata == nil {
							item.Metadata = fm.Metadata
						}
					}
				}
			}
		}

		hasUpdate := false
		requiresYes := false
		reason := ""

		if remoteVer != "" && rec.Version != "" {
			if isSemverGreater(remoteVer, rec.Version) {
				hasUpdate = true
				reason = fmt.Sprintf("new version available: %s -> %s", rec.Version, remoteVer)
			} else if remoteVer == rec.Version {
				hasUpdate = false
				reason = fmt.Sprintf("up to date (%s)", rec.Version)
			} else {
				reason = fmt.Sprintf("installed version (%s) is newer than catalog (%s)", rec.Version, remoteVer)
			}
		} else if item.Digest != "" && rec.Digest != "" {
			if item.Digest == rec.Digest {
				hasUpdate = false
				reason = "up to date (digest match)"
			} else {
				hasUpdate = true
				reason = fmt.Sprintf("digest updated: %s -> %s", rec.Digest, item.Digest)
			}
		} else {
			requiresYes = true
			hasUpdate = true
			reason = "unversioned skill in catalog (cannot verify if newer or different)"
		}

		isDirty := rec.InstalledDigest != "" && rec.Digest != "" && rec.Digest != rec.InstalledDigest
		if isDirty && hasUpdate {
			requiresYes = true
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
			HasUpdate:       hasUpdate,
			RequiresYes:     requiresYes,
			IsDirty:         isDirty,
			Reason:          reason,
			Status:          "up_to_date",
		}

		if hasUpdate {
			check.Status = "update_available"
		}

		if !dryRun && (hasUpdate || forceFlag) {
			if isDirty && !forceFlag {
				check.Status = "conflict"
				check.Message = "Local skill has been modified since install; pass -f/--force to overwrite local edits"
			} else if requiresYes && !yesFlag && !forceFlag {
				check.Status = "blocked"
				check.Message = "Pass -y/--yes or -f/--force to accept unversioned/drifted update"
			} else {
				content, err := a.loadItemContent(item)
				if err != nil {
					check.Status = "error"
					check.Message = err.Error()
				} else {
					var extraCandidates []string
					for _, ref := range a.listReferences(*item) {
						extraCandidates = append(extraCandidates, "references/"+ref+".md")
					}
					for _, scr := range a.listScripts(*item) {
						extraCandidates = append(extraCandidates, "scripts/"+scr)
					}

					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					files, err := runner.InstallSkillContext(ctx, *item, rec.Path, content, extraCandidates, a.getHTTPClient())
					cancel()
					if err != nil {
						check.Status = "error"
						check.Message = err.Error()
					} else {
						newDigest, _ := state.ComputePackageDigest(rec.Path, files)
						rec.Version = remoteVer
						rec.Digest = newDigest
						rec.Files = files
						rec.UpdatedAt = time.Now().UTC()
						st.RecordInstall(scope, wsDir, rec)

						check.Status = "updated"
						check.Files = files
					}
				}
			}
		}

		checks = append(checks, check)
	}

	if !dryRun {
		a.saveState(st)
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(checks)
	case OutputModeRaw:
		for _, c := range checks {
			if dryRun {
				_, _ = fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.InstalledVer, c.RemoteVer, c.Status, c.Reason)
			} else {
				if c.Status == "updated" {
					_, _ = fmt.Fprintf(a.Stdout, "updated\t%s\t%s\n", c.Name, c.Reason)
				} else {
					msg := c.Reason
					if c.Message != "" {
						msg = c.Message
					}
					_, _ = fmt.Fprintf(a.Stdout, "%s\t%s\t%s\n", c.Status, c.Name, msg)
				}
			}
		}
	default: // OutputModeHuman
		if dryRun {
			hasAnyUpdates := false
			_, _ = fmt.Fprintf(a.Stdout, "Checking for skill updates (%s scope)...\n\n", scope)
			for _, c := range checks {
				if c.Status == "custom" {
					_, _ = fmt.Fprintf(a.Stdout, "  • %-20s [Custom skill] %s\n", c.Name, c.Reason)
				} else if c.Status == "not_found" {
					_, _ = fmt.Fprintf(a.Stderr, "  ⚠ %-20s %s\n", c.Name, c.Reason)
					if len(installedNames) > 0 {
						suggs := search.Suggest(c.Name, installedNames, 3)
						if len(suggs) > 0 {
							_, _ = fmt.Fprintln(a.Stderr, "    Did you mean:")
							for _, s := range suggs {
								_, _ = fmt.Fprintf(a.Stderr, "      - %s\n", s)
							}
						}
					}
				} else if c.HasUpdate {
					hasAnyUpdates = true
					if c.IsDirty {
						_, _ = fmt.Fprintf(a.Stdout, "  ⚠ %-20s [Update available with -f] %s\n", c.Name, c.Reason)
					} else if c.RequiresYes {
						_, _ = fmt.Fprintf(a.Stdout, "  ⚠ %-20s [Update available with -y] %s\n", c.Name, c.Reason)
					} else {
						_, _ = fmt.Fprintf(a.Stdout, "  ↑ %-20s [Update available] %s\n", c.Name, c.Reason)
					}
				} else {
					_, _ = fmt.Fprintf(a.Stdout, "  ✓ %-20s %s\n", c.Name, c.Reason)
				}
			}

			if hasAnyUpdates {
				if globalFlag {
					_, _ = fmt.Fprintln(a.Stdout, "\nRun 'kungfu update -g <skill>' or 'kungfu update -g --all' to apply updates.")
				} else {
					_, _ = fmt.Fprintln(a.Stdout, "\nRun 'kungfu update <skill>' or 'kungfu update --all' to apply updates.")
				}
			} else {
				_, _ = fmt.Fprintln(a.Stdout, "\nAll installed skills are up to date.")
			}
		} else {
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
								_, _ = fmt.Fprintf(a.Stderr, "  - kungfu update %s\n", s)
							}
						}
					}
				case "conflict":
					_, _ = fmt.Fprintf(a.Stderr, "⚠ Skill %q was not updated: %s\n", c.Name, c.Message)
				case "blocked":
					_, _ = fmt.Fprintf(a.Stderr, "⚠ Skill %q was not updated: %s\n", c.Name, c.Message)
				case "error":
					_, _ = fmt.Fprintf(a.Stderr, "✗ Failed to update skill %q: %s\n", c.Name, c.Message)
				}
			}
		}
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
	if hasGeneralError {
		return ExitGeneral
	}
	return ExitSuccess
}

func parseSemver(v string) []int {
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	parts := strings.Split(v, ".")
	var res []int
	for _, p := range parts {
		var n int
		for _, r := range p {
			if r >= '0' && r <= '9' {
				n = n*10 + int(r-'0')
			} else {
				break
			}
		}
		res = append(res, n)
	}
	for len(res) < 3 {
		res = append(res, 0)
	}
	return res
}

func isSemverGreater(v1, v2 string) bool {
	p1 := parseSemver(v1)
	p2 := parseSemver(v2)
	for i := 0; i < len(p1) && i < len(p2); i++ {
		if p1[i] > p2[i] {
			return true
		}
		if p1[i] < p2[i] {
			return false
		}
	}
	return len(p1) > len(p2)
}
