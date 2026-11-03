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
			content, err := io.ReadAll(io.LimitReader(tr, 5<<20))
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
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("opening zip SKILL.md: %w", err)
			}
			content, err := io.ReadAll(io.LimitReader(rc, 5<<20))
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

// UnpackArchiveToCache wipes any stale cached skill directory, unpacks the archive into
// <cacheRoot>/skills/<skillName>, and records the normalized digest in .digest.
func UnpackArchiveToCache(data []byte, cacheRoot, skillName, expectedDigest string) ([]string, error) {
	if cacheRoot == "" || skillName == "" {
		return nil, errors.New("cacheRoot and skillName are required")
	}
	cleanSkillName := filepath.Base(filepath.Clean(skillName))
	if cleanSkillName == "" || cleanSkillName == "." || cleanSkillName == ".." {
		return nil, fmt.Errorf("invalid skill name %q", skillName)
	}
	skillDir := filepath.Join(cacheRoot, "skills", cleanSkillName)
	_ = os.RemoveAll(skillDir)

	extracted, err := UnpackArchive(data, skillDir, DefaultMaxUnpackedBytes)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(expectedDigest) != "" {
		_ = os.WriteFile(filepath.Join(skillDir, ".digest"), []byte(NormalizeDigest(expectedDigest)), 0644)
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

	if format == "tar.gz" {
		extracted, err = unpackTarGz(data, cleanDest, maxUnpackedBytes)
	} else if format == "zip" {
		extracted, err = unpackZip(data, cleanDest, maxUnpackedBytes)
	}

	if err != nil {
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
		return nil, ErrArchiveMissingSkillMD
	}

	return extracted, nil
}

func isArchiveContained(baseDir, targetPath string) bool {
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

	realTarget, err := filepath.EvalSymlinks(cleanTarget)
	if err != nil {
		parent := filepath.Dir(cleanTarget)
		var relParts []string
		relParts = append(relParts, filepath.Base(cleanTarget))
		for {
			evalParent, pErr := filepath.EvalSymlinks(parent)
			if pErr == nil {
				realTarget = filepath.Join(append([]string{evalParent}, relParts...)...)
				break
			}
			relParts = append([]string{filepath.Base(parent)}, relParts...)
			nextParent := filepath.Dir(parent)
			if nextParent == parent {
				realTarget = cleanTarget
				break
			}
			parent = nextParent
		}
	}

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
	if !isArchiveContained(destDir, fullPath) {
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
			return nil, fmt.Errorf("reading tar entry: %w", err)
		}

		fullPath, err := validateArchivePath(hdr.Name, destDir)
		if err != nil {
			return nil, err
		}
		if fullPath == "" {
			continue
		}

		relPath, _ := filepath.Rel(destDir, fullPath)
		relPath = filepath.ToSlash(relPath)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				return nil, fmt.Errorf("creating dir %s: %w", fullPath, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			totalBytes += hdr.Size
			if totalBytes > maxBytes {
				return nil, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
			}

			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return nil, fmt.Errorf("creating parent dir for %s: %w", fullPath, err)
			}

			perm := os.FileMode(hdr.Mode) & 0777
			if perm == 0 {
				perm = 0644
			}

			outFile, err := os.OpenFile(fullPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
			if err != nil {
				return nil, fmt.Errorf("creating file %s: %w", fullPath, err)
			}

			written, err := io.Copy(outFile, io.LimitReader(tr, maxBytes-totalBytes+hdr.Size))
			_ = outFile.Close()
			if err != nil {
				return nil, fmt.Errorf("writing file %s: %w", fullPath, err)
			}
			if written != hdr.Size {
				return nil, fmt.Errorf("incomplete file write for %s (expected %d, wrote %d)", fullPath, hdr.Size, written)
			}
			_ = os.Chmod(fullPath, perm)
			extracted = append(extracted, relPath)

		case tar.TypeSymlink:
			linkTarget := hdr.Linkname
			if filepath.IsAbs(linkTarget) {
				return nil, fmt.Errorf("%w: absolute symlink target %q", ErrArchiveSymlinkEscapes, linkTarget)
			}
			resolvedTarget := filepath.Clean(filepath.Join(filepath.Dir(fullPath), linkTarget))
			if !isArchiveContained(destDir, resolvedTarget) {
				return nil, fmt.Errorf("%w: symlink %q -> %q escapes %q", ErrArchiveSymlinkEscapes, hdr.Name, linkTarget, destDir)
			}

			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return nil, fmt.Errorf("creating parent dir for %s: %w", fullPath, err)
			}
			_ = os.Remove(fullPath)
			if err := os.Symlink(linkTarget, fullPath); err != nil {
				return nil, fmt.Errorf("creating symlink %s -> %s: %w", fullPath, linkTarget, err)
			}
			extracted = append(extracted, relPath)

		case tar.TypeLink:
			linkTarget := hdr.Linkname
			resolvedTarget := filepath.Clean(filepath.Join(destDir, filepath.FromSlash(linkTarget)))
			if !isArchiveContained(destDir, resolvedTarget) {
				return nil, fmt.Errorf("%w: hardlink %q -> %q escapes %q", ErrArchiveSymlinkEscapes, hdr.Name, linkTarget, destDir)
			}
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return nil, fmt.Errorf("creating parent dir for %s: %w", fullPath, err)
			}
			_ = os.Remove(fullPath)
			if err := os.Link(resolvedTarget, fullPath); err != nil {
				return nil, fmt.Errorf("creating hardlink %s -> %s: %w", fullPath, resolvedTarget, err)
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
			return nil, err
		}
		if fullPath == "" {
			continue
		}

		relPath, _ := filepath.Rel(destDir, fullPath)
		relPath = filepath.ToSlash(relPath)

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				return nil, fmt.Errorf("creating dir %s: %w", fullPath, err)
			}
			continue
		}

		totalBytes += int64(f.UncompressedSize64)
		if totalBytes > maxBytes {
			return nil, fmt.Errorf("%w: exceeded %d bytes", ErrArchiveDecompressionBomb, maxBytes)
		}

		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return nil, fmt.Errorf("creating parent dir for %s: %w", fullPath, err)
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening zip file %s: %w", f.Name, err)
		}

		if f.Mode()&os.ModeSymlink != 0 {
			linkTargetBytes, readErr := io.ReadAll(io.LimitReader(rc, 4096))
			_ = rc.Close()
			if readErr != nil {
				return nil, fmt.Errorf("reading zip symlink target %s: %w", f.Name, readErr)
			}
			linkTarget := string(linkTargetBytes)
			if filepath.IsAbs(linkTarget) {
				return nil, fmt.Errorf("%w: absolute symlink target %q", ErrArchiveSymlinkEscapes, linkTarget)
			}
			resolvedTarget := filepath.Clean(filepath.Join(filepath.Dir(fullPath), linkTarget))
			if !isArchiveContained(destDir, resolvedTarget) {
				return nil, fmt.Errorf("%w: zip symlink %q -> %q escapes %q", ErrArchiveSymlinkEscapes, f.Name, linkTarget, destDir)
			}
			_ = os.Remove(fullPath)
			if err := os.Symlink(linkTarget, fullPath); err != nil {
				return nil, fmt.Errorf("creating zip symlink %s -> %s: %w", fullPath, linkTarget, err)
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
			return nil, fmt.Errorf("creating file %s: %w", fullPath, err)
		}

		written, copyErr := io.Copy(outFile, io.LimitReader(rc, maxBytes-totalBytes+int64(f.UncompressedSize64)))
		_ = rc.Close()
		_ = outFile.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("writing zip file %s: %w", fullPath, copyErr)
		}
		if written != int64(f.UncompressedSize64) {
			return nil, fmt.Errorf("incomplete zip file write for %s (expected %d, wrote %d)", fullPath, f.UncompressedSize64, written)
		}
		_ = os.Chmod(fullPath, perm)
		extracted = append(extracted, relPath)
	}

	return extracted, nil
}
