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

package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danicat/kungfu/internal/state"
)

func TestMain(m *testing.M) {
	tempHome, err := os.MkdirTemp("", "kungfu-state-test-*")
	if err == nil {
		_ = os.Setenv("HOME", tempHome)
		_ = os.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tempHome, ".config", "kungfu"))
		_ = os.Setenv("KUNGFU_STATE_PATH", filepath.Join(tempHome, ".config", "kungfu", "state.json"))
		_ = os.Setenv("KUNGFU_CONFIG", "")
		_ = os.Setenv("KUNGFU_TELEMETRY", "0")
		_ = os.Setenv("DO_NOT_TRACK", "")
	}
	code := m.Run()
	if tempHome != "" {
		_ = os.RemoveAll(tempHome)
	}
	os.Exit(code)
}

func TestState_LifecycleAndPersistence(t *testing.T) {
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
	s.RecordLoad("godoctor")
	s.RecordLoad("godoctor")

	// 5. Save state atomically
	if err := state.SaveState(stateFile, s); err != nil {
		t.Fatalf("SaveState failed: %v", err)
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

	// 7. Test Soft Delete (Forget)
	ok := reloaded.RecordForget(state.ScopeWorkspace, wsDir, "social-copy")
	if !ok {
		t.Fatalf("RecordForget returned false")
	}

	installedAfterForget := reloaded.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	if len(installedAfterForget) != 0 {
		t.Errorf("expected 0 installed skills after forget, got %d", len(installedAfterForget))
	}

	forgotten := reloaded.GetForgottenSkills(state.ScopeWorkspace, wsDir)
	if len(forgotten) != 1 || forgotten["social-copy"].Status != state.StatusUninstalled || forgotten["social-copy"].UninstalledAt == nil {
		t.Errorf("unexpected forgotten skills: %+v", forgotten)
	}

	// 8. Test Global Soft Delete
	ok = reloaded.RecordForget(state.ScopeGlobal, "", "buffer")
	if !ok {
		t.Fatalf("RecordForget global returned false")
	}
	forgottenGlobal := reloaded.GetForgottenSkills(state.ScopeGlobal, "")
	if len(forgottenGlobal) != 1 || forgottenGlobal["buffer"].Status != state.StatusUninstalled {
		t.Errorf("unexpected global forgotten skills: %+v", forgottenGlobal)
	}
}

func TestComputePackageDigest(t *testing.T) {
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
}

func TestState_ConcurrentAccess(t *testing.T) {
	s := state.NewState()
	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			s.RecordLoad("skill-a")
			s.RecordInstall(state.ScopeWorkspace, "/workspace", state.SkillRecord{
				Name:        "skill-a",
				Version:     "1.0",
				InstalledAt: time.Now(),
			})
		}(i)

		go func(id int) {
			defer wg.Done()
			_ = s.GetInstalledSkills(state.ScopeWorkspace, "/workspace")
			s.GetSkill(state.ScopeWorkspace, "/workspace", "skill-a")
		}(i)
	}

	wg.Wait()
}

func TestState_ReconcileDisk(t *testing.T) {
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

	// Now delete godoctor from disk and test reconciliation
	_ = os.RemoveAll(globalSkillDir)
	changed = s.ReconcileDisk(wsDir, globalDir, isKnown)
	if !changed {
		t.Fatalf("expected ReconcileDisk to return true when skills deleted from disk")
	}
	gSkillsAfter := s.GetInstalledSkills(state.ScopeGlobal, "")
	if len(gSkillsAfter) != 0 {
		t.Fatalf("expected 0 global skills after deletion, got %d", len(gSkillsAfter))
	}
	gForgotten := s.GetForgottenSkills(state.ScopeGlobal, "")
	if len(gForgotten) != 1 || gForgotten["godoctor"].Status != state.StatusUninstalled {
		t.Errorf("expected godoctor to be marked uninstalled in forgotten skills: %+v", gForgotten)
	}
}

func TestState_WithLockedStateAndDetailedTelemetry(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	// Parallel WithLockedState invocations simulating concurrent ::parallel(N subagent loads
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := state.WithLockedState(stateFile, func(s *state.State) error {
				s.RecordLoadDetailed("godoctor", "", 1)
				if idx%2 == 0 {
					s.RecordLoadDetailed("godoctor", "", 2)
				}
				if idx%4 == 0 {
					s.RecordLoadDetailed("godoctor", "references/arch.md", 1)
				}
				if idx%5 == 0 {
					s.RecordRun("godoctor")
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

func TestState_TelemetryDeltaClampingAndBackfill(t *testing.T) {
	s := state.NewState()
	for range 150 {
		s.RecordLoadDetailedWithCatalog("godoctor", "https://skills.danicat.dev", "", 1)
	}
	resolveOfficial := func(name string) (string, bool) {
		return "https://skills.danicat.dev", name == "godoctor"
	}
	deltas := s.ComputeTelemetryDeltas(resolveOfficial)
	if len(deltas) != 1 || deltas[0].Loads != state.MaxTelemetryDelta {
		t.Fatalf("expected Loads clamped to %d, got %+v", state.MaxTelemetryDelta, deltas)
	}
	s.ApplySyncedDeltas(deltas, time.Now().UTC())
	if s.History["godoctor"].LoadCount != 150 {
		t.Errorf("expected immutable LoadCount=150, got %d", s.History["godoctor"].LoadCount)
	}
	if s.History["godoctor"].SyncedLoadCount != 100 {
		t.Errorf("expected SyncedLoadCount advanced by 100 (not snapped to 150), got %d", s.History["godoctor"].SyncedLoadCount)
	}
	remaining := s.ComputeTelemetryDeltas(resolveOfficial)
	if len(remaining) != 1 || remaining[0].Loads != 50 {
		t.Fatalf("expected 50 remaining unsynced loads, got %+v", remaining)
	}
	s.ApplySyncedDeltas(remaining, time.Now().UTC())
	if s.History["godoctor"].SyncedLoadCount != 150 {
		t.Errorf("expected SyncedLoadCount=150 after second batch, got %d", s.History["godoctor"].SyncedLoadCount)
	}
	after := s.ComputeTelemetryDeltas(resolveOfficial)
	if len(after) != 0 {
		t.Errorf("expected 0 pending deltas after full sync, got %+v", after)
	}

	// Resetting watermarks allows backfilling the full immutable 150 loads
	s.ResetTelemetryWatermarks()
	if s.History["godoctor"].LoadCount != 150 || s.History["godoctor"].SyncedLoadCount != 0 {
		t.Errorf("expected LoadCount=150 and SyncedLoadCount=0 after ResetTelemetryWatermarks, got %+v", s.History["godoctor"])
	}
}

func TestState_RecordInstallClearsDirtyDigestAndMergeInto(t *testing.T) {
	s := state.NewState()
	wsDir := "/tmp/ws"

	// Initial install
	s.RecordInstall(state.ScopeWorkspace, wsDir, state.SkillRecord{
		Name:            "alpha",
		Scope:           state.ScopeWorkspace,
		Version:         "1.0.0",
		Digest:          "sha256:1111",
		InstalledDigest: "sha256:1111",
	})

	// Simulate local edit making Digest != InstalledDigest
	wsSkills := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	dirtyRec := wsSkills["alpha"]
	dirtyRec.Digest = "sha256:dirty"
	s.RecordInstall(state.ScopeWorkspace, wsDir, dirtyRec)

	// Wait: when RecordInstall is called during `update -f` with a new upstream digest,
	// InstalledDigest must be updated to match the newly installed Digest so the skill is no longer dirty.
	updatedRec := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)["alpha"]
	updatedRec.Version = "1.1.0"
	updatedRec.Digest = "sha256:2222"
	s.RecordInstall(state.ScopeWorkspace, wsDir, updatedRec)

	finalRec := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)["alpha"]
	if finalRec.InstalledDigest != "sha256:2222" {
		t.Errorf("expected InstalledDigest to be updated to sha256:2222, got %q", finalRec.InstalledDigest)
	}

	// Test MergeInto preserves concurrent history and honors catalog deletions
	s.AddCatalog(state.CatalogRecord{Name: "cat-a", Type: state.CatalogTypeGitHub, Source: "org/a"})
	s.AddCatalog(state.CatalogRecord{Name: "cat-b", Type: state.CatalogTypeGitHub, Source: "org/b"})

	disk := state.NewState()
	disk.AddCatalog(state.CatalogRecord{Name: "cat-a", Type: state.CatalogTypeGitHub, Source: "org/a"})
	disk.AddCatalog(state.CatalogRecord{Name: "cat-b", Type: state.CatalogTypeGitHub, Source: "org/b"})
	disk.RecordLoadDetailed("concurrent-skill", "", 1)

	// Remove cat-a in s and merge into disk
	s.RemoveCatalog("cat-a")
	s.MergeInto(disk)

	if _, ok := disk.GetCatalog("cat-a"); ok {
		t.Errorf("expected cat-a to be removed from disk state after MergeInto")
	}
	if _, ok := disk.GetCatalog("cat-b"); !ok {
		t.Errorf("expected cat-b to remain in disk state after MergeInto")
	}
	if disk.History["concurrent-skill"] == nil || disk.History["concurrent-skill"].LoadCount != 1 {
		t.Errorf("expected concurrent-skill history to be preserved in disk state, got %+v", disk.History["concurrent-skill"])
	}
}

func TestState_ReconcileDiskSymlinkAndMissingSkillMD(t *testing.T) {
	tmpDir := t.TempDir()
	wsDir := filepath.Join(tmpDir, "workspace")
	externalSkillTarget := filepath.Join(tmpDir, "external-skill")
	if err := os.MkdirAll(externalSkillTarget, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalSkillTarget, "SKILL.md"), []byte("---\nname: linked-skill\nversion: \"3.0.0\"\n---\n# Linked"), 0644); err != nil {
		t.Fatal(err)
	}

	skillsBase := filepath.Join(wsDir, ".agents", "skills")
	if err := os.MkdirAll(skillsBase, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(skillsBase, "linked-skill")
	if err := os.Symlink(externalSkillTarget, symlinkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	s := state.NewState()
	if !s.ReconcileDisk(wsDir, "", nil) {
		t.Fatalf("expected ReconcileDisk to discover symlinked skill directory")
	}
	installed := s.GetInstalledSkills(state.ScopeWorkspace, wsDir)
	if rec, ok := installed["linked-skill"]; !ok || rec.Version != "3.0.0" {
		t.Fatalf("expected linked-skill v3.0.0 to be installed, got %+v", installed)
	}

	// Remove SKILL.md inside the target directory while leaving the directory intact
	_ = os.Remove(filepath.Join(externalSkillTarget, "SKILL.md"))
	if !s.ReconcileDisk(wsDir, "", nil) {
		t.Fatalf("expected ReconcileDisk to detect missing SKILL.md")
	}
	if len(s.GetInstalledSkills(state.ScopeWorkspace, wsDir)) != 0 {
		t.Errorf("expected linked-skill to be marked uninstalled when SKILL.md is removed")
	}
}
