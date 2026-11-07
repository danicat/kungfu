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
	"sort"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/config"
	"github.com/danicat/kungfu/internal/state"
	"github.com/danicat/kungfu/internal/telemetry"
)

func (a *App) resolveOfficialSkillCatalog(skillName string) (string, bool) {
	if skillName == "kungfu" {
		return "https://skills.danicat.dev", true
	}
	if a.Store == nil {
		return "", false
	}
	item, err := a.Store.Get(skillName)
	if err != nil || item == nil || item.Source == "local" {
		return "", false
	}
	isOfficialURL := strings.HasPrefix(item.URL, "https://skills.danicat.dev/") ||
		strings.HasPrefix(item.URL, "https://raw.githubusercontent.com/danicat/")
	isOfficialSource := item.Source == "remote" ||
		item.Source == "default" ||
		item.Source == "github:danicat/kungfu" ||
		strings.HasPrefix(item.Source, "https://skills.danicat.dev") ||
		(item.Source == "" && isOfficialURL)
	if !isOfficialSource || !isOfficialURL {
		return "", false
	}
	if item.Source == "github:danicat/kungfu" {
		return "github:danicat/kungfu", true
	}
	return "https://skills.danicat.dev", true
}

// RunTelemetry implements the 'kungfu telemetry' command family.
func (a *App) RunTelemetry(args []string) int {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" {
			_, _ = fmt.Fprintln(a.Stdout, `USAGE:
  kungfu telemetry [status|enable|disable|sync] [-b|--backfill] [--json] [--raw]

SUBCOMMANDS:
  status   Show telemetry status and pending official skill deltas (default).
  enable   Opt in to anonymous community usage counters.
  disable  Opt out of community usage counters.
  sync     Flush pending official skill counter deltas to Cloud Firestore (-b/--backfill replays full history).`)
			return ExitSuccess
		}
	}

	fs := flag.NewFlagSet("telemetry", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag, rawFlag, backfillFlag bool
	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.BoolVar(&backfillFlag, "backfill", false, "Reset sync watermarks and backfill full local history")
	fs.BoolVar(&backfillFlag, "b", false, "Reset sync watermarks and backfill full local history (shorthand)")

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
		return a.runTelemetrySync(mode, backfillFlag)
	default:
		_, _ = fmt.Fprintf(a.Stderr, "error: unknown telemetry subcommand %q. Expected status, enable, disable, or sync.\n", subcmd)
		return ExitUsage
	}
}

func (a *App) runTelemetryStatus(mode OutputMode) int {
	enabled := a.Config != nil && a.Config.IsTelemetryEnabled()
	cfgPath := config.ResolveConfigPath(a.Options.Config)

	st := a.loadState()
	deltas := st.ComputeTelemetryDeltas(a.resolveOfficialSkillCatalog)
	if deltas == nil {
		deltas = []state.TelemetryDelta{}
	}

	switch mode {
	case OutputModeJSON:
		out := struct {
			Enabled       bool                            `json:"enabled"`
			ConfigPath    string                          `json:"config_path"`
			PendingSkills int                             `json:"pending_skills"`
			PendingDeltas []state.TelemetryDelta          `json:"pending_deltas"`
			History       map[string]*state.HistoryRecord `json:"history,omitempty"`
		}{
			Enabled:       enabled,
			ConfigPath:    cfgPath,
			PendingSkills: len(deltas),
			PendingDeltas: deltas,
			History:       st.History,
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
				d.Skill, d.Catalog, d.Loads, d.PageLoads, d.SubpathLoads, d.Runs, d.Learns)
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
			_, _ = fmt.Fprintf(a.Stdout, "  %-24s %-28s %6s %6s %8s %6s %6s\n",
				"SKILL", "CATALOG", "LOADS", "PAGES", "SUBPATHS", "RUNS", "LEARNS")
			for _, d := range deltas {
				_, _ = fmt.Fprintf(a.Stdout, "  %-24s %-28s %6d %6d %8d %6d %6d\n",
					d.Skill, d.Catalog, d.Loads, d.PageLoads, d.SubpathLoads, d.Runs, d.Learns)
			}
		}

		var histNames []string
		for name, rec := range st.History {
			if rec != nil && (rec.LoadCount > 0 || rec.PageLoadCount > 0 || rec.SubpathLoadCount > 0 || rec.RunCount > 0 || rec.LearnCount > 0) {
				histNames = append(histNames, name)
			}
		}
		sort.Strings(histNames)
		if len(histNames) > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "\nRecorded:  %d skill(s) in immutable local history\n\n", len(histNames))
			_, _ = fmt.Fprintf(a.Stdout, "  %-24s %-28s %6s %6s %8s %6s %6s\n",
				"SKILL", "CATALOG", "LOADS", "PAGES", "SUBPATHS", "RUNS", "LEARNS")
			for _, name := range histNames {
				rec := st.History[name]
				cat := rec.Catalog
				if cat == "" {
					if resolvedCat, ok := a.resolveOfficialSkillCatalog(name); ok {
						cat = resolvedCat
					} else if a.Store != nil {
						if it, err := a.Store.Get(name); err == nil && it != nil {
							cat = normalizeItemCatalog(*it)
						}
					}
				}
				_, _ = fmt.Fprintf(a.Stdout, "  %-24s %-28s %6d %6d %8d %6d %6d\n",
					name, cat, rec.LoadCount, rec.PageLoadCount, rec.SubpathLoadCount, rec.RunCount, rec.LearnCount)
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

func (a *App) flushAllPendingTelemetry(ctx context.Context) ([]state.TelemetryDelta, error) {
	client := telemetry.NewClient(a.getHTTPClient())
	totalsBySkill := make(map[string]*state.TelemetryDelta)
	var skillOrder []string

	const maxBatches = 100
	for range maxBatches {
		st := a.loadState()
		deltas := st.ComputeTelemetryDeltas(a.resolveOfficialSkillCatalog)
		if len(deltas) == 0 {
			break
		}

		applied, err := client.Flush(ctx, deltas)
		if err != nil {
			return nil, err
		}
		if len(applied) == 0 {
			break
		}

		now := time.Now().UTC()
		if err := state.WithLockedState("", func(s *state.State) error {
			s.ApplySyncedDeltas(applied, now)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("updating synced telemetry watermarks in %s: %w", state.DefaultStateFilePath(), err)
		}

		for _, d := range applied {
			existing, ok := totalsBySkill[d.Skill]
			if !ok {
				cp := d
				totalsBySkill[d.Skill] = &cp
				skillOrder = append(skillOrder, d.Skill)
			} else {
				existing.Loads += d.Loads
				existing.PageLoads += d.PageLoads
				existing.SubpathLoads += d.SubpathLoads
				existing.Runs += d.Runs
				existing.Learns += d.Learns
			}
		}
	}

	var totalApplied []state.TelemetryDelta
	for _, skill := range skillOrder {
		totalApplied = append(totalApplied, *totalsBySkill[skill])
	}
	return totalApplied, nil
}

func (a *App) runTelemetrySync(mode OutputMode, backfill bool) int {
	if a.Config == nil || !a.Config.IsTelemetryEnabled() {
		if dnt := strings.ToLower(strings.TrimSpace(os.Getenv("DO_NOT_TRACK"))); dnt == "1" || dnt == "true" {
			_, _ = fmt.Fprintln(a.Stderr, "error: telemetry is disabled because DO_NOT_TRACK is set")
		} else {
			_, _ = fmt.Fprintln(a.Stderr, "error: telemetry is disabled; run 'kungfu telemetry enable' first")
		}
		return ExitGeneral
	}

	if backfill {
		if err := state.WithLockedState("", func(s *state.State) error {
			s.ResetTelemetryWatermarks()
			return nil
		}); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: resetting telemetry watermarks: %v\n", err)
			return ExitGeneral
		}
	}

	st := a.loadState()
	deltas := st.ComputeTelemetryDeltas(a.resolveOfficialSkillCatalog)
	if len(deltas) == 0 {
		return a.renderTelemetrySyncResult(mode, "up_to_date", nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	applied, err := a.flushAllPendingTelemetry(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return MapErrorToExitCode(err)
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
				d.Skill, d.Catalog, d.Loads, d.PageLoads, d.SubpathLoads, d.Runs, d.Learns)
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
// Piggybacked onto 'kungfu update'.
func (a *App) syncTelemetryIfEnabled(ctx context.Context, mode OutputMode) error {
	if a.Config == nil || !a.Config.IsTelemetryEnabled() {
		return nil
	}
	applied, err := a.flushAllPendingTelemetry(ctx)
	if err != nil {
		return err
	}

	if mode == OutputModeHuman && len(applied) > 0 {
		_, _ = fmt.Fprintf(a.Stdout, "✓ Synced telemetry counters (%d skills).\n", len(applied))
	}
	return nil
}
