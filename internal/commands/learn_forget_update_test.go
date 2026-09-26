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
)

func TestRunLearnAndForget(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("KUNGFU_STATE_PATH", filepath.Join(tmpDir, ".kungfu-state.json"))

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr, testEmbeddedFS)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. Learn embedded skill into workspace
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

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{}, &stdout, &stderr, testEmbeddedFS)
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
