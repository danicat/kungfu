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
	"github.com/danicat/kungfu/internal/config"
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
	if _, ok := errors.AsType[net.Error](err); ok {
		return ExitNetwork
	}
	if sysErr, ok := errors.AsType[syscall.Errno](err); ok {
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
	Config     *config.Config
}

func (a *App) getHTTPClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return http.DefaultClient
}

func NewApp(opts GlobalOptions, stdout, stderr io.Writer) (*App, error) {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	cfg, err := config.Load(opts.Config)
	if err != nil {
		return nil, err
	}

	catOpts := []catalog.Option{
		catalog.WithLocalDiscovery(true),
	}
	if defURL := cfg.GetCatalogURL("default"); defURL != "" && defURL != config.DefaultCatalogURL {
		catOpts = append(catOpts, catalog.WithRemoteURL(defURL))
	}
	if opts.CacheDir != "" {
		catOpts = append(catOpts, catalog.WithCacheDir(opts.CacheDir))
	}

	cat, err := catalog.NewCatalog(catOpts...)
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
		Config:     cfg,
	}, nil
}

func (a *App) loadState() *state.State {
	s, err := state.LoadState("")
	if err != nil {
		if os.IsNotExist(err) {
			s = state.NewState()
		} else {
			_, _ = fmt.Fprintf(a.Stderr, "FATAL: state file %s is corrupted or unreadable: %v\n", state.DefaultStateFilePath(), err)
			os.Exit(ExitGeneral)
		}
	}

	wd, _ := os.Getwd()
	homeDir, _ := os.UserHomeDir()

	isCatalogKnown := func(name string) bool {
		if a.Store != nil {
			if it, err := a.Store.Get(name); err == nil && it != nil && it.Source != "local" {
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
	err := state.WithLockedState("", func(disk *state.State) error {
		disk.Workspaces = s.Workspaces
		disk.Global = s.Global
		disk.Catalogs = s.Catalogs
		if disk.History == nil {
			disk.History = make(map[string]*state.HistoryRecord)
		}
		for name, h := range s.History {
			if h == nil {
				continue
			}
			cur, ok := disk.History[name]
			if !ok || cur == nil {
				cp := *h
				disk.History[name] = &cp
				continue
			}
			if h.LastSeenAt.After(cur.LastSeenAt) {
				cur.LastSeenAt = h.LastSeenAt
			}
			cur.LoadCount = max(cur.LoadCount, h.LoadCount)
			cur.PageLoadCount = max(cur.PageLoadCount, h.PageLoadCount)
			cur.SubpathLoadCount = max(cur.SubpathLoadCount, h.SubpathLoadCount)
			cur.RunCount = max(cur.RunCount, h.RunCount)
			cur.LearnCount = max(cur.LearnCount, h.LearnCount)
			cur.SyncedLoadCount = max(cur.SyncedLoadCount, h.SyncedLoadCount)
			cur.SyncedPageLoadCount = max(cur.SyncedPageLoadCount, h.SyncedPageLoadCount)
			cur.SyncedSubpathLoadCount = max(cur.SyncedSubpathLoadCount, h.SyncedSubpathLoadCount)
			cur.SyncedRunCount = max(cur.SyncedRunCount, h.SyncedRunCount)
			cur.SyncedLearnCount = max(cur.SyncedLearnCount, h.SyncedLearnCount)
			if h.LastSyncedAt != nil && (cur.LastSyncedAt == nil || h.LastSyncedAt.After(*cur.LastSyncedAt)) {
				ts := *h.LastSyncedAt
				cur.LastSyncedAt = &ts
			}
		}
		return nil
	})
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: failed to persist state to %s: %v\n", state.DefaultStateFilePath(), err)
	}
}

func (a *App) recordSkillLoad(skillName, subpath string, page int) {
	err := state.WithLockedState("", func(s *state.State) error {
		s.RecordLoadDetailed(skillName, subpath, page)
		return nil
	})
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: failed to record load telemetry in %s: %v\n", state.DefaultStateFilePath(), err)
	}
}

func (a *App) recordSkillRun(skillName string) {
	err := state.WithLockedState("", func(s *state.State) error {
		s.RecordRun(skillName)
		return nil
	})
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: failed to record run telemetry in %s: %v\n", state.DefaultStateFilePath(), err)
	}
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
		_, _ = fmt.Fprintf(a.Stderr, "error: "+format+"\n", args...)
	}
}

func truncateSuggestionDescription(desc string) string {
	clean := strings.Join(strings.Fields(desc), " ")
	runes := []rune(clean)
	if len(runes) > 69 {
		return string(runes[:66]) + "..."
	}
	return clean
}

func (a *App) printNotFoundWithSuggestions(cmd, query string) {
	_, _ = fmt.Fprintf(a.Stderr, "error: skill %q not found.\n", query)

	const maxSuggestions = 5
	searchQuery := query
	if idx := strings.LastIndex(searchQuery, "@"); idx > 0 {
		searchQuery = searchQuery[:idx]
	}

	allItems := a.Store.All()
	itemByName := make(map[string]catalog.Item, len(allItems))
	var skillNames []string
	for _, it := range allItems {
		skillNames = append(skillNames, it.Name)
		itemByName[strings.ToLower(strings.TrimSpace(it.Name))] = it
	}

	var matchedItems []catalog.Item
	seen := make(map[string]bool)
	addCandidate := func(it catalog.Item) {
		key := strings.ToLower(strings.TrimSpace(it.Name))
		if key == "" || seen[key] || len(matchedItems) >= maxSuggestions {
			return
		}
		seen[key] = true
		matchedItems = append(matchedItems, it)
	}

	// 1. Fuzzy and substring match against all known skill names
	for _, s := range search.Suggest(searchQuery, skillNames, maxSuggestions) {
		if it, ok := itemByName[strings.ToLower(strings.TrimSpace(s))]; ok {
			addCandidate(it)
		} else {
			addCandidate(catalog.Item{Name: s})
		}
	}

	// 2. Hybrid semantic/keyword search across Name, Category, Tags, and Description
	if len(matchedItems) < maxSuggestions && a.Searcher != nil && len(allItems) > 0 {
		_ = a.Searcher.Index(allItems)
		for _, r := range a.Searcher.Search(searchQuery, "hybrid", maxSuggestions, 0.0) {
			addCandidate(r.Item)
		}
	}

	maxNameLen := 0
	for _, it := range matchedItems {
		if len(it.Name) > maxNameLen {
			maxNameLen = len(it.Name)
		}
	}

	var suggestions []string
	for _, it := range matchedItems {
		desc := truncateSuggestionDescription(it.Description)
		if desc != "" {
			suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s %-*s # %s", cmd, maxNameLen, it.Name, desc))
		} else {
			suggestions = append(suggestions, fmt.Sprintf("  - kungfu %s %s", cmd, it.Name))
		}
	}

	if cmd != "find" {
		suggestions = append(suggestions, fmt.Sprintf("  - kungfu find %s", query))
	}

	if len(suggestions) > 0 {
		_, _ = fmt.Fprintln(a.Stderr, "\nDid you mean:")
		for _, sug := range suggestions {
			_, _ = fmt.Fprintln(a.Stderr, sug)
		}
	} else {
		_, _ = fmt.Fprintln(a.Stderr, "\nRun 'kungfu find <term>' to search for relevant skills across descriptions and keywords.")
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
		if _, err := os.Stat(filepath.Join(wd, "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeWorkspace
		}
	}
	if homeDir != "" {
		if _, err := os.Stat(filepath.Join(homeDir, ".agents", "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeGlobal
		}
		if _, err := os.Stat(filepath.Join(homeDir, ".cache", "kungfu", "skills", item.Name, "SKILL.md")); err == nil {
			return "cached"
		}
	}
	if cacheRoot := a.cacheRootDir(); cacheRoot != "" {
		if _, err := os.Stat(filepath.Join(cacheRoot, "skills", item.Name, "SKILL.md")); err == nil {
			return "cached"
		}
	}

	return "catalog"
}

// Run executes the command line entrypoint for kungfu CLI.
func Run(args []string, stdout, stderr io.Writer) int {
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
		_, _ = fmt.Fprintf(stdout, "kungfu version %s\n", version.Get())
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

	switch cmdName {
	case "list", "ls", "find", "search", "show", "info", "load", "get",
		"learn", "install", "add", "forget", "unlearn", "update", "upgrade",
		"status", "catalog", "catalogs", "telemetry", "run", "exec":
	default:
		validCommands := []string{
			"list", "find", "show", "load", "learn", "forget", "update", "status", "catalog", "telemetry", "run", "version", "help",
		}
		_, _ = fmt.Fprintf(stderr, "error: unknown command %q.\n", cmdName)
		suggs := search.Suggest(cmdName, validCommands, 2)
		if len(suggs) > 0 {
			_, _ = fmt.Fprintln(stderr, "\nDid you mean:")
			for _, s := range suggs {
				_, _ = fmt.Fprintf(stderr, "  - kungfu %s\n", s)
			}
		}
		_, _ = fmt.Fprintln(stderr, "\nRun 'kungfu --help' for available commands and usage.")
		return ExitUsage
	}

	app, err := NewApp(opts, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
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
	case "telemetry":
		return app.RunTelemetry(cmdArgs)
	case "run", "exec":
		return app.RunRun(cmdArgs)
	default:
		return ExitUsage
	}
}

func PrintGlobalHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, `kungfu - Agent Skills Gateway

USAGE:
  kungfu [global flags] <command> [flags] [arguments...]

COMMANDS:
  find      <keywords...> [--limit <n>]
            Search skills by keyword.
  load      <skill[/subpath]> [-p <page>] [--full] [--local] [-u] [-f]
            Stream skill instructions or reference files to stdout.
  run       <skill> <script> [args...]
            Run a companion script bundled with a skill.
  list      [--json] [--raw]
            List available skills.
  show      <skill...> [--json] [--raw]
            Show skill metadata, references, and scripts.
  learn     <skill...> [-g] [-f] [--load]
            Install skills to workspace or global scope.
  status    [skill...] [-g] [--json] [--raw]
            Show installed skills and usage history.
  update    [skill...] [-g] [--all] [-y] [-f] [--refresh]
            Synchronize catalogs and check for or apply skill updates.
  forget    <skill...> [-g] [-f] [--all]
            Uninstall skills from workspace or global scope.
  catalog   <add|list|remove> [source] [flags]
            Manage external skill catalogs.
  telemetry [status|enable|disable|sync] [--json] [--raw]
            Manage opt-in anonymous community usage counters.
  version
            Print kungfu version.

GLOBAL FLAGS:
  --json            Emit JSON output
  --raw             Emit raw tab/text output
  --config <path>   Custom configuration file path
  --cache-dir <dir> Custom cache directory (default ~/.cache/kungfu)
  -v, --verbose     Verbose diagnostics on stderr
  --no-color        Disable colored output
  -V, --version     Print version
  -h, --help        Show help`)
}

func (a *App) cacheRootDir() string {
	if a.Options.CacheDir != "" {
		return a.Options.CacheDir
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		return filepath.Join(homeDir, ".cache", "kungfu")
	}
	return ""
}

func (a *App) ensureArchiveUnpacked(item *catalog.Item) {
	if item == nil || !catalog.IsArchiveItem(*item) {
		return
	}
	cacheRoot := a.cacheRootDir()
	if cacheRoot != "" && catalog.IsArchiveCachedWithDigest(cacheRoot, item.Name, item.EffectiveDigest()) {
		if data, err := os.ReadFile(filepath.Join(cacheRoot, "skills", item.Name, "SKILL.md")); err == nil {
			catalog.EnrichItemFromSkillMD(item, data)
		}
		return
	}
	if a.Store != nil {
		_, _ = a.Store.LoadContent(item)
	}
}

func (a *App) candidateBaseDirs(item catalog.Item) []string {
	dirs := runner.GetCandidateBaseDirs(item)
	if a.Options.CacheDir == "" {
		return dirs
	}
	add := func(d string) {
		d = filepath.Clean(d)
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			return
		}
		for _, existing := range dirs {
			if existing == d {
				return
			}
		}
		dirs = append(dirs, d)
	}
	expectedDigest := item.EffectiveDigest()
	canUseCache := func(skillName string) bool {
		if item.Type == catalog.ItemTypeSkillMD && item.Source != "local" {
			return false
		}
		if catalog.IsArchiveItem(item) && expectedDigest != "" {
			return catalog.IsArchiveCachedWithDigest(a.Options.CacheDir, skillName, expectedDigest)
		}
		return true
	}
	if item.Name != "" && canUseCache(item.Name) {
		add(filepath.Join(a.Options.CacheDir, "skills", item.Name))
	}
	if item.ID != "" && item.ID != item.Name && canUseCache(item.ID) {
		add(filepath.Join(a.Options.CacheDir, "skills", item.ID))
	}
	return dirs
}

func walkRelativeFiles(rootDir string) []string {
	var files []string
	_ = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != rootDir && (strings.HasPrefix(name, ".") || name == "__pycache__" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		rel, err := filepath.Rel(rootDir, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files
}

func isScriptFile(relPath string) bool {
	clean := filepath.ToSlash(filepath.Clean(relPath))
	base := strings.ToLower(filepath.Base(clean))
	if base == "" || base == "." || strings.HasPrefix(base, ".") {
		return false
	}
	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "tsconfig.json",
		"pyproject.toml", "requirements.txt", "pipfile", "pipfile.lock", "poetry.lock",
		"uv.lock", "bun.lockb", "bun.lock", "deno.json", "deno.lock", "cargo.toml",
		"cargo.lock", "makefile", "license", "notice", "readme", "readme.md":
		return false
	}
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".pyc", ".pyo", ".pyd", ".md", ".txt", ".json", ".yaml", ".yml",
		".toml", ".lock", ".sum", ".mod", ".sql", ".csv", ".tsv", ".html",
		".css", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".wasm":
		return false
	}
	return true
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

	a.ensureArchiveUnpacked(&item)

	// 1. Check local or unpacked archive candidate directories
	baseDirs := a.candidateBaseDirs(item)
	if item.Type == catalog.ItemTypeSkillMD && item.Source != "local" && len(baseDirs) == 0 {
		return nil
	}
	for _, baseDir := range baseDirs {
		refsDir := filepath.Join(baseDir, "references")
		for _, rel := range walkRelativeFiles(refsDir) {
			add(rel)
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
		if clean != "" && isScriptFile(clean) && !seen[clean] {
			seen[clean] = true
			scripts = append(scripts, clean)
		}
	}

	a.ensureArchiveUnpacked(&item)

	// Check local or unpacked archive candidate directories on disk
	baseDirs := a.candidateBaseDirs(item)
	if item.Type == catalog.ItemTypeSkillMD && item.Source != "local" && len(baseDirs) == 0 {
		return nil
	}
	for _, baseDir := range baseDirs {
		scriptsDir := filepath.Join(baseDir, "scripts")
		for _, rel := range walkRelativeFiles(scriptsDir) {
			add(rel)
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
