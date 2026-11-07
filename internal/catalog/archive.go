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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Archive safety errors per Cloudflare Agent Skills Discovery RFC v0.2.0.
var (
	ErrArchiveTraversal         = errors.New("archive contains path traversal or absolute path")
	ErrArchiveSymlinkEscapes    = errors.New("archive contains symlink or hardlink escaping skill directory")
	ErrArchiveDecompressionBomb = errors.New("archive exceeds maximum unpacked size limit")
	ErrArchiveMissingSkillMD    = errors.New("archive missing required SKILL.md at root")
	ErrUnsupportedArchiveFormat = errors.New("unsupported archive format (expected .tar.gz or .zip)")
)

// DefaultMaxUnpackedBytes limits total unpacked size to prevent decompression bombs (50 MB).
const DefaultMaxUnpackedBytes = 50 * 1024 * 1024

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
	return "", errors.New("unrecognized archive format")
}

func extractSkillMDTarGz(data []byte) (string, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("reading gzip: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading tar entry: %w", err)
		}

		cleanName := strings.TrimPrefix(hdr.Name, "./")
		if cleanName == "SKILL.md" || strings.EqualFold(cleanName, "SKILL.md") {
			content, err := ReadBounded(tr, 5<<20)
			if err != nil {
				return "", fmt.Errorf("reading SKILL.md: %w", err)
			}
			return string(content), nil
		}
	}
	return "", errors.New("SKILL.md not found in archive")
}

func extractSkillMDZip(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("reading zip: %w", err)
	}
	for _, f := range zr.File {
		cleanName := strings.TrimPrefix(f.Name, "./")
		if cleanName == "SKILL.md" || strings.EqualFold(cleanName, "SKILL.md") {
			if f.UncompressedSize64 > 5<<20 {
				return "", fmt.Errorf("%w: SKILL.md exceeds 5 MiB", ErrArchiveDecompressionBomb)
			}
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("opening zip SKILL.md: %w", err)
			}
			content, err := ReadBounded(rc, 5<<20)
			_ = rc.Close()
			if err != nil {
				return "", fmt.Errorf("reading zip SKILL.md: %w", err)
			}
			return string(content), nil
		}
	}
	return "", errors.New("SKILL.md not found in archive")
}

// IsArchiveCachedWithDigest reports whether <cacheRoot>/skills/<skillName> has been unpacked
// and matches expectedDigest (when expectedDigest is non-empty).
func IsArchiveCachedWithDigest(cacheRoot, skillName, expectedDigest string) bool {
	if cacheRoot == "" || skillName == "" {
		return false
	}
	cleanSkillName := filepath.Base(filepath.Clean(skillName))
	if cleanSkillName == "" || cleanSkillName == "." || cleanSkillName == ".." {
		return false
	}
	skillDir := filepath.Join(cacheRoot, "skills", cleanSkillName)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return false
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
// records the normalized digest in .digest, and atomically swaps it into <cacheRoot>/skills/<skillName>.
func UnpackArchiveToCache(data []byte, cacheRoot, skillName, expectedDigest string) ([]string, error) {
	if cacheRoot == "" || skillName == "" {
		return nil, errors.New("cacheRoot and skillName are required")
	}
	cleanSkillName := filepath.Base(filepath.Clean(skillName))
	if cleanSkillName == "" || cleanSkillName == "." || cleanSkillName == ".." {
		return nil, fmt.Errorf("invalid skill name %q", skillName)
	}
	skillsParentDir := filepath.Join(cacheRoot, "skills")
	if err := os.MkdirAll(skillsParentDir, 0755); err != nil {
		return nil, fmt.Errorf("creating skills cache dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp(skillsParentDir, "."+cleanSkillName+"-tmp-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp archive dir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	extracted, err := UnpackArchive(data, tmpDir, DefaultMaxUnpackedBytes)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(expectedDigest) != "" {
		_ = os.WriteFile(filepath.Join(tmpDir, ".digest"), []byte(NormalizeDigest(expectedDigest)), 0644)
	}

	// If another concurrent process already unpacked the exact same digest, leave skillDir intact.
	if strings.TrimSpace(expectedDigest) != "" && IsArchiveCachedWithDigest(cacheRoot, cleanSkillName, expectedDigest) {
		return extracted, nil
	}

	skillDir := filepath.Join(skillsParentDir, cleanSkillName)
	var oldDir string
	if _, statErr := os.Stat(skillDir); statErr == nil {
		if trashDir, mkErr := os.MkdirTemp(skillsParentDir, "."+cleanSkillName+"-old-*"); mkErr == nil {
			_ = os.Remove(trashDir)
			if renErr := os.Rename(skillDir, trashDir); renErr == nil {
				oldDir = trashDir
			}
		}
	}
	defer func() {
		if oldDir != "" {
			_ = os.RemoveAll(oldDir)
		}
	}()

	if err := os.Rename(tmpDir, skillDir); err != nil {
		if strings.TrimSpace(expectedDigest) != "" && IsArchiveCachedWithDigest(cacheRoot, cleanSkillName, expectedDigest) {
			return extracted, nil
		}
		return nil, fmt.Errorf("moving unpacked skill into cache: %w", err)
	}
	return extracted, nil
}

// UnpackArchive extracts a .tar.gz or .zip archive into destDir with strict RFC v0.2.0 safety checks:
// 1. Rejects path traversal sequences (..) or absolute paths.
// 2. Rejects symlinks or hardlinks that resolve outside the destination directory.
// 3. Rejects archives whose total unpacked size exceeds maxUnpackedBytes (decompression bomb protection).
// 4. Verifies that SKILL.md exists at the archive root.
func UnpackArchive(data []byte, destDir string, maxUnpackedBytes int64) ([]string, error) {
	if destDir == "" {
		return nil, errors.New("destination directory is empty")
	}
	if maxUnpackedBytes <= 0 {
		maxUnpackedBytes = DefaultMaxUnpackedBytes
	}

	cleanDest := filepath.Clean(destDir)
	if err := os.MkdirAll(cleanDest, 0755); err != nil {
		return nil, fmt.Errorf("creating destination directory: %w", err)
	}

	format := DetectArchiveFormat(data, "")
	if format == "" {
		return nil, ErrUnsupportedArchiveFormat
	}

	var extracted []string
	var err error

	cleanupExtracted := func(files []string) {
		for _, rel := range files {
			p := filepath.Join(cleanDest, filepath.FromSlash(rel))
			_ = os.Remove(p)
			for dir := filepath.Dir(p); dir != cleanDest && strings.HasPrefix(dir, cleanDest+string(filepath.Separator)); dir = filepath.Dir(dir) {
				if err := os.Remove(dir); err != nil {
					break
				}
			}
		}
	}

	switch format {
	case "tar.gz":
		extracted, err = unpackTarGz(data, cleanDest, maxUnpackedBytes)
	case "zip":
		extracted, err = unpackZip(data, cleanDest, maxUnpackedBytes)
	}

	if err != nil {
		cleanupExtracted(extracted)
		return nil, err
	}

	// Verify SKILL.md exists at root
	hasSkillMD := false
	for _, f := range extracted {
		if f == "SKILL.md" || strings.EqualFold(f, "SKILL.md") {
			hasSkillMD = true
			break
		}
	}
	if !hasSkillMD {
		cleanupExtracted(extracted)
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

	realBase, err := filepath.EvalSymlinks(cleanBase)
	if err != nil {
		realBase = cleanBase
	}

	realTarget := evalSymlinkPrefix(cleanTarget)
	realRel, err := filepath.Rel(realBase, realTarget)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func validateArchivePath(targetRel, destDir string) (string, error) {
	cleanRel := filepath.ToSlash(filepath.Clean(targetRel))
	cleanRel = strings.TrimPrefix(cleanRel, "./")

	if cleanRel == "" || cleanRel == "." {
		return "", nil // root directory, skip
	}
	if cleanRel == ".." || strings.HasPrefix(cleanRel, "../") || strings.Contains(cleanRel, "/../") || filepath.IsAbs(targetRel) || strings.HasPrefix(cleanRel, "/") || strings.HasPrefix(targetRel, "\\") || strings.Contains(targetRel, ":") {
		return "", fmt.Errorf("%w: %q", ErrArchiveTraversal, targetRel)
	}

	fullPath := filepath.Join(destDir, filepath.FromSlash(cleanRel))
	if !IsPathContained(destDir, fullPath) {
		return "", fmt.Errorf("%w: %q escapes %q", ErrArchiveTraversal, targetRel, destDir)
	}
	return fullPath, nil
}

func unpackTarGz(data []byte, destDir string, maxBytes int64) ([]string, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("reading gzip header: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	tr := tar.NewReader(gzr)
	var extracted []string
	var totalBytes int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return extracted, fmt.Errorf("reading tar entry: %w", err)
		}

		fullPath, err := validateArchivePath(hdr.Name, destDir)
		if err != nil {
			return extracted, err
		}
		if fullPath == "" {
			continue
		}

		relPath, _ := filepath.Rel(destDir, fullPath)
		relPath = filepath.ToSlash(relPath)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				return extracted, fmt.Errorf("creating dir %s: %w", fullPath, err)
			}

		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > maxBytes-totalBytes {
				return extracted, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
			}

			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return extracted, fmt.Errorf("creating parent dir for %s: %w", fullPath, err)
			}

			perm := os.FileMode(hdr.Mode) & 0777
			if perm == 0 {
				perm = 0644
			}

			outFile, err := os.OpenFile(fullPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
			if err != nil {
				return extracted, fmt.Errorf("creating file %s: %w", fullPath, err)
			}

			remaining := maxBytes - totalBytes
			written, err := io.Copy(outFile, io.LimitReader(tr, remaining+1))
			_ = outFile.Close()
			extracted = append(extracted, relPath)
			totalBytes += written
			if totalBytes > maxBytes {
				return extracted, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
			}
			if err != nil {
				return extracted, fmt.Errorf("writing file %s: %w", fullPath, err)
			}
			if written != hdr.Size {
				return extracted, fmt.Errorf("incomplete file write for %s (expected %d, wrote %d)", fullPath, hdr.Size, written)
			}
			_ = os.Chmod(fullPath, perm)

		case tar.TypeSymlink:
			linkTarget := hdr.Linkname
			if filepath.IsAbs(linkTarget) {
				return extracted, fmt.Errorf("%w: absolute symlink target %q", ErrArchiveSymlinkEscapes, linkTarget)
			}
			realParent := evalSymlinkPrefix(filepath.Dir(fullPath))
			resolvedTarget := filepath.Clean(filepath.Join(realParent, linkTarget))
			if !IsPathContained(destDir, resolvedTarget) {
				return extracted, fmt.Errorf("%w: symlink %q -> %q escapes %q", ErrArchiveSymlinkEscapes, hdr.Name, linkTarget, destDir)
			}

			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return extracted, fmt.Errorf("creating parent dir for %s: %w", fullPath, err)
			}
			_ = os.Remove(fullPath)
			if err := os.Symlink(linkTarget, fullPath); err != nil {
				return extracted, fmt.Errorf("creating symlink %s -> %s: %w", fullPath, linkTarget, err)
			}
			extracted = append(extracted, relPath)

		case tar.TypeLink:
			linkTarget := hdr.Linkname
			if filepath.IsAbs(linkTarget) {
				return extracted, fmt.Errorf("%w: absolute hardlink target %q", ErrArchiveSymlinkEscapes, linkTarget)
			}
			resolvedTarget := filepath.Clean(filepath.Join(destDir, filepath.FromSlash(linkTarget)))
			if !IsPathContained(destDir, resolvedTarget) {
				return extracted, fmt.Errorf("%w: hardlink %q -> %q escapes %q", ErrArchiveSymlinkEscapes, hdr.Name, linkTarget, destDir)
			}
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return extracted, fmt.Errorf("creating parent dir for %s: %w", fullPath, err)
			}
			_ = os.Remove(fullPath)
			if err := os.Link(resolvedTarget, fullPath); err != nil {
				return extracted, fmt.Errorf("creating hardlink %s -> %s: %w", fullPath, resolvedTarget, err)
			}
			extracted = append(extracted, relPath)

		default:
			// Skip unsupported header types (devices, fifos, etc.)
		}
	}

	return extracted, nil
}

func unpackZip(data []byte, destDir string, maxBytes int64) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("reading zip header: %w", err)
	}

	var extracted []string
	var totalBytes int64

	for _, f := range zr.File {
		fullPath, err := validateArchivePath(f.Name, destDir)
		if err != nil {
			return extracted, err
		}
		if fullPath == "" {
			continue
		}

		relPath, _ := filepath.Rel(destDir, fullPath)
		relPath = filepath.ToSlash(relPath)

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				return extracted, fmt.Errorf("creating dir %s: %w", fullPath, err)
			}
			continue
		}

		if f.UncompressedSize64 > uint64(maxBytes) || int64(f.UncompressedSize64) > maxBytes-totalBytes {
			return extracted, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
		}

		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return extracted, fmt.Errorf("creating parent dir for %s: %w", fullPath, err)
		}

		rc, err := f.Open()
		if err != nil {
			return extracted, fmt.Errorf("opening zip file %s: %w", f.Name, err)
		}

		if f.Mode()&os.ModeSymlink != 0 {
			linkTargetBytes, readErr := ReadBounded(rc, 4096)
			_ = rc.Close()
			if readErr != nil {
				return extracted, fmt.Errorf("reading zip symlink target %s: %w", f.Name, readErr)
			}
			totalBytes += int64(len(linkTargetBytes))
			if totalBytes > maxBytes {
				return extracted, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
			}
			linkTarget := string(linkTargetBytes)
			if filepath.IsAbs(linkTarget) {
				return extracted, fmt.Errorf("%w: absolute symlink target %q", ErrArchiveSymlinkEscapes, linkTarget)
			}
			realParent := evalSymlinkPrefix(filepath.Dir(fullPath))
			resolvedTarget := filepath.Clean(filepath.Join(realParent, linkTarget))
			if !IsPathContained(destDir, resolvedTarget) {
				return extracted, fmt.Errorf("%w: zip symlink %q -> %q escapes %q", ErrArchiveSymlinkEscapes, f.Name, linkTarget, destDir)
			}
			_ = os.Remove(fullPath)
			if err := os.Symlink(linkTarget, fullPath); err != nil {
				return extracted, fmt.Errorf("creating zip symlink %s -> %s: %w", fullPath, linkTarget, err)
			}
			extracted = append(extracted, relPath)
			continue
		}

		perm := f.Mode().Perm()
		if perm == 0 {
			perm = 0644
		}

		outFile, err := os.OpenFile(fullPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
		if err != nil {
			_ = rc.Close()
			return extracted, fmt.Errorf("creating file %s: %w", fullPath, err)
		}

		remaining := maxBytes - totalBytes
		written, copyErr := io.Copy(outFile, io.LimitReader(rc, remaining+1))
		_ = rc.Close()
		_ = outFile.Close()
		extracted = append(extracted, relPath)
		totalBytes += written
		if totalBytes > maxBytes {
			return extracted, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
		}
		if copyErr != nil {
			return extracted, fmt.Errorf("writing zip file %s: %w", fullPath, copyErr)
		}
		if f.UncompressedSize64 != 0 && written != int64(f.UncompressedSize64) {
			return extracted, fmt.Errorf("incomplete zip file write for %s (expected %d, wrote %d)", fullPath, f.UncompressedSize64, written)
		}
		_ = os.Chmod(fullPath, perm)
	}

	return extracted, nil
}
