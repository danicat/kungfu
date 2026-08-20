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

package search

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Suggestion represents a ranked suggestion candidate.
type Suggestion struct {
	Text  string
	Score int // Lower is better (0 = exact match)
}

// Levenshtein calculates the Levenshtein edit distance between two strings using Unicode runes.
func Levenshtein(a, b string) int {
	ra := []rune(strings.ToLower(strings.TrimSpace(a)))
	rb := []rune(strings.ToLower(strings.TrimSpace(b)))
	la := len(ra)
	lb := len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		copy(prev, curr)
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// Suggest finds the best matching candidates for a given query from a slice of candidate strings.
// Returns at most maxResults suggestions sorted by relevance (prefixes, substrings, lowest edit distance).
func Suggest(query string, candidates []string, maxResults int) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || len(candidates) == 0 {
		return nil
	}
	if maxResults <= 0 {
		maxResults = 3
	}

	seen := make(map[string]bool)
	var scored []Suggestion
	qRuneLen := utf8.RuneCountInString(q)

	for _, cand := range candidates {
		candClean := strings.TrimSpace(cand)
		cLower := strings.ToLower(candClean)
		if cLower == "" || seen[cLower] {
			continue
		}
		seen[cLower] = true

		cRuneLen := utf8.RuneCountInString(cLower)

		// Exact match
		if cLower == q {
			scored = append(scored, Suggestion{Text: candClean, Score: 0})
			continue
		}

		// Prefix match (e.g. godoc -> godoctor)
		if strings.HasPrefix(cLower, q) {
			scored = append(scored, Suggestion{Text: candClean, Score: 1 + (cRuneLen - qRuneLen)})
			continue
		}

		// Substring match (e.g. doctor -> godoctor)
		if strings.Contains(cLower, q) {
			scored = append(scored, Suggestion{Text: candClean, Score: 10 + (cRuneLen - qRuneLen)})
			continue
		}

		// Edit distance match
		dist := Levenshtein(q, cLower)
		maxAllowedDist := 3
		if qRuneLen <= 3 {
			maxAllowedDist = 1
		} else if qRuneLen <= 5 {
			maxAllowedDist = 2
		}

		if dist <= maxAllowedDist {
			scored = append(scored, Suggestion{Text: candClean, Score: 20 + dist})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score < scored[j].Score
		}
		return len(scored[i].Text) < len(scored[j].Text)
	})

	var results []string
	for i := 0; i < len(scored) && i < maxResults; i++ {
		results = append(results, scored[i].Text)
	}
	return results
}
