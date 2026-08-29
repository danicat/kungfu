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
	"github.com/danicat/kungfu/internal/version"
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
	cleanSkillName := filepath.Base(filepath.Clean(skillName))
	if cleanSkillName == "" || cleanSkillName == "." || cleanSkillName == ".." {
		return
	}
	cleanRel := filepath.ToSlash(filepath.Clean(relPath))
	if strings.HasPrefix(cleanRel, "../") || strings.Contains(cleanRel, "/../") || strings.HasPrefix(cleanRel, "/") {
		return
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return
	}
	targetPath := filepath.Join(homeDir, ".cache", "kungfu", "skills", cleanSkillName, filepath.FromSlash(cleanRel))
	_ = os.MkdirAll(filepath.Dir(targetPath), 0755)
	perm := os.FileMode(0644)
	if strings.HasPrefix(cleanRel, "scripts/") || strings.HasSuffix(cleanRel, ".sh") || strings.HasSuffix(cleanRel, ".py") || strings.HasSuffix(cleanRel, ".js") {
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
// It searches local candidate directories first; if not found, it downloads/caches the script on demand.
func RunScriptContext(ctx context.Context, item catalog.Item, scriptName string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cleanName := filepath.Clean(strings.TrimSpace(scriptName))
	if cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, "..") {
		return fmt.Errorf("%w: %s", ErrInvalidPath, scriptName)
	}

	var scriptPath string
	var baseDir string

	// 1. Search local candidate directories
	baseDirs := GetCandidateBaseDirs(item)
	for _, dir := range baseDirs {
		candidates := []string{
			filepath.Join(dir, "scripts", cleanName),
			filepath.Join(dir, cleanName),
		}
		for _, cand := range candidates {
			candClean := filepath.Clean(cand)
			if !isContained(dir, candClean) {
				continue
			}
			info, err := os.Stat(candClean)
			if err == nil && !info.IsDir() {
				scriptPath = candClean
				baseDir = dir
				break
			}
		}
		if scriptPath != "" {
			break
		}
	}

	// 2. If not found locally, fetch JIT via ReadBytesContext and cache it
	if scriptPath == "" {
		var fetchTarget string
		if strings.HasPrefix(cleanName, "scripts/") {
			fetchTarget = cleanName
		} else {
			fetchTarget = "scripts/" + cleanName
		}

		data, err := ReadBytesContext(ctx, item, fetchTarget, nil)
		if err != nil && fetchTarget != cleanName {
			data, err = ReadBytesContext(ctx, item, cleanName, nil)
			if err == nil {
				fetchTarget = cleanName
			}
		}

		if err == nil && len(data) > 0 {
			homeDir, herr := os.UserHomeDir()
			if herr == nil && homeDir != "" {
				cachedPath := filepath.Join(homeDir, ".cache", "kungfu", "skills", item.Name, filepath.FromSlash(fetchTarget))
				_ = os.MkdirAll(filepath.Dir(cachedPath), 0755)
				_ = os.WriteFile(cachedPath, data, 0755)
				_ = os.Chmod(cachedPath, 0755)
				scriptPath = cachedPath
				baseDir = filepath.Join(homeDir, ".cache", "kungfu", "skills", item.Name)
			}
		}
	}

	if scriptPath == "" {
		return fmt.Errorf("%w: script %q not found in skill %q", ErrScriptNotFound, scriptName, item.Name)
	}

	bin, binArgs := detectInterpreter(scriptPath)
	allArgs := append(binArgs, args...)

	cmd := exec.CommandContext(ctx, bin, allArgs...)
	cmd.WaitDelay = 2 * time.Second
	configureProcessGroup(cmd)

	// Set execution working directory: prefer workspace cwd so tools operate on user files
	if wd, err := os.Getwd(); err == nil && wd != "" {
		cmd.Dir = wd
	} else if baseDir != "" {
		cmd.Dir = baseDir
	}

	// Build environment variables with PYTHONPATH / NODE_PATH pointing to script and skill dir
	env := os.Environ()
	scriptDir := filepath.Dir(scriptPath)
	env = appendEnvPath(env, "PYTHONPATH", scriptDir, baseDir)
	env = appendEnvPath(env, "NODE_PATH", scriptDir, baseDir)
	cmd.Env = env

	cmd.Stdout = stdout
	cmd.Stderr = stderr
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

func appendEnvPath(env []string, key string, dirs ...string) []string {
	var validDirs []string
	for _, d := range dirs {
		if d != "" {
			validDirs = append(validDirs, d)
		}
	}
	if len(validDirs) == 0 {
		return env
	}
	extra := strings.Join(validDirs, string(os.PathListSeparator))

	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			current := strings.TrimPrefix(e, prefix)
			if current != "" {
				env[i] = prefix + extra + string(os.PathListSeparator) + current
			} else {
				env[i] = prefix + extra
			}
			return env
		}
	}
	return append(env, prefix+extra)
}

// RunScript executes a bundled script for a catalog item.
func RunScript(item catalog.Item, scriptName string, args []string, stdout, stderr io.Writer) error {
	return RunScriptContext(context.Background(), item, scriptName, args, os.Stdin, stdout, stderr)
}

func deriveBaseURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}

	// GitHub blob / tree handling:
	// https://github.com/owner/repo/blob/branch/path/SKILL.md -> https://raw.githubusercontent.com/owner/repo/branch/path
	if strings.HasPrefix(trimmed, "https://github.com/") || strings.HasPrefix(trimmed, "http://github.com/") {
		u, err := url.Parse(trimmed)
		if err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 4 && (parts[2] == "blob" || parts[2] == "tree") {
				owner := parts[0]
				repo := parts[1]
				branch := parts[3]
				rest := parts[4:]
				cleanRest := strings.Join(rest, "/")
				if strings.HasSuffix(strings.ToLower(cleanRest), ".md") {
					cleanRest = path.Dir(cleanRest)
					if cleanRest == "." {
						cleanRest = ""
					}
				}
				if cleanRest != "" {
					return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, branch, cleanRest)
				}
				return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", owner, repo, branch)
			}
		}
	}

	// raw.githubusercontent.com handling:
	if strings.HasPrefix(trimmed, "https://raw.githubusercontent.com/") || strings.HasPrefix(trimmed, "http://raw.githubusercontent.com/") {
		if u, err := url.Parse(trimmed); err == nil {
			if strings.HasSuffix(strings.ToLower(u.Path), ".md") {
				u.Path = path.Dir(u.Path)
			}
			u.RawQuery = ""
			u.Fragment = ""
			return strings.TrimSuffix(u.String(), "/")
		}
	}

	if u, err := url.Parse(trimmed); err == nil {
		if strings.HasSuffix(strings.ToLower(u.Path), ".md") {
			u.Path = path.Dir(u.Path)
		}
		u.RawQuery = ""
		u.Fragment = ""
		return strings.TrimSuffix(u.String(), "/")
	}
	baseURL := strings.TrimSuffix(trimmed, "/SKILL.md")
	baseURL = strings.TrimSuffix(baseURL, "/skill.md")
	return strings.TrimSuffix(baseURL, "/")
}

// ReadBytesContext reads any skill file (text or binary) from local dirs, cache, or remote URL.
func ReadBytesContext(ctx context.Context, item catalog.Item, filePath string, client *http.Client) ([]byte, error) {
	trimmed := strings.TrimSpace(filePath)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return nil, fmt.Errorf("runner: %w: %s", ErrInvalidPath, filePath)
	}

	cleanPath := filepath.Clean(trimmed)
	cleanPath = filepath.ToSlash(cleanPath)
	cleanPath = strings.TrimPrefix(cleanPath, "./")
	if strings.Contains(cleanPath, "..") || filepath.IsAbs(trimmed) || strings.HasPrefix(cleanPath, "/") || strings.HasPrefix(trimmed, "\\") {
		return nil, fmt.Errorf("runner: %w: %s", ErrInvalidPath, filePath)
	}

	// 1. Try local candidate base directories
	baseDirs := GetCandidateBaseDirs(item)
	for _, baseDir := range baseDirs {
		candidates := []string{
			filepath.Join(baseDir, filepath.FromSlash(cleanPath)),
		}
		if !strings.HasPrefix(cleanPath, "scripts/") {
			candidates = append(candidates, filepath.Join(baseDir, "scripts", filepath.FromSlash(cleanPath)))
		}
		if !strings.HasPrefix(cleanPath, "references/") {
			candidates = append(candidates, filepath.Join(baseDir, "references", filepath.FromSlash(cleanPath)))
		}

		for _, candPath := range candidates {
			candClean := filepath.Clean(candPath)
			if !isContained(baseDir, candClean) {
				continue
			}
			data, err := os.ReadFile(candClean)
			if err == nil {
				return data, nil
			}
		}
	}

	// 2. Try remote URL or canonical metadata URL
	remoteURL := item.URL
	if remoteURL == "" && item.Metadata != nil && item.Metadata["canonical"] != "" {
		remoteURL = item.Metadata["canonical"]
	}

	if remoteURL != "" {
		baseURL := deriveBaseURL(remoteURL)
		if baseURL != "" {
			if client == nil {
				client = &http.Client{Timeout: 15 * time.Second}
			}

			remotePaths := []string{cleanPath}
			if !strings.HasPrefix(cleanPath, "scripts/") && (strings.HasSuffix(cleanPath, ".py") || strings.HasSuffix(cleanPath, ".sh") || strings.HasSuffix(cleanPath, ".js") || strings.HasSuffix(cleanPath, ".ts") || strings.HasSuffix(cleanPath, ".bash")) {
				remotePaths = append([]string{"scripts/" + cleanPath}, remotePaths...)
			}
			if !strings.HasPrefix(cleanPath, "references/") && strings.HasSuffix(cleanPath, ".md") && cleanPath != "SKILL.md" {
				remotePaths = append([]string{"references/" + cleanPath}, remotePaths...)
			}

			for _, rp := range remotePaths {
				targetURL := baseURL + "/" + strings.TrimPrefix(rp, "/")
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
				if err != nil {
					continue
				}
				req.Header.Set("User-Agent", version.UserAgent())

				resp, err := client.Do(req)
				if err != nil {
					continue
				}
				if resp.StatusCode == http.StatusOK {
					data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
					resp.Body.Close()
					if err == nil && len(data) > 0 {
						cacheSkillFile(item.Name, rp, data)
						return data, nil
					}
				} else {
					resp.Body.Close()
				}
			}
		}
	}

	return nil, fmt.Errorf("runner: %w: %s in skill %s", ErrFileNotFound, filePath, item.Name)
}

// ReadFileContext reads any skill file verbatim without magic path resolution.
func ReadFileContext(ctx context.Context, item catalog.Item, filePath string, client *http.Client) (string, error) {
	data, err := ReadBytesContext(ctx, item, filePath, client)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) != -1 {
		return "", fmt.Errorf("runner: %w: %s", ErrBinaryFile, filePath)
	}
	return string(data), nil
}

// ReadFile reads any skill file verbatim without magic path resolution.
func ReadFile(item catalog.Item, filePath string) (string, error) {
	return ReadFileContext(context.Background(), item, filePath, nil)
}

var (
	markdownLinkRegex  = regexp.MustCompile(`\[[^\]]*\]\(([^)#?]+)(?:[#?][^)]*)?\)`)
	markdownImgRegex   = regexp.MustCompile(`!\[[^\]]*\]\(([^)#?]+)(?:[#?][^)]*)?\)`)
	backtickPathRegex  = regexp.MustCompile("`((?:scripts|references|assets|evals)/[^`\\s]+)`")
	prosePathRegex     = regexp.MustCompile(`(?:^|[\s"'` + "`" + `\(\)\[\]])((?:scripts|references|assets|evals)/[a-zA-Z0-9_\-\./]+\.[a-zA-Z0-9_]+)`)
	kungfuRunRegex     = regexp.MustCompile(`kungfu\s+run\s+[a-zA-Z0-9_\-]+\s+([a-zA-Z0-9_\-\.]+)`)
	runnerCommandRegex = regexp.MustCompile(`(?:uv\s+run|python3?|bash|sh|node|bun(?:\s+run)?|deno(?:\s+run)?)\s+(?:scripts/)?([a-zA-Z0-9_\-\./]+\.[a-zA-Z0-9_]+)`)
)

// ExtractCandidates scans markdown text and extracts all referenced script, reference, asset, and evaluation paths.
func ExtractCandidates(content string) []string {
	var candidates []string
	seen := make(map[string]bool)

	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "#") || strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "mailto:") {
			return
		}
		if idx := strings.IndexAny(p, "?#"); idx != -1 {
			p = p[:idx]
		}
		clean := filepath.ToSlash(filepath.Clean(p))
		clean = strings.TrimPrefix(clean, "./")
		if clean == "" || clean == "." || clean == ".." || clean == "SKILL.md" || clean == "skill.md" || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasPrefix(clean, "/") {
			return
		}
		if !seen[clean] {
			seen[clean] = true
			candidates = append(candidates, clean)
		}
	}

	for _, m := range markdownLinkRegex.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range markdownImgRegex.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range backtickPathRegex.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range prosePathRegex.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range kungfuRunRegex.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			scr := m[1]
			if !strings.HasPrefix(scr, "scripts/") {
				add("scripts/" + scr)
			} else {
				add(scr)
			}
		}
	}
	for _, m := range runnerCommandRegex.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			scr := m[1]
			if !strings.HasPrefix(scr, "scripts/") {
				add("scripts/" + scr)
			} else {
				add(scr)
			}
		}
	}

	sort.Strings(candidates)
	return candidates
}

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

	// 1. Ensure SKILL.md is written
	skillFilePath := filepath.Join(cleanDest, "SKILL.md")
	if rawContent != "" {
		if err := os.WriteFile(skillFilePath, []byte(rawContent), 0644); err == nil {
			recordInstalled("SKILL.md")
		}
	} else if !installedMap["SKILL.md"] {
		if data, err := os.ReadFile(skillFilePath); err == nil {
			rawContent = string(data)
			recordInstalled("SKILL.md")
		} else {
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
		if clean == "" || clean == "." || clean == ".." || clean == "SKILL.md" || clean == "skill.md" || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasPrefix(clean, "/") {
			return
		}
		if !seenCandidates[clean] {
			seenCandidates[clean] = true
			queue = append(queue, clean)
		}
	}

	if rawContent != "" {
		for _, c := range ExtractCandidates(rawContent) {
			addCandidate(c)
		}
	}
	if item.Body != "" && item.Body != rawContent {
		for _, c := range ExtractCandidates(item.Body) {
			addCandidate(c)
		}
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

		data, err := ReadBytesContext(ctx, item, target, client)
		if err != nil {
			continue
		}

		destFilePath := filepath.Join(cleanDest, filepath.FromSlash(target))
		if err := os.MkdirAll(filepath.Dir(destFilePath), 0755); err != nil {
			continue
		}

		var perm os.FileMode = 0644
		if strings.HasPrefix(target, "scripts/") || strings.HasSuffix(target, ".sh") || strings.HasSuffix(target, ".py") || strings.HasSuffix(target, ".js") || strings.HasSuffix(target, ".ts") {
			perm = 0755
		}

		if err := os.WriteFile(destFilePath, data, perm); err == nil {
			_ = os.Chmod(destFilePath, perm)
			recordInstalled(target)
			if strings.HasSuffix(strings.ToLower(target), ".md") && utf8.Valid(data) {
				for _, next := range ExtractCandidates(string(data)) {
					addCandidate(next)
				}
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
