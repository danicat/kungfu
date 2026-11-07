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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/config"
	"github.com/danicat/kungfu/internal/state"
)

func TestRunTelemetry_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("KUNGFU_CONFIG_DIR", tmpDir)
	t.Setenv("KUNGFU_STATE_PATH", stateFile)
	t.Setenv("KUNGFU_CONFIG", "")
	t.Setenv("KUNGFU_TELEMETRY", "")
	t.Setenv("DO_NOT_TRACK", "")

	// Seed state with 2 official catalog skills ("godoctor", "brainstorm") and 1 third-party/custom skill ("private-skill")
	st := state.NewState()
	st.RecordLoadDetailedWithCatalog("godoctor", "https://skills.danicat.dev", "", 1)
	st.RecordLoadDetailedWithCatalog("godoctor", "https://skills.danicat.dev", "", 2)
	st.RecordRunWithCatalog("godoctor", "https://skills.danicat.dev")
	st.RecordLearnWithCatalog("godoctor", "https://skills.danicat.dev")
	st.RecordLoadDetailedWithCatalog("brainstorm", "https://skills.danicat.dev", "", 1)
	st.RecordLoadDetailedWithCatalog("private-skill", "github:acme/private", "", 1)
	if err := state.SaveState(stateFile, st); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	if cat, ok := app.Store.(*catalog.Catalog); ok {
		cat.AddItem(catalog.Item{
			ID:     "godoctor",
			Name:   "godoctor",
			Source: "remote",
			URL:    "https://skills.danicat.dev/.well-known/agent-skills/godoctor.tar.gz",
		})
		cat.AddItem(catalog.Item{
			ID:     "brainstorm",
			Name:   "brainstorm",
			Source: "remote",
			URL:    "https://skills.danicat.dev/.well-known/agent-skills/brainstorm/SKILL.md",
		})
		cat.AddItem(catalog.Item{
			ID:     "private-skill",
			Name:   "private-skill",
			Source: "github:acme/private",
			URL:    "https://raw.githubusercontent.com/acme/private/main/SKILL.md",
		})
	}

	// 1. Default status is disabled and includes official catalog skills ("brainstorm", "godoctor"), NOT "private-skill"
	stdout.Reset()
	stderr.Reset()
	if code := app.RunTelemetry([]string{"--json", "status"}); code != ExitSuccess {
		t.Fatalf("telemetry status failed: %d (%s)", code, stderr.String())
	}
	var statusOut struct {
		Enabled       bool                   `json:"enabled"`
		PendingSkills int                    `json:"pending_skills"`
		PendingDeltas []state.TelemetryDelta `json:"pending_deltas"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &statusOut); err != nil {
		t.Fatalf("unmarshal status JSON failed: %v", err)
	}
	if statusOut.Enabled {
		t.Errorf("expected telemetry disabled by default")
	}
	if statusOut.PendingSkills != 2 || len(statusOut.PendingDeltas) != 2 {
		t.Fatalf("expected 2 pending official skill deltas, got %d: %+v", statusOut.PendingSkills, statusOut.PendingDeltas)
	}
	d0 := statusOut.PendingDeltas[0]
	if d0.Skill != "brainstorm" || d0.Catalog != "https://skills.danicat.dev" || d0.Loads != 1 {
		t.Errorf("unexpected delta for brainstorm: %+v", d0)
	}
	d1 := statusOut.PendingDeltas[1]
	if d1.Skill != "godoctor" || d1.Catalog != "https://skills.danicat.dev" || d1.Loads != 1 || d1.PageLoads != 1 || d1.Runs != 1 || d1.Learns != 1 {
		t.Errorf("unexpected delta for godoctor: %+v", d1)
	}

	// 2. Running 'sync' while telemetry is disabled fails fast with ExitGeneral
	stdout.Reset()
	stderr.Reset()
	if code := app.RunTelemetry([]string{"sync"}); code != ExitGeneral {
		t.Fatalf("expected telemetry sync while disabled to return ExitGeneral (%d), got %d", ExitGeneral, code)
	}
	if !strings.Contains(stderr.String(), "telemetry is disabled") {
		t.Errorf("expected disabled error message on stderr, got: %s", stderr.String())
	}

	// 3. 'enable' opts in and preserves existing unsynced history (never wipes or baselines local telemetry)
	stdout.Reset()
	stderr.Reset()
	if code := app.RunTelemetry([]string{"enable"}); code != ExitSuccess {
		t.Fatalf("telemetry enable failed: %d (%s)", code, stderr.String())
	}
	loadedCfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if !loadedCfg.IsTelemetryEnabled() {
		t.Errorf("expected telemetry enabled in saved config")
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.RunTelemetry([]string{"status", "--json"}); code != ExitSuccess {
		t.Fatalf("telemetry status after enable failed: %d", code)
	}
	if err := json.Unmarshal(stdout.Bytes(), &statusOut); err != nil {
		t.Fatalf("unmarshal status JSON failed: %v", err)
	}
	if statusOut.PendingSkills != 2 {
		t.Errorf("expected 2 pending skills preserved after enable, got %d", statusOut.PendingSkills)
	}

	// 4. 'disable' turns telemetry off
	stdout.Reset()
	stderr.Reset()
	if code := app.RunTelemetry([]string{"disable"}); code != ExitSuccess {
		t.Fatalf("telemetry disable failed: %d (%s)", code, stderr.String())
	}
	loadedCfg, err = config.Load(cfgFile)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if loadedCfg.IsTelemetryEnabled() {
		t.Errorf("expected telemetry disabled after 'telemetry disable'")
	}
}

func TestRunTelemetry_SyncAndBackfill(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")
	t.Setenv("KUNGFU_CONFIG_DIR", tmpDir)
	t.Setenv("KUNGFU_STATE_PATH", stateFile)
	t.Setenv("KUNGFU_CONFIG", "")
	t.Setenv("KUNGFU_TELEMETRY", "1")
	t.Setenv("DO_NOT_TRACK", "")

	// Seed 150 loads for "brainstorm" (requires 2 batches of 100 and 50)
	st := state.NewState()
	for range 150 {
		st.RecordLoadDetailedWithCatalog("brainstorm", "https://skills.danicat.dev", "", 1)
	}
	if err := state.SaveState(stateFile, st); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	var commitBatches int
	fsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, ":signUp") {
			_, _ = w.Write([]byte(`{"idToken":"test-jwt-token"}`))
			return
		}
		commitBatches++
		_, _ = w.Write([]byte(`{"writeResults":[{}]}`))
	}))
	defer fsServer.Close()

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app.HTTPClient = &http.Client{
		Transport: &rewriteTransport{
			base:      fsServer.Client().Transport,
			targetURL: fsServer.URL,
		},
	}
	if cat, ok := app.Store.(*catalog.Catalog); ok {
		cat.AddItem(catalog.Item{
			ID:     "brainstorm",
			Name:   "brainstorm",
			Source: "remote",
			URL:    "https://skills.danicat.dev/.well-known/agent-skills/brainstorm/SKILL.md",
		})
	}

	// 1. Sync flushes all 150 loads across 2 batches (100 + 50)
	if code := app.RunTelemetry([]string{"sync", "--json"}); code != ExitSuccess {
		t.Fatalf("telemetry sync failed: %d (%s)", code, stderr.String())
	}
	if commitBatches != 2 {
		t.Errorf("expected 2 Firestore commit batches for 150 loads, got %d", commitBatches)
	}
	var syncOut struct {
		Status       string                 `json:"status"`
		SyncedSkills int                    `json:"synced_skills"`
		Deltas       []state.TelemetryDelta `json:"deltas"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &syncOut); err != nil {
		t.Fatalf("unmarshal sync JSON failed: %v", err)
	}
	if syncOut.Status != "synced" || syncOut.SyncedSkills != 1 || len(syncOut.Deltas) != 1 || syncOut.Deltas[0].Loads != 150 {
		t.Errorf("unexpected sync output: %+v", syncOut)
	}

	// 2. Local telemetry remains immutable (LoadCount=150) while SyncedLoadCount=150
	afterSync, err := state.LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	rec := afterSync.History["brainstorm"]
	if rec == nil || rec.LoadCount != 150 || rec.SyncedLoadCount != 150 {
		t.Fatalf("expected immutable LoadCount=150 and SyncedLoadCount=150, got %+v", rec)
	}

	// 3. Subsequent sync is up_to_date
	stdout.Reset()
	stderr.Reset()
	commitBatches = 0
	if code := app.RunTelemetry([]string{"sync", "--json"}); code != ExitSuccess {
		t.Fatalf("second telemetry sync failed: %d (%s)", code, stderr.String())
	}
	if commitBatches != 0 || !strings.Contains(stdout.String(), `"status": "up_to_date"`) {
		t.Errorf("expected up_to_date with 0 commits, got commits=%d out=%s", commitBatches, stdout.String())
	}

	// 4. 'sync --backfill' resets watermarks and replays all 150 loads from immutable history
	stdout.Reset()
	stderr.Reset()
	commitBatches = 0
	if code := app.RunTelemetry([]string{"sync", "--backfill", "--json"}); code != ExitSuccess {
		t.Fatalf("telemetry sync --backfill failed: %d (%s)", code, stderr.String())
	}
	if commitBatches != 2 {
		t.Errorf("expected 2 Firestore commit batches on backfill, got %d", commitBatches)
	}
	if err := json.Unmarshal(stdout.Bytes(), &syncOut); err != nil {
		t.Fatalf("unmarshal backfill JSON failed: %v", err)
	}
	if syncOut.Status != "synced" || len(syncOut.Deltas) != 1 || syncOut.Deltas[0].Loads != 150 {
		t.Errorf("unexpected backfill output: %+v", syncOut)
	}
}
