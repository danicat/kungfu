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

package search_test

import (
	"math"
	"sync"
	"testing"

	"github.com/danicat/kungfu/internal/catalog"
	"github.com/danicat/kungfu/internal/search"
)

func TestTokenizer_Basic(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestBM25_IDFAndParameterBounds(t *testing.T) {
	t.Parallel()

	if idf := search.ComputeIDF(0, 0); idf != 0 {
		t.Errorf("ComputeIDF(0, 0) = %f, want 0", idf)
	}

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

	// Invalid NaN/Inf/out-of-range parameters must be safely normalized.
	b := search.NewBM25Scorer(float32(math.NaN()), float32(math.Inf(1)))
	b.Build(1, 2.0, []float32{2.0}, map[uint32]uint32{1: 1})
	score := b.ScoreTerm(1, 1.0, 0)
	if math.IsNaN(float64(score)) || math.IsInf(float64(score), 0) || score <= 0 {
		t.Errorf("expected valid positive BM25 score after clamping invalid params, got %f", score)
	}
}

var sampleCatalogItems = []catalog.Item{
	{
		ID:          "ebitengineer",
		Name:        "ebitengineer",
		Type:        catalog.ItemTypeSkill,
		Title:       "2D Game Engine for Go",
		Tags:        []string{"ebitengine", "game", "2d", "graphics"},
		Description: "Master orchestrator for building 2D games with Ebitengine in pure Go",
	},
	{
		ID:          "astro-loader",
		Name:        "astro-loader",
		Type:        catalog.ItemTypeSkill,
		Title:       "Astro Content Loader",
		Tags:        []string{"astro", "ssg", "ssr", "content"},
		Description: "Content loader and CMS connector for Astro 5 static and dynamic sites",
	},
	{
		ID:          "godoctor",
		Name:        "godoctor",
		Type:        catalog.ItemTypeSkill,
		Title:       "Go Quality & AST Tooling",
		Tags:        []string{"go", "ast", "quality", "testing"},
		Description: "AST-aware Go code intelligence, test analytics, and quality enforcement",
	},
	{
		ID:          "physics-2d",
		Name:        "physics-2d",
		Type:        catalog.ItemTypeSkill,
		Title:       "2D Rigid Body Physics Engine",
		Tags:        []string{"physics", "simulation", "math", "game"},
		Description: "Impulse resolution, verlet integration, and broadphase collision detection",
	},
	{
		ID:          "buffer-cli",
		Name:        "buffer-cli",
		Type:        catalog.ItemTypeSkill,
		Title:       "Buffer Social Media CLI",
		Tags:        []string{"buffer", "social", "publishing", "analytics"},
		Description: "Manage and schedule social media campaigns across LinkedIn and Twitter",
	},
}

func TestEngine_IndexAndSearch(t *testing.T) {
	t.Parallel()

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
			name:        "exact match skill name",
			query:       "buffer-cli",
			strategy:    "exact",
			limit:       1,
			expectFirst: "buffer-cli",
			minResults:  1,
		},
		{
			name:        "tag terms match as bag-of-words keywords",
			query:       "physics simulation",
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
	t.Parallel()

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

func TestEngine_RRFDocIDCollisionAndExactPrecedenceAndAtCatalog(t *testing.T) {
	t.Parallel()

	items := []catalog.Item{
		{ID: "go", Name: "go", Description: "Go skill in catalog A", Source: "cat-a"},
		{ID: "go", Name: "go", Description: "Go skill in catalog B", Source: "cat-b"},
		{ID: "go-helper", Name: "go-helper", Description: "go go go helper for go", Tags: []string{"go"}, Source: "default"},
	}
	eng := search.NewEngine()
	_ = eng.Index(items)

	// 1. Identical IDs from distinct catalogs must not overwrite each other in RRF fusion.
	res := eng.Search("go", "hybrid", 10, 0.0)
	if len(res) != 3 {
		t.Fatalf("expected 3 results (including both duplicate IDs from cat-a and cat-b), got %d", len(res))
	}
	// Exact matches must strictly outrank prefix/substring match "go-helper", with deterministic Source ordering.
	if res[0].Item.ID != "go" || res[0].Item.Source != "cat-a" {
		t.Errorf("res[0] = %s@%s, want go@cat-a", res[0].Item.ID, res[0].Item.Source)
	}
	if res[1].Item.ID != "go" || res[1].Item.Source != "cat-b" {
		t.Errorf("res[1] = %s@%s, want go@cat-b", res[1].Item.ID, res[1].Item.Source)
	}
	if res[2].Item.ID != "go-helper" {
		t.Errorf("res[2] = %s, want go-helper", res[2].Item.ID)
	}

	// 2. @default filters to default catalog items only.
	defRes := eng.Search("go@default", "hybrid", 10, 0.0)
	if len(defRes) != 1 || defRes[0].Item.ID != "go-helper" {
		t.Errorf("expected go@default to return only go-helper, got %+v", defRes)
	}
}

func TestEngine_EmptyAndThreshold(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	eng := search.NewEngine()
	_ = eng.Index(sampleCatalogItems)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(iter int) {
			defer wg.Done()
			queries := []string{"game", "astro", "godoctor", "physics", "simulation"}
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

func TestEngine_CatalogFilterAndBlankQueryAndSuggestRanking(t *testing.T) {
	t.Parallel()

	items := append([]catalog.Item{}, sampleCatalogItems...)
	items = append(items, catalog.Item{
		ID:          "go-specialist",
		Name:        "go-specialist",
		Title:       "Go Specialist",
		Description: "Idiomatic Go patterns",
		Tags:        []string{"go", "patterns"},
		Source:      "github:go-skills",
	})

	eng := search.NewEngine()
	_ = eng.Index(items)

	// 1. Blank or whitespace-only query returns no results
	if got := eng.Search("   ", "hybrid", 10, 0.0); len(got) != 0 {
		t.Errorf("expected 0 results for whitespace query, got %d", len(got))
	}

	// 2. @<catalog> suffix filters to matching catalog
	resAt := eng.Search("go@go-skills", "hybrid", 10, 0.0)
	if len(resAt) != 1 || resAt[0].Item.ID != "go-specialist" {
		t.Errorf("expected go@go-skills to return only go-specialist, got %+v", resAt)
	}

	// 3. Filter-only @<catalog> returns items from that catalog
	resCatOnly := eng.Search("@go-skills", "hybrid", 10, 0.0)
	if len(resCatOnly) != 1 || resCatOnly[0].Item.ID != "go-specialist" {
		t.Errorf("expected @go-skills to return only go-specialist, got %+v", resCatOnly)
	}

	// 4. Suggest ranks prefix and substring matches strictly ahead of edit-distance matches even when lenDiff >= 100
	veryLongSubstring := "prefix-" + "genkit-" + "01234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789"
	candidates := []string{"genkis", veryLongSubstring, "godoctor"}
	suggs := search.Suggest("genkit", candidates, 3)
	if len(suggs) < 2 || suggs[0] != veryLongSubstring {
		t.Errorf("expected long substring match to rank ahead of edit-distance match, got %v", suggs)
	}
}
