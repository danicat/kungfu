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
			wantRef:   "main",
			wantOK:    true,
		},
		{
			input:     "github.com/danicat/kungfu",
			wantOwner: "danicat",
			wantRepo:  "kungfu",
			wantRef:   "main",
			wantOK:    true,
		},
		{
			input:       "anthropics/anthropic-quickstarts/evals",
			wantOwner:   "anthropics",
			wantRepo:    "anthropic-quickstarts",
			wantRef:     "main",
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
			wantRef:   "main",
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
		{
			input:  "invalid",
			wantOK: false,
		},
		{
			input:  "",
			wantOK: false,
		},
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
		})
	}
}

func TestFetchGitHubSkillMock(t *testing.T) {
	mockContent := `---
name: custom-evals
description: Custom evaluation harness
allowed-tools: [bash, python]
license: MIT
metadata:
  version: 1.2.0
  category: coding
  tags: eval, testing
---
# Custom Evals
Instructions here.
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/testowner/testrepo/main/skills/evals/SKILL.md" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(mockContent))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	target := &GitHubTarget{
		Owner:   "testowner",
		Repo:    "testrepo",
		Ref:     "main",
		Subpath: "skills/evals",
	}

	fm, _, err := ParseFrontmatter([]byte(mockContent))
	if err != nil {
		t.Fatalf("ParseFrontmatter failed: %v", err)
	}
	if fm.Name != "custom-evals" {
		t.Errorf("Name = %q, want custom-evals", fm.Name)
	}
	if fm.Description != "Custom evaluation harness" {
		t.Errorf("Description = %q", fm.Description)
	}
	if fm.Metadata["version"] != "1.2.0" {
		t.Errorf("version = %q, want 1.2.0", fm.Metadata["version"])
	}

	_ = target
	_ = server
	_ = context.Background()
}

func TestDiscoverLocalCatalog(t *testing.T) {
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
  category: coding
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
  category: agents
  author: tester2
  version: 2.0.0
---
# Skill Two Body
`
	if err := os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(skill2Content), 0644); err != nil {
		t.Fatalf("write file error: %v", err)
	}

	manifest, err := DiscoverLocalCatalog(tempDir)
	if err != nil {
		t.Fatalf("DiscoverLocalCatalog failed: %v", err)
	}

	if len(manifest.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(manifest.Items))
	}

	itemsMap := make(map[string]Item)
	for _, it := range manifest.Items {
		itemsMap[it.Name] = it
	}

	s1, ok := itemsMap["skill-one"]
	if !ok {
		t.Fatalf("skill-one not found in manifest")
	}
	if s1.Category != "coding" {
		t.Errorf("expected category coding, got %q", s1.Category)
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

	s2, ok := itemsMap["skill-two"]
	if !ok {
		t.Fatalf("skill-two not found in manifest")
	}
	if s2.Category != "agents" {
		t.Errorf("expected category agents, got %q", s2.Category)
	}
}

func TestFetchHTTPCatalog(t *testing.T) {
	manifest := Manifest{
		Version: "1.0.0",
		Items: []Item{
			{
				ID:          "remote-skill",
				Name:        "remote-skill",
				Description: "A remote skill",
				Category:    "coding",
			},
		},
	}
	data, _ := json.Marshal(manifest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "etag-12345")
		w.Write(data)
	}))
	defer server.Close()

	ctx := context.Background()
	m, etag, err := FetchHTTPCatalog(ctx, server.URL+"/catalog.json", server.Client())
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
	tempCache := t.TempDir()
	t.Setenv("HOME", tempCache)

	manifest := Manifest{
		Version: "1.0.0",
		Items: []Item{
			{
				ID:          "cached-skill",
				Name:        "cached-skill",
				Description: "Cached skill desc",
				Category:    "agents",
			},
		},
	}

	catName := "google/skills"
	if err := SaveNamedCatalogCache(catName, "https://github.com/google/skills", manifest, "etag-abc"); err != nil {
		t.Fatalf("SaveNamedCatalogCache failed: %v", err)
	}

	loaded, err := LoadNamedCatalogCache(catName)
	if err != nil {
		t.Fatalf("LoadNamedCatalogCache failed: %v", err)
	}
	if len(loaded.Manifest.Items) != 1 || loaded.Manifest.Items[0].Name != "cached-skill" {
		t.Errorf("unexpected cached item: %+v", loaded.Manifest.Items)
	}

	allCaches, err := LoadAllNamedCatalogCaches()
	if err != nil {
		t.Fatalf("LoadAllNamedCatalogCaches failed: %v", err)
	}
	if len(allCaches) == 0 {
		t.Fatalf("expected at least 1 cached catalog, got %d", len(allCaches))
	}

	if err := DeleteNamedCatalogCache(catName); err != nil {
		t.Fatalf("DeleteNamedCatalogCache failed: %v", err)
	}

	_, err = LoadNamedCatalogCache(catName)
	if err == nil {
		t.Errorf("expected error after cache deletion, got nil")
	}
}

func TestSafeCatalogFileName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"google/skills", "google-skills.json"},
		{"https://example.com/skills", "example.com-skills.json"},
		{"my-catalog", "my-catalog.json"},
		{"", "default.json"},
	}

	for _, tc := range tests {
		got := SafeCatalogFileName(tc.input)
		if got != tc.expected {
			t.Errorf("SafeCatalogFileName(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}

func TestFetchGitHubCatalog_StaticCatalog(t *testing.T) {
	manifest := Manifest{
		Version: "1.0.0",
		Items: []Item{
			{
				ID:          "skill-a",
				Name:        "skill-a",
				Description: "Skill A from static catalog",
				Category:    "coding",
			},
		},
	}
	data, _ := json.Marshal(manifest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "catalog.json") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := server.Client()
	origTransport := client.Transport
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		if origTransport != nil {
			return origTransport.RoundTrip(req)
		}
		return http.DefaultTransport.RoundTrip(req)
	})

	target := &GitHubTarget{
		Owner: "google",
		Repo:  "skills",
		Ref:   "main",
	}

	m, err := FetchGitHubCatalog(context.Background(), target, client)
	if err != nil {
		t.Fatalf("FetchGitHubCatalog static failed: %v", err)
	}
	if len(m.Items) != 1 || m.Items[0].Name != "skill-a" {
		t.Errorf("unexpected items from static catalog: %+v", m.Items)
	}
}

func TestFetchGitHubCatalog_TreeDiscovery(t *testing.T) {
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
  category: coding
  author: google
  version: 1.0.0
---
# Gemini Dev
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/trees/") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(treeData)
			return
		}
		if strings.HasSuffix(r.URL.Path, "SKILL.md") {
			w.Write([]byte(skillContent))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
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
	if m.Items[0].Category != "coding" {
		t.Errorf("expected category coding, got %q", m.Items[0].Category)
	}
	if m.Items[0].Author != "google" {
		t.Errorf("expected author google, got %q", m.Items[0].Author)
	}
}

func TestFetchGitHubCatalog_GoogleIndexJSON(t *testing.T) {
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
		if strings.HasSuffix(r.URL.Path, "index.json") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(googleIndexJSON))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
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
	if item1.Category != "cloud" {
		t.Errorf("item 0 category = %q, want cloud", item1.Category)
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
	if item2.Category != "ads" {
		t.Errorf("item 1 category = %q, want ads", item2.Category)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
