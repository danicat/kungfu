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

package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/danicat/kungfu/internal/search"
	"github.com/danicat/kungfu/internal/version"
)

//go:embed all:skills
var embeddedSkills embed.FS

func main() {
	code := Run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// Run parses global options and dispatches to subcommands.
func Run(args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	var opts GlobalOptions
	var showVersion, showHelp bool
	var jsonFlag, rawFlag bool

	fs := flag.NewFlagSet("kungfu", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")
	fs.BoolVar(&rawFlag, "raw", false, "Output in raw format")
	fs.StringVar(&opts.Config, "config", "", "Path to custom config file")
	fs.BoolVar(&opts.Verbose, "verbose", false, "Enable verbose logging")
	fs.BoolVar(&opts.Verbose, "v", false, "Enable verbose logging (shorthand)")
	fs.BoolVar(&opts.NoColor, "no-color", false, "Disable color and ANSI styling")
	fs.StringVar(&opts.CacheDir, "cache-dir", "", "Custom cache directory")
	fs.BoolVar(&showVersion, "version", false, "Print version information")
	fs.BoolVar(&showVersion, "V", false, "Print version information (shorthand)")
	fs.BoolVar(&showHelp, "help", false, "Show help information")
	fs.BoolVar(&showHelp, "h", false, "Show help information (shorthand)")

	fs.Usage = func() {
		printGlobalHelp(stdout)
	}

	var globalArgs []string
	var cmdName string
	var cmdArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			globalArgs = append(globalArgs, arg)
			if (arg == "--config" || arg == "-config" || arg == "--cache-dir" || arg == "-cache-dir") && i+1 < len(args) {
				i++
				globalArgs = append(globalArgs, args[i])
			}
		} else {
			cmdName = arg
			cmdArgs = args[i+1:]
			break
		}
	}

	if err := fs.Parse(globalArgs); err != nil {
		return ExitUsage
	}

	if showVersion || cmdName == "version" {
		fmt.Fprintf(stdout, "kungfu version %s\n", version.Get())
		return ExitSuccess
	}

	if showHelp || (cmdName == "" && len(globalArgs) == 0) || cmdName == "help" {
		printGlobalHelp(stdout)
		if cmdName == "" && len(globalArgs) == 0 {
			return ExitUsage
		}
		return ExitSuccess
	}

	if jsonFlag {
		opts.Mode = OutputModeJSON
	} else if rawFlag {
		opts.Mode = OutputModeRaw
	} else {
		opts.Mode = OutputModeHuman
	}

	app, err := NewApp(opts, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return mapErrorToExitCode(err)
	}

	switch cmdName {
	case "list", "ls":
		return app.RunList(cmdArgs)
	case "find", "search":
		return app.RunFind(cmdArgs)
	case "show", "info":
		return app.RunShow(cmdArgs)
	case "load", "get":
		return app.RunLoad(cmdArgs)
	case "learn", "install", "add":
		return app.RunLearn(cmdArgs)
	case "forget", "unlearn":
		return app.RunForget(cmdArgs)
	case "update", "upgrade":
		return app.RunUpdate(cmdArgs)
	case "status":
		return app.RunStatus(cmdArgs)
	case "run", "exec":
		return app.RunRun(cmdArgs)
	default:
		validCommands := []string{
			"list", "find", "show", "load", "learn", "forget", "update", "status", "run", "version", "help",
		}
		fmt.Fprintf(stderr, "error: unknown command %q.\n", cmdName)
		suggs := search.Suggest(cmdName, validCommands, 2)
		if len(suggs) > 0 {
			fmt.Fprintln(stderr, "\nDid you mean:")
			for _, s := range suggs {
				fmt.Fprintf(stderr, "  - kungfu %s\n", s)
			}
		}
		fmt.Fprintln(stderr, "\nRun 'kungfu --help' for available commands and usage.")
		return ExitUsage
	}
}

func printGlobalHelp(w io.Writer) {
	fmt.Fprintln(w, `kungfu - Agent Skills Gateway

USAGE:
  kungfu [global flags] <command> [flags] [arguments...]

COMMANDS:
  list    [--category <cat>] [--tags <expr>] [--all] [--json] [--raw]
          List available skill categories and items.
  find    <keywords...> [--category <cat>] [--tags <expr>] [--strategy hybrid|bm25|tfidf|exact] [--limit <n>] [--threshold <t>] [--json] [--raw]
          Search skills with pure Go BM25/TF-IDF/Hybrid ranking.
  show    <skill1> [skill2...] [--category <cat>] [--tags <expr>] [--all] [--json] [--raw]
          Display metadata, description, tags, and summary for one or more skills.
  load    <skill1[/subpath]> [skill2...] [--category <cat>] [--tags <expr>] [--all] [--json] [--raw]
          Load verbatim markdown instructions or subpath files (references, scripts, assets) for JIT execution.
  learn   <skill1> [skill2...] [--global] [--force] [--load] [--category <cat>] [--tags <expr>] [--all] [--json] [--raw]
          Install and persist skills to canonical workspace or global locations (optionally stream with --load).
  forget  <skill1> [skill2...] [--global] [--force] [--all] [--json] [--raw]
          Uninstall and soft-delete skills from workspace or global locations.
  update  [skill1...] [--global] [--all] [--yes] [--force] [--refresh] [--json] [--raw]
          Check for updates (dry-run without args) or apply updates to installed skills.
  status  [skill1...] [--category <cat>] [--tags <expr>] [--global] [--json] [--raw]
          Show installed skills, workspace paths, versions, forgotten skills, and JIT load history.
  version
          Display kungfu version.
  run     <skill> <script> [args...]
          Execute a script bundled within an agent skill.

GLOBAL FLAGS:
  --json            Emit strict RFC 8259 JSON output
  --raw             Emit verbatim raw markdown/text
  --config <path>   Path to custom configuration file
  --cache-dir <dir> Custom cache directory (default ~/.cache/kungfu)
  --verbose, -v     Verbose output to stderr
  --no-color        Disable colored output
  --version, -V     Show kungfu version
  --help, -h        Show this help message

EXIT CODES:
  0: Success
  1: General error
  2: Usage error / Invalid arguments
  3: Item or category not found
  4: Network error
  5: Integrity error (digest mismatch)`)
}
