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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

var canonicalCommands = []string{
	"list", "find", "show", "load", "learn", "forget", "update", "status", "catalog", "run", "version", "help",
}

// MapErrorToExitCode maps typed errors to deterministic CLI exit codes.
func MapErrorToExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, runner.ErrScriptNotFound) || errors.Is(err, runner.ErrFileNotFound) {
		return ExitNotFound
	}
	if errors.Is(err, catalog.ErrInvalidName) || errors.Is(err, catalog.ErrAmbiguousCatalog) || errors.Is(err, runner.ErrInvalidPath) {
		return ExitUsage
	}
	if errors.Is(err, catalog.ErrDigestMismatch) || errors.Is(err, catalog.ErrMissingDigest) ||
		errors.Is(err, catalog.ErrInvalidFrontmatter) || errors.Is(err, catalog.ErrInsecureURL) ||
		errors.Is(err, catalog.ErrArchiveTraversal) || errors.Is(err, catalog.ErrArchiveSymlinkEscapes) ||
		errors.Is(err, catalog.ErrArchiveDecompressionBomb) || errors.Is(err, catalog.ErrArchiveMissingSkillMD) {
		return ExitIntegrity
	}
	if httpErr, ok := errors.AsType[*catalog.HTTPStatusError](err); ok {
		switch {
		case httpErr.StatusCode == http.StatusNotFound:
			return ExitNotFound
		case httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500:
			return ExitNetwork
		default:
			return ExitGeneral
		}
	}
	if errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, runner.ErrExecutionTimeout) {
		return ExitNetwork
	}
	if netErr, ok := errors.AsType[net.Error](err); ok {
		if _, isErrno := any(netErr).(syscall.Errno); !isErrno {
			return ExitNetwork
		}
	}
	if sysErr, ok := errors.AsType[syscall.Errno](err); ok {
		switch sysErr {
		case syscall.ECONNREFUSED, syscall.ENETUNREACH, syscall.ETIMEDOUT, syscall.EHOSTUNREACH,
			syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ENETDOWN:
			return ExitNetwork
		case syscall.ENOENT:
			return ExitNotFound
		default:
			return ExitGeneral
		}
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
	CacheDir string
	Offline  bool
}

type App struct {
	Store      *catalog.Catalog
	Runner     *runner.Runner
	Searcher   *search.Engine
	Stdin      io.Reader
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
	return catalog.NewSafeHTTPClient(30 * time.Second)
}

func (a *App) getRunner() *runner.Runner {
	if a.Runner != nil {
		return a.Runner
	}
	return runner.New(a.Options.CacheDir, a.getHTTPClient())
}

func (a *App) getStdin() io.Reader {
	if a.Stdin != nil {
		return a.Stdin
	}
	return os.Stdin
}

func (a *App) cacheRootDir() string {
	dir, err := catalog.ResolveCacheDir(a.Options.CacheDir)
	if err != nil {
		return ""
	}
	return dir
}

func (a *App) configuredCatalogs() map[string]string {
	out := map[string]string{
		"default": a.resolveDefaultCatalogURL(),
		"kungfu":  config.DefaultGitHubCatalog,
	}
	if a.Config != nil && a.Config.Catalogs != nil {
		for k, v := range a.Config.Catalogs {
			if strings.TrimSpace(v) == "" {
				continue
			}
			if k == "default" {
				out["default"] = a.resolveDefaultCatalogURL()
			} else {
				out[k] = v
			}
		}
	}
	return out
}

func (a *App) lookupConfiguredCatalog(name string) (string, string, bool) {
	cats := a.configuredCatalogs()
	if src, ok := cats[name]; ok {
		return name, src, true
	}
	for k, v := range cats {
		if strings.EqualFold(k, name) {
			return k, v, true
		}
	}
	return "", "", false
}

// resolveConfiguredCatalog finds a configured catalog by exact case-insensitive name,
// or by unambiguous short repo suffix (e.g. "go-skills" matching "spf13/go-skills").
func (a *App) resolveConfiguredCatalog(name string) (string, string, bool, error) {
	if catName, src, ok := a.lookupConfiguredCatalog(name); ok {
		return catName, src, true, nil
	}
	cats := a.configuredCatalogs()
	wantSuffix := "/" + strings.ToLower(strings.TrimSpace(name))
	if wantSuffix == "/" {
		return "", "", false, nil
	}
	var matches []string
	for k := range cats {
		if strings.HasSuffix(strings.ToLower(k), wantSuffix) {
			matches = append(matches, k)
		}
	}
	if len(matches) == 1 {
		k := matches[0]
		return k, cats[k], true, nil
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return "", "", false, fmt.Errorf("%w: %q matches multiple catalogs (%s); use the full catalog name", catalog.ErrAmbiguousCatalog, name, strings.Join(matches, ", "))
	}
	return "", "", false, nil
}

// parseLoadSpec parses canonical "skill[/subpath][@catalog]" syntax without splitting
// slashes that belong to an owner/repo catalog name (e.g. "go@spf13/go-skills" or
// "go/references/guide.md@spf13/go-skills").
func parseLoadSpec(spec string) (lookupSpec, baseSkill, subpath, catQual string) {
	trimmed := strings.TrimSpace(spec)
	if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "github:") {
		return trimmed, trimmed, "", ""
	}
	if atIdx := strings.LastIndex(trimmed, "@"); atIdx > 0 && atIdx < len(trimmed)-1 {
		left := strings.TrimSpace(trimmed[:atIdx])
		catQual = strings.TrimSpace(trimmed[atIdx+1:])
		baseSkill, subpath, _ = strings.Cut(left, "/")
		return baseSkill + "@" + catQual, baseSkill, subpath, catQual
	}
	baseSkill, subpath, _ = strings.Cut(trimmed, "/")
	return baseSkill, baseSkill, subpath, ""
}

// ensureCatalogFresh ensures a configured named catalog (exact or unique short suffix)
// is loaded and within DefaultCatalogTTL, fetching and caching it on demand if missing or stale.
func (a *App) ensureCatalogFresh(nameOrShort string) error {
	if a.Options.Offline {
		return nil
	}
	trimmed := strings.TrimSpace(nameOrShort)
	if trimmed == "" || strings.EqualFold(trimmed, "default") {
		return nil
	}
	catName, src, ok, resolveErr := a.resolveConfiguredCatalog(trimmed)
	if resolveErr != nil {
		return resolveErr
	}
	if !ok || strings.EqualFold(catName, "default") {
		return nil
	}
	if a.Store.HasFreshCatalog(catName) {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m, etag, _, fetchErr := catalog.FetchSource(ctx, src, a.getHTTPClient())
	if fetchErr != nil {
		return fmt.Errorf("fetching registered catalog %q: %w", catName, fetchErr)
	}
	if m == nil {
		return nil
	}

	if cacheRoot := a.cacheRootDir(); cacheRoot != "" {
		if saveErr := catalog.SaveNamedCatalogCache(cacheRoot, catName, src, *m, etag); saveErr != nil && a.Options.Verbose {
			_, _ = fmt.Fprintf(a.Stderr, "warning: caching catalog %q: %v\n", catName, saveErr)
		}
	}
	a.Store.AddCatalogManifest(catName, m)
	return nil
}

// warnStaleCatalogs warns on stderr if any cached named catalogs have exceeded DefaultCatalogTTL.
func (a *App) warnStaleCatalogs() {
	if a.Options.Offline || a.Store == nil {
		return
	}
	stale := a.Store.StaleCatalogs()
	if len(stale) == 0 {
		return
	}
	_, _ = fmt.Fprintf(a.Stderr, "warning: cached catalog(s) are stale (>24h): %s; run 'kungfu update' to refresh\n", strings.Join(stale, ", "))
}

// warnIfLocalCopyOutdated checks if a locally installed copy of item in workspace or global scope
// is older than the catalog version (or digest) and emits a warning on stderr.
func (a *App) warnIfLocalCopyOutdated(item catalog.Item, usingLocal bool) {
	if item.Source == "local" || item.URL == "" || catalog.ValidateSkillName(item.Name) != nil {
		return
	}
	var candidatePaths []string
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidatePaths = append(candidatePaths, filepath.Join(wd, ".agents", "skills", item.Name, "SKILL.md"))
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		candidatePaths = append(candidatePaths, filepath.Join(homeDir, ".agents", "skills", item.Name, "SKILL.md"))
	}

	var localData []byte
	for _, p := range candidatePaths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		data, readErr := catalog.ReadBounded(f, catalog.DefaultMaxSkillMDBytes)
		_ = f.Close()
		if readErr == nil && len(data) > 0 {
			localData = data
			break
		}
	}
	if len(localData) == 0 {
		return
	}

	localVer := ""
	if fm, _, err := catalog.ParseFrontmatter(localData); err == nil && fm != nil && fm.Metadata != nil {
		localVer = fm.Metadata["version"]
	}
	remoteVer := itemVersion(item)

	isOutdated := false
	if remoteVer != "" && localVer != "" {
		isOutdated = catalog.CompareSemver(remoteVer, localVer) > 0
	} else if !catalog.IsArchiveItem(item) && item.EffectiveDigest() != "" {
		isOutdated = catalog.VerifyDigest(localData, item.EffectiveDigest()) != nil
	}
	if !isOutdated {
		return
	}

	if usingLocal {
		if remoteVer != "" && localVer != "" {
			_, _ = fmt.Fprintf(a.Stderr, "warning: local installed %q (%s) is behind catalog (%s); run 'kungfu update %s' to update\n", item.Name, localVer, remoteVer, item.Name)
		} else {
			_, _ = fmt.Fprintf(a.Stderr, "warning: local installed %q differs from latest catalog version; run 'kungfu update %s' to update\n", item.Name, item.Name)
		}
		return
	}
	if remoteVer != "" && localVer != "" {
		_, _ = fmt.Fprintf(a.Stderr, "note: loaded latest remote %q (%s), while local installed copy is %s; run 'kungfu update %s'\n", item.Name, remoteVer, localVer, item.Name)
	}
}

// resolveSkill looks up a skill in the catalog store. If the targeted catalog is missing
// from cache or stale (> DefaultCatalogTTL), it refreshes that catalog on demand first.
func (a *App) resolveSkill(spec string) (*catalog.Item, error) {
	trimmed := strings.TrimSpace(spec)
	skillName := trimmed
	catQual := ""
	if idx := strings.LastIndex(trimmed, "@"); idx > 0 {
		catQual = strings.TrimSpace(trimmed[idx+1:])
		skillName = strings.TrimSpace(trimmed[:idx])
	}

	if catQual != "" {
		if err := a.ensureCatalogFresh(catQual); err != nil {
			return nil, err
		}
	}

	item, err := a.Store.Get(trimmed)
	if err == nil {
		if !a.Options.Offline && item.Source != "" && !strings.EqualFold(item.Source, "default") && item.Source != "local" && a.Store.IsCatalogCacheStale(item.Source) {
			if refreshErr := a.ensureCatalogFresh(item.Source); refreshErr != nil {
				return nil, refreshErr
			}
			return a.Store.Get(trimmed)
		}
		return item, nil
	}
	if !errors.Is(err, catalog.ErrItemNotFound) || a.Options.Offline || catQual != "" {
		return nil, err
	}

	// Unqualified lookup fallback: if skillName itself matches a configured catalog (e.g. "kungfu"),
	// ensure it is fetched and cached before retrying lookup.
	if skillName == "" || strings.EqualFold(skillName, "default") {
		return nil, err
	}
	if _, _, ok, rErr := a.resolveConfiguredCatalog(skillName); rErr != nil {
		return nil, rErr
	} else if !ok {
		return nil, err
	}
	if refreshErr := a.ensureCatalogFresh(skillName); refreshErr != nil {
		return nil, refreshErr
	}
	return a.Store.Get(trimmed)
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
	if len(cfg.UnknownKeys) > 0 {
		cfgPath, _ := config.ResolveConfigPath(opts.Config)
		_, _ = fmt.Fprintf(stderr, "warning: ignoring unknown config key(s) in %s: %s\n", cfgPath, strings.Join(cfg.UnknownKeys, ", "))
	}

	effectiveDefaultURL := config.DefaultCatalogURL
	if envURL := strings.TrimSpace(os.Getenv("KUNGFU_URL")); envURL != "" {
		effectiveDefaultURL = catalog.ResolveCatalogURL(envURL)
	} else if defURL := cfg.GetCatalogURL("default"); defURL != "" && defURL != config.DefaultCatalogURL {
		effectiveDefaultURL = catalog.ResolveCatalogURL(defURL)
	}
	allowLoopback := catalog.IsLoopbackURL(effectiveDefaultURL)
	if !allowLoopback && cfg.Catalogs != nil {
		for _, v := range cfg.Catalogs {
			if catalog.IsLoopbackURL(v) {
				allowLoopback = true
				break
			}
		}
	}

	httpClient := catalog.NewSafeHTTPClientWithLoopback(30*time.Second, allowLoopback)
	catOpts := []catalog.Option{
		catalog.WithLocalDiscovery(true),
		catalog.WithHTTPClient(httpClient),
	}
	if defURL := cfg.GetCatalogURL("default"); defURL != "" && defURL != config.DefaultCatalogURL && strings.TrimSpace(os.Getenv("KUNGFU_URL")) == "" {
		catOpts = append(catOpts, catalog.WithRemoteURL(defURL))
	}
	if opts.CacheDir != "" {
		catOpts = append(catOpts, catalog.WithCacheDir(opts.CacheDir))
	}
	if opts.Offline {
		catOpts = append(catOpts, catalog.WithOffline(true))
	}

	cat, err := catalog.NewCatalog(catOpts...)
	if err != nil {
		return nil, fmt.Errorf("initializing catalog: %w", err)
	}

	return &App{
		Store:      cat,
		Runner:     runner.New(opts.CacheDir, httpClient),
		Searcher:   search.NewEngine(),
		Stdin:      os.Stdin,
		Stdout:     stdout,
		Stderr:     stderr,
		Options:    opts,
		HTTPClient: httpClient,
		Config:     cfg,
	}, nil
}

// loadStateReadOnly loads state.json without running disk reconciliation or writing locks.
func (a *App) loadStateReadOnly() *state.State {
	s, err := state.LoadState("")
	if err != nil {
		if !os.IsNotExist(err) {
			path, _ := state.DefaultStateFilePath()
			_, _ = fmt.Fprintf(a.Stderr, "warning: state file %s is corrupted or unreadable (%v); using empty in-memory state\n", path, err)
		}
		return state.NewState()
	}
	return s
}

// loadStateReconciled loads state.json and reconciles workspace/global .agents/skills directories,
// persisting any discovered disk changes under an exclusive cross-process lock.
func (a *App) loadStateReconciled() *state.State {
	s := a.loadStateReadOnly()

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
		var reconciled *state.State
		if err := state.WithLockedState("", func(disk *state.State) error {
			disk.ReconcileDisk(wd, homeDir, isCatalogKnown)
			reconciled = disk
			return nil
		}); err != nil && a.Options.Verbose {
			_, _ = fmt.Fprintf(a.Stderr, "warning: persisting reconciled state: %v\n", err)
		}
		if reconciled != nil {
			s = reconciled
		}
	}

	return s
}

func normalizeItemCatalog(item catalog.Item) string {
	src := strings.TrimSpace(item.Source)
	if src == "" || src == "remote" || src == "default" {
		if item.URL != "" {
			if u, err := url.Parse(item.URL); err == nil && u.Scheme != "" && u.Host != "" {
				return u.Scheme + "://" + u.Host
			}
		}
		return src
	}
	src = strings.TrimSuffix(src, "/.well-known/agent-skills/index.json")
	src = strings.TrimSuffix(src, "/index.json")
	return src
}

// recordSkillLoad records local load usage counts only when telemetry is opted in.
func (a *App) recordSkillLoad(item catalog.Item, subpath string, page int) {
	if a.Config == nil || !a.Config.IsTelemetryEnabled() {
		return
	}
	cat := normalizeItemCatalog(item)
	if err := state.WithLockedState("", func(s *state.State) error {
		s.RecordLoad(item.Name, cat, subpath, page)
		return nil
	}); err != nil {
		path, _ := state.DefaultStateFilePath()
		_, _ = fmt.Fprintf(a.Stderr, "warning: failed to record load history in %s: %v\n", path, err)
	}
}

// recordSkillRun records local run usage counts only when telemetry is opted in.
func (a *App) recordSkillRun(item catalog.Item) {
	if a.Config == nil || !a.Config.IsTelemetryEnabled() {
		return
	}
	cat := normalizeItemCatalog(item)
	if err := state.WithLockedState("", func(s *state.State) error {
		s.RecordRun(item.Name, cat)
		return nil
	}); err != nil {
		path, _ := state.DefaultStateFilePath()
		_, _ = fmt.Fprintf(a.Stderr, "warning: failed to record run history in %s: %v\n", path, err)
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
			if strings.HasPrefix(arg, "---") {
				err := fmt.Errorf("bad flag syntax: %s", arg)
				if fs.Output() != nil {
					_, _ = fmt.Fprintln(fs.Output(), err)
				}
				return nil, err
			}
			flags = append(flags, arg)
			flagName := strings.TrimPrefix(arg, "--")
			if flagName == arg {
				flagName = strings.TrimPrefix(arg, "-")
			}
			if strings.Contains(flagName, "=") {
				continue
			}
			f := fs.Lookup(flagName)
			if f != nil {
				if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
					continue
				}
				if i+1 < len(args) && args[i+1] != "--" {
					i++
					flags = append(flags, args[i])
				} else {
					err := fmt.Errorf("flag needs an argument: %s", arg)
					if fs.Output() != nil {
						_, _ = fmt.Fprintln(fs.Output(), err)
					}
					return nil, err
				}
			}
		} else {
			posArgs = append(posArgs, arg)
		}
	}

	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return append(fs.Args(), posArgs...), nil
}

func (a *App) resolveMode(jsonFlag bool) OutputMode {
	if jsonFlag || a.Options.Mode == OutputModeJSON {
		return OutputModeJSON
	}
	return OutputModeHuman
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func itemVersion(it catalog.Item) string {
	if it.Version != "" {
		return it.Version
	}
	if it.Metadata != nil {
		return it.Metadata["version"]
	}
	return ""
}

func itemAuthor(it catalog.Item) string {
	if it.Author != "" {
		return it.Author
	}
	if it.Metadata != nil {
		return it.Metadata["author"]
	}
	return ""
}

func singleLineDesc(desc string) string {
	return strings.Join(strings.Fields(desc), " ")
}

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) > maxRunes && maxRunes > 3 {
		return string(runes[:maxRunes-3]) + "..."
	}
	return s
}

func resolveScopeAndDir(globalFlag bool) (string, string, error) {
	if globalFlag {
		return state.ScopeGlobal, "", nil
	}
	wsDir, err := os.Getwd()
	if err != nil || wsDir == "" {
		return "", "", fmt.Errorf("unable to determine working directory: %w", err)
	}
	return state.ScopeWorkspace, wsDir, nil
}

func (a *App) resolveDefaultCatalogURL() string {
	catURL := config.DefaultCatalogURL
	if envURL := strings.TrimSpace(os.Getenv("KUNGFU_URL")); envURL != "" {
		catURL = envURL
	} else if a.Config != nil && a.Config.GetCatalogURL("default") != "" {
		catURL = a.Config.GetCatalogURL("default")
	}
	return catalog.ResolveCatalogURL(catURL)
}

func (a *App) printNotFoundWithSuggestions(cmd, query string) {
	if cmd == "find" {
		_, _ = fmt.Fprintf(a.Stderr, "error: no skills matching %q.\n", query)
	} else {
		_, _ = fmt.Fprintf(a.Stderr, "error: skill %q not found.\n", query)
	}

	const maxSuggestions = 5
	searchQuery := query
	if idx := strings.LastIndex(searchQuery, "@"); idx > 0 {
		searchQuery = searchQuery[:idx]
	}

	allItems := a.Store.All()
	itemByName := make(map[string]catalog.Item, len(allItems))
	skillNames := make([]string, 0, len(allItems))
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

	for _, s := range search.Suggest(searchQuery, skillNames, maxSuggestions) {
		if it, ok := itemByName[strings.ToLower(strings.TrimSpace(s))]; ok {
			addCandidate(it)
		} else {
			addCandidate(catalog.Item{Name: s})
		}
	}

	if cmd != "find" && len(matchedItems) < maxSuggestions && a.Searcher != nil && len(allItems) > 0 {
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
		desc := truncateRunes(singleLineDesc(it.Description), 69)
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

type skillStatusLookup struct {
	wd        string
	homeDir   string
	cacheRoot string
	st        *state.State
}

func (a *App) newSkillStatusLookup(st *state.State) skillStatusLookup {
	wd, _ := os.Getwd()
	homeDir, _ := os.UserHomeDir()
	if st == nil {
		st = a.loadStateReadOnly()
	}
	return skillStatusLookup{
		wd:        wd,
		homeDir:   homeDir,
		cacheRoot: a.cacheRootDir(),
		st:        st,
	}
}

func (l skillStatusLookup) status(item *catalog.Item) string {
	if item == nil {
		return "catalog"
	}

	if l.wd != "" && l.st.Workspaces != nil {
		if ws, ok := l.st.Workspaces[l.wd]; ok && ws != nil && ws.Skills != nil {
			if rec, found := ws.Skills[item.Name]; found && (rec.Status == "" || rec.Status == state.StatusInstalled) {
				return state.ScopeWorkspace
			}
		}
	}

	if l.st.Global != nil && l.st.Global.Skills != nil {
		if rec, found := l.st.Global.Skills[item.Name]; found && (rec.Status == "" || rec.Status == state.StatusInstalled) {
			return state.ScopeGlobal
		}
	}

	if catalog.ValidateSkillName(item.Name) != nil {
		return "catalog"
	}

	if l.wd != "" {
		if _, err := os.Stat(filepath.Join(l.wd, ".agents", "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeWorkspace
		}
		if _, err := os.Stat(filepath.Join(l.wd, "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeWorkspace
		}
	}
	if l.homeDir != "" {
		if _, err := os.Stat(filepath.Join(l.homeDir, ".agents", "skills", item.Name, "SKILL.md")); err == nil {
			return state.ScopeGlobal
		}
	}
	if l.cacheRoot != "" {
		if skillDir, err := catalog.SkillCacheDir(l.cacheRoot, item.Name, item.Source); err == nil {
			if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
				return "cached"
			}
		}
	}

	return "catalog"
}

func isGitHubSkillSpec(spec string) bool {
	if strings.HasPrefix(spec, "github:") {
		return true
	}
	if u, err := url.Parse(spec); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		h := strings.ToLower(u.Hostname())
		return h == "github.com" || h == "raw.githubusercontent.com"
	}
	if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "~") {
		return false
	}
	slashIdx := strings.IndexByte(spec, '/')
	if slashIdx < 1 {
		return false
	}
	// skill@owner/repo has '@' before '/' and is a catalog qualifier, not a GitHub owner/repo@ref.
	if atIdx := strings.IndexByte(spec, '@'); atIdx > 0 && atIdx < slashIdx {
		return false
	}
	return true
}

func isBundledSubpath(subpath string) bool {
	if subpath == "" {
		return false
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(subpath, "/"), "/")
	switch strings.ToLower(first) {
	case "references", "reference", "scripts", "script", "assets", "asset", "examples", "example", "resources", "resource":
		return true
	default:
		return false
	}
}

func shouldProbeGitHubOnLoad(spec, subpath string) bool {
	if !isGitHubSkillSpec(spec) {
		return false
	}
	if strings.HasPrefix(spec, "github:") {
		return true
	}
	if u, err := url.Parse(spec); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		h := strings.ToLower(u.Hostname())
		if h == "github.com" || h == "raw.githubusercontent.com" {
			return true
		}
	}
	return !isBundledSubpath(subpath)
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
	var jsonFlag bool

	fs := flag.NewFlagSet("kungfu", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.StringVar(&opts.Config, "config", "", "Path to custom config file")
	fs.BoolVar(&opts.Verbose, "verbose", false, "Enable verbose logging")
	fs.BoolVar(&opts.Verbose, "v", false, "Enable verbose logging (shorthand)")
	fs.StringVar(&opts.CacheDir, "cache-dir", "", "Custom cache directory")
	fs.BoolVar(&showVersion, "version", false, "Print version information")
	fs.BoolVar(&showVersion, "V", false, "Print version information (shorthand)")
	fs.BoolVar(&showHelp, "help", false, "Show help information")
	fs.BoolVar(&showHelp, "h", false, "Show help information (shorthand)")

	fs.Usage = func() {
		PrintGlobalHelp(stderr)
	}

	var globalArgs []string
	var cmdName string
	var cmdArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				cmdName = args[i+1]
				cmdArgs = args[i+2:]
			}
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			globalArgs = append(globalArgs, arg)
			if arg == "--config" || arg == "-config" || arg == "--cache-dir" || arg == "-cache-dir" {
				if i+1 < len(args) && args[i+1] != "--" && (!isSubcommandName(args[i+1]) || i+2 < len(args)) {
					i++
					globalArgs = append(globalArgs, args[i])
				} else {
					_, _ = fmt.Fprintf(stderr, "flag needs an argument: %s\n", arg)
					return ExitUsage
				}
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

	if cmdName == "help" {
		if len(cmdArgs) > 0 {
			if PrintSubcommandHelp(stdout, cmdArgs[0], cmdArgs[1:]) {
				return ExitSuccess
			}
			_, _ = fmt.Fprintf(stderr, "error: unknown help topic %q.\n", cmdArgs[0])
			PrintGlobalHelp(stderr)
			return ExitUsage
		}
		PrintGlobalHelp(stdout)
		return ExitSuccess
	}

	if showHelp {
		PrintGlobalHelp(stdout)
		return ExitSuccess
	}

	if cmdName == "" {
		PrintGlobalHelp(stderr)
		return ExitUsage
	}

	if hasHelpFlag(cmdName, cmdArgs) {
		if PrintSubcommandHelp(stdout, cmdName, cmdArgs) {
			return ExitSuccess
		}
	}

	if jsonFlag {
		opts.Mode = OutputModeJSON
	}

	if !isCanonicalAction(cmdName) {
		_, _ = fmt.Fprintf(stderr, "error: unknown command %q.\n", cmdName)
		suggs := search.Suggest(cmdName, canonicalCommands, 2)
		if len(suggs) > 0 {
			_, _ = fmt.Fprintln(stderr, "\nDid you mean:")
			for _, s := range suggs {
				_, _ = fmt.Fprintf(stderr, "  - kungfu %s\n", s)
			}
		}
		_, _ = fmt.Fprintln(stderr, "\nRun 'kungfu --help' for available commands and usage.")
		return ExitUsage
	}

	if cmdName == "load" && hasLocalFlag(cmdArgs) {
		opts.Offline = true
	}

	app, err := NewApp(opts, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return MapErrorToExitCode(err)
	}

	switch cmdName {
	case "list":
		return app.RunList(cmdArgs)
	case "find":
		return app.RunFind(cmdArgs)
	case "show":
		return app.RunShow(cmdArgs)
	case "load":
		return app.RunLoad(cmdArgs)
	case "learn":
		return app.RunLearn(cmdArgs)
	case "forget":
		return app.RunForget(cmdArgs)
	case "update":
		return app.RunUpdate(cmdArgs)
	case "status":
		return app.RunStatus(cmdArgs)
	case "catalog":
		return app.RunCatalog(cmdArgs)
	case "run":
		return app.RunRun(cmdArgs)
	default:
		return ExitUsage
	}
}

func isCanonicalAction(name string) bool {
	switch name {
	case "list", "find", "show", "load", "learn", "forget", "update", "status", "catalog", "run":
		return true
	default:
		return false
	}
}

func isSubcommandName(name string) bool {
	return isCanonicalAction(name) || name == "version" || name == "help"
}

func hasHelpFlag(cmdName string, args []string) bool {
	if cmdName == "run" {
		return len(args) == 1 && (args[0] == "--help" || args[0] == "-h")
	}
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func hasLocalFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--local" || arg == "-local" {
			return true
		}
		if val, ok := strings.CutPrefix(arg, "--local="); ok {
			b, err := strconv.ParseBool(val)
			return err == nil && b
		}
		if val, ok := strings.CutPrefix(arg, "-local="); ok {
			b, err := strconv.ParseBool(val)
			return err == nil && b
		}
	}
	return false
}

// PrintSubcommandHelp writes subcommand-specific usage to w and returns true if handled.
func PrintSubcommandHelp(w io.Writer, cmdName string, args []string) bool {
	switch cmdName {
	case "find":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu find <keywords...> [flags]

Search across skill names, descriptions, and tags.

FLAGS:
  -n, --limit <int>   Maximum number of search results to return (default 10)
      --json          Output in JSON format`)
		return true
	case "load":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu load <skill[/subpath]> [flags]

Stream skill instructions, references, or assets to stdout.

FLAGS:
  -p, --page <int>        Page number to display, 1-based (default 1)
      --page-size <int>   Maximum page size in bytes (default 6000, max 7000)
      --full              Output complete unpaginated content
      --local             Use locally cached or installed copy without checking remote upstream
  -u, --update            Check remote upstream and update cached copy before loading
  -f, --force             Force loading/updating even if digest verification fails
      --json              Output loaded content as JSON
      --raw               Output raw verbatim markdown including frontmatter (unpaginated unless -p or --page-size is set)`)
		return true
	case "run":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu run <skill> <script> [args...]

Execute bundled skill scripts or binaries (supports Python/uv, Node/Bun/Deno, Shell, and binaries).`)
		return true
	case "list":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu list [flags]

List all available skills across configured catalogs and local scopes.

FLAGS:
  --json   Output in JSON format`)
		return true
	case "show":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu show <skill...> [flags]

Show metadata, references, and bundled scripts for one or more skills.

FLAGS:
  --json   Output in JSON format`)
		return true
	case "learn":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu learn <skill...> [flags]

Install skills into workspace (.agents/skills/) or global (~/.agents/skills/) scope.

FLAGS:
  -g, --global   Install skill to global store (~/.agents/skills/)
  -f, --force    Overwrite existing skill installation
      --json     Output in JSON format`)
		return true
	case "status":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu status [skill...] [flags]

Show installed skills, versions, and opt-in local usage history.

FLAGS:
  -g, --global   Show global skills status only
      --json     Output in JSON format`)
		return true
	case "update":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu update [skill...] [flags]

Synchronize catalogs and check for or apply updates to installed skills.

FLAGS:
  -a, --all      Apply updates to all installed skills
  -g, --global   Update global skills (~/.agents/skills/)
  -f, --force    Force update even for unversioned, drifted, or locally modified skills
      --json     Output in JSON format`)
		return true
	case "forget":
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu forget <skill...> [flags]

Uninstall skills from workspace or global scope.

FLAGS:
  -a, --all      Forget all installed skills in scope
  -g, --global   Forget skill from global store (~/.agents/skills/)
  -f, --force    Also remove custom/external skills not installed by kungfu
      --json     Output in JSON format`)
		return true
	case "catalog":
		if len(args) > 0 {
			switch args[0] {
			case "add":
				_, _ = fmt.Fprintln(w, `USAGE:
  kungfu catalog add <source> [flags]

Register a skill catalog from a GitHub repository, HTTPS manifest, or local directory.

FLAGS:
  -n, --name <name>      Custom alias/name for the catalog
      --ref <ref>        Branch or tag ref for git repository
  -p, --subpath <path>   Subpath directory inside repository
  -f, --force            Overwrite an existing catalog with the same name
      --json             Output in JSON format`)
				return true
			case "list":
				_, _ = fmt.Fprintln(w, `USAGE:
  kungfu catalog list [flags]

List all registered skill catalogs and default sources.

FLAGS:
  --json   Output in JSON format`)
				return true
			case "remove":
				_, _ = fmt.Fprintln(w, `USAGE:
  kungfu catalog remove <name...> [flags]

Remove one or more registered catalog sources.

FLAGS:
  --json   Output in JSON format`)
				return true
			}
		}
		_, _ = fmt.Fprintln(w, `USAGE:
  kungfu catalog <subcommand> [flags] [arguments...]

SUBCOMMANDS:
  add <source> [--name <name>] [--ref <ref>] [--subpath <path>] [--force] [--json]
      Register a skill catalog from a GitHub repository, HTTPS manifest, or local directory.
  list [--json]
      List all registered skill catalogs and default sources.
  remove <name...> [--json]
      Remove one or more registered catalog sources.`)
		return true
	default:
		return false
	}
}

func PrintGlobalHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, `kungfu - Agent Skills Gateway

USAGE:
  kungfu [global flags] <command> [flags] [arguments...]

COMMANDS:
  find      <keywords...> [-n <limit>] [--json]
            Search skills by keyword.
  load      <skill[/subpath]> [-p <page>] [--full] [--local] [-u] [-f] [--raw]
            Stream skill instructions or reference files to stdout.
  run       <skill> <script> [args...]
            Run a companion script or binary bundled with a skill.
  list      [--json]
            List available skills.
  show      <skill...> [--json]
            Show skill metadata, references, and scripts.
  learn     <skill...> [-g] [-f] [--json]
            Install skills to workspace or global scope.
  status    [skill...] [-g] [--json]
            Show installed skills and opt-in local usage history.
  update    [skill...] [-g] [--all] [-f] [--json]
            Synchronize catalogs and check for or apply skill updates.
  forget    <skill...> [-g] [-f] [--all] [--json]
            Uninstall skills from workspace or global scope.
  catalog   <add|list|remove> [source] [flags]
            Manage external skill catalogs.
  version
            Print kungfu version.
  help      [command]
            Show help for kungfu or a command.

GLOBAL FLAGS:
  --json            Emit JSON output
  --config <path>   Custom configuration file path (default ~/.config/kungfu/config.yaml)
  --cache-dir <dir> Custom cache directory (default ~/.cache/kungfu)
  -v, --verbose     Verbose diagnostics on stderr
  -V, --version     Print version
  -h, --help        Show help`)
}

func (a *App) ensureArchiveUnpacked(item *catalog.Item) {
	if item == nil || !catalog.IsArchiveItem(*item) {
		return
	}
	cacheRoot := a.cacheRootDir()
	if cacheRoot != "" && catalog.IsArchiveCachedWithDigest(cacheRoot, item.Name, item.Source, item.EffectiveDigest()) {
		if skillDir, err := catalog.SkillCacheDir(cacheRoot, item.Name, item.Source); err == nil {
			if f, err := os.Open(filepath.Join(skillDir, "SKILL.md")); err == nil {
				if data, readErr := catalog.ReadBounded(f, catalog.DefaultMaxSkillMDBytes); readErr == nil {
					catalog.EnrichItemFromSkillMD(item, data)
				}
				_ = f.Close()
			}
		}
		return
	}
	if a.Store != nil {
		_, _ = a.Store.LoadContent(item)
	}
}

func walkRelativeFiles(rootDir string) []string {
	var files []string
	_ = filepath.WalkDir(rootDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != rootDir && (strings.HasPrefix(name, ".") || name == "__pycache__" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		rel, err := filepath.Rel(rootDir, p)
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

func (a *App) collectReferences(item catalog.Item, stripMarkdownExt bool) []string {
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
		if stripMarkdownExt {
			clean = strings.TrimSuffix(clean, ".md")
		}
		if clean != "" && !seen[clean] {
			seen[clean] = true
			refs = append(refs, clean)
		}
	}

	a.ensureArchiveUnpacked(&item)

	baseDirs := a.getRunner().GetCandidateBaseDirs(item)
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

	baseDirs := a.getRunner().GetCandidateBaseDirs(item)
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

func stripFrontmatter(content string) string {
	_, body, hasFM, err := catalog.ExtractFrontmatter([]byte(content))
	if err == nil && hasFM {
		return strings.TrimLeft(string(body), "\r\n")
	}
	return content
}
