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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/search"
	"github.com/danicat/kungfu/internal/state"
)

// Exit codes
const (
	ExitSuccess   = 0
	ExitGeneral   = 1
	ExitUsage     = 2
	ExitNotFound  = 3
	ExitNetwork   = 4
	ExitIntegrity = 5
)

type OutputMode string

const (
	OutputModeRaw   OutputMode = "raw"
	OutputModeJSON  OutputMode = "json"
	OutputModeHuman OutputMode = "human"
)

type GlobalOptions struct {
	Mode     OutputMode
	Config   string
	Verbose  bool
	NoColor  bool
	CacheDir string
}

type App struct {
	Store      catalog.Store
	Searcher   *search.Engine
	Stdout     io.Writer
	Stderr     io.Writer
	Options    GlobalOptions
	HTTPClient *http.Client
}

func (a *App) getHTTPClient() *http.Client {
	if a != nil && a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func NewApp(opts GlobalOptions, stdout, stderr io.Writer) (*App, error) {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	catOpts := []catalog.Option{
		catalog.WithRemoteSync(true),
		catalog.WithEmbeddedFS(embeddedSkills),
	}
	if opts.CacheDir != "" {
		catOpts = append(catOpts, catalog.WithCacheDir(opts.CacheDir))
	}

	store, err := catalog.NewCatalog(catOpts...)
	if err != nil {
		return nil, fmt.Errorf("initializing catalog store: %w", err)
	}

	engine := search.NewEngine()
	return &App{
		Store:      store,
		Searcher:   engine,
		Stdout:     stdout,
		Stderr:     stderr,
		Options:    opts,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (a *App) loadState() *state.State {
	s, err := state.LoadState("")
	if err != nil {
		path := state.DefaultStateFilePath()
		if data, rerr := os.ReadFile(path); rerr == nil && len(data) > 0 {
			backupPath := fmt.Sprintf("%s.corrupted.%d", path, time.Now().Unix())
			_ = os.WriteFile(backupPath, data, 0644)
			fmt.Fprintf(a.Stderr, "warning: state file corrupted; backed up to %s\n", backupPath)
		}
		s = state.NewState()
	}

	wd, _ := os.Getwd()
	homeDir, _ := os.UserHomeDir()

	isCatalogKnown := func(name string) bool {
		if a.Store != nil {
			if it, err := a.Store.Get(name); err == nil && it != nil {
				return true
			}
		}
		return false
	}

	if s.ReconcileDisk(wd, homeDir, isCatalogKnown) {
		_ = state.SaveState("", s)
	}

	return s
}

func (a *App) saveState(s *state.State) {
	_ = state.SaveState("", s)
}

func parseFlagsWithArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags []string
	var posArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// POSIX end-of-flags delimiter: all subsequent arguments are positional
			posArgs = append(posArgs, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			flagName := strings.TrimLeft(arg, "-")
			if strings.Contains(flagName, "=") {
				continue
			}
			f := fs.Lookup(flagName)
			if f != nil {
				if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
					continue
				}
				// Non-boolean flag consumes the next token as its value
				if i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
			}
		} else {
			posArgs = append(posArgs, arg)
		}
	}
	var reordered []string
	reordered = append(reordered, flags...)
	if len(posArgs) > 0 {
		reordered = append(reordered, "--")
		reordered = append(reordered, posArgs...)
	}
	if err := fs.Parse(reordered); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func (a *App) resolveMode(jsonFlag, rawFlag bool) OutputMode {
	if jsonFlag {
		return OutputModeJSON
	}
	if rawFlag {
		return OutputModeRaw
	}
	if a.Options.Mode != "" {
		return a.Options.Mode
	}
	return OutputModeHuman
}

func (a *App) logError(format string, args ...any) {
	if a.Options.Verbose || !strings.Contains(format, "not found") {
		fmt.Fprintf(a.Stderr, "error: "+format+"\n", args...)
	}
}

func (a *App) printNotFoundWithSuggestions(cmd, query string) {
	fmt.Fprintf(a.Stderr, "error: skill %q not found.\n", query)

	var suggestions []string

	// 1. Fuzzy match against all known skill names
	allItems := a.Store.All()
	var skillNames []string
	for _, it := range allItems {
		skillNames = append(skillNames, it.Name)
	}
	skillSuggestions := search.Suggest(query, skillNames, 3)
	for _, s := range skillSuggestions {
		suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s %s", cmd, s))
	}

	// 2. Exact or fuzzy match against categories
	categories := a.Store.Categories()
	var catCandidates []string
	for _, cat := range categories {
		if cat.ID != "" {
			catCandidates = append(catCandidates, cat.ID)
		}
		if cat.Name != "" && !strings.EqualFold(cat.Name, cat.ID) {
			catCandidates = append(catCandidates, cat.Name)
		}
	}
	catMatches := a.Store.ListByFilter(catalog.QueryFilter{Category: query})
	if len(catMatches) > 0 {
		suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s --category %s (%d matching skills)", cmd, query, len(catMatches)))
	} else {
		fuzzyCats := search.Suggest(query, catCandidates, 2)
		for _, fc := range fuzzyCats {
			items := a.Store.ListByFilter(catalog.QueryFilter{Category: fc})
			if len(items) > 0 {
				suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s --category %s (%d matching skills)", cmd, fc, len(items)))
			} else {
				suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s --category %s", cmd, fc))
			}
		}
	}

	// 3. Exact or fuzzy match against tags
	tagMatches := a.Store.ListByFilter(catalog.QueryFilter{TagQuery: query})
	if len(tagMatches) > 0 {
		suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s --tags %s (%d matching skills)", cmd, query, len(tagMatches)))
	}

	if len(suggestions) > 0 {
		fmt.Fprintln(a.Stderr, "\nDid you mean:")
		for _, sug := range suggestions {
			fmt.Fprintln(a.Stderr, sug)
		}
	}
}

func (a *App) printFilterNotFoundWithSuggestions(cmd, category, tagQuery string) {
	if category != "" && tagQuery != "" {
		fmt.Fprintf(a.Stderr, "error: no skills found matching category %q and tags %q\n", category, tagQuery)
	} else if category != "" {
		fmt.Fprintf(a.Stderr, "error: no skills found matching category %q\n", category)
	} else if tagQuery != "" {
		fmt.Fprintf(a.Stderr, "error: no skills found matching tags %q\n", tagQuery)
	}

	var suggestions []string

	if category != "" {
		// 1. Check if category matches or is close to known categories (IDs and Names)
		categories := a.Store.Categories()
		var catCandidates []string
		for _, cat := range categories {
			if cat.ID != "" {
				catCandidates = append(catCandidates, cat.ID)
			}
			if cat.Name != "" && !strings.EqualFold(cat.Name, cat.ID) {
				catCandidates = append(catCandidates, cat.Name)
			}
		}
		fuzzyCats := search.Suggest(category, catCandidates, 3)
		for _, fc := range fuzzyCats {
			items := a.Store.ListByFilter(catalog.QueryFilter{Category: fc})
			if len(items) > 0 {
				suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s --category %s (%d matching skills)", cmd, fc, len(items)))
			} else {
				suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s --category %s", cmd, fc))
			}
		}

		// 2. Check if the user accidentally passed a skill name to --category
		if item, err := a.Store.Get(category); err == nil && item != nil {
			suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s %s (is a skill name, not a category)", cmd, item.Name))
		} else {
			// Check if fuzzy skill match
			allItems := a.Store.All()
			var skillNames []string
			for _, it := range allItems {
				skillNames = append(skillNames, it.Name)
			}
			fuzzySkills := search.Suggest(category, skillNames, 2)
			for _, fs := range fuzzySkills {
				suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s %s", cmd, fs))
			}
		}
	}

	if tagQuery != "" {
		// Suggest matching tags
		allItems := a.Store.All()
		var allTags []string
		tagSet := make(map[string]struct{})
		for _, it := range allItems {
			for _, t := range it.Tags {
				tLower := strings.ToLower(t)
				if _, exists := tagSet[tLower]; !exists {
					tagSet[tLower] = struct{}{}
					allTags = append(allTags, tLower)
				}
			}
		}
		fuzzyTags := search.Suggest(tagQuery, allTags, 3)
		for _, ft := range fuzzyTags {
			items := a.Store.ListByFilter(catalog.QueryFilter{TagQuery: ft})
			if len(items) > 0 {
				suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s --tags %s (%d matching skills)", cmd, ft, len(items)))
			}
		}
	}

	if len(suggestions) > 0 {
		fmt.Fprintln(a.Stderr, "\nDid you mean:")
		for _, sug := range suggestions {
			fmt.Fprintln(a.Stderr, sug)
		}
	}
}

func mapErrorToExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	if errors.Is(err, catalog.ErrItemNotFound) {
		return ExitNotFound
	}
	if errors.Is(err, catalog.ErrDigestMismatch) || errors.Is(err, catalog.ErrInvalidFrontmatter) {
		return ExitIntegrity
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return ExitNetwork
	}
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		if sysErr == syscall.ECONNREFUSED || sysErr == syscall.ENETUNREACH || sysErr == syscall.ETIMEDOUT {
			return ExitNetwork
		}
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "http 404") || strings.Contains(errStr, "not found") || strings.Contains(errStr, "no such file") {
		return ExitNotFound
	}
	if strings.Contains(errStr, "http 5") || strings.Contains(errStr, "http 429") || strings.Contains(errStr, "network") || strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "timeout") {
		return ExitNetwork
	}
	if strings.Contains(errStr, "digest") || strings.Contains(errStr, "checksum") || strings.Contains(errStr, "invalid frontmatter") {
		return ExitIntegrity
	}
	return ExitGeneral
}

func (a *App) getSkillStatus(item *catalog.Item) string {
	if item == nil {
		return "online"
	}
	// 1. Check workspace .agents/skills/<name>
	if wd, err := os.Getwd(); err == nil {
		dir := filepath.Join(wd, ".agents", "skills", item.Name)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return "installed"
		}
	}
	// 2. Check global ~/.agents/skills/<name>
	if homeDir, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(homeDir, ".agents", "skills", item.Name)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return "installed"
		}
	}
	// 3. Check item.Path if explicitly set
	if item.Path != "" {
		if fi, err := os.Stat(item.Path); err == nil && fi.IsDir() {
			return "installed"
		}
	}
	return "online"
}

func (a *App) RunList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag bool

	fs.StringVar(&category, "category", "", "Filter skills by category")
	fs.StringVar(&category, "c", "", "Filter skills by category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Filter skills by boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Filter skills by boolean tag expression (shorthand)")
	fs.StringVar(&tagQuery, "tag", "", "Filter skills by boolean tag expression (alias)")
	fs.BoolVar(&allFlag, "all", false, "List all skills across all categories")
	fs.BoolVar(&allFlag, "a", false, "List all skills (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if category == "" && len(posArgs) > 0 {
		category = posArgs[0]
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	filter := catalog.QueryFilter{
		Category: category,
		TagQuery: tagQuery,
	}

	items := a.Store.ListByFilter(filter)
	categories := a.Store.Categories()

	if (category != "" || tagQuery != "") && len(items) == 0 {
		a.printFilterNotFoundWithSuggestions("list", category, tagQuery)
		return ExitNotFound
	}

	wd, _ := os.Getwd()
	st := a.loadState()
	for i := range items {
		rec, installed := st.GetSkill(state.ScopeWorkspace, wd, items[i].Name)
		if !installed || rec.Status != state.StatusInstalled {
			rec, installed = st.GetSkill(state.ScopeGlobal, "", items[i].Name)
		}
		if installed && rec.Status == state.StatusInstalled {
			items[i].Status = "installed"
			if rec.Origin == state.OriginGitHub || rec.Repo != "" {
				items[i].Source = "github"
				if items[i].Metadata == nil {
					items[i].Metadata = make(map[string]string)
				}
				if rec.Repo != "" {
					items[i].Metadata["repo"] = rec.Repo
				}
				if rec.Ref != "" {
					items[i].Metadata["ref"] = rec.Ref
				}
			}
			if items[i].Version == "" && rec.Version != "" {
				items[i].Version = rec.Version
			}
			if len(items[i].Tags) == 0 && rec.Path != "" {
				skillPath := rec.Path
				if fi, err := os.Stat(skillPath); err == nil && fi.IsDir() {
					skillPath = filepath.Join(rec.Path, "SKILL.md")
				}
				if data, err := os.ReadFile(skillPath); err == nil {
					if fm, _, err := catalog.ParseFrontmatter(data); err == nil && fm != nil && fm.Metadata != nil {
						if len(items[i].Tags) == 0 && fm.Metadata["tags"] != "" {
							items[i].Tags = catalog.ParseTags(fm.Metadata["tags"])
						}
						if items[i].Category == "" && fm.Metadata["category"] != "" {
							items[i].Category = fm.Metadata["category"]
						}
					}
				}
			}
		} else {
			items[i].Status = a.getSkillStatus(&items[i])
		}
	}

	switch mode {
	case OutputModeJSON:
		type ListPayload struct {
			Categories []catalog.Category `json:"categories,omitempty"`
			Items      []catalog.Item     `json:"items"`
			Total      int                `json:"total"`
		}
		payload := ListPayload{Items: items, Total: len(items)}
		if category == "" && tagQuery == "" {
			payload.Categories = categories
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload)
		return ExitSuccess

	case OutputModeRaw:
		var sb strings.Builder
		sb.WriteString("| Name | Version | Category | Tags | Status |\n")
		sb.WriteString("|---|---|---|---|---|\n")
		for _, item := range items {
			ver := item.Version
			if ver == "" && item.Metadata != nil {
				ver = item.Metadata["version"]
			}
			if ver == "" {
				ver = "-"
			}
			tagsStr := strings.Join(item.Tags, ", ")
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n", item.Name, ver, item.Category, tagsStr, item.Status))
		}
		if len(items) > 0 {
			sb.WriteString("\n*Hint: Load the desired skill with `kungfu load <name>` for JIT knowledge, or learn it with `kungfu learn <name>` to install permanently.*\n")
		}
		fmt.Fprint(a.Stdout, sb.String())
		return ExitSuccess

	default: // OutputModeHuman (default)
		nameWidth := 4   // "NAME"
		verWidth := 7    // "VERSION"
		catWidth := 8    // "CATEGORY"
		tagsWidth := 4   // "TAGS"
		statusWidth := 6 // "STATUS"

		for _, it := range items {
			if len(it.Name) > nameWidth {
				nameWidth = len(it.Name)
			}
			ver := it.Version
			if ver == "" && it.Metadata != nil {
				ver = it.Metadata["version"]
			}
			if ver == "" {
				ver = "-"
			}
			if len(ver) > verWidth {
				verWidth = len(ver)
			}
			if len(it.Category) > catWidth {
				catWidth = len(it.Category)
			}
			tagsStr := strings.Join(it.Tags, ", ")
			if len(tagsStr) > tagsWidth {
				tagsWidth = len(tagsStr)
			}
			if len(it.Status) > statusWidth {
				statusWidth = len(it.Status)
			}
		}

		fmt.Fprintf(a.Stdout, "%-*s  %-*s  %-*s  %-*s  %-*s\n", nameWidth, "NAME", verWidth, "VERSION", catWidth, "CATEGORY", tagsWidth, "TAGS", statusWidth, "STATUS")
		fmt.Fprintf(a.Stdout, "%s  %s  %s  %s  %s\n", strings.Repeat("-", nameWidth), strings.Repeat("-", verWidth), strings.Repeat("-", catWidth), strings.Repeat("-", tagsWidth), strings.Repeat("-", statusWidth))
		for _, item := range items {
			ver := item.Version
			if ver == "" && item.Metadata != nil {
				ver = item.Metadata["version"]
			}
			if ver == "" {
				ver = "-"
			}
			tagsStr := strings.Join(item.Tags, ", ")
			fmt.Fprintf(a.Stdout, "%-*s  %-*s  %-*s  %-*s  %-*s\n", nameWidth, item.Name, verWidth, ver, catWidth, item.Category, tagsWidth, tagsStr, statusWidth, item.Status)
		}
		if len(items) > 0 {
			fmt.Fprintf(a.Stdout, "\nHint: Load the desired skill with 'kungfu load <name>' for JIT knowledge, or learn it with 'kungfu learn <name>' to install permanently.\n")
		}
		return ExitSuccess
	}
}

func (a *App) RunShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag bool

	fs.StringVar(&category, "category", "", "Show all skills in category")
	fs.StringVar(&category, "c", "", "Show all skills in category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Show all skills matching boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Show all skills matching boolean tag expression (shorthand)")
	fs.StringVar(&tagQuery, "tag", "", "Show all skills matching boolean tag expression (alias)")
	fs.BoolVar(&allFlag, "all", false, "Show all skills")
	fs.BoolVar(&allFlag, "a", false, "Show all skills (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	var targetItems []catalog.Item

	if allFlag {
		targetItems = a.Store.All()
	} else if category != "" || tagQuery != "" {
		targetItems = a.Store.ListByFilter(catalog.QueryFilter{
			Category: category,
			TagQuery: tagQuery,
		})
		if len(targetItems) == 0 {
			a.printFilterNotFoundWithSuggestions("show", category, tagQuery)
			return ExitNotFound
		}
	} else if len(posArgs) > 0 {
		for _, name := range posArgs {
			item, err := a.Store.Get(name)
			if err == nil {
				targetItems = append(targetItems, *item)
				continue
			}

			// Check if installed locally in workspace or global scope
			st := a.loadState()
			wd, _ := os.Getwd()
			var rec *state.SkillRecord
			if r, ok := st.GetSkill(state.ScopeWorkspace, wd, name); ok && r.Status == state.StatusInstalled {
				rec = &r
			} else if r, ok := st.GetSkill(state.ScopeGlobal, "", name); ok && r.Status == state.StatusInstalled {
				rec = &r
			}

			if rec != nil {
				skillMD := filepath.Join(rec.Path, "SKILL.md")
				if data, rerr := os.ReadFile(skillMD); rerr == nil {
					if fm, body, perr := catalog.ParseFrontmatter(data); perr == nil && fm != nil {
						meta := fm.Metadata
						if meta == nil {
							meta = make(map[string]string)
						}
						source := "catalog"
						if rec.Origin == state.OriginGitHub {
							source = "github"
							meta["origin"] = "github"
							meta["repo"] = rec.Repo
							meta["ref"] = rec.Ref
							meta["canonical"] = rec.RawURL
						} else if rec.Origin == state.OriginCustom {
							source = "custom"
						}
						targetItems = append(targetItems, catalog.Item{
							ID:          fm.Name,
							Name:        fm.Name,
							Type:        catalog.ItemTypeSkill,
							Description: fm.Description,
							Category:    meta["category"],
							Version:     rec.Version,
							Path:        skillMD,
							Source:      source,
							Metadata:    meta,
							Body:        body,
						})
						continue
					}
				}
			}

			if catalog.IsGitHubTarget(name) {
				target, ok := catalog.ParseGitHubTarget(name)
				if ok {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					ghItem, ghErr := catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
					cancel()
					if ghErr == nil && ghItem != nil {
						targetItems = append(targetItems, *ghItem)
						continue
					}
				}
			}
			a.printNotFoundWithSuggestions("show", name)
			return ExitNotFound
		}
	} else {
		fmt.Fprintln(a.Stderr, "error: show requires a skill name, --category, --tags, or --all")
		return ExitUsage
	}

	return a.renderShowItems(targetItems, mode)
}

func (a *App) renderShowItems(items []catalog.Item, mode OutputMode) int {
	wd, _ := os.Getwd()
	st := a.loadState()
	for i := range items {
		rec, installed := st.GetSkill(state.ScopeWorkspace, wd, items[i].Name)
		if !installed || rec.Status != state.StatusInstalled {
			rec, installed = st.GetSkill(state.ScopeGlobal, "", items[i].Name)
		}
		if installed && (rec.Origin == state.OriginGitHub || rec.Repo != "") {
			items[i].Source = "github"
			if items[i].Metadata == nil {
				items[i].Metadata = make(map[string]string)
			}
			if rec.Repo != "" {
				items[i].Metadata["repo"] = rec.Repo
			}
			if rec.Ref != "" {
				items[i].Metadata["ref"] = rec.Ref
			}
			if rec.Origin != "" {
				items[i].Metadata["origin"] = rec.Origin
			}
		}

		if (items[i].Category == "" || items[i].Version == "") && items[i].URL != "" {
			if content, err := a.Store.LoadContent(&items[i]); err == nil {
				if fm, _, err := catalog.ParseFrontmatter([]byte(content)); err == nil && fm != nil {
					if fm.Metadata != nil {
						if items[i].Category == "" {
							items[i].Category = fm.Metadata["category"]
						}
						if items[i].Version == "" {
							items[i].Version = fm.Metadata["version"]
						}
						if items[i].Author == "" {
							items[i].Author = fm.Metadata["author"]
						}
						if len(items[i].Tags) == 0 && fm.Metadata["tags"] != "" {
							items[i].Tags = catalog.ParseTags(fm.Metadata["tags"])
						}
						if items[i].Metadata == nil {
							items[i].Metadata = fm.Metadata
						}
					}
					if items[i].License == "" && fm.License != "" {
						items[i].License = fm.License
					}
				}
			}
		}
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(items)
		return ExitSuccess

	case OutputModeRaw:
		var sb strings.Builder
		for i, item := range items {
			if i > 0 {
				sb.WriteString("\n---\n\n")
			}
			sb.WriteString(fmt.Sprintf("# %s\n\n", item.Name))
			sb.WriteString(fmt.Sprintf("- **Category**: `%s`\n", item.Category))
			sb.WriteString(fmt.Sprintf("- **Type**: `%s`\n", item.Type))
			if item.Source == "github" || (item.Metadata != nil && item.Metadata["repo"] != "") {
				repo := item.Metadata["repo"]
				if repo != "" {
					sb.WriteString(fmt.Sprintf("- **Origin**: `github` (%s)\n", repo))
				} else {
					sb.WriteString("- **Origin**: `github`\n")
				}
				if ref := item.Metadata["ref"]; ref != "" {
					sb.WriteString(fmt.Sprintf("- **Ref**: `%s`\n", ref))
				}
			}
			if len(item.Tags) > 0 {
				sb.WriteString(fmt.Sprintf("- **Tags**: `%s`\n", strings.Join(item.Tags, ", ")))
			}
			if item.Author != "" {
				sb.WriteString(fmt.Sprintf("- **Author**: %s\n", item.Author))
			}
			if item.Version != "" {
				sb.WriteString(fmt.Sprintf("- **Version**: %s\n", item.Version))
			}
			if item.License != "" {
				sb.WriteString(fmt.Sprintf("- **License**: %s\n", item.License))
			}
			if item.Digest != "" {
				sb.WriteString(fmt.Sprintf("- **Digest**: `%s`\n", item.Digest))
			}
			if item.URL != "" {
				sb.WriteString(fmt.Sprintf("- **Canonical URL**: %s\n", item.URL))
			}
			if item.AllowedTools != "" {
				sb.WriteString(fmt.Sprintf("- **Allowed Tools**: `%s`\n", item.AllowedTools))
			}
			if item.Path != "" {
				sb.WriteString(fmt.Sprintf("- **Path**: `%s`\n", item.Path))
			}
			sb.WriteString(fmt.Sprintf("\n## Description\n\n%s\n", item.Description))
		}
		fmt.Fprint(a.Stdout, sb.String())
		return ExitSuccess

	default: // OutputModeHuman (default)
		for i, item := range items {
			if i > 0 {
				fmt.Fprintln(a.Stdout, "------------------------------------------------------------")
			}
			fmt.Fprintf(a.Stdout, "Name:        %s\n", item.Name)
			fmt.Fprintf(a.Stdout, "Type:        %s\n", item.Type)
			fmt.Fprintf(a.Stdout, "Category:    %s\n", item.Category)
			if item.Title != "" {
				fmt.Fprintf(a.Stdout, "Title:       %s\n", item.Title)
			}
			fmt.Fprintf(a.Stdout, "Description: %s\n", item.Description)
			if item.Source == "github" || (item.Metadata != nil && item.Metadata["repo"] != "") {
				repo := item.Metadata["repo"]
				if repo != "" {
					fmt.Fprintf(a.Stdout, "Origin:      github (%s)\n", repo)
				} else {
					fmt.Fprintf(a.Stdout, "Origin:      github\n")
				}
				if ref := item.Metadata["ref"]; ref != "" {
					fmt.Fprintf(a.Stdout, "Ref:         %s\n", ref)
				}
			}
			if len(item.Tags) > 0 {
				fmt.Fprintf(a.Stdout, "Tags:        %s\n", strings.Join(item.Tags, ", "))
			}
			if item.Author != "" {
				fmt.Fprintf(a.Stdout, "Author:      %s\n", item.Author)
			}
			if item.Version != "" {
				fmt.Fprintf(a.Stdout, "Version:     %s\n", item.Version)
			}
			if item.License != "" {
				fmt.Fprintf(a.Stdout, "License:     %s\n", item.License)
			}
			if item.Digest != "" {
				fmt.Fprintf(a.Stdout, "Digest:      %s\n", item.Digest)
			}
			if item.URL != "" {
				fmt.Fprintf(a.Stdout, "URL:         %s\n", item.URL)
			}
			if item.AllowedTools != "" {
				fmt.Fprintf(a.Stdout, "Tools:       %s\n", item.AllowedTools)
			}
			if item.Path != "" {
				fmt.Fprintf(a.Stdout, "Path:        %s\n", item.Path)
			}
		}
		return ExitSuccess
	}
}

func (a *App) RunLoad(args []string) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag bool

	fs.StringVar(&category, "category", "", "Load all skills in category")
	fs.StringVar(&category, "c", "", "Load all skills in category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Load all skills matching boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Load all skills matching boolean tag expression (shorthand)")
	fs.StringVar(&tagQuery, "tag", "", "Load all skills matching boolean tag expression (alias)")
	fs.BoolVar(&allFlag, "all", false, "Load all skills")
	fs.BoolVar(&allFlag, "a", false, "Load all skills (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)

	type loadTarget struct {
		item    catalog.Item
		subPath string
	}
	var targets []loadTarget

	if allFlag {
		for _, it := range a.Store.All() {
			targets = append(targets, loadTarget{item: it})
		}
	} else if category != "" || tagQuery != "" {
		filtered := a.Store.ListByFilter(catalog.QueryFilter{
			Category: category,
			TagQuery: tagQuery,
		})
		if len(filtered) == 0 {
			a.printFilterNotFoundWithSuggestions("load", category, tagQuery)
			return ExitNotFound
		}
		for _, it := range filtered {
			targets = append(targets, loadTarget{item: it})
		}
	} else if len(posArgs) > 0 {
		if len(posArgs) == 2 && !strings.Contains(posArgs[0], "/") && (strings.Contains(posArgs[1], "/") || strings.Contains(posArgs[1], ".")) {
			// Check if posArgs[1] is a skill name vs a relative path
			if _, secondErr := a.Store.Get(posArgs[1]); secondErr != nil {
				item, err := a.Store.Get(posArgs[0])
				if err != nil {
					a.printNotFoundWithSuggestions("load", posArgs[0])
					return ExitNotFound
				}
				targets = append(targets, loadTarget{item: *item, subPath: posArgs[1]})
			}
		}

		if len(targets) == 0 {
			for _, arg := range posArgs {
				if strings.Contains(arg, "/") {
					parts := strings.SplitN(arg, "/", 2)
					skillName := parts[0]
					subPath := parts[1]
					item, err := a.Store.Get(skillName)
					if err == nil {
						targets = append(targets, loadTarget{item: *item, subPath: subPath})
						continue
					}
					if catalog.IsGitHubTarget(arg) {
						target, ok := catalog.ParseGitHubTarget(arg)
						if ok {
							ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
							ghItem, ghErr := catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
							cancel()
							if ghErr == nil && ghItem != nil {
								targets = append(targets, loadTarget{item: *ghItem, subPath: ""})
								continue
							}
						}
					}
					a.printNotFoundWithSuggestions("load", skillName)
					return ExitNotFound
				} else {
					item, err := a.Store.Get(arg)
					if err != nil {
						if catalog.IsGitHubTarget(arg) {
							target, ok := catalog.ParseGitHubTarget(arg)
							if ok {
								ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
								ghItem, ghErr := catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
								cancel()
								if ghErr == nil && ghItem != nil {
									targets = append(targets, loadTarget{item: *ghItem, subPath: ""})
									continue
								}
							}
						}
						a.printNotFoundWithSuggestions("load", arg)
						return ExitNotFound
					}
					targets = append(targets, loadTarget{item: *item, subPath: ""})
				}
			}
		}
	} else {
		fmt.Fprintln(a.Stderr, "error: load requires skill name(s), skill/filepath, --category, --tags, or --all")
		return ExitUsage
	}

	var loadedDocs []catalog.LoadedDoc
	st := a.loadState()
	for _, target := range targets {
		var rawContent string
		var err error
		displayPath := target.item.Path

		if target.subPath == "" || target.subPath == "SKILL.md" {
			rawContent, err = a.loadItemContent(&target.item)
			if err == nil && rawContent != "" && target.item.Name != "" {
				if homeDir, herr := os.UserHomeDir(); herr == nil && homeDir != "" {
					cacheSkillDir := filepath.Join(homeDir, ".cache", "kungfu", "skills", target.item.Name)
					_ = os.MkdirAll(cacheSkillDir, 0755)
					cachePath := filepath.Join(cacheSkillDir, "SKILL.md")
					_ = os.WriteFile(cachePath, []byte(rawContent), 0644)

					var extraCandidates []string
					for _, ref := range a.listReferences(target.item) {
						extraCandidates = append(extraCandidates, "references/"+ref+".md")
					}
					for _, scr := range a.listScripts(target.item) {
						extraCandidates = append(extraCandidates, "scripts/"+scr)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					_, _ = runner.InstallSkillContext(ctx, target.item, cacheSkillDir, rawContent, extraCandidates, a.getHTTPClient())
					cancel()
				}
			}
		} else {
			rawContent, err = runner.ReadFile(target.item, target.subPath)
			displayPath = target.subPath
			if err != nil {
				if errors.Is(err, runner.ErrInvalidPath) {
					fmt.Fprintf(a.Stderr, "error: %v\n", err)
					return ExitUsage
				}
				if errors.Is(err, runner.ErrBinaryFile) {
					fmt.Fprintf(a.Stderr, "error: %v: cannot stream binary file %q into LLM context\n", err, target.subPath)
					return ExitUsage
				}
				fmt.Fprintf(a.Stderr, "error: file %q not found in skill %q.\n", target.subPath, target.item.Name)
				var files []string
				files = append(files, "SKILL.md")
				for _, s := range a.listScripts(target.item) {
					files = append(files, "scripts/"+s, s)
				}
				for _, r := range a.listReferences(target.item) {
					files = append(files, "references/"+r+".md", r)
				}
				suggs := search.Suggest(target.subPath, files, 3)
				if len(suggs) > 0 {
					fmt.Fprintln(a.Stderr, "\nDid you mean:")
					for _, s := range suggs {
						fmt.Fprintf(a.Stderr, "  - kungfu load %s/%s\n", target.item.Name, s)
					}
				}
				return ExitNotFound
			}
		}

		if err != nil {
			a.logError("loading content for %q: %v", target.item.Name, err)
			return mapErrorToExitCode(err)
		}
		st.RecordLoad(target.item.Name)
		bodyContent := stripFrontmatter(rawContent)
		loadedDocs = append(loadedDocs, catalog.LoadedDoc{
			ID:       target.item.ID,
			Name:     target.item.Name,
			Type:     string(target.item.Type),
			Category: target.item.Category,
			Tags:     target.item.Tags,
			Path:     displayPath,
			Content:  bodyContent,
			Metadata: target.item.Metadata,
		})
	}
	a.saveState(st)

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(loadedDocs)
		return ExitSuccess

	case OutputModeRaw:
		var sb strings.Builder
		for i, doc := range loadedDocs {
			if i > 0 {
				sb.WriteString("\n\n---\n\n")
			}
			sb.WriteString(doc.Content)
		}
		fmt.Fprint(a.Stdout, sb.String())
		return ExitSuccess

	default: // OutputModeHuman (default)
		for i, doc := range loadedDocs {
			if i > 0 {
				fmt.Fprintln(a.Stdout, "\n============================================================")
			}
			if doc.Path != "" && doc.Path != "SKILL.md" && !strings.HasSuffix(doc.Path, "/SKILL.md") {
				fmt.Fprintf(a.Stdout, "SKILL: %s [%s] (File: %s)\n", doc.Name, doc.Type, doc.Path)
			} else {
				fmt.Fprintf(a.Stdout, "SKILL: %s [%s] (Category: %s)\n", doc.Name, doc.Type, doc.Category)
			}
			fmt.Fprintln(a.Stdout, "------------------------------------------------------------")
			fmt.Fprintln(a.Stdout, doc.Content)
		}
		return ExitSuccess
	}
}

func (a *App) RunLearn(args []string) int {
	fs := flag.NewFlagSet("learn", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, forceFlag, loadFlag bool
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Install skill to global directory (~/.agents/skills/)")
	fs.BoolVar(&globalFlag, "g", false, "Install skill to global directory (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Overwrite existing skill directory")
	fs.BoolVar(&forceFlag, "f", false, "Overwrite existing skill directory (shorthand)")
	fs.BoolVar(&loadFlag, "load", false, "Load and stream skill instructions to stdout after learning")
	fs.BoolVar(&loadFlag, "l", false, "Load and stream skill instructions to stdout after learning (shorthand)")
	fs.StringVar(&category, "category", "", "Learn all skills in category")
	fs.StringVar(&category, "c", "", "Learn all skills in category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Learn all skills matching boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Learn all skills matching boolean tag expression (shorthand)")
	fs.StringVar(&tagQuery, "tag", "", "Learn all skills matching boolean tag expression (alias)")
	fs.BoolVar(&allFlag, "all", false, "Learn all skills")
	fs.BoolVar(&allFlag, "a", false, "Learn all skills (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	var targetItems []catalog.Item

	if allFlag {
		targetItems = a.Store.All()
	} else if category != "" || tagQuery != "" {
		targetItems = a.Store.ListByFilter(catalog.QueryFilter{
			Category: category,
			TagQuery: tagQuery,
		})
		if len(targetItems) == 0 {
			a.printFilterNotFoundWithSuggestions("learn", category, tagQuery)
			return ExitNotFound
		}
	} else if len(posArgs) > 0 {
		for _, name := range posArgs {
			item, err := a.Store.Get(name)
			if err != nil {
				if catalog.IsGitHubTarget(name) {
					target, ok := catalog.ParseGitHubTarget(name)
					if ok {
						ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
						ghItem, ghErr := catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
						cancel()
						if ghErr == nil && ghItem != nil {
							targetItems = append(targetItems, *ghItem)
							continue
						}
					}
				}
				a.printNotFoundWithSuggestions("learn", name)
				return ExitNotFound
			}
			targetItems = append(targetItems, *item)
		}
	} else {
		fmt.Fprintln(a.Stderr, "error: learn requires skill name(s), --category, --tags, or --all")
		return ExitUsage
	}

	// Determine install base directory
	var baseDir string
	if globalFlag {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			a.logError("resolving user home directory: %v", err)
			return ExitGeneral
		}
		baseDir = filepath.Join(homeDir, ".agents", "skills")
	} else {
		wd, err := os.Getwd()
		if err != nil {
			a.logError("resolving current working directory: %v", err)
			return ExitGeneral
		}
		baseDir = filepath.Join(wd, ".agents", "skills")
	}

	type LearnResult struct {
		Name    string   `json:"name"`
		Path    string   `json:"path"`
		Status  string   `json:"status"`
		Files   []string `json:"files,omitempty"`
		Message string   `json:"message,omitempty"`
		Content string   `json:"content,omitempty"`
	}

	var results []LearnResult

	for _, item := range targetItems {
		cleanName := filepath.Base(filepath.Clean(item.Name))
		if cleanName == "." || cleanName == ".." || strings.Contains(item.Name, "..") {
			a.logError("invalid skill name %q: potential directory traversal", item.Name)
			return ExitGeneral
		}
		destDir := filepath.Join(baseDir, cleanName)
		rel, err := filepath.Rel(baseDir, destDir)
		if err != nil || strings.HasPrefix(rel, "..") {
			a.logError("invalid destination directory for skill %q", item.Name)
			return ExitGeneral
		}

		// Load SKILL.md content
		content, err := a.loadItemContent(&item)
		if err != nil {
			a.logError("rendering content for skill %q: %v", item.Name, err)
			return mapErrorToExitCode(err)
		}

		if fi, err := os.Stat(destDir); err == nil && fi.IsDir() {
			if !forceFlag {
				res := LearnResult{
					Name:    item.Name,
					Path:    destDir,
					Status:  "skipped",
					Message: "already exists (use -f/--force to overwrite)",
				}
				if loadFlag {
					res.Content = stripFrontmatter(content)
				}
				results = append(results, res)
				continue
			}
		}

		if err := os.MkdirAll(destDir, 0755); err != nil {
			a.logError("creating skill directory %q: %v", destDir, err)
			return ExitGeneral
		}

		// Collect candidate references and scripts
		var extraCandidates []string
		for _, ref := range a.listReferences(item) {
			extraCandidates = append(extraCandidates, "references/"+ref+".md")
		}
		for _, scr := range a.listScripts(item) {
			extraCandidates = append(extraCandidates, "scripts/"+scr)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		files, err := runner.InstallSkillContext(ctx, item, destDir, content, extraCandidates, a.getHTTPClient())
		cancel()
		if err != nil {
			a.logError("installing skill %q: %v", item.Name, err)
			return ExitGeneral
		}

		// Record in state manifest
		st := a.loadState()
		scope := state.ScopeWorkspace
		wsDir, _ := os.Getwd()
		if globalFlag {
			scope = state.ScopeGlobal
			wsDir = ""
		}

		digest, _ := state.ComputePackageDigest(destDir, files)
		skillVer := item.Version
		if skillVer == "" && item.Metadata != nil {
			skillVer = item.Metadata["version"]
		}
		if skillVer == "" {
			if fm, _, err := catalog.ParseFrontmatter([]byte(content)); err == nil && fm != nil {
				if fm.Metadata != nil && fm.Metadata["version"] != "" {
					skillVer = fm.Metadata["version"]
				}
			}
		}

		origin := state.OriginCatalog
		repo := ""
		ref := ""
		rawURL := ""
		if item.Source == "github" || (item.Metadata != nil && item.Metadata["origin"] != "") {
			origin = state.OriginGitHub
			if item.Metadata != nil {
				repo = item.Metadata["repo"]
				ref = item.Metadata["ref"]
				rawURL = item.Metadata["canonical"]
			}
			if rawURL == "" {
				rawURL = item.URL
			}
		}

		st.RecordInstall(scope, wsDir, state.SkillRecord{
			Name:    item.Name,
			Origin:  origin,
			Repo:    repo,
			Ref:     ref,
			RawURL:  rawURL,
			Path:    destDir,
			Version: skillVer,
			Digest:  digest,
			Files:   files,
		})
		a.saveState(st)

		res := LearnResult{
			Name:   item.Name,
			Path:   destDir,
			Status: "installed",
			Files:  files,
		}
		if loadFlag {
			res.Content = stripFrontmatter(content)
		}
		results = append(results, res)
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		return ExitSuccess

	case OutputModeRaw:
		var sb strings.Builder
		for i, r := range results {
			if i > 0 {
				if loadFlag && r.Content != "" {
					sb.WriteString("\n\n---\n\n")
				} else {
					sb.WriteString("\n")
				}
			}
			if r.Status == "installed" {
				if loadFlag && r.Content != "" {
					sb.WriteString(fmt.Sprintf("✓ Learned skill %q -> %s\n\n", r.Name, r.Path))
				} else {
					sb.WriteString(fmt.Sprintf("✓ Learned skill %q -> %s\n", r.Name, r.Path))
				}
			} else {
				if loadFlag && r.Content != "" {
					sb.WriteString(fmt.Sprintf("! Skipped skill %q: %s (%s)\n\n", r.Name, r.Message, r.Path))
				} else {
					sb.WriteString(fmt.Sprintf("! Skipped skill %q: %s (%s)\n", r.Name, r.Message, r.Path))
				}
			}
			if loadFlag && r.Content != "" {
				sb.WriteString(r.Content)
			}
		}
		fmt.Fprint(a.Stdout, sb.String())
		return ExitSuccess

	default: // OutputModeHuman (default)
		for i, r := range results {
			if i > 0 {
				fmt.Fprintln(a.Stdout, "\n============================================================")
			}
			if r.Status == "installed" {
				fmt.Fprintf(a.Stdout, "✓ Learned skill %q -> %s\n", r.Name, r.Path)
			} else {
				fmt.Fprintf(a.Stdout, "! Skipped skill %q: %s (%s)\n", r.Name, r.Message, r.Path)
			}
			if loadFlag && r.Content != "" {
				fmt.Fprintln(a.Stdout, "------------------------------------------------------------")
				fmt.Fprintln(a.Stdout, r.Content)
			}
		}
		return ExitSuccess
	}
}

func (a *App) RunFind(args []string) int {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var strategy string
	var limit int
	var threshold float64
	var category, tagQuery string
	var jsonFlag, rawFlag bool

	fs.StringVar(&strategy, "strategy", "hybrid", "Search strategy: hybrid, bm25, tfidf, exact")
	fs.StringVar(&strategy, "s", "hybrid", "Search strategy (shorthand)")
	fs.IntVar(&limit, "limit", 10, "Maximum number of results to return")
	fs.IntVar(&limit, "n", 10, "Maximum number of results (shorthand)")
	fs.Float64Var(&threshold, "threshold", 0.0, "Minimum relevance score threshold")
	fs.Float64Var(&threshold, "T", 0.0, "Minimum relevance score threshold (shorthand)")
	fs.StringVar(&category, "category", "", "Filter search results by category")
	fs.StringVar(&category, "c", "", "Filter search results by category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Filter search results by boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Filter search results by boolean tag expression (shorthand)")
	fs.StringVar(&tagQuery, "tag", "", "Filter search results by tag (alias)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	query := strings.Join(posArgs, " ")
	if strings.TrimSpace(query) == "" && category == "" && tagQuery == "" {
		fmt.Fprintln(a.Stderr, "error: find requires keywords or filtering flags (--category, --tags)")
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	items := a.Store.All()
	if category != "" || tagQuery != "" {
		items = a.Store.ListByFilter(catalog.QueryFilter{
			Category: category,
			TagQuery: tagQuery,
		})
		if len(items) == 0 {
			a.printFilterNotFoundWithSuggestions("find", category, tagQuery)
			return ExitNotFound
		}
	}

	_ = a.Searcher.Index(items)
	searchQuery := strings.TrimSpace(query)
	results := a.Searcher.Search(searchQuery, strategy, limit, float32(threshold))

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		return ExitSuccess

	case OutputModeRaw:
		if len(results) == 0 {
			fmt.Fprintf(a.Stdout, "No matching skills found for query %q.\n", query)
			return ExitSuccess
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# Search Results for %q (%d matches)\n\n", query, len(results)))
		for i, res := range results {
			tagInfo := ""
			if len(res.Item.Tags) > 0 {
				tagInfo = fmt.Sprintf(" `tags: %s`", strings.Join(res.Item.Tags, ", "))
			}
			sb.WriteString(fmt.Sprintf("%d. **%s** (score: %.3f) [%s]%s\n   %s\n", i+1, res.Item.Name, res.Score, res.Item.Category, tagInfo, res.Item.Description))
		}
		fmt.Fprint(a.Stdout, sb.String())
		return ExitSuccess

	default: // OutputModeHuman (default)
		if len(results) == 0 {
			fmt.Fprintf(a.Stdout, "No matching skills found for %q.\n", query)
			return ExitSuccess
		}
		fmt.Fprintf(a.Stdout, "SEARCH RESULTS (%d matches):\n\n", len(results))
		fmt.Fprintf(a.Stdout, "%-4s %-22s %-8s %-12s %s\n", "RANK", "NAME", "SCORE", "CATEGORY", "DESCRIPTION")
		fmt.Fprintf(a.Stdout, "%-4s %-22s %-8s %-12s %s\n", "----", "----------------------", "--------", "------------", "----------------------------------------")
		for i, res := range results {
			fmt.Fprintf(a.Stdout, "%-4d %-22s %-8.3f %-12s %s\n", i+1, res.Item.Name, res.Score, res.Item.Category, res.Item.Description)
		}
		return ExitSuccess
	}
}

func (a *App) RunRun(args []string) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		fmt.Fprintln(a.Stderr, "USAGE:\n  kungfu run <skill> <script> [args...]\n\nExecute bundled skill scripts (supports Python/uv, Node/Bun/Deno, Shell).")
		return ExitUsage
	}
	if len(args) < 2 {
		fmt.Fprintln(a.Stderr, "error: run requires <skill> <script> [args...]")
		return ExitUsage
	}
	skillName := args[0]
	scriptName := args[1]
	scriptArgs := args[2:]

	item, err := a.Store.Get(skillName)
	if err != nil {
		a.printNotFoundWithSuggestions("run", skillName)
		return mapErrorToExitCode(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	err = runner.RunScriptContext(ctx, *item, scriptName, scriptArgs, os.Stdin, a.Stdout, a.Stderr)
	if err != nil {
		if errors.Is(err, runner.ErrScriptNotFound) {
			fmt.Fprintf(a.Stderr, "error: script %q not found in skill %q.\n", scriptName, skillName)
			scripts := a.listScripts(*item)
			if len(scripts) > 0 {
				suggs := search.Suggest(scriptName, scripts, 3)
				if len(suggs) > 0 {
					fmt.Fprintln(a.Stderr, "\nDid you mean:")
					for _, s := range suggs {
						fmt.Fprintf(a.Stderr, "  - kungfu run %s %s\n", skillName, s)
					}
				}
			}
			return ExitNotFound
		}
		if errors.Is(err, runner.ErrInvalidPath) {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitUsage
		}
		if errors.Is(err, runner.ErrExecutionTimeout) {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitGeneral
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(a.Stderr, "error: running script %q: %v\n", scriptName, err)
		return mapErrorToExitCode(err)
	}
	return ExitSuccess
}

func (a *App) listReferences(item catalog.Item) []string {
	var refs []string
	seen := make(map[string]bool)

	add := func(r string) {
		r = strings.TrimSpace(r)
		if r == "" {
			return
		}
		clean := filepath.ToSlash(filepath.Clean(r))
		clean = strings.TrimPrefix(clean, "./")
		clean = strings.TrimPrefix(clean, "references/")
		clean = strings.TrimSuffix(clean, ".md")

		if clean != "" && !seen[clean] {
			seen[clean] = true
			refs = append(refs, clean)
		}
	}

	// 1. Check local candidate directories
	baseDirs := runner.GetCandidateBaseDirs(item)
	for _, baseDir := range baseDirs {
		refsDir := filepath.Join(baseDir, "references")
		if entries, err := os.ReadDir(refsDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					add(e.Name())
				}
			}
		}
	}

	// 2. Check item body content for referenced markdown documents
	content := item.Body
	if content == "" && a.Store != nil {
		if c, err := a.Store.LoadContent(&item); err == nil {
			content = c
		}
	}
	if content != "" {
		for _, c := range runner.ExtractCandidates(content) {
			if strings.HasPrefix(c, "references/") {
				add(c)
			}
		}
	}

	sort.Strings(refs)
	return refs
}

func (a *App) listScripts(item catalog.Item) []string {
	var scripts []string
	seen := make(map[string]bool)

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		clean := filepath.ToSlash(filepath.Clean(s))
		clean = strings.TrimPrefix(clean, "./")
		clean = strings.TrimPrefix(clean, "scripts/")
		if clean != "" && !seen[clean] {
			seen[clean] = true
			scripts = append(scripts, clean)
		}
	}

	// 1. Check local candidate directories
	baseDirs := runner.GetCandidateBaseDirs(item)
	for _, baseDir := range baseDirs {
		scriptsDir := filepath.Join(baseDir, "scripts")
		if entries, err := os.ReadDir(scriptsDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					add(e.Name())
				}
			}
		}
	}

	// 2. Check item body content
	content := item.Body
	if content == "" && a.Store != nil {
		if c, err := a.Store.LoadContent(&item); err == nil {
			content = c
		}
	}
	if content != "" {
		for _, c := range runner.ExtractCandidates(content) {
			if strings.HasPrefix(c, "scripts/") {
				add(c)
			}
		}
	}

	sort.Strings(scripts)
	return scripts
}

func listScripts(item catalog.Item) []string {
	var scripts []string
	seen := make(map[string]bool)

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		clean := filepath.ToSlash(filepath.Clean(s))
		clean = strings.TrimPrefix(clean, "./")
		clean = strings.TrimPrefix(clean, "scripts/")
		if clean != "" && !seen[clean] {
			seen[clean] = true
			scripts = append(scripts, clean)
		}
	}

	baseDirs := runner.GetCandidateBaseDirs(item)
	for _, baseDir := range baseDirs {
		scriptsDir := filepath.Join(baseDir, "scripts")
		if entries, err := os.ReadDir(scriptsDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					add(e.Name())
				}
			}
		}
	}

	if item.Body != "" {
		for _, c := range runner.ExtractCandidates(item.Body) {
			if strings.HasPrefix(c, "scripts/") {
				add(c)
			}
		}
	}

	sort.Strings(scripts)
	return scripts
}

func (a *App) loadItemContent(item *catalog.Item) (string, error) {
	return a.Store.LoadContent(item)
}

func stripFrontmatter(content string) string {
	_, body, hasFM, err := catalog.ExtractFrontmatter([]byte(content))
	if err == nil && hasFM {
		return strings.TrimLeft(string(body), "\r\n")
	}
	return content
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
	return false
}

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
			if r.Status == "forgotten" {
				if len(r.Files) > 0 {
					fmt.Fprintf(a.Stdout, "✓ Forgot skill %q (removed %d files from %s)\n", r.Name, len(r.Files), r.Path)
				} else {
					fmt.Fprintf(a.Stdout, "✓ Forgot skill %q\n", r.Name)
				}
			} else if r.Status == "not_found" {
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
			} else {
				fmt.Fprintf(a.Stderr, "✗ Failed to forget skill %q: %s\n", r.Name, r.Message)
			}
		}
	}

	hasNotFound := false
	hasGeneralError := false
	for _, r := range results {
		if r.Status == "not_found" {
			hasNotFound = true
		} else if r.Status == "error" {
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
			catalog.WithEmbeddedFS(embeddedSkills),
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

	if len(installed) == 0 {
		switch mode {
		case OutputModeJSON:
			_ = json.NewEncoder(a.Stdout).Encode([]any{})
		case OutputModeRaw:
			// empty output
		default:
			fmt.Fprintf(a.Stdout, "No installed skills found in %s scope.\n", scope)
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
				fmt.Fprintf(a.Stderr, "error: skill %q is a custom/external skill (not learned from catalog). Kungfu cannot update custom skills.\n", name)
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

		if rec.Origin == state.OriginGitHub || rec.RawURL != "" {
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
				fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.InstalledVer, c.RemoteVer, c.Status, c.Reason)
			} else {
				if c.Status == "updated" {
					fmt.Fprintf(a.Stdout, "updated\t%s\t%s\n", c.Name, c.Reason)
				} else {
					msg := c.Reason
					if c.Message != "" {
						msg = c.Message
					}
					fmt.Fprintf(a.Stdout, "%s\t%s\t%s\n", c.Status, c.Name, msg)
				}
			}
		}
	default: // OutputModeHuman
		if dryRun {
			hasAnyUpdates := false
			fmt.Fprintf(a.Stdout, "Checking for skill updates (%s scope)...\n\n", scope)
			for _, c := range checks {
				if c.Status == "custom" {
					fmt.Fprintf(a.Stdout, "  • %-20s [Custom skill] %s\n", c.Name, c.Reason)
				} else if c.Status == "not_found" {
					fmt.Fprintf(a.Stderr, "  ⚠ %-20s %s\n", c.Name, c.Reason)
					if len(installedNames) > 0 {
						suggs := search.Suggest(c.Name, installedNames, 3)
						if len(suggs) > 0 {
							fmt.Fprintln(a.Stderr, "    Did you mean:")
							for _, s := range suggs {
								fmt.Fprintf(a.Stderr, "      - %s\n", s)
							}
						}
					}
				} else if c.HasUpdate {
					hasAnyUpdates = true
					if c.RequiresYes {
						fmt.Fprintf(a.Stdout, "  ⚠ %-20s [Update available with -y] %s\n", c.Name, c.Reason)
					} else {
						fmt.Fprintf(a.Stdout, "  ↑ %-20s [Update available] %s\n", c.Name, c.Reason)
					}
				} else {
					fmt.Fprintf(a.Stdout, "  ✓ %-20s %s\n", c.Name, c.Reason)
				}
			}

			if hasAnyUpdates {
				fmt.Fprintln(a.Stdout, "\nRun 'kungfu update <skill>' or 'kungfu update --all' to apply updates.")
			} else {
				fmt.Fprintln(a.Stdout, "\nAll installed skills are up to date.")
			}
		} else {
			for _, c := range checks {
				switch c.Status {
				case "updated":
					fmt.Fprintf(a.Stdout, "✓ Updated skill %q (%s, %d files)\n", c.Name, c.Reason, len(c.Files))
				case "up_to_date":
					fmt.Fprintf(a.Stdout, "✓ Skill %q is up to date (%s)\n", c.Name, c.Reason)
				case "custom":
					fmt.Fprintf(a.Stdout, "• Skipped custom skill %q (%s)\n", c.Name, c.Reason)
				case "not_found":
					fmt.Fprintf(a.Stderr, "⚠ Skill %q is not installed in %s scope.\n", c.Name, scope)
					if len(installedNames) > 0 {
						suggs := search.Suggest(c.Name, installedNames, 3)
						if len(suggs) > 0 {
							fmt.Fprintln(a.Stderr, "\nDid you mean:")
							for _, s := range suggs {
								fmt.Fprintf(a.Stderr, "  - kungfu update %s\n", s)
							}
						}
					}
				case "blocked":
					fmt.Fprintf(a.Stderr, "⚠ Skill %q was not updated: %s\n", c.Name, c.Message)
				case "error":
					fmt.Fprintf(a.Stderr, "✗ Failed to update skill %q: %s\n", c.Name, c.Message)
				}
			}
		}
	}

	hasNotFound := false
	hasGeneralError := false
	for _, c := range checks {
		if c.Status == "not_found" {
			hasNotFound = true
		} else if c.Status == "error" {
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

func (a *App) RunStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, jsonFlag, rawFlag bool
	var categoryFlag, tagsFlag string

	fs.StringVar(&categoryFlag, "category", "", "Filter status by category")
	fs.StringVar(&categoryFlag, "c", "", "Filter status by category (shorthand)")
	fs.StringVar(&tagsFlag, "tags", "", "Filter status by boolean tag expression")
	fs.StringVar(&tagsFlag, "t", "", "Filter status by boolean tag expression (shorthand)")
	fs.StringVar(&tagsFlag, "tag", "", "Filter status by tag (alias)")
	fs.BoolVar(&globalFlag, "global", false, "Show global skills status only")
	fs.BoolVar(&globalFlag, "g", false, "Show global skills status only (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	st := a.loadState()
	wsDir, _ := os.Getwd()

	wsInstalled := st.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	wsForgotten := st.GetForgottenSkills(state.ScopeWorkspace, wsDir)
	globalInstalled := st.GetInstalledSkills(state.ScopeGlobal, "")
	globalForgotten := st.GetForgottenSkills(state.ScopeGlobal, "")

	targetNames := make(map[string]bool)
	for _, name := range posArgs {
		targetNames[name] = true
	}

	filterSkills := func(skills map[string]state.SkillRecord) map[string]state.SkillRecord {
		if len(targetNames) == 0 && categoryFlag == "" && tagsFlag == "" {
			return skills
		}
		res := make(map[string]state.SkillRecord)
		for name, rec := range skills {
			if len(targetNames) > 0 && !targetNames[name] {
				continue
			}
			if categoryFlag != "" || tagsFlag != "" {
				item, err := a.Store.Get(name)
				if err != nil {
					if categoryFlag != "" {
						continue
					}
				} else {
					if categoryFlag != "" && !strings.EqualFold(item.Category, categoryFlag) {
						continue
					}
					if tagsFlag != "" && !catalog.MatchTags(item.Tags, tagsFlag) {
						continue
					}
				}
			}
			res[name] = rec
		}
		return res
	}

	wsInstalled = filterSkills(wsInstalled)
	wsForgotten = filterSkills(wsForgotten)
	globalInstalled = filterSkills(globalInstalled)
	globalForgotten = filterSkills(globalForgotten)

	type StatusOutput struct {
		Workspace       string                          `json:"workspace,omitempty"`
		WorkspaceSkills map[string]state.SkillRecord    `json:"workspace_skills,omitempty"`
		GlobalSkills    map[string]state.SkillRecord    `json:"global_skills"`
		ForgottenSkills map[string]state.SkillRecord    `json:"forgotten_skills,omitempty"`
		History         map[string]*state.HistoryRecord `json:"history,omitempty"`
	}

	out := StatusOutput{
		GlobalSkills: globalInstalled,
		History:      st.History,
	}
	if !globalFlag {
		out.Workspace = wsDir
		out.WorkspaceSkills = wsInstalled
		out.ForgottenSkills = wsForgotten
	}

	mode := a.resolveMode(jsonFlag, rawFlag)
	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return ExitSuccess
	case OutputModeRaw:
		if !globalFlag {
			var wsNames []string
			for n := range wsInstalled {
				wsNames = append(wsNames, n)
			}
			sort.Strings(wsNames)
			for _, n := range wsNames {
				rec := wsInstalled[n]
				fmt.Fprintf(a.Stdout, "workspace\t%s\t%s\tinstalled\t%s\n", rec.Name, rec.Version, rec.Origin)
			}
			for _, rec := range wsForgotten {
				fmt.Fprintf(a.Stdout, "workspace\t%s\t%s\tforgotten\t%s\n", rec.Name, rec.Version, rec.Origin)
			}
		}
		var gNames []string
		for n := range globalInstalled {
			gNames = append(gNames, n)
		}
		sort.Strings(gNames)
		for _, n := range gNames {
			rec := globalInstalled[n]
			fmt.Fprintf(a.Stdout, "global\t%s\t%s\tinstalled\t%s\n", rec.Name, rec.Version, rec.Origin)
		}
		for _, rec := range globalForgotten {
			fmt.Fprintf(a.Stdout, "global\t%s\t%s\tforgotten\t%s\n", rec.Name, rec.Version, rec.Origin)
		}
		return ExitSuccess
	default: // OutputModeHuman
		fmt.Fprintf(a.Stdout, "KungFu Status\n")
		if !globalFlag {
			fmt.Fprintf(a.Stdout, "Workspace: %s\n\n", wsDir)

			// Workspace Installed
			fmt.Fprintf(a.Stdout, "Workspace Skills (%d):\n", len(wsInstalled))
			if len(wsInstalled) == 0 {
				fmt.Fprintf(a.Stdout, "  (none)\n")
			} else {
				var names []string
				for n := range wsInstalled {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					rec := wsInstalled[n]
					ver := rec.Version
					if ver == "" {
						ver = "unversioned"
					}
					if rec.Origin == state.OriginCustom {
						fmt.Fprintf(a.Stdout, "  • %-20s %-12s (%d files, custom - read-only to kungfu)\n", rec.Name, ver, len(rec.Files))
					} else {
						fmt.Fprintf(a.Stdout, "  ✓ %-20s %-12s (%d files, installed %s)\n", rec.Name, ver, len(rec.Files), rec.InstalledAt.Format("2006-01-02"))
					}
				}
			}
		}

		// Global Installed
		fmt.Fprintf(a.Stdout, "\nGlobal Skills (%d):\n", len(globalInstalled))
		if len(globalInstalled) == 0 {
			fmt.Fprintf(a.Stdout, "  (none)\n")
		} else {
			var names []string
			for n := range globalInstalled {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				rec := globalInstalled[n]
				ver := rec.Version
				if ver == "" {
					ver = "unversioned"
				}
				if rec.Origin == state.OriginCustom {
					fmt.Fprintf(a.Stdout, "  • %-20s %-12s (%d files, custom - read-only to kungfu)\n", rec.Name, ver, len(rec.Files))
				} else {
					fmt.Fprintf(a.Stdout, "  ✓ %-20s %-12s (%d files, installed %s)\n", rec.Name, ver, len(rec.Files), rec.InstalledAt.Format("2006-01-02"))
				}
			}
		}

		// Forgotten
		forgottenCount := len(globalForgotten)
		if !globalFlag {
			forgottenCount += len(wsForgotten)
		}
		if forgottenCount > 0 {
			fmt.Fprintf(a.Stdout, "\nForgotten Skills:\n")
			if !globalFlag {
				for _, rec := range wsForgotten {
					uninst := ""
					if rec.UninstalledAt != nil {
						uninst = rec.UninstalledAt.Format("2006-01-02")
					}
					fmt.Fprintf(a.Stdout, "  ✗ %-20s (workspace, uninstalled %s)\n", rec.Name, uninst)
				}
			}
			for _, rec := range globalForgotten {
				uninst := ""
				if rec.UninstalledAt != nil {
					uninst = rec.UninstalledAt.Format("2006-01-02")
				}
				fmt.Fprintf(a.Stdout, "  ✗ %-20s (global, uninstalled %s)\n", rec.Name, uninst)
			}
		}

		// Recent JIT Loads
		if len(st.History) > 0 {
			fmt.Fprintf(a.Stdout, "\nRecent JIT Loads:\n")
			var histNames []string
			for n := range st.History {
				histNames = append(histNames, n)
			}
			sort.Strings(histNames)
			for _, n := range histNames {
				h := st.History[n]
				fmt.Fprintf(a.Stdout, "  • %-20s (last seen %s, loaded %d times)\n", n, h.LastSeenAt.Format("2006-01-02 15:04"), h.LoadCount)
			}
		}

		return ExitSuccess
	}
}
