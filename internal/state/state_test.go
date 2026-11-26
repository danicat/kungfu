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

package state_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danicat/kungfu/internal/state"
)

func TestMain(m *testing.M) {
	tempHome, err := os.MkdirTemp("", "kungfu-state-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp test home: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", tempHome)
	_ = os.Setenv("USERPROFILE", tempHome)
	_ = os.Setenv("APPDATA", filepath.Join(tempHome, "AppData", "Roaming"))
	_ = os.Setenv("LOCALAPPDATA", filepath.Join(tempHome, "AppData", "Local"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tempHome, ".config"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(tempHome, ".cache"))
	_ = os.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tempHome, ".config", "kungfu"))
	_ = os.Setenv("KUNGFU_STATE_PATH", filepath.Join(tempHome, ".config", "kungfu", "state.json"))
	_ = os.Setenv("KUNGFU_CONFIG", "")

	code := m.Run()
	_ = os.RemoveAll(tempHome)
	os.Exit(code)
}

func TestState_LifecycleAndPersistence(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	// 1. Initial load from non-existent file returns empty state
	s, err := state.LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if s.SchemaVersion != state.SchemaVersion {
		t.Errorf("expected schema version %s, got %s", state.SchemaVersion, s.SchemaVersion)
	}

	wsDir := "/workspace/test-proj"

	// 2. Record install in workspace
	s.RecordInstall(state.ScopeWorkspace, wsDir, state.SkillRecord{
		Name:    "social-copy",
		Path:    "/workspace/test-proj/.agents/skills/social-copy",
		Version: "1.0.0",
		Digest:  "sha256:abc123",
		Files:   []string{"SKILL.md", "references/guide.md"},
	})

	// 3. Record install in global
	s.RecordInstall(state.ScopeGlobal, "", state.SkillRecord{
		Name:    "buffer",
		Path:    "/home/user/.agents/skills/buffer",
		Version: "2.0.0",
		Digest:  "sha256:def456",
		Files:   []string{"SKILL.md"},
	})

	// 4. Record load history
	s.RecordLoad("godoctor", "default", "", 1)
	s.RecordLoad("godoctor", "default", "", 1)

	// 5. Save state atomically with restricted permissions
	if err := state.SaveState(stateFile, s); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(stateFile)
		if err != nil {
			t.Fatalf("stat stateFile: %v", err)
		}
		if fi.Mode().Perm() != 0600 {
			t.Errorf("state.json mode = %04o, want 0600", fi.Mode().Perm())
		}
	}

	// 6. Reload from disk and verify
	reloaded, err := state.LoadState(stateFile)
	if err != nil {
		t.Fatalf("reloading state failed: %v", err)
	}

	installedWs := reloaded.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	if len(installedWs) != 1 || installedWs["social-copy"].Version != "1.0.0" {
		t.Errorf("unexpected installed ws skills: %+v", installedWs)
	}

	installedGlobal := reloaded.GetInstalledSkills(state.ScopeGlobal, "")
	if len(installedGlobal) != 1 || installedGlobal["buffer"].Version != "2.0.0" {
		t.Errorf("unexpected installed global skills: %+v", installedGlobal)
	}

	if hist, ok := reloaded.History["godoctor"]; !ok || hist.LoadCount != 2 {
		t.Errorf("unexpected history record: %+v", hist)
	}

	// 7. Forget removes workspace skill cleanly
	if !reloaded.RecordForget(state.ScopeWorkspace, wsDir, "social-copy") {
		t.Fatalf("RecordForget returned false")
	}
	if got := reloaded.GetInstalledSkills(state.ScopeWorkspace, wsDir); len(got) != 0 {
		t.Errorf("expected 0 installed skills after forget, got %d", len(got))
	}

	// 8. Forget removes global skill cleanly
	if !reloaded.RecordForget(state.ScopeGlobal, "", "buffer") {
		t.Fatalf("RecordForget global returned false")
	}
	if got := reloaded.GetInstalledSkills(state.ScopeGlobal, ""); len(got) != 0 {
		t.Errorf("expected 0 global skills after forget, got %d", len(got))
	}
}

func TestComputePackageDigest(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte("# Title"), 0644)
	refsDir := filepath.Join(tmpDir, "references")
	_ = os.MkdirAll(refsDir, 0755)
	_ = os.WriteFile(filepath.Join(refsDir, "doc.md"), []byte("# Reference doc"), 0644)

	d1, err := state.ComputePackageDigest(tmpDir, []string{"SKILL.md", "references/doc.md"})
	if err != nil {
		t.Fatalf("ComputePackageDigest failed: %v", err)
	}
	if !strings.HasPrefix(d1, "sha256:") {
		t.Fatalf("expected sha256: prefix, got: %s", d1)
	}

	// Order of input files should not affect deterministic hash
	d2, err := state.ComputePackageDigest(tmpDir, []string{"references/doc.md", "SKILL.md"})
	if err != nil || d1 != d2 {
		t.Fatalf("expected same digest regardless of file order, got %s vs %s", d1, d2)
	}

	// Changing content changes digest
	_ = os.WriteFile(filepath.Join(refsDir, "doc.md"), []byte("# Changed Reference doc"), 0644)
	d3, err := state.ComputePackageDigest(tmpDir, []string{"SKILL.md", "references/doc.md"})
	if err != nil || d1 == d3 {
		t.Fatalf("expected different digest when content changes, got %s vs %s", d1, d3)
	}

	// Path traversal must be rejected
	if _, err := state.ComputePackageDigest(tmpDir, []string{"../outside.txt"}); err == nil {
		t.Errorf("expected ComputePackageDigest to reject ../ traversal")
	}

	// Symlink escaping tmpDir must be rejected
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("secret"), 0644)
	if err := os.Symlink(outsideFile, filepath.Join(tmpDir, "escape.md")); err == nil {
		if _, err := state.ComputePackageDigest(tmpDir, []string{"escape.md"}); err == nil {
			t.Errorf("expected ComputePackageDigest to reject symlink escape")
		}
	}
}

func TestState_ConcurrentLockedAccess(t *testing.T) {
	t.Parallel()

	stateFile := filepath.Join(t.TempDir(), "state.json")
	var wg sync.WaitGroup

	for range 20 {
		wg.Go(func() {
			if err := state.WithLockedState(stateFile, func(s *state.State) error {
				s.RecordLoad("skill-a", "default", "", 1)
				s.RecordInstall(state.ScopeWorkspace, "/workspace", state.SkillRecord{
					Name:        "skill-a",
					Version:     "1.0",
					InstalledAt: time.Now(),
				})
				return nil
			}); err != nil {
				t.Errorf("WithLockedState failed: %v", err)
			}
		})
	}

	wg.Wait()

	loaded, err := state.LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if loaded.History["skill-a"] == nil || loaded.History["skill-a"].LoadCount != 20 {
		t.Errorf("expected LoadCount=20, got %+v", loaded.History["skill-a"])
	}
}

func TestState_ReconcileDisk(t *testing.T) {
	t.Parallel()

	wsDir := t.TempDir()
	globalDir := t.TempDir()

	// Create a catalog-known skill in workspace
	wsSkillDir := filepath.Join(wsDir, ".agents", "skills", "buffer")
	_ = os.MkdirAll(wsSkillDir, 0755)
	_ = os.WriteFile(filepath.Join(wsSkillDir, "SKILL.md"), []byte(`---
name: buffer
version: "1.2.3"
---
# Buffer Skill`), 0644)

	// Create a custom skill in workspace
	customSkillDir := filepath.Join(wsDir, ".agents", "skills", "my-custom-tool")
	_ = os.MkdirAll(customSkillDir, 0755)
	_ = os.WriteFile(filepath.Join(customSkillDir, "SKILL.md"), []byte(`---
name: my-custom-tool
version: "0.9.0"
---
# My Custom Tool`), 0644)

	// Create a catalog skill in global
	globalSkillDir := filepath.Join(globalDir, ".agents", "skills", "godoctor")
	_ = os.MkdirAll(globalSkillDir, 0755)
	_ = os.WriteFile(filepath.Join(globalSkillDir, "SKILL.md"), []byte(`---
name: godoctor
version: "2.0.0"
---
# GoDoctor`), 0644)

	s := state.NewState()
	isKnown := func(name string) bool {
		return name == "buffer" || name == "godoctor"
	}

	changed := s.ReconcileDisk(wsDir, globalDir, isKnown)
	if !changed {
		t.Fatalf("expected ReconcileDisk to return true on new skills")
	}

	// Verify workspace skills
	wsSkills := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	if len(wsSkills) != 2 {
		t.Fatalf("expected 2 workspace skills, got %d", len(wsSkills))
	}

	bufRec := wsSkills["buffer"]
	if bufRec.Origin != state.OriginCatalog || bufRec.Version != "1.2.3" {
		t.Errorf("buffer skill incorrect: %+v", bufRec)
	}

	customRec := wsSkills["my-custom-tool"]
	if customRec.Origin != state.OriginCustom || customRec.Version != "0.9.0" {
		t.Errorf("custom skill incorrect: %+v", customRec)
	}

	// Verify global skills
	gSkills := s.GetInstalledSkills(state.ScopeGlobal, "")
	if len(gSkills) != 1 {
		t.Fatalf("expected 1 global skill, got %d", len(gSkills))
	}
	goRec := gSkills["godoctor"]
	if goRec.Origin != state.OriginCatalog || goRec.Version != "2.0.0" {
		t.Errorf("godoctor global skill incorrect: %+v", goRec)
	}

	// Update godoctor version on disk without changing anything else and verify ReconcileDisk updates Version
	_ = os.WriteFile(filepath.Join(globalSkillDir, "SKILL.md"), []byte(`---
name: godoctor
version: "2.1.0"
---
# GoDoctor`), 0644)
	if !s.ReconcileDisk(wsDir, globalDir, isKnown) {
		t.Fatalf("expected ReconcileDisk to return true when version changes on disk")
	}
	if gotVer := s.GetInstalledSkills(state.ScopeGlobal, "")["godoctor"].Version; gotVer != "2.1.0" {
		t.Errorf("expected reconciled version 2.1.0, got %q", gotVer)
	}

	// Delete godoctor from disk and test reconciliation removes it cleanly
	_ = os.RemoveAll(globalSkillDir)
	changed = s.ReconcileDisk(wsDir, globalDir, isKnown)
	if !changed {
		t.Fatalf("expected ReconcileDisk to return true when skills deleted from disk")
	}
	if gSkillsAfter := s.GetInstalledSkills(state.ScopeGlobal, ""); len(gSkillsAfter) != 0 {
		t.Fatalf("expected 0 global skills after deletion, got %d", len(gSkillsAfter))
	}
}

func TestState_WithLockedStateAndDetailedHistory(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := state.WithLockedState(stateFile, func(s *state.State) error {
				s.RecordLoad("godoctor", "default", "", 1)
				if idx%2 == 0 {
					s.RecordLoad("godoctor", "default", "", 2)
				}
				if idx%4 == 0 {
					s.RecordLoad("godoctor", "default", "references/arch.md", 1)
				}
				if idx%5 == 0 {
					s.RecordRun("godoctor", "default")
				}
				return nil
			})
			if err != nil {
				t.Errorf("WithLockedState failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	loaded, err := state.LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	h, ok := loaded.History["godoctor"]
	if !ok {
		t.Fatalf("expected godoctor history entry")
	}
	if h.LoadCount != 20 {
		t.Errorf("expected LoadCount=20, got %d", h.LoadCount)
	}
	if h.PageLoadCount != 10 {
		t.Errorf("expected PageLoadCount=10, got %d", h.PageLoadCount)
	}
	if h.SubpathLoadCount != 5 {
		t.Errorf("expected SubpathLoadCount=5, got %d", h.SubpathLoadCount)
	}
	if h.RunCount != 4 {
		t.Errorf("expected RunCount=4, got %d", h.RunCount)
	}
}

func TestState_RecordInstallClearsDirtyDigest(t *testing.T) {
	t.Parallel()

	s := state.NewState()
	wsDir := "/tmp/ws"

	s.RecordInstall(state.ScopeWorkspace, wsDir, state.SkillRecord{
		Name:            "alpha",
		Scope:           state.ScopeWorkspace,
		Version:         "1.0.0",
		Digest:          "sha256:1111",
		InstalledDigest: "sha256:1111",
	})

	wsSkills := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	dirtyRec := wsSkills["alpha"]
	dirtyRec.Digest = "sha256:dirty"
	s.RecordInstall(state.ScopeWorkspace, wsDir, dirtyRec)

	// RecordInstall during update -f with new upstream digest updates InstalledDigest too.
	updatedRec := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)["alpha"]
	updatedRec.Version = "1.1.0"
	updatedRec.Digest = "sha256:2222"
	s.RecordInstall(state.ScopeWorkspace, wsDir, updatedRec)

	finalRec := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)["alpha"]
	if finalRec.InstalledDigest != "sha256:2222" {
		t.Errorf("expected InstalledDigest to be updated to sha256:2222, got %q", finalRec.InstalledDigest)
	}
}

func TestState_ReconcileDiskSkipsEscapingSymlinkAndDetectsMissingSkillMD(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	wsDir := filepath.Join(tmpDir, "workspace")
	skillsBase := filepath.Join(wsDir, ".agents", "skills")
	if err := os.MkdirAll(skillsBase, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Symlink pointing outside skillsBase must be skipped for security.
	externalSkillTarget := filepath.Join(tmpDir, "external-skill")
	if err := os.MkdirAll(externalSkillTarget, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalSkillTarget, "SKILL.md"), []byte("---\nname: linked-skill\nversion: \"3.0.0\"\n---\n# Linked"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalSkillTarget, filepath.Join(skillsBase, "linked-skill")); err == nil {
		s := state.NewState()
		s.ReconcileDisk(wsDir, "", nil)
		if _, found := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)["linked-skill"]; found {
			t.Errorf("expected ReconcileDisk to skip symlinked skill directory pointing outside skills root")
		}
	}

	// 2. Regular skill directory is discovered and removed when SKILL.md is deleted.
	realSkillDir := filepath.Join(skillsBase, "real-skill")
	if err := os.MkdirAll(realSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realSkillDir, "SKILL.md"), []byte("---\nname: real-skill\nversion: \"3.0.0\"\n---\n# Real"), 0644); err != nil {
		t.Fatal(err)
	}

	s := state.NewState()
	if !s.ReconcileDisk(wsDir, "", nil) {
		t.Fatalf("expected ReconcileDisk to discover real-skill")
	}
	if rec, ok := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)["real-skill"]; !ok || rec.Version != "3.0.0" {
		t.Fatalf("expected real-skill v3.0.0 to be installed, got %+v", rec)
	}

	_ = os.Remove(filepath.Join(realSkillDir, "SKILL.md"))
	if !s.ReconcileDisk(wsDir, "", nil) {
		t.Fatalf("expected ReconcileDisk to detect missing SKILL.md")
	}
	if len(s.GetInstalledSkills(state.ScopeWorkspace, wsDir)) != 0 {
		t.Errorf("expected real-skill to be removed when SKILL.md is deleted")
	}
}
