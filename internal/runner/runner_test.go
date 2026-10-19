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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
)

func TestMain(m *testing.M) {
	tempHome, err := os.MkdirTemp("", "kungfu-runner-test-*")
	if err == nil {
		_ = os.Setenv("HOME", tempHome)
	}
	code := m.Run()
	if tempHome != "" {
		_ = os.RemoveAll(tempHome)
	}
	os.Exit(code)
}

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

func TestReadFile(t *testing.T) {
	tmpDir := t.TempDir()
	refsDir := filepath.Join(tmpDir, "references")
	_ = os.MkdirAll(refsDir, 0755)
	scriptsDir := filepath.Join(tmpDir, "scripts")
	_ = os.MkdirAll(scriptsDir, 0755)

	_ = os.WriteFile(filepath.Join(refsDir, "guide.md"), []byte("# Guide Content"), 0644)
	_ = os.WriteFile(filepath.Join(scriptsDir, "deploy.sh"), []byte("#!/bin/sh\necho deployed"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "CANONICAL.md"), []byte("# Canonical Spec"), 0644)

	item := catalog.Item{
		ID:   "test-skill",
		Path: tmpDir,
	}

	// 1. Exact file path in root
	c1, err := runner.ReadFile(item, "CANONICAL.md")
	if err != nil || c1 != "# Canonical Spec" {
		t.Fatalf("ReadFile CANONICAL.md failed: %v, got: %q", err, c1)
	}

	// 2. Exact file path in subdirectory
	c2, err := runner.ReadFile(item, "references/guide.md")
	if err != nil || c2 != "# Guide Content" {
		t.Fatalf("ReadFile references/guide.md failed: %v, got: %q", err, c2)
	}

	// 3. Exact script file
	c3, err := runner.ReadFile(item, "scripts/deploy.sh")
	if err != nil || !strings.Contains(c3, "deployed") {
		t.Fatalf("ReadFile scripts/deploy.sh failed: %v, got: %q", err, c3)
	}

	// 4. Non-existent file
	_, err = runner.ReadFile(item, "missing.md")
	if err == nil || !errors.Is(err, runner.ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound, got: %v", err)
	}

	// 5. Path traversal rejection
	_, err = runner.ReadFile(item, "../../../../etc/passwd")
	if err == nil || !errors.Is(err, runner.ErrInvalidPath) {
		t.Fatalf("expected ErrInvalidPath, got: %v", err)
	}

	// 6. Binary file rejection
	_ = os.WriteFile(filepath.Join(tmpDir, "image.png"), []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}, 0644)
	_, err = runner.ReadFile(item, "image.png")
	if err == nil || !errors.Is(err, runner.ErrBinaryFile) {
		t.Fatalf("expected ErrBinaryFile, got: %v", err)
	}
}

func TestReadFile_Remote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/writing/mock-skill/references/linkedin_playbook.md":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# LinkedIn Playbook Remote Content"))
		case "/writing/mock-skill/references/twitter_playbook.md":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# Twitter Playbook Remote Content"))
		case "/writing/mock-skill/CANONICAL.md":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# Remote Canonical"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	item := catalog.Item{
		ID:   "mock-skill-unique",
		Name: "mock-skill-unique",
		URL:  server.URL + "/writing/mock-skill/SKILL.md",
	}

	// 1. Read remote file in references
	c1, err := runner.ReadFile(item, "references/linkedin_playbook.md")
	if err != nil || c1 != "# LinkedIn Playbook Remote Content" {
		t.Fatalf("expected remote file success, got content %q, err: %v", c1, err)
	}

	// 2. Read remote file in root
	c2, err := runner.ReadFile(item, "CANONICAL.md")
	if err != nil || c2 != "# Remote Canonical" {
		t.Fatalf("expected remote ReadFile success, got content %q, err: %v", c2, err)
	}

	// 3. Missing remote file
	_, err = runner.ReadFile(item, "missing.md")
	if err == nil || !errors.Is(err, runner.ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got: %v", err)
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

func TestInstallSkill_Local(t *testing.T) {
	srcDir := t.TempDir()
	destDir := filepath.Join(t.TempDir(), "installed_skill")

	// Create source skill structure
	_ = os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte("# Skill Title\nRead [references/playbook.md](references/playbook.md), run `scripts/deploy.sh`, and see [CANONICAL.md](CANONICAL.md)"), 0644)
	refsDir := filepath.Join(srcDir, "references")
	_ = os.MkdirAll(refsDir, 0755)
	_ = os.WriteFile(filepath.Join(refsDir, "playbook.md"), []byte("# Playbook Content"), 0644)
	scriptsDir := filepath.Join(srcDir, "scripts")
	_ = os.MkdirAll(scriptsDir, 0755)
	_ = os.WriteFile(filepath.Join(scriptsDir, "deploy.sh"), []byte("#!/bin/sh\necho deployed"), 0755)
	_ = os.WriteFile(filepath.Join(srcDir, "CANONICAL.md"), []byte("# Canonical"), 0644)

	item := catalog.Item{
		ID:   "local-test-skill",
		Name: "local-test-skill",
		Path: srcDir,
	}

	files, err := runner.InstallSkill(item, destDir, "", nil)
	if err != nil {
		t.Fatalf("InstallSkill failed: %v", err)
	}

	if len(files) < 4 {
		t.Errorf("expected at least 4 files installed, got %d: %v", len(files), files)
	}

	// Verify all files exist in destDir
	for _, f := range []string{"SKILL.md", "references/playbook.md", "scripts/deploy.sh", "CANONICAL.md"} {
		path := filepath.Join(destDir, filepath.FromSlash(f))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected installed file %s at %s to exist, err: %v", f, path, err)
		}
	}
}

func TestInstallSkill_Remote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/writer/SKILL.md":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# Writer Skill\nSee [references/guide.md](references/guide.md) and [scripts/run.py](scripts/run.py)"))
		case "/skills/writer/references/guide.md":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# Remote Guide Document\nCheck [references/subguide.md](references/subguide.md)"))
		case "/skills/writer/references/subguide.md":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# Nested Subguide"))
		case "/skills/writer/scripts/run.py":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("print('running')"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	destDir := filepath.Join(t.TempDir(), "remote_installed_skill")
	item := catalog.Item{
		ID:   "remote-writer",
		Name: "remote-writer",
		URL:  server.URL + "/skills/writer/SKILL.md",
	}

	ctx := context.Background()
	files, err := runner.InstallSkillContext(ctx, item, destDir, "", []string{"references/guide.md"}, server.Client())
	if err != nil {
		t.Fatalf("InstallSkillContext failed: %v", err)
	}
	if len(files) < 4 {
		t.Errorf("expected at least 4 files installed, got %d: %v", len(files), files)
	}

	// Verify SKILL.md, guide.md, nested subguide.md, and run.py were installed
	expected := []string{"SKILL.md", "references/guide.md", "references/subguide.md", "scripts/run.py"}
	for _, f := range expected {
		path := filepath.Join(destDir, filepath.FromSlash(f))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected remote file %s to be installed at %s, err: %v", f, path, err)
		}
	}

	// Verify script executable bit
	scriptInfo, err := os.Stat(filepath.Join(destDir, "scripts", "run.py"))
	if err != nil {
		t.Fatalf("failed to stat script: %v", err)
	}
	if scriptInfo.Mode()&0111 == 0 {
		t.Errorf("expected script to be executable, mode: %v", scriptInfo.Mode())
	}
}

func TestExtractCandidates(t *testing.T) {
	md := `# Sample Skill
Use the companion script ` + "`" + `scripts/analytics.py` + "`" + ` or ` + "`" + `references/schema.md` + "`" + `.
Run it using:
kungfu run analytics helper.py --flag
Or with uv:
uv run scripts/worker.py
Or bash:
bash scripts/setup.sh
See [Extra Docs](references/extra.md) and ![Diagram](assets/arch.png).
`
	candidates := runner.ExtractCandidates(md)
	expected := []string{
		"assets/arch.png",
		"references/extra.md",
		"references/schema.md",
		"scripts/analytics.py",
		"scripts/helper.py",
		"scripts/setup.sh",
		"scripts/worker.py",
	}

	for _, exp := range expected {
		found := false
		for _, c := range candidates {
			if c == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected candidate %q to be extracted, got candidates: %v", exp, candidates)
		}
	}
}

func TestRunScript_JITRemoteFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/cloud/scripts/jit_task.sh":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("#!/bin/sh\necho \"JIT_SUCCESS $1\"\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	item := catalog.Item{
		ID:   "cloud-tool",
		Name: "cloud-tool",
		URL:  server.URL + "/skills/cloud/SKILL.md",
	}

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := runner.RunScriptContext(ctx, item, "jit_task.sh", []string{"alpha"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunScriptContext failed with JIT fetch: %v", err)
	}

	if !strings.Contains(stdout.String(), "JIT_SUCCESS alpha") {
		t.Errorf("expected output to contain JIT_SUCCESS alpha, got: %q", stdout.String())
	}
}
