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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
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

func TestFrontmatterExtractionAndParsing(t *testing.T) {
	docContent := "---\nname: doc-one\ndescription: >\n  First Skill Doc with multiline\n  folded description.\nmetadata:\n  tags: [go, ai]\n---\n# Main Heading\nSome markdown content here.\n"
	fm, _, err := ParseFrontmatter([]byte(docContent))
	if err != nil {
		t.Fatalf("ParseFrontmatter failed: %v", err)
	}
	if fm.Name != "doc-one" {
		t.Errorf("unexpected frontmatter name: %+v", fm)
	}
	if !strings.Contains(fm.Description, "First Skill Doc with multiline folded description.") {
		t.Errorf("unexpected folded description: %q", fm.Description)
	}
	if fm.Metadata["tags"] != "go, ai" {
		t.Errorf("unexpected metadata tags: %q", fm.Metadata["tags"])
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
	tagFilteredFail := cat.ListByFilter(QueryFilter{TagQuery: "nonexistenttagxyz"})
	if len(tagFilteredFail) != 0 {
		t.Errorf("expected 0 tagFiltered items for nonexistenttagxyz, got %d", len(tagFilteredFail))
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
		"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
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

func TestCatalog_LoadContent_EmbeddedAndBody(t *testing.T) {
	cat, err := NewCatalog(WithRemoteURL("http://127.0.0.1:0/unused"), WithEmbeddedFS(os.DirFS("../..")))
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

	// 2. Direct in-memory body
	bodyItem := &Item{
		ID:   "bodyskill",
		Body: "---\nname: bodyskill\n---\nBody text",
	}
	bodyContent, err := cat.LoadContent(bodyItem)
	if err != nil || bodyContent != bodyItem.Body {
		t.Errorf("unexpected bodyContent: %s, err: %v", bodyContent, err)
	}

	// 3. Local path should not be loaded
	localItem := &Item{
		ID:   "myskill",
		Name: "myskill",
		Path: "/tmp/some/skill",
	}
	_, err = cat.LoadContent(localItem)
	if err == nil {
		t.Fatalf("expected error when loading item with only local path, got nil")
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
		WithRemoteURL("https://custom.example.com"),
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

func TestFetchWithRetry_SuccessAfterRetries(t *testing.T) {
	var attempts int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success-content"))
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := fetchWithRetry(ctx, ts.Client(), ts.URL, "kungfu-test", 3, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("expected fetchWithRetry to succeed after retries, got err: %v", err)
	}
	defer resp.Body.Close()

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "success-content" {
		t.Errorf("expected success-content, got %s", string(data))
	}
}

func TestFetchWithRetry_ContextCanceled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately after first failure
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()

	_, err := fetchWithRetry(ctx, ts.Client(), ts.URL, "kungfu-test", 3, 50*time.Millisecond)
	if err == nil {
		t.Fatalf("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestCatalog_NewCatalog_RetryRecovery(t *testing.T) {
	manifest := Manifest{
		Version: "1.0",
		Items: []Item{
			{
				ID:          "retry-skill",
				Name:        "retry-skill",
				Type:        ItemTypeSkill,
				Title:       "Retry Skill",
				Description: "Skill recovered after retry",
				Category:    "testing",
				Source:      "remote",
			},
		},
	}
	manifestData, _ := json.Marshal(manifest)

	var attempts int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(manifestData)
	}))
	defer ts.Close()

	cat, err := NewCatalog(
		WithRemoteURL(ts.URL),
		WithHTTPClient(ts.Client()),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}

	item, err := cat.Get("retry-skill")
	if err != nil {
		t.Fatalf("expected retry-skill to be found after retries, got err: %v", err)
	}
	if item.Title != "Retry Skill" {
		t.Errorf("expected title 'Retry Skill', got %q", item.Title)
	}
	if attempts != 3 {
		t.Errorf("expected 3 server attempts, got %d", attempts)
	}
}
