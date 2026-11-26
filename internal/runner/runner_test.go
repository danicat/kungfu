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

package runner_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
)

func TestMain(m *testing.M) {
	// Preserve Go's build cache before isolating HOME so helper binary builds stay fast.
	if os.Getenv("GOCACHE") == "" {
		if userCache, err := os.UserCacheDir(); err == nil && userCache != "" {
			_ = os.Setenv("GOCACHE", filepath.Join(userCache, "go-build"))
		}
	}
	tempHome, err := os.MkdirTemp("", "kungfu-runner-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp test home: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", tempHome)
	_ = os.Setenv("USERPROFILE", tempHome)
	_ = os.Setenv("APPDATA", filepath.Join(tempHome, "AppData", "Roaming"))
	_ = os.Setenv("LOCALAPPDATA", filepath.Join(tempHome, "AppData", "Local"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tempHome, ".config"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(tempHome, ".cache"))
	_ = os.Setenv("KUNGFU_CACHE_DIR", filepath.Join(tempHome, ".cache", "kungfu"))

	code := m.Run()
	_ = os.RemoveAll(tempHome)
	os.Exit(code)
}

func TestRunScript_ShellScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	t.Parallel()

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
		Name: "test-skill",
		Path: tmpDir,
	}

	r := runner.New(t.TempDir(), nil)
	var stdout, stderr bytes.Buffer
	err := r.RunScriptContext(context.Background(), item, "hello.sh", []string{"World"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunScriptContext failed: %v (stderr: %s)", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Hello World") {
		t.Errorf("expected Hello World in output, got: %q", stdout.String())
	}
}

func TestRunScript_CompiledBinaryDirectExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable test")
	}
	t.Parallel()

	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}

	// Build a minimal extensionless native binary into scripts/mytool.
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not found in PATH")
	}
	srcFile := filepath.Join(t.TempDir(), "main.go")
	_ = os.WriteFile(srcFile, []byte("package main\nimport \"os\"\nfunc main() { _, _ = os.Stdout.WriteString(\"BIN_OK \" + os.Args[1] + \"\\n\") }\n"), 0644)
	binPath := filepath.Join(scriptsDir, "mytool")
	buildCmd := exec.Command(goBin, "build", "-o", binPath, srcFile)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build helper binary: %v\n%s", err, out)
	}

	item := catalog.Item{ID: "bin-skill", Name: "bin-skill", Path: tmpDir}
	r := runner.New(t.TempDir(), nil)
	var stdout, stderr bytes.Buffer
	if err := r.RunScriptContext(context.Background(), item, "mytool", []string{"42"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("RunScriptContext(binary) failed: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "BIN_OK 42") {
		t.Errorf("expected BIN_OK 42, got %q", stdout.String())
	}
}

func TestRunScript_PythonScript(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("uv"); err != nil {
		if _, err := exec.LookPath("python3"); err != nil {
			if _, err := exec.LookPath("python"); err != nil {
				t.Skip("no python runtime (uv/python3/python) found in PATH")
			}
		}
	}

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
		Name: "python-skill",
		Path: tmpDir,
	}

	r := runner.New(t.TempDir(), nil)
	var stdout, stderr bytes.Buffer
	err := r.RunScriptContext(context.Background(), item, "test_script.py", []string{"kungfu"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunScriptContext(python) failed: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PyArg:kungfu") {
		t.Errorf("expected PyArg:kungfu in output, got: %q", stdout.String())
	}
}

func TestRunScript_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell sleep test")
	}
	t.Parallel()

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
		Name: "slow-skill",
		Path: tmpDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	r := runner.New(t.TempDir(), nil)
	var stdout, stderr bytes.Buffer
	err := r.RunScriptContext(ctx, item, "slow.sh", nil, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, runner.ErrExecutionTimeout) {
		t.Errorf("expected ErrExecutionTimeout, got: %v", err)
	}
}

func TestRunScript_PathTraversalAndNotFound(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	item := catalog.Item{
		ID:   "test-skill",
		Name: "test-skill",
		Path: tmpDir,
	}
	r := runner.New(t.TempDir(), nil)
	var stdout, stderr bytes.Buffer

	for _, bad := range []string{"../../bin/sh", "/bin/sh", `C:\cmd.exe`, "scripts/../../evil.sh"} {
		err := r.RunScriptContext(context.Background(), item, bad, nil, nil, &stdout, &stderr)
		if !errors.Is(err, runner.ErrInvalidPath) {
			t.Errorf("RunScriptContext(%q) = %v, want ErrInvalidPath", bad, err)
		}
	}

	err := r.RunScriptContext(context.Background(), item, "nonexistent.sh", nil, nil, &stdout, &stderr)
	if !errors.Is(err, runner.ErrScriptNotFound) {
		t.Errorf("expected ErrScriptNotFound, got: %v", err)
	}
}

func TestReadFile(t *testing.T) {
	t.Parallel()

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
		Name: "test-skill",
		Path: tmpDir,
	}
	r := runner.New(t.TempDir(), nil)
	ctx := context.Background()

	// 1. Exact file path in root
	c1, err := r.ReadFileContext(ctx, item, "CANONICAL.md", nil)
	if err != nil || c1 != "# Canonical Spec" {
		t.Fatalf("ReadFileContext CANONICAL.md failed: %v, got: %q", err, c1)
	}

	// 2. Exact file path in subdirectory
	c2, err := r.ReadFileContext(ctx, item, "references/guide.md", nil)
	if err != nil || c2 != "# Guide Content" {
		t.Fatalf("ReadFileContext references/guide.md failed: %v, got: %q", err, c2)
	}

	// 3. Exact script file
	c3, err := r.ReadFileContext(ctx, item, "scripts/deploy.sh", nil)
	if err != nil || !strings.Contains(c3, "deployed") {
		t.Fatalf("ReadFileContext scripts/deploy.sh failed: %v, got: %q", err, c3)
	}

	// 4. Non-existent file
	_, err = r.ReadFileContext(ctx, item, "missing.md", nil)
	if !errors.Is(err, runner.ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound, got: %v", err)
	}

	// 5. Path traversal rejection
	_, err = r.ReadFileContext(ctx, item, "../../../../etc/passwd", nil)
	if !errors.Is(err, runner.ErrInvalidPath) {
		t.Fatalf("expected ErrInvalidPath, got: %v", err)
	}

	// 6. Binary file rejection
	_ = os.WriteFile(filepath.Join(tmpDir, "image.png"), []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}, 0644)
	_, err = r.ReadFileContext(ctx, item, "image.png", nil)
	if !errors.Is(err, runner.ErrBinaryFile) {
		t.Fatalf("expected ErrBinaryFile, got: %v", err)
	}
}

func TestReadFile_Remote(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/writing/mock-skill/references/linkedin_playbook.md":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# LinkedIn Playbook Remote Content"))
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
	r := runner.New(t.TempDir(), server.Client())
	ctx := context.Background()

	c1, err := r.ReadFileContext(ctx, item, "references/linkedin_playbook.md", server.Client())
	if err != nil || c1 != "# LinkedIn Playbook Remote Content" {
		t.Fatalf("expected remote file success, got content %q, err: %v", c1, err)
	}

	c2, err := r.ReadFileContext(ctx, item, "CANONICAL.md", server.Client())
	if err != nil || c2 != "# Remote Canonical" {
		t.Fatalf("expected remote ReadFileContext success, got content %q, err: %v", c2, err)
	}

	_, err = r.ReadFileContext(ctx, item, "missing.md", server.Client())
	if !errors.Is(err, runner.ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got: %v", err)
	}
}

func TestDetectInterpreter_ShebangsAndAbsolutePathSafety(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shebang test")
	}

	tmpDir := t.TempDir()
	scriptsDir := filepath.Join(tmpDir, "scripts")
	_ = os.MkdirAll(scriptsDir, 0755)

	// 1. env shebang
	_ = os.WriteFile(filepath.Join(scriptsDir, "shebang1.sh"), []byte("#!/usr/bin/env sh\necho ENV_OK\n"), 0755)

	// 2. Absolute shebang must NOT be hijacked by a fake "sh" placed earlier in PATH
	_ = os.WriteFile(filepath.Join(scriptsDir, "shebang2.sh"), []byte("#!/bin/sh\necho ABS_OK\n"), 0755)

	fakeBinDir := t.TempDir()
	fakeSh := filepath.Join(fakeBinDir, "sh")
	_ = os.WriteFile(fakeSh, []byte("#!/bin/sh\necho HIJACKED\n"), 0755)
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	item := catalog.Item{ID: "dummy", Name: "dummy", Path: tmpDir}
	r := runner.New(t.TempDir(), nil)

	var out, errBuf bytes.Buffer
	if err := r.RunScriptContext(context.Background(), item, "shebang2.sh", nil, nil, &out, &errBuf); err != nil {
		t.Fatalf("RunScriptContext(shebang2.sh) failed: %v (stderr: %s)", err, errBuf.String())
	}
	if strings.Contains(out.String(), "HIJACKED") || !strings.Contains(out.String(), "ABS_OK") {
		t.Errorf("absolute shebang was hijacked or failed: stdout=%q", out.String())
	}

	// Restore clean PATH for env shebang test
	t.Setenv("PATH", "/usr/bin:/bin")
	out.Reset()
	errBuf.Reset()
	if err := r.RunScriptContext(context.Background(), item, "shebang1.sh", nil, nil, &out, &errBuf); err != nil {
		t.Fatalf("RunScriptContext(shebang1.sh) failed: %v (stderr: %s)", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "ENV_OK") {
		t.Errorf("expected ENV_OK from env shebang, got %q", out.String())
	}
}

func TestInstallSkill_Local(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	destDir := filepath.Join(t.TempDir(), "installed_skill")

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

	r := runner.New(t.TempDir(), nil)
	files, err := r.InstallSkillContext(context.Background(), item, destDir, "", nil, nil)
	if err != nil {
		t.Fatalf("InstallSkillContext failed: %v", err)
	}

	if len(files) < 4 {
		t.Errorf("expected at least 4 files installed, got %d: %v", len(files), files)
	}

	for _, f := range []string{"SKILL.md", "references/playbook.md", "scripts/deploy.sh", "CANONICAL.md"} {
		path := filepath.Join(destDir, filepath.FromSlash(f))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected installed file %s at %s to exist, err: %v", f, path, err)
		}
	}
}

func TestInstallSkill_Remote(t *testing.T) {
	t.Parallel()

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

	r := runner.New(t.TempDir(), server.Client())
	files, err := r.InstallSkillContext(context.Background(), item, destDir, "", []string{"references/guide.md"}, server.Client())
	if err != nil {
		t.Fatalf("InstallSkillContext failed: %v", err)
	}
	if len(files) < 4 {
		t.Errorf("expected at least 4 files installed, got %d: %v", len(files), files)
	}

	expected := []string{"SKILL.md", "references/guide.md", "references/subguide.md", "scripts/run.py"}
	for _, f := range expected {
		path := filepath.Join(destDir, filepath.FromSlash(f))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected remote file %s to be installed at %s, err: %v", f, path, err)
		}
	}

	if runtime.GOOS != "windows" {
		scriptInfo, err := os.Stat(filepath.Join(destDir, "scripts", "run.py"))
		if err != nil {
			t.Fatalf("failed to stat script: %v", err)
		}
		if scriptInfo.Mode()&0111 == 0 {
			t.Errorf("expected script to be executable, mode: %v", scriptInfo.Mode())
		}
	}
}

func TestExtractCandidates(t *testing.T) {
	t.Parallel()

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
		if !slices.Contains(candidates, exp) {
			t.Errorf("expected candidate %q to be extracted, got candidates: %v", exp, candidates)
		}
	}
}

func TestRunScript_JITRemoteFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	t.Parallel()

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

	r := runner.New(t.TempDir(), server.Client())
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := r.RunScriptContext(ctx, item, "jit_task.sh", []string{"alpha"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("RunScriptContext failed with JIT fetch: %v (stderr: %s)", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "JIT_SUCCESS alpha") {
		t.Errorf("expected output to contain JIT_SUCCESS alpha, got: %q", stdout.String())
	}
}

func TestInstallSkillContext_UpgradesAndPrunesStaleFiles(t *testing.T) {
	t.Parallel()

	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := version.Load()
		switch r.URL.Path {
		case "/skills/upgrader/SKILL.md":
			w.WriteHeader(http.StatusOK)
			if v == 1 {
				_, _ = w.Write([]byte("# Upgrader v1\nSee [Guide](references/guide.md) and [Old](references/old.md).\n"))
			} else {
				_, _ = w.Write([]byte("# Upgrader v2\nSee [Guide](references/guide.md).\n"))
			}
		case "/skills/upgrader/references/guide.md":
			w.WriteHeader(http.StatusOK)
			if v == 1 {
				_, _ = w.Write([]byte("guide v1"))
			} else {
				_, _ = w.Write([]byte("guide v2"))
			}
		case "/skills/upgrader/references/old.md":
			if v == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("old v1"))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := runner.New(t.TempDir(), server.Client())
	destDir := filepath.Join(t.TempDir(), "upgrader")
	item := catalog.Item{
		ID:   "upgrader",
		Name: "upgrader",
		URL:  server.URL + "/skills/upgrader/SKILL.md",
	}

	ctx := context.Background()
	filesV1, err := r.InstallSkillContext(ctx, item, destDir, "", nil, server.Client())
	if err != nil {
		t.Fatalf("v1 InstallSkillContext failed: %v", err)
	}
	if len(filesV1) != 3 {
		t.Fatalf("expected 3 files in v1, got %v", filesV1)
	}

	version.Store(2)
	filesV2, err := r.InstallSkillContext(ctx, item, destDir, "", nil, server.Client())
	if err != nil {
		t.Fatalf("v2 InstallSkillContext failed: %v", err)
	}
	if len(filesV2) != 2 {
		t.Fatalf("expected 2 files in v2, got %v", filesV2)
	}

	guideBytes, err := os.ReadFile(filepath.Join(destDir, "references", "guide.md"))
	if err != nil {
		t.Fatalf("failed reading upgraded guide.md: %v", err)
	}
	if string(guideBytes) != "guide v2" {
		t.Errorf("expected fresh remote guide.md content 'guide v2', got %q", string(guideBytes))
	}

	if _, err := os.Stat(filepath.Join(destDir, "references", "old.md")); !os.IsNotExist(err) {
		t.Errorf("expected stale file references/old.md to be pruned on upgrade, stat err: %v", err)
	}
}

func TestReadFile_StaleCacheDoesNotShadowRemote(t *testing.T) {
	t.Parallel()

	cacheRoot := t.TempDir()
	// Seed a stale cached skill with old SKILL.md and old reference.
	staleSkillDir, err := catalog.SkillCacheDir(cacheRoot, "drift-skill", "spf13/go-skills")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(staleSkillDir, "references"), 0755)
	_ = os.WriteFile(filepath.Join(staleSkillDir, "SKILL.md"), []byte("---\nname: drift-skill\n---\n# Old v1\n"), 0644)
	_ = os.WriteFile(filepath.Join(staleSkillDir, "references", "guide.md"), []byte("# Stale Cached Guide\n"), 0644)

	newSkillMD := "---\nname: drift-skill\n---\n# Fresh v2\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drift-skill/references/guide.md" {
			_, _ = w.Write([]byte("# Fresh Remote Guide v2\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	item := catalog.Item{
		ID:     "drift-skill",
		Name:   "drift-skill",
		Type:   catalog.ItemTypeSkill,
		Source: "spf13/go-skills",
		URL:    server.URL + "/drift-skill/SKILL.md",
		Digest: catalog.ComputeDigest([]byte(newSkillMD)),
	}

	r := runner.New(cacheRoot, server.Client())
	got, err := r.ReadFileContext(context.Background(), item, "references/guide.md", server.Client())
	if err != nil {
		t.Fatalf("ReadFileContext failed: %v", err)
	}
	if !strings.Contains(got, "# Fresh Remote Guide v2") {
		t.Errorf("stale cache shadowed fresh remote file! got: %q", got)
	}
}
