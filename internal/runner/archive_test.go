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

package runner_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/runner"
)

func createValidTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write content %s: %v", name, err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestInstallSkillContext_Archive(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"SKILL.md":           "---\nname: testarchive\ndescription: demo\n---\n# Demo Skill",
		"references/arch.md": "# Architecture",
	}
	tarData := createValidTarGz(t, files)
	digest := catalog.ComputeDigest(tarData)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(tarData)
	}))
	defer server.Close()

	r := runner.New(t.TempDir(), server.Client())

	// 1. Valid archive with matching digest installs cleanly.
	item := catalog.Item{
		Name:   "testarchive",
		Type:   catalog.ItemTypeArchive,
		URL:    server.URL + "/testarchive.tar.gz",
		Digest: digest,
	}

	destDir := filepath.Join(t.TempDir(), "testarchive")
	installed, err := r.InstallSkillContext(context.Background(), item, destDir, "", nil, server.Client())
	if err != nil {
		t.Fatalf("InstallSkillContext archive failed: %v", err)
	}
	if len(installed) != 2 {
		t.Fatalf("expected 2 installed files, got %d: %v", len(installed), installed)
	}

	archData, err := os.ReadFile(filepath.Join(destDir, "references", "arch.md"))
	if err != nil || !bytes.Contains(archData, []byte("# Architecture")) {
		t.Errorf("arch.md content mismatch: %s, err: %v", archData, err)
	}

	// 2. Archive with missing digest must fail closed with ErrMissingDigest.
	unsignedItem := catalog.Item{
		Name: "unsigned-archive",
		Type: catalog.ItemTypeArchive,
		URL:  server.URL + "/testarchive.tar.gz",
	}
	if _, err := r.InstallSkillContext(context.Background(), unsignedItem, filepath.Join(t.TempDir(), "unsigned"), "", nil, server.Client()); !errors.Is(err, catalog.ErrMissingDigest) {
		t.Errorf("expected ErrMissingDigest for archive without digest, got: %v", err)
	}
}
