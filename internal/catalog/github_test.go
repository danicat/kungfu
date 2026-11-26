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

package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGitHubTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input       string
		wantOwner   string
		wantRepo    string
		wantRef     string
		wantSubpath string
		wantOK      bool
	}{
		{
			input:     "https://github.com/anthropics/anthropic-quickstarts",
			wantOwner: "anthropics",
			wantRepo:  "anthropic-quickstarts",
			wantRef:   "HEAD",
			wantOK:    true,
		},
		{
			input:     "github.com/danicat/kungfu",
			wantOwner: "danicat",
			wantRepo:  "kungfu",
			wantRef:   "HEAD",
			wantOK:    true,
		},
		{
			input:       "anthropics/anthropic-quickstarts/evals",
			wantOwner:   "anthropics",
			wantRepo:    "anthropic-quickstarts",
			wantRef:     "HEAD",
			wantSubpath: "evals",
			wantOK:      true,
		},
		{
			input:       "https://github.com/danicat/skills/tree/dev/coding/godoctor",
			wantOwner:   "danicat",
			wantRepo:    "skills",
			wantRef:     "dev",
			wantSubpath: "coding/godoctor",
			wantOK:      true,
		},
		{
			input:       "https://github.com/danicat/skills/blob/v1.0.0/coding/godoctor/SKILL.md",
			wantOwner:   "danicat",
			wantRepo:    "skills",
			wantRef:     "v1.0.0",
			wantSubpath: "coding/godoctor",
			wantOK:      true,
		},
		{
			input:     "owner/repo@v2.1.0",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantRef:   "v2.1.0",
			wantOK:    true,
		},
		{
			input:     "owner/repo#branch-name",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantRef:   "branch-name",
			wantOK:    true,
		},
		{
			input:     "https://github.com/danicat/kungfu.git",
			wantOwner: "danicat",
			wantRepo:  "kungfu",
			wantRef:   "HEAD",
			wantOK:    true,
		},
		{
			input:       "https://raw.githubusercontent.com/anthropics/anthropic-quickstarts/main/evals/SKILL.md",
			wantOwner:   "anthropics",
			wantRepo:    "anthropic-quickstarts",
			wantRef:     "main",
			wantSubpath: "evals",
			wantOK:      true,
		},
		{
			input:       "https://raw.githubusercontent.com/danicat/skills/v1.0.0/coding/godoctor/SKILL.md",
			wantOwner:   "danicat",
			wantRepo:    "skills",
			wantRef:     "v1.0.0",
			wantSubpath: "coding/godoctor",
			wantOK:      true,
		},
		{
			input:     "owner/repo@v1.2.3/",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantRef:   "v1.2.3",
			wantOK:    true,
		},
		{input: "invalid", wantOK: false},
		{input: "", wantOK: false},
		{input: "https://evil.example.com/owner/repo", wantOK: false},
		{input: "https://github.com/owner/repo?foo=bar", wantOK: false},
		{input: "owner/repo/../../etc/passwd", wantOK: false},
		{input: "owner/repo/sub?query=1", wantOK: false},
		{input: "-badowner/repo", wantOK: false},
		{input: "owner/repo@", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			target, ok := ParseGitHubTarget(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ParseGitHubTarget(%q) ok = %v, want %v", tc.input, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if target.Owner != tc.wantOwner {
				t.Errorf("Owner = %q, want %q", target.Owner, tc.wantOwner)
			}
			if target.Repo != tc.wantRepo {
				t.Errorf("Repo = %q, want %q", target.Repo, tc.wantRepo)
			}
			if target.Ref != tc.wantRef {
				t.Errorf("Ref = %q, want %q", target.Ref, tc.wantRef)
			}
			if target.Subpath != tc.wantSubpath {
				t.Errorf("Subpath = %q, want %q", target.Subpath, tc.wantSubpath)
			}

			// CanonicalSource must round-trip cleanly.
			roundTripped, rtOK := ParseGitHubTarget(target.CanonicalSource())
			if !rtOK || roundTripped.Owner != target.Owner || roundTripped.Repo != target.Repo || roundTripped.Ref != target.Ref || roundTripped.Subpath != target.Subpath {
				t.Errorf("CanonicalSource(%q) round-trip mismatch: got %+v (ok=%v), want %+v", target.CanonicalSource(), roundTripped, rtOK, target)
			}
		})
	}
}

func TestFetchGitHubSkillMock(t *testing.T) {
	t.Parallel()

	mockContent := `---
name: custom-evals
description: Custom evaluation harness
allowed-tools: [bash, python]
license: MIT
metadata:
  version: 1.2.0
  tags: eval, testing
---
# Custom Evals
Instructions here.
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/testowner/testrepo/main/skills/evals/SKILL.md" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(mockContent))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := server.Client()
	origTransport := client.Transport
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = server.Listener.Addr().String()
		return origTransport.RoundTrip(clone)
	})

	target := &GitHubTarget{
		Owner:   "testowner",
		Repo:    "testrepo",
		Ref:     "main",
		Subpath: "skills/evals",
	}

	item, err := FetchGitHubSkill(context.Background(), target, client)
	if err != nil {
		t.Fatalf("FetchGitHubSkill failed: %v", err)
	}
	if item.Name != "custom-evals" {
		t.Errorf("Name = %q, want custom-evals", item.Name)
	}
	if item.Description != "Custom evaluation harness" {
		t.Errorf("Description = %q", item.Description)
	}
	if item.Version != "1.2.0" {
		t.Errorf("Version = %q, want 1.2.0", item.Version)
	}
	if item.URL != "https://raw.githubusercontent.com/testowner/testrepo/main/skills/evals/SKILL.md" {
		t.Errorf("URL = %q", item.URL)
	}
	if item.Digest != ComputeDigest([]byte(mockContent)) {
		t.Errorf("Digest = %q, want %q", item.Digest, ComputeDigest([]byte(mockContent)))
	}
}

func TestDiscoverLocalCatalog(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	// Create skill 1
	skill1Dir := filepath.Join(tempDir, "skills", "skill-one")
	if err := os.MkdirAll(skill1Dir, 0755); err != nil {
		t.Fatalf("mkdir error: %v", err)
	}
	skill1Content := `---
name: skill-one
description: Skill one for testing
metadata:
  author: tester
  version: 1.0.0
  tags: go, test
---
# Skill One Body
`
	if err := os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(skill1Content), 0644); err != nil {
		t.Fatalf("write file error: %v", err)
	}

	// Create skill 2
	skill2Dir := filepath.Join(tempDir, "skills", "skill-two")
	if err := os.MkdirAll(skill2Dir, 0755); err != nil {
		t.Fatalf("mkdir error: %v", err)
	}
	skill2Content := `---
name: skill-two
description: Skill two for testing
metadata:
  author: tester2
  version: 2.0.0
---
# Skill Two Body
`
	if err := os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(skill2Content), 0644); err != nil {
		t.Fatalf("write file error: %v", err)
	}

	// Symlink escaping tempDir must be ignored.
	outsideDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(outsideDir, "SKILL.md"), []byte("---\nname: escaped\n---\n# Escaped\n"), 0644)
	_ = os.Symlink(filepath.Join(outsideDir, "SKILL.md"), filepath.Join(tempDir, "skills", "SKILL.md"))

	manifest, err := DiscoverLocalCatalog(tempDir)
	if err != nil {
		t.Fatalf("DiscoverLocalCatalog failed: %v", err)
	}

	if len(manifest.Items) != 2 {
		t.Fatalf("expected 2 items, got %d (%+v)", len(manifest.Items), manifest.Items)
	}

	itemsMap := make(map[string]Item)
	for _, it := range manifest.Items {
		itemsMap[it.Name] = it
	}

	s1, ok := itemsMap["skill-one"]
	if !ok {
		t.Fatalf("skill-one not found in manifest")
	}
	if s1.Author != "tester" {
		t.Errorf("expected author tester, got %q", s1.Author)
	}
	if s1.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %q", s1.Version)
	}
	if len(s1.Tags) != 2 {
		t.Errorf("expected 2 tags, got %v", s1.Tags)
	}

	if _, ok := itemsMap["skill-two"]; !ok {
		t.Fatalf("skill-two not found in manifest")
	}
}

func TestFetchHTTPCatalog(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		Version: "1.0.0",
		Items: []Item{
			{
				ID:          "remote-skill",
				Name:        "remote-skill",
				Description: "A remote skill",
			},
		},
	}
	data, _ := json.Marshal(manifest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "etag-12345")
		_, _ = w.Write(data)
	}))
	defer server.Close()

	ctx := context.Background()
	m, etag, err := FetchHTTPCatalog(ctx, server.URL+"/index.json", server.Client())
	if err != nil {
		t.Fatalf("FetchHTTPCatalog failed: %v", err)
	}
	if etag != "etag-12345" {
		t.Errorf("expected etag 'etag-12345', got %q", etag)
	}
	if len(m.Items) != 1 || m.Items[0].Name != "remote-skill" {
		t.Errorf("unexpected items in manifest: %+v", m.Items)
	}
}

func TestNamedCatalogCache(t *testing.T) {
	t.Parallel()

	tempCache := t.TempDir()
	manifest := Manifest{
		Version: "1.0.0",
		Items: []Item{
			{
				ID:          "cached-skill",
				Name:        "cached-skill",
				Description: "Cached skill desc",
			},
		},
	}

	catName := "google/skills"
	if err := SaveNamedCatalogCache(tempCache, catName, "https://github.com/google/skills", manifest, "etag-abc"); err != nil {
		t.Fatalf("SaveNamedCatalogCache failed: %v", err)
	}

	loaded, err := LoadNamedCatalogCache(tempCache, catName)
	if err != nil {
		t.Fatalf("LoadNamedCatalogCache failed: %v", err)
	}
	if len(loaded.Manifest.Items) != 1 || loaded.Manifest.Items[0].Name != "cached-skill" {
		t.Errorf("unexpected cached item: %+v", loaded.Manifest.Items)
	}

	allCaches, err := LoadAllNamedCatalogCaches(tempCache)
	if err != nil {
		t.Fatalf("LoadAllNamedCatalogCaches failed: %v", err)
	}
	if _, ok := allCaches[catName]; !ok {
		t.Fatalf("expected catalog %q in LoadAllNamedCatalogCaches, got keys: %v", catName, allCaches)
	}

	if err := DeleteNamedCatalogCache(tempCache, catName); err != nil {
		t.Fatalf("DeleteNamedCatalogCache failed: %v", err)
	}

	if _, err := LoadNamedCatalogCache(tempCache, catName); err == nil {
		t.Errorf("expected error after cache deletion, got nil")
	}
}

func TestSafeCatalogFileName_CollisionFree(t *testing.T) {
	t.Parallel()

	a := SafeCatalogFileName("google/skills")
	b := SafeCatalogFileName("google-skills")
	if a == b {
		t.Errorf("expected collision-free names for google/skills and google-skills, both got %q", a)
	}
	for _, name := range []string{a, b, SafeCatalogFileName(""), SafeCatalogFileName("https://example.com/skills")} {
		if !strings.HasSuffix(name, ".json") {
			t.Errorf("expected .json suffix, got %q", name)
		}
		if strings.ContainsAny(name, `/\:@`) {
			t.Errorf("unsafe characters in catalog filename %q", name)
		}
	}
	if SafeCatalogFileName("my-cat") != SafeCatalogFileName("MY-CAT") {
		t.Errorf("expected case-insensitive deterministic catalog filename")
	}
}

func TestFetchGitHubCatalog_FailFastOnServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/agent-skills/index.json") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		t.Errorf("unexpected fallback request after 500 error: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := server.Client()
	origTransport := client.Transport
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = server.Listener.Addr().String()
		return origTransport.RoundTrip(clone)
	})

	target := &GitHubTarget{Owner: "google", Repo: "skills", Ref: "main"}
	_, err := FetchGitHubCatalog(context.Background(), target, client)
	if err == nil {
		t.Fatalf("expected fail-fast error on 500 response")
	}
}

func TestFetchGitHubCatalog_TreeDiscovery(t *testing.T) {
	t.Parallel()

	treeResp := githubTreeResponse{
		Sha: "treesha",
		Tree: []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			Sha  string `json:"sha"`
			Size int    `json:"size"`
			URL  string `json:"url"`
		}{
			{
				Path: "skills/gemini-dev/SKILL.md",
				Type: "blob",
			},
		},
	}
	treeData, _ := json.Marshal(treeResp)

	skillContent := `---
name: gemini-dev
description: Gemini Dev Skill
metadata:
  author: google
  version: 1.0.0
---
# Gemini Dev
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/trees/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(treeData)
			return
		}
		if strings.HasSuffix(r.URL.Path, "SKILL.md") {
			_, _ = w.Write([]byte(skillContent))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := server.Client()
	origTransport := client.Transport
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = server.Listener.Addr().String()
		return origTransport.RoundTrip(clone)
	})

	target := &GitHubTarget{
		Owner: "google",
		Repo:  "skills",
		Ref:   "main",
	}

	m, err := FetchGitHubCatalog(context.Background(), target, client)
	if err != nil {
		t.Fatalf("FetchGitHubCatalog tree discovery failed: %v", err)
	}
	if len(m.Items) != 1 || m.Items[0].Name != "gemini-dev" {
		t.Errorf("unexpected items from tree discovery: %+v", m.Items)
	}
	if m.Items[0].Author != "google" {
		t.Errorf("expected author google, got %q", m.Items[0].Author)
	}
	wantURL := "https://raw.githubusercontent.com/google/skills/main/skills/gemini-dev/SKILL.md"
	if m.Items[0].URL != wantURL {
		t.Errorf("tree-discovered URL = %q, want %q", m.Items[0].URL, wantURL)
	}

	// Truncated tree response must fail fast.
	truncResp := githubTreeResponse{Sha: "treesha", Truncated: true}
	truncData, _ := json.Marshal(truncResp)
	truncServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/trees/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(truncData)
			return
		}
		http.NotFound(w, r)
	}))
	defer truncServer.Close()

	tClient := truncServer.Client()
	tOrig := tClient.Transport
	tClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = truncServer.Listener.Addr().String()
		return tOrig.RoundTrip(clone)
	})
	if _, err := FetchGitHubCatalog(context.Background(), target, tClient); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Errorf("expected truncated tree error, got: %v", err)
	}
}

func TestFetchGitHubCatalog_GoogleIndexJSON(t *testing.T) {
	t.Parallel()

	googleIndexJSON := `{
  "generator": "This file is generated. Do not edit it by hand.",
  "skills": [
    {
      "name": "agent-platform-alert-configuration",
      "description": "Configures alerting policies using OpenTelemetry metrics.",
      "entrypoint": "https://raw.githubusercontent.com/google/skills/main/skills/cloud/agent-platform-alert-configuration/SKILL.md"
    },
    {
      "name": "data-manager-api-setup",
      "description": "Guides developers through client library installation.",
      "entrypoint": "https://raw.githubusercontent.com/google/skills/main/skills/ads/data-manager-api-setup/SKILL.md"
    }
  ]
}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/index.json") && !strings.Contains(r.URL.Path, ".well-known") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(googleIndexJSON))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := server.Client()
	origTransport := client.Transport
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = server.Listener.Addr().String()
		return origTransport.RoundTrip(clone)
	})

	target := &GitHubTarget{
		Owner: "google",
		Repo:  "skills",
		Ref:   "main",
	}

	m, err := FetchGitHubCatalog(context.Background(), target, client)
	if err != nil {
		t.Fatalf("FetchGitHubCatalog google index.json failed: %v", err)
	}
	if len(m.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(m.Items))
	}

	item1 := m.Items[0]
	if item1.Name != "agent-platform-alert-configuration" {
		t.Errorf("item 0 name = %q, want agent-platform-alert-configuration", item1.Name)
	}
	if item1.RelativePath != "skills/cloud/agent-platform-alert-configuration" {
		t.Errorf("item 0 relativePath = %q, want skills/cloud/agent-platform-alert-configuration", item1.RelativePath)
	}
	if item1.URL != "https://raw.githubusercontent.com/google/skills/main/skills/cloud/agent-platform-alert-configuration/SKILL.md" {
		t.Errorf("item 0 URL = %q", item1.URL)
	}

	item2 := m.Items[1]
	if item2.Name != "data-manager-api-setup" {
		t.Errorf("item 1 name = %q, want data-manager-api-setup", item2.Name)
	}
}

func TestFetchGitHubCatalog_WellKnownAgentSkillsIndexJSON(t *testing.T) {
	t.Parallel()

	wellKnownJSON := `{
  "$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
  "version": "1.0.0",
  "name": "danicat/kungfu",
  "skills": [
    {
      "name": "kungfu",
      "type": "skill-md",
      "description": "KungFu CLI gateway",
      "version": "0.6.0",
      "url": "https://raw.githubusercontent.com/danicat/kungfu/main/skills/kungfu/SKILL.md",
      "digest": "sha256:808b8f76640610211c8b5d37927edaa0fd18e488b920efe795ddf088e3917423"
    }
  ]
}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/agent-skills/index.json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(wellKnownJSON))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := server.Client()
	origTransport := client.Transport
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = server.Listener.Addr().String()
		return origTransport.RoundTrip(clone)
	})

	target := &GitHubTarget{
		Owner: "danicat",
		Repo:  "kungfu",
		Ref:   "main",
	}

	m, err := FetchGitHubCatalog(context.Background(), target, client)
	if err != nil {
		t.Fatalf("FetchGitHubCatalog .well-known/agent-skills/index.json failed: %v", err)
	}
	if len(m.Items) != 1 || m.Items[0].Name != "kungfu" || m.Items[0].Version != "0.6.0" {
		t.Fatalf("unexpected items from well-known index.json: %+v", m.Items)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
