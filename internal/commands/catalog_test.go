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

	"github.com/danicat/kungfu/internal/catalog"
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

func TestNewApp_IgnoresOrphanAndLegacyCatalogCachesAndUpdatePrunes(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, ".cache"))
	t.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tmpDir, ".config", "kungfu"))
	t.Setenv("KUNGFU_STATE_PATH", filepath.Join(tmpDir, ".kungfu-state.json"))

	manifestJSON := `{
		"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
		"skills": [
			{"name": "alpha", "description": "Alpha skill", "type": "skill-md", "url": "/alpha/SKILL.md", "digest": "sha256:aaa"}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(manifestJSON))
	}))
	defer srv.Close()

	// Create a local directory catalog so RunUpdate can sync both default, kungfu (via srv override), and team-skills offline/locally.
	localCatDir := filepath.Join(tmpDir, "team-skills")
	if err := os.MkdirAll(filepath.Join(localCatDir, "go"), 0755); err != nil {
		t.Fatalf("mkdir local catalog: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localCatDir, "go", "SKILL.md"), []byte("---\nname: go\ndescription: Go skill\n---\n# Go\n"), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	cfgPath := filepath.Join(tmpDir, ".config", "kungfu", "config.yaml")
	cfg := config.DefaultConfig()
	cfg.Catalogs["default"] = srv.URL + "/.well-known/agent-skills/index.json"
	cfg.Catalogs["kungfu"] = srv.URL + "/.well-known/agent-skills/index.json"
	cfg.Catalogs["team/go-skills"] = localCatDir
	if _, err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	cacheDir := filepath.Join(tmpDir, ".cache", "kungfu")
	catsDir, err := catalog.GetCatalogsCacheDir(cacheDir)
	if err != nil {
		t.Fatalf("GetCatalogsCacheDir: %v", err)
	}

	// Pre-populate:
	// 1. Valid configured catalog cache for team/go-skills
	// 2. Orphaned hashed cache for a catalog no longer in config.yaml
	// 3. Legacy unhashed v0.8.x cache file (team-go-skills.json)
	if err := catalog.SaveNamedCatalogCache(cacheDir, "team/go-skills", localCatDir, catalog.Manifest{
		Items: []catalog.Item{{ID: "go", Name: "go", Description: "Go skill"}},
	}, ""); err != nil {
		t.Fatalf("save configured cache: %v", err)
	}
	if err := catalog.SaveNamedCatalogCache(cacheDir, "removed/orphan", "https://example.com/orphan", catalog.Manifest{
		Items: []catalog.Item{{ID: "orphan-skill", Name: "orphan-skill", Description: "Removed catalog skill"}},
	}, ""); err != nil {
		t.Fatalf("save orphan cache: %v", err)
	}
	legacyPath := filepath.Join(catsDir, "team-go-skills.json")
	if err := os.WriteFile(legacyPath, []byte(`{"fetched_at":"2026-01-01T00:00:00Z","manifest":{"items":[{"id":"go","name":"go","description":"Legacy duplicate"}]}}`), 0600); err != nil {
		t.Fatalf("write legacy cache: %v", err)
	}

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{Config: cfgPath, CacheDir: cacheDir}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. List must not show orphan-skill or legacy team-go-skills duplicate, and must not emit stale warning for legacy file.
	if code := app.RunList(nil); code != ExitSuccess {
		t.Fatalf("RunList = %d, want %d; stderr=%s", code, ExitSuccess, stderr.String())
	}
	listOut := stdout.String()
	if strings.Contains(listOut, "orphan-skill") {
		t.Errorf("RunList leaked skill from unconfigured orphan cache:\n%s", listOut)
	}
	if strings.Contains(listOut, "go@team-go-skills") {
		t.Errorf("RunList leaked duplicate from legacy unhashed cache:\n%s", listOut)
	}
	if strings.Contains(stderr.String(), "team-go-skills") {
		t.Errorf("RunList emitted stale warning for ignored legacy cache:\n%s", stderr.String())
	}

	// 2. RunUpdate must prune both legacyPath and the orphaned catalog cache file while keeping configured caches.
	stdout.Reset()
	stderr.Reset()
	if code := app.RunUpdate(nil); code != ExitSuccess {
		t.Fatalf("RunUpdate = %d, want %d; stdout=%s stderr=%s", code, ExitSuccess, stdout.String(), stderr.String())
	}

	orphanPath := filepath.Join(catsDir, catalog.SafeCatalogFileName("removed/orphan"))
	for _, gone := range []string{legacyPath, orphanPath} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("expected RunUpdate to prune %s, stat err = %v", gone, err)
		}
	}
	for _, keep := range []string{"default", "kungfu", "team/go-skills"} {
		keepPath := filepath.Join(catsDir, catalog.SafeCatalogFileName(keep))
		if _, err := os.Stat(keepPath); err != nil {
			t.Errorf("expected RunUpdate to keep %s (%s), stat err = %v", keep, keepPath, err)
		}
	}
}
