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
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/state"
)

func resolveInstallDirectory(scope, wsDir, skillName string) (string, error) {
	if err := catalog.ValidateSkillName(skillName); err != nil {
		return "", fmt.Errorf("unsafe skill name %q: %w", skillName, err)
	}
	var targetDir string
	if scope == state.ScopeGlobal {
		homeDir, err := os.UserHomeDir()
		if err != nil || homeDir == "" {
			return "", fmt.Errorf("unable to determine user home directory: %w", err)
		}
		targetDir = filepath.Join(homeDir, ".agents", "skills", skillName)
	} else {
		if wsDir == "" {
			var err error
			wsDir, err = os.Getwd()
			if err != nil || wsDir == "" {
				return "", fmt.Errorf("unable to determine working directory: %w", err)
			}
		}
		targetDir = filepath.Join(wsDir, ".agents", "skills", skillName)
	}
	if !isSafeSkillDirectory(targetDir, scope, wsDir) {
		return "", fmt.Errorf("resolved install directory %q escapes authorized skills directory: %w", targetDir, catalog.ErrInvalidName)
	}
	return targetDir, nil
}

func (a *App) RunLearn(args []string) int {
	fs := flag.NewFlagSet("learn", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, forceFlag, jsonFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Install skill to global store (~/.agents/skills/)")
	fs.BoolVar(&globalFlag, "g", false, "Install skill to global store (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Overwrite existing skill installation")
	fs.BoolVar(&forceFlag, "f", false, "Overwrite existing skill installation (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag)

	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: learn requires at least one skill name or repository (e.g. 'danicat/kungfu')")
		return ExitUsage
	}

	var itemsToInstall []catalog.Item
	var notFounds []string

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

		item, err := a.resolveSkill(spec)
		if err != nil {
			if errors.Is(err, catalog.ErrItemNotFound) {
				notFounds = append(notFounds, spec)
				continue
			}
			_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return MapErrorToExitCode(err)
		}
		itemsToInstall = append(itemsToInstall, *item)
	}

	if len(notFounds) > 0 {
		for _, name := range notFounds {
			a.printNotFoundWithSuggestions("learn", name)
		}
		return ExitNotFound
	}

	scope, wsDir, scopeErr := resolveScopeAndDir(globalFlag)
	if scopeErr != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", scopeErr)
		return ExitGeneral
	}

	st := a.loadStateReconciled()
	installed := st.GetInstalledSkills(scope, wsDir)

	type InstallResult struct {
		Name    string   `json:"name"`
		Status  string   `json:"status"`
		Path    string   `json:"path,omitempty"`
		Version string   `json:"version,omitempty"`
		Digest  string   `json:"digest,omitempty"`
		Files   []string `json:"files,omitempty"`
		Message string   `json:"message,omitempty"`
	}

	type pendingInstall struct {
		rec       state.SkillRecord
		catSource string
	}

	results := make([]InstallResult, 0, len(itemsToInstall))
	var pending []pendingInstall
	errExitCode := ExitSuccess

	for _, item := range itemsToInstall {
		skillDir, dirErr := resolveInstallDirectory(scope, wsDir, item.Name)
		if dirErr != nil {
			if code := MapErrorToExitCode(dirErr); errExitCode == ExitSuccess {
				errExitCode = code
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
		if _, statErr := os.Stat(skillDir); statErr == nil && !forceFlag {
			results = append(results, InstallResult{
				Name:    item.Name,
				Status:  "already_installed",
				Path:    skillDir,
				Message: fmt.Sprintf("directory %q already exists on disk; pass -f/--force to overwrite", skillDir),
			})
			continue
		}

		content, err := a.Store.LoadContent(&item)
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
		for _, refFile := range a.collectReferences(item, false) {
			extraCandidates = append(extraCandidates, "references/"+refFile)
		}
		for _, scr := range a.listScripts(item) {
			extraCandidates = append(extraCandidates, "scripts/"+scr)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		files, err := a.getRunner().InstallSkillContext(ctx, item, skillDir, content, extraCandidates, a.getHTTPClient())
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

		pkgDigest, digestErr := state.ComputePackageDigest(skillDir, files)
		if digestErr != nil {
			if errExitCode == ExitSuccess {
				errExitCode = ExitIntegrity
			}
			results = append(results, InstallResult{
				Name:    item.Name,
				Status:  "error",
				Path:    skillDir,
				Message: fmt.Sprintf("computing package digest: %v", digestErr),
			})
			continue
		}

		ver := itemVersion(item)

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
		pending = append(pending, pendingInstall{
			rec:       rec,
			catSource: normalizeItemCatalog(item),
		})

		results = append(results, InstallResult{
			Name:    item.Name,
			Status:  "installed",
			Path:    skillDir,
			Version: ver,
			Digest:  pkgDigest,
			Files:   files,
		})
	}

	if len(pending) > 0 {
		telemetryOn := a.Config != nil && a.Config.IsTelemetryEnabled()
		if err := state.WithLockedState("", func(disk *state.State) error {
			for _, p := range pending {
				disk.RecordInstall(scope, wsDir, p.rec)
				if telemetryOn {
					disk.RecordLearn(p.rec.Name, p.catSource)
				}
			}
			return nil
		}); err != nil {
			path, _ := state.DefaultStateFilePath()
			_, _ = fmt.Fprintf(a.Stderr, "error: failed to persist state to %s: %v\n", path, err)
			if errExitCode == ExitSuccess {
				errExitCode = ExitGeneral
			}
		}
	}

	if mode == OutputModeJSON {
		if err := writeJSON(a.Stdout, results); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
	} else {
		for _, r := range results {
			switch r.Status {
			case "installed":
				_, _ = fmt.Fprintf(a.Stdout, "✓ Learned skill %q into %s scope (%d files in %s)\n", r.Name, scope, len(r.Files), r.Path)
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
