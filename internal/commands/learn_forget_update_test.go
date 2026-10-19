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
)

func startMockKungfuServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
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
					"description": "agent skills gateway",
					"category": "agents",
					"url": "%s/skills/kungfu/SKILL.md",
					"digest": "%s"
				}
			]
		}`, server.URL, catalog.ComputeDigest([]byte(skillContent)))))
	}))
	return server, skillContent
}

func TestRunLearnAndForget(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("KUNGFU_STATE_PATH", filepath.Join(tmpDir, ".kungfu-state.json"))

	server, _ := startMockKungfuServer(t)
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. Learn skill into workspace
	stdout.Reset()
	stderr.Reset()
	code := app.RunLearn([]string{"kungfu"})
	if code != ExitSuccess {
		t.Fatalf("RunLearn(kungfu) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Learned skill \"kungfu\"") {
		t.Errorf("expected learned confirmation, got: %s", stdout.String())
	}

	// Verify file exists on disk
	installedMD := filepath.Join(tmpDir, ".agents", "skills", "kungfu", "SKILL.md")
	if _, err := os.Stat(installedMD); err != nil {
		t.Fatalf("expected installed SKILL.md at %s, got: %v", installedMD, err)
	}

	// 2. Status shows skill installed
	stdout.Reset()
	stderr.Reset()
	code = app.RunStatus([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunStatus = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "kungfu") {
		t.Errorf("expected kungfu in status output, got: %s", stdout.String())
	}

	// 3. Forget skill
	stdout.Reset()
	stderr.Reset()
	code = app.RunForget([]string{"kungfu"})
	if code != ExitSuccess {
		t.Fatalf("RunForget(kungfu) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Forgot skill \"kungfu\"") {
		t.Errorf("expected forgot confirmation, got: %s", stdout.String())
	}

	// Verify file deleted
	if _, err := os.Stat(installedMD); !os.IsNotExist(err) {
		t.Errorf("expected %s to be removed, but stat returned: %v", installedMD, err)
	}
}

func TestRunUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("KUNGFU_STATE_PATH", filepath.Join(tmpDir, ".kungfu-state.json"))

	server, _ := startMockKungfuServer(t)
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// Learn skill first
	code := app.RunLearn([]string{"kungfu"})
	if code != ExitSuccess {
		t.Fatalf("RunLearn = %d; stderr: %s", code, stderr.String())
	}

	// Check update (dry-run)
	stdout.Reset()
	stderr.Reset()
	code = app.RunUpdate([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunUpdate dry-run = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "kungfu") {
		t.Errorf("expected kungfu in update check, got: %s", stdout.String())
	}
}

func TestRunUpdate_ConflictAndForce(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("KUNGFU_STATE_PATH", filepath.Join(tmpDir, ".kungfu-state.json"))

	server, _ := startMockKungfuServer(t)
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. Learn skill
	if code := app.RunLearn([]string{"kungfu"}); code != ExitSuccess {
		t.Fatalf("RunLearn = %d; stderr: %s", code, stderr.String())
	}

	// 2. Modify local skill file to create conflict
	skillFilePath := filepath.Join(tmpDir, ".agents", "skills", "kungfu", "SKILL.md")
	if err := os.WriteFile(skillFilePath, []byte("# Kungfu modified locally"), 0644); err != nil {
		t.Fatalf("failed to write local modification: %v", err)
	}

	// 3. Dry-run update should note -f requirement
	stdout.Reset()
	stderr.Reset()
	if code := app.RunUpdate([]string{}); code != ExitSuccess {
		t.Fatalf("RunUpdate dry-run = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[Update available with -f]") {
		t.Errorf("expected '[Update available with -f]', got: %s", stdout.String())
	}

	// 4. Update without -f should fail visibly
	stdout.Reset()
	stderr.Reset()
	if code := app.RunUpdate([]string{"kungfu"}); code != ExitGeneral {
		t.Fatalf("expected RunUpdate without -f to fail with ExitGeneral, got %d", code)
	}
	if !strings.Contains(stderr.String(), "Local skill has been modified since install; pass -f/--force") {
		t.Errorf("expected conflict message in stderr, got: %s", stderr.String())
	}

	// 5. Update with -f should succeed
	stdout.Reset()
	stderr.Reset()
	if code := app.RunUpdate([]string{"-f", "kungfu"}); code != ExitSuccess {
		t.Fatalf("RunUpdate with -f failed = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Updated skill \"kungfu\"") {
		t.Errorf("expected successful update message, got: %s", stdout.String())
	}
}
