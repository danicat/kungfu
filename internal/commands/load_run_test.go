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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
)

func TestRunLoad(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"load missing arg", []string{"load"}, ExitUsage, ""},
		{"load embedded kungfu raw default", []string{"load", "kungfu"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load by category flag", []string{"load", "-c", "agents"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load by tags flag", []string{"load", "-t", "agent-skills AND cli"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load all flag", []string{"load", "--all"}, ExitSuccess, "# KungFu: Agent Skills Gateway"},
		{"load skill json", []string{"load", "kungfu", "--json"}, ExitSuccess, "\"name\": \"kungfu\""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr, testEmbeddedFS)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			if tt.wantSub != "" && !strings.Contains(stdout.String(), tt.wantSub) {
				t.Errorf("Run(%v) output %q does not contain %q; stderr: %s", tt.args, stdout.String(), tt.wantSub, stderr.String())
			}
		})
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
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr, testEmbeddedFS)
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
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr, testEmbeddedFS)
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
