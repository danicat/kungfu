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

package compress

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danicat/kungfu/internal/catalog"
)

var (
	fencedCodeRegex = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\n.*?```|```.*?```")
	inlineCodeRegex = regexp.MustCompile("\x60[^\x60\n]+\x60")
	urlRegex        = regexp.MustCompile("https?://[^\\s)\\]>\x22\x27\x60]+")
	flagRegex       = regexp.MustCompile("(?:^|\\s)(--[a-zA-Z0-9_-]+|-[a-zA-Z0-9])\\b")
	paramTypeRegex  = regexp.MustCompile("\\b[a-zA-Z0-9_]+\\s*:\\s*(?:string|str|int|integer|float|bool|boolean|any|map|slice|array|\\[\\][a-zA-Z0-9_]+)\\b")
	promptVarRegex  = regexp.MustCompile("\\$\\{?[A-Z0-9_]+\\}?")
	articlesRegex   = regexp.MustCompile("(?i)\\b(a|an|the)\\b\\s*")
	copulasRegex    = regexp.MustCompile("(?i)\\b(is|are|was|were)\\b\\s*")
	multiNewlines   = regexp.MustCompile("\\n{3,}")
	multiSpaces     = regexp.MustCompile("[ \\t]+")
	spacePunct      = regexp.MustCompile("\\s+([,.:;?!])")
	multiColon      = regexp.MustCompile(":{2,}")
	tableSepRegex   = regexp.MustCompile("^\\s*\\|?\\s*[-:]+[-| :]*\\|\\s*$")
)

type replacement struct {
	pattern *regexp.Regexp
	repl    string
}

var fillerReplacements = []replacement{
	{pattern: regexp.MustCompile("(?i)\\bplease\\s+(?:note\\s+that|note|be\\s+aware\\s+that|make\\s+sure\\s+to|ensure\\s+that)\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\b(?:note\\s+that|keep\\s+in\\s+mind\\s+that|remember\\s+to|be\\s+aware\\s+that)\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\bmake\\s+sure\\s+(?:to|that)\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\byou\\s+can\\s+(?:use|activate|run|execute)\\s+this\\s+(?:tool|skill|command|function)\\s+(?:whenever\\s+you\\s+need\\s+to|whenever\\s+you\\s+want\\s+to|to|for)?\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\byou\\s+can\\s+(?:use|run|find|see)\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\byou\\s+(?:should|must|will\\s+need\\s+to|need\\s+to)\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\b(?:this\\s+tool|this\\s+skill)\\s+(?:provides\\s+complete\\s+functionality\\s+to|provides\\s+functionality\\s+to|provides\\s+tools\\s+to|provides|allows\\s+you\\s+to|allows\\s+the\\s+user\\s+to|is\\s+used\\s+to|is\\s+designed\\s+to)\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\bthe\\s+following\\s+skills\\s+are\\s+installed\\s+and\\s+available\\s+in\\s+the\\s+system\\.?\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\byou\\s+can\\s+activate\\s+any\\s+skill\\s+when\\s+its\\s+domain\\s+matches\\s+the\\s+user\\x27s\\s+requirements\\.?\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\bin\\s+this\\s+comprehensive\\s+guide,?\\s*(?:you\\s+will\\s+learn\\s+how\\s+to)?\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\bto\\s+get\\s+started,?\\s*(?:you\\s+must\\s+first)?\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\bafter\\s+the\\s+installation\\s+is\\s+complete,?\\b"), repl: ""},
	{pattern: regexp.MustCompile("(?i)\\bin\\s+order\\s+to\\b"), repl: "to"},
	{pattern: regexp.MustCompile("(?i)\\bwith\\s+regards?\\s+to\\b"), repl: "re:"},
	{pattern: regexp.MustCompile("(?i)\\bwith\\s+respect\\s+to\\b"), repl: "re:"},
	{pattern: regexp.MustCompile("(?i)\\bas\\s+well\\s+as\\b"), repl: "&"},
	{pattern: regexp.MustCompile("(?i)\\bis\\s+used\\s+for\\b"), repl: ":"},
	{pattern: regexp.MustCompile("(?i)\\bcan\\s+be\\s+used\\s+to\\b"), repl: "to"},
	{pattern: regexp.MustCompile("(?i)\\bleads\\s+to\\b"), repl: "->"},
	{pattern: regexp.MustCompile("(?i)\\bthen\\s+execute\\b"), repl: "->"},
}

// Compress compresses general markdown text into high-density telegraphic cave-speak.
func Compress(text string) string {
	return CompressMarkdown(text)
}

// CompressMarkdown compresses markdown text while strictly preserving AST golden invariants.
func CompressMarkdown(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}

	var invariants []string
	placeholderFormat := "\x00INV%d\x00"

	protect := func(match string) string {
		idx := len(invariants)
		invariants = append(invariants, match)
		return fmt.Sprintf(placeholderFormat, idx)
	}

	text = fencedCodeRegex.ReplaceAllStringFunc(text, protect)
	text = inlineCodeRegex.ReplaceAllStringFunc(text, protect)
	text = urlRegex.ReplaceAllStringFunc(text, protect)
	text = flagRegex.ReplaceAllStringFunc(text, func(m string) string {
		trimmed := strings.TrimSpace(m)
		idx := len(invariants)
		invariants = append(invariants, trimmed)
		prefix := ""
		if strings.HasPrefix(m, " ") {
			prefix = " "
		}
		return prefix + fmt.Sprintf(placeholderFormat, idx)
	})
	text = paramTypeRegex.ReplaceAllStringFunc(text, protect)
	text = promptVarRegex.ReplaceAllStringFunc(text, protect)

	text = compactMarkdownTables(text)

	for _, repl := range fillerReplacements {
		text = repl.pattern.ReplaceAllString(text, repl.repl)
	}

	text = articlesRegex.ReplaceAllString(text, "")
	text = copulasRegex.ReplaceAllString(text, "")
	text = cleanFormatting(text)

	for i, inv := range invariants {
		holder := fmt.Sprintf(placeholderFormat, i)
		text = strings.ReplaceAll(text, holder, inv)
	}

	return strings.TrimSpace(text)
}

func compactMarkdownTables(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	var inTable bool
	var tableLines []string

	flushTable := func() {
		if len(tableLines) == 0 {
			return
		}
		compacted := processTable(tableLines)
		result = append(result, compacted...)
		tableLines = nil
		inTable = false
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") || (strings.Contains(trimmed, "|") && !strings.HasPrefix(trimmed, "```")) {
			inTable = true
			tableLines = append(tableLines, trimmed)
		} else {
			if inTable {
				flushTable()
			}
			result = append(result, line)
		}
	}
	if inTable {
		flushTable()
	}

	return strings.Join(result, "\n")
}

func processTable(lines []string) []string {
	var rows [][]string
	for _, l := range lines {
		if tableSepRegex.MatchString(l) {
			continue
		}
		cells := strings.Split(l, "|")
		var cleanCells []string
		for _, c := range cells {
			cTrim := strings.TrimSpace(c)
			if cTrim != "" {
				cleanCells = append(cleanCells, cTrim)
			}
		}
		if len(cleanCells) > 0 {
			rows = append(rows, cleanCells)
		}
	}

	if len(rows) == 0 {
		return nil
	}

	var out []string
	if len(rows) == 1 {
		out = append(out, strings.Join(rows[0], " | "))
		return out
	}

	for i := 1; i < len(rows); i++ {
		row := rows[i]
		if len(row) == 1 {
			out = append(out, fmt.Sprintf("- %s", row[0]))
		} else if len(row) == 2 {
			out = append(out, fmt.Sprintf("- %s: %s", row[0], row[1]))
		} else {
			mainPart := fmt.Sprintf("- %s: %s", row[0], row[1])
			extra := strings.Join(row[2:], " | ")
			out = append(out, fmt.Sprintf("%s (%s)", mainPart, extra))
		}
	}
	return out
}

func cleanFormatting(text string) string {
	lines := strings.Split(text, "\n")
	var cleanedLines []string
	for _, line := range lines {
		line = multiSpaces.ReplaceAllString(line, " ")
		line = spacePunct.ReplaceAllString(line, "$1")
		line = multiColon.ReplaceAllString(line, ":")
		trimmed := strings.TrimSpace(line)
		if trimmed == "-" || trimmed == "*" || trimmed == ":" || trimmed == "." {
			continue
		}
		cleanedLines = append(cleanedLines, strings.TrimRight(line, " \t"))
	}

	joined := strings.Join(cleanedLines, "\n")
	joined = multiNewlines.ReplaceAllString(joined, "\n\n")
	return joined
}

// CompressItemSummary formats an item into a high-density single line summary.
func CompressItemSummary(item catalog.Item, score float32) string {
	var sb strings.Builder
	id := item.ID
	if id == "" {
		id = item.Name
	}

	if score > 0 {
		sb.WriteString(fmt.Sprintf("- [%.2f] %s", score, id))
	} else {
		sb.WriteString(fmt.Sprintf("- %s", id))
	}

	if item.Type != "" && item.Type != catalog.ItemTypeSkill {
		sb.WriteString(fmt.Sprintf(" (%s)", item.Type))
	}

	desc := CompressMarkdown(item.Description)
	if desc != "" {
		sb.WriteString(fmt.Sprintf(": %s", desc))
	}

	if len(item.Tags) > 0 {
		sb.WriteString(fmt.Sprintf(" [tags: %s]", strings.Join(item.Tags, ", ")))
	}

	return sb.String()
}

// CompressItemFull formats an item into a high-density full detail view.
func CompressItemFull(item catalog.Item) string {
	var sb strings.Builder
	name := item.Name
	if name == "" {
		name = item.ID
	}
	id := item.ID
	if id == "" {
		id = item.Name
	}

	sb.WriteString(fmt.Sprintf("# %s (%s)", name, id))

	var meta []string
	if item.Type != "" {
		meta = append(meta, fmt.Sprintf("type: %s", item.Type))
	}
	if item.Category != "" {
		meta = append(meta, fmt.Sprintf("cat: %s", item.Category))
	}
	if item.Source != "" {
		meta = append(meta, fmt.Sprintf("src: %s", item.Source))
	}
	if item.Version != "" {
		meta = append(meta, fmt.Sprintf("v: %s", item.Version))
	}
	if item.Author != "" {
		meta = append(meta, fmt.Sprintf("author: %s", item.Author))
	}
	if item.URL != "" {
		meta = append(meta, fmt.Sprintf("url: %s", item.URL))
	}

	if len(meta) > 0 {
		sb.WriteString(fmt.Sprintf(" [%s]\n", strings.Join(meta, " | ")))
	} else {
		sb.WriteString("\n")
	}

	if len(item.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("- Tags: %s\n", strings.Join(item.Tags, ", ")))
	}
	if item.Description != "" {
		sb.WriteString(fmt.Sprintf("- Desc: %s\n", CompressMarkdown(item.Description)))
	}
	if item.Path != "" {
		sb.WriteString(fmt.Sprintf("- Path: %s\n", item.Path))
	}
	if len(item.Metadata) > 0 {
		var mEntries []string
		for k, v := range item.Metadata {
			mEntries = append(mEntries, fmt.Sprintf("%s: %v", k, v))
		}
		sb.WriteString(fmt.Sprintf("- Meta: %s\n", strings.Join(mEntries, " | ")))
	}

	return strings.TrimSpace(sb.String())
}

// CompressCategory formats a category and its items into high-density listing.
func CompressCategory(cat catalog.Category, items []catalog.Item) string {
	var sb strings.Builder
	name := cat.Name
	desc := CompressMarkdown(cat.Description)
	if desc != "" {
		sb.WriteString(fmt.Sprintf("# %s: %s\n", name, desc))
	} else {
		sb.WriteString(fmt.Sprintf("# %s\n", name))
	}

	for _, it := range items {
		sb.WriteString(CompressItemSummary(it, 0) + "\n")
	}

	return strings.TrimSpace(sb.String())
}

// CalculateSavings returns the character/token savings ratio between original and compressed text.
func CalculateSavings(original, compressed string) float64 {
	if len(original) == 0 {
		return 0
	}
	origLen := float64(len(original))
	compLen := float64(len(compressed))
	return (origLen - compLen) / origLen
}
