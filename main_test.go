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

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/state"
)

func TestRun_VersionAndHelp(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"version long", []string{"--version"}, ExitSuccess, "kungfu version"},
		{"version short", []string{"-V"}, ExitSuccess, "kungfu version"},
		{"help long", []string{"--help"}, ExitSuccess, "USAGE:"},
		{"help short", []string{"-h"}, ExitSuccess, "USAGE:"},
		{"help command", []string{"help"}, ExitSuccess, "USAGE:"},
		{"no args", []string{}, ExitUsage, "USAGE:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, tt.wantSub) {
				t.Errorf("Run(%v) output %q does not contain %q", tt.args, out, tt.wantSub)
			}
		})
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"nonexistentcommand"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("Run(nonexistentcommand) = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("expected unknown command error message, got %q", stderr.String())
	}
}

func TestRun_List(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		checkOut func(t *testing.T, stdout string)
	}{
		{
			name:     "list default human",
			args:     []string{"list"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "NAME") || !strings.Contains(out, "VERSION") || !strings.Contains(out, "STATUS") {
					t.Errorf("list human missing headers: %s", out)
				}
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list human missing embedded kungfu item: %s", out)
				}
				if !strings.Contains(out, "kungfu load") || !strings.Contains(out, "kungfu learn") {
					t.Errorf("list human missing load/learn hint: %s", out)
				}
			},
		},
		{
			name:     "list json",
			args:     []string{"list", "--json"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				var payload struct {
					Categories []catalog.Category `json:"categories"`
					Items      []catalog.Item     `json:"items"`
					Total      int                `json:"total"`
				}
				if err := json.Unmarshal([]byte(out), &payload); err != nil {
					t.Fatalf("failed to parse json output: %v, out: %s", err, out)
				}
				if payload.Total == 0 || len(payload.Items) == 0 {
					t.Errorf("expected non-empty items in json list, got %d", payload.Total)
				}
				if payload.Items[0].Status != "installed" && payload.Items[0].Status != "online" {
					t.Errorf("expected status to be installed or online, got %q", payload.Items[0].Status)
				}
			},
		},
		{
			name:     "list raw flag",
			args:     []string{"list", "--raw"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "| Name | Version | Category | Tags | Status |") {
					t.Errorf("list raw missing table header: %s", out)
				}
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list raw missing items: %s", out)
				}
				if !strings.Contains(out, "kungfu load") || !strings.Contains(out, "kungfu learn") {
					t.Errorf("list raw missing load/learn hint: %s", out)
				}
			},
		},
		{
			name:     "list positional category",
			args:     []string{"list", "gateway"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list positional category missing item: %s", out)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			tt.checkOut(t, stdout.String())
		})
	}
}

func TestRun_Find(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		checkOut func(t *testing.T, stdout string)
	}{
		{
			name:     "find missing query and filter",
			args:     []string{"find"},
			wantCode: ExitUsage,
			checkOut: func(t *testing.T, out string) {},
		},
		{
			name:     "find hybrid search default human",
			args:     []string{"find", "gateway", "discovery"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "SEARCH RESULTS") {
					t.Errorf("expected SEARCH RESULTS in find results, got: %s", out)
				}
				if !strings.Contains(out, "kungfu") {
					t.Errorf("expected kungfu in find results, got: %s", out)
				}
			},
		},
		{
			name:     "find with tag shorthand and threshold shorthand",
			args:     []string{"find", "kungfu", "-t", "cli", "-T", "0.0"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("expected kungfu in find results with -t and -T, got: %s", out)
				}
			},
		},
		{
			name:     "find json output",
			args:     []string{"find", "kungfu", "--json"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				var res []struct {
					Item  catalog.Item `json:"item"`
					Score float32      `json:"score"`
				}
				if err := json.Unmarshal([]byte(out), &res); err != nil {
					t.Fatalf("failed to parse find json output: %v, out: %s", err, out)
				}
				if len(res) == 0 {
					t.Errorf("expected non-empty search results in json")
				}
			},
		},
		{
			name:     "find raw output",
			args:     []string{"find", "gateway", "--raw"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "# Search Results for") {
					t.Errorf("expected # Search Results for header in raw output, got: %s", out)
				}
				if !strings.Contains(out, "kungfu") {
					t.Errorf("expected kungfu in raw find output, got: %s", out)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			tt.checkOut(t, stdout.String())
		})
	}
}

func TestRun_Show(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"show missing arg", []string{"show"}, ExitUsage, ""},
		{"show single skill human default", []string{"show", "kungfu"}, ExitSuccess, "Name:"},
		{"show skill raw", []string{"show", "kungfu", "--raw"}, ExitSuccess, "# kungfu"},
		{"show skill json", []string{"show", "kungfu", "--json"}, ExitSuccess, "\"name\": \"kungfu\""},
		{"show with category flag", []string{"show", "-c", "gateway"}, ExitSuccess, "kungfu"},
		{"show with tags flag", []string{"show", "-t", "agent-skills"}, ExitSuccess, "kungfu"},
		{"show all flag", []string{"show", "--all"}, ExitSuccess, "kungfu"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			if tt.wantSub != "" && !strings.Contains(stdout.String(), tt.wantSub) {
				t.Errorf("Run(%v) output %q does not contain %q", tt.args, stdout.String(), tt.wantSub)
			}
		})
	}
}

func TestRun_DidYouMeanSuggestions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "godoctor",
					"type": "skill-md",
					"description": "Go doctor",
					"category": "coding",
					"url": "https://skills.danicat.dev/coding/godoctor/SKILL.md",
					"digest": "sha256:123456"
				},
				{
					"name": "tech-writer",
					"type": "skill-md",
					"description": "Tech writer",
					"category": "writing",
					"url": "https://skills.danicat.dev/writing/tech-writer/SKILL.md",
					"digest": "sha256:234567"
				},
				{
					"name": "ebitengineer",
					"type": "skill-md",
					"description": "Ebitengine game dev",
					"category": "game-dev",
					"url": "https://skills.danicat.dev/game-dev/ebitengineer/SKILL.md",
					"digest": "sha256:345678"
				}
			]
		}`))
	}))
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{
			name:       "typo kungfuu suggests kungfu",
			args:       []string{"load", "kungfuu"},
			wantCode:   ExitNotFound,
			wantStderr: "kungfu load kungfu",
		},
		{
			name:       "prefix godoc suggests godoctor",
			args:       []string{"load", "godoc"},
			wantCode:   ExitNotFound,
			wantStderr: "kungfu load godoctor",
		},
		{
			name:       "show category name suggests --category",
			args:       []string{"show", "gateway"},
			wantCode:   ExitNotFound,
			wantStderr: "kungfu show --category gateway",
		},
		{
			name:       "learn typo kungfuu suggests kungfu",
			args:       []string{"learn", "kungfuu"},
			wantCode:   ExitNotFound,
			wantStderr: "kungfu learn kungfu",
		},
		{
			name:       "load category typo writings suggests writing",
			args:       []string{"load", "-c", "writings"},
			wantCode:   ExitNotFound,
			wantStderr: "kungfu load --category writing",
		},
		{
			name:       "show category typo gamedev suggests game-dev",
			args:       []string{"show", "-c", "gamedev"},
			wantCode:   ExitNotFound,
			wantStderr: "kungfu show --category game-dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("Run(%v) stderr %q does not contain %q", tt.args, stderr.String(), tt.wantStderr)
			}
			if !strings.Contains(stderr.String(), "Did you mean:") {
				t.Errorf("Run(%v) stderr missing 'Did you mean:' header: %s", tt.args, stderr.String())
			}
		})
	}
}

func TestRun_Load(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"load missing arg", []string{"load"}, ExitUsage, ""},
		{"load embedded kungfu raw default", []string{"load", "kungfu"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load by category flag", []string{"load", "-c", "gateway"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load by tags flag boolean AND", []string{"load", "-t", "agent-skills AND discovery"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load by tags flag boolean OR", []string{"load", "-t", "cli OR search"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load all flag", []string{"load", "--all"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
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
				t.Errorf("Run(%v) output %q does not contain %q", tt.args, stdout.String(), tt.wantSub)
			}
			if tt.wantCode == ExitSuccess && tt.name != "load skill json" {
				if strings.HasPrefix(stdout.String(), "---") {
					t.Errorf("Run(%v) output unexpectedly starts with frontmatter delimiter", tt.args)
				}
			}
			if tt.name == "load embedded kungfu raw default" {
				if strings.Contains(stdout.String(), "license: Apache-2.0") {
					t.Errorf("Run(%v) output contains frontmatter metadata: %s", tt.args, stdout.String())
				}
			}
		})
	}
}

func TestRun_Load_Subpath(t *testing.T) {
	tmpDir := t.TempDir()
	refsDir := filepath.Join(tmpDir, "references")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	assetsDir := filepath.Join(tmpDir, "assets")

	_ = os.MkdirAll(refsDir, 0755)
	_ = os.MkdirAll(scriptsDir, 0755)
	_ = os.MkdirAll(assetsDir, 0755)

	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte("---\nname: pkgskill\ncategory: test\n---\n# Root Playbook\nRoot body."), 0644)
	_ = os.WriteFile(filepath.Join(refsDir, "schema.md"), []byte("---\ntitle: Schema\n---\n# Database Schema\nSchema body content."), 0644)
	_ = os.WriteFile(filepath.Join(scriptsDir, "sync.py"), []byte("#!/usr/bin/env python3\nprint('syncing')\n"), 0755)
	_ = os.WriteFile(filepath.Join(assetsDir, "init.sql"), []byte("CREATE TABLE users (id INT);"), 0644)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	if cat, ok := app.Store.(*catalog.Catalog); ok {
		cat.AddItem(catalog.Item{
			ID:       "pkgskill",
			Name:     "pkgskill",
			Category: "test",
			Path:     tmpDir,
			Body:     "---\nname: pkgskill\ncategory: test\n---\n# Root Playbook\nRoot body.",
		})
	}

	// 1. Load root skill via standard name
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"pkgskill"}); code != ExitSuccess {
		t.Fatalf("RunLoad(pkgskill) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Root body.") {
		t.Errorf("expected root body in output, got: %s", stdout.String())
	}

	// 2. Load reference via subpath URI: pkgskill/references/schema.md
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"pkgskill/references/schema.md"}); code != ExitSuccess {
		t.Fatalf("RunLoad(pkgskill/references/schema.md) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Schema body content.") {
		t.Errorf("expected schema content in output, got: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "title: Schema") {
		t.Errorf("expected frontmatter to be stripped, got: %s", stdout.String())
	}

	// 3. Load script via subpath URI: pkgskill/scripts/sync.py
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"pkgskill/scripts/sync.py", "--raw"}); code != ExitSuccess {
		t.Fatalf("RunLoad(pkgskill/scripts/sync.py --raw) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "print('syncing')") {
		t.Errorf("expected script content in output, got: %s", stdout.String())
	}

	// 4. Load asset via 2 positional arguments: pkgskill assets/init.sql
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"pkgskill", "assets/init.sql", "--json"}); code != ExitSuccess {
		t.Fatalf("RunLoad(pkgskill assets/init.sql --json) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "CREATE TABLE users") {
		t.Errorf("expected SQL content in json, got: %s", stdout.String())
	}

	// 5. Load nonexistent file with fuzzy suggestion
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"pkgskill/references/schem.md"}); code != ExitNotFound {
		t.Fatalf("RunLoad nonexistent = %d, want %d", code, ExitNotFound)
	}
	if !strings.Contains(stderr.String(), "Did you mean:") {
		t.Errorf("expected fuzzy suggestions on typo, got stderr: %s", stderr.String())
	}

	// 6. Security path traversal attempt
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"pkgskill/../secret"}); code != ExitUsage {
		t.Fatalf("RunLoad traversal = %d, want %d", code, ExitUsage)
	}

	// 7. Binary file rejection
	_ = os.WriteFile(filepath.Join(tmpDir, "logo.png"), []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x01}, 0644)
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"pkgskill/logo.png"}); code != ExitUsage {
		t.Fatalf("RunLoad binary file = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "cannot stream binary file") {
		t.Errorf("expected binary file rejection message, got: %s", stderr.String())
	}
}

func TestRun_Learn(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// 1. Learn embedded kungfu skill into current workspace (.agents/skills/kungfu) - default no load
	var stdout, stderr bytes.Buffer
	code := Run([]string{"learn", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn kungfu) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Learned skill \"kungfu\"") {
		t.Errorf("unexpected learn output: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "KungFu: Agent Skills Gateway") {
		t.Errorf("expected skill body NOT to be loaded in stdout by default: %s", stdout.String())
	}

	installedSkill := filepath.Join(tmpDir, ".agents", "skills", "kungfu", "SKILL.md")
	data, err := os.ReadFile(installedSkill)
	if err != nil {
		t.Fatalf("failed to read installed SKILL.md: %v", err)
	}
	if !strings.Contains(string(data), "name: kungfu") {
		t.Errorf("installed SKILL.md content mismatch (missing frontmatter on disk): %s", string(data))
	}

	// 2. Learn again without force (should skip, no body by default)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"learn", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn kungfu duplicate) = %d, want %d", code, ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "Skipped skill") {
		t.Errorf("expected skip message, got: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "KungFu: Agent Skills Gateway") {
		t.Errorf("expected skill body NOT to be in stdout on skip: %s", stdout.String())
	}

	// 3. Learn with force and --load (should overwrite and stream body without frontmatter)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"learn", "--force", "--load", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn --force --load kungfu) = %d, want %d", code, ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "Learned skill \"kungfu\"") {
		t.Errorf("expected learned message with force, got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "KungFu: Agent Skills Gateway") {
		t.Errorf("expected skill body in stdout when --load is specified: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "license: Apache-2.0") {
		t.Errorf("expected frontmatter NOT to be streamed in stdout: %s", stdout.String())
	}

	// 4. Learn with shorthand -l
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"learn", "-f", "-l", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn -f -l kungfu) = %d, want %d", code, ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "KungFu: Agent Skills Gateway") {
		t.Errorf("expected skill body in stdout when -l is specified: %s", stdout.String())
	}

	// 5. Learn with JSON output without --load
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"learn", "--force", "--json", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn --json) = %d, want %d", code, ExitSuccess)
	}
	var resultsNoLoad []struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Status  string `json:"status"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resultsNoLoad); err != nil {
		t.Fatalf("failed to parse json learn result: %v", err)
	}
	if len(resultsNoLoad) != 1 || resultsNoLoad[0].Status != "installed" || resultsNoLoad[0].Content != "" {
		t.Errorf("unexpected json learn results without --load: %+v", resultsNoLoad)
	}

	// 6. Learn with JSON output with --load
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"learn", "--force", "--json", "--load", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn --json --load) = %d, want %d", code, ExitSuccess)
	}
	var resultsWithLoad []struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Status  string `json:"status"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resultsWithLoad); err != nil {
		t.Fatalf("failed to parse json learn --load result: %v", err)
	}
	if len(resultsWithLoad) != 1 || resultsWithLoad[0].Status != "installed" || !strings.Contains(resultsWithLoad[0].Content, "KungFu: Agent Skills Gateway") {
		t.Errorf("unexpected json learn results with --load: %+v", resultsWithLoad)
	}
	if strings.Contains(resultsWithLoad[0].Content, "license: Apache-2.0") {
		t.Errorf("expected no frontmatter in json content: %s", resultsWithLoad[0].Content)
	}
}

func TestRun_Learn_With_Companion_Files(t *testing.T) {
	multiDocContent := []byte("---\nname: multi-doc-skill\ndescription: Multi doc skill\n---\n# Multi Doc Skill\nRead [references/api.md](references/api.md) and [scripts/build.sh](scripts/build.sh)")
	digest := catalog.ComputeDigest(multiDocContent)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog.json", "/.well-known/agent-skills/index.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{
				"skills": [
					{
						"name": "multi-doc-skill",
						"description": "Multi doc skill",
						"type": "skill-md",
						"url": "http://%s/testing/multi-doc-skill/SKILL.md",
						"digest": "%s"
					}
				]
			}`, r.Host, digest)))
		case "/testing/multi-doc-skill/SKILL.md":
			w.Write(multiDocContent)
		case "/testing/multi-doc-skill/references/api.md":
			w.Write([]byte("# API Reference Spec"))
		case "/testing/multi-doc-skill/scripts/build.sh":
			w.Write([]byte("#!/bin/sh\necho built"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	cat, err := catalog.NewCatalog(catalog.WithRemoteURL(server.URL))
	if err != nil {
		t.Fatalf("catalog init failed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app.Store = cat

	code := app.RunLearn([]string{"multi-doc-skill", "--force"})
	if code != ExitSuccess {
		t.Fatalf("RunLearn failed with %d: %s", code, stderr.String())
	}

	skillDir := filepath.Join(tmpDir, ".agents", "skills", "multi-doc-skill")
	skillMD := filepath.Join(skillDir, "SKILL.md")
	refMD := filepath.Join(skillDir, "references", "api.md")
	scriptSH := filepath.Join(skillDir, "scripts", "build.sh")

	if _, err := os.Stat(skillMD); err != nil {
		t.Errorf("expected SKILL.md to exist: %v", err)
	}
	if _, err := os.Stat(refMD); err != nil {
		t.Errorf("expected references/api.md to exist: %v", err)
	}
	if info, err := os.Stat(scriptSH); err != nil {
		t.Errorf("expected scripts/build.sh to exist: %v", err)
	} else if info.Mode()&0111 == 0 {
		t.Errorf("expected scripts/build.sh to be executable, mode: %v", info.Mode())
	}
}

func TestRun_Run(t *testing.T) {
	t.Run("run missing args", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"run"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("Run(run) = %d, want %d", code, ExitUsage)
		}
	})

	t.Run("run JIT remote script execution without learning", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/catalog.json":
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"version":"1.0","skills":[{"name":"remote-runner-skill","url":"http://` + r.Host + `/skills/remote-runner-skill/SKILL.md","description":"Runner test"}]}`))
			case "/skills/remote-runner-skill/SKILL.md":
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("---\nname: remote-runner-skill\ndescription: Runner test\n---\n# Runner\nRun `scripts/action.sh`"))
			case "/skills/remote-runner-skill/scripts/action.sh":
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("#!/bin/sh\necho \"JIT_RUNNER_OUTPUT $1\"\n"))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		cat, err := catalog.NewCatalog(catalog.WithRemoteURL(server.URL + "/catalog.json"))
		if err != nil {
			t.Fatalf("catalog init failed: %v", err)
		}

		var stdout, stderr bytes.Buffer
		app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("NewApp failed: %v", err)
		}
		app.Store = cat

		code := app.RunRun([]string{"remote-runner-skill", "action.sh", "matrix"})
		if code != ExitSuccess {
			t.Fatalf("RunRun failed with code %d: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "JIT_RUNNER_OUTPUT matrix") {
			t.Errorf("expected output to contain JIT_RUNNER_OUTPUT matrix, got: %s", stdout.String())
		}
	})
}

func TestMapErrorToExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil error", nil, ExitSuccess},
		{"not found sentinel", catalog.ErrItemNotFound, ExitNotFound},
		{"digest mismatch sentinel", catalog.ErrDigestMismatch, ExitIntegrity},
		{"invalid frontmatter sentinel", catalog.ErrInvalidFrontmatter, ExitIntegrity},
		{"generic error", errors.New("random failure"), ExitGeneral},
		{"network error string", errors.New("connection refused: network timeout"), ExitNetwork},
		{"net.OpError", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("timed out")}, ExitNetwork},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapErrorToExitCode(tt.err)
			if got != tt.want {
				t.Errorf("mapErrorToExitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestScriptAndReferenceHelpers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kungfu-test-skill-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	scriptsDir := filepath.Join(tmpDir, "scripts")
	_ = os.MkdirAll(scriptsDir, 0755)
	refsDir := filepath.Join(tmpDir, "references")
	_ = os.MkdirAll(refsDir, 0755)

	scriptFile := filepath.Join(scriptsDir, "echo.sh")
	_ = os.WriteFile(scriptFile, []byte("#!/bin/bash\necho test-output $1\n"), 0755)

	refFile := filepath.Join(refsDir, "guide.md")
	_ = os.WriteFile(refFile, []byte("# Skill Guide\nSample documentation.\n"), 0644)

	item := catalog.Item{
		ID:   "test-skill",
		Name: "test-skill",
		Path: tmpDir,
	}

	refContent, err := runner.ReadFile(item, "references/guide.md")
	if err != nil {
		t.Fatalf("runner.ReadFile failed: %v", err)
	}
	if !strings.Contains(refContent, "Skill Guide") {
		t.Errorf("expected Skill Guide in ref content, got: %s", refContent)
	}

	var scriptOut, scriptErr bytes.Buffer
	err = runner.RunScript(item, "echo.sh", []string{"hello"}, &scriptOut, &scriptErr)
	if err != nil {
		t.Fatalf("runner.RunScript failed: %v", err)
	}
	if !strings.Contains(scriptOut.String(), "test-output hello") {
		t.Errorf("expected test-output hello in script stdout, got: %s", scriptOut.String())
	}
}

func TestKungFuLiveWorkflow(t *testing.T) {
	// Step 1: List Categories & Items
	var listOut, listErr bytes.Buffer
	if code := Run([]string{"list"}, &listOut, &listErr); code != ExitSuccess {
		t.Fatalf("list failed with code %d: %s", code, listErr.String())
	}
	t.Logf("=== kungfu list output ===\n%s", listOut.String())

	// Step 2: Find with Hybrid Search
	var findOut, findErr bytes.Buffer
	if code := Run([]string{"find", "agent-skills", "gateway"}, &findOut, &findErr); code != ExitSuccess {
		t.Fatalf("find failed with code %d: %s", code, findErr.String())
	}
	t.Logf("=== kungfu find output ===\n%s", findOut.String())

	// Step 3: Show Skill Metadata
	var showOut, showErr bytes.Buffer
	if code := Run([]string{"show", "kungfu"}, &showOut, &showErr); code != ExitSuccess {
		t.Fatalf("show failed with code %d: %s", code, showErr.String())
	}
	t.Logf("=== kungfu show kungfu output ===\n%s", showOut.String())

	// Step 4: Load Verbatim Raw Markdown
	var loadOut, loadErr bytes.Buffer
	if code := Run([]string{"load", "kungfu"}, &loadOut, &loadErr); code != ExitSuccess {
		t.Fatalf("load failed with code %d: %s", code, loadErr.String())
	}
	t.Logf("=== kungfu load kungfu output ===\n%s", loadOut.String())
}

func TestRun_Learn_Global(t *testing.T) {
	origHome := os.Getenv("HOME")
	tempHome := t.TempDir()
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", origHome)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"learn", "kungfu", "-g", "--json"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("learn -g failed with code %d: %s", code, stderr.String())
	}

	expectedSkill := filepath.Join(tempHome, ".agents", "skills", "kungfu", "SKILL.md")
	if _, err := os.Stat(expectedSkill); err != nil {
		t.Errorf("expected global skill file at %s, got error: %v", expectedSkill, err)
	}
}

func TestRun_State_Lifecycle(t *testing.T) {
	// Setup isolated test environment with custom HOME and Working Dir
	tempHome := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", origHome)

	tempDir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(origWd)

	var server *httptest.Server
	var catalogJSON string
	var tool1Content []byte
	tool1Content = []byte("---\nname: my-tool\ndescription: A test tool\nmetadata:\n  version: 1.0.0\n---\n# My Tool v1.0.0 Content")
	tool1Digest := catalog.ComputeDigest(tool1Content)
	unversionedContent := []byte("---\nname: unversioned-tool\ndescription: Unversioned\n---\n# Unversioned Content")
	unversionedDigest := catalog.ComputeDigest(unversionedContent)

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog.json", "/.well-known/agent-skills/index.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(catalogJSON))
		case "/skills/my-tool/SKILL.md":
			w.Write(tool1Content)
		case "/skills/unversioned-tool/SKILL.md":
			w.Write(unversionedContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	catalogJSON = fmt.Sprintf(`{
		"skills": [
			{
				"name": "my-tool",
				"type": "skill-md",
				"description": "A test tool",
				"url": "%%s/skills/my-tool/SKILL.md",
				"digest": "%s"
			},
			{
				"name": "unversioned-tool",
				"type": "skill-md",
				"description": "Unversioned",
				"url": "%%s/skills/unversioned-tool/SKILL.md",
				"digest": "%s"
			}
		]
	}`, tool1Digest, unversionedDigest)
	catalogJSON = fmt.Sprintf(catalogJSON, server.URL, server.URL)

	t.Setenv("KUNGFU_URL", server.URL)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. Learn skill in workspace
	code := app.RunLearn([]string{"my-tool"})
	if code != ExitSuccess {
		t.Fatalf("RunLearn failed: code %d, err: %s", code, stderr.String())
	}

	// Verify state file was created
	statePath := filepath.Join(tempHome, ".config", "kungfu", "state.json")
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Fatalf("state.json was not created at %s", statePath)
	}

	st, err := state.LoadState(statePath)
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}

	actualWd, _ := os.Getwd()

	installed := st.GetInstalledSkills(state.ScopeWorkspace, actualWd)
	rec, ok := installed["my-tool"]
	if !ok || rec.Status != "installed" || rec.Version != "1.0.0" {
		t.Fatalf("expected my-tool installed with version 1.0.0, got: %+v", rec)
	}

	// 2. Run JIT Load and verify history recording
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"unversioned-tool"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad failed: code %d, err: %s", code, stderr.String())
	}

	st, _ = state.LoadState(statePath)
	if h, ok := st.History["unversioned-tool"]; !ok || h.LoadCount != 1 {
		t.Fatalf("expected unversioned-tool in history with load count 1, got: %+v", h)
	}

	// 3. Run Status
	stdout.Reset()
	stderr.Reset()
	code = app.RunStatus([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunStatus failed: code %d, err: %s", code, stderr.String())
	}
	statusOut := stdout.String()
	if !strings.Contains(statusOut, "my-tool") || !strings.Contains(statusOut, "1.0.0") {
		t.Fatalf("status output missing my-tool: %s", statusOut)
	}
	if !strings.Contains(statusOut, "unversioned-tool") || !strings.Contains(statusOut, "loaded 1 times") {
		t.Fatalf("status output missing JIT load history: %s", statusOut)
	}

	// Status --json
	stdout.Reset()
	stderr.Reset()
	code = app.RunStatus([]string{"--json"})
	if code != ExitSuccess {
		t.Fatalf("RunStatus --json failed: code %d, err: %s", code, stderr.String())
	}
	var statusJSON map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &statusJSON); err != nil {
		t.Fatalf("failed to unmarshal status JSON: %v", err)
	}
	if statusJSON["workspace"] != actualWd {
		t.Fatalf("unexpected workspace in status JSON: %v", statusJSON["workspace"])
	}

	// 4. Update check (dry-run when up to date)
	stdout.Reset()
	stderr.Reset()
	code = app.RunUpdate([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunUpdate dry-run failed: code %d, err: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "All installed skills are up to date.") {
		t.Fatalf("expected up to date in dry-run, got: %s", stdout.String())
	}

	// 5. Update with new remote version available
	tool1Content = []byte("---\nname: my-tool\ndescription: A test tool\nmetadata:\n  version: 1.1.0\n---\n# My Tool v1.1.0 Content")
	tool1Digest = catalog.ComputeDigest(tool1Content)
	catalogJSON = fmt.Sprintf(`{
		"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
		"skills": [
			{
				"name": "my-tool",
				"type": "skill-md",
				"description": "A test tool",
				"url": "%%s/skills/my-tool/SKILL.md",
				"digest": "%s"
			}
		]
	}`, tool1Digest)
	catalogJSON = fmt.Sprintf(catalogJSON, server.URL)

	// Force refresh catalog store
	refreshedStore, err := catalog.NewCatalog(
		catalog.WithRemoteSync(true),
		catalog.WithForceRefresh(),
	)
	if err != nil {
		t.Fatalf("failed to refresh store: %v", err)
	}
	app.Store = refreshedStore

	// Dry run with update available
	stdout.Reset()
	stderr.Reset()
	code = app.RunUpdate([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunUpdate dry-run failed: code %d, err: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "new version available: 1.0.0 -> 1.1.0") {
		t.Fatalf("expected update available message, got: %s", stdout.String())
	}

	// Apply update
	stdout.Reset()
	stderr.Reset()
	code = app.RunUpdate([]string{"my-tool"})
	if code != ExitSuccess {
		t.Fatalf("RunUpdate apply failed: code %d, err: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Updated skill \"my-tool\"") {
		t.Fatalf("expected updated skill confirmation, got: %s", stdout.String())
	}

	// Verify state updated to 1.1.0
	st, _ = state.LoadState(statePath)
	installed = st.GetInstalledSkills(state.ScopeWorkspace, actualWd)
	if installed["my-tool"].Version != "1.1.0" {
		t.Fatalf("expected state version updated to 1.1.0, got: %s", installed["my-tool"].Version)
	}

	// 6. Forget skill
	stdout.Reset()
	stderr.Reset()
	code = app.RunForget([]string{"my-tool"})
	if code != ExitSuccess {
		t.Fatalf("RunForget failed: code %d, err: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Forgot skill \"my-tool\"") {
		t.Fatalf("expected forgot confirmation, got: %s", stdout.String())
	}

	// Verify files deleted from disk
	skillDir := filepath.Join(actualWd, ".agents", "skills", "my-tool")
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Fatalf("expected skill directory removed from disk at %s", skillDir)
	}

	// Verify state marked soft-deleted
	st, _ = state.LoadState(statePath)
	wsRec := st.Workspaces[actualWd].Skills["my-tool"]
	if wsRec.Status != "uninstalled" || wsRec.UninstalledAt == nil {
		t.Fatalf("expected soft-delete record in state, got: %+v", wsRec)
	}

	// Forgotten list in status
	stdout.Reset()
	stderr.Reset()
	code = app.RunStatus([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunStatus after forget failed: code %d, err: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Forgotten Skills:") || !strings.Contains(stdout.String(), "my-tool") {
		t.Fatalf("status output missing forgotten skills: %s", stdout.String())
	}

	// 7. Test Custom Skill Protection
	customSkillDir := filepath.Join(actualWd, ".agents", "skills", "proprietary-tool")
	if err := os.MkdirAll(customSkillDir, 0755); err != nil {
		t.Fatalf("failed to create custom skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(customSkillDir, "SKILL.md"), []byte("---\nname: proprietary-tool\nversion: 0.1.0\n---\n# Proprietary"), 0644); err != nil {
		t.Fatalf("failed to write custom skill file: %v", err)
	}

	// Run status to trigger reconciliation and verify display
	stdout.Reset()
	stderr.Reset()
	code = app.RunStatus([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunStatus with custom skill failed: code %d, err: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "proprietary-tool") || !strings.Contains(stdout.String(), "custom - read-only to kungfu") {
		t.Fatalf("expected custom skill status output, got: %s", stdout.String())
	}

	// Attempt to forget custom skill by name -> must be rejected
	stdout.Reset()
	stderr.Reset()
	code = app.RunForget([]string{"proprietary-tool"})
	if code == ExitSuccess {
		t.Fatalf("expected RunForget on custom skill to fail, but got success")
	}
	if !strings.Contains(stderr.String(), "custom/external skill") {
		t.Fatalf("expected custom skill error message, got stderr: %s", stderr.String())
	}

	// Verify custom skill is still intact on disk
	if _, err := os.Stat(filepath.Join(customSkillDir, "SKILL.md")); os.IsNotExist(err) {
		t.Fatalf("custom skill was deleted from disk when it should be protected")
	}

	// Run forget --all -> must skip custom skill
	stdout.Reset()
	stderr.Reset()
	code = app.RunForget([]string{"--all"})
	if code != ExitSuccess {
		t.Fatalf("RunForget --all failed: code %d, err: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(customSkillDir, "SKILL.md")); os.IsNotExist(err) {
		t.Fatalf("custom skill was deleted by forget --all when it should be preserved")
	}
}

func TestRun_SubcommandSuggestions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"statuss"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("Run(statuss) = %d, want %d", code, ExitUsage)
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "Did you mean:") || !strings.Contains(errOut, "kungfu status") {
		t.Errorf("expected suggestion for status, got: %s", errOut)
	}
}

func TestRun_NegativeFlagsAndDelimiter(t *testing.T) {
	// Test -- delimiter
	var stdout, stderr bytes.Buffer
	code := Run([]string{"find", "--", "-weirdname"}, &stdout, &stderr)
	// -weirdname is treated as a positional search keyword, not an invalid flag
	if code != ExitSuccess {
		t.Fatalf("Run(find -- -weirdname) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}

	// Test negative threshold argument -T -0.5
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"find", "kungfu", "-T", "-0.5"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(find kungfu -T -0.5) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
}

func TestRun_TagAliases(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// Test list --tag
	code := Run([]string{"list", "--tag", "cli"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(list --tag cli) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "kungfu") {
		t.Errorf("expected kungfu in list --tag cli output: %s", stdout.String())
	}

	// Test show --tag
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"show", "--tag", "cli"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(show --tag cli) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "kungfu") {
		t.Errorf("expected kungfu in show --tag cli output: %s", stdout.String())
	}
}

func TestRun_RawAndExitCodes(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// Status --raw
	var stdout, stderr bytes.Buffer
	code := Run([]string{"status", "--raw"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(status --raw) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}

	// Forget non-existent skill -> ExitNotFound (3)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"forget", "nonexistent-skill-xyz"}, &stdout, &stderr)
	if code != ExitNotFound {
		t.Fatalf("Run(forget nonexistent) = %d, want %d (%s); stderr: %s", code, ExitNotFound, "ExitNotFound", stderr.String())
	}

	// Update non-existent skill -> ExitNotFound (3)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"update", "nonexistent-skill-xyz"}, &stdout, &stderr)
	if code != ExitNotFound {
		t.Fatalf("Run(update nonexistent) = %d, want %d (%s); stderr: %s", code, ExitNotFound, "ExitNotFound", stderr.String())
	}
}

func TestRun_Status_GlobalFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"status", "-g"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(status -g) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "Workspace Skills") {
		t.Errorf("expected status -g to omit Workspace Skills section, got: %s", out)
	}
	if !strings.Contains(out, "Global Skills") {
		t.Errorf("expected status -g to contain Global Skills section, got: %s", out)
	}
}

func TestRun_VersionSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(version) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "kungfu version") {
		t.Errorf("expected version output, got: %s", stdout.String())
	}
}

func TestRun_Status_CategoryAndTagFiltering(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// status -c gateway
	code := Run([]string{"status", "-c", "gateway"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(status -c gateway) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}

	// status -c nonexistent
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"status", "-c", "nonexistent-category-xyz"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(status -c nonexistent) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Workspace Skills (0):") && !strings.Contains(stdout.String(), "Global Skills (0):") {
		t.Errorf("expected 0 matching skills for nonexistent category, got: %s", stdout.String())
	}

	// status -t cli
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"status", "-t", "cli"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(status -t cli) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
}

type roundTripFunc func(req *http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

func TestRun_GitHub_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	evalsSkillV1 := `---
name: evals
description: Evaluation harness for agents
allowed-tools: [bash, python]
license: MIT
metadata:
  version: 1.0.0
  category: testing
  tags: eval, benchmark
---
# Evals Skill
Evaluation harness instructions.
`

	evalsSkillV2 := `---
name: evals
description: Evaluation harness for agents (v2)
allowed-tools: [bash, python]
license: MIT
metadata:
  version: 1.1.0
  category: testing
  tags: eval, benchmark, advanced
---
# Evals Skill v2
Updated evaluation harness instructions.
`

	currentSkillContent := evalsSkillV1

	mockClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			urlStr := req.URL.String()
			if strings.Contains(urlStr, "evals/SKILL.md") || strings.Contains(urlStr, "evals/skill.md") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(currentSkillContent)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("404 not found")),
				Header:     make(http.Header),
			}
		}),
	}

	app, err := NewApp(GlobalOptions{Mode: OutputModeHuman}, nil, nil)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app.HTTPClient = mockClient

	// 1. Learn from GitHub shorthand: anthropics/anthropic-quickstarts/evals
	var stdout, stderr bytes.Buffer
	app.Stdout = &stdout
	app.Stderr = &stderr

	code := app.RunLearn([]string{"anthropics/anthropic-quickstarts/evals"})
	if code != ExitSuccess {
		t.Fatalf("RunLearn GitHub target = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Learned skill \"evals\"") {
		t.Errorf("expected learned evals output, got: %s", stdout.String())
	}

	installedSkill := filepath.Join(tmpDir, ".agents", "skills", "evals", "SKILL.md")
	data, err := os.ReadFile(installedSkill)
	if err != nil {
		t.Fatalf("failed to read installed SKILL.md: %v", err)
	}
	if !strings.Contains(string(data), "name: evals") {
		t.Errorf("unexpected file content: %s", string(data))
	}

	// 2. Verify state manifest recorded GitHub provenance
	actualWd, _ := os.Getwd()
	st := app.loadState()
	rec, ok := st.GetSkill(state.ScopeWorkspace, actualWd, "evals")
	if !ok {
		t.Fatalf("evals skill not recorded in state manifest")
	}
	if rec.Origin != state.OriginGitHub {
		t.Errorf("rec.Origin = %q, want %q", rec.Origin, state.OriginGitHub)
	}
	if rec.Repo != "anthropics/anthropic-quickstarts" {
		t.Errorf("rec.Repo = %q, want %q", rec.Repo, "anthropics/anthropic-quickstarts")
	}
	if rec.Version != "1.0.0" {
		t.Errorf("rec.Version = %q, want 1.0.0", rec.Version)
	}

	// 3. Test Show displaying GitHub Origin & Ref
	stdout.Reset()
	stderr.Reset()
	code = app.RunShow([]string{"evals"})
	if code != ExitSuccess {
		t.Fatalf("RunShow evals = %d, want %d", code, ExitSuccess)
	}
	showOut := stdout.String()
	if !strings.Contains(showOut, "Origin:      github (anthropics/anthropic-quickstarts)") {
		t.Errorf("expected Show to display GitHub origin, got: %s", showOut)
	}
	if !strings.Contains(showOut, "Ref:         main") {
		t.Errorf("expected Show to display Ref main, got: %s", showOut)
	}

	// 4. Test List showing installed status
	stdout.Reset()
	stderr.Reset()
	code = app.RunList([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunList = %d, want %d", code, ExitSuccess)
	}

	// 5. Test Update detection & execution
	// First check dry-run (no changes yet)
	stdout.Reset()
	stderr.Reset()
	code = app.RunUpdate([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunUpdate dry-run = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}

	// Now bump remote version to 1.1.0
	currentSkillContent = evalsSkillV2

	// Check update detection
	stdout.Reset()
	stderr.Reset()
	code = app.RunUpdate([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunUpdate check = %d, want %d", code, ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "new version available") && !strings.Contains(stdout.String(), "1.1.0") {
		t.Errorf("expected update available notice, got: %s", stdout.String())
	}

	// Apply update
	stdout.Reset()
	stderr.Reset()
	code = app.RunUpdate([]string{"--yes", "evals"})
	if code != ExitSuccess {
		t.Fatalf("RunUpdate --yes = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}

	dataV2, _ := os.ReadFile(installedSkill)
	if !strings.Contains(string(dataV2), "version: 1.1.0") {
		t.Errorf("expected updated skill content with v1.1.0, got: %s", string(dataV2))
	}

	// 6. Test Load directly with GitHub URL shorthand
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"anthropics/anthropic-quickstarts/evals"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad GitHub target = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Evals Skill v2") {
		t.Errorf("expected streamed skill body in Load, got: %s", stdout.String())
	}

	// 7. Test 'add' alias
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"add", "--force", "anthropics/anthropic-quickstarts/evals"}, &stdout, &stderr)
	// Run creates a fresh App without the mock HTTP transport, but 'add' command routing should be accepted (not unknown command)
	if strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("'add' command not recognized as alias for 'learn': %s", stderr.String())
	}
}
