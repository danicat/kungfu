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
	"fmt"
	"io/fs"
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
	defaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$schema":"https://schemas.agentskills.io/discovery/0.2.0/schema.json","skills":[]}`))
	}))

	tempHome, err := os.MkdirTemp("", "kungfu-cmd-test-home-*")
	if err == nil {
		_ = os.Setenv("HOME", tempHome)
		_ = os.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tempHome, ".config", "kungfu"))
		_ = os.Setenv("KUNGFU_STATE_PATH", filepath.Join(tempHome, ".config", "kungfu", "state.json"))
		_ = os.Setenv("KUNGFU_CONFIG", "")
		_ = os.Setenv("KUNGFU_URL", defaultServer.URL)
		_ = os.Setenv("KUNGFU_TELEMETRY", "0")
		_ = os.Setenv("DO_NOT_TRACK", "")
	}
	code := m.Run()
	defaultServer.Close()
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
		{"fs ErrNotExist", fmt.Errorf("config: reading /missing.yaml: %w", &os.PathError{Op: "open", Path: "/missing.yaml", Err: syscall.ENOENT}), ExitNotFound},
		{"fs ErrExist", fs.ErrExist, ExitGeneral},
		{"syscall EISDIR", &os.PathError{Op: "read", Path: "/tmp", Err: syscall.EISDIR}, ExitGeneral},
		{"digest mismatch", catalog.ErrDigestMismatch, ExitIntegrity},
		{"invalid frontmatter", catalog.ErrInvalidFrontmatter, ExitIntegrity},
		{"generic not found", errors.New("resource not found"), ExitNotFound},
		{"http 404", errors.New("HTTP 404 Not Found"), ExitNotFound},
		{"network timeout", errors.New("connection timeout"), ExitNetwork},
		{"connection refused", syscall.ECONNREFUSED, ExitNetwork},
		{"connection reset", syscall.ECONNRESET, ExitNetwork},
		{"net OpError wrapping ECONNRESET", &net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}}, ExitNetwork},
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
		{"help subcommand find", []string{"help", "find"}, ExitSuccess, "kungfu find"},
		{"help subcommand load", []string{"help", "load"}, ExitSuccess, "kungfu load"},
		{"subcommand --help offline", []string{"find", "--help"}, ExitSuccess, "kungfu find"},
		{"subcommand -h offline", []string{"load", "-h"}, ExitSuccess, "kungfu load"},
		{"run --help offline", []string{"run", "--help"}, ExitSuccess, "kungfu run"},
		{"catalog add --help offline", []string{"catalog", "add", "--help"}, ExitSuccess, "kungfu catalog add"},
		{"no args", []string{}, ExitUsage, "USAGE:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Point KUNGFU_URL to an unreachable port to prove help never fetches the remote catalog.
			t.Setenv("KUNGFU_URL", "http://127.0.0.1:1/index.json")
			t.Setenv("HOME", t.TempDir())
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

func TestFlagParsingEdgeCases(t *testing.T) {
	server := setupTestCatalogServer(t)
	defer server.Close()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "missing value for --config before subcommand",
			args:       []string{"--config", "list"},
			wantCode:   ExitUsage,
			wantStderr: "flag needs an argument: --config",
		},
		{
			name:       "missing value for --cache-dir before subcommand",
			args:       []string{"--cache-dir", "find"},
			wantCode:   ExitUsage,
			wantStderr: "flag needs an argument: --cache-dir",
		},
		{
			name:       "missing value for trailing subcommand flag",
			args:       []string{"load", "kungfu", "-p"},
			wantCode:   ExitUsage,
			wantStderr: "flag needs an argument: -p",
		},
		{
			name:       "missing value for catalog add --name after positional",
			args:       []string{"catalog", "add", "google/skills", "--name"},
			wantCode:   ExitUsage,
			wantStderr: "flag needs an argument: --name",
		},
		{
			name:       "double-dash terminator preserves dash-prefixed search term",
			args:       []string{"find", "--", "-z"},
			wantCode:   ExitNotFound,
			wantStderr: `skill "-z" not found`,
		},
		{
			name:       "missing explicit config file returns ExitNotFound",
			args:       []string{"--config=/nonexistent-dir-xyz/missing.yaml", "list"},
			wantCode:   ExitNotFound,
			wantStderr: "no such file or directory",
		},
		{
			name:       "find rejects limit less than 1",
			args:       []string{"find", "kungfu", "-n", "0"},
			wantCode:   ExitUsage,
			wantStderr: "limit must be >= 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stdout=%q stderr=%q", tt.args, code, tt.wantCode, stdout.String(), stderr.String())
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("Run(%v) stdout %q does not contain %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("Run(%v) stderr %q does not contain %q", tt.args, stderr.String(), tt.wantStderr)
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
					"url": "https://skills.danicat.dev/agents/kungfu/SKILL.md",
					"digest": "sha256:345678"
				},
				{
					"name": "godoctor",
					"type": "skill-md",
					"description": "Go doctor",
					"url": "https://skills.danicat.dev/coding/godoctor/SKILL.md",
					"digest": "sha256:123456"
				},
				{
					"name": "tech-writer",
					"type": "skill-md",
					"description": "Tech writer",
					"url": "https://skills.danicat.dev/writing/tech-writer/SKILL.md",
					"digest": "sha256:234567"
				},
				{
					"name": "developing-genkit-go",
					"type": "skill-md",
					"description": "Develop AI-powered applications using Genkit in Go",
					"url": "https://skills.danicat.dev/coding/developing-genkit-go/SKILL.md",
					"digest": "sha256:456781"
				},
				{
					"name": "developing-genkit-js",
					"type": "skill-md",
					"description": "Develop AI-powered applications using Genkit in Node.js/TypeScript",
					"url": "https://skills.danicat.dev/coding/developing-genkit-js/SKILL.md",
					"digest": "sha256:456782"
				},
				{
					"name": "developing-genkit-python",
					"type": "skill-md",
					"description": "Develop AI-powered applications using Genkit in Python",
					"url": "https://skills.danicat.dev/coding/developing-genkit-python/SKILL.md",
					"digest": "sha256:456783"
				},
				{
					"name": "developing-genkit-dart",
					"type": "skill-md",
					"description": "Generates code and provides documentation for the Genkit Dart SDK",
					"url": "https://skills.danicat.dev/coding/developing-genkit-dart/SKILL.md",
					"digest": "sha256:456784"
				},
				{
					"name": "lyria",
					"type": "skill-md",
					"description": "Generative music and audio synthesis from text prompts or reference images",
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
			name:     "show unknown name suggests description match and find",
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

func TestStatusUnknownSkillReturnsNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := Run([]string{"status", "nonexistent-skill-xyz"}, &stdout, &stderr)
	if code != ExitNotFound {
		t.Fatalf("Run(status nonexistent-skill-xyz) = %d, want %d; stderr: %s", code, ExitNotFound, stderr.String())
	}
}

func TestGlobalFlagStopAtDoubleDashAndSubcommandNamedConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Create a config file literally named "list" in tmpDir
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origWD) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "list"), []byte("telemetry:\n  enabled: false\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--config", "list", "status", "--json"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(--config list status --json) = %d, want %d; stderr: %s", code, ExitSuccess, stderr.String())
	}
}

func TestLoadLocalSubpathAndCorruptDigestForceGuard(t *testing.T) {
	tmpHome := t.TempDir()
	tmpCache := t.TempDir()
	t.Setenv("HOME", tmpHome)

	var subpathHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-skills/index.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"version": "0.2.0",
				"skills": [
					{
						"name": "bad-digest-skill",
						"type": "skill-md",
						"description": "Skill with mismatched digest",
						"url": "http://` + r.Host + `/skills/bad/SKILL.md",
						"digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
					}
				]
			}`))
		case "/skills/bad/SKILL.md":
			_, _ = w.Write([]byte("---\nname: bad-digest-skill\n---\n# Tampered Content\n"))
		case "/skills/bad/references/secret.md":
			subpathHits++
			_, _ = w.Write([]byte("# Remote Subpath\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	// 1. Prime index cache by running list
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--cache-dir", tmpCache, "list"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("Run(list) failed: %d, stderr: %s", code, stderr.String())
	}

	// 2. `load bad-digest-skill/references/secret.md --local` must NOT hit the network
	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"--cache-dir", tmpCache, "load", "bad-digest-skill/references/secret.md", "--local"}, &stdout, &stderr)
	if code != ExitNotFound {
		t.Fatalf("expected ExitNotFound for uncached --local subpath load, got %d (stderr: %s)", code, stderr.String())
	}
	if subpathHits != 0 {
		t.Errorf("expected 0 network requests for --local subpath load, got %d", subpathHits)
	}

	// 3. `load bad-digest-skill --force` bypasses verification for stdout, but must NOT poison the shared cache
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"--cache-dir", tmpCache, "load", "bad-digest-skill", "--force"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("expected ExitSuccess when bypassing integrity with --force, got %d (stderr: %s)", code, stderr.String())
	}
	cachedFile := filepath.Join(tmpCache, "skills", "bad-digest-skill", "SKILL.md")
	if _, err := os.Stat(cachedFile); !os.IsNotExist(err) {
		t.Errorf("expected corrupt skill content NOT to be written to cache %s", cachedFile)
	}

	// Subsequent `load bad-digest-skill` without --force must still fail with ExitIntegrity
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"--cache-dir", tmpCache, "load", "bad-digest-skill"}, &stdout, &stderr)
	if code != ExitIntegrity {
		t.Fatalf("expected ExitIntegrity (4) on subsequent load without --force, got %d (stderr: %s)", code, stderr.String())
	}
}
