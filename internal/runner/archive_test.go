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

package runner_test

import (
	"archive/tar"
	"archive/zip"
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

func createValidZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestUnpackArchive_TarGz_Success(t *testing.T) {
	files := map[string]string{
		"SKILL.md":             "---\nname: myarchive\ndescription: test\n---\n# My Archive",
		"references/GUIDE.md":  "# Guide content",
		"scripts/run.sh":       "#!/bin/bash\necho hello",
	}
	data := createValidTarGz(t, files)

	destDir := t.TempDir()
	extracted, err := catalog.UnpackArchive(data, destDir, catalog.DefaultMaxUnpackedBytes)
	if err != nil {
		t.Fatalf("UnpackArchive failed: %v", err)
	}

	if len(extracted) != 3 {
		t.Fatalf("expected 3 files extracted, got %d: %v", len(extracted), extracted)
	}

	skillMD, err := os.ReadFile(filepath.Join(destDir, "SKILL.md"))
	if err != nil || !bytes.Contains(skillMD, []byte("# My Archive")) {
		t.Errorf("SKILL.md content mismatch: %s, err: %v", skillMD, err)
	}
}

func TestUnpackArchive_Zip_Success(t *testing.T) {
	files := map[string]string{
		"SKILL.md":             "---\nname: myzip\ndescription: test\n---\n# My Zip",
		"references/DOCS.md":   "# Docs content",
	}
	data := createValidZip(t, files)

	destDir := t.TempDir()
	extracted, err := catalog.UnpackArchive(data, destDir, catalog.DefaultMaxUnpackedBytes)
	if err != nil {
		t.Fatalf("UnpackArchive zip failed: %v", err)
	}

	if len(extracted) != 2 {
		t.Fatalf("expected 2 files extracted, got %d: %v", len(extracted), extracted)
	}

	docs, err := os.ReadFile(filepath.Join(destDir, "references", "DOCS.md"))
	if err != nil || !bytes.Contains(docs, []byte("# Docs content")) {
		t.Errorf("DOCS.md content mismatch: %s, err: %v", docs, err)
	}
}

func TestUnpackArchive_MissingSkillMD(t *testing.T) {
	files := map[string]string{
		"README.md": "# No skill file here",
	}
	data := createValidTarGz(t, files)

	destDir := t.TempDir()
	_, err := catalog.UnpackArchive(data, destDir, catalog.DefaultMaxUnpackedBytes)
	if !errors.Is(err, catalog.ErrArchiveMissingSkillMD) {
		t.Fatalf("expected ErrArchiveMissingSkillMD, got: %v", err)
	}
}

func TestUnpackArchive_PathTraversal_Rejected(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	hdr := &tar.Header{
		Name: "../evil.txt",
		Mode: 0644,
		Size: 4,
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("evil"))
	_ = tw.Close()
	_ = gzw.Close()

	destDir := t.TempDir()
	_, err := catalog.UnpackArchive(buf.Bytes(), destDir, catalog.DefaultMaxUnpackedBytes)
	if !errors.Is(err, catalog.ErrArchiveTraversal) {
		t.Fatalf("expected ErrArchiveTraversal for .., got: %v", err)
	}

	// Test absolute path
	buf.Reset()
	gzw = gzip.NewWriter(&buf)
	tw = tar.NewWriter(gzw)
	hdr = &tar.Header{
		Name: "/etc/evil.txt",
		Mode: 0644,
		Size: 4,
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("evil"))
	_ = tw.Close()
	_ = gzw.Close()

	_, err = catalog.UnpackArchive(buf.Bytes(), destDir, catalog.DefaultMaxUnpackedBytes)
	if !errors.Is(err, catalog.ErrArchiveTraversal) {
		t.Fatalf("expected ErrArchiveTraversal for absolute path, got: %v", err)
	}
}

func TestUnpackArchive_SymlinkEscape_Rejected(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	hdr := &tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "symlink_to_escape",
		Linkname: "../../etc/passwd",
	}
	_ = tw.WriteHeader(hdr)
	_ = tw.Close()
	_ = gzw.Close()

	destDir := t.TempDir()
	_, err := catalog.UnpackArchive(buf.Bytes(), destDir, catalog.DefaultMaxUnpackedBytes)
	if !errors.Is(err, catalog.ErrArchiveSymlinkEscapes) {
		t.Fatalf("expected ErrArchiveSymlinkEscapes, got: %v", err)
	}
}

func TestUnpackArchive_DecompressionBomb_Rejected(t *testing.T) {
	files := map[string]string{
		"SKILL.md":   "---\nname: bomb\n---\n# Bomb",
		"large.txt":  "01234567890123456789", // 20 bytes
	}
	data := createValidTarGz(t, files)

	destDir := t.TempDir()
	// limit to 10 bytes
	_, err := catalog.UnpackArchive(data, destDir, 10)
	if !errors.Is(err, catalog.ErrArchiveDecompressionBomb) {
		t.Fatalf("expected ErrArchiveDecompressionBomb, got: %v", err)
	}
}

func TestInstallSkillContext_Archive(t *testing.T) {
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

	item := catalog.Item{
		Name:   "testarchive",
		Type:   catalog.ItemTypeArchive,
		URL:    server.URL + "/testarchive.tar.gz",
		Digest: digest,
	}

	destDir := t.TempDir()
	installed, err := runner.InstallSkillContext(context.Background(), item, destDir, "", nil, server.Client())
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
}
