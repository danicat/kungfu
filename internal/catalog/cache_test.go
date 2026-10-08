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

package catalog_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
)

func TestCatalogCache_ETagAndFailFast(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.Header.Get("If-None-Match") == "v1-etag" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", "v1-etag")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
			"skills": [
				{
					"name": "cached-skill",
					"description": "Cached Skill",
					"type": "skill-md",
					"url": "https://skills.danicat.dev/testing/cached-skill/SKILL.md",
					"digest": "sha256:123456"
				}
			]
		}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	// 1. First fetch: populates cache with ETag
	c1, err := catalog.NewCatalog(
		catalog.WithRemoteURL(server.URL),
		catalog.WithCacheDir(tmpDir),
	)
	if err != nil {
		t.Fatalf("first NewCatalog failed: %v", err)
	}
	item, err := c1.Get("cached-skill")
	if err != nil || item == nil {
		t.Fatalf("expected cached-skill, err: %v", err)
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("expected 1 request, got %d", atomic.LoadInt32(&requestCount))
	}

	// 2. Second fetch with fresh TTL: hits cache directly without network request
	c2, err := catalog.NewCatalog(
		catalog.WithRemoteURL(server.URL),
		catalog.WithCacheDir(tmpDir),
	)
	if err != nil {
		t.Fatalf("second NewCatalog failed: %v", err)
	}
	if _, err := c2.Get("cached-skill"); err != nil {
		t.Fatalf("expected cached-skill in c2, err: %v", err)
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("expected still 1 request due to TTL cache hit, got %d", atomic.LoadInt32(&requestCount))
	}

	// 3. Force refresh sends If-None-Match header and handles 304 Not Modified
	c3, err := catalog.NewCatalog(
		catalog.WithRemoteURL(server.URL),
		catalog.WithCacheDir(tmpDir),
		catalog.WithForceRefresh(),
	)
	if err != nil {
		t.Fatalf("third NewCatalog failed: %v", err)
	}
	if _, err := c3.Get("cached-skill"); err != nil {
		t.Fatalf("expected cached-skill in c3, err: %v", err)
	}
	if atomic.LoadInt32(&requestCount) != 2 {
		t.Errorf("expected 2 requests (1 full + 1 304 conditional), got %d", atomic.LoadInt32(&requestCount))
	}

	// 4. Server closed: force refresh fails fast and does not fallback to stale cache
	server.Close()
	_, err = catalog.NewCatalog(
		catalog.WithRemoteURL(server.URL),
		catalog.WithCacheDir(tmpDir),
		catalog.WithForceRefresh(),
	)
	if err == nil {
		t.Fatalf("expected NewCatalog to fail fast when server is closed with WithForceRefresh")
	}
}

func TestCatalogCache_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "index.json")

	manifest := catalog.Manifest{
		Items: []catalog.Item{
			{ID: "skill-1", Name: "skill-1", Description: "Skill One"},
		},
	}

	err := catalog.SaveCachedCatalog(cachePath, "https://example.com/catalog.json", manifest, "etag-123")
	if err != nil {
		t.Fatalf("SaveCachedCatalog failed: %v", err)
	}

	loaded, err := catalog.LoadCachedCatalog(cachePath)
	if err != nil {
		t.Fatalf("LoadCachedCatalog failed: %v", err)
	}
	if loaded.ETag != "etag-123" {
		t.Errorf("expected etag-123, got: %s", loaded.ETag)
	}
	if loaded.URL != "https://example.com/catalog.json" {
		t.Errorf("expected url https://example.com/catalog.json, got: %s", loaded.URL)
	}
	if len(loaded.Manifest.Items) != 1 || loaded.Manifest.Items[0].Name != "skill-1" {
		t.Errorf("unexpected loaded manifest: %+v", loaded.Manifest)
	}
}
