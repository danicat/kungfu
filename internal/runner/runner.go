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

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danicat/kungfu/internal/catalog"
)

var (
	ErrScriptNotFound   = errors.New("script not found")
	ErrFileNotFound     = errors.New("file not found")
	ErrBinaryFile       = errors.New("cannot stream binary file as text")
	ErrInvalidItemPath  = errors.New("invalid item path")
	ErrInvalidPath      = errors.New("invalid path or directory traversal detected")
	ErrExecutionTimeout = errors.New("script execution timed out")
)

// GetCandidateBaseDirs returns all existing local directories where a skill might be located.
func GetCandidateBaseDirs(item catalog.Item) []string {
	var dirs []string
	add := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			for _, existing := range dirs {
				if existing == d {
					return
				}
			}
			dirs = append(dirs, d)
		}
	}

	// 1. Workspace .agents/skills/<name> or <id>
	if wd, err := os.Getwd(); err == nil {
		if item.Name != "" {
			add(filepath.Join(wd, ".agents", "skills", item.Name))
		}
		if item.ID != "" && item.ID != item.Name {
			add(filepath.Join(wd, ".agents", "skills", item.ID))
		}
	}

	// 2. User Home ~/.agents/skills/<name> or <id>
	if homeDir, err := os.UserHomeDir(); err == nil {
		if item.Name != "" {
			add(filepath.Join(homeDir, ".agents", "skills", item.Name))
		}
		if item.ID != "" && item.ID != item.Name {
			add(filepath.Join(homeDir, ".agents", "skills", item.ID))
		}
	}

	// 3. Cache directory ~/.cache/kungfu/skills/<name> or <id>
	if homeDir, err := os.UserHomeDir(); err == nil {
		if item.Name != "" {
			add(filepath.Join(homeDir, ".cache", "kungfu", "skills", item.Name))
		}
		if item.ID != "" && item.ID != item.Name {
			add(filepath.Join(homeDir, ".cache", "kungfu", "skills", item.ID))
		}
	}

	// 4. Explicit item.Path
	if item.Path != "" {
		if fi, err := os.Stat(item.Path); err == nil {
			if fi.IsDir() {
				add(item.Path)
			} else {
				add(filepath.Dir(item.Path))
			}
		}
	}

	return dirs
}

func cacheSkillFile(skillName, relPath string, data []byte) {
	if skillName == "" || relPath == "" || len(data) == 0 {
		return
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return
	}
	targetPath := filepath.Join(homeDir, ".cache", "kungfu", "skills", skillName, filepath.FromSlash(relPath))
	_ = os.MkdirAll(filepath.Dir(targetPath), 0755)
	perm := os.FileMode(0644)
	if strings.HasPrefix(relPath, "scripts/") || strings.HasSuffix(relPath, ".sh") || strings.HasSuffix(relPath, ".py") || strings.HasSuffix(relPath, ".js") {
		perm = 0755
	}
	if err := os.WriteFile(targetPath, data, perm); err == nil {
		_ = os.Chmod(targetPath, perm)
	}
}

func resolveItemDir(item catalog.Item) (string, error) {
	dirs := GetCandidateBaseDirs(item)
	if len(dirs) > 0 {
		return dirs[0], nil
	}
	return "", fmt.Errorf("%w: path is empty", ErrInvalidItemPath)
}

func isContained(baseDir, targetPath string) bool {
	realBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		realBase = baseDir
	}
	realTarget, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		realTarget = targetPath
	}
	rel, err := filepath.Rel(realBase, realTarget)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func resolveScriptPath(baseDir, scriptName string) (string, error) {
	cleanName := filepath.Clean(strings.TrimSpace(scriptName))
	if cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, "..") {
		return "", fmt.Errorf("%w: %s", ErrInvalidPath, scriptName)
	}

	candidates := []string{
		filepath.Join(baseDir, "scripts", cleanName),
		filepath.Join(baseDir, cleanName),
	}
	for _, cand := range candidates {
		candClean := filepath.Clean(cand)
		if !isContained(baseDir, candClean) {
			continue
		}
		info, err := os.Stat(candClean)
		if err == nil && !info.IsDir() {
			return candClean, nil
		}
	}
	return "", fmt.Errorf("%w: %s (searched in %s)", ErrScriptNotFound, scriptName, baseDir)
}

func detectInterpreter(scriptPath string) (string, []string) {
	ext := strings.ToLower(filepath.Ext(scriptPath))

	if f, err := os.Open(scriptPath); err == nil {
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		_ = f.Close()
		if n > 2 && string(buf[:2]) == "#!" {
			shebang := string(buf[:n])
			if idx := strings.Index(shebang, "\n"); idx != -1 {
				shebang = shebang[:idx]
			}
			shebang = strings.TrimSpace(strings.TrimPrefix(shebang, "#!"))
			parts := strings.Fields(shebang)
			if len(parts) > 0 {
				var interpName string
				var interpFlags []string
				if filepath.Base(parts[0]) == "env" && len(parts) > 1 {
					if parts[1] == "-S" && len(parts) > 2 {
						interpName = parts[2]
						interpFlags = parts[3:]
					} else {
						interpName = parts[1]
						interpFlags = parts[2:]
					}
				} else {
					interpName = parts[0]
					interpFlags = parts[1:]
				}

				base := filepath.Base(interpName)
				switch {
				case strings.HasPrefix(base, "python"):
					return resolvePythonCommand(scriptPath)
				case strings.HasPrefix(base, "node") || strings.HasPrefix(base, "bun") || strings.HasPrefix(base, "deno") || strings.HasPrefix(base, "tsx"):
					bin := interpName
					if found, err := exec.LookPath(base); err == nil {
						bin = found
					}
					return bin, append(append([]string{}, interpFlags...), scriptPath)
				case strings.HasPrefix(base, "bash") || strings.HasPrefix(base, "sh") || strings.HasPrefix(base, "zsh"):
					bin := interpName
					if found, err := exec.LookPath(base); err == nil {
						bin = found
					}
					return bin, append(append([]string{}, interpFlags...), scriptPath)
				}
			}
		}
	}

	switch ext {
	case ".py":
		return resolvePythonCommand(scriptPath)
	case ".js", ".mjs", ".cjs":
		if bin, err := exec.LookPath("node"); err == nil {
			return bin, []string{scriptPath}
		}
		return "node", []string{scriptPath}
	case ".ts":
		if bin, err := exec.LookPath("bun"); err == nil {
			return bin, []string{"run", scriptPath}
		}
		if bin, err := exec.LookPath("deno"); err == nil {
			return bin, []string{"run", scriptPath}
		}
		if bin, err := exec.LookPath("tsx"); err == nil {
			return bin, []string{scriptPath}
		}
		if bin, err := exec.LookPath("node"); err == nil {
			return bin, []string{scriptPath}
		}
		return "node", []string{scriptPath}
	case ".sh", ".bash":
		if bin, err := exec.LookPath("bash"); err == nil {
			return bin, []string{scriptPath}
		}
		return "sh", []string{scriptPath}
	default:
		return scriptPath, nil
	}
}

func resolvePythonCommand(scriptPath string) (string, []string) {
	if bin, err := exec.LookPath("uv"); err == nil {
		return bin, []string{"run", scriptPath}
	}
	if bin, err := exec.LookPath("python3"); err == nil {
		return bin, []string{scriptPath}
	}
	if bin, err := exec.LookPath("python"); err == nil {
		return bin, []string{scriptPath}
	}
	return "python", []string{scriptPath}
}

// RunScriptContext executes a bundled script for a catalog item with context cancellation and custom I/O.
func RunScriptContext(ctx context.Context, item catalog.Item, scriptName string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	baseDir, err := resolveItemDir(item)
	if err != nil {
		return fmt.Errorf("runner: %w", err)
	}

	scriptPath, err := resolveScriptPath(baseDir, scriptName)
	if err != nil {
		return fmt.Errorf("runner: %w", err)
	}

	bin, binArgs := detectInterpreter(scriptPath)
	allArgs := append(binArgs, args...)

	cmd := exec.CommandContext(ctx, bin, allArgs...)
	cmd.WaitDelay = 2 * time.Second
	configureProcessGroup(cmd)
	cmd.Dir = baseDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()

	if stdin != nil {
		cmd.Stdin = stdin
	}

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("runner: %w: %v", ErrExecutionTimeout, err)
		}
		return fmt.Errorf("runner: execution failed: %w", err)
	}
	return nil
}

// RunScript executes a bundled script for a catalog item.
func RunScript(item catalog.Item, scriptName string, args []string, stdout, stderr io.Writer) error {
	return RunScriptContext(context.Background(), item, scriptName, args, os.Stdin, stdout, stderr)
}

func deriveBaseURL(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		if strings.HasSuffix(strings.ToLower(u.Path), ".md") {
			u.Path = path.Dir(u.Path)
		}
		u.RawQuery = ""
		u.Fragment = ""
		return strings.TrimSuffix(u.String(), "/")
	}
	baseURL := strings.TrimSuffix(rawURL, "/SKILL.md")
	baseURL = strings.TrimSuffix(baseURL, "/skill.md")
	return strings.TrimSuffix(baseURL, "/")
}

// ReadFileContext reads any skill file verbatim without magic path resolution.
func ReadFileContext(ctx context.Context, item catalog.Item, filePath string, client *http.Client) (string, error) {
	trimmed := strings.TrimSpace(filePath)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return "", fmt.Errorf("runner: %w: %s", ErrInvalidPath, filePath)
	}

	cleanPath := filepath.Clean(trimmed)
	cleanPath = filepath.ToSlash(cleanPath)
	cleanPath = strings.TrimPrefix(cleanPath, "./")
	if strings.Contains(cleanPath, "..") || filepath.IsAbs(trimmed) || strings.HasPrefix(cleanPath, "/") || strings.HasPrefix(trimmed, "\\") {
		return "", fmt.Errorf("runner: %w: %s", ErrInvalidPath, filePath)
	}

	// 1. Try local directories
	baseDirs := GetCandidateBaseDirs(item)
	for _, baseDir := range baseDirs {
		candPath := filepath.Join(baseDir, filepath.FromSlash(cleanPath))
		candClean := filepath.Clean(candPath)
		if !isContained(baseDir, candClean) {
			continue
		}
		data, err := os.ReadFile(candClean)
		if err == nil {
			if !utf8.Valid(data) || bytes.IndexByte(data, 0) != -1 {
				return "", fmt.Errorf("runner: %w: %s", ErrBinaryFile, filePath)
			}
			return string(data), nil
		}
	}

	// 2. Try remote URL
	if item.URL != "" {
		baseURL := deriveBaseURL(item.URL)
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Second}
		}

		targetURL := baseURL + "/" + strings.TrimPrefix(cleanPath, "/")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", "kungfu/0.1.0")
			resp, err := client.Do(req)
			if err == nil {
				if resp.StatusCode == http.StatusOK {
					data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
					resp.Body.Close()
					if err == nil && len(data) > 0 {
						if !utf8.Valid(data) || bytes.IndexByte(data, 0) != -1 {
							return "", fmt.Errorf("runner: %w: %s", ErrBinaryFile, filePath)
						}
						cacheSkillFile(item.Name, cleanPath, data)
						return string(data), nil
					}
				} else {
					resp.Body.Close()
				}
			}
		}
	}

	return "", fmt.Errorf("runner: %w: %s in skill %s", ErrFileNotFound, filePath, item.Name)
}

// ReadFile reads any skill file verbatim without magic path resolution.
func ReadFile(item catalog.Item, filePath string) (string, error) {
	return ReadFileContext(context.Background(), item, filePath, nil)
}

var markdownLinkPattern = regexp.MustCompile(`\[[^\]]*\]\(([^)#]+)(?:#[^)]+)?\)`)

// InstallSkillContext installs all skill files (SKILL.md, references, scripts, auxiliary docs) into destDir.
func InstallSkillContext(ctx context.Context, item catalog.Item, destDir string, rawContent string, extraCandidates []string, client *http.Client) ([]string, error) {
	if destDir == "" {
		return nil, fmt.Errorf("runner: destination directory is empty")
	}

	cleanDest := filepath.Clean(destDir)
	if err := os.MkdirAll(cleanDest, 0755); err != nil {
		return nil, fmt.Errorf("runner: creating destination directory %s: %w", cleanDest, err)
	}

	installedMap := make(map[string]bool)
	var installedFiles []string

	recordInstalled := func(rel string) {
		cleanRel := filepath.ToSlash(filepath.Clean(rel))
		cleanRel = strings.TrimPrefix(cleanRel, "./")
		if !installedMap[cleanRel] {
			installedMap[cleanRel] = true
			installedFiles = append(installedFiles, cleanRel)
		}
	}

	// 1. Copy from local candidate directories if one exists and differs from destDir
	baseDirs := GetCandidateBaseDirs(item)
	for _, baseDir := range baseDirs {
		cleanBase := filepath.Clean(baseDir)
		if cleanBase == cleanDest {
			continue
		}
		_ = filepath.Walk(cleanBase, func(srcPath string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if !isContained(cleanBase, srcPath) {
				return nil
			}
			rel, err := filepath.Rel(cleanBase, srcPath)
			if err != nil || rel == "." {
				return nil
			}
			// Skip .git, .DS_Store, hidden files
			parts := strings.Split(filepath.ToSlash(rel), "/")
			for _, p := range parts {
				if strings.HasPrefix(p, ".") && p != "." && p != ".." {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			destPath := filepath.Join(cleanDest, rel)
			if info.IsDir() {
				_ = os.MkdirAll(destPath, 0755)
				return nil
			}
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return nil
			}
			perm := info.Mode().Perm()
			if perm == 0 {
				perm = 0644
			}
			if strings.HasPrefix(rel, "scripts/") || strings.HasSuffix(rel, ".sh") || strings.HasSuffix(rel, ".py") || strings.HasSuffix(rel, ".js") {
				perm = 0755
			}
			if err := os.WriteFile(destPath, data, perm); err == nil {
				_ = os.Chmod(destPath, perm)
				recordInstalled(rel)
			}
			return nil
		})
	}

	// 2. Ensure SKILL.md is written
	skillFilePath := filepath.Join(cleanDest, "SKILL.md")
	if rawContent != "" {
		if err := os.WriteFile(skillFilePath, []byte(rawContent), 0644); err == nil {
			recordInstalled("SKILL.md")
		}
	} else if !installedMap["SKILL.md"] {
		if data, err := os.ReadFile(skillFilePath); err == nil {
			rawContent = string(data)
			recordInstalled("SKILL.md")
		} else if item.URL != "" {
			if content, err := ReadFileContext(ctx, item, "SKILL.md", client); err == nil {
				rawContent = content
				if err := os.WriteFile(skillFilePath, []byte(content), 0644); err == nil {
					recordInstalled("SKILL.md")
				}
			}
		}
	}

	// 3. Queue candidate files referenced in markdown and extraCandidates
	seenCandidates := make(map[string]bool)
	var queue []string

	addCandidate := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "#") || strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "mailto:") {
			return
		}
		if idx := strings.IndexAny(p, "?#"); idx != -1 {
			p = p[:idx]
		}
		clean := filepath.ToSlash(filepath.Clean(p))
		clean = strings.TrimPrefix(clean, "./")
		if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasPrefix(clean, "/") {
			return
		}
		if !seenCandidates[clean] {
			seenCandidates[clean] = true
			queue = append(queue, clean)
		}
	}

	scanMarkdown := func(md string) {
		matches := markdownLinkPattern.FindAllStringSubmatch(md, -1)
		for _, m := range matches {
			if len(m) > 1 {
				addCandidate(m[1])
			}
		}
	}

	if rawContent != "" {
		scanMarkdown(rawContent)
	}
	if item.Body != "" && item.Body != rawContent {
		scanMarkdown(item.Body)
	}
	for _, cand := range extraCandidates {
		addCandidate(cand)
	}

	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]

		if installedMap[target] {
			continue
		}

		content, err := ReadFileContext(ctx, item, target, client)
		if err != nil {
			continue
		}

		destFilePath := filepath.Join(cleanDest, filepath.FromSlash(target))
		if err := os.MkdirAll(filepath.Dir(destFilePath), 0755); err != nil {
			continue
		}

		var perm os.FileMode = 0644
		if strings.HasPrefix(target, "scripts/") || strings.HasSuffix(target, ".sh") || strings.HasSuffix(target, ".py") || strings.HasSuffix(target, ".js") {
			perm = 0755
		}

		if err := os.WriteFile(destFilePath, []byte(content), perm); err == nil {
			_ = os.Chmod(destFilePath, perm)
			recordInstalled(target)
			if strings.HasSuffix(strings.ToLower(target), ".md") {
				scanMarkdown(content)
			}
		}
	}

	sort.Strings(installedFiles)
	return installedFiles, nil
}

// InstallSkill installs all skill files (SKILL.md, references, scripts, auxiliary docs) into destDir.
func InstallSkill(item catalog.Item, destDir string, rawContent string, extraCandidates []string) ([]string, error) {
	return InstallSkillContext(context.Background(), item, destDir, rawContent, extraCandidates, nil)
}
