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

package catalog

import (
	"testing"
)

func TestParseTags(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"go", []string{"go"}},
		{"go, golang, testing", []string{"go", "golang", "testing"}},
		{"go; python\nrust", []string{"go", "python", "rust"}},
		{"GO, go, Go", []string{"go"}},
	}

	for _, tt := range tests {
		got := ParseTags(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("ParseTags(%q) length = %d, want %d (%v vs %v)", tt.input, len(got), len(tt.want), got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseTags(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestMatchTags(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		query    string
		expected bool
	}{
		{"empty query matches all", []string{"go", "web"}, "", true},
		{"single tag match", []string{"go", "testing"}, "go", true},
		{"single tag mismatch", []string{"python", "django"}, "go", false},
		{"implicit AND match", []string{"go", "ebiten", "game"}, "ebiten game", true},
		{"implicit AND mismatch", []string{"go", "game"}, "ebiten game", false},
		{"explicit AND match", []string{"gamedev", "ebiten", "2d"}, "gamedev AND ebiten", true},
		{"explicit AND mismatch", []string{"gamedev", "unity"}, "gamedev AND ebiten", false},
		{"plus sign AND match", []string{"gamedev", "ebiten"}, "gamedev+ebiten", true},
		{"comma OR match left", []string{"go", "testing"}, "go, python", true},
		{"comma OR match right", []string{"python", "testing"}, "go, python", true},
		{"comma OR mismatch", []string{"rust", "testing"}, "go, python", false},
		{"explicit OR match", []string{"golang", "ast"}, "golang OR typescript", true},
		{"explicit NOT match", []string{"gamedev", "2d"}, "gamedev NOT 3d", true},
		{"explicit NOT mismatch", []string{"gamedev", "3d"}, "gamedev NOT 3d", false},
		{"minus sign NOT match", []string{"gamedev", "2d"}, "gamedev -3d", true},
		{"parenthesized expression", []string{"go", "game"}, "(go OR rust) AND (game OR web)", true},
		{"parenthesized expression fail", []string{"go", "cli"}, "(go OR rust) AND (game OR web)", false},
		{"case insensitivity", []string{"GOLANG", "Testing"}, "golang AND testing", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchTags(tt.tags, tt.query)
			if got != tt.expected {
				t.Errorf("MatchTags(%v, %q) = %v, want %v", tt.tags, tt.query, got, tt.expected)
			}
		})
	}
}
