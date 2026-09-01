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
	"github.com/danicat/kungfu/internal/state"
)

func (a *App) RunCatalog(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(a.Stdout, `USAGE:
  kungfu catalog <subcommand> [flags] [arguments...]

SUBCOMMANDS:
  add <source> [--name <name>] [--ref <ref>] [--subpath <path>] [--refresh] [--json] [--raw]
      Register a skill catalog from a GitHub repository, HTTP manifest, or local directory.
  list [--json] [--raw]
      List all registered skill catalogs and default sources.
  remove <name> [--json] [--raw]
      Remove a registered catalog source.
  sync [name] [--json] [--raw]
      Synchronize and refresh skill indexes for one or all registered catalogs.

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
	case "sync", "refresh", "update":
		return a.runCatalogSync(subArgs)
	default:
		fmt.Fprintf(a.Stderr, "error: unknown catalog subcommand %q. Run 'kungfu catalog --help' for usage.\n", subcmd)
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
		fmt.Fprintln(a.Stderr, "error: catalog add requires a source (GitHub repository 'owner/repo', HTTP URL, or local directory)")
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
			a.logError("discovering local catalog %s: %v", source, err)
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
			catName = strings.TrimSuffix(catName, "/catalog.json")
			catName = strings.TrimSuffix(catName, "/")
		}
		m, tag, err := catalog.FetchHTTPCatalog(ctx, source, a.getHTTPClient())
		if err != nil {
			a.logError("fetching http catalog %s: %v", source, err)
			return ExitGeneral
		}
		manifest = m
		etag = tag
	} else {
		// 3. GitHub repository
		target, ok := catalog.ParseGitHubTarget(source)
		if !ok {
			fmt.Fprintf(a.Stderr, "error: invalid catalog source %q. Expected GitHub repository (e.g. google/skills), HTTP URL, or local directory path.\n", source)
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
			a.logError("fetching github catalog %s: %v", source, err)
			return ExitGeneral
		}
		manifest = m
	}

	if manifest == nil || len(manifest.Items) == 0 {
		fmt.Fprintf(a.Stderr, "error: no skills found in catalog %q\n", source)
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
		fmt.Fprintf(a.Stdout, "added\t%s\t%s\t%d\n", catName, catType, len(manifest.Items))
		return ExitSuccess
	default:
		fmt.Fprintf(a.Stdout, "✓ Added catalog %q (%s, %d skills indexed)\n", catName, catType, len(manifest.Items))
		for _, it := range manifest.Items {
			fmt.Fprintf(a.Stdout, "  • %-20s [%s] %s\n", it.Name, it.Category, it.Description)
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

	// Default catalog
	defSkills := 0
	if all := a.Store.All(); len(all) > 0 {
		for _, it := range all {
			if it.Source == "remote" || it.Source == "" || strings.Contains(it.Source, "skills.danicat.dev") {
				defSkills++
			}
		}
	}
	views = append(views, CatalogView{
		Name:       "default",
		Type:       state.CatalogTypeDefault,
		Source:     "https://skills.danicat.dev/catalog.json",
		URL:        "https://skills.danicat.dev/catalog.json",
		SkillCount: defSkills,
	})

	for _, c := range registered {
		views = append(views, CatalogView{
			Name:       c.Name,
			Type:       c.Type,
			Source:     c.Source,
			URL:        c.URL,
			SkillCount: c.SkillCount,
			UpdatedAt:  c.UpdatedAt.Format("2006-01-02 15:04"),
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
			fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%d\t%s\n", v.Name, v.Type, v.Source, v.SkillCount, v.UpdatedAt)
		}
		return ExitSuccess
	default:
		fmt.Fprintf(a.Stdout, "CONFIGURED CATALOGS (%d total):\n\n", len(views))
		fmt.Fprintf(a.Stdout, "%-22s %-10s %-42s %-8s %s\n", "NAME", "TYPE", "SOURCE", "SKILLS", "UPDATED")
		fmt.Fprintf(a.Stdout, "%-22s %-10s %-42s %-8s %s\n", "----------------------", "----------", "------------------------------------------", "--------", "-------------------")
		for _, v := range views {
			up := v.UpdatedAt
			if up == "" {
				up = "built-in"
			}
			fmt.Fprintf(a.Stdout, "%-22s %-10s %-42s %-8d %s\n", v.Name, v.Type, v.Source, v.SkillCount, up)
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
		fmt.Fprintln(a.Stderr, "error: catalog remove requires catalog name(s)")
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
	for _, name := range posArgs {
		if strings.EqualFold(name, "default") {
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
		if hasNotFound {
			return ExitNotFound
		}
		return ExitSuccess
	case OutputModeRaw:
		for _, r := range results {
			fmt.Fprintf(a.Stdout, "%s\t%s\t%s\n", r.Status, r.Name, r.Message)
		}
		if hasNotFound {
			return ExitNotFound
		}
		return ExitSuccess
	default:
		for _, r := range results {
			switch r.Status {
			case "removed":
				fmt.Fprintf(a.Stdout, "✓ Removed catalog %q\n", r.Name)
			case "not_found":
				fmt.Fprintf(a.Stderr, "⚠ Catalog %q not found.\n", r.Name)
			default:
				fmt.Fprintf(a.Stderr, "✗ Failed to remove catalog %q: %s\n", r.Name, r.Message)
			}
		}
		if hasNotFound {
			return ExitNotFound
		}
		return ExitSuccess
	}
}

func (a *App) runCatalogSync(args []string) int {
	fs := flag.NewFlagSet("catalog sync", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag bool
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	st := a.loadState()
	targets := st.GetCatalogs()

	if len(posArgs) > 0 {
		targetMap := make(map[string]bool)
		for _, p := range posArgs {
			targetMap[p] = true
		}
		var filtered []state.CatalogRecord
		for _, c := range targets {
			if targetMap[c.Name] {
				filtered = append(filtered, c)
			}
		}
		targets = filtered
	}

	if len(targets) == 0 && len(posArgs) > 0 {
		fmt.Fprintf(a.Stderr, "error: none of the specified catalogs were found in registry\n")
		return ExitNotFound
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

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

		_ = catalog.SaveNamedCatalogCache(cat.Name, cat.URL, *m, etag)
		cat.SkillCount = len(m.Items)
		cat.UpdatedAt = time.Now().UTC()
		st.AddCatalog(cat)
		a.Store.AddCatalogManifest(cat.Name, m)

		results = append(results, SyncResult{
			Name:       cat.Name,
			Type:       cat.Type,
			SkillCount: len(m.Items),
			Status:     "synced",
		})
	}

	a.saveState(st)

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		return ExitSuccess
	case OutputModeRaw:
		for _, r := range results {
			fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%d\n", r.Status, r.Name, r.Type, r.SkillCount)
		}
		return ExitSuccess
	default:
		fmt.Fprintf(a.Stdout, "Synchronized %d catalogs:\n", len(results))
		for _, r := range results {
			if r.Status == "synced" {
				fmt.Fprintf(a.Stdout, "  ✓ %-22s (%s, %d skills indexed)\n", r.Name, r.Type, r.SkillCount)
			} else {
				fmt.Fprintf(a.Stderr, "  ✗ %-22s (%s: %s)\n", r.Name, r.Type, r.Message)
			}
		}
		return ExitSuccess
	}
}
