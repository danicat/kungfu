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
)

const (
	SchemaVersion     = "1.0"
	StatusInstalled   = "installed"
	StatusUninstalled = "uninstalled"

	ScopeWorkspace = "workspace"
	ScopeGlobal    = "global"

	OriginCatalog = "catalog"
	OriginCustom  = "custom"
)

// SkillRecord represents an installed or soft-deleted skill package.
type SkillRecord struct {
	Name            string     `json:"name"`
	Status          string     `json:"status"`                     // "installed" or "uninstalled"
	Scope           string     `json:"scope"`                      // "workspace" or "global"
	Origin          string     `json:"origin,omitempty"`           // "catalog" or "custom"
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

// HistoryRecord tracks JIT load activity for a skill.
type HistoryRecord struct {
	LastSeenAt time.Time `json:"last_seen_at"`
	LoadCount  int       `json:"load_count"`
}

// State represents the complete root manifest in ~/.config/kungfu/state.json.
type State struct {
	SchemaVersion string                     `json:"schema_version"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	Workspaces    map[string]*WorkspaceState `json:"workspaces,omitempty"`
	Global        *WorkspaceState            `json:"global,omitempty"`
	History       map[string]*HistoryRecord  `json:"history,omitempty"`

	mu sync.RWMutex `json:"-"`
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

// DefaultStateFilePath returns ~/.config/kungfu/state.json.
func DefaultStateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
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

	tmpFile := filePath + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("state: writing temp file %s: %w", tmpFile, err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("state: renaming %s -> %s: %w", tmpFile, filePath, err)
	}

	return nil
}

// RecordInstall records or updates a skill installation in the state.
func (s *State) RecordInstall(scope string, workspaceDir string, record SkillRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record.Status = StatusInstalled
	record.UninstalledAt = nil
	if record.InstalledDigest == "" && record.Digest != "" {
		record.InstalledDigest = record.Digest
	}
	if record.InstalledAt.IsZero() {
		record.InstalledAt = time.Now().UTC()
	}
	record.UpdatedAt = time.Now().UTC()

	if scope == ScopeGlobal {
		record.Scope = ScopeGlobal
		if s.Global == nil {
			s.Global = &WorkspaceState{Skills: make(map[string]SkillRecord)}
		}
		s.Global.Skills[record.Name] = record
		return
	}

	record.Scope = ScopeWorkspace
	cleanWs := filepath.Clean(workspaceDir)
	ws, ok := s.Workspaces[cleanWs]
	if !ok || ws == nil {
		ws = &WorkspaceState{Skills: make(map[string]SkillRecord)}
		s.Workspaces[cleanWs] = ws
	}
	ws.Skills[record.Name] = record
}

// RecordForget soft-deletes a skill by marking it uninstalled.
func (s *State) RecordForget(scope string, workspaceDir string, skillName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()

	if scope == ScopeGlobal {
		if s.Global != nil {
			if rec, ok := s.Global.Skills[skillName]; ok {
				rec.Status = StatusUninstalled
				rec.UninstalledAt = &now
				rec.UpdatedAt = now
				s.Global.Skills[skillName] = rec
				return true
			}
		}
		return false
	}

	cleanWs := filepath.Clean(workspaceDir)
	if ws, ok := s.Workspaces[cleanWs]; ok && ws != nil {
		if rec, ok := ws.Skills[skillName]; ok {
			rec.Status = StatusUninstalled
			rec.UninstalledAt = &now
			rec.UpdatedAt = now
			ws.Skills[skillName] = rec
			return true
		}
	}
	return false
}

// RecordLoad updates the last seen timestamp and increments load count for a skill.
func (s *State) RecordLoad(skillName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.History[skillName]
	if !ok || rec == nil {
		rec = &HistoryRecord{
			LastSeenAt: time.Now().UTC(),
			LoadCount:  1,
		}
		s.History[skillName] = rec
	} else {
		rec.LastSeenAt = time.Now().UTC()
		rec.LoadCount++
	}
}

// GetSkill returns the SkillRecord for a skill in a workspace or global scope.
func (s *State) GetSkill(scope string, workspaceDir string, skillName string) (SkillRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if scope == ScopeGlobal {
		if s.Global != nil {
			rec, ok := s.Global.Skills[skillName]
			return rec, ok
		}
		return SkillRecord{}, false
	}

	cleanWs := filepath.Clean(workspaceDir)
	if ws, ok := s.Workspaces[cleanWs]; ok && ws != nil {
		rec, ok := ws.Skills[skillName]
		return rec, ok
	}
	return SkillRecord{}, false
}

// GetInstalledSkills returns all currently installed skills in the given scope.
func (s *State) GetInstalledSkills(scope string, workspaceDir string) map[string]SkillRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]SkillRecord)
	if scope == ScopeGlobal {
		if s.Global != nil {
			for name, rec := range s.Global.Skills {
				if rec.Status == StatusInstalled {
					out[name] = rec
				}
			}
		}
		return out
	}

	cleanWs := filepath.Clean(workspaceDir)
	if ws, ok := s.Workspaces[cleanWs]; ok && ws != nil {
		for name, rec := range ws.Skills {
			if rec.Status == StatusInstalled {
				out[name] = rec
			}
		}
	}
	return out
}

// GetForgottenSkills returns all soft-deleted skills in the given scope.
func (s *State) GetForgottenSkills(scope string, workspaceDir string) map[string]SkillRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]SkillRecord)
	if scope == ScopeGlobal {
		if s.Global != nil {
			for name, rec := range s.Global.Skills {
				if rec.Status == StatusUninstalled {
					out[name] = rec
				}
			}
		}
		return out
	}

	cleanWs := filepath.Clean(workspaceDir)
	if ws, ok := s.Workspaces[cleanWs]; ok && ws != nil {
		for name, rec := range ws.Skills {
			if rec.Status == StatusUninstalled {
				out[name] = rec
			}
		}
	}
	return out
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

	// Helper to reconcile a single directory of skills
	reconcileScope := func(skillsRoot string, scope string, wsDir string) {
		if skillsRoot == "" {
			return
		}
		entries, err := os.ReadDir(skillsRoot)
		if err != nil {
			return
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillName := entry.Name()
			skillPath := filepath.Join(skillsRoot, skillName)
			skillMD := filepath.Join(skillPath, "SKILL.md")

			// Only process directories with a valid SKILL.md
			fi, err := os.Stat(skillMD)
			if err != nil || fi.IsDir() {
				continue
			}

			files, _ := scanSkillFiles(skillPath)
			digest, _ := ComputePackageDigest(skillPath, files)

			// Extract version from SKILL.md
			version := ""
			if content, err := os.ReadFile(skillMD); err == nil {
				version = extractVersion(string(content))
			}

			origin := OriginCustom
			if isCatalogKnown != nil && isCatalogKnown(skillName) {
				origin = OriginCatalog
			}

			if scope == ScopeGlobal {
				if s.Global == nil {
					s.Global = &WorkspaceState{Skills: make(map[string]SkillRecord)}
				}
				rec, exists := s.Global.Skills[skillName]
				if !exists || rec.Status != StatusInstalled {
					// Adopt/register new skill
					s.Global.Skills[skillName] = SkillRecord{
						Name:            skillName,
						Status:          StatusInstalled,
						Scope:           ScopeGlobal,
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
				} else {
					// Update existing record if files or digest changed
					updated := false
					if rec.Origin == "" {
						rec.Origin = origin
						updated = true
					}
					if rec.Version == "" && version != "" {
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
						s.Global.Skills[skillName] = rec
						changed = true
					}
				}
			} else {
				cleanWs := filepath.Clean(wsDir)
				ws, ok := s.Workspaces[cleanWs]
				if !ok || ws == nil {
					ws = &WorkspaceState{Skills: make(map[string]SkillRecord)}
					s.Workspaces[cleanWs] = ws
				}
				rec, exists := ws.Skills[skillName]
				if !exists || rec.Status != StatusInstalled {
					ws.Skills[skillName] = SkillRecord{
						Name:            skillName,
						Status:          StatusInstalled,
						Scope:           ScopeWorkspace,
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
				} else {
					updated := false
					if rec.Origin == "" {
						rec.Origin = origin
						updated = true
					}
					if rec.Version == "" && version != "" {
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
						ws.Skills[skillName] = rec
						changed = true
					}
				}
			}
		}
	}

	// 1. Reconcile workspace
	if workspaceDir != "" {
		wsSkillsDir := filepath.Join(workspaceDir, ".agents", "skills")
		reconcileScope(wsSkillsDir, ScopeWorkspace, workspaceDir)
	}

	// 2. Reconcile global
	if globalDir != "" {
		gSkillsDir := filepath.Join(globalDir, ".agents", "skills")
		if _, err := os.Stat(gSkillsDir); err == nil {
			reconcileScope(gSkillsDir, ScopeGlobal, "")
		}
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
			if strings.HasPrefix(line, "version:") {
				v := strings.TrimSpace(strings.TrimPrefix(line, "version:"))
				return strings.Trim(v, "\"'")
			}
		}
	}
	return ""
}
