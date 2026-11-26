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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	tempHome, err := os.MkdirTemp("", "kungfu-catalog-test-*")
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
	_ = os.Setenv("KUNGFU_CONFIG_DIR", filepath.Join(tempHome, ".config", "kungfu"))
	_ = os.Setenv("KUNGFU_CACHE_DIR", filepath.Join(tempHome, ".cache", "kungfu"))
	_ = os.Setenv("KUNGFU_STATE_PATH", filepath.Join(tempHome, ".config", "kungfu", "state.json"))
	_ = os.Setenv("KUNGFU_URL", "")

	code := m.Run()
	_ = os.RemoveAll(tempHome)
	os.Exit(code)
}

func TestValidateSkillName(t *testing.T) {
	t.Parallel()

	valid := []string{"kungfu", "go-doctor", "skill_v1.2", "my.skill"}
	for _, name := range valid {
		if err := ValidateSkillName(name); err != nil {
			t.Errorf("ValidateSkillName(%q) unexpected error: %v", name, err)
		}
	}

	invalid := []string{"", " ", ".", "..", "a/b", `a\b`, "a:b", "a@b", " leading", "trailing ", "a..b", "bad\x00name", "bad\nname"}
	for _, name := range invalid {
		if err := ValidateSkillName(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateSkillName(%q) = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestFrontmatterExtractionAndParsing(t *testing.T) {
	t.Parallel()

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
	if err := VerifyDigest([]byte(docContent), "md5:deadbeef"); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("VerifyDigest should reject unsupported algorithm prefix, got: %v", err)
	}
	if err := VerifyRequiredDigest([]byte(docContent), ""); !errors.Is(err, ErrMissingDigest) {
		t.Errorf("VerifyRequiredDigest should fail with ErrMissingDigest on empty digest, got: %v", err)
	}

	// Frontmatter must start at byte 0 with "---\n"
	if _, _, err := ParseFrontmatter([]byte("preamble\n---\nname: x\n---\nbody")); !errors.Is(err, ErrInvalidFrontmatter) {
		t.Errorf("expected ErrInvalidFrontmatter when --- is not at start, got: %v", err)
	}
	// Frontmatter exceeding 64 KiB cap must be rejected
	hugeFM := "---\nname: huge\ndescription: " + strings.Repeat("a", 70*1024) + "\n---\n# body\n"
	if _, _, err := ParseFrontmatter([]byte(hugeFM)); !errors.Is(err, ErrInvalidFrontmatter) {
		t.Errorf("expected ErrInvalidFrontmatter for oversized frontmatter, got: %v", err)
	}
}

func TestCatalogStoreOperationsAndDeepCopy(t *testing.T) {
	t.Parallel()

	manifest := &Manifest{
		Version: "1.0.0",
		Items: []Item{
			{
				ID:          "my-skill",
				Name:        "my-skill",
				Type:        ItemTypeSkill,
				Title:       "My Skill",
				Description: "A test skill for discovery",
				Tags:        []string{"cli", "golang"},
				Source:      "remote",
				Metadata:    map[string]string{"author": "tester"},
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
		WithNoCache(),
		WithLocalDiscovery(false),
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

	// Mutating returned item slices/maps must not affect catalog internals.
	item.Tags[0] = "mutated"
	item.Metadata["author"] = "mutated"

	itemAgain, err := cat.Get("MY-SKILL")
	if err != nil {
		t.Fatalf("Get MY-SKILL (case-insensitive) failed: %v", err)
	}
	if itemAgain.ID != "my-skill" || itemAgain.Tags[0] != "cli" || itemAgain.Metadata["author"] != "tester" {
		t.Errorf("catalog state leaked across Get() call: %+v", itemAgain)
	}

	_, err = cat.Get("nonexistent")
	if !errors.Is(err, ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got: %v", err)
	}

	all := cat.All()
	if len(all) != 1 {
		t.Fatalf("All() length = %d, want 1", len(all))
	}
	all[0].Tags[0] = "mutated-all"
	if check, _ := cat.Get("my-skill"); check.Tags[0] != "cli" {
		t.Errorf("catalog state leaked across All() call: %+v", check)
	}
}

func TestManifestNormalization(t *testing.T) {
	t.Parallel()

	rawJSON := `{
		"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
		"name": "danicat/skills",
		"title": "Daniela Petruzalek Agent Skills Catalog",
		"items": [
			{
				"name": "ebitengineer",
				"digest": "sha256:abcdef123456",
				"entrypoint": "https://skills.danicat.dev/game-dev/ebitengineer/SKILL.md",
				"metadata": {
					"tags": "game-dev, ebitengine, 2d, golang",
					"author": "Daniela Petruzalek (daniela@danicat.dev)",
					"version": "1.2.0",
					"repository": "https://github.com/danicat/skills/tree/main/game-dev/ebitengineer"
				}
			}
		]
	}`

	var m Manifest
	if err := json.Unmarshal([]byte(rawJSON), &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
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
		t.Errorf("expected normalized entrypoint URL, got %q", item.URL)
	}
	if len(item.Tags) != 4 {
		t.Errorf("expected 4 normalized tags, got %d (%+v)", len(item.Tags), item.Tags)
	}

	// Specifying both "skills" and "items" must fail fast.
	bothJSON := `{"skills":[{"name":"a"}],"items":[{"name":"b"}]}`
	var bad Manifest
	if err := json.Unmarshal([]byte(bothJSON), &bad); err == nil {
		t.Errorf("expected error when manifest specifies both skills and items")
	}
}

func TestCatalog_LoadContent_BodyAndLocalError(t *testing.T) {
	t.Parallel()

	cat, err := NewCatalog(WithRemoteSync(false), WithLocalDiscovery(false), WithNoCache())
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}

	// 1. Direct in-memory body with matching digest
	body := "---\nname: bodyskill\n---\nBody text"
	bodyItem := &Item{
		ID:     "bodyskill",
		Name:   "bodyskill",
		Body:   body,
		Digest: ComputeDigest([]byte(body)),
	}
	bodyContent, err := cat.LoadContent(bodyItem)
	if err != nil || bodyContent != body {
		t.Errorf("unexpected bodyContent: %s, err: %v", bodyContent, err)
	}

	// 2. Direct in-memory body with mismatched digest must fail
	tampered := &Item{
		ID:     "bodyskill",
		Name:   "bodyskill",
		Body:   body,
		Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
	if _, err := cat.LoadContent(tampered); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("expected ErrDigestMismatch for tampered inline body, got: %v", err)
	}

	// 3. Local path without URL or body fails cleanly
	localItem := &Item{
		ID:   "myskill",
		Name: "myskill",
		Path: "/tmp/some/skill",
	}
	if _, err := cat.LoadContent(localItem); err == nil {
		t.Fatalf("expected error when loading item with only local path, got nil")
	}
}

func TestCatalog_LoadContent_Remote(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("---\nname: remote-skill\ndescription: remote\n---\n# Remote Content"))
	}))
	defer ts.Close()

	cat, err := NewCatalog(
		WithRemoteSync(false),
		WithLocalDiscovery(false),
		WithNoCache(),
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
	t.Parallel()

	tmpDir := t.TempDir()
	cat, err := NewCatalog(
		WithRemoteURL("https://custom.example.com"),
		WithCacheDir(tmpDir),
		WithRemoteSync(false),
		WithLocalDiscovery(false),
	)
	if err != nil {
		t.Fatalf("NewCatalog with options failed: %v", err)
	}
	if cat.remoteURL != "https://custom.example.com/.well-known/agent-skills/index.json" {
		t.Errorf("remoteURL option failed: %s", cat.remoteURL)
	}
	if cat.enableRemote {
		t.Errorf("WithRemoteSync(false) failed")
	}
}

func TestResolveManifestURLsAndCompareSemver(t *testing.T) {
	t.Parallel()

	rawJSON := `{
		"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
		"skills": [
			{
				"name": "godoctor",
				"type": "skill-md",
				"url": "/.well-known/agent-skills/godoctor/SKILL.md",
				"digest": "sha256:abc123"
			}
		]
	}`

	var m Manifest
	if err := json.Unmarshal([]byte(rawJSON), &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if err := ResolveManifestURLs(&m, "https://skills.danicat.dev/.well-known/agent-skills/index.json", false); err != nil {
		t.Fatalf("ResolveManifestURLs failed: %v", err)
	}

	if len(m.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(m.Items))
	}
	it := m.Items[0]
	if it.URL != "https://skills.danicat.dev/.well-known/agent-skills/godoctor/SKILL.md" {
		t.Errorf("expected resolved absolute URL, got %q", it.URL)
	}

	// Non-HTTPS URL inside a manifest must be rejected.
	insecureManifest := Manifest{
		Items: []Item{{Name: "bad", URL: "http://evil.example.com/SKILL.md"}},
	}
	if err := ResolveManifestURLs(&insecureManifest, "https://skills.danicat.dev/.well-known/agent-skills/index.json", false); !errors.Is(err, ErrInsecureURL) {
		t.Errorf("expected ErrInsecureURL for http:// skill URL, got: %v", err)
	}

	// SemVer 2.0 comparisons including prerelease & build metadata.
	semverCases := []struct {
		v1, v2 string
		want   int
	}{
		{"0.6.0", "0.5.2", 1},
		{"1.1.0", "1.1.0", 0},
		{"0.4.0", "0.6.0", -1},
		{"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0", "1.0.0-alpha", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha.2", -1},
		{"1.0.0-alpha.2", "1.0.0-alpha.10", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0+build.1", "1.0.0+build.2", 0},
		{"v2.0.0", "2.0.0", 0},
	}
	for _, tc := range semverCases {
		got := CompareSemver(tc.v1, tc.v2)
		if got != tc.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", tc.v1, tc.v2, got, tc.want)
		}
	}
}

func TestCatalog_LoadContent_NetworkAndSecurityErrors(t *testing.T) {
	t.Parallel()

	// 1. Default hardened client rejects plaintext http://127.0.0.1
	safeCat, err := NewCatalog(
		WithCacheDir(t.TempDir()),
		WithRemoteSync(false),
		WithLocalDiscovery(false),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}
	_, err = safeCat.LoadContent(&Item{
		ID:   "insecure-skill",
		Name: "insecure-skill",
		URL:  "http://127.0.0.1:1/SKILL.md",
	})
	if !errors.Is(err, ErrInsecureURL) {
		t.Errorf("expected ErrInsecureURL for plaintext http on default safe client, got: %v", err)
	}

	// 2. Closed loopback server with loopback-enabled client produces real connection error
	closedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closedServer.URL
	closedClient := closedServer.Client()
	closedServer.Close()

	netCat, err := NewCatalog(
		WithCacheDir(t.TempDir()),
		WithRemoteSync(false),
		WithLocalDiscovery(false),
		WithHTTPClient(closedClient),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}
	_, err = netCat.LoadContent(&Item{
		ID:   "uncached-skill",
		Name: "uncached-skill",
		URL:  closedURL + "/SKILL.md",
	})
	if err == nil {
		t.Fatalf("expected network error when loading from closed server, got nil")
	}
	if errors.Is(err, ErrInsecureURL) {
		t.Errorf("expected connection error rather than ErrInsecureURL, got: %v", err)
	}
}

func TestSafeHTTPClient_SSRFAndRedirectProtection(t *testing.T) {
	t.Parallel()

	// Safe client with loopback disabled blocks loopback IP even over https.
	safeClient := NewSafeHTTPClient(2 * time.Second)
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsSrv.Close()

	_, err := FetchWithRetryAndHeaders(context.Background(), safeClient, tlsSrv.URL, "test", nil, 0, time.Millisecond)
	if !errors.Is(err, ErrInsecureURL) {
		t.Errorf("expected NewSafeHTTPClient to block loopback TLS server with ErrInsecureURL, got: %v", err)
	}

	// Safe client rejects private IP literals (metadata/LAN).
	for _, blocked := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://169.254.169.254/latest/meta-data/",
		"https://10.0.0.1/index.json",
		"https://192.168.1.1/index.json",
		"file:///etc/passwd",
	} {
		if _, err := FetchWithRetryAndHeaders(context.Background(), safeClient, blocked, "test", nil, 0, time.Millisecond); !errors.Is(err, ErrInsecureURL) {
			t.Errorf("FetchWithRetryAndHeaders(%q) = %v, want ErrInsecureURL", blocked, err)
		}
	}

	// Cross-host redirect strips Authorization and Cookie headers.
	var gotAuth, gotCookie atomic.Value
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotCookie.Store(r.Header.Get("Cookie"))
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	redirSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL+"/dest", http.StatusFound)
	}))
	defer redirSrv.Close()

	loopClient := NewSafeHTTPClientWithLoopback(5*time.Second, true)
	resp, err := FetchWithRetryAndHeaders(context.Background(), loopClient, redirSrv.URL, "test", map[string]string{
		"Authorization": "Bearer secret",
		"Cookie":        "sid=secret",
	}, 0, time.Millisecond)
	if err != nil {
		t.Fatalf("redirect request failed: %v", err)
	}
	_ = resp.Body.Close()

	if auth, _ := gotAuth.Load().(string); auth != "" {
		t.Errorf("expected Authorization header stripped across hosts, got %q", auth)
	}
	if cookie, _ := gotCookie.Load().(string); cookie != "" {
		t.Errorf("expected Cookie header stripped across hosts, got %q", cookie)
	}
}

func TestFetchWithRetry_SuccessAfterRetries(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success-content"))
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := FetchWithRetryAndHeaders(ctx, ts.Client(), ts.URL, "kungfu-test", nil, 3, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("expected FetchWithRetryAndHeaders to succeed after retries, got err: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "success-content" {
		t.Errorf("expected success-content, got %s", string(data))
	}
}

func TestFetchWithRetry_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deterministically cancel context as soon as the first retryable response is served.
		w.WriteHeader(http.StatusServiceUnavailable)
		cancel()
	}))
	defer ts.Close()

	_, err := FetchWithRetryAndHeaders(ctx, ts.Client(), ts.URL, "kungfu-test", nil, 3, 50*time.Millisecond)
	if err == nil {
		t.Fatalf("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestCatalog_NewCatalog_RetryRecovery(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		Version: "1.0",
		Items: []Item{
			{
				ID:          "retry-skill",
				Name:        "retry-skill",
				Type:        ItemTypeSkill,
				Title:       "Retry Skill",
				Description: "Skill recovered after retry",
				Source:      "remote",
			},
		},
	}
	manifestData, _ := json.Marshal(manifest)

	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
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
		WithNoCache(),
		WithLocalDiscovery(false),
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
	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 server attempts, got %d", got)
	}
}

func TestReadBounded_And_CatalogIsolation(t *testing.T) {
	t.Parallel()

	// 1. ReadBounded rejects streams exceeding maxBytes
	data, err := ReadBounded(strings.NewReader("0123456789"), 5)
	if err == nil {
		t.Fatalf("expected ReadBounded to fail when stream exceeds maxBytes, got %q", string(data))
	}

	// 2. RemoveCatalogItems does not delete items from superstring catalog names
	cat, err := NewCatalog(WithRemoteSync(false), WithLocalDiscovery(false), WithNoCache())
	if err != nil {
		t.Fatal(err)
	}
	cat.AddCatalogManifest("go", &Manifest{
		Version: "1.0",
		Items:   []Item{{ID: "go-lint", Name: "go-lint"}},
	})
	cat.AddCatalogManifest("go-extra", &Manifest{
		Version: "1.0",
		Items:   []Item{{ID: "go-vet", Name: "go-vet"}},
	})

	cat.RemoveCatalogItems("go")
	if _, err := cat.Get("go-lint@go"); err == nil {
		t.Errorf("expected go-lint@go to be removed")
	}
	if _, err := cat.Get("go-vet@go-extra"); err != nil {
		t.Errorf("expected go-vet@go-extra to remain intact, got err: %v", err)
	}
}

func TestCatalog_GetAtCatalogDefaultAndShortSuffix(t *testing.T) {
	t.Parallel()

	manifest := &Manifest{
		Version: "1.0.0",
		Items: []Item{
			{ID: "godoctor", Name: "godoctor", Description: "Default godoctor"},
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
		WithNoCache(),
		WithLocalDiscovery(false),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}

	// 1. @default resolves default catalog items.
	if it, err := cat.Get("godoctor@default"); err != nil || it.Name != "godoctor" {
		t.Fatalf("Get(godoctor@default) = %+v, err=%v", it, err)
	}

	// 2. Full owner/repo and unique short repo suffix both resolve.
	cat.AddCatalogManifest("spf13/go-skills", &Manifest{
		Items: []Item{{ID: "go", Name: "go", Description: "Idiomatic Go"}},
	})
	if it, err := cat.Get("go@spf13/go-skills"); err != nil || it.Source != "spf13/go-skills" {
		t.Fatalf("Get(go@spf13/go-skills) = %+v, err=%v", it, err)
	}
	if it, err := cat.Get("go@go-skills"); err != nil || it.Source != "spf13/go-skills" {
		t.Fatalf("Get(go@go-skills) = %+v, err=%v", it, err)
	}

	// 3. Ambiguous short repo suffix fails fast with ErrAmbiguousCatalog listing both catalogs.
	cat.AddCatalogManifest("other/go-skills", &Manifest{
		Items: []Item{{ID: "go", Name: "go", Description: "Other Go"}},
	})
	_, err = cat.Get("go@go-skills")
	if !errors.Is(err, ErrAmbiguousCatalog) {
		t.Fatalf("expected ErrAmbiguousCatalog for duplicate short suffix, got: %v", err)
	}
	if !strings.Contains(err.Error(), "other/go-skills") || !strings.Contains(err.Error(), "spf13/go-skills") {
		t.Errorf("expected both catalog names in ambiguity error, got: %v", err)
	}
	// Exact full names still resolve cleanly.
	if it, err := cat.Get("go@other/go-skills"); err != nil || it.Source != "other/go-skills" {
		t.Fatalf("Get(go@other/go-skills) = %+v, err=%v", it, err)
	}

	// 4. All() retains shadowed skills across distinct catalogs (no silent drops).
	all := cat.All()
	goSources := make(map[string]bool)
	for _, it := range all {
		if it.Name == "go" {
			goSources[it.Source] = true
		}
	}
	if !goSources["spf13/go-skills"] || !goSources["other/go-skills"] {
		t.Errorf("All() dropped shadowed skills: got go sources %v", goSources)
	}

	// 5. Refreshing a catalog removes skills deleted upstream.
	cat.AddCatalogManifest("other/go-skills", &Manifest{
		Items: []Item{{ID: "new-only", Name: "new-only", Description: "Replacement"}},
	})
	if _, err := cat.Get("go@other/go-skills"); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("expected stale skill go@other/go-skills to be removed after manifest refresh, got err=%v", err)
	}
}

func TestCatalog_NoCacheDiscoveryGhostAndStaleCatalogDetection(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	// Seed a stale skill in <cacheDir>/skills/ghost-skill/SKILL.md that is NOT in any catalog.
	ghostDir := filepath.Join(cacheDir, "skills", "ghost-skill")
	if err := os.MkdirAll(ghostDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghostDir, "SKILL.md"), []byte("---\nname: ghost-skill\ndescription: stale cache\n---\n# Ghost\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Seed an expired named catalog (> 24h old).
	if err := SaveNamedCatalogCache(cacheDir, "spf13/go-skills", "spf13/go-skills", Manifest{
		Items: []Item{{ID: "go", Name: "go", Description: "Old cached Go"}},
	}, ""); err != nil {
		t.Fatal(err)
	}
	catCachePath := filepath.Join(cacheDir, "catalogs", SafeCatalogFileName("spf13/go-skills"))
	raw, err := os.ReadFile(catCachePath)
	if err != nil {
		t.Fatal(err)
	}
	expired := strings.ReplaceAll(string(raw), `"fetched_at": "20`, `"fetched_at": "2020-01-01T00:00:00Z", "old_fetched_at": "20`)
	if err := os.WriteFile(catCachePath, []byte(expired), 0600); err != nil {
		t.Fatal(err)
	}

	cat, err := NewCatalog(
		WithRemoteSync(false),
		WithCacheDir(cacheDir),
		WithLocalDiscovery(true),
		WithOffline(true),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}

	// Disposable cache dir must NOT resurface deleted skills as local items.
	if _, err := cat.Get("ghost-skill"); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("expected ghost-skill from disposable cache dir not to be discovered, got err=%v", err)
	}
	// Expired named catalog must be reported as stale.
	if !cat.IsCatalogCacheStale("spf13/go-skills") {
		t.Errorf("expected spf13/go-skills to be marked stale")
	}
	if cat.HasFreshCatalog("spf13/go-skills") {
		t.Errorf("expected HasFreshCatalog(spf13/go-skills) = false for expired cache")
	}
	stale := cat.StaleCatalogs()
	if len(stale) != 1 || stale[0] != "spf13/go-skills" {
		t.Errorf("StaleCatalogs() = %v, want [spf13/go-skills]", stale)
	}
}
