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
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/config"
)

func validateCatalogName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != name {
		return fmt.Errorf("catalog name cannot be empty or have leading/trailing whitespace")
	}
	if config.IsBuiltinCatalog(trimmed) {
		return fmt.Errorf("cannot use reserved built-in catalog name %q", trimmed)
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") || strings.Contains(trimmed, "//") || strings.Contains(trimmed, "..") {
		return fmt.Errorf("invalid catalog name %q", trimmed)
	}
	for _, r := range trimmed {
		if r == '@' || r == '\\' || r == ':' || r == 0 || unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("invalid catalog name %q: contains forbidden character", trimmed)
		}
	}
	return nil
}

func expandHomePath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", fmt.Errorf("expanding ~ in %q: %w", p, err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func (a *App) RunCatalog(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_ = PrintSubcommandHelp(a.Stdout, "catalog", nil)
		return ExitSuccess
	}

	subcmd := args[0]
	subArgs := args[1:]

	switch subcmd {
	case "add":
		return a.runCatalogAdd(subArgs)
	case "list":
		return a.runCatalogList(subArgs)
	case "remove":
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
	var forceFlag, jsonFlag bool

	fs.StringVar(&nameFlag, "name", "", "Custom alias/name for the catalog")
	fs.StringVar(&nameFlag, "n", "", "Custom alias/name for the catalog (shorthand)")
	fs.StringVar(&refFlag, "ref", "", "Branch or tag ref for git repository")
	fs.StringVar(&subpathFlag, "subpath", "", "Subpath directory inside repository")
	fs.StringVar(&subpathFlag, "p", "", "Subpath directory inside repository (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Overwrite an existing catalog with the same name")
	fs.BoolVar(&forceFlag, "f", false, "Overwrite an existing catalog (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: catalog add requires a source (GitHub repository 'owner/repo', HTTPS URL, or local directory)")
		return ExitUsage
	}
	if len(posArgs) > 1 {
		_, _ = fmt.Fprintf(a.Stderr, "error: catalog add accepts a single source argument (unexpected %q)\n", posArgs[1])
		return ExitUsage
	}

	source := posArgs[0]
	mode := a.resolveMode(jsonFlag)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var manifest *catalog.Manifest
	var catType string
	var catName string
	var persistedSource string
	var catURL string
	var etag string

	isExplicitLocalPath := strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~") || strings.HasPrefix(source, `\`)
	expandedSource := source
	if isExplicitLocalPath {
		exp, expErr := expandHomePath(source)
		if expErr != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", expErr)
			return ExitGeneral
		}
		expandedSource = exp
	}

	isHTTPURL := false
	isGitHubURL := false
	if u, uErr := url.Parse(source); uErr == nil && (u.Scheme == "http" || u.Scheme == "https") {
		host := strings.ToLower(u.Hostname())
		if host == "github.com" || host == "raw.githubusercontent.com" {
			isGitHubURL = !strings.HasSuffix(strings.ToLower(u.Path), ".json")
			isHTTPURL = !isGitHubURL
		} else {
			isHTTPURL = true
		}
	}

	if info, statErr := os.Stat(expandedSource); statErr == nil {
		if !info.IsDir() {
			_, _ = fmt.Fprintf(a.Stderr, "error: local catalog source %q is a file, expected a directory\n", source)
			return ExitUsage
		}
		absPath, absErr := filepath.Abs(expandedSource)
		if absErr != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: resolving path %q: %v\n", expandedSource, absErr)
			return ExitGeneral
		}
		catType = "local"
		persistedSource = absPath
		catURL = absPath
		catName = nameFlag
		if catName == "" {
			catName = filepath.Base(absPath)
		}
	} else if isExplicitLocalPath {
		_, _ = fmt.Fprintf(a.Stderr, "error: local catalog directory %q not found: %v\n", source, statErr)
		return MapErrorToExitCode(statErr)
	} else if isHTTPURL {
		catType = "http"
		persistedSource = source
		catURL = source
		catName = nameFlag
		if catName == "" {
			u, _ := url.Parse(source)
			hostPath := u.Hostname() + strings.TrimRight(u.Path, "/")
			hostPath = strings.TrimSuffix(hostPath, "/.well-known/agent-skills/index.json")
			hostPath = strings.TrimSuffix(hostPath, "/index.json")
			catName = strings.TrimRight(hostPath, "/")
		}
	} else {
		target, ok := catalog.ParseGitHubTarget(source)
		if !ok {
			_, _ = fmt.Fprintf(a.Stderr, "error: invalid catalog source %q. Expected GitHub repository (e.g. google/skills), HTTPS URL, or local directory path.\n", source)
			return ExitUsage
		}
		if refFlag != "" {
			target.Ref = refFlag
		}
		if subpathFlag != "" {
			target.Subpath = subpathFlag
		}
		catType = "github"
		persistedSource = target.CanonicalSource()
		catURL = target.FullRepoURL()
		catName = nameFlag
		if catName == "" {
			catName = target.RepoString()
			if target.Subpath != "" {
				catName += "/" + target.Subpath
			}
		}
	}

	if err := validateCatalogName(catName); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return ExitUsage
	}

	if existingName, _, exists := a.lookupConfiguredCatalog(catName); exists && !forceFlag {
		_, _ = fmt.Fprintf(a.Stderr, "error: catalog %q already exists; pass -f/--force to overwrite\n", existingName)
		return ExitUsage
	}

	m, tag, resolvedType, fetchErr := catalog.FetchSource(ctx, persistedSource, a.getHTTPClient())
	if fetchErr != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: fetching %s catalog %s: %v\n", catType, source, fetchErr)
		return MapErrorToExitCode(fetchErr)
	}
	manifest = m
	etag = tag
	if resolvedType != "" {
		catType = resolvedType
	}

	if manifest == nil || len(manifest.Items) == 0 {
		_, _ = fmt.Fprintf(a.Stderr, "error: no skills found in catalog %q\n", source)
		return ExitNotFound
	}

	// Persist catalog in config.yaml (single source of truth).
	if a.Config == nil {
		a.Config = config.DefaultConfig()
	}
	if a.Config.Catalogs == nil {
		a.Config.Catalogs = make(map[string]string)
	}
	a.Config.Catalogs[catName] = persistedSource
	if _, err := config.Save(a.Options.Config, a.Config); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: saving catalog to config: %v\n", err)
		return ExitGeneral
	}

	if cacheRoot := a.cacheRootDir(); cacheRoot != "" {
		if err := catalog.SaveNamedCatalogCache(cacheRoot, catName, catURL, *manifest, etag); err != nil && a.Options.Verbose {
			_, _ = fmt.Fprintf(a.Stderr, "warning: saving catalog cache: %v\n", err)
		}
	}
	a.Store.AddCatalogManifest(catName, manifest)

	if mode == OutputModeJSON {
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
			Source:     persistedSource,
			URL:        catURL,
			SkillCount: len(manifest.Items),
			Status:     "added",
		}
		if err := writeJSON(a.Stdout, res); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
		return ExitSuccess
	}

	_, _ = fmt.Fprintf(a.Stdout, "✓ Added catalog %q (%s, %d skills indexed)\n", catName, catType, len(manifest.Items))
	for _, it := range manifest.Items {
		_, _ = fmt.Fprintf(a.Stdout, "  • %-20s %s\n", it.Name, singleLineDesc(it.Description))
	}
	return ExitSuccess
}

func (a *App) runCatalogList(args []string) int {
	fs := flag.NewFlagSet("catalog list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag bool
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(posArgs) > 0 {
		_, _ = fmt.Fprintf(a.Stderr, "error: catalog list does not accept positional arguments (got %q)\n", posArgs[0])
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag)
	configured := a.configuredCatalogs()
	cacheRoot := a.cacheRootDir()

	type CatalogView struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Source     string `json:"source"`
		SkillCount int    `json:"skill_count"`
		UpdatedAt  string `json:"updated_at,omitempty"`
	}

	names := make([]string, 0, len(configured))
	for k := range configured {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "default" {
			return true
		}
		if names[j] == "default" {
			return false
		}
		return names[i] < names[j]
	})

	views := make([]CatalogView, 0, len(names))
	for _, name := range names {
		src := configured[name]
		catType := catalog.ClassifySource(src)
		if name == "default" {
			catType = "default"
		}
		skillCount := 0
		upStr := ""
		if c, err := catalog.LoadNamedCatalogCache(cacheRoot, name); err == nil && c != nil {
			skillCount = len(c.Manifest.Items)
			if !c.FetchedAt.IsZero() {
				upStr = c.FetchedAt.Format("2006-01-02 15:04")
			}
		} else if name == "default" && a.Store != nil {
			for _, it := range a.Store.All() {
				if it.Source == "" || it.Source == "default" || it.Source == "remote" || it.Source == src {
					skillCount++
				}
			}
		}

		views = append(views, CatalogView{
			Name:       name,
			Type:       catType,
			Source:     src,
			SkillCount: skillCount,
			UpdatedAt:  upStr,
		})
	}

	if mode == OutputModeJSON {
		if err := writeJSON(a.Stdout, views); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
		return ExitSuccess
	}

	nameW, typeW, srcW, skillsW, updatedW := 22, 10, 42, 8, 19
	for _, v := range views {
		nameW = max(nameW, len(v.Name))
		typeW = max(typeW, len(v.Type))
		dispSrc := formatDisplaySource(v.Source)
		srcW = max(srcW, len(dispSrc))
		skillsW = max(skillsW, len(fmt.Sprintf("%d", v.SkillCount)))
		up := v.UpdatedAt
		if up == "" {
			up = "built-in"
		}
		updatedW = max(updatedW, len(up))
	}

	_, _ = fmt.Fprintf(a.Stdout, "CONFIGURED CATALOGS (%d total):\n\n", len(views))
	_, _ = fmt.Fprintf(a.Stdout, "%-*s %-*s %-*s %-*s %s\n", nameW, "NAME", typeW, "TYPE", srcW, "SOURCE", skillsW, "SKILLS", "UPDATED")
	_, _ = fmt.Fprintf(a.Stdout, "%s %s %s %s %s\n",
		strings.Repeat("-", nameW),
		strings.Repeat("-", typeW),
		strings.Repeat("-", srcW),
		strings.Repeat("-", skillsW),
		strings.Repeat("-", updatedW),
	)
	for _, v := range views {
		up := v.UpdatedAt
		if up == "" {
			up = "built-in"
		}
		_, _ = fmt.Fprintf(a.Stdout, "%-*s %-*s %-*s %-*d %s\n", nameW, v.Name, typeW, v.Type, srcW, formatDisplaySource(v.Source), skillsW, v.SkillCount, up)
	}
	return ExitSuccess
}

func formatDisplaySource(src string) string {
	src = strings.TrimPrefix(src, "https://")
	src = strings.TrimPrefix(src, "http://")
	return src
}

func (a *App) runCatalogRemove(args []string) int {
	fs := flag.NewFlagSet("catalog remove", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag bool
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: catalog remove requires catalog name(s)")
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag)
	if a.Config == nil {
		a.Config = config.DefaultConfig()
	}

	type RemoveResult struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Message string `json:"message,omitempty"`
	}

	results := make([]RemoveResult, 0, len(posArgs))
	hasNotFound := false
	hasError := false
	modified := false

	for _, name := range posArgs {
		if config.IsBuiltinCatalog(name) {
			hasError = true
			results = append(results, RemoveResult{
				Name:    name,
				Status:  "error",
				Message: fmt.Sprintf("cannot remove built-in catalog %q", name),
			})
			continue
		}

		matchedKey := ""
		if a.Config.Catalogs != nil {
			if _, ok := a.Config.Catalogs[name]; ok {
				matchedKey = name
			} else {
				for k := range a.Config.Catalogs {
					if strings.EqualFold(k, name) {
						matchedKey = k
						break
					}
				}
			}
		}

		if matchedKey != "" {
			delete(a.Config.Catalogs, matchedKey)
			modified = true
			_ = catalog.DeleteNamedCatalogCache(a.cacheRootDir(), matchedKey)
			a.Store.RemoveCatalogItems(matchedKey)
			results = append(results, RemoveResult{
				Name:   matchedKey,
				Status: "removed",
			})
		} else {
			hasNotFound = true
			results = append(results, RemoveResult{
				Name:    name,
				Status:  "not_found",
				Message: "catalog not found",
			})
		}
	}

	saveFailed := false
	if modified {
		if _, err := config.Save(a.Options.Config, a.Config); err != nil {
			saveFailed = true
			_, _ = fmt.Fprintf(a.Stderr, "error: saving config after catalog removal: %v\n", err)
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
			case "removed":
				_, _ = fmt.Fprintf(a.Stdout, "✓ Removed catalog %q\n", r.Name)
			case "not_found":
				_, _ = fmt.Fprintf(a.Stderr, "⚠ Catalog %q not found.\n", r.Name)
			default:
				_, _ = fmt.Fprintf(a.Stderr, "✗ Failed to remove catalog %q: %s\n", r.Name, r.Message)
			}
		}
	}

	if saveFailed {
		return ExitGeneral
	}
	if hasError {
		return ExitUsage
	}
	if hasNotFound {
		return ExitNotFound
	}
	return ExitSuccess
}

func (a *App) syncRegisteredCatalogs(ctx context.Context, mode OutputMode) int {
	configured := a.configuredCatalogs()
	names := make([]string, 0, len(configured))
	for k := range configured {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "default" {
			return true
		}
		if names[j] == "default" {
			return false
		}
		return names[i] < names[j]
	})

	type SyncResult struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		SkillCount int    `json:"skill_count"`
		Status     string `json:"status"`
		Message    string `json:"message,omitempty"`
	}

	results := make([]SyncResult, 0, len(names))
	errExitCode := ExitSuccess
	cacheRoot := a.cacheRootDir()

	for _, name := range names {
		src := configured[name]
		m, etag, catType, err := catalog.FetchSource(ctx, src, a.getHTTPClient())
		if name == "default" {
			catType = "default"
		}
		if err != nil || m == nil {
			msg := "empty catalog manifest"
			if err != nil {
				msg = err.Error()
				if code := MapErrorToExitCode(err); errExitCode == ExitSuccess || errExitCode == ExitGeneral {
					errExitCode = code
				}
			}
			results = append(results, SyncResult{
				Name:    name,
				Type:    catType,
				Status:  "error",
				Message: msg,
			})
			continue
		}

		if cacheRoot != "" {
			_ = catalog.SaveNamedCatalogCache(cacheRoot, name, src, *m, etag)
		}
		if a.Store != nil {
			a.Store.AddCatalogManifest(name, m)
		}

		results = append(results, SyncResult{
			Name:       name,
			Type:       catType,
			SkillCount: len(m.Items),
			Status:     "synced",
		})
	}

	hasError := false
	for _, r := range results {
		if r.Status == "error" {
			hasError = true
			break
		}
	}

	exitCode := ExitSuccess
	if hasError {
		if errExitCode != ExitSuccess {
			exitCode = errExitCode
		} else {
			exitCode = ExitGeneral
		}
	}

	if cacheRoot != "" {
		_ = catalog.PruneNamedCatalogCaches(cacheRoot, names)
	}
	a.pruneSkillCache(!hasError)

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

	return exitCode
}

func (a *App) pruneSkillCache(removeOrphans bool) {
	cacheRoot := a.cacheRootDir()
	if cacheRoot == "" || a.Store == nil {
		return
	}
	skillsDir := filepath.Join(cacheRoot, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return
	}

	activeByKey := make(map[string]catalog.Item)
	for _, it := range a.Store.All() {
		if key, err := catalog.SkillCacheKey(it.Name, it.Source); err == nil {
			activeByKey[key] = it
		}
		if key, err := catalog.SkillCacheKey(it.Name, ""); err == nil {
			if _, exists := activeByKey[key]; !exists {
				activeByKey[key] = it
			}
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		skillCachePath := filepath.Join(skillsDir, dirName)
		it, exists := activeByKey[dirName]
		if !exists {
			if removeOrphans {
				_ = os.RemoveAll(skillCachePath)
			}
			continue
		}

		if catalog.IsArchiveItem(it) {
			expected := it.EffectiveDigest()
			if expected != "" && !catalog.IsArchiveCachedWithDigest(cacheRoot, it.Name, it.Source, expected) {
				_ = os.RemoveAll(skillCachePath)
			}
			continue
		}

		if it.Type == catalog.ItemTypeSkillMD {
			if subEntries, sErr := os.ReadDir(skillCachePath); sErr == nil {
				for _, sub := range subEntries {
					if sub.Name() != "SKILL.md" {
						_ = os.RemoveAll(filepath.Join(skillCachePath, sub.Name()))
					}
				}
			}
		}
	}
}
