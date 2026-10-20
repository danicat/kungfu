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
	"github.com/danicat/kungfu/internal/config"
	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunCatalog(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, _ = fmt.Fprintln(a.Stdout, `USAGE:
  kungfu catalog <subcommand> [flags] [arguments...]

SUBCOMMANDS:
  add <source> [--name <name>] [--ref <ref>] [--subpath <path>] [--refresh] [--json] [--raw]
      Register a skill catalog from a GitHub repository, HTTP manifest, or local directory.
  list [--json] [--raw]
      List all registered skill catalogs and default sources.
  remove <name> [--json] [--raw]
      Remove a registered catalog source.

ALIASES:
  ls (list), rm / delete (remove)`)
		return ExitSuccess
	}

	subcmd := args[0]
	subArgs := args[1:]

	switch subcmd {
	case "add", "install":
		return a.runCatalogAdd(subArgs)
	case "list", "ls":
		return a.runCatalogList(subArgs)
	case "remove", "rm", "delete":
		return a.runCatalogRemove(subArgs)
	default:
		_, _ = fmt.Fprintf(a.Stderr, "error: unknown catalog subcommand %q. Run 'kungfu catalog --help' for usage.\n", subcmd)
		return ExitUsage
	}
}

func (a *App) runCatalogAdd(args []string) int {
	fs := flag.NewFlagSet("catalog add", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var nameFlag, refFlag, subpathFlag string
	var refreshFlag, jsonFlag, rawFlag bool

	fs.StringVar(&nameFlag, "name", "", "Custom alias/name for the catalog")
	fs.StringVar(&nameFlag, "n", "", "Custom alias/name for the catalog (shorthand)")
	fs.StringVar(&refFlag, "ref", "main", "Branch or tag ref for git repository")
	fs.StringVar(&refFlag, "b", "main", "Branch or tag ref (shorthand)")
	fs.StringVar(&refFlag, "branch", "main", "Branch or tag ref (alias)")
	fs.StringVar(&subpathFlag, "subpath", "", "Subpath directory inside repository")
	fs.StringVar(&subpathFlag, "p", "", "Subpath directory inside repository (shorthand)")
	fs.BoolVar(&refreshFlag, "refresh", false, "Force fresh download bypassing cache")
	fs.BoolVar(&refreshFlag, "r", false, "Force fresh download (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: catalog add requires a source (GitHub repository 'owner/repo', HTTP URL, or local directory)")
		return ExitUsage
	}

	source := posArgs[0]
	mode := a.resolveMode(jsonFlag, rawFlag)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var manifest *catalog.Manifest
	var catType string
	var catName string
	var catURL string
	var etag string

	// 1. Check if source is a local directory
	if info, err := os.Stat(source); err == nil && info.IsDir() {
		catType = state.CatalogTypeLocal
		absPath, _ := filepath.Abs(source)
		catURL = absPath
		catName = nameFlag
		if catName == "" {
			catName = filepath.Base(absPath)
		}
		m, err := catalog.DiscoverLocalCatalog(source)
		if err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: discovering local catalog %s: %v\n", source, err)
			return ExitGeneral
		}
		manifest = m
	} else if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") && !strings.Contains(source, "github.com") {
		// 2. HTTP Manifest URL
		catType = state.CatalogTypeHTTP
		catURL = source
		catName = nameFlag
		if catName == "" {
			catName = strings.TrimPrefix(strings.TrimPrefix(source, "https://"), "http://")
			catName = strings.TrimSuffix(catName, "/.well-known/agent-skills/index.json")
			catName = strings.TrimSuffix(catName, "/index.json")
			catName = strings.TrimSuffix(catName, "/catalog.json")
			catName = strings.TrimSuffix(catName, "/")
		}
		m, tag, err := catalog.FetchHTTPCatalog(ctx, source, a.getHTTPClient())
		if err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: fetching http catalog %s: %v\n", source, err)
			return ExitGeneral
		}
		manifest = m
		etag = tag
	} else {
		// 3. GitHub repository
		target, ok := catalog.ParseGitHubTarget(source)
		if !ok {
			_, _ = fmt.Fprintf(a.Stderr, "error: invalid catalog source %q. Expected GitHub repository (e.g. google/skills), HTTP URL, or local directory path.\n", source)
			return ExitUsage
		}
		if refFlag != "main" && refFlag != "" {
			target.Ref = refFlag
		}
		if subpathFlag != "" {
			target.Subpath = subpathFlag
		}
		catType = state.CatalogTypeGitHub
		catURL = target.FullRepoURL()
		catName = nameFlag
		if catName == "" {
			catName = target.RepoString()
			if target.Subpath != "" {
				catName += "/" + target.Subpath
			}
		}

		m, err := catalog.FetchGitHubCatalog(ctx, target, a.getHTTPClient())
		if err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: fetching github catalog %s: %v\n", source, err)
			return ExitGeneral
		}
		manifest = m
	}

	if manifest == nil || len(manifest.Items) == 0 {
		_, _ = fmt.Fprintf(a.Stderr, "error: no skills found in catalog %q\n", source)
		return ExitNotFound
	}

	// Save cache
	if err := catalog.SaveNamedCatalogCache(catName, catURL, *manifest, etag); err != nil {
		a.logError("saving catalog cache: %v", err)
	}

	// Save state record
	st := a.loadState()
	rec := state.CatalogRecord{
		Name:       catName,
		Type:       catType,
		Source:     source,
		URL:        catURL,
		Ref:        refFlag,
		Subpath:    subpathFlag,
		SkillCount: len(manifest.Items),
		AddedAt:    time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	st.AddCatalog(rec)
	a.saveState(st)

	// Dynamically register into current store
	a.Store.AddCatalogManifest(catName, manifest)

	switch mode {
	case OutputModeJSON:
		type AddResult struct {
			Name       string `json:"name"`
			Type       string `json:"type"`
			Source     string `json:"source"`
			URL        string `json:"url,omitempty"`
			SkillCount int    `json:"skill_count"`
			Status     string `json:"status"`
		}
		res := AddResult{
			Name:       catName,
			Type:       catType,
			Source:     source,
			URL:        catURL,
			SkillCount: len(manifest.Items),
			Status:     "added",
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return ExitSuccess
	case OutputModeRaw:
		_, _ = fmt.Fprintf(a.Stdout, "added\t%s\t%s\t%d\n", catName, catType, len(manifest.Items))
		return ExitSuccess
	default:
		_, _ = fmt.Fprintf(a.Stdout, "✓ Added catalog %q (%s, %d skills indexed)\n", catName, catType, len(manifest.Items))
		for _, it := range manifest.Items {
			_, _ = fmt.Fprintf(a.Stdout, "  • %-20s [%s] %s\n", it.Name, it.Category, it.Description)
		}
		return ExitSuccess
	}
}

func (a *App) runCatalogList(args []string) int {
	fs := flag.NewFlagSet("catalog list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag bool
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	if _, err := parseFlagsWithArgs(fs, args); err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	st := a.loadState()
	registered := st.GetCatalogs()

	type CatalogView struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Source     string `json:"source"`
		URL        string `json:"url,omitempty"`
		SkillCount int    `json:"skill_count"`
		UpdatedAt  string `json:"updated_at,omitempty"`
	}

	var views []CatalogView

	defaultURL := config.DefaultCatalogURL
	if a.Config != nil && a.Config.GetCatalogURL("default") != "" {
		defaultURL = a.Config.GetCatalogURL("default")
	}

	for _, c := range registered {
		src := c.Source
		if src == "" {
			src = c.URL
		}
		if c.Name == "default" {
			src = defaultURL
			// Count skills in default catalog
			defSkills := 0
			if all := a.Store.All(); len(all) > 0 {
				for _, it := range all {
					if it.Source == "remote" || it.Source == "" || strings.Contains(it.Source, "skills.danicat.dev") || it.Source == "default" {
						defSkills++
					}
				}
			}
			if defSkills > 0 {
				c.SkillCount = defSkills
			}
		}

		upStr := ""
		if !c.UpdatedAt.IsZero() {
			upStr = c.UpdatedAt.Format("2006-01-02 15:04")
		}
		views = append(views, CatalogView{
			Name:       c.Name,
			Type:       c.Type,
			Source:     src,
			URL:        c.URL,
			SkillCount: c.SkillCount,
			UpdatedAt:  upStr,
		})
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(views)
		return ExitSuccess
	case OutputModeRaw:
		for _, v := range views {
			_, _ = fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%d\t%s\n", v.Name, v.Type, v.Source, v.SkillCount, v.UpdatedAt)
		}
		return ExitSuccess
	default:
		_, _ = fmt.Fprintf(a.Stdout, "CONFIGURED CATALOGS (%d total):\n\n", len(views))
		_, _ = fmt.Fprintf(a.Stdout, "%-22s %-10s %-42s %-8s %s\n", "NAME", "TYPE", "SOURCE", "SKILLS", "UPDATED")
		_, _ = fmt.Fprintf(a.Stdout, "%-22s %-10s %-42s %-8s %s\n", "----------------------", "----------", "------------------------------------------", "--------", "-------------------")
		for _, v := range views {
			up := v.UpdatedAt
			if up == "" {
				up = "built-in"
			}
			_, _ = fmt.Fprintf(a.Stdout, "%-22s %-10s %-42s %-8d %s\n", v.Name, v.Type, v.Source, v.SkillCount, up)
		}
		return ExitSuccess
	}
}

func (a *App) runCatalogRemove(args []string) int {
	fs := flag.NewFlagSet("catalog remove", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag bool
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: catalog remove requires catalog name(s)")
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	st := a.loadState()
	var results []struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Message string `json:"message,omitempty"`
	}

	hasNotFound := false
	hasError := false
	for _, name := range posArgs {
		if strings.EqualFold(name, "default") {
			hasError = true
			results = append(results, struct {
				Name    string `json:"name"`
				Status  string `json:"status"`
				Message string `json:"message,omitempty"`
			}{
				Name:    name,
				Status:  "error",
				Message: "cannot remove default catalog",
			})
			continue
		}

		if st.RemoveCatalog(name) {
			_ = catalog.DeleteNamedCatalogCache(name)
			a.Store.RemoveCatalogItems(name)
			results = append(results, struct {
				Name    string `json:"name"`
				Status  string `json:"status"`
				Message string `json:"message,omitempty"`
			}{
				Name:   name,
				Status: "removed",
			})
		} else {
			hasNotFound = true
			results = append(results, struct {
				Name    string `json:"name"`
				Status  string `json:"status"`
				Message string `json:"message,omitempty"`
			}{
				Name:    name,
				Status:  "not_found",
				Message: "catalog not found",
			})
		}
	}

	a.saveState(st)

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		if hasError {
			return ExitUsage
		}
		if hasNotFound {
			return ExitNotFound
		}
		return ExitSuccess
	case OutputModeRaw:
		for _, r := range results {
			_, _ = fmt.Fprintf(a.Stdout, "%s\t%s\t%s\n", r.Status, r.Name, r.Message)
		}
		if hasError {
			return ExitUsage
		}
		if hasNotFound {
			return ExitNotFound
		}
		return ExitSuccess
	default:
		for _, r := range results {
			switch r.Status {
			case "removed":
				_, _ = fmt.Fprintf(a.Stdout, "✓ Removed catalog %q\n", r.Name)
			case "not_found":
				_, _ = fmt.Fprintf(a.Stderr, "⚠ Catalog %q not found.\n", r.Name)
			default:
				_, _ = fmt.Fprintf(a.Stderr, "✗ Failed to remove catalog %q: %s\n", r.Name, r.Message)
			}
		}
		if hasError {
			return ExitUsage
		}
		if hasNotFound {
			return ExitNotFound
		}
		return ExitSuccess
	}
}

func (a *App) syncRegisteredCatalogs(ctx context.Context, st *state.State, mode OutputMode) int {
	targets := st.GetCatalogs()

	type SyncResult struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		SkillCount int    `json:"skill_count"`
		Status     string `json:"status"`
		Message    string `json:"message,omitempty"`
	}

	var results []SyncResult
	for _, cat := range targets {
		var m *catalog.Manifest
		var etag string
		var err error

		switch cat.Type {
		case state.CatalogTypeDefault:
			defaultURL := config.DefaultCatalogURL
			if envURL := os.Getenv("KUNGFU_URL"); envURL != "" {
				defaultURL = envURL
			} else if envURL := os.Getenv("KUNGFU_REMOTE_URL"); envURL != "" {
				defaultURL = envURL
			} else if a.Config != nil && a.Config.GetCatalogURL("default") != "" {
				defaultURL = a.Config.GetCatalogURL("default")
			}
			defaultURL = catalog.ResolveCatalogURL(defaultURL)
			cat.Source = defaultURL
			cat.URL = defaultURL
			m, etag, err = catalog.FetchHTTPCatalog(ctx, defaultURL, a.getHTTPClient())
			if err == nil && m != nil {
				// Save to index.json cache
				cachePath := catalog.GetCatalogCachePath()
				if a.Options.CacheDir != "" {
					cachePath = filepath.Join(a.Options.CacheDir, "index.json")
				}
				_ = catalog.SaveCachedCatalog(cachePath, defaultURL, *m, etag)
				if syncer, ok := a.Store.(interface {
					Sync(context.Context, bool) error
				}); ok {
					_ = syncer.Sync(ctx, true)
				}
			}
		case state.CatalogTypeGitHub:
			target, ok := catalog.ParseGitHubTarget(cat.Source)
			if ok {
				if cat.Ref != "" {
					target.Ref = cat.Ref
				}
				if cat.Subpath != "" {
					target.Subpath = cat.Subpath
				}
				m, err = catalog.FetchGitHubCatalog(ctx, target, a.getHTTPClient())
			}
		case state.CatalogTypeHTTP:
			m, etag, err = catalog.FetchHTTPCatalog(ctx, cat.Source, a.getHTTPClient())
		case state.CatalogTypeLocal:
			m, err = catalog.DiscoverLocalCatalog(cat.Source)
		}

		if err != nil || m == nil {
			results = append(results, SyncResult{
				Name:    cat.Name,
				Type:    cat.Type,
				Status:  "error",
				Message: fmt.Sprintf("%v", err),
			})
			continue
		}

		if cat.Type != state.CatalogTypeDefault {
			_ = catalog.SaveNamedCatalogCache(cat.Name, cat.URL, *m, etag)
			a.Store.AddCatalogManifest(cat.Name, m)
		}
		cat.SkillCount = len(m.Items)
		cat.UpdatedAt = time.Now().UTC()
		st.AddCatalog(cat)

		results = append(results, SyncResult{
			Name:       cat.Name,
			Type:       cat.Type,
			SkillCount: len(m.Items),
			Status:     "synced",
		})
	}

	a.saveState(st)

	hasError := false
	for _, r := range results {
		if r.Status == "error" {
			hasError = true
			break
		}
	}

	exitCode := ExitSuccess
	if hasError {
		exitCode = ExitGeneral
	}

	if mode == OutputModeHuman && len(results) > 0 {
		_, _ = fmt.Fprintf(a.Stdout, "Synchronized %d catalogs:\n", len(results))
		for _, r := range results {
			if r.Status == "synced" {
				_, _ = fmt.Fprintf(a.Stdout, "  ✓ %-22s (%s, %d skills indexed)\n", r.Name, r.Type, r.SkillCount)
			} else {
				_, _ = fmt.Fprintf(a.Stderr, "  ✗ %-22s (%s: %s)\n", r.Name, r.Type, r.Message)
			}
		}
		_, _ = fmt.Fprintln(a.Stdout)
	}

	if err := a.syncTelemetryIfEnabled(ctx, mode); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", err)
		if exitCode == ExitSuccess {
			exitCode = MapErrorToExitCode(err)
		}
	}

	return exitCode
}
