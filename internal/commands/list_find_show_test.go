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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/config"
)

func setupTestCatalogServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "kungfu",
					"type": "skill-md",
					"description": "Agent skills gateway and discovery hybrid tool",
					"tags": ["gateway", "discovery"],
					"url": "https://skills.danicat.dev/agents/kungfu/SKILL.md",
					"digest": "sha256:123"
				},
				{
					"name": "godoctor",
					"type": "skill-md",
					"description": "Go doctor",
					"tags": ["go", "linter"],
					"url": "https://skills.danicat.dev/coding/godoctor/SKILL.md",
					"digest": "sha256:456"
				}
			]
		}`))
	}))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUNGFU_CACHE_DIR", t.TempDir())
	t.Setenv("KUNGFU_URL", server.URL)
	return server
}

func TestRunList(t *testing.T) {
	server := setupTestCatalogServer(t)
	defer server.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		checkOut func(t *testing.T, stdout string)
	}{
		{
			name:     "list default human skill listing",
			args:     []string{"list"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "AVAILABLE SKILLS") {
					t.Errorf("list human missing header: %s", out)
				}
				if !strings.Contains(out, "kungfu") {
					t.Errorf("list human missing kungfu skill: %s", out)
				}
			},
		},
		{
			name:     "list json",
			args:     []string{"list", "--json"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				var items []struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal([]byte(out), &items); err != nil {
					t.Fatalf("failed to parse json output: %v, out: %s", err, out)
				}
				if len(items) == 0 {
					t.Errorf("expected non-empty items in json list")
				}
			},
		},
		{
			name:     "list rejects removed --raw flag",
			args:     []string{"list", "--raw"},
			wantCode: ExitUsage,
			checkOut: func(t *testing.T, out string) {},
		},
		{
			name:     "list rejects unexpected positional args",
			args:     []string{"list", "extra"},
			wantCode: ExitUsage,
			checkOut: func(t *testing.T, out string) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			tt.checkOut(t, stdout.String())
		})
	}
}

func TestRunFind(t *testing.T) {
	server := setupTestCatalogServer(t)
	defer server.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		checkOut func(t *testing.T, stdout string)
	}{
		{
			name:     "find missing query",
			args:     []string{"find"},
			wantCode: ExitUsage,
			checkOut: func(t *testing.T, out string) {},
		},
		{
			name:     "find hybrid search default human",
			args:     []string{"find", "gateway", "discovery"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "SEARCH RESULTS") {
					t.Errorf("expected SEARCH RESULTS in find results, got: %s", out)
				}
				if !strings.Contains(out, "kungfu") {
					t.Errorf("expected kungfu in find results, got: %s", out)
				}
			},
		},
		{
			name:     "find with limit shorthand",
			args:     []string{"find", "kungfu", "-n", "3"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "kungfu") {
					t.Errorf("expected kungfu in find results with -n, got: %s", out)
				}
			},
		},
		{
			name:     "find json output",
			args:     []string{"find", "kungfu", "--json"},
			wantCode: ExitSuccess,
			checkOut: func(t *testing.T, out string) {
				var res []struct {
					Name  string  `json:"name"`
					Score float32 `json:"score"`
				}
				if err := json.Unmarshal([]byte(out), &res); err != nil {
					t.Fatalf("failed to parse find json output: %v, out: %s", err, out)
				}
				if len(res) == 0 {
					t.Errorf("expected non-empty search results in json")
				}
			},
		},
		{
			name:     "find json empty returns ExitNotFound and []",
			args:     []string{"find", "nonexistentxyz999", "--json"},
			wantCode: ExitNotFound,
			checkOut: func(t *testing.T, out string) {
				if strings.TrimSpace(out) != "[]" {
					t.Errorf("expected [] for empty find --json, got: %q", out)
				}
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
			tt.checkOut(t, stdout.String())
		})
	}
}

func TestRunShow(t *testing.T) {
	server := setupTestCatalogServer(t)
	defer server.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantSub  string
	}{
		{"show missing arg", []string{"show"}, ExitUsage, ""},
		{"show single skill human default", []string{"show", "kungfu"}, ExitSuccess, "SKILL: kungfu"},
		{"show rejects removed --raw flag", []string{"show", "kungfu", "--raw"}, ExitUsage, ""},
		{"show skill json always emits array", []string{"show", "kungfu", "--json"}, ExitSuccess, `"name": "kungfu"`},
		{"show multiple skills", []string{"show", "kungfu", "godoctor"}, ExitSuccess, "SKILL: kungfu"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			if tt.wantSub != "" && !strings.Contains(stdout.String(), tt.wantSub) {
				t.Errorf("Run(%v) output %q does not contain %q", tt.args, stdout.String(), tt.wantSub)
			}
			if strings.Contains(stdout.String(), "Scripts:") {
				t.Errorf("Run(%v) unexpectedly listed Scripts from markdown text when no scripts/ directory exists on disk:\n%s", tt.args, stdout.String())
			}
		})
	}
}

func buildTestTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar WriteHeader(%s): %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar Write(%s): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func TestRunShow_ArchiveAndSkillMDScripts(t *testing.T) {
	archiveBytes := buildTestTarGz(t, map[string]string{
		"SKILL.md": `---
name: archiveskill
description: Packaged archive skill with real scripts
---
# Archive Skill

Example commands in prose that should not cause duplicates or false positives:
uv run ./scripts/outline.py
uv run scripts/fake_in_docs.py
python3 -m py_compile scripts/outline.py
`,
		"scripts/outline.py":      "print('outline')\n",
		"scripts/sub/helper.py":   "print('helper')\n",
		"scripts/go.mod":          "module example.com/test\n",
		"scripts/go.sum":          "\n",
		"scripts/visualizer.html": "<html></html>\n",
		"references/guide.md":     "# Guide\n",
		"references/sub/deep.md":  "# Deep\n",
	})
	archiveDigest := catalog.ComputeDigest(archiveBytes)

	skillMDContent := `---
name: doconlyskill
description: Single-file skill-md that mentions scripts in examples
---
# Doc-Only Skill

Run example:
uv run scripts/not_bundled.py
`
	skillMDDigest := catalog.ComputeDigest([]byte(skillMDContent))

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-skills/index.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
				"skills": [
					{
						"name": "archiveskill",
						"type": "archive",
						"description": "Packaged archive skill with real scripts",
						"url": "%s/.well-known/agent-skills/archiveskill.tar.gz",
						"digest": "%s"
					},
					{
						"name": "doconlyskill",
						"type": "skill-md",
						"description": "Single-file skill-md that mentions scripts in examples",
						"url": "%s/skills/doconlyskill/SKILL.md",
						"digest": "%s"
					}
				]
			}`, serverURL, archiveDigest, serverURL, skillMDDigest)
		case "/.well-known/agent-skills/archiveskill.tar.gz":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(archiveBytes)
		case "/skills/doconlyskill/SKILL.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(skillMDContent))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	t.Setenv("KUNGFU_URL", server.URL+"/.well-known/agent-skills/index.json")

	// Seed a stale cache entry for doconlyskill to verify skill-md ignores stale cache scripts
	cacheDir := t.TempDir()
	staleScriptDir := filepath.Join(cacheDir, "skills", "doconlyskill", "scripts")
	_ = os.MkdirAll(staleScriptDir, 0755)
	_ = os.WriteFile(filepath.Join(staleScriptDir, "stale.py"), []byte("print('stale')\n"), 0644)

	// 1. Verify archive skill unpacks and lists only real scripts and references (JSON array)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--cache-dir", cacheDir, "show", "archiveskill", "--json"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(show archiveskill) = %d; stderr: %s", code, stderr.String())
	}
	var archiveDetails []struct {
		Name       string   `json:"name"`
		Scripts    []string `json:"scripts"`
		References []string `json:"references"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &archiveDetails); err != nil {
		t.Fatalf("json.Unmarshal(archiveskill): %v; out=%s", err, stdout.String())
	}
	if len(archiveDetails) != 1 {
		t.Fatalf("expected 1 ShowDetail element, got %d", len(archiveDetails))
	}
	wantScripts := []string{"outline.py", "sub/helper.py"}
	if !reflect.DeepEqual(archiveDetails[0].Scripts, wantScripts) {
		t.Errorf("archiveskill scripts = %v, want %v", archiveDetails[0].Scripts, wantScripts)
	}
	wantRefs := []string{"guide", "sub/deep"}
	if !reflect.DeepEqual(archiveDetails[0].References, wantRefs) {
		t.Errorf("archiveskill references = %v, want %v", archiveDetails[0].References, wantRefs)
	}

	// 2. Verify skill-md item reports no scripts even when SKILL.md mentions scripts or stale cache exists
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"--cache-dir", cacheDir, "show", "doconlyskill", "--json"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(show doconlyskill) = %d; stderr: %s", code, stderr.String())
	}
	var docDetails []struct {
		Name       string   `json:"name"`
		Scripts    []string `json:"scripts"`
		References []string `json:"references"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &docDetails); err != nil {
		t.Fatalf("json.Unmarshal(doconlyskill): %v; out=%s", err, stdout.String())
	}
	if len(docDetails) != 1 {
		t.Fatalf("expected 1 ShowDetail element, got %d", len(docDetails))
	}
	if len(docDetails[0].Scripts) != 0 {
		t.Errorf("doconlyskill scripts = %v, want empty", docDetails[0].Scripts)
	}
	if len(docDetails[0].References) != 0 {
		t.Errorf("doconlyskill references = %v, want empty", docDetails[0].References)
	}
}

type rewriteTransport struct {
	base      http.RoundTripper
	targetURL string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(rt.targetURL, "http://")
	return rt.base.RoundTrip(clone)
}

func TestGitHubSkillSpec_ShowAndLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	skillMD := "---\nname: evals\ndescription: Evaluation quickstart skill\n---\n# Evals Playbook\nRun evaluation suites.\n"

	var ghHits atomic.Int32
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghHits.Add(1)
		if r.URL.Path == "/anthropics/anthropic-quickstarts/HEAD/evals/SKILL.md" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(skillMD))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ghServer.Close()

	catServer := setupTestCatalogServer(t)
	defer catServer.Close()

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{CacheDir: t.TempDir()}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	app.HTTPClient = &http.Client{
		Transport: &rewriteTransport{
			base:      ghServer.Client().Transport,
			targetURL: ghServer.URL,
		},
	}

	// 1. show anthropics/anthropic-quickstarts/evals
	code := app.RunShow([]string{"anthropics/anthropic-quickstarts/evals"})
	if code != ExitSuccess {
		t.Fatalf("RunShow(anthropics/anthropic-quickstarts/evals) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "SKILL: evals") {
		t.Errorf("expected SKILL: evals in show output, got: %s", stdout.String())
	}

	// 2. load anthropics/anthropic-quickstarts/evals
	stdout.Reset()
	stderr.Reset()
	code = app.RunLoad([]string{"anthropics/anthropic-quickstarts/evals"})
	if code != ExitSuccess {
		t.Fatalf("RunLoad(anthropics/anthropic-quickstarts/evals) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Evals Playbook") {
		t.Errorf("expected # Evals Playbook in load output, got: %s", stdout.String())
	}

	// 3. load with mistyped skill and bundled subpath should not probe GitHub and should suggest godoctor.
	stdout.Reset()
	stderr.Reset()
	ghHits.Store(0)
	code = app.RunLoad([]string{"godocter/references/arch.md"})
	if code != ExitNotFound {
		t.Fatalf("RunLoad(godocter/references/arch.md) = %d, want %d", code, ExitNotFound)
	}
	if got := ghHits.Load(); got != 0 {
		t.Errorf("expected 0 GitHub HTTP requests for bundled subpath typo, got %d", got)
	}
	if !strings.Contains(stderr.String(), "kungfu load godoctor") {
		t.Errorf("expected suggestion 'kungfu load godoctor', got stderr: %s", stderr.String())
	}

	// 4. load with --local should never probe GitHub
	stdout.Reset()
	stderr.Reset()
	ghHits.Store(0)
	code = app.RunLoad([]string{"anthropics/anthropic-quickstarts/evals", "--local"})
	if code != ExitNotFound {
		t.Fatalf("RunLoad(--local) = %d, want %d", code, ExitNotFound)
	}
	if got := ghHits.Load(); got != 0 {
		t.Errorf("expected 0 GitHub HTTP requests with --local, got %d", got)
	}
}

func TestMultiCatalogFreshness_StaleRefreshOnDemandFindAndLocalDriftWarning(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))
	t.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tmpDir, ".config", "kungfu"))
	t.Setenv("KUNGFU_CACHE_DIR", cacheDir)

	// Default catalog serves godoctor v1.2.0 and a shadowed "go" skill.
	remoteGoDoctor := "---\nname: godoctor\ndescription: Remote v1.2.0\nmetadata:\n  version: \"1.2.0\"\n---\n# GoDoctor v1.2.0\n"
	var defSrv *httptest.Server
	defSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "godoctor/SKILL.md") {
			_, _ = w.Write([]byte(remoteGoDoctor))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "godoctor",
					"type": "skill-md",
					"version": "1.2.0",
					"description": "Remote v1.2.0",
					"url": "%s/godoctor/SKILL.md",
					"digest": "%s"
				},
				{
					"name": "go",
					"type": "skill-md",
					"description": "Default Go skill",
					"url": "%s/go/SKILL.md"
				}
			]
		}`, defSrv.URL, catalog.ComputeDigest([]byte(remoteGoDoctor)), defSrv.URL)
	}))
	defer defSrv.Close()
	t.Setenv("KUNGFU_URL", defSrv.URL)

	// Remote spf13/go-skills server serves fresh v2.0.0 "go" skill.
	var catHits atomic.Int32
	freshGoBody := "---\nname: go\ndescription: Fresh Go v2\nmetadata:\n  version: \"2.0.0\"\n---\n# Fresh Go v2\n"
	var goCatSrv *httptest.Server
	goCatSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SKILL.md") {
			_, _ = w.Write([]byte(freshGoBody))
			return
		}
		catHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "go",
					"type": "skill-md",
					"version": "2.0.0",
					"description": "Fresh Go v2 from spf13",
					"url": "%s/go/SKILL.md",
					"digest": "%s"
				}
			]
		}`, goCatSrv.URL, catalog.ComputeDigest([]byte(freshGoBody)))
	}))
	defer goCatSrv.Close()

	// Install an older local copy of godoctor v1.0.0 in workspace .agents/skills/godoctor.
	localGD := filepath.Join(tmpDir, ".agents", "skills", "godoctor")
	_ = os.MkdirAll(localGD, 0755)
	_ = os.WriteFile(filepath.Join(localGD, "SKILL.md"), []byte("---\nname: godoctor\nmetadata:\n  version: \"1.0.0\"\n---\n# Old Local GoDoctor\n"), 0644)

	// Seed an EXPIRED cache for spf13/go-skills (>24h old).
	if err := catalog.SaveNamedCatalogCache(cacheDir, "spf13/go-skills", goCatSrv.URL, catalog.Manifest{
		Items: []catalog.Item{{ID: "go", Name: "go", Version: "1.0.0", Description: "Stale Go v1"}},
	}, ""); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(cacheDir, "catalogs", catalog.SafeCatalogFileName("spf13/go-skills"))
	raw, _ := os.ReadFile(stalePath)
	_ = os.WriteFile(stalePath, []byte(strings.ReplaceAll(string(raw), `"fetched_at": "20`, `"fetched_at": "2020-01-01T00:00:00Z", "old_fetched_at": "20`)), 0600)

	cfgPath := filepath.Join(tmpDir, ".config", "kungfu", "config.yaml")
	cfg := config.DefaultConfig()
	cfg.Catalogs["spf13/go-skills"] = goCatSrv.URL
	if _, err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	var stdout, stderr bytes.Buffer
	app, err := NewApp(GlobalOptions{Config: cfgPath, CacheDir: cacheDir}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("NewApp failed: %v", err)
	}

	// 1. `list` warns about stale named catalog and shows source provenance in JSON + both shadowed "go" entries.
	if code := app.RunList([]string{"--json"}); code != ExitSuccess {
		t.Fatalf("RunList(--json) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "stale") || !strings.Contains(stderr.String(), "spf13/go-skills") {
		t.Errorf("expected stale catalog warning on stderr, got: %q", stderr.String())
	}
	var listItems []struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listItems); err != nil {
		t.Fatalf("unmarshal list --json: %v", err)
	}
	goCount := 0
	for _, it := range listItems {
		if it.Name == "go" && it.Source != "" {
			goCount++
		}
	}
	if goCount != 2 {
		t.Errorf("expected both shadowed 'go' skills with source in list --json, got %d (%+v)", goCount, listItems)
	}

	// 2. `find go@go-skills` auto-refreshes the stale spf13/go-skills catalog and returns fresh v2.0.0 with source.
	stdout.Reset()
	stderr.Reset()
	if code := app.RunFind([]string{"go@go-skills", "--json"}); code != ExitSuccess {
		t.Fatalf("RunFind(go@go-skills) = %d; stderr: %s", code, stderr.String())
	}
	if catHits.Load() != 1 {
		t.Errorf("expected stale spf13/go-skills to be auto-refreshed on find, hits=%d", catHits.Load())
	}
	if !strings.Contains(stdout.String(), "Fresh Go v2 from spf13") || !strings.Contains(stdout.String(), `"source": "spf13/go-skills"`) {
		t.Errorf("expected refreshed v2 skill + source in find output, got: %s", stdout.String())
	}

	// 3. `load godoctor` serves fresh remote v1.2.0 AND notes that local installed copy is older (1.0.0).
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"godoctor"}); code != ExitSuccess {
		t.Fatalf("RunLoad(godoctor) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# GoDoctor v1.2.0") {
		t.Errorf("expected fresh remote v1.2.0 content, got: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "1.0.0") || !strings.Contains(stderr.String(), "1.2.0") || !strings.Contains(stderr.String(), "kungfu update godoctor") {
		t.Errorf("expected local-copy-outdated note on stderr, got: %q", stderr.String())
	}

	// 4. `load godoctor --local` serves local copy AND warns that catalog has newer 1.2.0.
	stdout.Reset()
	stderr.Reset()
	if code := app.RunLoad([]string{"godoctor", "--local"}); code != ExitSuccess {
		t.Fatalf("RunLoad(godoctor --local) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Old Local GoDoctor") {
		t.Errorf("expected local content under --local, got: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warning: local installed") || !strings.Contains(stderr.String(), "1.2.0") {
		t.Errorf("expected outdated local warning on stderr, got: %q", stderr.String())
	}
}
