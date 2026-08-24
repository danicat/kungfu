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

package search_test

import (
	"math"
	"sync"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/search"
)

func TestTokenizer_Basic(t *testing.T) {
	tok := search.NewTokenizer()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple ascii",
			input:    "Quick Brown Fox",
			expected: []string{"quick", "brown", "fox"},
		},
		{
			name:     "with stopwords",
			input:    "the quick brown fox jumps over the lazy dog",
			expected: []string{"quick", "brown", "fox", "jump", "lazi", "dog"},
		},
		{
			name:     "kebab and snake case",
			input:    "game-engine astro_loader pure.go",
			expected: []string{"game", "engin", "astro", "loader", "pure", "go"},
		},
		{
			name:     "camel case and acronyms",
			input:    "EbitEngine ASTParser JSONDecoder",
			expected: []string{"ebit", "engin", "ast", "parser", "json", "decod"},
		},
		{
			name:     "utf8 characters",
			input:    "Café über standard",
			expected: []string{"café", "über", "standard"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tok.Tokenize(tc.input)
			if len(got) != len(tc.expected) {
				t.Fatalf("Tokenize(%q) len = %d, expected %d: %v", tc.input, len(got), len(tc.expected), got)
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Errorf("Tokenize(%q)[%d] = %q, expected %q", tc.input, i, got[i], tc.expected[i])
				}
			}
		})
	}
}

func TestTokenizer_Stemmer(t *testing.T) {
	tok := search.NewTokenizer()

	stemTests := map[string]string{
		"caresses":  "caress",
		"ponies":    "poni",
		"ties":      "ti",
		"cats":      "cat",
		"feed":      "feed",
		"agreed":    "agre",
		"plastered": "plaster",
		"sing":      "sing",
		"motoring":  "motor",
		"physics":   "physic",
		"running":   "run",
	}

	for word, expected := range stemTests {
		got := tok.Stem(word)
		if got != expected {
			t.Errorf("Stem(%q) = %q, expected %q", word, got, expected)
		}
	}
}

func TestBM25_IDF(t *testing.T) {
	// RSJ non-negative IDF test: ln(1 + (N - n + 0.5) / (n + 0.5))
	N := 100
	for df := 0; df <= N; df++ {
		idf := search.ComputeIDF(df, N)
		if idf < 0 {
			t.Errorf("IDF for df=%d, N=%d was negative: %f", df, N, idf)
		}
		if math.IsNaN(float64(idf)) || math.IsInf(float64(idf), 0) {
			t.Errorf("IDF for df=%d, N=%d returned NaN/Inf: %f", df, N, idf)
		}
	}
}

func TestTFIDF_Sublinear(t *testing.T) {
	tf0 := search.SublinearTF(0)
	if tf0 != 0 {
		t.Errorf("SublinearTF(0) = %f, expected 0", tf0)
	}

	tf1 := search.SublinearTF(1)
	if tf1 != 1.0 {
		t.Errorf("SublinearTF(1) = %f, expected 1.0", tf1)
	}

	tf10 := search.SublinearTF(10)
	if tf10 <= tf1 {
		t.Errorf("SublinearTF(10) (%f) should be > SublinearTF(1) (%f)", tf10, tf1)
	}
}

var sampleCatalogItems = []catalog.Item{
	{
		ID:          "ebitengineer",
		Name:        "ebitengineer",
		Type:        catalog.ItemTypeSkill,
		Title:       "2D Game Engine for Go",
		Category:    "game-dev",
		Tags:        []string{"ebitengine", "game", "2d", "graphics"},
		Description: "Master orchestrator for building 2D games with Ebitengine in pure Go",
	},
	{
		ID:          "astro-loader",
		Name:        "astro-loader",
		Type:        catalog.ItemTypeSkill,
		Title:       "Astro Content Loader",
		Category:    "web",
		Tags:        []string{"astro", "ssg", "ssr", "content"},
		Description: "Content loader and CMS connector for Astro 5 static and dynamic sites",
	},
	{
		ID:          "godoctor",
		Name:        "godoctor",
		Type:        catalog.ItemTypeSkill,
		Title:       "Go Quality & AST Tooling",
		Category:    "tooling",
		Tags:        []string{"go", "ast", "quality", "testing"},
		Description: "AST-aware Go code intelligence, test analytics, and quality enforcement",
	},
	{
		ID:          "physics-2d",
		Name:        "physics-2d",
		Type:        catalog.ItemTypeSkill,
		Title:       "2D Rigid Body Physics Engine",
		Category:    "game-dev",
		Tags:        []string{"physics", "simulation", "math", "game"},
		Description: "Impulse resolution, verlet integration, and broadphase collision detection",
	},
	{
		ID:          "buffer-cli",
		Name:        "buffer-cli",
		Type:        catalog.ItemTypeSkill,
		Title:       "Buffer Social Media CLI",
		Category:    "marketing",
		Tags:        []string{"buffer", "social", "publishing", "analytics"},
		Description: "Manage and schedule social media campaigns across LinkedIn and Twitter",
	},
}

func TestEngine_IndexAndSearch(t *testing.T) {
	eng := search.NewEngine()
	err := eng.Index(sampleCatalogItems)
	if err != nil {
		t.Fatalf("Index failed: %v", err)
	}

	tests := []struct {
		name        string
		query       string
		strategy    string
		limit       int
		expectFirst string
		minResults  int
	}{
		{
			name:        "hybrid search game engine",
			query:       "ebitengine 2d game",
			strategy:    "hybrid",
			limit:       5,
			expectFirst: "ebitengineer",
			minResults:  2,
		},
		{
			name:        "bm25 search astro web",
			query:       "astro content loader",
			strategy:    "bm25",
			limit:       3,
			expectFirst: "astro-loader",
			minResults:  1,
		},
		{
			name:        "tfidf search ast quality",
			query:       "ast quality testing",
			strategy:    "tfidf",
			limit:       3,
			expectFirst: "godoctor",
			minResults:  1,
		},
		{
			name:        "exact match skill name",
			query:       "buffer-cli",
			strategy:    "exact",
			limit:       1,
			expectFirst: "buffer-cli",
			minResults:  1,
		},
		{
			name:       "category filtering directive",
			query:      "category:game-dev",
			strategy:   "hybrid",
			limit:      5,
			minResults: 2,
		},
		{
			name:        "tag filtering with keyword",
			query:       "tag:physics simulation",
			strategy:    "hybrid",
			limit:       5,
			expectFirst: "physics-2d",
			minResults:  1,
		},
		{
			name:        "delimiter normalization space to hyphen (buffer cli -> buffer-cli)",
			query:       "buffer cli",
			strategy:    "hybrid",
			limit:       3,
			expectFirst: "buffer-cli",
			minResults:  1,
		},
		{
			name:        "compound name without separator (ebitengineer -> ebit engineer)",
			query:       "ebit engineer",
			strategy:    "hybrid",
			limit:       3,
			expectFirst: "ebitengineer",
			minResults:  1,
		},
		{
			name:        "edit distance 1 fuzzy match (astro loader typo -> astro-loader)",
			query:       "astro loadr",
			strategy:    "hybrid",
			limit:       3,
			expectFirst: "astro-loader",
			minResults:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results := eng.Search(tc.query, tc.strategy, tc.limit, 0.0)
			if len(results) < tc.minResults {
				t.Fatalf("Search(%q, %q) returned %d results, expected at least %d", tc.query, tc.strategy, len(results), tc.minResults)
			}
			if tc.expectFirst != "" && len(results) > 0 {
				if results[0].Item.ID != tc.expectFirst && results[0].Item.Name != tc.expectFirst {
					t.Errorf("Search(%q) top result = %s, expected %s", tc.query, results[0].Item.ID, tc.expectFirst)
				}
			}
			for _, r := range results {
				if len(r.MatchFields) == 0 && tc.strategy != "" {
					t.Errorf("Expected MatchFields to be populated for %s", r.Item.ID)
				}
			}
		})
	}
}

func TestEngine_FuzzyAndDelimiterScores(t *testing.T) {
	eng := search.NewEngine()
	_ = eng.Index(sampleCatalogItems)

	// "buffer cli" vs "buffer-cli" should have score >= 0.99
	resExact := eng.Search("buffer cli", "hybrid", 1, 0.0)
	if len(resExact) == 0 || resExact[0].Item.Name != "buffer-cli" {
		t.Fatalf("Expected buffer-cli top match, got %v", resExact)
	}
	if resExact[0].Score < 0.95 {
		t.Errorf("Expected score >= 0.95 for exact normalized match, got %f", resExact[0].Score)
	}

	// "buffer clii" (1 edit distance typo) should have score >= 0.90
	resFuzzy1 := eng.Search("buffer clii", "hybrid", 1, 0.0)
	if len(resFuzzy1) == 0 || resFuzzy1[0].Item.Name != "buffer-cli" {
		t.Fatalf("Expected buffer-cli top match, got %v", resFuzzy1)
	}
	if resFuzzy1[0].Score < 0.90 {
		t.Errorf("Expected score >= 0.90 for 1-edit distance match, got %f", resFuzzy1[0].Score)
	}
}

func TestEngine_EmptyAndThreshold(t *testing.T) {
	eng := search.NewEngine()
	_ = eng.Index(sampleCatalogItems)

	// Non-existent word
	res := eng.Search("nonexistentxyz12345", "hybrid", 5, 0.0)
	if len(res) != 0 {
		t.Errorf("Expected 0 results for non-existent word, got %d", len(res))
	}

	// Unindexed engine
	emptyEng := search.NewEngine()
	resEmpty := emptyEng.Search("game", "hybrid", 5, 0.0)
	if len(resEmpty) != 0 {
		t.Errorf("Expected 0 results on unindexed engine, got %d", len(resEmpty))
	}
}

func TestEngine_ConcurrentAccess(t *testing.T) {
	eng := search.NewEngine()
	_ = eng.Index(sampleCatalogItems)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(iter int) {
			defer wg.Done()
			queries := []string{"game", "astro", "godoctor", "physics", "category:game-dev"}
			q := queries[iter%len(queries)]
			results := eng.Search(q, "hybrid", 3, 0.0)
			if len(results) == 0 {
				t.Errorf("Concurrent search for %q returned 0 results", q)
			}
		}(i)
	}
	wg.Wait()
}

func BenchmarkTokenizer(b *testing.B) {
	tok := search.NewTokenizer()
	text := "Master orchestrator for building 2D games with Ebitengine in pure Go and WebAssembly"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tok.Tokenize(text)
	}
}

func BenchmarkSearch_Hybrid(b *testing.B) {
	eng := search.NewEngine()
	_ = eng.Index(sampleCatalogItems)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = eng.Search("2D game physics", "hybrid", 5, 0.0)
	}
}
