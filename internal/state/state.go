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

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
)

const (
	SchemaVersion     = "1.0"
	StatusInstalled   = "installed"
	StatusUninstalled = "uninstalled"

	ScopeWorkspace = "workspace"
	ScopeGlobal    = "global"

	OriginCatalog = "catalog"
	OriginCustom  = "custom"
	OriginGitHub  = "github"

	CatalogTypeDefault = "default"
	CatalogTypeGitHub  = "github"
	CatalogTypeHTTP    = "http"
	CatalogTypeLocal   = "local"
)

// CatalogRecord represents a configured external or custom catalog source.
type CatalogRecord struct {
	Name       string    `json:"name"`              // Unique catalog identifier (e.g. "google/skills", "internal")
	Type       string    `json:"type"`              // "github", "http", "local", "default"
	Source     string    `json:"source"`            // "google/skills", "https://...", "/path/to/dir"
	URL        string    `json:"url,omitempty"`     // Canonical URL or repo URL
	Ref        string    `json:"ref,omitempty"`     // Branch/tag ref for github (e.g. "main")
	Subpath    string    `json:"subpath,omitempty"` // Subpath inside repository or directory
	SkillCount int       `json:"skill_count"`       // Number of indexed skills
	AddedAt    time.Time `json:"added_at"`          // When catalog was registered
	UpdatedAt  time.Time `json:"updated_at"`        // When catalog index was last updated
}

// SkillRecord represents an installed or soft-deleted skill package.
type SkillRecord struct {
	Name            string     `json:"name"`
	Status          string     `json:"status"`            // "installed" or "uninstalled"
	Scope           string     `json:"scope"`             // "workspace" or "global"
	Origin          string     `json:"origin,omitempty"`  // "catalog", "custom", or "github"
	Repo            string     `json:"repo,omitempty"`    // e.g. "https://github.com/owner/repo" or "owner/repo"
	Ref             string     `json:"ref,omitempty"`     // e.g. "main", "v1.0.0"
	RawURL          string     `json:"raw_url,omitempty"` // e.g. "https://raw.githubusercontent.com/owner/repo/main/path/SKILL.md"
	Catalog         string     `json:"catalog,omitempty"` // Catalog name (e.g. "kungfu", "default")
	Path            string     `json:"path"`
	Version         string     `json:"version,omitempty"`
	Digest          string     `json:"digest,omitempty"`           // Current/reconciled digest
	InstalledDigest string     `json:"installed_digest,omitempty"` // Immutable baseline digest at install time
	InstalledAt     time.Time  `json:"installed_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	UninstalledAt   *time.Time `json:"uninstalled_at,omitempty"`
	Files           []string   `json:"files,omitempty"`
}

// WorkspaceState holds skills installed in a specific workspace directory.
type WorkspaceState struct {
	Skills map[string]SkillRecord `json:"skills"`
}

// HistoryRecord tracks JIT load, learn, and script execution activity for a skill,
// along with synced watermarks for opt-in community telemetry.
type HistoryRecord struct {
	LastSeenAt       time.Time `json:"last_seen_at"`
	LoadCount        int       `json:"load_count"`
	PageLoadCount    int       `json:"page_load_count,omitempty"`
	SubpathLoadCount int       `json:"subpath_load_count,omitempty"`
	RunCount         int       `json:"run_count,omitempty"`
	LearnCount       int       `json:"learn_count,omitempty"`

	SyncedLoadCount        int        `json:"synced_load_count,omitempty"`
	SyncedPageLoadCount    int        `json:"synced_page_load_count,omitempty"`
	SyncedSubpathLoadCount int        `json:"synced_subpath_load_count,omitempty"`
	SyncedRunCount         int        `json:"synced_run_count,omitempty"`
	SyncedLearnCount       int        `json:"synced_learn_count,omitempty"`
	LastSyncedAt           *time.Time `json:"last_synced_at,omitempty"`
}

// TelemetryDelta represents unsent positive counter increments for an official catalog skill.
type TelemetryDelta struct {
	Skill        string `json:"skill"`
	Category     string `json:"category"`
	Loads        int    `json:"loads"`
	PageLoads    int    `json:"page_loads"`
	SubpathLoads int    `json:"subpath_loads"`
	Runs         int    `json:"runs"`
	Learns       int    `json:"learns"`
}

// State represents the complete root manifest in ~/.config/kungfu/state.json.
type State struct {
	SchemaVersion string                     `json:"schema_version"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	Workspaces    map[string]*WorkspaceState `json:"workspaces,omitempty"`
	Global        *WorkspaceState            `json:"global,omitempty"`
	History       map[string]*HistoryRecord  `json:"history,omitempty"`
	Catalogs      map[string]CatalogRecord   `json:"catalogs,omitempty"`

	deletedCatalogs map[string]bool `json:"-"`
	mu              sync.RWMutex    `json:"-"`
}

const DefaultRemoteCatalogURL = "https://skills.danicat.dev/.well-known/agent-skills/index.json"
const DefaultGitHubCatalog = "danicat/kungfu"

// NewState creates an initialized State struct.
func NewState() *State {
	now := time.Now().UTC()
	s := &State{
		SchemaVersion: SchemaVersion,
		UpdatedAt:     now,
		Workspaces:    make(map[string]*WorkspaceState),
		Global: &WorkspaceState{
			Skills: make(map[string]SkillRecord),
		},
		History:  make(map[string]*HistoryRecord),
		Catalogs: make(map[string]CatalogRecord),
	}
	s.Catalogs[CatalogTypeDefault] = CatalogRecord{
		Name:      CatalogTypeDefault,
		Type:      CatalogTypeDefault,
		Source:    DefaultRemoteCatalogURL,
		URL:       DefaultRemoteCatalogURL,
		AddedAt:   now,
		UpdatedAt: now,
	}
	s.Catalogs["kungfu"] = CatalogRecord{
		Name:      "kungfu",
		Type:      CatalogTypeGitHub,
		Source:    DefaultGitHubCatalog,
		URL:       "https://github.com/" + DefaultGitHubCatalog,
		Ref:       "main",
		AddedAt:   now,
		UpdatedAt: now,
	}
	return s
}

// DefaultStateFilePath returns ~/.config/kungfu/state.json, or respects KUNGFU_STATE_PATH / KUNGFU_CONFIG_DIR.
func DefaultStateFilePath() string {
	if statePath := os.Getenv("KUNGFU_STATE_PATH"); statePath != "" {
		return statePath
	}
	if configDir := os.Getenv("KUNGFU_CONFIG_DIR"); configDir != "" {
		return filepath.Join(configDir, "state.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		panic(fmt.Sprintf("kungfu: unable to determine user home directory: %v", err))
	}
	return filepath.Join(home, ".config", "kungfu", "state.json")
}

// LoadState loads the State from the specified file path, or returns a new State if it does not exist.
func LoadState(filePath string) (*State, error) {
	if filePath == "" {
		filePath = DefaultStateFilePath()
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), nil
		}
		return nil, fmt.Errorf("state: reading %s: %w", filePath, err)
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("state: parsing %s: %w", filePath, err)
	}

	if s.Workspaces == nil {
		s.Workspaces = make(map[string]*WorkspaceState)
	}
	if s.Global == nil {
		s.Global = &WorkspaceState{Skills: make(map[string]SkillRecord)}
	} else if s.Global.Skills == nil {
		s.Global.Skills = make(map[string]SkillRecord)
	}
	if s.History == nil {
		s.History = make(map[string]*HistoryRecord)
	}
	if s.Catalogs == nil {
		s.Catalogs = make(map[string]CatalogRecord)
	}
	if _, ok := s.Catalogs[CatalogTypeDefault]; !ok {
		now := time.Now().UTC()
		s.Catalogs[CatalogTypeDefault] = CatalogRecord{
			Name:      CatalogTypeDefault,
			Type:      CatalogTypeDefault,
			Source:    DefaultRemoteCatalogURL,
			URL:       DefaultRemoteCatalogURL,
			AddedAt:   now,
			UpdatedAt: now,
		}
	}
	if _, ok := s.Catalogs["kungfu"]; !ok {
		now := time.Now().UTC()
		s.Catalogs["kungfu"] = CatalogRecord{
			Name:      "kungfu",
			Type:      CatalogTypeGitHub,
			Source:    DefaultGitHubCatalog,
			URL:       "https://github.com/" + DefaultGitHubCatalog,
			Ref:       "main",
			AddedAt:   now,
			UpdatedAt: now,
		}
	}

	return &s, nil
}

// SaveState saves the State atomically to the specified file path.
func SaveState(filePath string, s *State) error {
	if s == nil {
		return fmt.Errorf("state: cannot save nil state")
	}
	if filePath == "" {
		filePath = DefaultStateFilePath()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.UpdatedAt = time.Now().UTC()
	if s.SchemaVersion == "" {
		s.SchemaVersion = SchemaVersion
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("state: creating directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encoding JSON: %w", err)
	}
	data = append(data, '\n')

	tmpFile, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("state: creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("state: writing temp file %s: %w", tmpPath, err)
	}
	if err := tmpFile.Chmod(0644); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("state: setting permissions on %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("state: closing temp file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("state: renaming %s -> %s: %w", tmpPath, filePath, err)
	}

	return nil
}

// WithLockedState acquires an exclusive cross-process file lock on filePath+".lock",
// loads the current State from disk, executes fn(s), and saves the updated State atomically.
func WithLockedState(filePath string, fn func(s *State) error) error {
	if filePath == "" {
		filePath = DefaultStateFilePath()
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("state: creating lock directory %s: %w", dir, err)
	}

	lockPath := filePath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("state: opening lock file %s: %w", lockPath, err)
	}
	defer func() {
		_ = lockFile.Close()
	}()

	if err := lockFileExclusive(lockFile); err != nil {
		return fmt.Errorf("state: acquiring exclusive lock on %s: %w", lockPath, err)
	}
	defer func() {
		_ = unlockFile(lockFile)
	}()

	s, err := LoadState(filePath)
	if err != nil {
		return err
	}

	if err := fn(s); err != nil {
		return err
	}

	return SaveState(filePath, s)
}

func (s *State) scopeSkillsMap(scope, workspaceDir string, create bool) map[string]SkillRecord {
	if scope == ScopeGlobal {
		if s.Global == nil || s.Global.Skills == nil {
			if !create {
				return nil
			}
			s.Global = &WorkspaceState{Skills: make(map[string]SkillRecord)}
		}
		return s.Global.Skills
	}

	cleanWs := filepath.Clean(workspaceDir)
	ws, ok := s.Workspaces[cleanWs]
	if !ok || ws == nil || ws.Skills == nil {
		if !create {
			return nil
		}
		if s.Workspaces == nil {
			s.Workspaces = make(map[string]*WorkspaceState)
		}
		ws = &WorkspaceState{Skills: make(map[string]SkillRecord)}
		s.Workspaces[cleanWs] = ws
	}
	return ws.Skills
}

// RecordInstall records or updates a skill installation in the state.
func (s *State) RecordInstall(scope string, workspaceDir string, record SkillRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record.Status = StatusInstalled
	record.UninstalledAt = nil
	if record.Digest != "" {
		record.InstalledDigest = record.Digest
	}
	if record.InstalledAt.IsZero() {
		record.InstalledAt = time.Now().UTC()
	}
	record.UpdatedAt = time.Now().UTC()
	if scope == ScopeGlobal {
		record.Scope = ScopeGlobal
	} else {
		record.Scope = ScopeWorkspace
	}

	skills := s.scopeSkillsMap(scope, workspaceDir, true)
	skills[record.Name] = record
}

// RecordForget soft-deletes a skill by marking it uninstalled.
func (s *State) RecordForget(scope string, workspaceDir string, skillName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	skills := s.scopeSkillsMap(scope, workspaceDir, false)
	rec, ok := skills[skillName]
	if !ok {
		return false
	}

	now := time.Now().UTC()
	rec.Status = StatusUninstalled
	rec.UninstalledAt = &now
	rec.UpdatedAt = now
	skills[skillName] = rec
	return true
}

// RecordLoad updates the last seen timestamp and increments primary load count for a skill.
func (s *State) RecordLoad(skillName string) {
	s.RecordLoadDetailed(skillName, "", 1)
}

// RecordLoadDetailed updates the last seen timestamp and increments the appropriate counter
// (LoadCount for primary page-1 loads, PageLoadCount for continuation pages > 1, or SubpathLoadCount for subpaths).
func (s *State) RecordLoadDetailed(skillName, subpath string, page int) {
	if skillName == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	rec, ok := s.History[skillName]
	if !ok || rec == nil {
		rec = &HistoryRecord{
			LastSeenAt: now,
		}
		s.History[skillName] = rec
	} else {
		rec.LastSeenAt = now
	}

	if subpath != "" {
		rec.SubpathLoadCount++
	} else if page > 1 {
		rec.PageLoadCount++
	} else {
		rec.LoadCount++
	}
}

// RecordRun updates the last seen timestamp and increments the script execution count for a skill.
func (s *State) RecordRun(skillName string) {
	if skillName == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	rec, ok := s.History[skillName]
	if !ok || rec == nil {
		rec = &HistoryRecord{
			LastSeenAt: now,
			RunCount:   1,
		}
		s.History[skillName] = rec
	} else {
		rec.LastSeenAt = now
		rec.RunCount++
	}
}

// RecordLearn updates the last seen timestamp and increments the learn count for a skill.
func (s *State) RecordLearn(skillName string) {
	if skillName == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	rec, ok := s.History[skillName]
	if !ok || rec == nil {
		rec = &HistoryRecord{
			LastSeenAt: now,
			LearnCount: 1,
		}
		s.History[skillName] = rec
	} else {
		rec.LastSeenAt = now
		rec.LearnCount++
	}
}

// MaxTelemetryDelta caps each counter increment in a single flush to match Firestore security rule bounds.
const MaxTelemetryDelta = 100

func clampTelemetryDelta(v int) int {
	return min(MaxTelemetryDelta, max(0, v))
}

// ComputeTelemetryDeltas computes positive unsent counter deltas (clamped to MaxTelemetryDelta) for all skills that pass resolveCategory.
func (s *State) ComputeTelemetryDeltas(resolveCategory func(skillName string) (string, bool)) []TelemetryDelta {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.History == nil || resolveCategory == nil {
		return nil
	}

	var names []string
	for name := range s.History {
		names = append(names, name)
	}
	sort.Strings(names)

	var deltas []TelemetryDelta
	for _, name := range names {
		rec := s.History[name]
		if rec == nil {
			continue
		}
		cat, ok := resolveCategory(name)
		if !ok || cat == "" {
			continue
		}

		dLoads := clampTelemetryDelta(rec.LoadCount - rec.SyncedLoadCount)
		dPageLoads := clampTelemetryDelta(rec.PageLoadCount - rec.SyncedPageLoadCount)
		dSubpathLoads := clampTelemetryDelta(rec.SubpathLoadCount - rec.SyncedSubpathLoadCount)
		dRuns := clampTelemetryDelta(rec.RunCount - rec.SyncedRunCount)
		dLearns := clampTelemetryDelta(rec.LearnCount - rec.SyncedLearnCount)

		if dLoads == 0 && dPageLoads == 0 && dSubpathLoads == 0 && dRuns == 0 && dLearns == 0 {
			continue
		}

		deltas = append(deltas, TelemetryDelta{
			Skill:        name,
			Category:     cat,
			Loads:        dLoads,
			PageLoads:    dPageLoads,
			SubpathLoads: dSubpathLoads,
			Runs:         dRuns,
			Learns:       dLearns,
		})
	}
	return deltas
}

// ApplySyncedDeltas snaps synced watermarks to current counter values for synced skills and records last_synced_at.
func (s *State) ApplySyncedDeltas(applied []TelemetryDelta, syncedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.History == nil {
		return
	}
	ts := syncedAt.UTC()
	for _, d := range applied {
		rec, ok := s.History[d.Skill]
		if !ok || rec == nil {
			continue
		}
		rec.SyncedLoadCount = rec.LoadCount
		rec.SyncedPageLoadCount = rec.PageLoadCount
		rec.SyncedSubpathLoadCount = rec.SubpathLoadCount
		rec.SyncedRunCount = rec.RunCount
		rec.SyncedLearnCount = rec.LearnCount
		rec.LastSyncedAt = &ts
	}
}

// BaselineTelemetryWatermarks sets all synced watermarks equal to current counts so only future events produce deltas.
func (s *State) BaselineTelemetryWatermarks(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ts := now.UTC()
	for _, rec := range s.History {
		if rec == nil {
			continue
		}
		rec.SyncedLoadCount = rec.LoadCount
		rec.SyncedPageLoadCount = rec.PageLoadCount
		rec.SyncedSubpathLoadCount = rec.SubpathLoadCount
		rec.SyncedRunCount = rec.RunCount
		rec.SyncedLearnCount = rec.LearnCount
		rec.LastSyncedAt = &ts
	}
}

// GetSkill returns the SkillRecord for a skill in a workspace or global scope.
func (s *State) GetSkill(scope string, workspaceDir string, skillName string) (SkillRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	skills := s.scopeSkillsMap(scope, workspaceDir, false)
	if skills == nil {
		return SkillRecord{}, false
	}
	rec, ok := skills[skillName]
	return rec, ok
}

func (s *State) filterSkillsByStatus(scope, workspaceDir, status string) map[string]SkillRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]SkillRecord)
	for name, rec := range s.scopeSkillsMap(scope, workspaceDir, false) {
		if rec.Status == status {
			out[name] = rec
		}
	}
	return out
}

// GetInstalledSkills returns all currently installed skills in the given scope.
func (s *State) GetInstalledSkills(scope string, workspaceDir string) map[string]SkillRecord {
	return s.filterSkillsByStatus(scope, workspaceDir, StatusInstalled)
}

// GetForgottenSkills returns all soft-deleted skills in the given scope.
func (s *State) GetForgottenSkills(scope string, workspaceDir string) map[string]SkillRecord {
	return s.filterSkillsByStatus(scope, workspaceDir, StatusUninstalled)
}

// ComputePackageDigest computes a deterministic SHA-256 hash over a skill package's files.
func ComputePackageDigest(baseDir string, files []string) (string, error) {
	if len(files) == 0 {
		return "", nil
	}

	sortedFiles := make([]string, len(files))
	copy(sortedFiles, files)
	sort.Strings(sortedFiles)

	h := sha256.New()
	for _, rel := range sortedFiles {
		cleanRel := filepath.ToSlash(filepath.Clean(rel))
		h.Write([]byte(cleanRel + "\n"))

		filePath := filepath.Join(baseDir, filepath.FromSlash(cleanRel))
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("state: hashing %s: %w", filePath, err)
		}
		h.Write(data)
		h.Write([]byte("\n--FILE-BOUNDARY--\n"))
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ReconcileDisk scans the workspace and global skills directories on disk,
// registering untracked skills and reconciling metadata (files, digests, versions).
// isCatalogKnown is a function that checks if a skill name is a known catalog skill.
func (s *State) ReconcileDisk(workspaceDir string, globalDir string, isCatalogKnown func(string) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false

	reconcileScope := func(skillsRoot string, scope string, wsDir string) {
		if skillsRoot == "" {
			return
		}
		entries, err := os.ReadDir(skillsRoot)
		if err != nil {
			return
		}

		for _, entry := range entries {
			skillName := entry.Name()
			skillPath := filepath.Join(skillsRoot, skillName)
			if !entry.IsDir() {
				dirInfo, statErr := os.Stat(skillPath)
				if statErr != nil || !dirInfo.IsDir() {
					continue
				}
			}
			skillMD := filepath.Join(skillPath, "SKILL.md")

			// Only process directories with a valid SKILL.md
			fi, err := os.Stat(skillMD)
			if err != nil || fi.IsDir() {
				continue
			}

			files, _ := scanSkillFiles(skillPath)
			digest, _ := ComputePackageDigest(skillPath, files)

			version := ""
			if content, err := os.ReadFile(skillMD); err == nil {
				version = extractVersion(string(content))
			}

			origin := OriginCustom
			if isCatalogKnown != nil && isCatalogKnown(skillName) {
				origin = OriginCatalog
			}

			skills := s.scopeSkillsMap(scope, wsDir, true)
			rec, exists := skills[skillName]
			if !exists || rec.Status != StatusInstalled {
				skills[skillName] = SkillRecord{
					Name:            skillName,
					Status:          StatusInstalled,
					Scope:           scope,
					Origin:          origin,
					Path:            skillPath,
					Version:         version,
					Digest:          digest,
					InstalledDigest: digest,
					InstalledAt:     fi.ModTime().UTC(),
					UpdatedAt:       time.Now().UTC(),
					Files:           files,
				}
				changed = true
				continue
			}

			updated := false
			if rec.Origin == "" || (rec.Origin == OriginCatalog && origin == OriginCustom && rec.Catalog == "" && rec.Repo == "" && rec.RawURL == "") {
				rec.Origin = origin
				updated = true
			}
			if version != "" && rec.Version != version {
				rec.Version = version
				updated = true
			}
			if rec.InstalledDigest == "" {
				if rec.Digest != "" {
					rec.InstalledDigest = rec.Digest
				} else {
					rec.InstalledDigest = digest
				}
				updated = true
			}
			if rec.Digest != digest {
				rec.Digest = digest
				rec.Files = files
				rec.UpdatedAt = time.Now().UTC()
				updated = true
			}
			if updated {
				skills[skillName] = rec
				changed = true
			}
		}
	}

	reconcileDeleted := func(skillsRoot string, scope string, wsDir string) {
		skills := s.scopeSkillsMap(scope, wsDir, false)
		if skills == nil {
			return
		}
		now := time.Now().UTC()
		for name, rec := range skills {
			if rec.Status != StatusInstalled {
				continue
			}
			skillMD := filepath.Join(rec.Path, "SKILL.md")
			if rec.Path == "" {
				skillMD = filepath.Join(skillsRoot, name, "SKILL.md")
			}
			if fi, err := os.Stat(skillMD); err != nil || fi.IsDir() {
				rec.Status = StatusUninstalled
				rec.UninstalledAt = &now
				rec.UpdatedAt = now
				skills[name] = rec
				changed = true
			}
		}
	}

	// 1. Reconcile workspace
	if workspaceDir != "" {
		wsSkillsDir := filepath.Join(workspaceDir, ".agents", "skills")
		reconcileScope(wsSkillsDir, ScopeWorkspace, workspaceDir)
		reconcileDeleted(wsSkillsDir, ScopeWorkspace, workspaceDir)
	}

	// 2. Reconcile global
	if globalDir != "" {
		gSkillsDir := filepath.Join(globalDir, ".agents", "skills")
		if _, err := os.Stat(gSkillsDir); err == nil {
			reconcileScope(gSkillsDir, ScopeGlobal, "")
		}
		reconcileDeleted(gSkillsDir, ScopeGlobal, "")
	}

	return changed
}

func scanSkillFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func extractVersion(content string) string {
	if fm, _, err := catalog.ParseFrontmatter([]byte(content)); err == nil && fm != nil && fm.Metadata != nil {
		if v := strings.TrimSpace(fm.Metadata["version"]); v != "" {
			return strings.Trim(v, "\"'")
		}
	}
	lines := strings.Split(content, "\n")
	inFrontmatter := false
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			break
		}
		if inFrontmatter {
			if after, ok := strings.CutPrefix(line, "version:"); ok {
				v := strings.TrimSpace(after)
				return strings.Trim(v, "\"'")
			}
		}
	}
	return ""
}

// AddCatalog adds or updates a registered catalog source.
func (s *State) AddCatalog(rec CatalogRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Catalogs == nil {
		s.Catalogs = make(map[string]CatalogRecord)
	}
	if s.deletedCatalogs != nil {
		delete(s.deletedCatalogs, rec.Name)
	}
	if rec.AddedAt.IsZero() {
		rec.AddedAt = time.Now().UTC()
	}
	rec.UpdatedAt = time.Now().UTC()
	s.Catalogs[rec.Name] = rec
	s.UpdatedAt = time.Now().UTC()
}

// RemoveCatalog removes a registered catalog by name.
func (s *State) RemoveCatalog(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Catalogs == nil {
		return false
	}
	if _, ok := s.Catalogs[name]; ok {
		delete(s.Catalogs, name)
		if s.deletedCatalogs == nil {
			s.deletedCatalogs = make(map[string]bool)
		}
		s.deletedCatalogs[name] = true
		s.UpdatedAt = time.Now().UTC()
		return true
	}
	return false
}

// MergeInto merges changes from s into disk without clobbering newer concurrent updates on disk.
func (s *State) MergeInto(disk *State) {
	if s == nil || disk == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if disk.Workspaces == nil {
		disk.Workspaces = make(map[string]*WorkspaceState)
	}
	for wsPath, ws := range s.Workspaces {
		if ws == nil || len(ws.Skills) == 0 {
			continue
		}
		diskWs, ok := disk.Workspaces[wsPath]
		if !ok || diskWs == nil || diskWs.Skills == nil {
			diskWs = &WorkspaceState{Skills: make(map[string]SkillRecord)}
			disk.Workspaces[wsPath] = diskWs
		}
		for name, rec := range ws.Skills {
			if diskRec, exists := diskWs.Skills[name]; !exists || !rec.UpdatedAt.Before(diskRec.UpdatedAt) {
				diskWs.Skills[name] = rec
			}
		}
	}

	if s.Global != nil && len(s.Global.Skills) > 0 {
		if disk.Global == nil || disk.Global.Skills == nil {
			disk.Global = &WorkspaceState{Skills: make(map[string]SkillRecord)}
		}
		for name, rec := range s.Global.Skills {
			if diskRec, exists := disk.Global.Skills[name]; !exists || !rec.UpdatedAt.Before(diskRec.UpdatedAt) {
				disk.Global.Skills[name] = rec
			}
		}
	}

	if disk.Catalogs == nil {
		disk.Catalogs = make(map[string]CatalogRecord)
	}
	for deleted := range s.deletedCatalogs {
		delete(disk.Catalogs, deleted)
	}
	for name, rec := range s.Catalogs {
		if s.deletedCatalogs[name] {
			continue
		}
		if diskRec, exists := disk.Catalogs[name]; !exists || !rec.UpdatedAt.Before(diskRec.UpdatedAt) {
			disk.Catalogs[name] = rec
		}
	}

	if disk.History == nil {
		disk.History = make(map[string]*HistoryRecord)
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
}

// GetCatalog retrieves a registered catalog by name.
func (s *State) GetCatalog(name string) (CatalogRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Catalogs == nil {
		return CatalogRecord{}, false
	}
	rec, ok := s.Catalogs[name]
	return rec, ok
}

// GetCatalogs returns all registered catalogs sorted by name.
func (s *State) GetCatalogs() []CatalogRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var list []CatalogRecord
	for _, rec := range s.Catalogs {
		list = append(list, rec)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}
