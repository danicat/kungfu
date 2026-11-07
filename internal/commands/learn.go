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
	"path/filepath"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/state"
)

func resolveInstallDirectory(scope, skillName string) (string, error) {
	if scope == state.ScopeGlobal {
		homeDir, err := os.UserHomeDir()
		if err != nil || homeDir == "" {
			return "", fmt.Errorf("unable to determine user home directory: %w", err)
		}
		return filepath.Join(homeDir, ".agents", "skills", skillName), nil
	}
	wsDir, err := os.Getwd()
	if err != nil || wsDir == "" {
		return "", fmt.Errorf("unable to determine working directory: %w", err)
	}
	return filepath.Join(wsDir, ".agents", "skills", skillName), nil
}

func (a *App) RunLearn(args []string) int {
	fs := flag.NewFlagSet("learn", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, forceFlag, loadFlag, jsonFlag, rawFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Install skill to global store (~/.agents/skills/)")
	fs.BoolVar(&globalFlag, "g", false, "Install skill to global store (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Overwrite existing skill installation")
	fs.BoolVar(&forceFlag, "f", false, "Overwrite existing skill installation (shorthand)")
	fs.BoolVar(&loadFlag, "load", false, "Output loaded skill markdown to stdout upon successful installation")
	fs.BoolVar(&loadFlag, "l", false, "Output loaded skill markdown (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	var itemsToInstall []catalog.Item
	var notFounds []string

	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: learn requires at least one skill name or repository (e.g. 'github:owner/repo')")
		return ExitUsage
	}

	for _, spec := range posArgs {
		if isGitHubSkillSpec(spec) {
			target, ok := catalog.ParseGitHubTarget(spec)
			if !ok {
				_, _ = fmt.Fprintf(a.Stderr, "error: invalid github skill target %q\n", spec)
				return ExitUsage
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			item, err := catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
			cancel()
			if err != nil {
				_, _ = fmt.Fprintf(a.Stderr, "error: fetching skill from github %q: %v\n", spec, err)
				return MapErrorToExitCode(err)
			}
			itemsToInstall = append(itemsToInstall, *item)
			continue
		}

		item, err := a.Store.Get(spec)
		if err != nil {
			notFounds = append(notFounds, spec)
			continue
		}
		itemsToInstall = append(itemsToInstall, *item)
	}

	if len(notFounds) > 0 {
		for _, name := range notFounds {
			a.printNotFoundWithSuggestions("learn", name)
		}
		return ExitNotFound
	}

	scope, wsDir := resolveScopeAndDir(globalFlag)

	st := a.loadState()
	installed := st.GetInstalledSkills(scope, wsDir)

	type InstallResult struct {
		Name    string   `json:"name"`
		Status  string   `json:"status"`
		Path    string   `json:"path,omitempty"`
		Version string   `json:"version,omitempty"`
		Digest  string   `json:"digest,omitempty"`
		Files   []string `json:"files,omitempty"`
		Content string   `json:"content,omitempty"`
		Message string   `json:"message,omitempty"`
	}

	var results []InstallResult
	errExitCode := ExitSuccess

	for _, item := range itemsToInstall {
		skillDir, dirErr := resolveInstallDirectory(scope, item.Name)
		if dirErr != nil {
			if errExitCode == ExitSuccess {
				errExitCode = ExitGeneral
			}
			results = append(results, InstallResult{
				Name:    item.Name,
				Status:  "error",
				Message: dirErr.Error(),
			})
			continue
		}

		if rec, exists := installed[item.Name]; exists && !forceFlag {
			results = append(results, InstallResult{
				Name:    item.Name,
				Status:  "already_installed",
				Path:    rec.Path,
				Version: rec.Version,
				Digest:  rec.Digest,
				Files:   rec.Files,
				Message: fmt.Sprintf("skill %q is already installed in %s scope; pass -f/--force to overwrite", item.Name, scope),
			})
			continue
		}

		content, err := a.loadItemContent(&item)
		if err != nil {
			if code := MapErrorToExitCode(err); errExitCode == ExitSuccess || errExitCode == ExitGeneral {
				errExitCode = code
			}
			results = append(results, InstallResult{
				Name:    item.Name,
				Status:  "error",
				Message: fmt.Sprintf("failed to load skill content: %v", err),
			})
			continue
		}

		var extraCandidates []string
		for _, refFile := range a.listReferenceFiles(item) {
			extraCandidates = append(extraCandidates, "references/"+refFile)
		}
		for _, scr := range a.listScripts(item) {
			extraCandidates = append(extraCandidates, "scripts/"+scr)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		files, err := runner.InstallSkillContext(ctx, item, skillDir, content, extraCandidates, a.getHTTPClient())
		cancel()

		if err != nil {
			if code := MapErrorToExitCode(err); errExitCode == ExitSuccess || errExitCode == ExitGeneral {
				errExitCode = code
			}
			results = append(results, InstallResult{
				Name:    item.Name,
				Status:  "error",
				Message: fmt.Sprintf("installation failed: %v", err),
			})
			continue
		}

		pkgDigest, _ := state.ComputePackageDigest(skillDir, files)

		ver := item.Version
		if ver == "" && item.Metadata != nil {
			ver = item.Metadata["version"]
		}

		origin := state.OriginCatalog
		repo := ""
		ref := ""
		rawURL := ""
		if strings.HasPrefix(item.Source, "github:") {
			origin = state.OriginGitHub
			if item.Metadata != nil {
				repo = item.Metadata["repo"]
				ref = item.Metadata["ref"]
				rawURL = item.Metadata["canonical"]
			}
		} else if item.Metadata != nil && (item.Metadata["repo"] != "" || item.Metadata["canonical"] != "") {
			if item.Metadata["repo"] != "" {
				repo = item.Metadata["repo"]
				ref = item.Metadata["ref"]
				origin = state.OriginGitHub
			}
			if item.Metadata["canonical"] != "" {
				rawURL = item.Metadata["canonical"]
			}
		} else if strings.Contains(item.URL, "raw.githubusercontent.com") {
			origin = state.OriginGitHub
			rawURL = item.URL
		}
		if rawURL == "" && item.URL != "" {
			rawURL = item.URL
		}

		catSource := normalizeItemCatalog(item)
		rec := state.SkillRecord{
			Name:            item.Name,
			Scope:           scope,
			Path:            skillDir,
			Version:         ver,
			Digest:          pkgDigest,
			InstalledDigest: pkgDigest,
			Status:          state.StatusInstalled,
			InstalledAt:     time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
			Origin:          origin,
			Repo:            repo,
			Ref:             ref,
			RawURL:          rawURL,
			Catalog:         item.Source,
			Files:           files,
		}
		st.RecordInstall(scope, wsDir, rec)
		st.RecordLearnWithCatalog(item.Name, catSource)

		res := InstallResult{
			Name:    item.Name,
			Status:  "installed",
			Path:    skillDir,
			Version: ver,
			Digest:  pkgDigest,
			Files:   files,
		}
		if loadFlag {
			res.Content = stripFrontmatter(content)
			st.RecordLoadDetailedWithCatalog(item.Name, catSource, "", 1)
		}
		results = append(results, res)
	}

	a.saveState(st)

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
	case OutputModeRaw:
		for _, r := range results {
			if r.Status == "installed" {
				_, _ = fmt.Fprintf(a.Stdout, "installed\t%s\t%s\t%s\t%d\n",
					sanitizeTSVField(r.Name),
					sanitizeTSVField(r.Version),
					sanitizeTSVField(r.Path),
					len(r.Files),
				)
				if loadFlag && r.Content != "" {
					_, _ = fmt.Fprint(a.Stdout, r.Content)
					if !strings.HasSuffix(r.Content, "\n") {
						_, _ = fmt.Fprintln(a.Stdout)
					}
				}
			} else {
				_, _ = fmt.Fprintf(a.Stdout, "%s\t%s\t%s\n",
					sanitizeTSVField(r.Status),
					sanitizeTSVField(r.Name),
					sanitizeTSVField(r.Message),
				)
			}
		}
	default: // OutputModeHuman
		for _, r := range results {
			switch r.Status {
			case "installed":
				_, _ = fmt.Fprintf(a.Stdout, "✓ Learned skill %q into %s scope (%d files in %s)\n", r.Name, scope, len(r.Files), r.Path)
				if loadFlag && r.Content != "" {
					_, _ = fmt.Fprintln(a.Stdout, "\n--- Skill Content ---")
					_, _ = fmt.Fprint(a.Stdout, r.Content)
					if !strings.HasSuffix(r.Content, "\n") {
						_, _ = fmt.Fprintln(a.Stdout)
					}
				}
			case "already_installed":
				_, _ = fmt.Fprintf(a.Stderr, "• %s\n", r.Message)
			default:
				_, _ = fmt.Fprintf(a.Stderr, "✗ Failed to learn skill %q: %s\n", r.Name, r.Message)
			}
		}
	}

	if errExitCode != ExitSuccess {
		return errExitCode
	}
	for _, r := range results {
		if r.Status == "error" {
			return ExitGeneral
		}
	}
	return ExitSuccess
}
