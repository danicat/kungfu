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

package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/danicat/kungfu/internal/runner"
	"github.com/danicat/kungfu/internal/search"
)

func (a *App) RunRun(args []string) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		_, _ = fmt.Fprintln(a.Stderr, "USAGE:\n  kungfu run <skill> <script> [args...]\n\nExecute bundled skill scripts (supports Python/uv, Node/Bun/Deno, Shell).")
		return ExitUsage
	}
	if len(args) < 2 {
		_, _ = fmt.Fprintln(a.Stderr, "error: run requires <skill> <script> [args...]")
		return ExitUsage
	}
	skillName := args[0]
	scriptName := args[1]
	scriptArgs := args[2:]

	item, err := a.Store.Get(skillName)
	if err != nil {
		a.printNotFoundWithSuggestions("run", skillName)
		return MapErrorToExitCode(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	err = runner.RunScriptContext(ctx, *item, scriptName, scriptArgs, os.Stdin, a.Stdout, a.Stderr)
	if err != nil {
		if errors.Is(err, runner.ErrScriptNotFound) {
			_, _ = fmt.Fprintf(a.Stderr, "error: script %q not found in skill %q.\n", scriptName, skillName)
			scripts := a.listScripts(*item)
			if len(scripts) > 0 {
				suggs := search.Suggest(scriptName, scripts, 3)
				if len(suggs) > 0 {
					_, _ = fmt.Fprintln(a.Stderr, "\nDid you mean:")
					for _, s := range suggs {
						_, _ = fmt.Fprintf(a.Stderr, "  - kungfu run %s %s\n", skillName, s)
					}
				}
			}
			return ExitNotFound
		}
		if errors.Is(err, runner.ErrInvalidPath) {
			_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitUsage
		}
		if errors.Is(err, runner.ErrExecutionTimeout) {
			_, _ = fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitGeneral
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(a.Stderr, "error: running script %q: %v\n", scriptName, err)
		return MapErrorToExitCode(err)
	}
	a.recordSkillRun(item.Name)
	return ExitSuccess
}
