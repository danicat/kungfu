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

package catalog_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
)

func TestCatalogCache_ETagAndFailFast(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
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
		catalog.WithLocalDiscovery(false),
	)
	if err != nil {
		t.Fatalf("first NewCatalog failed: %v", err)
	}
	item, err := c1.Get("cached-skill")
	if err != nil || item == nil {
		t.Fatalf("expected cached-skill, err: %v", err)
	}
	if requestCount.Load() != 1 {
		t.Errorf("expected 1 request, got %d", requestCount.Load())
	}

	// 2. Second fetch with fresh TTL: hits cache directly without network request
	c2, err := catalog.NewCatalog(
		catalog.WithRemoteURL(server.URL),
		catalog.WithCacheDir(tmpDir),
		catalog.WithLocalDiscovery(false),
	)
	if err != nil {
		t.Fatalf("second NewCatalog failed: %v", err)
	}
	if _, err := c2.Get("cached-skill"); err != nil {
		t.Fatalf("expected cached-skill in c2, err: %v", err)
	}
	if requestCount.Load() != 1 {
		t.Errorf("expected still 1 request due to TTL cache hit, got %d", requestCount.Load())
	}

	// 3. Force refresh sends If-None-Match header and handles 304 Not Modified
	c3, err := catalog.NewCatalog(
		catalog.WithRemoteURL(server.URL),
		catalog.WithCacheDir(tmpDir),
		catalog.WithForceRefresh(),
		catalog.WithLocalDiscovery(false),
	)
	if err != nil {
		t.Fatalf("third NewCatalog failed: %v", err)
	}
	if _, err := c3.Get("cached-skill"); err != nil {
		t.Fatalf("expected cached-skill in c3, err: %v", err)
	}
	if requestCount.Load() != 2 {
		t.Errorf("expected 2 requests (1 full + 1 304 conditional), got %d", requestCount.Load())
	}

	// 4. Server closed: force refresh fails fast and does not fallback to stale cache
	server.Close()
	_, err = catalog.NewCatalog(
		catalog.WithRemoteURL(server.URL),
		catalog.WithCacheDir(tmpDir),
		catalog.WithForceRefresh(),
		catalog.WithLocalDiscovery(false),
	)
	if err == nil {
		t.Fatalf("expected NewCatalog to fail fast when server is closed with WithForceRefresh")
	}
}

func TestCatalogCache_SaveAndLoad(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "index.json")

	manifest := catalog.Manifest{
		Items: []catalog.Item{
			{ID: "skill-1", Name: "skill-1", Description: "Skill One"},
		},
	}

	err := catalog.SaveCachedCatalog(cachePath, "test-cat", "https://example.com/index.json", manifest, "etag-123")
	if err != nil {
		t.Fatalf("SaveCachedCatalog failed: %v", err)
	}

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(cachePath)
		if err != nil {
			t.Fatalf("stat cachePath: %v", err)
		}
		if fi.Mode().Perm() != 0600 {
			t.Errorf("cache file permissions = %04o, want 0600", fi.Mode().Perm())
		}
	}

	loaded, err := catalog.LoadCachedCatalog(cachePath)
	if err != nil {
		t.Fatalf("LoadCachedCatalog failed: %v", err)
	}
	if loaded.Name != "test-cat" {
		t.Errorf("expected name test-cat, got: %s", loaded.Name)
	}
	if loaded.ETag != "etag-123" {
		t.Errorf("expected etag-123, got: %s", loaded.ETag)
	}
	if loaded.URL != "https://example.com/index.json" {
		t.Errorf("expected url https://example.com/index.json, got: %s", loaded.URL)
	}
	if len(loaded.Manifest.Items) != 1 || loaded.Manifest.Items[0].Name != "skill-1" {
		t.Errorf("unexpected loaded manifest: %+v", loaded.Manifest)
	}
}

func TestResolveCacheDir_Precedence(t *testing.T) {
	customDir := filepath.Join(t.TempDir(), "override")
	xdgDir := filepath.Join(t.TempDir(), "xdg-cache")
	envDir := filepath.Join(t.TempDir(), "env-cache")

	t.Setenv("XDG_CACHE_HOME", xdgDir)
	t.Setenv("KUNGFU_CACHE_DIR", "")

	got, err := catalog.ResolveCacheDir("")
	if err != nil {
		t.Fatalf("ResolveCacheDir(XDG) failed: %v", err)
	}
	if got != filepath.Join(xdgDir, "kungfu") {
		t.Errorf("ResolveCacheDir(XDG) = %q, want %q", got, filepath.Join(xdgDir, "kungfu"))
	}

	t.Setenv("KUNGFU_CACHE_DIR", envDir)
	got, err = catalog.ResolveCacheDir("")
	if err != nil {
		t.Fatalf("ResolveCacheDir(KUNGFU_CACHE_DIR) failed: %v", err)
	}
	if got != envDir {
		t.Errorf("ResolveCacheDir(KUNGFU_CACHE_DIR) = %q, want %q", got, envDir)
	}

	got, err = catalog.ResolveCacheDir(customDir)
	if err != nil {
		t.Fatalf("ResolveCacheDir(override) failed: %v", err)
	}
	if got != customDir {
		t.Errorf("ResolveCacheDir(override) = %q, want %q", got, customDir)
	}
}
