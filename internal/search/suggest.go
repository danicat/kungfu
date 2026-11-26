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
	"sort"
	"strings"
	"unicode/utf8"
)

// levenshteinNormalized calculates the Levenshtein edit distance between two pre-normalized strings using Unicode runes.
func levenshteinNormalized(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	la, lb := len(ra), len(rb)
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
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

type scoredSuggestion struct {
	text    string
	tier    int // 0=exact, 1=prefix, 2=substring, 3=edit-distance
	score   int // within-tier distance/length delta
	runeLen int
}

// Suggest finds the best matching candidates for query sorted strictly by tier
// (exact, prefix, substring, edit distance), within-tier score, rune length, and text.
func Suggest(query string, candidates []string, maxResults int) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || len(candidates) == 0 {
		return nil
	}
	if maxResults <= 0 {
		maxResults = 3
	}

	seen := make(map[string]struct{}, len(candidates))
	scored := make([]scoredSuggestion, 0, min(len(candidates), maxResults*2))
	qRuneLen := utf8.RuneCountInString(q)

	for _, cand := range candidates {
		candClean := strings.TrimSpace(cand)
		cLower := strings.ToLower(candClean)
		if cLower == "" {
			continue
		}
		if _, ok := seen[cLower]; ok {
			continue
		}
		seen[cLower] = struct{}{}

		cRuneLen := utf8.RuneCountInString(cLower)

		if cLower == q {
			scored = append(scored, scoredSuggestion{text: candClean, tier: 0, score: 0, runeLen: cRuneLen})
			continue
		}
		if strings.HasPrefix(cLower, q) {
			scored = append(scored, scoredSuggestion{text: candClean, tier: 1, score: cRuneLen - qRuneLen, runeLen: cRuneLen})
			continue
		}
		if strings.Contains(cLower, q) {
			scored = append(scored, scoredSuggestion{text: candClean, tier: 2, score: cRuneLen - qRuneLen, runeLen: cRuneLen})
			continue
		}

		dist := levenshteinNormalized(q, cLower)
		maxAllowedDist := 3
		if qRuneLen <= 3 {
			maxAllowedDist = 1
		} else if qRuneLen <= 5 {
			maxAllowedDist = 2
		}
		if dist <= maxAllowedDist {
			scored = append(scored, scoredSuggestion{text: candClean, tier: 3, score: dist, runeLen: cRuneLen})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].tier != scored[j].tier {
			return scored[i].tier < scored[j].tier
		}
		if scored[i].score != scored[j].score {
			return scored[i].score < scored[j].score
		}
		if scored[i].runeLen != scored[j].runeLen {
			return scored[i].runeLen < scored[j].runeLen
		}
		return scored[i].text < scored[j].text
	})

	limit := min(len(scored), maxResults)
	results := make([]string, limit)
	for i := range limit {
		results[i] = scored[i].text
	}
	return results
}
