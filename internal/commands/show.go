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

package commands

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/danicat/kungfu/internal/catalog"
)

func (a *App) RunShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var jsonFlag bool

	fs.BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	posArgs, err := parseFlagsWithArgs(fs, args)
	if err != nil {
		return ExitUsage
	}

	mode := a.resolveMode(jsonFlag)

	if len(posArgs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "error: show requires at least one skill name")
		return ExitUsage
	}

	var items []catalog.Item
	var notFounds []string

	for _, name := range posArgs {
		item, err := a.resolveSkill(name)
		if err != nil && isGitHubSkillSpec(name) {
			if target, ok := catalog.ParseGitHubTarget(name); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				ghItem, ghErr := catalog.FetchGitHubSkill(ctx, target, a.getHTTPClient())
				cancel()
				if ghErr == nil && ghItem != nil {
					item = ghItem
					err = nil
				} else if ghErr != nil && !errors.Is(ghErr, catalog.ErrItemNotFound) {
					_, _ = fmt.Fprintf(a.Stderr, "error: fetching skill from github %q: %v\n", name, ghErr)
					return MapErrorToExitCode(ghErr)
				}
			}
		}
		if err != nil {
			if !errors.Is(err, catalog.ErrItemNotFound) {
				_, _ = fmt.Fprintf(a.Stderr, "error: resolving skill %q: %v\n", name, err)
				return MapErrorToExitCode(err)
			}
			notFounds = append(notFounds, name)
			continue
		}
		items = append(items, *item)
	}

	if len(notFounds) > 0 {
		for _, name := range notFounds {
			a.printNotFoundWithSuggestions("show", name)
		}
		return ExitNotFound
	}

	return a.renderShowItems(items, mode)
}

func (a *App) renderShowItems(items []catalog.Item, mode OutputMode) int {
	type ShowDetail struct {
		Name         string            `json:"name"`
		Title        string            `json:"title,omitempty"`
		Version      string            `json:"version,omitempty"`
		Description  string            `json:"description"`
		Author       string            `json:"author,omitempty"`
		License      string            `json:"license,omitempty"`
		Status       string            `json:"status"`
		Tags         []string          `json:"tags,omitempty"`
		AllowedTools string            `json:"allowed_tools,omitempty"`
		References   []string          `json:"references,omitempty"`
		Scripts      []string          `json:"scripts,omitempty"`
		Digest       string            `json:"digest,omitempty"`
		URL          string            `json:"url,omitempty"`
		Source       string            `json:"source,omitempty"`
		Metadata     map[string]string `json:"metadata,omitempty"`
	}

	details := make([]ShowDetail, 0, len(items))
	lookup := a.newSkillStatusLookup(a.loadStateReadOnly())
	for _, it := range items {
		a.ensureArchiveUnpacked(&it)
		details = append(details, ShowDetail{
			Name:         it.Name,
			Title:        it.Title,
			Version:      itemVersion(it),
			Description:  it.Description,
			Author:       itemAuthor(it),
			License:      it.License,
			Status:       lookup.status(&it),
			Tags:         it.Tags,
			AllowedTools: it.AllowedTools,
			References:   a.collectReferences(it, true),
			Scripts:      a.listScripts(it),
			Digest:       it.Digest,
			URL:          it.URL,
			Source:       it.Source,
			Metadata:     it.Metadata,
		})
	}

	if mode == OutputModeJSON {
		if err := writeJSON(a.Stdout, details); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "error: writing JSON output: %v\n", err)
			return ExitGeneral
		}
		return ExitSuccess
	}

	for i, d := range details {
		if i > 0 {
			_, _ = fmt.Fprintln(a.Stdout)
			_, _ = fmt.Fprintln(a.Stdout, "--------------------------------------------------------------------------------")
			_, _ = fmt.Fprintln(a.Stdout)
		}
		_, _ = fmt.Fprintf(a.Stdout, "SKILL: %s\n", d.Name)
		if d.Title != "" && d.Title != d.Name {
			_, _ = fmt.Fprintf(a.Stdout, "Title:        %s\n", d.Title)
		}
		if d.Version != "" {
			_, _ = fmt.Fprintf(a.Stdout, "Version:      %s\n", d.Version)
		}
		_, _ = fmt.Fprintf(a.Stdout, "Status:       [%s]\n", d.Status)
		if d.Author != "" {
			_, _ = fmt.Fprintf(a.Stdout, "Author:       %s\n", d.Author)
		}
		if d.License != "" {
			_, _ = fmt.Fprintf(a.Stdout, "License:      %s\n", d.License)
		}
		if len(d.Tags) > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "Tags:         %s\n", strings.Join(d.Tags, ", "))
		}
		if d.AllowedTools != "" {
			_, _ = fmt.Fprintf(a.Stdout, "Tools:        %s\n", d.AllowedTools)
		}
		if len(d.References) > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "References:   %s\n", strings.Join(d.References, ", "))
		}
		if len(d.Scripts) > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "Scripts:      %s\n", strings.Join(d.Scripts, ", "))
		}
		if d.URL != "" {
			_, _ = fmt.Fprintf(a.Stdout, "URL:          %s\n", d.URL)
		}
		if d.Digest != "" {
			_, _ = fmt.Fprintf(a.Stdout, "Digest:       %s\n", d.Digest)
		}
		_, _ = fmt.Fprintf(a.Stdout, "Description:\n  %s\n", d.Description)
	}
	return ExitSuccess
}
