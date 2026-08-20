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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/danicat/kungfu/internal/catalog"
)

var (
	ErrScriptNotFound    = errors.New("script not found")
	ErrReferenceNotFound = errors.New("reference document not found")
	ErrInvalidItemPath   = errors.New("invalid item path")
	ErrInvalidPath       = errors.New("invalid path or directory traversal detected")
	ErrExecutionTimeout  = errors.New("script execution timed out")
)

func resolveItemDir(item catalog.Item) (string, error) {
	if item.Path == "" {
		return "", fmt.Errorf("%w: path is empty", ErrInvalidItemPath)
	}
	info, err := os.Stat(item.Path)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrInvalidItemPath, item.Path, err)
	}
	if info.IsDir() {
		return filepath.Clean(item.Path), nil
	}
	return filepath.Dir(filepath.Clean(item.Path)), nil
}

func isContained(baseDir, targetPath string) bool {
	rel, err := filepath.Rel(baseDir, targetPath)
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
		buf := make([]byte, 128)
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

// ReadReference reads a companion reference document for a catalog item.
func ReadReference(item catalog.Item, refName string) (string, error) {
	baseDir, err := resolveItemDir(item)
	if err != nil {
		return "", fmt.Errorf("runner: %w", err)
	}

	cleanRef := filepath.Clean(strings.TrimSpace(refName))
	if cleanRef == "." || cleanRef == ".." || strings.HasPrefix(cleanRef, "..") {
		return "", fmt.Errorf("runner: %w: %s", ErrInvalidPath, refName)
	}

	candidates := []string{
		filepath.Join(baseDir, "references", cleanRef),
		filepath.Join(baseDir, "references", cleanRef+".md"),
		filepath.Join(baseDir, cleanRef),
		filepath.Join(baseDir, cleanRef+".md"),
	}

	for _, cand := range candidates {
		candClean := filepath.Clean(cand)
		if !isContained(baseDir, candClean) {
			continue
		}
		data, err := os.ReadFile(candClean)
		if err == nil {
			return string(data), nil
		}
	}

	return "", fmt.Errorf("runner: %w: %s in %s", ErrReferenceNotFound, refName, baseDir)
}
