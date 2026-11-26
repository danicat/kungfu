// Copyright 2026 Daniela Petruzalek
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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

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
		_, _ = fmt.Fprintf(w, `{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "kungfu",
					"type": "skill-md",
					"description": "Agent skills gateway",
					"url": "%s/skills/kungfu/SKILL.md",
					"digest": "%s"
				}
			]
		}`, server.URL, catalog.ComputeDigest([]byte(skillContent)))
	}))
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)
	cacheDir := t.TempDir()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"load missing arg", []string{"--cache-dir", cacheDir, "load"}, ExitUsage, ""},
		{"load kungfu default", []string{"--cache-dir", cacheDir, "load", "kungfu"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load kungfu with --local", []string{"--cache-dir", cacheDir, "load", "kungfu", "--local"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load kungfu with --raw includes frontmatter", []string{"--cache-dir", cacheDir, "load", "kungfu", "--raw"}, ExitSuccess, "name: kungfu"},
		{"load kungfu with -u", []string{"--cache-dir", cacheDir, "load", "kungfu", "-u"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load kungfu with -f", []string{"--cache-dir", cacheDir, "load", "kungfu", "-f"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load multiple skills rejected", []string{"--cache-dir", cacheDir, "load", "kungfu", "godoctor"}, ExitUsage, ""},
		{"load skill json", []string{"--cache-dir", cacheDir, "load", "kungfu", "--json"}, ExitSuccess, `"name": "kungfu"`},
		{"load rejects --json and --raw", []string{"--cache-dir", cacheDir, "load", "kungfu", "--json", "--raw"}, ExitUsage, ""},
		{"load rejects --local and --update", []string{"--cache-dir", cacheDir, "load", "kungfu", "--local", "--update"}, ExitUsage, ""},
		{"load rejects --local and --force", []string{"--cache-dir", cacheDir, "load", "kungfu", "--local", "--force"}, ExitUsage, ""},
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
	app, err := NewApp(GlobalOptions{CacheDir: t.TempDir()}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app.Store.AddItem(catalog.Item{
		ID:   "local-skill",
		Name: "local-skill",
		Path: skillDir,
	})

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

	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte("---\nname: subskill\n---\n# Root Playbook\nRoot body."), 0644)
	_ = os.WriteFile(filepath.Join(refsDir, "schema.md"), []byte("---\ntitle: Schema\n---\n# Database Schema\nSchema body content."), 0644)
	_ = os.WriteFile(filepath.Join(scriptsDir, "sync.py"), []byte("#!/usr/bin/env python3\nprint('syncing')\n"), 0755)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{CacheDir: t.TempDir()}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app.Store.AddItem(catalog.Item{
		ID:   "subskill",
		Name: "subskill",
		Path: tmpDir,
		Body: "---\nname: subskill\n---\n# Root Playbook\nRoot body.",
	})

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
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}

	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	_ = os.MkdirAll(scriptsDir, 0755)

	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte("---\nname: runskill\n---\n# Run Skill"), 0644)
	_ = os.WriteFile(filepath.Join(scriptsDir, "echo.sh"), []byte("#!/bin/sh\necho \"kungfu-run-ok: $1\"\n"), 0755)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{CacheDir: t.TempDir()}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app.Store.AddItem(catalog.Item{
		ID:   "runskill",
		Name: "runskill",
		Path: tmpDir,
		Body: "---\nname: runskill\n---\n# Run Skill",
	})

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
	t.Parallel()

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

	// Verify blank lines are preserved across pagination (no silent drops).
	blankDoc := "para 1\n\npara 2\n\npara 3\n\npara 4"
	blankPages := paginateContent(blankDoc, 20)
	rejoined := strings.Join(blankPages, "\n")
	if !strings.Contains(rejoined, "\n\n") {
		t.Errorf("expected blank lines to be preserved, got pages: %q", blankPages)
	}

	// Verify Markdown code fences are closed at page end and reopened on next page.
	codeDoc := "```go\n" + strings.Repeat("fmt.Println(\"hello\")\n", 10) + "```\n"
	codePages := paginateContent(codeDoc, 80)
	if len(codePages) < 2 {
		t.Fatalf("expected >= 2 pages for fenced code doc, got %d", len(codePages))
	}
	if !strings.HasSuffix(codePages[0], "\n```") {
		t.Errorf("expected page 0 to close open code fence, got:\n%s", codePages[0])
	}
	if !strings.HasPrefix(codePages[1], "```go\n") {
		t.Errorf("expected page 1 to reopen ```go fence, got:\n%s", codePages[1])
	}

	// Verify multibyte UTF-8 runes are never split mid-rune.
	utf8Line := strings.Repeat("日", 50) // 150 bytes
	utf8Pages := paginateContent(utf8Line, 20)
	for i, p := range utf8Pages {
		if !utf8.ValidString(p) {
			t.Errorf("page %d contains invalid UTF-8: %x", i, p)
		}
	}
}

func TestRunLoad_PaginationAndRawDefault(t *testing.T) {
	skillContent := "---\nname: kungfu\ndescription: agent skills gateway\n---\n# KungFu: Agent Skills Gateway\n" + strings.Repeat("Long body line for pagination testing purposes.\n", 40)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SKILL.md") {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(skillContent))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "kungfu",
					"type": "skill-md",
					"description": "Agent skills gateway",
					"url": "%s/skills/kungfu/SKILL.md",
					"digest": "%s"
				}
			]
		}`, server.URL, catalog.ComputeDigest([]byte(skillContent)))
	}))
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{CacheDir: t.TempDir()}, &stdout, &stderr)
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
	if !strings.Contains(out1, "[Page 1 of") || !strings.Contains(out1, "Next: kungfu load kungfu -p 2]") {
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

	// 3. --raw without explicit -p/--page-size emits complete unpaginated content with frontmatter
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--raw"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad --raw failed: %d, stderr: %s", code, stderr.String())
	}
	if stdout.String() != skillContent {
		t.Errorf("--raw truncated or altered content: got len=%d want len=%d", len(stdout.String()), len(skillContent))
	}

	// 4. Out of bounds page
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=500", "-p", "9999"})
	if code != ExitUsage || !strings.Contains(stderr.String(), "out of bounds") {
		t.Fatalf("expected ExitUsage + out of bounds, got %d stderr=%q", code, stderr.String())
	}

	// 5. Invalid page < 1
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "-p", "0"})
	if code != ExitUsage || !strings.Contains(stderr.String(), "page must be >= 1") {
		t.Fatalf("expected ExitUsage + page must be >= 1, got %d stderr=%q", code, stderr.String())
	}

	// 6. Invalid page-size <= 0 or > 7000
	for _, ps := range []string{"0", "7001"} {
		stdout.Reset()
		stderr.Reset()
		code = app.RunLoad([]string{"kungfu", "--page-size=" + ps})
		if code != ExitUsage || !strings.Contains(stderr.String(), "page-size must be between 1 and 7000") {
			t.Fatalf("page-size=%s expected ExitUsage, got %d stderr=%q", ps, code, stderr.String())
		}
	}

	// 7. Conflicting --full and -p > 1
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--full", "-p", "2"})
	if code != ExitUsage || !strings.Contains(stderr.String(), "cannot combine --full") {
		t.Fatalf("expected ExitUsage + cannot combine --full, got %d stderr=%q", code, stderr.String())
	}

	// 8. --full outputs unpaginated content without pagination framing
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=500", "--full"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad --full failed: %d, stderr: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "[Page ") || !strings.Contains(stdout.String(), "# KungFu: Agent Skills Gateway") {
		t.Errorf("unexpected --full output: %s", stdout.String())
	}

	// 9. Multiple skills rejected
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "godoctor"})
	if code != ExitUsage || !strings.Contains(stderr.String(), "loading multiple skills simultaneously is disabled") {
		t.Fatalf("expected ExitUsage for multiple skills, got %d stderr=%q", code, stderr.String())
	}

	// 10. JSON mode with pagination
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"kungfu", "--page-size=500", "-p", "1", "--json"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad --json with pagination failed: %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"page": 1`) || !strings.Contains(stdout.String(), `"total_pages":`) {
		t.Errorf("expected json to contain page and total_pages, got: %s", stdout.String())
	}
}

func TestHistoryRecording_OptInTelemetryFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}

	stateFile := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("KUNGFU_STATE_PATH", stateFile)
	t.Setenv("KUNGFU_TELEMETRY", "")

	tmpDir := t.TempDir()
	refsDir := filepath.Join(tmpDir, "references")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	_ = os.MkdirAll(refsDir, 0755)
	_ = os.MkdirAll(scriptsDir, 0755)

	body := "---\nname: teleskill\n---\n# Telemetry Skill\n" + strings.Repeat("Line of skill body for pagination.\n", 30)
	_ = os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte(body), 0644)
	_ = os.WriteFile(filepath.Join(refsDir, "guide.md"), []byte("# Guide Reference\nDetails."), 0644)
	_ = os.WriteFile(filepath.Join(scriptsDir, "ping.sh"), []byte("#!/bin/sh\necho pong\n"), 0755)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{CacheDir: t.TempDir()}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app.Store.AddItem(catalog.Item{
		ID:     "teleskill",
		Name:   "teleskill",
		Source: "remote",
		URL:    "https://skills.danicat.dev/.well-known/agent-skills/teleskill/SKILL.md",
		Path:   tmpDir,
		Body:   body,
	})

	// 1. Default telemetry OFF: load and run must NOT write state.json at all.
	if code := app.RunLoad([]string{"teleskill", "--page-size=400", "-p", "1"}); code != ExitSuccess {
		t.Fatalf("RunLoad (telemetry off) failed: %d (%s)", code, stderr.String())
	}
	if code := app.RunRun([]string{"teleskill", "ping.sh"}); code != ExitSuccess {
		t.Fatalf("RunRun (telemetry off) failed: %d (%s)", code, stderr.String())
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("expected no state.json write when local telemetry is disabled (default), got stat err: %v", err)
	}

	// 2. Opt in via KUNGFU_TELEMETRY=1: all usage counters are recorded locally.
	t.Setenv("KUNGFU_TELEMETRY", "1")
	if code := app.RunLoad([]string{"teleskill", "--page-size=400", "-p", "1"}); code != ExitSuccess {
		t.Fatalf("RunLoad page 1 failed: %d (%s)", code, stderr.String())
	}
	if code := app.RunLoad([]string{"teleskill", "--page-size=400", "-p", "2"}); code != ExitSuccess {
		t.Fatalf("RunLoad page 2 failed: %d (%s)", code, stderr.String())
	}
	if code := app.RunLoad([]string{"teleskill/references/guide.md"}); code != ExitSuccess {
		t.Fatalf("RunLoad subpath failed: %d (%s)", code, stderr.String())
	}
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
	if h.Catalog != "https://skills.danicat.dev" {
		t.Errorf("expected Catalog=%q, got %q", "https://skills.danicat.dev", h.Catalog)
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

func TestRunLoad_DigestCacheHitAndOfflineLocal(t *testing.T) {
	skillContent := "---\nname: cachedpage\ndescription: pagination cache test\n---\n# Cached Page Skill\n" + strings.Repeat("Line of body for multi-page test.\n", 40)
	digest := catalog.ComputeDigest([]byte(skillContent))

	var skillFetchCount atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SKILL.md") {
			skillFetchCount.Add(1)
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(skillContent))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "cachedpage",
					"type": "skill-md",
					"description": "pagination cache test",
					"url": "%s/skills/cachedpage/SKILL.md",
					"digest": "%s"
				}
			]
		}`, server.URL, digest)
	}))
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	tmpCache := t.TempDir()
	var stdout, stderr bytes.Buffer

	// 1. Page 1 downloads from remote (skillFetchCount == 1)
	if code := Run([]string{"--cache-dir", tmpCache, "load", "cachedpage", "--page-size=500", "-p", "1"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("page 1 load failed: %d (%s)", code, stderr.String())
	}
	if got := skillFetchCount.Load(); got != 1 {
		t.Fatalf("expected 1 remote fetch on page 1, got %d", got)
	}

	// 2. Page 2 uses the digest-verified local cache without a second HTTP download (skillFetchCount == 1)
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--cache-dir", tmpCache, "load", "cachedpage", "--page-size=500", "-p", "2"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("page 2 load failed: %d (%s)", code, stderr.String())
	}
	if got := skillFetchCount.Load(); got != 1 {
		t.Fatalf("expected page 2 to hit digest-verified cache (1 fetch total), got %d", got)
	}

	// 3. Passing -u forces a fresh remote check and download (skillFetchCount == 2)
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--cache-dir", tmpCache, "load", "cachedpage", "-u"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("load -u failed: %d (%s)", code, stderr.String())
	}
	if got := skillFetchCount.Load(); got != 2 {
		t.Fatalf("expected load -u to re-fetch from remote (2 fetches total), got %d", got)
	}

	// 4. Close the remote server and expire the unified default catalog cache; `load cachedpage --local` must still succeed offline
	server.Close()
	indexPath := filepath.Join(tmpCache, "catalogs", catalog.SafeCatalogFileName("default"))
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("expected default catalog cache at %s: %v", indexPath, err)
	}
	expired := strings.ReplaceAll(string(data), `"fetched_at": "20`, `"fetched_at": "2020-01-01T00:00:00Z", "old_fetched_at": "20`)
	_ = os.WriteFile(indexPath, []byte(expired), 0600)

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--cache-dir", tmpCache, "load", "cachedpage", "--local"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("expected offline load --local with expired index cache to succeed, got %d (%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Cached Page Skill") {
		t.Errorf("expected cached skill content from offline --local load, got %q", stdout.String())
	}
}

func TestAtCatalog_LoadShowLearnAndAmbiguity(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("HOME", tmpDir)
	t.Setenv("KUNGFU_STATE_PATH", filepath.Join(tmpDir, "state.json"))

	// Verify isGitHubSkillSpec distinguishes skill@owner/repo from owner/repo@ref.
	if isGitHubSkillSpec("go@spf13/go-skills") {
		t.Errorf("expected isGitHubSkillSpec(go@spf13/go-skills) to be false")
	}
	if !isGitHubSkillSpec("owner/repo@v1.0.0") {
		t.Errorf("expected isGitHubSkillSpec(owner/repo@v1.0.0) to be true")
	}
	if !isGitHubSkillSpec("danicat/kungfu") {
		t.Errorf("expected isGitHubSkillSpec(danicat/kungfu) to be true")
	}

	skillDir := filepath.Join(tmpDir, "src-go-skill")
	refsDir := filepath.Join(skillDir, "references")
	_ = os.MkdirAll(refsDir, 0755)
	goBody := "---\nname: go\ndescription: Idiomatic Go from spf13\n---\n# Idiomatic Go\nGo body.\n"
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(goBody), 0644)
	_ = os.WriteFile(filepath.Join(refsDir, "guide.md"), []byte("# Go Style Guide\nDetails.\n"), 0644)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{CacheDir: t.TempDir()}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}
	app.Store.AddCatalogManifest("spf13/go-skills", &catalog.Manifest{
		Items: []catalog.Item{{
			ID:          "go",
			Name:        "go",
			Description: "Idiomatic Go from spf13",
			Path:        skillDir,
			Body:        goBody,
		}},
	})

	// 1. load go@spf13/go-skills (slashed catalog name must not split as subpath)
	if code := app.RunLoad([]string{"go@spf13/go-skills"}); code != ExitSuccess {
		t.Fatalf("RunLoad(go@spf13/go-skills) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Idiomatic Go") {
		t.Errorf("unexpected output: %s", stdout.String())
	}

	// 2. load go@go-skills (unique short repo suffix)
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"go@go-skills"}); code != ExitSuccess {
		t.Fatalf("RunLoad(go@go-skills) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Idiomatic Go") {
		t.Errorf("unexpected output: %s", stdout.String())
	}

	// 3. load go/references/guide.md@spf13/go-skills and @go-skills
	for _, spec := range []string{"go/references/guide.md@spf13/go-skills", "go/references/guide.md@go-skills"} {
		stdout.Reset()
		stderr.Reset()
		if code := app.RunLoad([]string{spec}); code != ExitSuccess {
			t.Fatalf("RunLoad(%s) = %d; stderr: %s", spec, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "# Go Style Guide") {
			t.Errorf("RunLoad(%s) unexpected output: %s", spec, stdout.String())
		}
	}

	// 4. show and learn with slashed catalog name go@spf13/go-skills
	stdout.Reset()
	stderr.Reset()
	if code := app.RunShow([]string{"go@spf13/go-skills"}); code != ExitSuccess {
		t.Fatalf("RunShow(go@spf13/go-skills) = %d; stderr: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLearn([]string{"go@spf13/go-skills"}); code != ExitSuccess {
		t.Fatalf("RunLearn(go@spf13/go-skills) = %d; stderr: %s", code, stderr.String())
	}

	// 5. Ambiguous short repo suffix fails fast with ExitUsage
	app.Store.AddCatalogManifest("other/go-skills", &catalog.Manifest{
		Items: []catalog.Item{{
			ID:   "go",
			Name: "go",
			Body: "---\nname: go\n---\n# Other Go\n",
		}},
	})
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"go@go-skills"}); code != ExitUsage {
		t.Fatalf("expected ExitUsage for ambiguous go@go-skills, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "spf13/go-skills") || !strings.Contains(stderr.String(), "other/go-skills") {
		t.Errorf("expected both catalogs in ambiguity stderr, got: %s", stderr.String())
	}
}
