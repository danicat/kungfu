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
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/config"
	"github.com/danicat/kungfu/internal/state"
	"github.com/danicat/kungfu/internal/telemetry"
)

var validOfficialCategories = map[string]bool{
	"agents":    true,
	"analytics": true,
	"coding":    true,
	"game-dev":  true,
	"media":     true,
	"writing":   true,
	"standards": true,
}

func (a *App) resolveOfficialSkillCategory(skillName string) (string, bool) {
	if skillName == "kungfu" {
		return "agents", true
	}
	if a.Store == nil {
		return "", false
	}
	item, err := a.Store.Get(skillName)
	if err != nil || item == nil || item.Source == "local" {
		return "", false
	}
	cat := strings.ToLower(strings.TrimSpace(item.Category))
	if !validOfficialCategories[cat] {
		return "", false
	}
	isOfficialURL := strings.HasPrefix(item.URL, "https://skills.danicat.dev/") ||
		strings.HasPrefix(item.URL, "https://raw.githubusercontent.com/danicat/")
	isOfficialSource := item.Source == "remote" ||
		item.Source == "default" ||
		item.Source == "github:danicat/kungfu" ||
		strings.HasPrefix(item.Source, "https://skills.danicat.dev/") ||
		(item.Source == "" && isOfficialURL)
	if !isOfficialSource || !isOfficialURL {
		return "", false
	}
	return cat, true
}

// RunTelemetry implements the 'kungfu telemetry' command family.
func (a *App) RunTelemetry(args []string) int {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" {
			_, _ = fmt.Fprintln(a.Stdout, `USAGE:
  kungfu telemetry [status|enable|disable|sync] [--json] [--raw]

SUBCOMMANDS:
  status   Show telemetry status and pending official skill deltas (default).
  enable   Opt in to anonymous community usage counters (baselines existing history).
  disable  Opt out of community usage counters.
  sync     Flush pending official skill counter deltas to Cloud Firestore.`)
			return ExitSuccess
		}
	}

	fs := flag.NewFlagSet("telemetry", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag bool
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(posArgs) > 1 {
		_, _ = fmt.Fprintf(a.Stderr, "error: unexpected argument %q. Run 'kungfu telemetry --help' for usage.\n", posArgs[1])
		return ExitUsage
	}

	subcmd := "status"
	if len(posArgs) == 1 {
		subcmd = posArgs[0]
	}
	mode := a.resolveMode(jsonFlag, rawFlag)

	switch subcmd {
	case "status":
		return a.runTelemetryStatus(mode)
	case "enable":
		return a.runTelemetryToggle(mode, true)
	case "disable":
		return a.runTelemetryToggle(mode, false)
	case "sync":
		return a.runTelemetrySync(mode)
	default:
		_, _ = fmt.Fprintf(a.Stderr, "error: unknown telemetry subcommand %q. Expected status, enable, disable, or sync.\n", subcmd)
		return ExitUsage
	}
}

func (a *App) runTelemetryStatus(mode OutputMode) int {
	enabled := a.Config != nil && a.Config.IsTelemetryEnabled()
	cfgPath := config.ResolveConfigPath(a.Options.Config)

	st := a.loadState()
	deltas := st.ComputeTelemetryDeltas(a.resolveOfficialSkillCategory)
	if deltas == nil {
		deltas = []state.TelemetryDelta{}
	}

	switch mode {
	case OutputModeJSON:
		out := struct {
			Enabled       bool                   `json:"enabled"`
			ConfigPath    string                 `json:"config_path"`
			PendingSkills int                    `json:"pending_skills"`
			PendingDeltas []state.TelemetryDelta `json:"pending_deltas"`
		}{
			Enabled:       enabled,
			ConfigPath:    cfgPath,
			PendingSkills: len(deltas),
			PendingDeltas: deltas,
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	case OutputModeRaw:
		stateStr := "disabled"
		if enabled {
			stateStr = "enabled"
		}
		_, _ = fmt.Fprintf(a.Stdout, "status\t%s\t%s\t%d\n", stateStr, cfgPath, len(deltas))
		for _, d := range deltas {
			_, _ = fmt.Fprintf(a.Stdout, "delta\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
				d.Category, d.Skill, d.Loads, d.PageLoads, d.SubpathLoads, d.Runs, d.Learns)
		}
	default:
		stateLabel := "disabled"
		if enabled {
			stateLabel = "enabled"
		}
		_, _ = fmt.Fprintf(a.Stdout, "Telemetry: %s (%s)\n", stateLabel, cfgPath)
		if len(deltas) == 0 {
			_, _ = fmt.Fprintln(a.Stdout, "Pending:   none")
		} else {
			_, _ = fmt.Fprintf(a.Stdout, "Pending:   %d official catalog skill(s)\n\n", len(deltas))
			_, _ = fmt.Fprintf(a.Stdout, "  %-24s %-12s %6s %6s %8s %6s %6s\n",
				"SKILL", "CATEGORY", "LOADS", "PAGES", "SUBPATHS", "RUNS", "LEARNS")
			for _, d := range deltas {
				_, _ = fmt.Fprintf(a.Stdout, "  %-24s %-12s %6d %6d %8d %6d %6d\n",
					d.Skill, d.Category, d.Loads, d.PageLoads, d.SubpathLoads, d.Runs, d.Learns)
			}
		}
	}
	return ExitSuccess
}

func (a *App) runTelemetryToggle(mode OutputMode, enable bool) int {
	if a.Config == nil {
		a.Config = config.DefaultConfig()
	}
	a.Config.SetTelemetryEnabled(enable)

	savedPath, err := config.Save(a.Options.Config, a.Config)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: saving telemetry configuration: %v\n", err)
		return ExitGeneral
	}

	if enable {
		if err := state.WithLockedState("", func(s *state.State) error {
			s.BaselineTelemetryWatermarks(time.Now().UTC())
			return nil
		}); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: baselining telemetry watermarks: %v\n", err)
			return ExitGeneral
		}
	}

	switch mode {
	case OutputModeJSON:
		res := map[string]any{
			"enabled":     enable,
			"config_path": savedPath,
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	case OutputModeRaw:
		stateStr := "disabled"
		if enable {
			stateStr = "enabled"
		}
		_, _ = fmt.Fprintf(a.Stdout, "%s\t%s\n", stateStr, savedPath)
	default:
		if enable {
			_, _ = fmt.Fprintf(a.Stdout, "✓ Telemetry enabled (%s)\n", savedPath)
		} else {
			_, _ = fmt.Fprintf(a.Stdout, "✓ Telemetry disabled (%s)\n", savedPath)
		}
	}
	return ExitSuccess
}

func (a *App) runTelemetrySync(mode OutputMode) int {
	if a.Config == nil || !a.Config.IsTelemetryEnabled() {
		if dnt := strings.ToLower(strings.TrimSpace(os.Getenv("DO_NOT_TRACK"))); dnt == "1" || dnt == "true" {
			_, _ = fmt.Fprintln(a.Stderr, "error: telemetry is disabled because DO_NOT_TRACK is set")
		} else {
			_, _ = fmt.Fprintln(a.Stderr, "error: telemetry is disabled; run 'kungfu telemetry enable' first")
		}
		return ExitGeneral
	}

	st := a.loadState()
	deltas := st.ComputeTelemetryDeltas(a.resolveOfficialSkillCategory)
	if len(deltas) == 0 {
		return a.renderTelemetrySyncResult(mode, "up_to_date", nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := telemetry.NewClient(a.getHTTPClient())
	applied, err := client.Flush(ctx, deltas)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return MapErrorToExitCode(err)
	}

	now := time.Now().UTC()
	if err := state.WithLockedState("", func(s *state.State) error {
		s.ApplySyncedDeltas(applied, now)
		return nil
	}); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: updating synced telemetry watermarks in %s: %v\n", state.DefaultStateFilePath(), err)
		return ExitGeneral
	}

	return a.renderTelemetrySyncResult(mode, "synced", applied)
}

func (a *App) renderTelemetrySyncResult(mode OutputMode, status string, deltas []state.TelemetryDelta) int {
	if deltas == nil {
		deltas = []state.TelemetryDelta{}
	}
	switch mode {
	case OutputModeJSON:
		res := map[string]any{
			"status":        status,
			"synced_skills": len(deltas),
			"deltas":        deltas,
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	case OutputModeRaw:
		_, _ = fmt.Fprintf(a.Stdout, "%s\t%d\n", status, len(deltas))
		for _, d := range deltas {
			_, _ = fmt.Fprintf(a.Stdout, "delta\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
				d.Category, d.Skill, d.Loads, d.PageLoads, d.SubpathLoads, d.Runs, d.Learns)
		}
	default:
		if status == "up_to_date" {
			_, _ = fmt.Fprintln(a.Stdout, "✓ Telemetry up to date (0 pending skills).")
		} else {
			_, _ = fmt.Fprintf(a.Stdout, "✓ Synced telemetry counters (%d skills).\n", len(deltas))
		}
	}
	return ExitSuccess
}

// syncTelemetryIfEnabled flushes pending official catalog skill deltas when opt-in telemetry is enabled.
// Piggybacked onto 'kungfu catalog sync'.
func (a *App) syncTelemetryIfEnabled(ctx context.Context, mode OutputMode) error {
	if a.Config == nil || !a.Config.IsTelemetryEnabled() {
		return nil
	}
	st := a.loadState()
	deltas := st.ComputeTelemetryDeltas(a.resolveOfficialSkillCategory)
	if len(deltas) == 0 {
		return nil
	}

	client := telemetry.NewClient(a.getHTTPClient())
	applied, err := client.Flush(ctx, deltas)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if err := state.WithLockedState("", func(s *state.State) error {
		s.ApplySyncedDeltas(applied, now)
		return nil
	}); err != nil {
		return fmt.Errorf("telemetry: saving watermarks: %w", err)
	}

	if mode == OutputModeHuman && len(applied) > 0 {
		_, _ = fmt.Fprintf(a.Stdout, "✓ Synced telemetry counters (%d skills).\n", len(applied))
	}
	return nil
}
