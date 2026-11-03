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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
)

func createTestTarGz(t *testing.T, skillMDContent string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	hdr := &tar.Header{
		Name: "SKILL.md",
		Mode: 0644,
		Size: int64(len(skillMDContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar write header: %v", err)
	}
	if _, err := tw.Write([]byte(skillMDContent)); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	_ = tw.Close()
	_ = gzw.Close()
	return buf.Bytes()
}

func TestRFC3986_RelativeURLResolution(t *testing.T) {
	manifestJSON := `{
		"$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
		"skills": [
			{
				"name": "relative-skill",
				"type": "skill-md",
				"description": "relative url test",
				"url": "code-review/SKILL.md",
				"digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111"
			},
			{
				"name": "path-abs-skill",
				"type": "skill-md",
				"description": "path absolute url test",
				"url": "/.well-known/agent-skills/path-abs/SKILL.md",
				"digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222"
			},
			{
				"name": "full-abs-skill",
				"type": "archive",
				"description": "full absolute url test",
				"url": "https://cdn.example.com/v2/skills/full-abs.tar.gz",
				"digest": "sha256:3333333333333333333333333333333333333333333333333333333333333333"
			},
			{
				"name": "unknown-type-skill",
				"type": "future-unsupported-type",
				"description": "should be skipped",
				"url": "future/SKILL.md",
				"digest": "sha256:4444444444444444444444444444444444444444444444444444444444444444"
			}
		]
	}`

	var m catalog.Manifest
	if err := json.Unmarshal([]byte(manifestJSON), &m); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Unknown type must be skipped
	if len(m.Items) != 3 {
		t.Fatalf("expected 3 valid items (unknown-type skipped), got %d", len(m.Items))
	}

	endpoint := "https://example.com/.well-known/agent-skills/index.json"
	catalog.ResolveManifestURLs(&m, endpoint)

	expectedURLs := map[string]string{
		"relative-skill": "https://example.com/.well-known/agent-skills/code-review/SKILL.md",
		"path-abs-skill": "https://example.com/.well-known/agent-skills/path-abs/SKILL.md",
		"full-abs-skill": "https://cdn.example.com/v2/skills/full-abs.tar.gz",
	}

	for _, item := range m.Items {
		expected, ok := expectedURLs[item.Name]
		if !ok {
			t.Errorf("unexpected item name: %s", item.Name)
			continue
		}
		if item.URL != expected {
			t.Errorf("item %s URL mismatch: got %q, expected %q", item.Name, item.URL, expected)
		}
	}
}

func TestCatalog_LoadContent_Archive(t *testing.T) {
	skillMD := "---\nname: archiveskill\ndescription: test\n---\n# Archive Loaded"
	tarData := createTestTarGz(t, skillMD)
	digest := catalog.ComputeDigest(tarData)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(tarData)
	}))
	defer server.Close()

	cat, err := catalog.NewCatalog(
		catalog.WithRemoteDisabled(),
		catalog.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("NewCatalog failed: %v", err)
	}

	item := &catalog.Item{
		Name:   "archiveskill",
		Type:   catalog.ItemTypeArchive,
		URL:    server.URL + "/archiveskill.tar.gz",
		Digest: digest,
	}

	content, err := cat.LoadContent(item)
	if err != nil {
		t.Fatalf("LoadContent archive failed: %v", err)
	}

	if !strings.Contains(content, "# Archive Loaded") {
		t.Errorf("expected extracted content, got: %s", content)
	}
}
