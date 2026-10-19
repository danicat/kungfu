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

	// Seed state with 1 official catalog skill ("godoctor") and 1 third-party/custom skill ("private-skill")
	st := state.NewState()
	st.RecordLoadDetailed("godoctor", "", 1)
	st.RecordLoadDetailed("godoctor", "", 2)
	st.RecordRun("godoctor")
	st.RecordLearn("godoctor")
	st.RecordLoadDetailed("private-skill", "", 1)
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
			ID:       "godoctor",
			Name:     "godoctor",
			Category: "coding",
			Source:   "remote",
			URL:      "https://skills.danicat.dev/coding/godoctor/SKILL.md",
		})
		cat.AddItem(catalog.Item{
			ID:       "private-skill",
			Name:     "private-skill",
			Category: "coding",
			Source:   "github:acme/private",
			URL:      "https://raw.githubusercontent.com/acme/private/main/SKILL.md",
		})
	}

	// 1. Default status is disabled and only includes official catalog skill ("godoctor"), NOT "private-skill"
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
	if statusOut.PendingSkills != 1 || len(statusOut.PendingDeltas) != 1 {
		t.Fatalf("expected 1 pending official skill delta, got %d: %+v", statusOut.PendingSkills, statusOut.PendingDeltas)
	}
	d := statusOut.PendingDeltas[0]
	if d.Skill != "godoctor" || d.Category != "coding" || d.Loads != 1 || d.PageLoads != 1 || d.Runs != 1 || d.Learns != 1 {
		t.Errorf("unexpected delta for godoctor: %+v", d)
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

	// 3. 'enable' opts in and automatically baselines existing history so pending deltas become 0
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
	if statusOut.PendingSkills != 0 {
		t.Errorf("expected 0 pending skills after enable auto-baseline, got %d", statusOut.PendingSkills)
	}

	// 4. 'sync' when enabled and 0 pending deltas succeeds with up_to_date
	stdout.Reset()
	stderr.Reset()
	if code := app.RunTelemetry([]string{"sync", "--json"}); code != ExitSuccess {
		t.Fatalf("telemetry sync failed: %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "up_to_date"`) {
		t.Errorf("expected up_to_date JSON status, got: %s", stdout.String())
	}

	// 5. 'disable' turns telemetry off
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
