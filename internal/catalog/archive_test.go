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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
)

func buildTarGzEntries(t *testing.T, entries []tar.Header, bodies map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	for _, hdr := range entries {
		h := hdr
		body := bodies[h.Name]
		if h.Typeflag == 0 || h.Typeflag == tar.TypeReg {
			h.Typeflag = tar.TypeReg
			h.Size = int64(len(body))
			if h.Mode == 0 {
				h.Mode = 0644
			}
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("tar WriteHeader(%s): %v", h.Name, err)
		}
		if h.Typeflag == tar.TypeReg && len(body) > 0 {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatalf("tar Write(%s): %v", h.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func buildZipEntries(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip Create(%s): %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip Write(%s): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}

func TestArchive_ZipExtractSkillMDAndSecurity(t *testing.T) {
	t.Parallel()

	// 1. Valid zip extraction of SKILL.md
	validZip := buildZipEntries(t, map[string]string{
		"SKILL.md":           "---\nname: zipskill\ndescription: zip demo\n---\n# Zip Body\n",
		"references/docs.md": "# Docs",
	})
	md, err := catalog.ExtractSkillMDFromArchive(validZip)
	if err != nil {
		t.Fatalf("ExtractSkillMDFromArchive(zip) failed: %v", err)
	}
	if !strings.Contains(md, "# Zip Body") {
		t.Errorf("unexpected extracted zip SKILL.md: %q", md)
	}

	// 2. Zip missing SKILL.md
	missingZip := buildZipEntries(t, map[string]string{"README.md": "hello"})
	if _, err := catalog.UnpackArchive(missingZip, t.TempDir(), catalog.DefaultMaxUnpackedBytes); !errors.Is(err, catalog.ErrArchiveMissingSkillMD) {
		t.Errorf("expected ErrArchiveMissingSkillMD for zip, got: %v", err)
	}

	// 3. Zip-Slip traversal rejected
	for _, badPath := range []string{"../evil.txt", "/etc/evil.txt", "sub/../../evil.txt", `C:\evil.txt`} {
		badZip := buildZipEntries(t, map[string]string{
			"SKILL.md": "---\nname: bad\n---\n# Bad",
			badPath:    "pwned",
		})
		if _, err := catalog.UnpackArchive(badZip, t.TempDir(), catalog.DefaultMaxUnpackedBytes); !errors.Is(err, catalog.ErrArchiveTraversal) {
			t.Errorf("zip path %q: got %v, want ErrArchiveTraversal", badPath, err)
		}
	}

	// 4. Zip symlink entry rejected
	var symBuf bytes.Buffer
	zw := zip.NewWriter(&symBuf)
	hdr := &zip.FileHeader{Name: "link", Method: zip.Deflate}
	hdr.SetMode(os.ModeSymlink | 0777)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("../../etc/passwd"))
	w2, _ := zw.Create("SKILL.md")
	_, _ = w2.Write([]byte("---\nname: sym\n---\n# Sym"))
	_ = zw.Close()

	if _, err := catalog.UnpackArchive(symBuf.Bytes(), t.TempDir(), catalog.DefaultMaxUnpackedBytes); !errors.Is(err, catalog.ErrArchiveSymlinkEscapes) {
		t.Errorf("expected ErrArchiveSymlinkEscapes for zip symlink, got: %v", err)
	}

	// 5. Zip decompression bomb rejected
	bombZip := buildZipEntries(t, map[string]string{
		"SKILL.md": "---\nname: bomb\n---\n# Bomb",
		"big.txt":  strings.Repeat("A", 1024),
	})
	if _, err := catalog.UnpackArchive(bombZip, t.TempDir(), 64); !errors.Is(err, catalog.ErrArchiveDecompressionBomb) {
		t.Errorf("expected ErrArchiveDecompressionBomb for zip, got: %v", err)
	}
}

func TestArchive_TarHardlinkSymlinkModeAndFileCountBomb(t *testing.T) {
	t.Parallel()

	// 1. Hardlink rejected
	hardlinkTar := buildTarGzEntries(t, []tar.Header{
		{Name: "SKILL.md"},
		{Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"},
	}, map[string]string{"SKILL.md": "---\nname: hl\n---\n# HL"})
	if _, err := catalog.UnpackArchive(hardlinkTar, t.TempDir(), catalog.DefaultMaxUnpackedBytes); !errors.Is(err, catalog.ErrArchiveSymlinkEscapes) {
		t.Errorf("expected ErrArchiveSymlinkEscapes for tar hardlink, got: %v", err)
	}

	// 2. Two-stage / internal symlink rejected
	twoStageTar := buildTarGzEntries(t, []tar.Header{
		{Name: "SKILL.md"},
		{Name: "sub", Typeflag: tar.TypeSymlink, Linkname: ".."},
		{Name: "sub/outside.txt"},
	}, map[string]string{"SKILL.md": "---\nname: ts\n---\n# TS", "sub/outside.txt": "boom"})
	if _, err := catalog.UnpackArchive(twoStageTar, t.TempDir(), catalog.DefaultMaxUnpackedBytes); !errors.Is(err, catalog.ErrArchiveSymlinkEscapes) {
		t.Errorf("expected ErrArchiveSymlinkEscapes for two-stage symlink tar, got: %v", err)
	}

	// 3. Mode bits strip group/world write bits (0777 -> 0755, 0666 -> 0644)
	modeTar := buildTarGzEntries(t, []tar.Header{
		{Name: "SKILL.md", Mode: 0666},
		{Name: "scripts/run.sh", Mode: 0777},
	}, map[string]string{
		"SKILL.md":       "---\nname: perms\n---\n# Perms",
		"scripts/run.sh": "#!/bin/sh\necho ok\n",
	})
	dest := t.TempDir()
	if _, err := catalog.UnpackArchive(modeTar, dest, catalog.DefaultMaxUnpackedBytes); err != nil {
		t.Fatalf("UnpackArchive(perms) failed: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, rel := range []string{"SKILL.md", "scripts/run.sh"} {
			fi, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("stat %s: %v", rel, err)
			}
			if fi.Mode().Perm()&0022 != 0 {
				t.Errorf("%s retained group/world write bits: %04o", rel, fi.Mode().Perm())
			}
		}
	}

	// 4. File-count / entry-count bomb (> DefaultMaxArchiveFiles) rejected for both tar.gz and zip.
	entries := make([]tar.Header, 0, catalog.DefaultMaxArchiveFiles+3)
	bodies := map[string]string{"SKILL.md": "---\nname: many\n---\n# Many"}
	entries = append(entries, tar.Header{Name: "SKILL.md"})
	for range catalog.DefaultMaxArchiveFiles + 2 {
		entries = append(entries, tar.Header{Name: ".", Typeflag: tar.TypeDir})
	}
	bombTar := buildTarGzEntries(t, entries, bodies)
	if _, err := catalog.UnpackArchive(bombTar, t.TempDir(), catalog.DefaultMaxUnpackedBytes); !errors.Is(err, catalog.ErrArchiveDecompressionBomb) {
		t.Errorf("expected ErrArchiveDecompressionBomb for tar file-count bomb, got: %v", err)
	}

	zipFiles := make(map[string]string, catalog.DefaultMaxArchiveFiles+3)
	zipFiles["SKILL.md"] = "---\nname: many\n---\n# Many"
	for i := range catalog.DefaultMaxArchiveFiles + 2 {
		zipFiles[fmt.Sprintf("files/f%d.txt", i)] = ""
	}
	bombZip := buildZipEntries(t, zipFiles)
	if _, err := catalog.UnpackArchive(bombZip, t.TempDir(), catalog.DefaultMaxUnpackedBytes); !errors.Is(err, catalog.ErrArchiveDecompressionBomb) {
		t.Errorf("expected ErrArchiveDecompressionBomb for zip file-count bomb, got: %v", err)
	}
}

func TestArchive_CacheCatalogIsolationAndConcurrentSafety(t *testing.T) {
	t.Parallel()

	tarA := buildTarGzEntries(t, []tar.Header{{Name: "SKILL.md"}}, map[string]string{
		"SKILL.md": "---\nname: shared\n---\n# From Catalog A\n",
	})
	tarB := buildTarGzEntries(t, []tar.Header{{Name: "SKILL.md"}}, map[string]string{
		"SKILL.md": "---\nname: shared\n---\n# From Catalog B\n",
	})
	digestA := catalog.ComputeDigest(tarA)
	digestB := catalog.ComputeDigest(tarB)

	cacheRoot := t.TempDir()
	_, dirA, err := catalog.UnpackArchiveToCache(tarA, cacheRoot, "shared", "cat-a", digestA)
	if err != nil {
		t.Fatalf("UnpackArchiveToCache(cat-a): %v", err)
	}
	_, dirB, err := catalog.UnpackArchiveToCache(tarB, cacheRoot, "shared", "cat-b", digestB)
	if err != nil {
		t.Fatalf("UnpackArchiveToCache(cat-b): %v", err)
	}
	if dirA == dirB {
		t.Fatalf("expected distinct cache directories for different catalogs, both got %q", dirA)
	}

	// Concurrent unpack + read safety
	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, _, err := catalog.UnpackArchiveToCache(tarA, cacheRoot, "shared", "cat-a", digestA); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			if data, err := os.ReadFile(filepath.Join(dirA, "SKILL.md")); err != nil || !bytes.Contains(data, []byte("# From Catalog A")) {
				errCh <- errors.New("concurrent read failed or saw partial SKILL.md")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent UnpackArchiveToCache error: %v", err)
	}
}

func TestFetchAndUnpackArchive_RequiresDigest(t *testing.T) {
	t.Parallel()

	tarData := buildTarGzEntries(t, []tar.Header{{Name: "SKILL.md"}}, map[string]string{
		"SKILL.md": "---\nname: req\n---\n# Required Digest\n",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tarData)
	}))
	defer srv.Close()

	// Fail closed when digest is missing
	_, _, _, err := catalog.FetchAndUnpackArchive(context.Background(), srv.Client(), srv.URL+"/req.tar.gz", "", t.TempDir(), "req", "default", false)
	if !errors.Is(err, catalog.ErrMissingDigest) {
		t.Errorf("expected ErrMissingDigest, got: %v", err)
	}

	// Succeeds when digest matches
	md, _, files, err := catalog.FetchAndUnpackArchive(context.Background(), srv.Client(), srv.URL+"/req.tar.gz", catalog.ComputeDigest(tarData), t.TempDir(), "req", "default", false)
	if err != nil {
		t.Fatalf("FetchAndUnpackArchive valid failed: %v", err)
	}
	if !strings.Contains(md, "# Required Digest") || len(files) != 1 {
		t.Errorf("unexpected result: md=%q files=%v", md, files)
	}
}
