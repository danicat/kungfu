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
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/danicat/kungfu/internal/catalog"
)

// Field weights and bitmasks for search ranking.
const (
	WeightName        float32 = 4.0
	WeightTitle       float32 = 4.0
	WeightTags        float32 = 3.0
	WeightDescription float32 = 1.5
	WeightOther       float32 = 1.0

	FieldMaskName        uint8 = 1 << 0
	FieldMaskTitle       uint8 = 1 << 1
	FieldMaskTags        uint8 = 1 << 2
	FieldMaskDescription uint8 = 1 << 3
	FieldMaskOther       uint8 = 1 << 4

	// DefaultRRFK is the smoothing constant for Reciprocal Rank Fusion.
	DefaultRRFK = 60.0
	maxDualRRF  = 2.0 / (DefaultRRFK + 1.0)
)

// Result represents a ranked search result item with scoring metadata.
type Result struct {
	Item        catalog.Item `json:"item"`
	Score       float32      `json:"score"`
	Strategy    string       `json:"strategy"`
	MatchFields []string     `json:"match_fields"`
}

type precomputedTag struct {
	lower   string
	norm    string
	compact string
}

type indexedDoc struct {
	lowName     string
	normName    string
	compName    string
	lowTitle    string
	normTitle   string
	compTitle   string
	lowID       string
	normID      string
	compID      string
	lowDesc     string
	normDesc    string
	lowSource   string
	trimSource  string
	safeSource  string
	safeTrimSrc string
	tags        []precomputedTag
}

type docScore struct {
	docID uint32
	score float32
	mask  uint8
}

// Engine is a pure Go search engine supporting BM25, Exact, and Hybrid RRF ranking.
// After Index completes, Search is safe for concurrent read-only calls.
type Engine struct {
	tokenizer  *Tokenizer
	items      []catalog.Item
	docs       []indexedDoc
	docLengths []float32
	avgDocLen  float32
	termToID   map[string]uint32
	docTFs     []map[uint32]float32
	docMasks   []map[uint32]uint8
	docFreqs   map[uint32]uint32
	bm25Scorer *BM25Scorer
}

// NewEngine initializes an empty search Engine.
func NewEngine() *Engine {
	return &Engine{
		tokenizer:  NewTokenizer(),
		termToID:   make(map[string]uint32),
		docFreqs:   make(map[uint32]uint32),
		bm25Scorer: NewBM25Scorer(DefaultBM25K1, DefaultBM25B),
	}
}

// Index indexes a collection of catalog items and precomputes normalized fields.
func (e *Engine) Index(items []catalog.Item) error {
	numDocs := len(items)
	e.items = make([]catalog.Item, numDocs)
	copy(e.items, items)
	e.docs = make([]indexedDoc, numDocs)
	e.termToID = make(map[string]uint32)
	e.docLengths = make([]float32, numDocs)
	e.docTFs = make([]map[uint32]float32, numDocs)
	e.docMasks = make([]map[uint32]uint8, numDocs)
	e.docFreqs = make(map[uint32]uint32)

	if numDocs == 0 {
		e.avgDocLen = 0
		e.bm25Scorer.Build(0, 0, nil, nil)
		return nil
	}

	var totalLength float32
	getTermID := func(term string) uint32 {
		if id, ok := e.termToID[term]; ok {
			return id
		}
		id := uint32(len(e.termToID))
		e.termToID[term] = id
		return id
	}

	for docID, item := range items {
		tfs := make(map[uint32]float32)
		masks := make(map[uint32]uint8)
		var docLen float32

		indexField := func(text string, weight float32, mask uint8) {
			if len(text) == 0 {
				return
			}
			for _, tok := range e.tokenizer.Tokenize(text) {
				tid := getTermID(tok)
				tfs[tid] += weight
				masks[tid] |= mask
				docLen += weight
			}
		}

		indexField(item.Name, WeightName, FieldMaskName)
		indexField(item.Title, WeightTitle, FieldMaskTitle)
		for _, tag := range item.Tags {
			indexField(tag, WeightTags, FieldMaskTags)
		}
		indexField(item.Description, WeightDescription, FieldMaskDescription)
		indexField(item.Path, WeightOther, FieldMaskOther)
		indexField(item.Source, WeightOther, FieldMaskOther)
		indexField(item.Author, WeightOther, FieldMaskOther)
		indexField(string(item.Type), WeightOther, FieldMaskOther)

		e.docLengths[docID] = docLen
		e.docTFs[docID] = tfs
		e.docMasks[docID] = masks
		totalLength += docLen

		for tid := range tfs {
			e.docFreqs[tid]++
		}

		lowName := strings.ToLower(item.Name)
		lowTitle := strings.ToLower(item.Title)
		lowID := strings.ToLower(item.ID)
		lowDesc := strings.ToLower(item.Description)
		lowSrc := strings.ToLower(strings.TrimSpace(item.Source))
		trimSrc := strings.TrimPrefix(lowSrc, "github:")

		tags := make([]precomputedTag, len(item.Tags))
		for i, tag := range item.Tags {
			tags[i] = precomputedTag{
				lower:   strings.ToLower(tag),
				norm:    normalizeKey(tag),
				compact: compactKey(tag),
			}
		}

		e.docs[docID] = indexedDoc{
			lowName:     lowName,
			normName:    normalizeKey(lowName),
			compName:    compactKey(lowName),
			lowTitle:    lowTitle,
			normTitle:   normalizeKey(lowTitle),
			compTitle:   compactKey(lowTitle),
			lowID:       lowID,
			normID:      normalizeKey(lowID),
			compID:      compactKey(lowID),
			lowDesc:     lowDesc,
			normDesc:    normalizeKey(lowDesc),
			lowSource:   lowSrc,
			trimSource:  trimSrc,
			safeSource:  catalog.SafeCatalogFileName(lowSrc),
			safeTrimSrc: catalog.SafeCatalogFileName(trimSrc),
			tags:        tags,
		}
	}

	e.avgDocLen = totalLength / float32(numDocs)
	e.bm25Scorer.Build(numDocs, e.avgDocLen, e.docLengths, e.docFreqs)
	return nil
}

type parsedFilter struct {
	cleanQuery string
	catalog    string
	safeCat    string
}

func parseQueryFilters(query string) parsedFilter {
	parts := strings.Fields(query)
	var cleanParts []string
	var filter parsedFilter

	for _, p := range parts {
		if idx := strings.LastIndex(p, "@"); idx != -1 && idx < len(p)-1 {
			catPart := strings.ToLower(strings.TrimSpace(p[idx+1:]))
			if catPart != "" {
				filter.catalog = catPart
			}
			if idx > 0 {
				cleanParts = append(cleanParts, p[:idx])
			}
		} else {
			cleanParts = append(cleanParts, p)
		}
	}

	if filter.catalog != "" {
		filter.safeCat = catalog.SafeCatalogFileName(filter.catalog)
	}
	filter.cleanQuery = strings.Join(cleanParts, " ")
	return filter
}

func matchesFilter(doc *indexedDoc, filter *parsedFilter) bool {
	if filter.catalog != "" {
		wantCat := filter.catalog
		matched := false
		if wantCat == "default" && (doc.lowSource == "" || doc.lowSource == "default" || doc.lowSource == "remote") {
			matched = true
		} else if doc.lowSource == wantCat || doc.trimSource == wantCat ||
			(filter.safeCat != "" && (doc.safeSource == filter.safeCat || doc.safeTrimSrc == filter.safeCat)) ||
			strings.HasSuffix(doc.trimSource, "/"+wantCat) {
			matched = true
		}
		if !matched {
			return false
		}
	}
	return true
}

func (e *Engine) compareDocIDs(a, b uint32) bool {
	ia, ib := &e.items[a], &e.items[b]
	if ia.ID != ib.ID {
		return ia.ID < ib.ID
	}
	if ia.Name != ib.Name {
		return ia.Name < ib.Name
	}
	if ia.Source != ib.Source {
		return ia.Source < ib.Source
	}
	return a < b
}

func (e *Engine) sortDocScores(scores []docScore) {
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return e.compareDocIDs(scores[i].docID, scores[j].docID)
	})
}

// Search searches indexed catalog items using the specified strategy (hybrid, bm25, exact).
func (e *Engine) Search(query string, strategy string, limit int, threshold float32) []Result {
	if len(e.items) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 10
	}

	strat := strings.ToLower(strings.TrimSpace(strategy))
	if strat == "" {
		strat = "hybrid"
	}

	filter := parseQueryFilters(query)
	activeQuery := strings.TrimSpace(filter.cleanQuery)
	hasFilter := filter.catalog != ""

	if activeQuery == "" && !hasFilter {
		return nil
	}

	candidateDocIDs := make([]uint32, 0, len(e.items))
	for docID := range e.items {
		if matchesFilter(&e.docs[docID], &filter) {
			candidateDocIDs = append(candidateDocIDs, uint32(docID))
		}
	}
	if len(candidateDocIDs) == 0 {
		return nil
	}

	if activeQuery == "" {
		if 1.0 <= threshold {
			return nil
		}
		baseFields := []string{"other"}
		sort.Slice(candidateDocIDs, func(i, j int) bool {
			return e.compareDocIDs(candidateDocIDs[i], candidateDocIDs[j])
		})
		if len(candidateDocIDs) > limit {
			candidateDocIDs = candidateDocIDs[:limit]
		}
		results := make([]Result, 0, len(candidateDocIDs))
		for _, docID := range candidateDocIDs {
			results = append(results, Result{
				Item:        e.items[docID],
				Score:       1.0,
				Strategy:    strat,
				MatchFields: slices.Clone(baseFields),
			})
		}
		return results
	}

	switch strat {
	case "bm25":
		return e.buildResults(e.scoreBM25(activeQuery, candidateDocIDs, threshold), "bm25", limit)
	case "exact":
		return e.buildResults(e.scoreExact(activeQuery, candidateDocIDs, threshold), "exact", limit)
	default:
		return e.searchHybrid(activeQuery, candidateDocIDs, limit, threshold)
	}
}

func (e *Engine) buildResults(scores []docScore, strategy string, limit int) []Result {
	if len(scores) > limit {
		scores = scores[:limit]
	}
	results := make([]Result, 0, len(scores))
	for _, ds := range scores {
		results = append(results, Result{
			Item:        e.items[ds.docID],
			Score:       ds.score,
			Strategy:    strategy,
			MatchFields: maskToFieldNames(ds.mask),
		})
	}
	return results
}

func (e *Engine) scoreBM25(query string, candidateDocIDs []uint32, threshold float32) []docScore {
	tokens := e.tokenizer.Tokenize(query)
	if len(tokens) == 0 {
		tokens = e.tokenizer.TokenizeRaw(query)
	}
	if len(tokens) == 0 {
		return nil
	}

	seenTerms := make(map[uint32]struct{}, len(tokens))
	queryTermIDs := make([]uint32, 0, len(tokens))
	for _, tok := range tokens {
		if tid, ok := e.termToID[tok]; ok {
			if _, dup := seenTerms[tid]; !dup {
				seenTerms[tid] = struct{}{}
				queryTermIDs = append(queryTermIDs, tid)
			}
		}
	}
	if len(queryTermIDs) == 0 {
		return nil
	}

	scores := make([]docScore, 0, len(candidateDocIDs))
	for _, docID := range candidateDocIDs {
		tfs := e.docTFs[docID]
		masks := e.docMasks[docID]
		score := e.bm25Scorer.Score(docID, queryTermIDs, nil, tfs)
		if score > threshold {
			var combinedMask uint8
			for _, tid := range queryTermIDs {
				combinedMask |= masks[tid]
			}
			scores = append(scores, docScore{
				docID: docID,
				score: score,
				mask:  combinedMask,
			})
		}
	}

	e.sortDocScores(scores)
	return scores
}

func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' || r == '/' || r == ':' {
			sb.WriteRune(' ')
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			sb.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

func compactKey(s string) string {
	s = strings.ToLower(s)
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func normalizeRRFScore(rawScore float32) float32 {
	if rawScore <= 0 {
		return 0
	}
	// Raw score >= 2.0 indicates an exact identity match (+2.0 boost + RRF).
	if rawScore >= 2.0 {
		val := 0.95 + 0.05*((rawScore-2.0)/maxDualRRF)
		if val > 1.0 {
			return 1.0
		}
		return val
	}
	// Raw score >= 1.5 indicates 1-edit distance fuzzy match (+1.6 boost, capped < 1.95).
	if rawScore >= 1.5 {
		val := 0.90 + 0.049*((rawScore-1.5)/0.45)
		if val > 0.949 {
			return 0.949
		}
		return val
	}
	// Raw score >= 1.0 indicates prefix/substring or 2-edit distance match.
	if rawScore >= 1.0 {
		val := 0.80 + 0.10*((rawScore-1.0)/0.5)
		if val > 0.90 {
			return 0.90
		}
		return val
	}
	// Raw score >= 0.7 indicates exact tag match (+0.8 boost).
	if rawScore >= 0.7 {
		val := 0.70 + 0.10*((rawScore-0.7)/0.3)
		if val > 0.80 {
			return 0.80
		}
		return val
	}
	scaled := (rawScore / maxDualRRF) * 0.70
	if scaled > 0.70 {
		return 0.70
	}
	if scaled < 0.01 {
		return 0.01
	}
	return scaled
}

func (e *Engine) scoreExact(query string, candidateDocIDs []uint32, threshold float32) []docScore {
	queryNorm := strings.ToLower(strings.TrimSpace(query))
	if queryNorm == "" {
		return nil
	}

	normQuery := normalizeKey(queryNorm)
	compQuery := compactKey(queryNorm)
	compRuneLen := utf8.RuneCountInString(compQuery)
	rawTokens := e.tokenizer.TokenizeRaw(queryNorm)
	rawTokNorms := make([]string, len(rawTokens))
	for i, tok := range rawTokens {
		rawTokNorms[i] = normalizeKey(tok)
	}

	scores := make([]docScore, 0, len(candidateDocIDs))
	for _, docID := range candidateDocIDs {
		doc := &e.docs[docID]
		var score float32
		var mask uint8

		if doc.lowID == queryNorm || doc.lowName == queryNorm || doc.lowTitle == queryNorm ||
			(normQuery != "" && (doc.normName == normQuery || doc.normID == normQuery || doc.normTitle == normQuery)) ||
			(compQuery != "" && (doc.compName == compQuery || doc.compID == compQuery || doc.compTitle == compQuery)) {
			score += 10.0 * WeightName
			mask |= FieldMaskName | FieldMaskTitle
		} else if strings.Contains(doc.lowName, queryNorm) ||
			(normQuery != "" && strings.Contains(doc.normName, normQuery)) ||
			(normQuery != "" && len(doc.normTitle) > 0 && strings.Contains(doc.normTitle, normQuery)) {
			score += 5.0 * WeightName
			mask |= FieldMaskName
		} else if normQuery != "" && compQuery != "" && compRuneLen >= 3 {
			// Fuzzy match (edit distance <= 2) on name.
			dist := levenshteinNormalized(normQuery, doc.normName)
			distComp := levenshteinNormalized(compQuery, doc.compName)
			minDist := min(distComp, dist)
			if minDist == 1 {
				score += 8.0 * WeightName
				mask |= FieldMaskName
			} else if minDist == 2 && compRuneLen >= 4 {
				score += 4.0 * WeightName
				mask |= FieldMaskName
			}
		}

		for _, tag := range doc.tags {
			if tag.lower == queryNorm || (normQuery != "" && tag.norm == normQuery) || (compQuery != "" && tag.compact == compQuery) {
				score += 8.0 * WeightTags
				mask |= FieldMaskTags
			} else if strings.Contains(tag.lower, queryNorm) || (normQuery != "" && strings.Contains(tag.norm, normQuery)) {
				score += 3.0 * WeightTags
				mask |= FieldMaskTags
			}
		}

		if strings.Contains(doc.lowDesc, queryNorm) || (normQuery != "" && strings.Contains(doc.normDesc, normQuery)) {
			score += 3.0 * WeightDescription
			mask |= FieldMaskDescription
		}

		matchedTokens := 0
		for i, tok := range rawTokens {
			tokNorm := rawTokNorms[i]
			tokFound := false
			if strings.Contains(doc.lowName, tok) || (tokNorm != "" && strings.Contains(doc.normName, tokNorm)) || strings.Contains(doc.lowTitle, tok) {
				score += WeightName
				mask |= FieldMaskName
				tokFound = true
			}
			for _, tag := range doc.tags {
				if strings.Contains(tag.lower, tok) || (tokNorm != "" && strings.Contains(tag.norm, tokNorm)) {
					score += WeightTags
					mask |= FieldMaskTags
					tokFound = true
				}
			}
			if strings.Contains(doc.lowDesc, tok) {
				score += WeightDescription
				mask |= FieldMaskDescription
				tokFound = true
			}
			if tokFound {
				matchedTokens++
			}
		}

		if len(rawTokens) > 0 && matchedTokens == len(rawTokens) {
			score += 4.0
		}

		if score > threshold {
			scores = append(scores, docScore{
				docID: docID,
				score: score,
				mask:  mask,
			})
		}
	}

	e.sortDocScores(scores)
	return scores
}

func (e *Engine) searchHybrid(query string, candidateDocIDs []uint32, limit int, threshold float32) []Result {
	bm25Scores := e.scoreBM25(query, candidateDocIDs, 0.0)
	exactScores := e.scoreExact(query, candidateDocIDs, 0.0)

	rrfScores := make(map[uint32]float32, len(bm25Scores)+len(exactScores))
	matchMasks := make(map[uint32]uint8, len(bm25Scores)+len(exactScores))

	for rank, ds := range bm25Scores {
		rrfScores[ds.docID] += 1.0 / (DefaultRRFK + float32(rank+1))
		matchMasks[ds.docID] |= ds.mask
	}
	for rank, ds := range exactScores {
		rrfScores[ds.docID] += 1.0 / (DefaultRRFK + float32(rank+1))
		matchMasks[ds.docID] |= ds.mask
	}

	queryNorm := strings.ToLower(strings.TrimSpace(query))
	normQuery := normalizeKey(queryNorm)
	compQuery := compactKey(queryNorm)
	compRuneLen := utf8.RuneCountInString(compQuery)

	ranked := make([]docScore, 0, len(rrfScores))
	for docID, rrf := range rrfScores {
		doc := &e.docs[docID]
		mask := matchMasks[docID]
		var boost float32
		isExact := false

		if doc.lowName == queryNorm || doc.lowID == queryNorm || doc.lowTitle == queryNorm ||
			(normQuery != "" && (doc.normName == normQuery || doc.normID == normQuery || doc.normTitle == normQuery)) ||
			(compQuery != "" && (doc.compName == compQuery || doc.compID == compQuery || doc.compTitle == compQuery)) {
			boost = 2.0
			mask |= FieldMaskName
			isExact = true
		} else if normQuery != "" && (strings.Contains(doc.normName, normQuery) || strings.Contains(doc.normTitle, normQuery)) {
			boost = 1.2
			mask |= FieldMaskName
		} else if normQuery != "" && compQuery != "" && compRuneLen >= 3 {
			dist := levenshteinNormalized(normQuery, doc.normName)
			distComp := levenshteinNormalized(compQuery, doc.compName)
			minDist := min(distComp, dist)
			if minDist == 1 {
				boost = 1.6
				mask |= FieldMaskName
			} else if minDist == 2 && compRuneLen >= 4 {
				boost = 1.0
				mask |= FieldMaskName
			}
		}

		for _, tag := range doc.tags {
			if tag.lower == queryNorm || (normQuery != "" && tag.norm == normQuery) || (compQuery != "" && tag.compact == compQuery) {
				if !isExact {
					boost += 0.8
				}
				mask |= FieldMaskTags
				break
			}
		}

		// Keep non-exact boost sums strictly below the exact identity match tier (< 1.95).
		if !isExact && boost > 1.90 {
			boost = 1.90
		}

		normScore := normalizeRRFScore(rrf + boost)
		if normScore > threshold {
			ranked = append(ranked, docScore{
				docID: docID,
				score: normScore,
				mask:  mask,
			})
		}
	}

	e.sortDocScores(ranked)
	return e.buildResults(ranked, "hybrid", limit)
}

func maskToFieldNames(mask uint8) []string {
	var fields []string
	if mask&FieldMaskName != 0 {
		fields = append(fields, "name")
	}
	if mask&FieldMaskTitle != 0 {
		fields = append(fields, "title")
	}
	if mask&FieldMaskTags != 0 {
		fields = append(fields, "tags")
	}
	if mask&FieldMaskDescription != 0 {
		fields = append(fields, "description")
	}
	if mask&FieldMaskOther != 0 {
		fields = append(fields, "other")
	}
	return fields
}
