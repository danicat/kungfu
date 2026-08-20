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

package compress_test

import (
	"strings"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/compress"
)

func TestCompressMarkdown_ASTInvariants(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		mustHave []string
	}{
		{
			name:  "Fenced code blocks",
			input: "Here is some code:\n```go\nfunc Main() {\n\tfmt.Println(\"test\")\n}\n```\nPlease make sure to run it.",
			mustHave: []string{
				"```go\nfunc Main() {\n\tfmt.Println(\"test\")\n}\n```",
			},
		},
		{
			name:  "Inline commands and flags",
			input: "You can execute \x60kungfu run buffer --dry-run -v\x60 to test your setup.",
			mustHave: []string{
				"\x60kungfu run buffer --dry-run -v\x60",
			},
		},
		{
			name:  "Standalone flags and URLs",
			input: "Please visit https://skills.danicat.dev/catalog.json with --json and -f flags in order to download.",
			mustHave: []string{
				"https://skills.danicat.dev/catalog.json",
				"--json",
				"-f",
			},
		},
		{
			name:  "Parameter signatures and prompt vars",
			input: "The function takes query: string and count: int using ${API_KEY}.",
			mustHave: []string{
				"query: string",
				"count: int",
				"${API_KEY}",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := compress.CompressMarkdown(tc.input)
			for _, item := range tc.mustHave {
				if !strings.Contains(got, item) {
					t.Errorf("missing invariant %q in compressed output:\n%s", item, got)
				}
			}
		})
	}
}

func TestCompressMarkdown_CompressionRatio(t *testing.T) {
	verboseDoc := `
# Comprehensive User Guide for Social Media CLI

Please note that this tool provides complete functionality to manage, draft, schedule,
and publish social media content across connected channels using the buffer CLI.

In this comprehensive guide, you will learn how to get started. You can use this tool whenever you need to
publish posts.

| Command | Description | Options |
| :--- | :--- | :--- |
| kungfu search | Search skills | --category, --limit |
| kungfu run | Execute skill script | --dry-run, --args |

After the installation is complete, you should make sure that you have configured your token in order to authenticate.
`
	compressed := compress.CompressMarkdown(verboseDoc)
	savings := compress.CalculateSavings(verboseDoc, compressed)
	if savings < 0.30 {
		t.Errorf("expected at least 30%% savings, got %.2f%% (original: %d, compressed: %d)", savings*100, len(verboseDoc), len(compressed))
	}
}

func TestCompressItemSummary(t *testing.T) {
	item := catalog.Item{
		ID:          "buffer-tool",
		Name:        "Buffer CLI",
		Type:        catalog.ItemTypeSkill,
		Description: "This tool provides complete functionality to manage social posts.",
		Tags:        []string{"social", "buffer"},
	}
	summary := compress.CompressItemSummary(item, 0.95)
	if !strings.Contains(summary, "[0.95] buffer-tool") {
		t.Errorf("expected score and ID in summary, got: %s", summary)
	}
	if !strings.Contains(summary, "[tags: social, buffer]") {
		t.Errorf("expected tags in summary, got: %s", summary)
	}
}

func TestCompressItemFull(t *testing.T) {
	item := catalog.Item{
		ID:          "godoctor",
		Name:        "GoDoctor Intelligence",
		Type:        catalog.ItemTypeSkill,
		Category:    "engineering",
		Description: "Please note that this skill is used for Go analysis.",
		Tags:        []string{"go", "ast"},
		Source:      "global",
		Version:     "1.0.0",
	}
	full := compress.CompressItemFull(item)
	if !strings.Contains(full, "# GoDoctor Intelligence (godoctor)") {
		t.Errorf("expected header in full view, got: %s", full)
	}
	if !strings.Contains(full, "type: skill") || !strings.Contains(full, "cat: engineering") {
		t.Errorf("expected metadata tags in full view, got: %s", full)
	}
}

func TestCompressCategory(t *testing.T) {
	cat := catalog.Category{
		Name:        "Engineering",
		Description: "Skills designed for software development.",
	}
	items := []catalog.Item{
		{ID: "godoctor", Description: "Go developer tools."},
	}
	res := compress.CompressCategory(cat, items)
	if !strings.Contains(res, "# Engineering:") {
		t.Errorf("expected category header, got: %s", res)
	}
	if !strings.Contains(res, "godoctor") {
		t.Errorf("expected item in category listing, got: %s", res)
	}
}
