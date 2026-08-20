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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/search"
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

const version = "0.1.0"

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
	Store    catalog.Store
	Searcher *search.Engine
	Stdout   io.Writer
	Stderr   io.Writer
	Options  GlobalOptions
}

func NewApp(opts GlobalOptions, stdout, stderr io.Writer) (*App, error) {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	catOpts := []catalog.Option{
		catalog.WithLocalDiscovery(true),
		catalog.WithRemoteSync(true),
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
		Store:    store,
		Searcher: engine,
		Stdout:   stdout,
		Stderr:   stderr,
		Options:  opts,
	}, nil
}

func parseFlagsWithArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags []string
	var posArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
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
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flags = append(flags, args[i])
				}
			}
		} else {
			posArgs = append(posArgs, arg)
		}
	}
	reordered := append(flags, posArgs...)
	if err := fs.Parse(reordered); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func (a *App) resolveMode(jsonFlag, rawFlag, humanFlag bool) OutputMode {
	if jsonFlag {
		return OutputModeJSON
	}
	if humanFlag {
		return OutputModeHuman
	}
	if rawFlag {
		return OutputModeRaw
	}
	if a.Options.Mode != "" {
		return a.Options.Mode
	}
	return OutputModeRaw
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
	if strings.Contains(errStr, "not found") || strings.Contains(errStr, "no such file") {
		return ExitNotFound
	}
	if strings.Contains(errStr, "network") || strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "timeout") {
		return ExitNetwork
	}
	if strings.Contains(errStr, "digest") || strings.Contains(errStr, "checksum") || strings.Contains(errStr, "invalid frontmatter") {
		return ExitIntegrity
	}
	return ExitGeneral
}

func (a *App) RunList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag, humanFlag bool

	fs.StringVar(&category, "category", "", "Filter skills by category")
	fs.StringVar(&category, "c", "", "Filter skills by category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Filter skills by boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Filter skills by boolean tag expression (shorthand)")
	fs.BoolVar(&allFlag, "all", false, "List all skills across all categories")
	fs.BoolVar(&allFlag, "a", false, "List all skills (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.BoolVar(&humanFlag, "human", false, "Output in human-readable table format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if category == "" && len(posArgs) > 0 {
		category = posArgs[0]
	}

	mode := a.resolveMode(jsonFlag, rawFlag, humanFlag)
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

	case OutputModeHuman:
		if category == "" && tagQuery == "" && len(categories) > 0 {
			fmt.Fprintln(a.Stdout, "CATEGORIES:")
			fmt.Fprintf(a.Stdout, "%-18s %-6s %s\n", "NAME", "ITEMS", "DESCRIPTION")
			fmt.Fprintf(a.Stdout, "%-18s %-6s %s\n", "------------------", "------", "----------------------------------------")
			for _, cat := range categories {
				fmt.Fprintf(a.Stdout, "%-18s %-6d %s\n", cat.Name, cat.Count, cat.Description)
			}
			fmt.Fprintln(a.Stdout)
		}
		fmt.Fprintln(a.Stdout, "SKILLS & KNOWLEDGE DOCS:")
		fmt.Fprintf(a.Stdout, "%-22s %-6s %-14s %s\n", "NAME", "TYPE", "CATEGORY", "DESCRIPTION")
		fmt.Fprintf(a.Stdout, "%-22s %-6s %-14s %s\n", "----------------------", "------", "--------------", "----------------------------------------")
		for _, item := range items {
			fmt.Fprintf(a.Stdout, "%-22s %-6s %-14s %s\n", item.Name, item.Type, item.Category, item.Description)
		}
		return ExitSuccess

	default: // OutputModeRaw
		var sb strings.Builder
		if category == "" && tagQuery == "" && len(categories) > 0 {
			sb.WriteString("# Categories\n\n")
			for _, cat := range categories {
				sb.WriteString(fmt.Sprintf("- **%s** (%d items): %s\n", cat.Name, cat.Count, cat.Description))
			}
			sb.WriteString("\n# Skills & Knowledge Documents\n\n")
		}
		for _, item := range items {
			tagInfo := ""
			if len(item.Tags) > 0 {
				tagInfo = fmt.Sprintf(" `tags: %s`", strings.Join(item.Tags, ", "))
			}
			sb.WriteString(fmt.Sprintf("## %s (%s) [%s]%s\n%s\n\n", item.Name, item.Type, item.Category, tagInfo, item.Description))
		}
		fmt.Fprint(a.Stdout, sb.String())
		return ExitSuccess
	}
}

func (a *App) RunShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag, humanFlag bool

	fs.StringVar(&category, "category", "", "Show all skills in category")
	fs.StringVar(&category, "c", "", "Show all skills in category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Show all skills matching boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Show all skills matching boolean tag expression (shorthand)")
	fs.BoolVar(&allFlag, "all", false, "Show all skills")
	fs.BoolVar(&allFlag, "a", false, "Show all skills (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.BoolVar(&humanFlag, "human", false, "Output in human-readable format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag, humanFlag)

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
			if err != nil {
				a.printNotFoundWithSuggestions("show", name)
				return ExitNotFound
			}
			targetItems = append(targetItems, *item)
		}
	} else {
		fmt.Fprintln(a.Stderr, "error: show requires a skill name, --category, --tags, or --all")
		return ExitUsage
	}

	return a.renderShowItems(targetItems, mode)
}

func (a *App) renderShowItems(items []catalog.Item, mode OutputMode) int {
	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(items)
		return ExitSuccess

	case OutputModeHuman:
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

	default: // OutputModeRaw
		var sb strings.Builder
		for i, item := range items {
			if i > 0 {
				sb.WriteString("\n---\n\n")
			}
			sb.WriteString(fmt.Sprintf("# %s\n\n", item.Name))
			sb.WriteString(fmt.Sprintf("- **Category**: `%s`\n", item.Category))
			sb.WriteString(fmt.Sprintf("- **Type**: `%s`\n", item.Type))
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
	}
}

func (a *App) RunLoad(args []string) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag, humanFlag bool

	fs.StringVar(&category, "category", "", "Load all skills in category")
	fs.StringVar(&category, "c", "", "Load all skills in category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Load all skills matching boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Load all skills matching boolean tag expression (shorthand)")
	fs.BoolVar(&allFlag, "all", false, "Load all skills")
	fs.BoolVar(&allFlag, "a", false, "Load all skills (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.BoolVar(&humanFlag, "human", false, "Output in human-readable format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag, humanFlag)

	var targetItems []catalog.Item

	if allFlag {
		targetItems = a.Store.All()
	} else if category != "" || tagQuery != "" {
		targetItems = a.Store.ListByFilter(catalog.QueryFilter{
			Category: category,
			TagQuery: tagQuery,
		})
		if len(targetItems) == 0 {
			a.printFilterNotFoundWithSuggestions("load", category, tagQuery)
			return ExitNotFound
		}
	} else if len(posArgs) > 0 {
		for _, name := range posArgs {
			item, err := a.Store.Get(name)
			if err != nil {
				a.printNotFoundWithSuggestions("load", name)
				return ExitNotFound
			}
			targetItems = append(targetItems, *item)
		}
	} else {
		fmt.Fprintln(a.Stderr, "error: load requires skill name(s), --category, --tags, or --all")
		return ExitUsage
	}

	var loadedDocs []catalog.LoadedDoc
	for _, item := range targetItems {
		content, err := a.loadItemContent(&item)
		if err != nil {
			a.logError("loading content for %q: %v", item.Name, err)
			return mapErrorToExitCode(err)
		}
		loadedDocs = append(loadedDocs, catalog.LoadedDoc{
			ID:       item.ID,
			Name:     item.Name,
			Type:     string(item.Type),
			Category: item.Category,
			Tags:     item.Tags,
			Path:     item.Path,
			Content:  content,
			Metadata: item.Metadata,
		})
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(loadedDocs)
		return ExitSuccess

	case OutputModeHuman:
		for i, doc := range loadedDocs {
			if i > 0 {
				fmt.Fprintln(a.Stdout, "\n============================================================")
			}
			fmt.Fprintf(a.Stdout, "SKILL: %s [%s] (Category: %s)\n", doc.Name, doc.Type, doc.Category)
			fmt.Fprintln(a.Stdout, "------------------------------------------------------------")
			fmt.Fprintln(a.Stdout, doc.Content)
		}
		return ExitSuccess

	default: // OutputModeRaw
		var sb strings.Builder
		for i, doc := range loadedDocs {
			if i > 0 {
				sb.WriteString("\n\n---\n\n")
			}
			sb.WriteString(doc.Content)
		}
		fmt.Fprint(a.Stdout, sb.String())
		return ExitSuccess
	}
}

func (a *App) RunLearn(args []string) int {
	fs := flag.NewFlagSet("learn", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var globalFlag, forceFlag bool
	var category, tagQuery string
	var allFlag, jsonFlag, rawFlag, humanFlag bool

	fs.BoolVar(&globalFlag, "global", false, "Install skill to global directory (~/.agents/skills/)")
	fs.BoolVar(&globalFlag, "g", false, "Install skill to global directory (shorthand)")
	fs.BoolVar(&forceFlag, "force", false, "Overwrite existing skill directory")
	fs.BoolVar(&forceFlag, "f", false, "Overwrite existing skill directory (shorthand)")
	fs.StringVar(&category, "category", "", "Learn all skills in category")
	fs.StringVar(&category, "c", "", "Learn all skills in category (shorthand)")
	fs.StringVar(&tagQuery, "tags", "", "Learn all skills matching boolean tag expression")
	fs.StringVar(&tagQuery, "t", "", "Learn all skills matching boolean tag expression (shorthand)")
	fs.BoolVar(&allFlag, "all", false, "Learn all skills")
	fs.BoolVar(&allFlag, "a", false, "Learn all skills (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.BoolVar(&humanFlag, "human", false, "Output in human-readable format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag, humanFlag)
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
		Name    string `json:"name"`
		Path    string `json:"path"`
		Status  string `json:"status"`
		Message string `json:"message,omitempty"`
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

		if fi, err := os.Stat(destDir); err == nil && fi.IsDir() {
			if !forceFlag {
				results = append(results, LearnResult{
					Name:    item.Name,
					Path:    destDir,
					Status:  "skipped",
					Message: "already exists (use -f/--force to overwrite)",
				})
				continue
			}
		}

		if err := os.MkdirAll(destDir, 0755); err != nil {
			a.logError("creating skill directory %q: %v", destDir, err)
			return ExitGeneral
		}

		// Write or copy content
		if item.Path != "" {
			// Copy from source folder
			if err := copyDir(item.Path, destDir); err != nil {
				a.logError("copying skill from %q to %q: %v", item.Path, destDir, err)
				return ExitGeneral
			}
		} else {
			// Write synthesized or embedded SKILL.md
			content, err := a.loadItemContent(&item)
			if err != nil {
				a.logError("rendering content for skill %q: %v", item.Name, err)
				return mapErrorToExitCode(err)
			}
			skillFile := filepath.Join(destDir, "SKILL.md")
			if err := os.WriteFile(skillFile, []byte(content), 0644); err != nil {
				a.logError("writing SKILL.md to %q: %v", skillFile, err)
				return ExitGeneral
			}
		}

		results = append(results, LearnResult{
			Name:   item.Name,
			Path:   destDir,
			Status: "installed",
		})
	}

	switch mode {
	case OutputModeJSON:
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		return ExitSuccess
	default:
		for _, r := range results {
			if r.Status == "installed" {
				fmt.Fprintf(a.Stdout, "✓ Learned skill %q -> %s\n", r.Name, r.Path)
			} else {
				fmt.Fprintf(a.Stdout, "! Skipped skill %q: %s (%s)\n", r.Name, r.Message, r.Path)
			}
		}
		return ExitSuccess
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	perm := mode.Perm()
	if perm == 0 {
		perm = 0644
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip symlinks
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target, info.Mode())
	})
}

func (a *App) RunFind(args []string) int {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var strategy string
	var limit int
	var threshold float64
	var category, tagQuery string
	var jsonFlag, rawFlag, humanFlag bool

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
	fs.BoolVar(&humanFlag, "human", false, "Output in human-readable table format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	query := strings.Join(posArgs, " ")
	if strings.TrimSpace(query) == "" && category == "" && tagQuery == "" {
		fmt.Fprintln(a.Stderr, "error: find requires keywords or filtering flags (--category, --tags)")
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag, humanFlag)
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

	case OutputModeHuman:
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

	default: // OutputModeRaw
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
	}
}

func (a *App) RunRead(args []string) int {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag, humanFlag bool
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.BoolVar(&humanFlag, "human", false, "Output in human-readable format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(posArgs) < 2 {
		fmt.Fprintln(a.Stderr, "error: read requires <skill> <reference>")
		return ExitUsage
	}

	skillName := posArgs[0]
	refName := posArgs[1]

	item, err := a.Store.Get(skillName)
	if err != nil {
		a.printNotFoundWithSuggestions("read", skillName)
		return mapErrorToExitCode(err)
	}

	content, err := runner.ReadReference(*item, refName)
	if err != nil {
		if errors.Is(err, runner.ErrInvalidPath) {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitUsage
		}
		fmt.Fprintf(a.Stderr, "error: reference %q not found in skill %q.\n", refName, skillName)
		refs := listReferences(*item)
		if len(refs) > 0 {
			suggs := search.Suggest(refName, refs, 3)
			if len(suggs) > 0 {
				fmt.Fprintln(a.Stderr, "\nDid you mean:")
				for _, s := range suggs {
					fmt.Fprintf(a.Stderr, "  - kungfu read %s %s\n", skillName, s)
				}
			}
		}
		return ExitNotFound
	}

	mode := a.resolveMode(jsonFlag, rawFlag, humanFlag)
	switch mode {
	case OutputModeJSON:
		type ReadResp struct {
			Skill     string `json:"skill"`
			Reference string `json:"reference"`
			Content   string `json:"content"`
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(ReadResp{Skill: skillName, Reference: refName, Content: content})
		return ExitSuccess
	default:
		fmt.Fprint(a.Stdout, content)
		return ExitSuccess
	}
}

func (a *App) RunRun(args []string) int {
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
			scripts := listScripts(*item)
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

func listReferences(item catalog.Item) []string {
	if item.Path == "" {
		return nil
	}
	baseDir := item.Path
	if fi, err := os.Stat(baseDir); err == nil && !fi.IsDir() {
		baseDir = filepath.Dir(baseDir)
	}
	var refs []string
	seen := make(map[string]bool)
	refsDir := filepath.Join(baseDir, "references")
	if entries, err := os.ReadDir(refsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				clean := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
				if !seen[clean] {
					seen[clean] = true
					refs = append(refs, clean)
				}
			}
		}
	}
	return refs
}

func listScripts(item catalog.Item) []string {
	if item.Path == "" {
		return nil
	}
	baseDir := item.Path
	if fi, err := os.Stat(baseDir); err == nil && !fi.IsDir() {
		baseDir = filepath.Dir(baseDir)
	}
	var scripts []string
	seen := make(map[string]bool)
	scriptsDir := filepath.Join(baseDir, "scripts")
	if entries, err := os.ReadDir(scriptsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				if !seen[e.Name()] {
					seen[e.Name()] = true
					scripts = append(scripts, e.Name())
				}
			}
		}
	}
	return scripts
}

func (a *App) RunSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var forceFlag, jsonFlag, rawFlag, humanFlag bool
	fs.BoolVar(&forceFlag, "force", false, "Force synchronization ignoring local cache")
	fs.BoolVar(&forceFlag, "f", false, "Force synchronization (shorthand)")
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.BoolVar(&humanFlag, "human", false, "Output in human-readable format")

	if _, err := parseFlagsWithArgs(fs, args); err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag, rawFlag, humanFlag)

	err := a.Store.Sync(forceFlag)
	if err != nil {
		a.logError("catalog sync failed: %v", err)
		return mapErrorToExitCode(err)
	}

	items := a.Store.All()
	categories := a.Store.Categories()

	switch mode {
	case OutputModeJSON:
		type SyncPayload struct {
			Status          string `json:"status"`
			ItemsCount      int    `json:"items_count"`
			CategoriesCount int    `json:"categories_count"`
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(SyncPayload{Status: "synced", ItemsCount: len(items), CategoriesCount: len(categories)})
		return ExitSuccess
	default:
		fmt.Fprintf(a.Stdout, "Catalog synchronized successfully. Total items: %d, Categories: %d\n", len(items), len(categories))
		return ExitSuccess
	}
}

func (a *App) loadItemContent(item *catalog.Item) (string, error) {
	return a.Store.LoadContent(item)
}
