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
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
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
			name:     "list default raw",
			args:     []string{"list"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list raw missing embedded kungfu item: %s", out)
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
			},
		},
		{
			name:     "list human",
			args:     []string{"list", "--human"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "SKILLS & KNOWLEDGE DOCS:") {
					t.Errorf("list human missing headers: %s", out)
				}
			},
		},
		{
			name:     "list raw flag",
			args:     []string{"list", "--raw"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list raw missing items: %s", out)
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
			name:     "find hybrid search",
			args:     []string{"find", "gateway", "discovery"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
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
			name:     "find human output",
			args:     []string{"find", "gateway", "--human"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "SEARCH RESULTS") {
					t.Errorf("expected SEARCH RESULTS header in human output, got: %s", out)
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
		{"show single skill raw default", []string{"show", "kungfu"}, ExitSuccess, "kungfu"},
		{"show skill human", []string{"show", "kungfu", "--human"}, ExitSuccess, "Name:"},
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
		{"load embedded kungfu raw default", []string{"load", "kungfu"}, ExitSuccess, "name: kungfu"},
		{"load by category flag", []string{"load", "-c", "gateway"}, ExitSuccess, "name: kungfu"},
		{"load by tags flag boolean AND", []string{"load", "-t", "agent-skills AND discovery"}, ExitSuccess, "name: kungfu"},
		{"load by tags flag boolean OR", []string{"load", "-t", "cli OR okf"}, ExitSuccess, "name: kungfu"},
		{"load all flag", []string{"load", "--all"}, ExitSuccess, "name: kungfu"},
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
		})
	}
}

func TestRun_Learn(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// 1. Learn embedded kungfu skill into current workspace (.agents/skills/kungfu)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"learn", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn kungfu) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Learned skill \"kungfu\"") {
		t.Errorf("unexpected learn output: %s", stdout.String())
	}

	installedSkill := filepath.Join(tmpDir, ".agents", "skills", "kungfu", "SKILL.md")
	data, err := os.ReadFile(installedSkill)
	if err != nil {
		t.Fatalf("failed to read installed SKILL.md: %v", err)
	}
	if !strings.Contains(string(data), "name: kungfu") {
		t.Errorf("installed SKILL.md content mismatch: %s", string(data))
	}

	// 2. Learn again without force (should skip)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"learn", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn kungfu duplicate) = %d, want %d", code, ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "Skipped skill") {
		t.Errorf("expected skip message, got: %s", stdout.String())
	}

	// 3. Learn with force (should overwrite)
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"learn", "--force", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn --force kungfu) = %d, want %d", code, ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "Learned skill \"kungfu\"") {
		t.Errorf("expected learned message with force, got: %s", stdout.String())
	}

	// 4. Learn with JSON output
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"learn", "--force", "--json", "kungfu"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(learn --json) = %d, want %d", code, ExitSuccess)
	}
	var results []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("failed to parse json learn result: %v", err)
	}
	if len(results) != 1 || results[0].Status != "installed" {
		t.Errorf("unexpected json learn results: %+v", results)
	}
}

func TestRun_RunAndRead(t *testing.T) {
	t.Run("run missing args", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"run"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("Run(run) = %d, want %d", code, ExitUsage)
		}
	})

	t.Run("run non-existent skill", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"run", "fake-skill-123", "script.sh"}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("Run(run fake) = %d, want %d", code, ExitNotFound)
		}
	})

	t.Run("read missing args", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"read"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("Run(read) = %d, want %d", code, ExitUsage)
		}
	})

	t.Run("read non-existent skill", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"read", "fake-skill-123", "doc.md"}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("Run(read fake) = %d, want %d", code, ExitNotFound)
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

	refContent, err := runner.ReadReference(item, "guide")
	if err != nil {
		t.Fatalf("runner.ReadReference failed: %v", err)
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

func TestRun_Read_JSON(t *testing.T) {
	tmpDir := t.TempDir()
	refsDir := filepath.Join(tmpDir, "references")
	_ = os.MkdirAll(refsDir, 0755)
	_ = os.WriteFile(filepath.Join(refsDir, "doc.md"), []byte("Reference body content"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte("---\nname: readskill\n---\n"), 0644)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	if cat, ok := app.Store.(*catalog.Catalog); ok {
		cat.AddItem(catalog.Item{
			ID:   "readskill",
			Name: "readskill",
			Path: tmpDir,
		})
	}

	code := app.RunRead([]string{"readskill", "doc", "--json"})
	if code != ExitSuccess {
		t.Fatalf("RunRead --json failed with code %d: %s", code, stderr.String())
	}

	var resp struct {
		Skill     string `json:"skill"`
		Reference string `json:"reference"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v, out: %s", err, stdout.String())
	}
	if resp.Skill != "readskill" || resp.Reference != "doc" || resp.Content != "Reference body content" {
		t.Errorf("unexpected read JSON response: %+v", resp)
	}
}
