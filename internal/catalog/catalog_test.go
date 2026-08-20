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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaTypesAndErrors(t *testing.T) {
	it := Item{
		ID:          "test-skill",
		Name:        "test-skill",
		Type:        ItemTypeSkill,
		Category:    "development",
		Tags:        []string{"go", "testing"},
		Description: "A test skill for unit testing",
	}

	if it.ID != "test-skill" || it.Type != ItemTypeSkill {
		t.Errorf("unexpected item fields: %+v", it)
	}

	cat := Category{
		Name:        "development",
		Emoji:       "🛠️",
		Description: "Dev tools",
		Count:       1,
	}

	if cat.Name != "development" || cat.Count != 1 {
		t.Errorf("unexpected category fields: %+v", cat)
	}

	if !errors.Is(ErrItemNotFound, ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound identity")
	}
}

func TestOKFExtractionAndParsing(t *testing.T) {
	docContent := "---\nname: doc-one\ndescription: >\n  First OKF Doc with multiline\n  folded description.\nmetadata:\n  tags: [go, ai]\n---\n# Main Heading\nLink to [[other-doc]] and [second doc](second.md)\n"
	fm, body, err := ParseFrontmatter([]byte(docContent))
	if err != nil {
		t.Fatalf("ParseFrontmatter failed: %v", err)
	}
	if fm.Name != "doc-one" {
		t.Errorf("unexpected frontmatter name: %+v", fm)
	}
	if !strings.Contains(fm.Description, "First OKF Doc with multiline folded description.") {
		t.Errorf("unexpected folded description: %q", fm.Description)
	}
	if fm.Metadata["tags"] != "go, ai" {
		t.Errorf("unexpected metadata tags: %q", fm.Metadata["tags"])
	}
	links := ExtractInterlinks(body)
	if len(links) != 2 || links[0] != "other-doc" || links[1] != "second" {
		t.Errorf("unexpected interlinks: %v", links)
	}
	digest := ComputeDigest([]byte(docContent))
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("digest should have sha256 prefix: %s", digest)
	}
	if err := VerifyDigest([]byte(docContent), digest); err != nil {
		t.Errorf("VerifyDigest should succeed: %v", err)
	}
	if err := VerifyDigest([]byte("corrupted"), digest); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("VerifyDigest should fail with ErrDigestMismatch, got: %v", err)
	}
}

func TestOKFBundleParsing(t *testing.T) {
	bundleDir := filepath.Join(t.TempDir(), "test-bundle")
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	doc1 := "---\nname: doc-a\ndescription: Document A\nmetadata:\n  tags: arch\n---\nBody A"
	doc2 := "---\nname: doc-b\ndescription: Document B\nmetadata:\n  tags: eng\n---\nBody B"
	manifest := "name: test-bundle\ndescription: Bundle of documents\nmetadata:\n  category: architecture\n"
	_ = os.WriteFile(filepath.Join(bundleDir, "doc-a.md"), []byte(doc1), 0644)
	_ = os.WriteFile(filepath.Join(bundleDir, "doc-b.md"), []byte(doc2), 0644)
	_ = os.WriteFile(filepath.Join(bundleDir, "okf.yaml"), []byte(manifest), 0644)

	bundle, err := ParseOKFBundle(bundleDir)
	if err != nil {
		t.Fatalf("ParseOKFBundle failed: %v", err)
	}
	if bundle.Name != "test-bundle" || len(bundle.Documents) != 2 {
		t.Errorf("bundle parsing unexpected result: %+v", bundle)
	}
	item := bundle.ToItem("local")
	if item.ID != "test-bundle" || item.Type != ItemTypeOKF || item.Category != "architecture" {
		t.Errorf("ToItem conversion unexpected result: %+v", item)
	}
}

func TestCatalogStoreOperations(t *testing.T) {
	manifest := &Manifest{
		Version: "1.0.0",
		Categories: []Category{
			{ID: "tooling", Name: "Tooling", Description: "Developer tools"},
		},
		Items: []Item{
			{
				ID:          "my-skill",
				Name:        "my-skill",
				Type:        ItemTypeSkill,
				Title:       "My Skill",
				Description: "A test skill for discovery",
				Category:    "tooling",
				Tags:        []string{"cli", "golang"},
				Source:      "remote",
			},
		},
	}
	data, _ := json.Marshal(manifest)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer ts.Close()

	cat, err := NewCatalog(
		WithRemoteURL(ts.URL),
		WithHTTPClient(ts.Client()),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}
	item, err := cat.Get("my-skill")
	if err != nil {
		t.Fatalf("Get my-skill failed: %v", err)
	}
	if item.Description != "A test skill for discovery" {
		t.Errorf("Get my-skill unexpected content: %+v", item)
	}
	if item.Category != "tooling" {
		t.Errorf("Get my-skill expected category tooling, got %q", item.Category)
	}
	itemCase, err := cat.Get("MY-SKILL")
	if err != nil {
		t.Fatalf("Get MY-SKILL (case-insensitive) failed: %v", err)
	}
	if itemCase.ID != "my-skill" {
		t.Errorf("Get case-insensitive failed: %+v", itemCase)
	}
	_, err = cat.Get("nonexistent")
	if !errors.Is(err, ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got: %v", err)
	}
	toolingItems, err := cat.List("tooling")
	if err != nil {
		t.Fatalf("List tooling failed: %v", err)
	}
	if len(toolingItems) != 1 {
		t.Errorf("expected 1 tooling item, got %d", len(toolingItems))
	}

	tagFiltered := cat.ListByFilter(QueryFilter{TagQuery: "golang AND cli"})
	if len(tagFiltered) != 1 {
		t.Errorf("expected 1 tagFiltered item, got %d", len(tagFiltered))
	}
	tagFilteredFail := cat.ListByFilter(QueryFilter{TagQuery: "python"})
	if len(tagFilteredFail) != 0 {
		t.Errorf("expected 0 tagFiltered items for python, got %d", len(tagFilteredFail))
	}

	// Verify embedded kungfu item is present
	kfItem, err := cat.Get("kungfu")
	if err != nil {
		t.Fatalf("Get embedded kungfu failed: %v", err)
	}
	if kfItem.Name != "kungfu" || kfItem.Category != "gateway" {
		t.Errorf("embedded kungfu item unexpected: %+v", kfItem)
	}

	all := cat.All()
	if len(all) < 2 {
		t.Errorf("All unexpected length: %d (expected at least my-skill and kungfu)", len(all))
	}
}

func TestManifestNormalization(t *testing.T) {
	rawJSON := `{
		"$schema": "https://agentskills.io/schema/v1/catalog.json",
		"name": "danicat/skills",
		"title": "Daniela Petruzalek Agent Skills Catalog",
		"gateway": {
			"name": "catalog",
			"kungfuLoad": "kungfu load catalog"
		},
		"items": [
			{
				"name": "ebitengineer",
				"category": "game-dev",
				"categoryName": "Game Development",
				"categoryEmoji": "🕹️",
				"sha256": "abcdef123456",
				"installCommand": "npx skills add danicat/skills --skill ebitengineer -y",
				"metadata": {
					"category": "game-dev",
					"tags": "game-dev, ebitengine, 2d, golang",
					"author": "Daniela Petruzalek (daniela@danicat.dev)",
					"version": "1.2.0",
					"canonical": "https://skills.danicat.dev/game-dev/ebitengineer/SKILL.md",
					"repository": "https://github.com/danicat/skills/tree/main/game-dev/ebitengineer"
				}
			}
		]
	}`

	var m Manifest
	if err := json.Unmarshal([]byte(rawJSON), &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if m.Gateway == nil || m.Gateway.KungfuLoad != "kungfu load catalog" {
		t.Errorf("expected gateway kungfuLoad, got %+v", m.Gateway)
	}
	if len(m.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(m.Items))
	}
	item := m.Items[0]
	if item.ID != "ebitengineer" {
		t.Errorf("expected ID 'ebitengineer', got %q", item.ID)
	}
	if item.Author != "Daniela Petruzalek (daniela@danicat.dev)" {
		t.Errorf("expected normalized author, got %q", item.Author)
	}
	if item.Version != "1.2.0" {
		t.Errorf("expected normalized version, got %q", item.Version)
	}
	if item.URL != "https://skills.danicat.dev/game-dev/ebitengineer/SKILL.md" {
		t.Errorf("expected normalized canonical URL, got %q", item.URL)
	}
	if item.InstallCmd != "npx skills add danicat/skills --skill ebitengineer -y" {
		t.Errorf("expected normalized InstallCmd, got %q", item.InstallCmd)
	}
	if len(item.Tags) != 4 {
		t.Errorf("expected 4 normalized tags, got %d (%+v)", len(item.Tags), item.Tags)
	}
}

func TestCatalog_LoadContent_LocalAndEmbedded(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "myskill")
	_ = os.MkdirAll(skillDir, 0755)
	expectedBody := "---\nname: myskill\ndescription: test\n---\n# My Skill\nContent here.\n"
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(expectedBody), 0644)

	cat, err := NewCatalog(WithCacheDir(tmpDir), WithRemoteURL("http://127.0.0.1:0/unused"))
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}

	// 1. Embedded
	kfItem, _ := cat.Get("kungfu")
	kfContent, err := cat.LoadContent(kfItem)
	if err != nil {
		t.Fatalf("LoadContent embedded failed: %v", err)
	}
	if !strings.Contains(kfContent, "name: kungfu") {
		t.Errorf("expected embedded kungfu content, got: %s", kfContent)
	}

	// 2. Local path
	localItem := &Item{
		ID:   "myskill",
		Name: "myskill",
		Path: skillDir,
	}
	localContent, err := cat.LoadContent(localItem)
	if err != nil {
		t.Fatalf("LoadContent local failed: %v", err)
	}
	if localContent != expectedBody {
		t.Errorf("expected %q, got %q", expectedBody, localContent)
	}

	// 3. Direct body
	bodyItem := &Item{
		ID:   "bodyskill",
		Body: "---\nname: bodyskill\n---\nBody text",
	}
	bodyContent, err := cat.LoadContent(bodyItem)
	if err != nil || bodyContent != bodyItem.Body {
		t.Errorf("unexpected bodyContent: %s, err: %v", bodyContent, err)
	}
}

func TestCatalog_LoadContent_Remote(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("---\nname: remote-skill\ndescription: remote\n---\n# Remote Content"))
	}))
	defer ts.Close()

	cat, err := NewCatalog(
		WithHTTPClient(ts.Client()),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}

	remoteItem := &Item{
		ID:   "remote-skill",
		Name: "remote-skill",
		URL:  ts.URL + "/SKILL.md",
	}

	content, err := cat.LoadContent(remoteItem)
	if err != nil {
		t.Fatalf("LoadContent remote failed: %v", err)
	}
	if !strings.Contains(content, "Remote Content") {
		t.Errorf("expected Remote Content, got: %s", content)
	}
}

func TestCatalog_Options(t *testing.T) {
	tmpDir := t.TempDir()
	cat, err := NewCatalog(
		WithRemoteURL("https://custom.example.com/catalog.json"),
		WithCacheDir(tmpDir),
		WithFallbackCacheDir(tmpDir),
		WithWorkspaceDir(tmpDir),
		WithHomeDir(tmpDir),
		WithRemoteDiscovery(false),
	)
	if err != nil {
		t.Fatalf("NewCatalog with options failed: %v", err)
	}
	if cat.remoteURL != "https://custom.example.com/catalog.json" {
		t.Errorf("remoteURL option failed: %s", cat.remoteURL)
	}
	if cat.enableRemote {
		t.Errorf("WithRemoteDiscovery(false) failed")
	}
}

func TestCatalog_LoadContent_NetworkError(t *testing.T) {
	tmpDir := t.TempDir()
	cat, err := NewCatalog(
		WithCacheDir(tmpDir),
		WithRemoteURL("http://127.0.0.1:0/unreachable"),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}

	remoteItem := &Item{
		ID:   "uncached-skill",
		Name: "uncached-skill",
		URL:  "http://127.0.0.1:0/unreachable/SKILL.md",
	}

	_, err = cat.LoadContent(remoteItem)
	if err == nil {
		t.Fatalf("expected network error when loading uncached remote item offline, got nil")
	}
	if !strings.Contains(err.Error(), "network") && !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected network-related error message, got: %v", err)
	}
}
