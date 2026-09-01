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
	"strings"
	"testing"
)

func TestRunList(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		checkOut func(t *testing.T, stdout string)
	}{
		{
			name:     "list default human category overview",
			args:     []string{"list"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "CATEGORY") || !strings.Contains(out, "COUNT") {
					t.Errorf("list human missing headers: %s", out)
				}
				if !strings.Contains(out, "agents") {
					t.Errorf("list human missing agents category: %s", out)
				}
			},
		},
		{
			name:     "list all flag",
			args:     []string{"list", "--all"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "NAME") || !strings.Contains(out, "CATEGORY") {
					t.Errorf("list --all missing table header: %s", out)
				}
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list --all missing kungfu skill: %s", out)
				}
			},
		},
		{
			name:     "list json",
			args:     []string{"list", "--all", "--json"},
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
			args:     []string{"list", "--all", "--raw"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list raw missing kungfu item: %s", out)
				}
			},
		},
		{
			name:     "list positional category",
			args:     []string{"list", "agents"},
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
			code := Run(tt.args, &stdout, &stderr, testEmbeddedFS)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			tt.checkOut(t, stdout.String())
		})
	}
}

func TestRunFind(t *testing.T) {
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
			name:     "find with tag shorthand",
			args:     []string{"find", "kungfu", "-t", "cli"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("expected kungfu in find results with -t, got: %s", out)
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
			code := Run(tt.args, &stdout, &stderr, testEmbeddedFS)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			tt.checkOut(t, stdout.String())
		})
	}
}

func TestRunShow(t *testing.T) {
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
		{"show with category flag", []string{"show", "-c", "agents"}, ExitSuccess, "SKILL: kungfu"},
		{"show with tags flag", []string{"show", "-t", "agent-skills"}, ExitSuccess, "SKILL: kungfu"},
		{"show all flag", []string{"show", "--all"}, ExitSuccess, "SKILL: kungfu"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr, testEmbeddedFS)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			if tt.wantSub != "" && !strings.Contains(stdout.String(), tt.wantSub) {
				t.Errorf("Run(%v) output %q does not contain %q", tt.args, stdout.String(), tt.wantSub)
			}
		})
	}
}
