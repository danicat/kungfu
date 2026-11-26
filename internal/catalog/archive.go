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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/danicat/kungfu/internal/version"
)

// Archive safety errors per Cloudflare Agent Skills Discovery RFC v0.2.0.
var (
	ErrArchiveTraversal         = errors.New("archive contains path traversal or absolute path")
	ErrArchiveSymlinkEscapes    = errors.New("archive contains disallowed symlink or hardlink")
	ErrArchiveDecompressionBomb = errors.New("archive exceeds maximum unpacked size or file count limit")
	ErrArchiveMissingSkillMD    = errors.New("archive missing required SKILL.md at root")
	ErrUnsupportedArchiveFormat = errors.New("unsupported archive format (expected .tar.gz or .zip)")
)

const (
	// DefaultMaxUnpackedBytes limits total unpacked archive size (50 MiB).
	DefaultMaxUnpackedBytes = 50 * 1024 * 1024
	// DefaultMaxArchiveFiles limits total entries in a skill archive to prevent inode exhaustion.
	DefaultMaxArchiveFiles = 2000
	// DefaultMaxSkillMDBytes limits plain SKILL.md downloads and extractions (5 MiB).
	DefaultMaxSkillMDBytes = 5 * 1024 * 1024
)

// ReadBounded reads all bytes from r up to maxBytes, returning ErrArchiveDecompressionBomb if r exceeds maxBytes.
func ReadBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxUnpackedBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: payload exceeds %d bytes", ErrArchiveDecompressionBomb, maxBytes)
	}
	return data, nil
}

// DetectArchiveFormat inspects file bytes or path/URL to determine if it is .tar.gz or .zip.
func DetectArchiveFormat(data []byte, nameOrURL string) string {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return "tar.gz"
	}
	if len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04 {
		return "zip"
	}
	lower := strings.ToLower(nameOrURL)
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") {
		return "tar.gz"
	}
	if strings.HasSuffix(lower, ".zip") {
		return "zip"
	}
	return ""
}

// IsArchiveItem returns true if the catalog item represents an archive skill (.tar.gz or .zip).
func IsArchiveItem(item Item) bool {
	if item.Type == ItemTypeArchive {
		return true
	}
	if DetectArchiveFormat(nil, item.URL) != "" {
		return true
	}
	if item.Metadata != nil && DetectArchiveFormat(nil, item.Metadata["canonical"]) != "" {
		return true
	}
	return false
}

// ExtractSkillMDFromArchive inspects and extracts the root SKILL.md file from a .tar.gz or .zip archive in memory.
func ExtractSkillMDFromArchive(data []byte) (string, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return extractSkillMDTarGz(data)
	}
	if len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04 {
		return extractSkillMDZip(data)
	}
	return "", ErrUnsupportedArchiveFormat
}

func extractSkillMDTarGz(data []byte) (string, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("reading gzip: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	// Bound total decompressed tar stream to prevent gzip bombs on non-SKILL.md entries.
	maxStream := int64(DefaultMaxUnpackedBytes) + int64(DefaultMaxArchiveFiles)*4096
	limited := io.LimitReader(gzr, maxStream+1)
	tr := tar.NewReader(limited)

	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if limited.(*io.LimitedReader).N == 0 {
				return "", fmt.Errorf("%w: decompressed stream exceeded limit", ErrArchiveDecompressionBomb)
			}
			return "", fmt.Errorf("reading tar entry: %w", err)
		}
		entries++
		if entries > DefaultMaxArchiveFiles {
			return "", fmt.Errorf("%w: exceeds %d entries", ErrArchiveDecompressionBomb, DefaultMaxArchiveFiles)
		}

		cleanName := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag == tar.TypeReg && (cleanName == "SKILL.md" || strings.EqualFold(cleanName, "SKILL.md")) {
			content, err := ReadBounded(tr, DefaultMaxSkillMDBytes)
			if err != nil {
				return "", fmt.Errorf("reading SKILL.md: %w", err)
			}
			return string(content), nil
		}
	}
	return "", ErrArchiveMissingSkillMD
}

func extractSkillMDZip(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("reading zip: %w", err)
	}
	if len(zr.File) > DefaultMaxArchiveFiles {
		return "", fmt.Errorf("%w: exceeds %d entries", ErrArchiveDecompressionBomb, DefaultMaxArchiveFiles)
	}
	for _, f := range zr.File {
		cleanName := strings.TrimPrefix(f.Name, "./")
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if cleanName == "SKILL.md" || strings.EqualFold(cleanName, "SKILL.md") {
			if f.UncompressedSize64 > DefaultMaxSkillMDBytes {
				return "", fmt.Errorf("%w: SKILL.md exceeds %d bytes", ErrArchiveDecompressionBomb, DefaultMaxSkillMDBytes)
			}
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("opening zip SKILL.md: %w", err)
			}
			content, err := ReadBounded(rc, DefaultMaxSkillMDBytes)
			_ = rc.Close()
			if err != nil {
				return "", fmt.Errorf("reading zip SKILL.md: %w", err)
			}
			return string(content), nil
		}
	}
	return "", ErrArchiveMissingSkillMD
}

// IsArchiveCachedWithDigest reports whether the skill cache directory has been unpacked,
// matches expectedDigest, and has an intact SKILL.md matching its recorded digest.
func IsArchiveCachedWithDigest(cacheRoot, skillName, source, expectedDigest string) bool {
	if cacheRoot == "" || skillName == "" {
		return false
	}
	skillDir, err := SkillCacheDir(cacheRoot, skillName, source)
	if err != nil {
		return false
	}
	return isArchiveDirCachedWithDigest(skillDir, expectedDigest)
}

func isArchiveDirCachedWithDigest(skillDir, expectedDigest string) bool {
	skillMDPath := filepath.Join(skillDir, "SKILL.md")
	f, err := os.Open(skillMDPath)
	if err != nil {
		return false
	}
	mdBytes, readErr := ReadBounded(f, DefaultMaxSkillMDBytes)
	_ = f.Close()
	if readErr != nil || len(mdBytes) == 0 {
		return false
	}

	// Verify cached SKILL.md digest if present (protects against on-disk tampering).
	if mdDigestBytes, err := os.ReadFile(filepath.Join(skillDir, ".skillmd.digest")); err == nil {
		if VerifyDigest(mdBytes, strings.TrimSpace(string(mdDigestBytes))) != nil {
			return false
		}
	}

	if strings.TrimSpace(expectedDigest) == "" {
		return true
	}
	digestBytes, err := os.ReadFile(filepath.Join(skillDir, ".digest"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(digestBytes)) == NormalizeDigest(expectedDigest)
}

// UnpackArchiveToCache unpacks the archive into a temporary directory under <cacheRoot>/skills,
// records the normalized archive and SKILL.md digests, and atomically swaps it into the skill cache directory.
func UnpackArchiveToCache(data []byte, cacheRoot, skillName, source, expectedDigest string) ([]string, string, error) {
	if cacheRoot == "" || skillName == "" {
		return nil, "", errors.New("cacheRoot and skillName are required")
	}
	skillDir, err := SkillCacheDir(cacheRoot, skillName, source)
	if err != nil {
		return nil, "", err
	}
	skillsParentDir := filepath.Dir(skillDir)
	cleanKey := filepath.Base(skillDir)

	if err := os.MkdirAll(skillsParentDir, 0700); err != nil {
		return nil, "", fmt.Errorf("creating skills cache dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp(skillsParentDir, "."+cleanKey+"-tmp-*")
	if err != nil {
		return nil, "", fmt.Errorf("creating temp archive dir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	extracted, err := UnpackArchive(data, tmpDir, DefaultMaxUnpackedBytes)
	if err != nil {
		return nil, "", err
	}

	mdBytes, err := os.ReadFile(filepath.Join(tmpDir, "SKILL.md"))
	if err != nil {
		return nil, "", fmt.Errorf("reading unpacked SKILL.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".skillmd.digest"), []byte(ComputeDigest(mdBytes)), 0600); err != nil {
		return nil, "", fmt.Errorf("writing SKILL.md digest marker: %w", err)
	}
	if strings.TrimSpace(expectedDigest) != "" {
		if err := os.WriteFile(filepath.Join(tmpDir, ".digest"), []byte(NormalizeDigest(expectedDigest)), 0600); err != nil {
			return nil, "", fmt.Errorf("writing archive digest marker: %w", err)
		}
	}

	if strings.TrimSpace(expectedDigest) != "" && isArchiveDirCachedWithDigest(skillDir, expectedDigest) {
		return extracted, skillDir, nil
	}

	var oldDir string
	if _, statErr := os.Stat(skillDir); statErr == nil {
		if trashDir, mkErr := os.MkdirTemp(skillsParentDir, "."+cleanKey+"-old-*"); mkErr == nil {
			_ = os.Remove(trashDir)
			if renErr := os.Rename(skillDir, trashDir); renErr == nil {
				oldDir = trashDir
			}
		}
	}

	if err := os.Rename(tmpDir, skillDir); err != nil {
		// Roll back oldDir if rename failed so we never leave the cache empty.
		if oldDir != "" {
			_ = os.Rename(oldDir, skillDir)
		}
		if strings.TrimSpace(expectedDigest) != "" && isArchiveDirCachedWithDigest(skillDir, expectedDigest) {
			return extracted, skillDir, nil
		}
		return nil, "", fmt.Errorf("moving unpacked skill into cache: %w", err)
	}
	if oldDir != "" {
		_ = os.RemoveAll(oldDir)
	}
	return extracted, skillDir, nil
}

// FetchAndUnpackArchive downloads a remote skill archive, verifies its SHA-256 digest
// (failing closed if missing unless forceBypassDigest is true), extracts SKILL.md,
// and optionally unpacks into cacheRoot.
func FetchAndUnpackArchive(ctx context.Context, client *http.Client, archiveURL, expectedDigest, cacheRoot, skillName, source string, forceBypassDigest bool) (string, string, []string, error) {
	resp, err := FetchWithRetryAndHeaders(ctx, client, archiveURL, version.UserAgent(), nil, defaultMaxRetries, defaultInitialBackoff)
	if err != nil {
		return "", "", nil, fmt.Errorf("downloading archive %s: %w", archiveURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", nil, &HTTPStatusError{StatusCode: resp.StatusCode, URL: archiveURL}
	}

	data, err := ReadBounded(resp.Body, DefaultMaxUnpackedBytes)
	if err != nil {
		return "", "", nil, fmt.Errorf("reading archive %s: %w", archiveURL, err)
	}
	if len(data) == 0 {
		return "", "", nil, fmt.Errorf("empty archive downloaded from %s", archiveURL)
	}

	if !forceBypassDigest {
		if err := VerifyRequiredDigest(data, expectedDigest); err != nil {
			return "", "", nil, fmt.Errorf("%w: archive %q from %s", err, skillName, archiveURL)
		}
	}

	skillMD, err := ExtractSkillMDFromArchive(data)
	if err != nil {
		return "", "", nil, fmt.Errorf("extracting SKILL.md from archive %q: %w", skillName, err)
	}

	var skillDir string
	var extracted []string
	if cacheRoot != "" && skillName != "" {
		extracted, skillDir, err = UnpackArchiveToCache(data, cacheRoot, skillName, source, expectedDigest)
		if err != nil {
			return "", "", nil, fmt.Errorf("unpacking archive %q to cache: %w", skillName, err)
		}
	}

	return skillMD, skillDir, extracted, nil
}

// UnpackArchive extracts a .tar.gz or .zip archive into destDir with kernel-enforced os.Root confinement:
// 1. Rejects path traversal sequences (..), absolute paths, Windows drive/UNC paths, and symlinks/hardlinks.
// 2. Limits total unpacked bytes and total entry count (decompression & inode bomb protection).
// 3. Strips group/world write permissions on extracted files.
// 4. Verifies that SKILL.md exists at the archive root.
func UnpackArchive(data []byte, destDir string, maxUnpackedBytes int64) ([]string, error) {
	if destDir == "" {
		return nil, errors.New("destination directory is empty")
	}
	if maxUnpackedBytes <= 0 {
		maxUnpackedBytes = DefaultMaxUnpackedBytes
	}

	cleanDest := filepath.Clean(destDir)
	if err := os.MkdirAll(cleanDest, 0700); err != nil {
		return nil, fmt.Errorf("creating destination directory: %w", err)
	}

	format := DetectArchiveFormat(data, "")
	if format == "" {
		return nil, ErrUnsupportedArchiveFormat
	}

	root, err := os.OpenRoot(cleanDest)
	if err != nil {
		return nil, fmt.Errorf("opening confined archive root %s: %w", cleanDest, err)
	}
	defer func() { _ = root.Close() }()

	var extracted []string
	var createdDirs []string

	cleanup := func() {
		for _, rel := range extracted {
			_ = root.Remove(filepath.FromSlash(rel))
		}
		for _, createdDir := range slices.Backward(createdDirs) {
			_ = root.Remove(filepath.FromSlash(createdDir))
		}
	}

	switch format {
	case "tar.gz":
		extracted, createdDirs, err = unpackTarGz(data, root, maxUnpackedBytes)
	case "zip":
		extracted, createdDirs, err = unpackZip(data, root, maxUnpackedBytes)
	}
	if err != nil {
		cleanup()
		return nil, err
	}

	hasSkillMD := false
	for _, f := range extracted {
		if f == "SKILL.md" || strings.EqualFold(f, "SKILL.md") {
			hasSkillMD = true
			break
		}
	}
	if !hasSkillMD {
		cleanup()
		return nil, ErrArchiveMissingSkillMD
	}

	return extracted, nil
}

func evalSymlinkPrefix(targetPath string) string {
	cleanTarget := filepath.Clean(targetPath)
	if realTarget, err := filepath.EvalSymlinks(cleanTarget); err == nil {
		return realTarget
	}
	parent := filepath.Dir(cleanTarget)
	relParts := []string{filepath.Base(cleanTarget)}
	for {
		if evalParent, pErr := filepath.EvalSymlinks(parent); pErr == nil {
			return filepath.Join(append([]string{evalParent}, relParts...)...)
		}
		relParts = append([]string{filepath.Base(parent)}, relParts...)
		nextParent := filepath.Dir(parent)
		if nextParent == parent {
			return cleanTarget
		}
		parent = nextParent
	}
}

// IsPathContained reports whether targetPath resolves inside baseDir after evaluating any existing symlinks.
func IsPathContained(baseDir, targetPath string) bool {
	cleanBase := filepath.Clean(baseDir)
	cleanTarget := filepath.Clean(targetPath)

	rel, err := filepath.Rel(cleanBase, cleanTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}

	realBase := evalSymlinkPrefix(cleanBase)
	realTarget := evalSymlinkPrefix(cleanTarget)
	realRel, err := filepath.Rel(realBase, realTarget)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func validateArchiveRelPath(targetRel string) (string, error) {
	if strings.ContainsRune(targetRel, 0) || strings.HasPrefix(targetRel, "/") || strings.HasPrefix(targetRel, "\\") || strings.Contains(targetRel, ":") || filepath.VolumeName(targetRel) != "" || filepath.IsAbs(targetRel) {
		return "", fmt.Errorf("%w: %q", ErrArchiveTraversal, targetRel)
	}
	slashNormalized := strings.ReplaceAll(targetRel, "\\", "/")
	cleanRel := filepath.ToSlash(filepath.Clean(slashNormalized))
	cleanRel = strings.TrimPrefix(cleanRel, "./")

	if cleanRel == "" || cleanRel == "." {
		return "", nil
	}
	if cleanRel == ".." || strings.HasPrefix(cleanRel, "../") || strings.Contains(cleanRel, "/../") || strings.HasPrefix(cleanRel, "/") {
		return "", fmt.Errorf("%w: %q", ErrArchiveTraversal, targetRel)
	}
	return cleanRel, nil
}

func safeExtractedMode(raw os.FileMode) os.FileMode {
	perm := raw.Perm()
	if perm == 0 {
		perm = 0600
	}
	// Strip group/other write bits and guarantee owner read/write.
	return (perm & 0755 &^ 0022) | 0600
}

func unpackTarGz(data []byte, root *os.Root, maxBytes int64) ([]string, []string, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("reading gzip header: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	// Bound the total decompressed tar stream so skipped/PAX entries cannot cause a gzip bomb.
	maxStream := maxBytes + int64(DefaultMaxArchiveFiles)*4096
	limited := io.LimitReader(gzr, maxStream+1)
	tr := tar.NewReader(limited)

	var extracted []string
	var createdDirs []string
	var totalBytes int64
	entries := 0

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if limited.(*io.LimitedReader).N == 0 {
				return extracted, createdDirs, fmt.Errorf("%w: decompressed stream exceeded limit", ErrArchiveDecompressionBomb)
			}
			return extracted, createdDirs, fmt.Errorf("reading tar entry: %w", err)
		}

		entries++
		if entries > DefaultMaxArchiveFiles {
			return extracted, createdDirs, fmt.Errorf("%w: exceeds %d entries", ErrArchiveDecompressionBomb, DefaultMaxArchiveFiles)
		}

		relPath, err := validateArchiveRelPath(hdr.Name)
		if err != nil {
			return extracted, createdDirs, err
		}
		if relPath == "" {
			continue
		}
		nativeRel := filepath.FromSlash(relPath)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(nativeRel, 0700); err != nil {
				return extracted, createdDirs, fmt.Errorf("creating dir %s: %w", relPath, err)
			}
			createdDirs = append(createdDirs, relPath)

		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > maxBytes-totalBytes {
				return extracted, createdDirs, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
			}

			if parent := filepath.Dir(nativeRel); parent != "." {
				if err := root.MkdirAll(parent, 0700); err != nil {
					return extracted, createdDirs, fmt.Errorf("creating parent dir for %s: %w", relPath, err)
				}
				createdDirs = append(createdDirs, filepath.ToSlash(parent))
			}

			perm := safeExtractedMode(os.FileMode(hdr.Mode))
			outFile, err := root.OpenFile(nativeRel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
			if err != nil {
				return extracted, createdDirs, fmt.Errorf("creating file %s: %w", relPath, err)
			}

			remaining := maxBytes - totalBytes
			written, copyErr := io.Copy(outFile, io.LimitReader(tr, remaining+1))
			closeErr := outFile.Close()
			extracted = append(extracted, relPath)
			totalBytes += written
			if totalBytes > maxBytes {
				return extracted, createdDirs, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
			}
			if copyErr != nil {
				return extracted, createdDirs, fmt.Errorf("writing file %s: %w", relPath, copyErr)
			}
			if closeErr != nil {
				return extracted, createdDirs, fmt.Errorf("closing file %s: %w", relPath, closeErr)
			}
			if written != hdr.Size {
				return extracted, createdDirs, fmt.Errorf("incomplete file write for %s (expected %d, wrote %d)", relPath, hdr.Size, written)
			}
			_ = root.Chmod(nativeRel, perm)

		case tar.TypeSymlink, tar.TypeLink:
			return extracted, createdDirs, fmt.Errorf("%w: entry %q -> %q", ErrArchiveSymlinkEscapes, hdr.Name, hdr.Linkname)

		default:
			// Skip non-regular metadata headers (PAX, GNU longname, etc.)
		}
	}

	return extracted, createdDirs, nil
}

func unpackZip(data []byte, root *os.Root, maxBytes int64) ([]string, []string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("reading zip header: %w", err)
	}
	if len(zr.File) > DefaultMaxArchiveFiles {
		return nil, nil, fmt.Errorf("%w: exceeds %d entries", ErrArchiveDecompressionBomb, DefaultMaxArchiveFiles)
	}

	var extracted []string
	var createdDirs []string
	var totalBytes int64

	for _, f := range zr.File {
		relPath, err := validateArchiveRelPath(f.Name)
		if err != nil {
			return extracted, createdDirs, err
		}
		if relPath == "" {
			continue
		}
		nativeRel := filepath.FromSlash(relPath)

		if f.Mode()&os.ModeSymlink != 0 {
			return extracted, createdDirs, fmt.Errorf("%w: zip symlink entry %q", ErrArchiveSymlinkEscapes, f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := root.MkdirAll(nativeRel, 0700); err != nil {
				return extracted, createdDirs, fmt.Errorf("creating dir %s: %w", relPath, err)
			}
			createdDirs = append(createdDirs, relPath)
			continue
		}

		if f.UncompressedSize64 > uint64(maxBytes) || int64(f.UncompressedSize64) > maxBytes-totalBytes {
			return extracted, createdDirs, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
		}

		if parent := filepath.Dir(nativeRel); parent != "." {
			if err := root.MkdirAll(parent, 0700); err != nil {
				return extracted, createdDirs, fmt.Errorf("creating parent dir for %s: %w", relPath, err)
			}
			createdDirs = append(createdDirs, filepath.ToSlash(parent))
		}

		rc, err := f.Open()
		if err != nil {
			return extracted, createdDirs, fmt.Errorf("opening zip file %s: %w", f.Name, err)
		}

		perm := safeExtractedMode(f.Mode())
		outFile, err := root.OpenFile(nativeRel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
		if err != nil {
			_ = rc.Close()
			return extracted, createdDirs, fmt.Errorf("creating file %s: %w", relPath, err)
		}

		remaining := maxBytes - totalBytes
		written, copyErr := io.Copy(outFile, io.LimitReader(rc, remaining+1))
		_ = rc.Close()
		closeErr := outFile.Close()
		extracted = append(extracted, relPath)
		totalBytes += written
		if totalBytes > maxBytes {
			return extracted, createdDirs, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
		}
		if copyErr != nil {
			return extracted, createdDirs, fmt.Errorf("writing zip file %s: %w", relPath, copyErr)
		}
		if closeErr != nil {
			return extracted, createdDirs, fmt.Errorf("closing zip file %s: %w", relPath, closeErr)
		}
		if f.UncompressedSize64 != 0 && written != int64(f.UncompressedSize64) {
			return extracted, createdDirs, fmt.Errorf("incomplete zip file write for %s (expected %d, wrote %d)", relPath, f.UncompressedSize64, written)
		}
		_ = root.Chmod(nativeRel, perm)
	}

	return extracted, createdDirs, nil
}
