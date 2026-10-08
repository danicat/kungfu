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
	"strings"
	"testing"
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
		})
	}
}
