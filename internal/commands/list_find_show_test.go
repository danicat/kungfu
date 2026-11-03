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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
)

func setupTestCatalogServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "kungfu",
					"type": "skill-md",
					"description": "Agent skills gateway and discovery hybrid tool",
					"category": "agents",
					"tags": ["gateway", "discovery"],
					"url": "https://skills.danicat.dev/agents/kungfu/SKILL.md",
					"digest": "sha256:123"
				},
				{
					"name": "godoctor",
					"type": "skill-md",
					"description": "Go doctor",
					"category": "coding",
					"tags": ["go", "linter"],
					"url": "https://skills.danicat.dev/coding/godoctor/SKILL.md",
					"digest": "sha256:456"
				}
			]
		}`))
	}))
	t.Setenv("KUNGFU_URL", server.URL)
	return server
}

func TestRunList(t *testing.T) {
	server := setupTestCatalogServer(t)
	defer server.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		checkOut func(t *testing.T, stdout string)
	}{
		{
			name:     "list default human skill listing",
			args:     []string{"list"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "AVAILABLE SKILLS") {
					t.Errorf("list human missing header: %s", out)
				}
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list human missing kungfu skill: %s", out)
				}
			},
		},
		{
			name:     "list json",
			args:     []string{"list", "--json"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				var items []struct {
					Name     string `json:"name"`
					Category string `json:"category"`
				}
				if err := json.Unmarshal([]byte(out), &items); err != nil {
					t.Fatalf("failed to parse json output: %v, out: %s", err, out)
				}
				if len(items) == 0 {
					t.Errorf("expected non-empty items in json list")
				}
			},
		},
		{
			name:     "list raw flag",
			args:     []string{"list", "--raw"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list raw missing kungfu item: %s", out)
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

func TestRunFind(t *testing.T) {
	server := setupTestCatalogServer(t)
	defer server.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		checkOut func(t *testing.T, stdout string)
	}{
		{
			name:     "find missing query",
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
			name:     "find with limit shorthand",
			args:     []string{"find", "kungfu", "-n", "3"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("expected kungfu in find results with -n, got: %s", out)
				}
			},
		},
		{
			name:     "find json output",
			args:     []string{"find", "kungfu", "--json"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				var res []struct {
					Name  string  `json:"name"`
					Score float32 `json:"score"`
				}
				if err := json.Unmarshal([]byte(out), &res); err != nil {
					t.Fatalf("failed to parse find json output: %v, out: %s", err, out)
				}
				if len(res) == 0 {
					t.Errorf("expected non-empty search results in json")
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

func TestRunShow(t *testing.T) {
	server := setupTestCatalogServer(t)
	defer server.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"show missing arg", []string{"show"}, ExitUsage, ""},
		{"show single skill human default", []string{"show", "kungfu"}, ExitSuccess, "SKILL: kungfu"},
		{"show skill raw", []string{"show", "kungfu", "--raw"}, ExitSuccess, "kungfu\tagents"},
		{"show skill json", []string{"show", "kungfu", "--json"}, ExitSuccess, "\"name\": \"kungfu\""},
		{"show multiple skills", []string{"show", "kungfu", "godoctor"}, ExitSuccess, "SKILL: kungfu"},
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
			if strings.Contains(stdout.String(), "Scripts:") {
				t.Errorf("Run(%v) unexpectedly listed Scripts from markdown text when no scripts/ directory exists on disk:\n%s", tt.args, stdout.String())
			}
		})
	}
}

func buildTestTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar WriteHeader(%s): %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar Write(%s): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func TestRunShow_ArchiveAndSkillMDScripts(t *testing.T) {
	archiveBytes := buildTestTarGz(t, map[string]string{
		"SKILL.md": `---
name: archiveskill
description: Packaged archive skill with real scripts
---
# Archive Skill

Example commands in prose that should not cause duplicates or false positives:
uv run ./scripts/outline.py
uv run scripts/fake_in_docs.py
python3 -m py_compile scripts/outline.py
`,
		"scripts/outline.py":      "print('outline')\n",
		"scripts/sub/helper.py":   "print('helper')\n",
		"scripts/go.mod":          "module example.com/test\n",
		"scripts/go.sum":          "\n",
		"scripts/visualizer.html": "<html></html>\n",
		"references/guide.md":     "# Guide\n",
		"references/sub/deep.md":  "# Deep\n",
	})
	archiveDigest := catalog.ComputeDigest(archiveBytes)

	skillMDContent := `---
name: doconlyskill
description: Single-file skill-md that mentions scripts in examples
---
# Doc-Only Skill

Run example:
uv run scripts/not_bundled.py
`
	skillMDDigest := catalog.ComputeDigest([]byte(skillMDContent))

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-skills/index.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
				"skills": [
					{
						"name": "archiveskill",
						"type": "archive",
						"description": "Packaged archive skill with real scripts",
						"url": "%s/.well-known/agent-skills/archiveskill.tar.gz",
						"digest": "%s"
					},
					{
						"name": "doconlyskill",
						"type": "skill-md",
						"description": "Single-file skill-md that mentions scripts in examples",
						"url": "%s/skills/doconlyskill/SKILL.md",
						"digest": "%s"
					}
				]
			}`, serverURL, archiveDigest, serverURL, skillMDDigest)
		case "/.well-known/agent-skills/archiveskill.tar.gz":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(archiveBytes)
		case "/skills/doconlyskill/SKILL.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(skillMDContent))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	t.Setenv("KUNGFU_URL", server.URL+"/.well-known/agent-skills/index.json")

	// Seed a stale cache entry for doconlyskill to verify skill-md ignores stale cache scripts
	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		staleScriptDir := filepath.Join(homeDir, ".cache", "kungfu", "skills", "doconlyskill", "scripts")
		_ = os.MkdirAll(staleScriptDir, 0755)
		_ = os.WriteFile(filepath.Join(staleScriptDir, "stale.py"), []byte("print('stale')\n"), 0644)
	}

	// 1. Verify archive skill unpacks and lists only real scripts and references
	var stdout, stderr bytes.Buffer
	cacheDir := t.TempDir()
	code := Run([]string{"--cache-dir", cacheDir, "show", "archiveskill", "--json"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(show archiveskill) = %d; stderr: %s", code, stderr.String())
	}
	var archiveDetail struct {
		Name       string   `json:"name"`
		Scripts    []string `json:"scripts"`
		References []string `json:"references"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &archiveDetail); err != nil {
		t.Fatalf("json.Unmarshal(archiveskill): %v; out=%s", err, stdout.String())
	}
	wantScripts := []string{"outline.py", "sub/helper.py"}
	if !reflect.DeepEqual(archiveDetail.Scripts, wantScripts) {
		t.Errorf("archiveskill scripts = %v, want %v", archiveDetail.Scripts, wantScripts)
	}
	wantRefs := []string{"guide", "sub/deep"}
	if !reflect.DeepEqual(archiveDetail.References, wantRefs) {
		t.Errorf("archiveskill references = %v, want %v", archiveDetail.References, wantRefs)
	}

	// 2. Verify skill-md item reports no scripts even when SKILL.md mentions scripts or stale cache exists
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"--cache-dir", cacheDir, "show", "doconlyskill", "--json"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(show doconlyskill) = %d; stderr: %s", code, stderr.String())
	}
	var docDetail struct {
		Name       string   `json:"name"`
		Scripts    []string `json:"scripts"`
		References []string `json:"references"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &docDetail); err != nil {
		t.Fatalf("json.Unmarshal(doconlyskill): %v; out=%s", err, stdout.String())
	}
	if len(docDetail.Scripts) != 0 {
		t.Errorf("doconlyskill scripts = %v, want empty", docDetail.Scripts)
	}
	if len(docDetail.References) != 0 {
		t.Errorf("doconlyskill references = %v, want empty", docDetail.References)
	}
}
