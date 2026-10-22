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
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
)

func TestMain(m *testing.M) {
	tempHome, err := os.MkdirTemp("", "kungfu-cmd-test-home-*")
	if err == nil {
		_ = os.Setenv("HOME", tempHome)
		_ = os.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tempHome, ".config", "kungfu"))
		_ = os.Setenv("KUNGFU_STATE_PATH", filepath.Join(tempHome, ".config", "kungfu", "state.json"))
		_ = os.Setenv("KUNGFU_CONFIG", "")
		_ = os.Setenv("KUNGFU_TELEMETRY", "0")
		_ = os.Setenv("DO_NOT_TRACK", "")
	}
	code := m.Run()
	if tempHome != "" {
		_ = os.RemoveAll(tempHome)
	}
	os.Exit(code)
}

func TestMapErrorToExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"nil error", nil, ExitSuccess},
		{"item not found", catalog.ErrItemNotFound, ExitNotFound},
		{"digest mismatch", catalog.ErrDigestMismatch, ExitIntegrity},
		{"invalid frontmatter", catalog.ErrInvalidFrontmatter, ExitIntegrity},
		{"generic not found", errors.New("resource not found"), ExitNotFound},
		{"http 404", errors.New("HTTP 404 Not Found"), ExitNotFound},
		{"network timeout", errors.New("connection timeout"), ExitNetwork},
		{"connection refused", syscall.ECONNREFUSED, ExitNetwork},
		{"custom net error", &net.DNSError{Err: "no such host"}, ExitNetwork},
		{"unclassified error", errors.New("some unexpected syntax error"), ExitGeneral},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MapErrorToExitCode(tt.err)
			if got != tt.wantCode {
				t.Errorf("MapErrorToExitCode(%v) = %d, want %d", tt.err, got, tt.wantCode)
			}
		})
	}
}

func TestGlobalHelpAndVersion(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"version flag", []string{"--version"}, ExitSuccess, "kungfu version"},
		{"version short", []string{"-V"}, ExitSuccess, "kungfu version"},
		{"version cmd", []string{"version"}, ExitSuccess, "kungfu version"},
		{"help flag", []string{"--help"}, ExitSuccess, "USAGE:"},
		{"help short", []string{"-h"}, ExitSuccess, "USAGE:"},
		{"help cmd", []string{"help"}, ExitSuccess, "USAGE:"},
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
				t.Errorf("Run(%v) stdout %q does not contain %q", tt.args, out, tt.wantSub)
			}
		})
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"unknowncommand"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("Run(unknowncommand) = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("expected unknown command error message, got %q", stderr.String())
	}
}

func TestDidYouMeanSuggestions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "kungfu",
					"type": "skill-md",
					"description": "Agent skills gateway",
					"category": "agents",
					"url": "https://skills.danicat.dev/agents/kungfu/SKILL.md",
					"digest": "sha256:345678"
				},
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
					"name": "developing-genkit-go",
					"type": "skill-md",
					"description": "Develop AI-powered applications using Genkit in Go",
					"category": "coding",
					"url": "https://skills.danicat.dev/coding/developing-genkit-go/SKILL.md",
					"digest": "sha256:456781"
				},
				{
					"name": "developing-genkit-js",
					"type": "skill-md",
					"description": "Develop AI-powered applications using Genkit in Node.js/TypeScript",
					"category": "coding",
					"url": "https://skills.danicat.dev/coding/developing-genkit-js/SKILL.md",
					"digest": "sha256:456782"
				},
				{
					"name": "developing-genkit-python",
					"type": "skill-md",
					"description": "Develop AI-powered applications using Genkit in Python",
					"category": "coding",
					"url": "https://skills.danicat.dev/coding/developing-genkit-python/SKILL.md",
					"digest": "sha256:456783"
				},
				{
					"name": "developing-genkit-dart",
					"type": "skill-md",
					"description": "Generates code and provides documentation for the Genkit Dart SDK",
					"category": "coding",
					"url": "https://skills.danicat.dev/coding/developing-genkit-dart/SKILL.md",
					"digest": "sha256:456784"
				},
				{
					"name": "lyria",
					"type": "skill-md",
					"description": "Generative music and audio synthesis from text prompts or reference images",
					"category": "media",
					"tags": ["audio", "music", "lyria"],
					"url": "https://skills.danicat.dev/media/lyria/SKILL.md",
					"digest": "sha256:567890"
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
		wantStderr []string
	}{
		{
			name:     "typo kungfuu suggests kungfu with description",
			args:     []string{"load", "kungfuu"},
			wantCode: ExitNotFound,
			wantStderr: []string{
				"kungfu load kungfu",
				"# Agent skills gateway",
			},
		},
		{
			name:     "prefix godoc suggests godoctor",
			args:     []string{"load", "godoc"},
			wantCode: ExitNotFound,
			wantStderr: []string{
				"kungfu load godoctor",
				"# Go doctor",
			},
		},
		{
			name:     "genkit suggests all 4 genkit skills (up to 5)",
			args:     []string{"load", "genkit"},
			wantCode: ExitNotFound,
			wantStderr: []string{
				"developing-genkit-go",
				"developing-genkit-js",
				"developing-genkit-python",
				"developing-genkit-dart",
				"kungfu find genkit",
			},
		},
		{
			name:     "concept/tag query audio suggests lyria via hybrid search",
			args:     []string{"load", "audio"},
			wantCode: ExitNotFound,
			wantStderr: []string{
				"kungfu load lyria",
				"# Generative music and audio synthesis",
				"kungfu find audio",
			},
		},
		{
			name:     "show unknown name suggests category match and find",
			args:     []string{"show", "agents"},
			wantCode: ExitNotFound,
			wantStderr: []string{
				"kungfu show kungfu",
				"kungfu find agents",
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
			for _, sub := range tt.wantStderr {
				if !strings.Contains(stderr.String(), sub) {
					t.Errorf("Run(%v) stderr %q does not contain %q", tt.args, stderr.String(), sub)
				}
			}
		})
	}
}

func TestCatalogSyncRemoved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"catalog", "sync"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("Run(catalog sync) = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown catalog subcommand") {
		t.Errorf("expected unknown catalog subcommand error, got %q", stderr.String())
	}
}
