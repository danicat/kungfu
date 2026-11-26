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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/config"
)

func TestRunCatalog(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, ".cache"))
	t.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tmpDir, ".config", "kungfu"))
	t.Setenv("KUNGFU_STATE_PATH", filepath.Join(tmpDir, ".kungfu-state.json"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"name": "mycatalog",
			"skills": [
				{
					"name": "remoteskill",
					"description": "A remote test skill",
					"url": "https://example.com/skill/SKILL.md"
				}
			]
		}`))
	}))
	defer server.Close()
	t.Setenv("KUNGFU_URL", server.URL)

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{CacheDir: t.TempDir()}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. Add catalog and verify persistence in config.yaml
	stdout.Reset()
	stderr.Reset()
	code := app.RunCatalog([]string{"add", server.URL, "--name", "mock-cat"})
	if code != ExitSuccess {
		t.Fatalf("RunCatalog add = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `Added catalog "mock-cat"`) {
		t.Errorf("expected added catalog confirmation, got: %s", stdout.String())
	}
	reloadedCfg, err := config.Load("")
	if err != nil {
		t.Fatalf("config.Load after catalog add: %v", err)
	}
	if reloadedCfg.Catalogs["mock-cat"] != server.URL {
		t.Errorf("expected mock-cat persisted in config.yaml, got %q", reloadedCfg.Catalogs["mock-cat"])
	}

	// 1b. Adding duplicate catalog without -f must fail; with -f must succeed
	stdout.Reset()
	stderr.Reset()
	if code := app.RunCatalog([]string{"add", server.URL, "--name", "mock-cat"}); code != ExitUsage {
		t.Errorf("duplicate catalog add without -f = %d, want ExitUsage", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.RunCatalog([]string{"add", server.URL, "--name", "mock-cat", "-f"}); code != ExitSuccess {
		t.Errorf("duplicate catalog add with -f = %d, want ExitSuccess; stderr: %s", code, stderr.String())
	}

	// 1c. Invalid or reserved catalog names must be rejected
	for _, badName := range []string{"default", "kungfu", "../evil", "bad name", "bad@name"} {
		stdout.Reset()
		stderr.Reset()
		if code := app.RunCatalog([]string{"add", server.URL, "--name", badName}); code != ExitUsage {
			t.Errorf("catalog add --name %q = %d, want ExitUsage", badName, code)
		}
	}

	// 2. List catalogs
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"list"})
	if code != ExitSuccess {
		t.Fatalf("RunCatalog list = %d; stderr: %s", code, stderr.String())
	}
	listOut := stdout.String()
	if !strings.Contains(listOut, "mock-cat") || !strings.Contains(listOut, "kungfu") || !strings.Contains(listOut, "default") {
		t.Errorf("expected default, kungfu, and mock-cat in catalog list, got: %s", listOut)
	}
	if strings.Contains(listOut, "https://") || strings.Contains(listOut, "http://") {
		t.Errorf("expected catalog list to omit http(s):// prefix in SOURCE column, got: %s", listOut)
	}

	// 3. Synchronize all catalogs via RunUpdate (override built-ins to mock server so tests never hit live internet)
	app.Config.Catalogs["default"] = server.URL
	app.Config.Catalogs["kungfu"] = server.URL
	stdout.Reset()
	stderr.Reset()
	code = app.RunUpdate([]string{})
	if code != ExitSuccess {
		t.Fatalf("RunUpdate = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Synchronized") {
		t.Errorf("expected synchronized confirmation, got: %s", stdout.String())
	}

	// 4. Remove catalog and verify removal from config.yaml
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"remove", "mock-cat"})
	if code != ExitSuccess {
		t.Fatalf("RunCatalog remove = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `Removed catalog "mock-cat"`) {
		t.Errorf("expected removed confirmation, got: %s", stdout.String())
	}
	reloadedCfg, _ = config.Load("")
	if _, exists := reloadedCfg.Catalogs["mock-cat"]; exists {
		t.Errorf("expected mock-cat to be removed from config.yaml")
	}

	// 5. Attempt to remove built-in catalogs ("default" and "kungfu") must fail
	for _, builtin := range []string{"default", "kungfu"} {
		stdout.Reset()
		stderr.Reset()
		code = app.RunCatalog([]string{"remove", builtin})
		if code != ExitUsage || strings.Contains(stdout.String(), `Removed catalog "`+builtin+`"`) {
			t.Errorf("expected built-in catalog %q removal to be prevented with ExitUsage, got %d", builtin, code)
		}
	}

	// 6. Legacy catalog sync subcommand should fail with ExitUsage
	stdout.Reset()
	stderr.Reset()
	if code := app.RunCatalog([]string{"sync"}); code != ExitUsage {
		t.Fatalf("RunCatalog sync = %d, want %d", code, ExitUsage)
	}

	// 7. Missing local path starting with ./ should return ExitNotFound without hitting GitHub API
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"add", "./does-not-exist"})
	if code != ExitNotFound {
		t.Fatalf("RunCatalog add ./does-not-exist = %d, want %d; stderr: %s", code, ExitNotFound, stderr.String())
	}
	if strings.Contains(stderr.String(), "github") {
		t.Errorf("expected local path error, got github error: %s", stderr.String())
	}

	// 8. Existing regular file passed to catalog add should return ExitUsage
	regularFile := filepath.Join(tmpDir, "not-a-dir.txt")
	_ = os.WriteFile(regularFile, []byte("hello"), 0644)
	stdout.Reset()
	stderr.Reset()
	code = app.RunCatalog([]string{"add", regularFile})
	if code != ExitUsage {
		t.Fatalf("RunCatalog add regularFile = %d, want %d; stderr: %s", code, ExitUsage, stderr.String())
	}
	if strings.Contains(stderr.String(), "github") {
		t.Errorf("expected local file error, got github error: %s", stderr.String())
	}
}
