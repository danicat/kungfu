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
	"io"
	"os"

	"github.com/danicat/kungfu/internal/commands"
)

// Exit codes
const (
	ExitSuccess   = commands.ExitSuccess
	ExitGeneral   = commands.ExitGeneral
	ExitUsage     = commands.ExitUsage
	ExitNotFound  = commands.ExitNotFound
	ExitNetwork   = commands.ExitNetwork
	ExitIntegrity = commands.ExitIntegrity
)

//go:embed all:skills
var embeddedSkills embed.FS

func main() {
	code := Run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// Run executes the command line entrypoint for kungfu CLI.
func Run(args []string, stdout, stderr io.Writer) int {
	return commands.Run(args, stdout, stderr, embeddedSkills)
}
