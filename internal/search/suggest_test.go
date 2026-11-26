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

package search

import (
	"reflect"
	"strings"
	"testing"
)

func TestLevenshtein(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "abc", 3},
		{"godoctor", "godoctor", 0},
		{"godoc", "godoctor", 3},
		{"godoctr", "godoctor", 1},
		{"ebitengineer", "ebiten", 6},
		{"kungfu", "kungfuu", 1},
		{"KUNGFU", "kungfu", 0},
		{"café", "cafe", 1},
		{"über", "uber", 1},
		{"こんにちは", "こんには", 1},
	}

	for _, tt := range tests {
		got := levenshteinNormalized(strings.ToLower(strings.TrimSpace(tt.a)), strings.ToLower(strings.TrimSpace(tt.b)))
		if got != tt.want {
			t.Errorf("levenshteinNormalized(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSuggest(t *testing.T) {
	t.Parallel()

	candidates := []string{
		"godoctor",
		"ebitengineer",
		"deslopify",
		"buffer",
		"google-analytics",
		"kungfu",
	}

	t.Run("prefix match godoc -> godoctor", func(t *testing.T) {
		got := Suggest("godoc", candidates, 3)
		want := []string{"godoctor"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Suggest(godoc) = %v, want %v", got, want)
		}
	})

	t.Run("typo match godoctr -> godoctor", func(t *testing.T) {
		got := Suggest("godoctr", candidates, 3)
		want := []string{"godoctor"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Suggest(godoctr) = %v, want %v", got, want)
		}
	})

	t.Run("prefix match ebiten -> ebitengineer", func(t *testing.T) {
		got := Suggest("ebiten", candidates, 3)
		want := []string{"ebitengineer"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Suggest(ebiten) = %v, want %v", got, want)
		}
	})

	t.Run("typo match kungfuu -> kungfu", func(t *testing.T) {
		got := Suggest("kungfuu", candidates, 3)
		want := []string{"kungfu"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Suggest(kungfuu) = %v, want %v", got, want)
		}
	})

	t.Run("prefix strictly beats substring even with >100 char difference", func(t *testing.T) {
		longPrefix := "alpha-" + strings.Repeat("x", 120)
		got := Suggest("alpha", []string{"my-alpha", longPrefix}, 2)
		want := []string{longPrefix, "my-alpha"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Suggest(alpha) = %v, want %v", got, want)
		}
	})

	t.Run("no matches for completely random query", func(t *testing.T) {
		got := Suggest("xyz999qwerty", candidates, 3)
		if len(got) != 0 {
			t.Errorf("expected no suggestions, got %v", got)
		}
	})
}
