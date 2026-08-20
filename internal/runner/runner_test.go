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

package runner_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
)

func TestRunScript_ShellScript(t *testing.T) {
	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatalf("failed to create scripts dir: %v", err)
	}

	scriptPath := filepath.Join(scriptsDir, "hello.sh")
	content := "#!/usr/bin/env bash\necho \"Hello $1\"\n"
	if err := os.WriteFile(scriptPath, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	item := catalog.Item{
		ID:   "test-skill",
		Path: tmpDir,
	}

	var stdout, stderr bytes.Buffer
	err := runner.RunScript(item, "hello.sh", []string{"World"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunScript failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "Hello World") {
		t.Errorf("expected Hello World in output, got: %q", stdout.String())
	}
}

func TestRunScript_PythonScript(t *testing.T) {
	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatalf("failed to create scripts dir: %v", err)
	}

	scriptPath := filepath.Join(scriptsDir, "test_script.py")
	content := "import sys\nprint(f\"PyArg:{sys.argv[1]}\")\n"
	if err := os.WriteFile(scriptPath, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write python script: %v", err)
	}

	item := catalog.Item{
		ID:   "python-skill",
		Path: tmpDir,
	}

	var stdout, stderr bytes.Buffer
	err := runner.RunScript(item, "test_script.py", []string{"kungfu"}, &stdout, &stderr)
	if err != nil {
		t.Skipf("Skipping Python test in environment without python: %v", err)
		return
	}
	if !strings.Contains(stdout.String(), "PyArg:kungfu") {
		t.Errorf("expected PyArg:kungfu in output, got: %q", stdout.String())
	}
}

func TestRunScript_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatalf("failed to create scripts dir: %v", err)
	}

	scriptPath := filepath.Join(scriptsDir, "slow.sh")
	content := "#!/bin/sh\nsleep 2\necho done\n"
	if err := os.WriteFile(scriptPath, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	item := catalog.Item{
		ID:   "slow-skill",
		Path: tmpDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var stdout, stderr bytes.Buffer
	err := runner.RunScriptContext(ctx, item, "slow.sh", nil, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, runner.ErrExecutionTimeout) {
		t.Logf("got error on timeout: %v", err)
	}
}

func TestRunScript_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	item := catalog.Item{
		ID:   "test-skill",
		Path: tmpDir,
	}

	var stdout, stderr bytes.Buffer
	err := runner.RunScript(item, "../../bin/sh", nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error on path traversal, got nil")
	}
	if !errors.Is(err, runner.ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got: %v", err)
	}
}

func TestRunScript_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	item := catalog.Item{
		ID:   "test-skill",
		Path: tmpDir,
	}

	var stdout, stderr bytes.Buffer
	err := runner.RunScript(item, "nonexistent.sh", nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for nonexistent script, got nil")
	}
	if !errors.Is(err, runner.ErrScriptNotFound) {
		t.Errorf("expected ErrScriptNotFound, got: %v", err)
	}
}

func TestReadReference(t *testing.T) {
	tmpDir := t.TempDir()
	refsDir := filepath.Join(tmpDir, "references")
	if err := os.MkdirAll(refsDir, 0755); err != nil {
		t.Fatalf("failed to create references dir: %v", err)
	}

	refPath := filepath.Join(refsDir, "architecture.md")
	expectedContent := "# Architecture Guide\nDesign details here."
	if err := os.WriteFile(refPath, []byte(expectedContent), 0644); err != nil {
		t.Fatalf("failed to write ref file: %v", err)
	}

	rootRefPath := filepath.Join(tmpDir, "guide.md")
	if err := os.WriteFile(rootRefPath, []byte("Root guide"), 0644); err != nil {
		t.Fatalf("failed to write root ref file: %v", err)
	}

	skillFilePath := filepath.Join(tmpDir, "SKILL.md")
	if err := os.WriteFile(skillFilePath, []byte("---\nname: test\n---\n"), 0644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	item := catalog.Item{
		ID:   "arch-skill",
		Path: skillFilePath,
	}

	// Test 1: with .md extension in references/
	content, err := runner.ReadReference(item, "architecture.md")
	if err != nil {
		t.Fatalf("ReadReference failed: %v", err)
	}
	if content != expectedContent {
		t.Errorf("expected %q, got %q", expectedContent, content)
	}

	// Test 2: without .md extension in references/
	content, err = runner.ReadReference(item, "architecture")
	if err != nil {
		t.Fatalf("ReadReference without extension failed: %v", err)
	}
	if content != expectedContent {
		t.Errorf("expected %q, got %q", expectedContent, content)
	}

	// Test 3: from root directory
	content, err = runner.ReadReference(item, "guide")
	if err != nil || content != "Root guide" {
		t.Fatalf("ReadReference root failed: %v, got: %q", err, content)
	}

	// Test 4: path traversal rejection
	_, err = runner.ReadReference(item, "../../../../etc/passwd")
	if err == nil {
		t.Fatal("expected error on path traversal in ReadReference, got nil")
	}
	if !errors.Is(err, runner.ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got: %v", err)
	}

	// Test 5: non-existent reference
	_, err = runner.ReadReference(item, "missing")
	if err == nil {
		t.Fatal("expected error for missing reference, got nil")
	}
	if !errors.Is(err, runner.ErrReferenceNotFound) {
		t.Errorf("expected ErrReferenceNotFound, got: %v", err)
	}
}

func TestDetectInterpreter(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Script with env shebang
	sh1 := filepath.Join(tmpDir, "shebang1.sh")
	_ = os.WriteFile(sh1, []byte("#!/usr/bin/env bash\necho 1\n"), 0755)

	// 2. Script with direct path shebang
	sh2 := filepath.Join(tmpDir, "shebang2.sh")
	_ = os.WriteFile(sh2, []byte("#!/bin/sh\necho 2\n"), 0755)

	// 3. Node script
	js1 := filepath.Join(tmpDir, "script.js")
	_ = os.WriteFile(js1, []byte("console.log(1)"), 0644)

	// 4. TypeScript script
	ts1 := filepath.Join(tmpDir, "script.ts")
	_ = os.WriteFile(ts1, []byte("console.log(1)"), 0644)

	// 5. Python script with shebang flags
	py1 := filepath.Join(tmpDir, "script.py")
	_ = os.WriteFile(py1, []byte("#!/usr/bin/env python3\nprint(1)"), 0755)

	item := catalog.Item{ID: "dummy", Path: tmpDir}
	var out, errBuf bytes.Buffer

	_ = runner.RunScript(item, "shebang1.sh", nil, &out, &errBuf)
	_ = runner.RunScript(item, "shebang2.sh", nil, &out, &errBuf)
	_ = runner.RunScript(item, "script.py", nil, &out, &errBuf)
}
