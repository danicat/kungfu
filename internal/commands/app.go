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
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/search"
	"github.com/danicat/kungfu/internal/state"
	"github.com/danicat/kungfu/internal/version"
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

func MapErrorToExitCode(err error) int {
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
	EmbeddedFS fs.FS
}

func (a *App) getHTTPClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return http.DefaultClient
}

func NewApp(opts GlobalOptions, stdout, stderr io.Writer, embeddedFS fs.FS) (*App, error) {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	cat, err := catalog.NewCatalog(
		catalog.WithEmbeddedFS(embeddedFS),
		catalog.WithLocalDiscovery(true),
	)
	if err != nil {
		return nil, fmt.Errorf("initializing catalog: %w", err)
	}

	searcher := search.NewEngine()
	return &App{
		Store:      cat,
		Searcher:   searcher,
		Stdout:     stdout,
		Stderr:     stderr,
		Options:    opts,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		EmbeddedFS: embeddedFS,
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
	reordered = append(reordered, posArgs...)

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
	if a.Options.Verbose {
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
	} else {
		fmt.Fprintln(a.Stderr, "\nRun 'kungfu find <term>' to search for relevant skills across descriptions and keywords.")
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

		if item, err := a.Store.Get(category); err == nil && item != nil {
			suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s %s (is a skill name, not a category)", cmd, item.Name))
		} else {
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
	} else {
		fmt.Fprintln(a.Stderr, "\nRun 'kungfu list' to see all available skills and categories.")
	}
}

func (a *App) getSkillStatus(item *catalog.Item) string {
	wd, _ := os.Getwd()
	homeDir, _ := os.UserHomeDir()
	st := a.loadState()

	if wd != "" && st.Workspaces != nil {
		if ws, ok := st.Workspaces[wd]; ok && ws.Skills != nil {
			if rec, found := ws.Skills[item.Name]; found {
				if rec.Status == state.StatusInstalled {
					return state.ScopeWorkspace
				}
			}
		}
	}

	if st.Global != nil && st.Global.Skills != nil {
		if rec, found := st.Global.Skills[item.Name]; found {
			if rec.Status == state.StatusInstalled {
				return state.ScopeGlobal
			}
		}
	}

	if wd != "" {
		if _, err := os.Stat(filepath.Join(wd, ".agents", "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeWorkspace
		}
		if _, err := os.Stat(filepath.Join(wd, ".gemini", "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeWorkspace
		}
		if _, err := os.Stat(filepath.Join(wd, "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeWorkspace
		}
	}
	if homeDir != "" {
		if _, err := os.Stat(filepath.Join(homeDir, ".agents", "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeGlobal
		}
		if _, err := os.Stat(filepath.Join(homeDir, ".gemini", "config", "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeGlobal
		}
		if _, err := os.Stat(filepath.Join(homeDir, ".cache", "kungfu", "skills", item.Name, "SKILL.md")); err == nil {
			return "cached"
		}
	}

	return "catalog"
}

// Run executes the command line entrypoint for kungfu CLI.
func Run(args []string, stdout, stderr io.Writer, embeddedFS fs.FS) int {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	var opts GlobalOptions
	var showVersion, showHelp bool
	var jsonFlag, rawFlag bool

	fs := flag.NewFlagSet("kungfu", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.StringVar(&opts.Config, "config", "", "Path to custom config file")
	fs.BoolVar(&opts.Verbose, "verbose", false, "Enable verbose logging")
	fs.BoolVar(&opts.Verbose, "v", false, "Enable verbose logging (shorthand)")
	fs.BoolVar(&opts.NoColor, "no-color", false, "Disable color and ANSI styling")
	fs.StringVar(&opts.CacheDir, "cache-dir", "", "Custom cache directory")
	fs.BoolVar(&showVersion, "version", false, "Print version information")
	fs.BoolVar(&showVersion, "V", false, "Print version information (shorthand)")
	fs.BoolVar(&showHelp, "help", false, "Show help information")
	fs.BoolVar(&showHelp, "h", false, "Show help information (shorthand)")

	fs.Usage = func() {
		PrintGlobalHelp(stdout)
	}

	var globalArgs []string
	var cmdName string
	var cmdArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			globalArgs = append(globalArgs, arg)
			if (arg == "--config" || arg == "-config" || arg == "--cache-dir" || arg == "-cache-dir") && i+1 < len(args) {
				i++
				globalArgs = append(globalArgs, args[i])
			}
		} else {
			cmdName = arg
			cmdArgs = args[i+1:]
			break
		}
	}

	if err := fs.Parse(globalArgs); err != nil {
		return ExitUsage
	}

	if showVersion || cmdName == "version" {
		fmt.Fprintf(stdout, "kungfu version %s\n", version.Get())
		return ExitSuccess
	}

	if showHelp || (cmdName == "" && len(globalArgs) == 0) || cmdName == "help" {
		PrintGlobalHelp(stdout)
		if cmdName == "" && len(globalArgs) == 0 {
			return ExitUsage
		}
		return ExitSuccess
	}

	if jsonFlag {
		opts.Mode = OutputModeJSON
	} else if rawFlag {
		opts.Mode = OutputModeRaw
	}

	app, err := NewApp(opts, stdout, stderr, embeddedFS)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return MapErrorToExitCode(err)
	}

	switch cmdName {
	case "list", "ls":
		return app.RunList(cmdArgs)
	case "find", "search":
		return app.RunFind(cmdArgs)
	case "show", "info":
		return app.RunShow(cmdArgs)
	case "load", "get":
		return app.RunLoad(cmdArgs)
	case "learn", "install", "add":
		return app.RunLearn(cmdArgs)
	case "forget", "unlearn":
		return app.RunForget(cmdArgs)
	case "update", "upgrade":
		return app.RunUpdate(cmdArgs)
	case "status":
		return app.RunStatus(cmdArgs)
	case "catalog", "catalogs":
		return app.RunCatalog(cmdArgs)
	case "run", "exec":
		return app.RunRun(cmdArgs)
	default:
		validCommands := []string{
			"list", "find", "show", "load", "learn", "forget", "update", "status", "catalog", "run", "version", "help",
		}
		fmt.Fprintf(stderr, "error: unknown command %q.\n", cmdName)
		suggs := search.Suggest(cmdName, validCommands, 2)
		if len(suggs) > 0 {
			fmt.Fprintln(stderr, "\nDid you mean:")
			for _, s := range suggs {
				fmt.Fprintf(stderr, "  - kungfu %s\n", s)
			}
		}
		fmt.Fprintln(stderr, "\nRun 'kungfu --help' for available commands and usage.")
		return ExitUsage
	}
}

func PrintGlobalHelp(w io.Writer) {
	fmt.Fprintln(w, `kungfu - Agent Skills Gateway

USAGE:
  kungfu [global flags] <command> [flags] [arguments...]

COMMANDS:
  list    [--category <cat>] [--tags <expr>] [--all] [--json] [--raw]
          List available skill categories and items.
  find    <keywords...> [--category <cat>] [--tags <expr>] [--strategy hybrid|bm25|tfidf|exact] [--limit <n>] [--threshold <t>] [--json] [--raw]
          Search skills with pure Go BM25/TF-IDF/Hybrid ranking.
  show    <skill1> [skill2...] [--category <cat>] [--tags <expr>] [--all] [--json] [--raw]
          Display metadata, description, tags, and summary for one or more skills.
  load    <skill1[/subpath]> [skill2...] [--category <cat>] [--tags <expr>] [--all] [--json] [--raw]
          Load verbatim markdown instructions or subpath files (references, scripts, assets) for JIT execution.
  learn   <skill1> [skill2...] [--global] [--force] [--load] [--category <cat>] [--tags <expr>] [--all] [--json] [--raw]
          Install and persist skills to canonical workspace or global locations (optionally stream with --load).
  forget  <skill1> [skill2...] [--global] [--force] [--all] [--json] [--raw]
          Uninstall and soft-delete skills from workspace or global locations.
  update  [skill1...] [--global] [--all] [--yes] [--force] [--refresh] [--json] [--raw]
          Check for updates (dry-run without args) or apply updates to installed skills.
  catalog <add|list|remove|sync> [source] [flags]
          Manage external skill catalogs (GitHub repositories, HTTP manifests, local directories).
  status  [skill1...] [--category <cat>] [--tags <expr>] [--global] [--json] [--raw]
          Show installed skills, workspace paths, versions, forgotten skills, and JIT load history.
  version
          Display kungfu version.
  run     <skill> <script> [args...]
          Execute a script bundled within an agent skill.

GLOBAL FLAGS:
  --json            Emit strict RFC 8259 JSON output
  --raw             Emit verbatim raw markdown/text
  --config <path>   Path to custom configuration file
  --cache-dir <dir> Custom cache directory (default ~/.cache/kungfu)
  --verbose, -v     Verbose output to stderr
  --no-color        Disable colored output
  --version, -V     Show kungfu version
  --help, -h        Show this help message

EXIT CODES:
  0: Success
  1: General error
  2: Usage error / Invalid arguments
  3: Item or category not found
  4: Network error
  5: Integrity error (digest mismatch)`)
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
