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
	"runtime"
	"slices"
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
	ErrInvalidPath      = errors.New("invalid path or directory traversal detected")
	ErrExecutionTimeout = errors.New("script execution timed out")
	ErrRuntimeNotFound  = errors.New("interpreter runtime not found in PATH")
)

const (
	maxInstallFiles      = 200
	maxInstallTotalBytes = catalog.DefaultMaxUnpackedBytes
)

// Runner executes skill scripts, reads skill assets, and installs skill packages using
// an explicit cache directory and HTTP client (zero global mutable state).
type Runner struct {
	CacheDir   string
	HTTPClient *http.Client
}

// New creates a Runner configured with an optional cache directory and HTTP client.
func New(cacheDir string, client *http.Client) *Runner {
	return &Runner{
		CacheDir:   strings.TrimSpace(cacheDir),
		HTTPClient: client,
	}
}

func (r *Runner) resolveCacheRoot() string {
	override := ""
	if r != nil {
		override = r.CacheDir
	}
	dir, err := catalog.ResolveCacheDir(override)
	if err != nil {
		return ""
	}
	return dir
}

func (r *Runner) httpClient(explicit *http.Client) *http.Client {
	if explicit != nil {
		return explicit
	}
	if r != nil && r.HTTPClient != nil {
		return r.HTTPClient
	}
	return catalog.NewSafeHTTPClient(30 * time.Second)
}

func primarySkillName(item catalog.Item) string {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = strings.TrimSpace(item.ID)
	}
	if err := catalog.ValidateSkillName(name); err != nil {
		return ""
	}
	return name
}

// GetCandidateBaseDirs returns all existing local directories where a skill might be located.
func (r *Runner) GetCandidateBaseDirs(item catalog.Item) []string {
	var dirs []string
	addContained := func(root, candidate string) {
		if root == "" || candidate == "" {
			return
		}
		d := filepath.Clean(candidate)
		if !catalog.IsPathContained(root, d) {
			return
		}
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			if !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}

	validName := catalog.ValidateSkillName(item.Name) == nil
	validID := item.ID != "" && item.ID != item.Name && catalog.ValidateSkillName(item.ID) == nil

	// 1. Workspace .agents/skills/<name> or <id>
	if wd, err := os.Getwd(); err == nil && wd != "" {
		wsRoot := filepath.Join(wd, ".agents", "skills")
		if validName {
			addContained(wsRoot, filepath.Join(wsRoot, item.Name))
		}
		if validID {
			addContained(wsRoot, filepath.Join(wsRoot, item.ID))
		}
	}

	// 2. User Home ~/.agents/skills/<name> or <id>
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		homeRoot := filepath.Join(homeDir, ".agents", "skills")
		if validName {
			addContained(homeRoot, filepath.Join(homeRoot, item.Name))
		}
		if validID {
			addContained(homeRoot, filepath.Join(homeRoot, item.ID))
		}
	}

	// 3. Cache directory <cacheRoot>/skills/<scoped-key>
	if cacheRoot := r.resolveCacheRoot(); cacheRoot != "" {
		cacheSkillsRoot := filepath.Join(cacheRoot, "skills")
		expectedDigest := item.EffectiveDigest()
		canUseCache := func(skillName string) bool {
			if item.Type == catalog.ItemTypeSkillMD && item.Source != "local" {
				return false
			}
			if catalog.IsArchiveItem(item) && expectedDigest != "" {
				return catalog.IsArchiveCachedWithDigest(cacheRoot, skillName, item.Source, expectedDigest)
			}
			if item.URL != "" && item.Source != "local" {
				if expectedDigest == "" {
					return false
				}
				skillDir, err := catalog.SkillCacheDir(cacheRoot, skillName, item.Source)
				if err != nil {
					return false
				}
				f, err := os.Open(filepath.Join(skillDir, "SKILL.md"))
				if err != nil {
					return false
				}
				data, readErr := catalog.ReadBounded(f, catalog.DefaultMaxSkillMDBytes)
				_ = f.Close()
				return readErr == nil && len(data) > 0 && catalog.VerifyDigest(data, expectedDigest) == nil
			}
			return true
		}
		if validName && canUseCache(item.Name) {
			if dir, err := catalog.SkillCacheDir(cacheRoot, item.Name, item.Source); err == nil {
				addContained(cacheSkillsRoot, dir)
			}
		}
		if validID && canUseCache(item.ID) {
			if dir, err := catalog.SkillCacheDir(cacheRoot, item.ID, item.Source); err == nil {
				addContained(cacheSkillsRoot, dir)
			}
		}
	}

	// 4. Explicit item.Path
	if item.Path != "" {
		if fi, err := os.Stat(item.Path); err == nil {
			d := filepath.Clean(item.Path)
			if !fi.IsDir() {
				d = filepath.Dir(d)
			}
			if !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}

	return dirs
}

func writeFileAtomic(baseDir, targetPath string, data []byte, perm os.FileMode) error {
	if !catalog.IsPathContained(baseDir, targetPath) {
		return fmt.Errorf("runner: %w: %s escapes %s", ErrInvalidPath, targetPath, baseDir)
	}
	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(targetDir, "."+filepath.Base(targetPath)+"-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmpFile.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmpFile.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func (r *Runner) cacheSkillFile(item catalog.Item, relPath string, data []byte) error {
	skillName := primarySkillName(item)
	if skillName == "" || relPath == "" || len(data) == 0 {
		return nil
	}
	cleanRel := filepath.ToSlash(filepath.Clean(relPath))
	if strings.HasPrefix(cleanRel, "../") || strings.Contains(cleanRel, "/../") || strings.HasPrefix(cleanRel, "/") {
		return fmt.Errorf("runner: %w: %s", ErrInvalidPath, relPath)
	}
	cacheRoot := r.resolveCacheRoot()
	if cacheRoot == "" {
		return fmt.Errorf("runner: unable to determine cache directory")
	}
	skillDir, err := catalog.SkillCacheDir(cacheRoot, skillName, item.Source)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(skillDir, filepath.FromSlash(cleanRel))
	perm := os.FileMode(0600)
	if strings.HasPrefix(cleanRel, "scripts/") || strings.HasSuffix(cleanRel, ".sh") || strings.HasSuffix(cleanRel, ".py") || strings.HasSuffix(cleanRel, ".js") || strings.HasSuffix(cleanRel, ".ts") {
		perm = 0700
	}
	return writeFileAtomic(skillDir, targetPath, data, perm)
}

func detectInterpreter(scriptPath string) (string, []string, error) {
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
				if strings.HasPrefix(base, "python") {
					return resolvePythonCommand(scriptPath, interpFlags...)
				}

				// If shebang specifies an absolute path (e.g. #!/usr/bin/bash), honor it before PATH lookup
				// to prevent PATH hijacking.
				var bin string
				if filepath.IsAbs(interpName) {
					if found, err := exec.LookPath(interpName); err == nil {
						bin = found
					} else if found, err := exec.LookPath(base); err == nil {
						bin = found
					}
				} else {
					if found, err := exec.LookPath(base); err == nil {
						bin = found
					} else if found, err := exec.LookPath(interpName); err == nil {
						bin = found
					}
				}
				if bin == "" {
					return "", nil, fmt.Errorf("%w: shebang interpreter %q not found", ErrRuntimeNotFound, interpName)
				}
				return bin, append(append([]string{}, interpFlags...), scriptPath), nil
			}
		}
	}

	switch ext {
	case ".py":
		return resolvePythonCommand(scriptPath)
	case ".js", ".mjs", ".cjs":
		if bin, err := exec.LookPath("node"); err == nil {
			return bin, []string{scriptPath}, nil
		}
		if bin, err := exec.LookPath("bun"); err == nil {
			return bin, []string{"run", scriptPath}, nil
		}
		if bin, err := exec.LookPath("deno"); err == nil {
			return bin, []string{"run", scriptPath}, nil
		}
		return "", nil, fmt.Errorf("%w: JavaScript runtime not found (checked node, bun, deno in PATH)", ErrRuntimeNotFound)
	case ".ts":
		if bin, err := exec.LookPath("bun"); err == nil {
			return bin, []string{"run", scriptPath}, nil
		}
		if bin, err := exec.LookPath("deno"); err == nil {
			return bin, []string{"run", scriptPath}, nil
		}
		if bin, err := exec.LookPath("tsx"); err == nil {
			return bin, []string{scriptPath}, nil
		}
		if bin, err := exec.LookPath("node"); err == nil {
			return bin, []string{scriptPath}, nil
		}
		return "", nil, fmt.Errorf("%w: TypeScript runtime not found (checked bun, deno, tsx, node in PATH)", ErrRuntimeNotFound)
	case ".sh", ".bash":
		if bin, err := exec.LookPath("bash"); err == nil {
			return bin, []string{scriptPath}, nil
		}
		if bin, err := exec.LookPath("sh"); err == nil {
			return bin, []string{scriptPath}, nil
		}
		return "", nil, fmt.Errorf("%w: shell runtime not found (checked bash, sh in PATH)", ErrRuntimeNotFound)
	default:
		// Allow direct execution of compiled/extensionless binaries bundled in scripts/.
		return scriptPath, nil, nil
	}
}

func resolvePythonCommand(scriptPath string, interpFlags ...string) (string, []string, error) {
	if bin, err := exec.LookPath("uv"); err == nil {
		return bin, []string{"run", scriptPath}, nil
	}
	if bin, err := exec.LookPath("python3"); err == nil {
		return bin, append(append([]string{}, interpFlags...), scriptPath), nil
	}
	if bin, err := exec.LookPath("python"); err == nil {
		return bin, append(append([]string{}, interpFlags...), scriptPath), nil
	}
	return "", nil, fmt.Errorf("%w: Python runtime not found (checked uv, python3, python in PATH)", ErrRuntimeNotFound)
}

func validateRelativeScriptOrFilePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "." || trimmed == ".." || filepath.IsAbs(trimmed) ||
		strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") ||
		strings.Contains(trimmed, ":") || strings.ContainsRune(trimmed, 0) ||
		filepath.VolumeName(trimmed) != "" {
		return "", fmt.Errorf("runner: %w: %s", ErrInvalidPath, raw)
	}
	cleanPath := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(trimmed, "\\", "/")))
	cleanPath = strings.TrimPrefix(cleanPath, "./")
	if cleanPath == "" || cleanPath == "." || cleanPath == ".." ||
		strings.HasPrefix(cleanPath, "../") || strings.Contains(cleanPath, "/../") || strings.HasPrefix(cleanPath, "/") {
		return "", fmt.Errorf("runner: %w: %s", ErrInvalidPath, raw)
	}
	return cleanPath, nil
}

// RunScriptContext executes a bundled script or binary for a catalog item with context cancellation and custom I/O.
// Searches local candidate directories first; if not found, downloads and atomically caches the file on demand.
func (r *Runner) RunScriptContext(ctx context.Context, item catalog.Item, scriptName string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cleanName, err := validateRelativeScriptOrFilePath(scriptName)
	if err != nil {
		return err
	}

	var scriptPath string
	var baseDir string

	// 1. Search local candidate directories
	baseDirs := r.GetCandidateBaseDirs(item)
	for _, dir := range baseDirs {
		candidates := []string{
			filepath.Join(dir, "scripts", filepath.FromSlash(cleanName)),
			filepath.Join(dir, filepath.FromSlash(cleanName)),
		}
		for _, cand := range candidates {
			candClean := filepath.Clean(cand)
			if !catalog.IsPathContained(dir, candClean) {
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

	// 2. If not found locally, fetch JIT via ReadBytesContext and atomically cache it
	if scriptPath == "" {
		fetchTarget := cleanName
		if !strings.HasPrefix(cleanName, "scripts/") {
			fetchTarget = "scripts/" + cleanName
		}

		data, fetchErr := r.ReadBytesContext(ctx, item, fetchTarget, nil)
		if fetchErr != nil && fetchTarget != cleanName {
			if errors.Is(fetchErr, ErrFileNotFound) {
				data, fetchErr = r.ReadBytesContext(ctx, item, cleanName, nil)
				if fetchErr == nil {
					fetchTarget = cleanName
				}
			}
		}
		if fetchErr != nil && !errors.Is(fetchErr, ErrFileNotFound) {
			return fetchErr
		}

		if fetchErr == nil && len(data) > 0 {
			cacheRoot := r.resolveCacheRoot()
			skillName := primarySkillName(item)
			if cacheRoot == "" || skillName == "" {
				return fmt.Errorf("runner: unable to determine cache directory for %q", item.Name)
			}
			skillDir, err := catalog.SkillCacheDir(cacheRoot, skillName, item.Source)
			if err != nil {
				return err
			}
			cachedPath := filepath.Join(skillDir, filepath.FromSlash(fetchTarget))
			if err := writeFileAtomic(skillDir, cachedPath, data, 0700); err != nil {
				return fmt.Errorf("runner: caching script %s: %w", cachedPath, err)
			}
			scriptPath = cachedPath
			baseDir = skillDir
		}
	}

	if scriptPath == "" {
		return fmt.Errorf("%w: script %q not found in skill %q", ErrScriptNotFound, scriptName, item.Name)
	}

	bin, binArgs, err := detectInterpreter(scriptPath)
	if err != nil {
		return fmt.Errorf("runner: %w", err)
	}
	allArgs := append(binArgs, args...)

	cmd := exec.CommandContext(ctx, bin, allArgs...)
	cmd.WaitDelay = 2 * time.Second
	configureProcessGroup(cmd)

	if wd, err := os.Getwd(); err == nil && wd != "" {
		cmd.Dir = wd
	} else if baseDir != "" {
		cmd.Dir = baseDir
	}

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
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("runner: %w: %v", ErrExecutionTimeout, err)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return fmt.Errorf("runner: execution canceled: %w", ctx.Err())
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

	for i, e := range env {
		k, current, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		match := k == key
		if runtime.GOOS == "windows" {
			match = strings.EqualFold(k, key)
		}
		if match {
			if current != "" {
				env[i] = k + "=" + extra + string(os.PathListSeparator) + current
			} else {
				env[i] = k + "=" + extra
			}
			return env
		}
	}
	return append(env, key+"="+extra)
}

func deriveBaseURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}

	if strings.EqualFold(u.Hostname(), "github.com") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 4 && (parts[2] == "blob" || parts[2] == "tree") {
			owner := url.PathEscape(parts[0])
			repo := url.PathEscape(parts[1])
			branch := url.PathEscape(parts[3])
			cleanRest := strings.Join(parts[4:], "/")
			if strings.HasSuffix(strings.ToLower(cleanRest), ".md") {
				cleanRest = path.Dir(cleanRest)
				if cleanRest == "." {
					cleanRest = ""
				}
			}
			if cleanRest != "" {
				return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, branch, escapeURLPath(cleanRest))
			}
			return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", owner, repo, branch)
		}
	}

	if strings.HasSuffix(strings.ToLower(u.Path), ".md") {
		u.Path = path.Dir(u.Path)
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/")
}

func escapeURLPath(relPath string) string {
	segs := strings.Split(strings.Trim(relPath, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// ReadBytesContext reads any skill file (text or binary) from local dirs, cache, or remote URL.
func (r *Runner) ReadBytesContext(ctx context.Context, item catalog.Item, filePath string, client *http.Client) ([]byte, error) {
	return r.readBytesInternal(ctx, item, filePath, client, "", false)
}

func (r *Runner) readBytesInternal(ctx context.Context, item catalog.Item, filePath string, client *http.Client, skipDir string, preferRemote bool) ([]byte, error) {
	cleanPath, err := validateRelativeScriptOrFilePath(filePath)
	if err != nil {
		return nil, err
	}

	cleanSkip := ""
	if skipDir != "" {
		cleanSkip = filepath.Clean(skipDir)
	}

	readFromLocal := func() ([]byte, bool) {
		baseDirs := r.GetCandidateBaseDirs(item)
		for _, baseDir := range baseDirs {
			if cleanSkip != "" && filepath.Clean(baseDir) == cleanSkip {
				if item.Path == "" || (!catalog.IsPathContained(cleanSkip, item.Path) && filepath.Clean(item.Path) != cleanSkip) {
					continue
				}
			}
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
				if !catalog.IsPathContained(baseDir, candClean) {
					continue
				}
				f, err := os.Open(candClean)
				if err != nil {
					continue
				}
				data, readErr := catalog.ReadBounded(f, catalog.DefaultMaxUnpackedBytes)
				_ = f.Close()
				if readErr == nil {
					return data, true
				}
			}
		}
		return nil, false
	}

	if !preferRemote {
		if data, ok := readFromLocal(); ok {
			return data, nil
		}
	}

	remoteURL := item.URL
	if remoteURL == "" && item.Metadata != nil {
		remoteURL = item.Metadata["canonical"]
	}

	if remoteURL != "" {
		effectiveClient := r.httpClient(client)
		if catalog.IsArchiveItem(item) {
			cacheRoot := r.resolveCacheRoot()
			skillName := primarySkillName(item)
			if cacheRoot == "" || skillName == "" {
				return nil, fmt.Errorf("runner: unable to resolve archive cache dir for %q", item.Name)
			}
			_, cacheDir, _, err := catalog.FetchAndUnpackArchive(ctx, effectiveClient, remoteURL, item.EffectiveDigest(), cacheRoot, skillName, item.Source, false)
			if err != nil {
				return nil, fmt.Errorf("runner: %w", err)
			}
			candidates := []string{
				filepath.Join(cacheDir, filepath.FromSlash(cleanPath)),
			}
			if !strings.HasPrefix(cleanPath, "scripts/") {
				candidates = append(candidates, filepath.Join(cacheDir, "scripts", filepath.FromSlash(cleanPath)))
			}
			if !strings.HasPrefix(cleanPath, "references/") {
				candidates = append(candidates, filepath.Join(cacheDir, "references", filepath.FromSlash(cleanPath)))
			}
			for _, cachedTarget := range candidates {
				if catalog.IsPathContained(cacheDir, cachedTarget) {
					if f, err := os.Open(cachedTarget); err == nil {
						fileBytes, readErr := catalog.ReadBounded(f, catalog.DefaultMaxUnpackedBytes)
						_ = f.Close()
						if readErr == nil {
							return fileBytes, nil
						}
					}
				}
			}
			return nil, fmt.Errorf("runner: %w: %s in skill %s", ErrFileNotFound, filePath, item.Name)
		}

		baseURL := deriveBaseURL(remoteURL)
		if baseURL != "" {
			remotePaths := []string{cleanPath}
			if !strings.HasPrefix(cleanPath, "scripts/") && (strings.HasSuffix(cleanPath, ".py") || strings.HasSuffix(cleanPath, ".sh") || strings.HasSuffix(cleanPath, ".js") || strings.HasSuffix(cleanPath, ".ts") || strings.HasSuffix(cleanPath, ".bash")) {
				remotePaths = append([]string{"scripts/" + cleanPath}, remotePaths...)
			}
			if !strings.HasPrefix(cleanPath, "references/") && strings.HasSuffix(cleanPath, ".md") && cleanPath != "SKILL.md" {
				remotePaths = append([]string{"references/" + cleanPath}, remotePaths...)
			}

			for _, rp := range remotePaths {
				targetURL := baseURL + "/" + escapeURLPath(rp)
				resp, err := catalog.FetchWithRetryAndHeaders(ctx, effectiveClient, targetURL, version.UserAgent(), nil, 1, 100*time.Millisecond)
				if err != nil {
					if errors.Is(err, catalog.ErrInsecureURL) {
						return nil, err
					}
					continue
				}
				if resp.StatusCode == http.StatusOK {
					data, err := catalog.ReadBounded(resp.Body, 10<<20)
					_ = resp.Body.Close()
					if err == nil && len(data) > 0 {
						_ = r.cacheSkillFile(item, rp, data)
						return data, nil
					}
				} else {
					_ = resp.Body.Close()
				}
			}
		}
	}

	if preferRemote {
		if data, ok := readFromLocal(); ok {
			return data, nil
		}
	}

	return nil, fmt.Errorf("runner: %w: %s in skill %s", ErrFileNotFound, filePath, item.Name)
}

// ReadFileContext reads any text skill file verbatim without magic path resolution.
func (r *Runner) ReadFileContext(ctx context.Context, item catalog.Item, filePath string, client *http.Client) (string, error) {
	data, err := r.ReadBytesContext(ctx, item, filePath, client)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) != -1 {
		return "", fmt.Errorf("runner: %w: %s", ErrBinaryFile, filePath)
	}
	return string(data), nil
}

var (
	markdownLinkRegex  = regexp.MustCompile(`\[[^\]]*\]\(([^)#?]+)(?:[#?][^)]*)?\)`)
	markdownImgRegex   = regexp.MustCompile(`!\[[^\]]*\]\(([^)#?]+)(?:[#?][^)]*)?\)`)
	backtickPathRegex  = regexp.MustCompile("`((?:scripts|references|assets|evals)/[^`\\s]+)`")
	prosePathRegex     = regexp.MustCompile(`(?:^|[\s"'` + "`" + `\(\)\[\]])((?:scripts|references|assets|evals)/[a-zA-Z0-9_\-\./]+\.[a-zA-Z0-9_]+)`)
	kungfuRunRegex     = regexp.MustCompile(`kungfu\s+run\s+[a-zA-Z0-9_\-]+\s+([a-zA-Z0-9_\-\./]+)`)
	runnerCommandRegex = regexp.MustCompile(`(?:uv\s+run|python3?|bash|sh|node|bun(?:\s+run)?|deno(?:\s+run)?)\s+(?:\./)?scripts/([a-zA-Z0-9_\-\./]+\.[a-zA-Z0-9_]+)`)
)

func cleanCandidatePath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "#") ||
		strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "mailto:") {
		return "", false
	}
	if idx := strings.IndexAny(p, "?#"); idx != -1 {
		p = p[:idx]
	}
	if strings.ContainsAny(p, ":<>{}*") || strings.HasPrefix(p, "\\") || filepath.IsAbs(p) {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	clean = strings.TrimPrefix(clean, "./")
	if clean == "" || clean == "." || clean == ".." || clean == "SKILL.md" || clean == "skill.md" ||
		strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

// ExtractCandidates scans markdown text and extracts all referenced script, reference, asset, and evaluation paths.
func ExtractCandidates(content string) []string {
	var candidates []string
	seen := make(map[string]struct{})

	add := func(p string) {
		clean, ok := cleanCandidatePath(p)
		if !ok {
			return
		}
		if _, exists := seen[clean]; !exists {
			seen[clean] = struct{}{}
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
			scr := strings.TrimPrefix(strings.TrimPrefix(m[1], "./"), "scripts/")
			if scr != "" {
				add("scripts/" + scr)
			}
		}
	}
	for _, m := range runnerCommandRegex.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			scr := strings.TrimPrefix(strings.TrimPrefix(m[1], "./"), "scripts/")
			if scr != "" {
				add("scripts/" + scr)
			}
		}
	}

	sort.Strings(candidates)
	return candidates
}

// InstallSkillContext atomically stages and installs all skill files (SKILL.md, references, scripts, assets)
// into destDir, rolling back on error so destDir is never left half-written.
func (r *Runner) InstallSkillContext(ctx context.Context, item catalog.Item, destDir string, rawContent string, extraCandidates []string, client *http.Client) ([]string, error) {
	if destDir == "" {
		return nil, fmt.Errorf("runner: destination directory is empty")
	}

	cleanDest := filepath.Clean(destDir)
	parentDir := filepath.Dir(cleanDest)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, fmt.Errorf("runner: creating parent directory %s: %w", parentDir, err)
	}

	stageDir, err := os.MkdirTemp(parentDir, "."+filepath.Base(cleanDest)+"-stage-*")
	if err != nil {
		return nil, fmt.Errorf("runner: creating staging directory in %s: %w", parentDir, err)
	}
	defer func() {
		_ = os.RemoveAll(stageDir)
	}()

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

	remoteURL := item.URL
	if remoteURL == "" && item.Metadata != nil {
		remoteURL = item.Metadata["canonical"]
	}
	effectiveClient := r.httpClient(client)

	if catalog.IsArchiveItem(item) && remoteURL != "" {
		expectedDigest := item.EffectiveDigest()
		cacheRoot := r.resolveCacheRoot()
		skillName := primarySkillName(item)
		if expectedDigest != "" && cacheRoot != "" && skillName != "" && catalog.IsArchiveCachedWithDigest(cacheRoot, skillName, item.Source, expectedDigest) {
			if cacheSkillDir, err := catalog.SkillCacheDir(cacheRoot, skillName, item.Source); err == nil {
				if extracted, err := copyCachedArchiveDir(cacheSkillDir, stageDir); err == nil && len(extracted) > 0 {
					for _, rel := range extracted {
						recordInstalled(rel)
					}
					sort.Strings(installedFiles)
					if err := atomicSwapDir(stageDir, cleanDest); err != nil {
						return nil, err
					}
					return installedFiles, nil
				}
			}
		}

		// Download, enforce required digest, and unpack into cache or directly into stageDir.
		if cacheRoot != "" && skillName != "" {
			_, unpackedDir, _, err := catalog.FetchAndUnpackArchive(ctx, effectiveClient, remoteURL, expectedDigest, cacheRoot, skillName, item.Source, false)
			if err != nil {
				return nil, fmt.Errorf("runner: %w", err)
			}
			extracted, err := copyCachedArchiveDir(unpackedDir, stageDir)
			if err != nil {
				return nil, fmt.Errorf("runner: copying unpacked archive for %s: %w", item.Name, err)
			}
			for _, rel := range extracted {
				recordInstalled(rel)
			}
		} else {
			resp, err := catalog.FetchWithRetryAndHeaders(ctx, effectiveClient, remoteURL, version.UserAgent(), nil, 3, 100*time.Millisecond)
			if err != nil {
				return nil, fmt.Errorf("runner: downloading archive for %s: %w", item.Name, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return nil, &catalog.HTTPStatusError{StatusCode: resp.StatusCode, URL: remoteURL}
			}
			data, err := catalog.ReadBounded(resp.Body, catalog.DefaultMaxUnpackedBytes)
			if err != nil {
				return nil, fmt.Errorf("runner: reading archive data for %s: %w", item.Name, err)
			}
			if err := catalog.VerifyRequiredDigest(data, expectedDigest); err != nil {
				return nil, fmt.Errorf("runner: %w", err)
			}
			extracted, err := catalog.UnpackArchive(data, stageDir, catalog.DefaultMaxUnpackedBytes)
			if err != nil {
				return nil, fmt.Errorf("runner: unpacking archive for %s: %w", item.Name, err)
			}
			for _, rel := range extracted {
				recordInstalled(rel)
			}
		}

		sort.Strings(installedFiles)
		if err := atomicSwapDir(stageDir, cleanDest); err != nil {
			return nil, err
		}
		return installedFiles, nil
	}

	// 1. Write SKILL.md into stageDir
	skillFilePath := filepath.Join(stageDir, "SKILL.md")
	if !catalog.IsPathContained(stageDir, skillFilePath) {
		return nil, fmt.Errorf("runner: %w", ErrInvalidPath)
	}
	if rawContent != "" {
		if err := os.WriteFile(skillFilePath, []byte(rawContent), 0644); err != nil {
			return nil, fmt.Errorf("runner: writing %s: %w", skillFilePath, err)
		}
		recordInstalled("SKILL.md")
	} else {
		data, err := r.readBytesInternal(ctx, item, "SKILL.md", effectiveClient, cleanDest, remoteURL != "")
		if err != nil {
			// Fall back to existing local SKILL.md only if no remote source was provided.
			if remoteURL == "" {
				if existingData, rErr := os.ReadFile(filepath.Join(cleanDest, "SKILL.md")); rErr == nil {
					data = existingData
					err = nil
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("runner: fetching SKILL.md for %s: %w", item.Name, err)
		}
		rawContent = string(data)
		if err := os.WriteFile(skillFilePath, data, 0644); err != nil {
			return nil, fmt.Errorf("runner: writing %s: %w", skillFilePath, err)
		}
		recordInstalled("SKILL.md")
	}

	// 2. Crawl referenced companion files with strict count and total-byte caps.
	seenCandidates := make(map[string]struct{})
	var queue []string
	addCandidate := func(p string) {
		clean, ok := cleanCandidatePath(p)
		if !ok {
			return
		}
		if _, exists := seenCandidates[clean]; !exists {
			seenCandidates[clean] = struct{}{}
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

	preferRemote := remoteURL != "" && item.Source != "local"
	totalBytes := int64(len(rawContent))

	for len(queue) > 0 {
		if len(installedFiles) >= maxInstallFiles {
			return nil, fmt.Errorf("runner: skill %q exceeds maximum %d files", item.Name, maxInstallFiles)
		}
		target := queue[0]
		queue = queue[1:]

		if installedMap[target] {
			continue
		}

		data, err := r.readBytesInternal(ctx, item, target, effectiveClient, cleanDest, preferRemote)
		if err != nil {
			continue
		}
		totalBytes += int64(len(data))
		if totalBytes > maxInstallTotalBytes {
			return nil, fmt.Errorf("runner: skill %q exceeds maximum total install size (%d bytes)", item.Name, maxInstallTotalBytes)
		}

		destFilePath := filepath.Join(stageDir, filepath.FromSlash(target))
		if !catalog.IsPathContained(stageDir, destFilePath) {
			return nil, fmt.Errorf("runner: candidate %q escapes destination directory: %w", target, ErrInvalidPath)
		}
		if err := os.MkdirAll(filepath.Dir(destFilePath), 0755); err != nil {
			return nil, fmt.Errorf("runner: creating directory for %s: %w", destFilePath, err)
		}

		perm := os.FileMode(0644)
		if strings.HasPrefix(target, "scripts/") || strings.HasSuffix(target, ".sh") || strings.HasSuffix(target, ".py") || strings.HasSuffix(target, ".js") || strings.HasSuffix(target, ".ts") {
			perm = 0755
		}

		if err := os.WriteFile(destFilePath, data, perm); err != nil {
			return nil, fmt.Errorf("runner: writing %s: %w", destFilePath, err)
		}
		if err := os.Chmod(destFilePath, perm); err != nil {
			return nil, fmt.Errorf("runner: setting permissions on %s: %w", destFilePath, err)
		}
		recordInstalled(target)

		if strings.HasSuffix(strings.ToLower(target), ".md") && utf8.Valid(data) {
			for _, next := range ExtractCandidates(string(data)) {
				addCandidate(next)
			}
		}
	}

	sort.Strings(installedFiles)
	if err := atomicSwapDir(stageDir, cleanDest); err != nil {
		return nil, err
	}
	return installedFiles, nil
}

func atomicSwapDir(stageDir, cleanDest string) error {
	// Ensure stageDir has standard 0755 permissions when promoted to workspace/global skills dir.
	_ = os.Chmod(stageDir, 0755)

	parentDir := filepath.Dir(cleanDest)
	var oldDir string
	if _, err := os.Stat(cleanDest); err == nil {
		backup, mkErr := os.MkdirTemp(parentDir, "."+filepath.Base(cleanDest)+"-old-*")
		if mkErr != nil {
			return fmt.Errorf("runner: creating backup slot for %s: %w", cleanDest, mkErr)
		}
		_ = os.Remove(backup)
		if renErr := os.Rename(cleanDest, backup); renErr != nil {
			return fmt.Errorf("runner: moving existing skill dir %s aside: %w", cleanDest, renErr)
		}
		oldDir = backup
	}

	if err := os.Rename(stageDir, cleanDest); err != nil {
		if oldDir != "" {
			_ = os.Rename(oldDir, cleanDest)
		}
		return fmt.Errorf("runner: promoting staged install to %s: %w", cleanDest, err)
	}
	if oldDir != "" {
		_ = os.RemoveAll(oldDir)
	}
	return nil
}

func copyCachedArchiveDir(cacheSkillDir, cleanDest string) ([]string, error) {
	var extracted []string
	err := filepath.WalkDir(cacheSkillDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == cacheSkillDir {
			return nil
		}
		rel, err := filepath.Rel(cacheSkillDir, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(filepath.Clean(rel))
		if relSlash == ".digest" || relSlash == ".skillmd.digest" {
			return nil
		}
		destPath := filepath.Join(cleanDest, rel)
		if !catalog.IsPathContained(cleanDest, destPath) {
			return ErrInvalidPath
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runner: refusing to copy symlink %q from cache: %w", relSlash, catalog.ErrArchiveSymlinkEscapes)
		}
		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		data, readErr := catalog.ReadBounded(f, catalog.DefaultMaxUnpackedBytes)
		_ = f.Close()
		if readErr != nil {
			return readErr
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		perm := (info.Mode().Perm() & 0755 &^ 0022) | 0600
		if err := os.WriteFile(destPath, data, perm); err != nil {
			return err
		}
		_ = os.Chmod(destPath, perm)
		extracted = append(extracted, relSlash)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return extracted, nil
}
