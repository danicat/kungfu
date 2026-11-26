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

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/config"
)

const (
	SchemaVersion   = "1.0"
	StatusInstalled = "installed"

	ScopeWorkspace = "workspace"
	ScopeGlobal    = "global"

	OriginCatalog = "catalog"
	OriginCustom  = "custom"
	OriginGitHub  = "github"

	defaultLockTimeout = 10 * time.Second
	maxStateFileBytes  = 10 << 20 // 10 MiB
	maxHashFileBytes   = 50 << 20 // 50 MiB per file when computing package digest
)

// SkillRecord represents an installed skill package.
type SkillRecord struct {
	Name            string    `json:"name"`
	Status          string    `json:"status"`            // "installed"
	Scope           string    `json:"scope"`             // "workspace" or "global"
	Origin          string    `json:"origin,omitempty"`  // "catalog", "custom", or "github"
	Repo            string    `json:"repo,omitempty"`    // e.g. "https://github.com/owner/repo" or "owner/repo"
	Ref             string    `json:"ref,omitempty"`     // e.g. "HEAD", "main", "v1.0.0"
	RawURL          string    `json:"raw_url,omitempty"` // Canonical download URL
	Catalog         string    `json:"catalog,omitempty"` // Source catalog name (e.g. "default", "kungfu")
	Path            string    `json:"path"`
	Version         string    `json:"version,omitempty"`
	Digest          string    `json:"digest,omitempty"`           // Current/reconciled package digest
	InstalledDigest string    `json:"installed_digest,omitempty"` // Immutable baseline digest at install time
	InstalledAt     time.Time `json:"installed_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Files           []string  `json:"files,omitempty"`
}

// WorkspaceState holds skills installed in a specific workspace directory.
type WorkspaceState struct {
	Skills map[string]SkillRecord `json:"skills"`
}

// HistoryRecord tracks opt-in local usage counts for a skill.
type HistoryRecord struct {
	Catalog          string    `json:"catalog,omitempty"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	LoadCount        int       `json:"load_count"`
	PageLoadCount    int       `json:"page_load_count,omitempty"`
	SubpathLoadCount int       `json:"subpath_load_count,omitempty"`
	RunCount         int       `json:"run_count,omitempty"`
	LearnCount       int       `json:"learn_count,omitempty"`
}

// State represents the persisted state manifest in ~/.config/kungfu/state.json.
type State struct {
	SchemaVersion string                     `json:"schema_version"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	Workspaces    map[string]*WorkspaceState `json:"workspaces,omitempty"`
	Global        *WorkspaceState            `json:"global,omitempty"`
	History       map[string]*HistoryRecord  `json:"history,omitempty"`
}

// NewState creates an initialized State struct.
func NewState() *State {
	return &State{
		SchemaVersion: SchemaVersion,
		UpdatedAt:     time.Now().UTC(),
		Workspaces:    make(map[string]*WorkspaceState),
		Global: &WorkspaceState{
			Skills: make(map[string]SkillRecord),
		},
		History: make(map[string]*HistoryRecord),
	}
}

// DefaultStateFilePath resolves ~/.config/kungfu/state.json, respecting KUNGFU_STATE_PATH and config.UserConfigDir.
func DefaultStateFilePath() (string, error) {
	if statePath := strings.TrimSpace(os.Getenv("KUNGFU_STATE_PATH")); statePath != "" {
		return statePath, nil
	}
	dir, err := config.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("state: %w", err)
	}
	return filepath.Join(dir, "state.json"), nil
}

// LoadState loads State from filePath, returning a fresh State if the file does not exist.
func LoadState(filePath string) (*State, error) {
	if filePath == "" {
		var err error
		filePath, err = DefaultStateFilePath()
		if err != nil {
			return nil, err
		}
	}

	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), nil
		}
		return nil, fmt.Errorf("state: reading %s: %w", filePath, err)
	}
	data, readErr := catalog.ReadBounded(f, maxStateFileBytes)
	_ = f.Close()
	if readErr != nil {
		return nil, fmt.Errorf("state: reading %s: %w", filePath, readErr)
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

	// Prune any legacy soft-deleted records left over from older state files.
	for name, rec := range s.Global.Skills {
		if rec.Status != "" && rec.Status != StatusInstalled {
			delete(s.Global.Skills, name)
		}
	}
	for _, ws := range s.Workspaces {
		if ws == nil || ws.Skills == nil {
			continue
		}
		for name, rec := range ws.Skills {
			if rec.Status != "" && rec.Status != StatusInstalled {
				delete(ws.Skills, name)
			}
		}
	}

	return &s, nil
}

// SaveState writes State atomically to filePath with 0700/0600 modes and fsync.
func SaveState(filePath string, s *State) error {
	if s == nil {
		return fmt.Errorf("state: cannot save nil state")
	}
	if filePath == "" {
		var err error
		filePath, err = DefaultStateFilePath()
		if err != nil {
			return err
		}
	}

	s.UpdatedAt = time.Now().UTC()
	if s.SchemaVersion == "" {
		s.SchemaVersion = SchemaVersion
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
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
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmpFile.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("state: writing temp file %s: %w", tmpPath, err)
	}
	if err := tmpFile.Chmod(0600); err != nil {
		cleanup()
		return fmt.Errorf("state: setting permissions on %s: %w", tmpPath, err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("state: syncing temp file %s: %w", tmpPath, err)
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

// WithLockedState acquires a bounded-timeout exclusive cross-process file lock on filePath+".lock",
// loads the current State from disk, executes fn(s), and saves the updated State atomically.
func WithLockedState(filePath string, fn func(s *State) error) error {
	if filePath == "" {
		var err error
		filePath, err = DefaultStateFilePath()
		if err != nil {
			return err
		}
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("state: creating lock directory %s: %w", dir, err)
	}

	lockPath := filePath + ".lock"
	lockFile, err := openLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("state: opening lock file %s: %w", lockPath, err)
	}
	defer func() {
		_ = lockFile.Close()
	}()

	if err := lockFileExclusive(lockFile, defaultLockTimeout); err != nil {
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
	record.Status = StatusInstalled
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

// RecordForget removes a skill record cleanly from the state.
func (s *State) RecordForget(scope string, workspaceDir string, skillName string) bool {
	skills := s.scopeSkillsMap(scope, workspaceDir, false)
	if _, ok := skills[skillName]; !ok {
		return false
	}
	delete(skills, skillName)
	return true
}

func (s *State) ensureHistory(skillName, catalogSource string) *HistoryRecord {
	if s.History == nil {
		s.History = make(map[string]*HistoryRecord)
	}
	now := time.Now().UTC()
	rec, ok := s.History[skillName]
	if !ok || rec == nil {
		rec = &HistoryRecord{
			Catalog:    strings.TrimSpace(catalogSource),
			LastSeenAt: now,
		}
		s.History[skillName] = rec
	} else {
		rec.LastSeenAt = now
		if catalogSource != "" {
			rec.Catalog = strings.TrimSpace(catalogSource)
		}
	}
	return rec
}

// RecordLoad increments local load history counters (LoadCount, PageLoadCount, or SubpathLoadCount).
func (s *State) RecordLoad(skillName, catalogSource, subpath string, page int) {
	if skillName == "" {
		return
	}
	rec := s.ensureHistory(skillName, catalogSource)
	if subpath != "" {
		rec.SubpathLoadCount++
	} else if page > 1 {
		rec.PageLoadCount++
	} else {
		rec.LoadCount++
	}
}

// RecordRun increments the local script execution counter for a skill.
func (s *State) RecordRun(skillName, catalogSource string) {
	if skillName == "" {
		return
	}
	rec := s.ensureHistory(skillName, catalogSource)
	rec.RunCount++
}

// RecordLearn increments the local install counter for a skill.
func (s *State) RecordLearn(skillName, catalogSource string) {
	if skillName == "" {
		return
	}
	rec := s.ensureHistory(skillName, catalogSource)
	rec.LearnCount++
}

// GetSkill returns the SkillRecord for a skill in a workspace or global scope.
func (s *State) GetSkill(scope string, workspaceDir string, skillName string) (SkillRecord, bool) {
	skills := s.scopeSkillsMap(scope, workspaceDir, false)
	if skills == nil {
		return SkillRecord{}, false
	}
	rec, ok := skills[skillName]
	return rec, ok
}

// GetInstalledSkills returns all currently installed skills in the given scope.
func (s *State) GetInstalledSkills(scope string, workspaceDir string) map[string]SkillRecord {
	out := make(map[string]SkillRecord)
	for name, rec := range s.scopeSkillsMap(scope, workspaceDir, false) {
		if rec.Status == "" || rec.Status == StatusInstalled {
			out[name] = rec
		}
	}
	return out
}

// ComputePackageDigest computes a deterministic SHA-256 hash over regular files inside baseDir.
// Rejects path traversal, symlinks, and non-regular files, and stream-hashes with a size cap.
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
		if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") || strings.HasPrefix(cleanRel, "/") || filepath.IsAbs(rel) {
			return "", fmt.Errorf("state: invalid relative file path %q", rel)
		}

		filePath := filepath.Join(baseDir, filepath.FromSlash(cleanRel))
		if !catalog.IsPathContained(baseDir, filePath) {
			return "", fmt.Errorf("state: file %q escapes %q", rel, baseDir)
		}

		fi, err := os.Lstat(filePath)
		if err != nil {
			return "", fmt.Errorf("state: stating %s: %w", filePath, err)
		}
		if !fi.Mode().IsRegular() {
			continue
		}

		h.Write([]byte(cleanRel + "\n"))
		f, err := os.Open(filePath)
		if err != nil {
			return "", fmt.Errorf("state: hashing %s: %w", filePath, err)
		}
		n, copyErr := io.Copy(h, io.LimitReader(f, maxHashFileBytes+1))
		_ = f.Close()
		if copyErr != nil {
			return "", fmt.Errorf("state: hashing %s: %w", filePath, copyErr)
		}
		if n > maxHashFileBytes {
			return "", fmt.Errorf("state: file %s exceeds maximum hash size (%d bytes)", filePath, maxHashFileBytes)
		}
		h.Write([]byte("\n--FILE-BOUNDARY--\n"))
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ReconcileDisk scans workspace and global skill directories on disk, registering untracked skills,
// updating modified digests/versions, and pruning records whose SKILL.md was removed from disk.
func (s *State) ReconcileDisk(workspaceDir string, globalDir string, isCatalogKnown func(string) bool) bool {
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
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			skillName := entry.Name()
			skillPath := filepath.Join(skillsRoot, skillName)
			if !catalog.IsPathContained(skillsRoot, skillPath) {
				continue
			}

			skillMD := filepath.Join(skillPath, "SKILL.md")
			fi, err := os.Lstat(skillMD)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}

			files, _ := scanSkillFiles(skillPath)
			digest, _ := ComputePackageDigest(skillPath, files)

			version := ""
			if f, err := os.Open(skillMD); err == nil {
				if content, readErr := catalog.ReadBounded(f, catalog.DefaultMaxSkillMDBytes); readErr == nil {
					version = extractVersion(string(content))
				}
				_ = f.Close()
			}

			origin := OriginCustom
			if isCatalogKnown != nil && isCatalogKnown(skillName) {
				origin = OriginCatalog
			}

			skills := s.scopeSkillsMap(scope, wsDir, true)
			rec, exists := skills[skillName]
			if !exists {
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
		for name, rec := range skills {
			skillMD := filepath.Join(rec.Path, "SKILL.md")
			if rec.Path == "" {
				skillMD = filepath.Join(skillsRoot, name, "SKILL.md")
			}
			if fi, err := os.Lstat(skillMD); err != nil || !fi.Mode().IsRegular() {
				delete(skills, name)
				changed = true
			}
		}
	}

	if workspaceDir != "" {
		wsSkillsDir := filepath.Join(workspaceDir, ".agents", "skills")
		reconcileScope(wsSkillsDir, ScopeWorkspace, workspaceDir)
		reconcileDeleted(wsSkillsDir, ScopeWorkspace, workspaceDir)
	}

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
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() || !info.Mode().IsRegular() {
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
	return ""
}
