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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/state"
)

func TestRunLoad(t *testing.T) {
	skillContent := "---\nname: kungfu\ndescription: agent skills gateway\n---\n# KungFu: Agent Skills Gateway\n"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SKILL.md") {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(skillContent))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "kungfu",
					"type": "skill-md",
					"description": "Agent skills gateway",
					"category": "agents",
					"url": "%s/skills/kungfu/SKILL.md",
					"digest": "%s"
				}
			]
		}`, server.URL, catalog.ComputeDigest([]byte(skillContent)))))
	}))
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"load missing arg", []string{"load"}, ExitUsage, ""},
		{"load kungfu raw default", []string{"load", "kungfu"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load kungfu with --local", []string{"load", "kungfu", "--local"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load kungfu with -u", []string{"load", "kungfu", "-u"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load kungfu with -f", []string{"load", "kungfu", "-f"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load multiple skills rejected", []string{"load", "kungfu", "godoctor"}, ExitUsage, ""},
		{"load skill json", []string{"load", "kungfu", "--json"}, ExitSuccess, "\"name\": \"kungfu\""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			if tt.wantSub != "" && !strings.Contains(stdout.String(), tt.wantSub) {
				t.Errorf("Run(%v) output %q does not contain %q; stderr: %s", tt.args, stdout.String(), tt.wantSub, stderr.String())
			}
		})
	}
}

func TestRunLoad_Resilience(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "local-skill")
	_ = os.MkdirAll(skillDir, 0755)
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: local-skill\n---\n# Local Skill Content"), 0644)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	if cat, ok := app.Store.(*catalog.Catalog); ok {
		cat.AddItem(catalog.Item{
			ID:       "local-skill",
			Name:     "local-skill",
			Category: "test",
			Path:     skillDir,
		})
	}

	// 1. Load with --local
	stdout.Reset()
	stderr.Reset()
	code := app.RunLoad([]string{"local-skill", "--local"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad(--local) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Local Skill Content") {
		t.Errorf("expected Local Skill Content, got: %s", stdout.String())
	}

	// 2. Load missing item with --local returns error
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"nonexistent-skill", "--local"})
	if code == ExitSuccess {
		t.Errorf("expected error when loading nonexistent-skill with --local, got success")
	}
}

func TestRunLoad_Subpath(t *testing.T) {
	tmpDir := t.TempDir()
	refsDir := filepath.Join(tmpDir, "references")
	scriptsDir := filepath.Join(tmpDir, "scripts")

	_ = os.MkdirAll(refsDir, 0755)
	_ = os.MkdirAll(scriptsDir, 0755)

	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte("---\nname: subskill\ncategory: test\n---\n# Root Playbook\nRoot body."), 0644)
	_ = os.WriteFile(filepath.Join(refsDir, "schema.md"), []byte("---\ntitle: Schema\n---\n# Database Schema\nSchema body content."), 0644)
	_ = os.WriteFile(filepath.Join(scriptsDir, "sync.py"), []byte("#!/usr/bin/env python3\nprint('syncing')\n"), 0755)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	if cat, ok := app.Store.(*catalog.Catalog); ok {
		cat.AddItem(catalog.Item{
			ID:       "subskill",
			Name:     "subskill",
			Category: "test",
			Path:     tmpDir,
			Body:     "---\nname: subskill\ncategory: test\n---\n# Root Playbook\nRoot body.",
		})
	}

	// 1. Load root skill
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"subskill"}); code != ExitSuccess {
		t.Fatalf("RunLoad(subskill) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Root Playbook") {
		t.Errorf("expected root playbook, got: %s", stdout.String())
	}

	// 2. Load subpath reference
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"subskill/references/schema.md"}); code != ExitSuccess {
		t.Fatalf("RunLoad(subskill/references/schema.md) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Database Schema") {
		t.Errorf("expected schema content, got: %s", stdout.String())
	}

	// 3. Load subpath script
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"subskill/scripts/sync.py"}); code != ExitSuccess {
		t.Fatalf("RunLoad(subskill/scripts/sync.py) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "print('syncing')") {
		t.Errorf("expected sync.py content, got: %s", stdout.String())
	}
}

func TestRunRun(t *testing.T) {
	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	_ = os.MkdirAll(scriptsDir, 0755)

	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte("---\nname: runskill\ncategory: test\n---\n# Run Skill"), 0644)
	_ = os.WriteFile(filepath.Join(scriptsDir, "echo.sh"), []byte("#!/bin/sh\necho \"kungfu-run-ok: $1\"\n"), 0755)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	if cat, ok := app.Store.(*catalog.Catalog); ok {
		cat.AddItem(catalog.Item{
			ID:       "runskill",
			Name:     "runskill",
			Category: "test",
			Path:     tmpDir,
			Body:     "---\nname: runskill\ncategory: test\n---\n# Run Skill",
		})
	}

	stdout.Reset()
	stderr.Reset()
	code := app.RunRun([]string{"runskill", "echo.sh", "hello"})
	if code != ExitSuccess {
		t.Fatalf("RunRun = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "kungfu-run-ok: hello") {
		t.Errorf("expected output from script, got: %s", stdout.String())
	}
}

func TestPaginateContent(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		pageSize  int
		wantPages int
	}{
		{"empty content", "", 100, 1},
		{"content under pageSize", "hello world", 100, 1},
		{"exact pageSize", "hello world", 11, 1},
		{"multiline split into two pages", "line 1\nline 2\nline 3\nline 4", 15, 2},
		{"huge single line chunks", strings.Repeat("A", 250), 100, 3},
		{"zero pageSize returns 1 page", "hello\nworld", 0, 1},
		{"negative pageSize returns 1 page", "hello\nworld", -1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pages := paginateContent(tt.content, tt.pageSize)
			if len(pages) != tt.wantPages {
				t.Fatalf("paginateContent() len = %d, want %d (pages: %v)", len(pages), tt.wantPages, pages)
			}
			for i, p := range pages {
				if tt.pageSize > 0 && len(p) > tt.pageSize {
					t.Errorf("page %d size %d exceeds pageSize %d", i, len(p), tt.pageSize)
				}
			}
		})
	}
}

func TestRunLoad_Pagination(t *testing.T) {
	skillContent := "---\nname: kungfu\ndescription: agent skills gateway\n---\n# KungFu: Agent Skills Gateway\n" + strings.Repeat("Long body line for pagination testing purposes.\n", 40)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SKILL.md") {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(skillContent))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "kungfu",
					"type": "skill-md",
					"description": "Agent skills gateway",
					"category": "agents",
					"url": "%s/skills/kungfu/SKILL.md",
					"digest": "%s"
				}
			]
		}`, server.URL, catalog.ComputeDigest([]byte(skillContent)))))
	}))
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. Load kungfu with page-size 500 (page 1)
	stdout.Reset()
	stderr.Reset()
	code := app.RunLoad([]string{"kungfu", "--page-size=500", "-p", "1"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad page 1 failed: %d, stderr: %s", code, stderr.String())
	}
	out1 := stdout.String()
	if !strings.Contains(out1, "[Page 1 of") || !strings.Contains(out1, "Next: kungfu load kungfu -p 2") {
		t.Errorf("expected page 1 footer with next page hint, got: %s", out1)
	}

	// 2. Load kungfu with page-size 500 (page 2)
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=500", "-p", "2"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad page 2 failed: %d, stderr: %s", code, stderr.String())
	}
	out2 := stdout.String()
	if !strings.Contains(out2, "[Page 2 of") || !strings.Contains(out2, "(continued)") {
		t.Errorf("expected page 2 header (continued), got: %s", out2)
	}

	// 3. Out of bounds page
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=500", "-p", "9999"})
	if code != ExitUsage {
		t.Fatalf("RunLoad out of bounds page expected ExitUsage (2), got: %d", code)
	}
	if !strings.Contains(stderr.String(), "out of bounds") {
		t.Errorf("expected out of bounds error, got: %s", stderr.String())
	}

	// 4. Invalid page < 1
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "-p", "0"})
	if code != ExitUsage {
		t.Fatalf("RunLoad invalid page 0 expected ExitUsage (2), got: %d", code)
	}
	if !strings.Contains(stderr.String(), "page must be >= 1") {
		t.Errorf("expected page must be >= 1 error, got: %s", stderr.String())
	}

	// 5. Invalid page-size <= 0 or > 7000
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=0"})
	if code != ExitUsage {
		t.Fatalf("RunLoad invalid page-size 0 expected ExitUsage (2), got: %d", code)
	}
	if !strings.Contains(stderr.String(), "page-size must be between 1 and 7000") {
		t.Errorf("expected page-size must be between 1 and 7000 error, got: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=7001"})
	if code != ExitUsage {
		t.Fatalf("RunLoad invalid page-size 7001 expected ExitUsage (2), got: %d", code)
	}
	if !strings.Contains(stderr.String(), "page-size must be between 1 and 7000") {
		t.Errorf("expected page-size must be between 1 and 7000 error, got: %s", stderr.String())
	}

	// 6. Conflicting --full and -p > 1
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--full", "-p", "2"})
	if code != ExitUsage {
		t.Fatalf("RunLoad --full with -p 2 expected ExitUsage (2), got: %d", code)
	}
	if !strings.Contains(stderr.String(), "cannot combine --full") {
		t.Errorf("expected cannot combine --full error, got: %s", stderr.String())
	}

	// 7. --full outputs unpaginated content without pagination framing
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=500", "--full"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad --full failed: %d, stderr: %s", code, stderr.String())
	}
	fullOut := stdout.String()
	if strings.Contains(fullOut, "[Page ") {
		t.Errorf("expected no pagination framing with --full, got: %s", fullOut)
	}
	if !strings.Contains(fullOut, "# KungFu: Agent Skills Gateway") {
		t.Errorf("expected full content, got: %s", fullOut)
	}

	// 8. Multiple skills rejected
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "godoctor"})
	if code != ExitUsage {
		t.Fatalf("RunLoad multiple skills expected ExitUsage (2), got: %d", code)
	}
	if !strings.Contains(stderr.String(), "loading multiple skills simultaneously is disabled") {
		t.Errorf("expected multiple skills error, got: %s", stderr.String())
	}

	// 9. JSON mode with pagination
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=500", "-p", "1", "--json"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad --json with pagination failed: %d, stderr: %s", code, stderr.String())
	}
	jsonOut := stdout.String()
	if !strings.Contains(jsonOut, `"page": 1`) || !strings.Contains(jsonOut, `"total_pages":`) {
		t.Errorf("expected json to contain page and total_pages, got: %s", jsonOut)
	}
}

func TestTelemetryRecording_LoadLearnRun(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("KUNGFU_STATE_PATH", stateFile)

	tmpDir := t.TempDir()
	refsDir := filepath.Join(tmpDir, "references")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	_ = os.MkdirAll(refsDir, 0755)
	_ = os.MkdirAll(scriptsDir, 0755)

	body := "---\nname: teleskill\ncategory: test\n---\n# Telemetry Skill\n" + strings.Repeat("Line of skill body for pagination.\n", 30)
	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte(body), 0644)
	_ = os.WriteFile(filepath.Join(refsDir, "guide.md"), []byte("# Guide Reference\nDetails."), 0644)
	_ = os.WriteFile(filepath.Join(scriptsDir, "ping.sh"), []byte("#!/bin/sh\necho pong\n"), 0755)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	if cat, ok := app.Store.(*catalog.Catalog); ok {
		cat.AddItem(catalog.Item{
			ID:       "teleskill",
			Name:     "teleskill",
			Category: "test",
			Path:     tmpDir,
			Body:     body,
		})
	}

	// 1. Root page 1 load -> increments LoadCount
	if code := app.RunLoad([]string{"teleskill", "--page-size=400", "-p", "1"}); code != ExitSuccess {
		t.Fatalf("RunLoad page 1 failed: %d (%s)", code, stderr.String())
	}
	// 2. Continuation page 2 load -> increments PageLoadCount, NOT LoadCount
	if code := app.RunLoad([]string{"teleskill", "--page-size=400", "-p", "2"}); code != ExitSuccess {
		t.Fatalf("RunLoad page 2 failed: %d (%s)", code, stderr.String())
	}
	// 3. Subpath load -> increments SubpathLoadCount, NOT LoadCount
	if code := app.RunLoad([]string{"teleskill/references/guide.md"}); code != ExitSuccess {
		t.Fatalf("RunLoad subpath failed: %d (%s)", code, stderr.String())
	}
	// 4. Script run -> increments RunCount
	if code := app.RunRun([]string{"teleskill", "ping.sh"}); code != ExitSuccess {
		t.Fatalf("RunRun failed: %d (%s)", code, stderr.String())
	}

	st, err := state.LoadState(stateFile)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	h, ok := st.History["teleskill"]
	if !ok {
		t.Fatalf("expected teleskill in state.History")
	}
	if h.LoadCount != 1 {
		t.Errorf("expected LoadCount=1, got %d", h.LoadCount)
	}
	if h.PageLoadCount != 1 {
		t.Errorf("expected PageLoadCount=1, got %d", h.PageLoadCount)
	}
	if h.SubpathLoadCount != 1 {
		t.Errorf("expected SubpathLoadCount=1, got %d", h.SubpathLoadCount)
	}
	if h.RunCount != 1 {
		t.Errorf("expected RunCount=1, got %d", h.RunCount)
	}
}
