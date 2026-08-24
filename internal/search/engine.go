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
	"sync"
	"unicode"

	"github.com/danicat/kungfu/internal/catalog"
)

// Field weights for search ranking.
const (
	WeightName        float32 = 4.0
	WeightTitle       float32 = 4.0
	WeightTags        float32 = 3.0
	WeightCategory    float32 = 2.5
	WeightDescription float32 = 1.5
	WeightOther       float32 = 1.0

	FieldMaskName        uint8 = 1 << 0
	FieldMaskTitle       uint8 = 1 << 1
	FieldMaskTags        uint8 = 1 << 2
	FieldMaskCategory    uint8 = 1 << 3
	FieldMaskDescription uint8 = 1 << 4
	FieldMaskOther       uint8 = 1 << 5

	// DefaultRRFK is the smoothing constant for Reciprocal Rank Fusion.
	DefaultRRFK = 60.0
)

// Result represents a ranked search result item with scoring metadata.
type Result struct {
	Item        catalog.Item `json:"item"`
	Score       float32      `json:"score"`
	Strategy    string       `json:"strategy"`
	MatchFields []string     `json:"match_fields"`
}

// Indexer defines the core catalog search engine contract.
type Indexer interface {
	Index(items []catalog.Item) error
	Search(query string, strategy string, limit int, threshold float32) []Result
}

// Posting represents an entry in the inverted index for a term in a document.
type Posting struct {
	DocID     uint32
	Freq      float32
	FieldMask uint8
}

// Engine is a pure Go high-throughput search engine supporting BM25, TF-IDF, Exact, and Hybrid RRF ranking.
type Engine struct {
	mu          sync.RWMutex
	tokenizer   *Tokenizer
	items       []catalog.Item
	docLengths  []float32
	avgDocLen   float32
	termToID    map[string]uint32
	idToTerm    []string
	postings    [][]Posting
	docTFs      []map[uint32]float32
	docMasks    []map[uint32]uint8
	docFreqs    map[uint32]uint32
	bm25Scorer  *BM25Scorer
	tfidfScorer *TFIDFScorer
}

// NewEngine initializes an empty search Engine.
func NewEngine() *Engine {
	return &Engine{
		tokenizer:   NewTokenizer(),
		termToID:    make(map[string]uint32),
		docFreqs:    make(map[uint32]uint32),
		bm25Scorer:  NewBM25Scorer(DefaultBM25K1, DefaultBM25B),
		tfidfScorer: NewTFIDFScorer(),
	}
}

// Index indexes a collection of catalog items.
func (e *Engine) Index(items []catalog.Item) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.items = make([]catalog.Item, len(items))
	copy(e.items, items)

	numDocs := len(items)
	e.termToID = make(map[string]uint32)
	e.idToTerm = make([]string, 0)
	e.postings = make([][]Posting, 0)
	e.docLengths = make([]float32, numDocs)
	e.docTFs = make([]map[uint32]float32, numDocs)
	e.docMasks = make([]map[uint32]uint8, numDocs)
	e.docFreqs = make(map[uint32]uint32)

	if numDocs == 0 {
		e.avgDocLen = 0
		e.bm25Scorer.Build(0, 0, nil, nil)
		e.tfidfScorer.Build(0, nil, nil)
		return nil
	}

	var totalLength float32

	getTermID := func(term string) uint32 {
		if id, ok := e.termToID[term]; ok {
			return id
		}
		id := uint32(len(e.idToTerm))
		e.termToID[term] = id
		e.idToTerm = append(e.idToTerm, term)
		e.postings = append(e.postings, make([]Posting, 0))
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
			tokens := e.tokenizer.Tokenize(text)
			for _, tok := range tokens {
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
		indexField(item.Category, WeightCategory, FieldMaskCategory)
		indexField(item.Description, WeightDescription, FieldMaskDescription)
		indexField(item.Path, WeightOther, FieldMaskOther)
		indexField(item.Source, WeightOther, FieldMaskOther)
		indexField(item.Author, WeightOther, FieldMaskOther)
		indexField(string(item.Type), WeightOther, FieldMaskOther)

		e.docLengths[docID] = docLen
		e.docTFs[docID] = tfs
		e.docMasks[docID] = masks
		totalLength += docLen

		for tid, tf := range tfs {
			e.postings[tid] = append(e.postings[tid], Posting{
				DocID:     uint32(docID),
				Freq:      tf,
				FieldMask: masks[tid],
			})
			e.docFreqs[tid]++
		}
	}

	e.avgDocLen = totalLength / float32(numDocs)
	e.bm25Scorer.Build(numDocs, e.avgDocLen, e.docLengths, e.docFreqs)
	e.tfidfScorer.Build(numDocs, e.docTFs, e.docFreqs)

	return nil
}

// parsedFilter holds query directives like category:x, tag:y, type:z.
type parsedFilter struct {
	cleanQuery string
	category   string
	tags       []string
	itemType   string
}

func parseQueryFilters(query string) parsedFilter {
	parts := strings.Fields(query)
	var cleanParts []string
	var filter parsedFilter

	for _, p := range parts {
		low := strings.ToLower(p)
		if strings.HasPrefix(low, "category:") {
			filter.category = strings.TrimPrefix(low, "category:")
		} else if strings.HasPrefix(low, "cat:") {
			filter.category = strings.TrimPrefix(low, "cat:")
		} else if strings.HasPrefix(low, "tag:") {
			filter.tags = append(filter.tags, strings.TrimPrefix(low, "tag:"))
		} else if strings.HasPrefix(low, "type:") {
			filter.itemType = strings.TrimPrefix(low, "type:")
		} else {
			cleanParts = append(cleanParts, p)
		}
	}

	filter.cleanQuery = strings.Join(cleanParts, " ")
	return filter
}

// matchesFilter checks whether a catalog Item satisfies extracted category, tag, or type constraints.
func matchesFilter(item *catalog.Item, filter *parsedFilter) bool {
	if len(filter.category) > 0 {
		if !strings.EqualFold(item.Category, filter.category) {
			return false
		}
	}
	if len(filter.itemType) > 0 {
		if !strings.EqualFold(string(item.Type), filter.itemType) {
			return false
		}
	}
	if len(filter.tags) > 0 {
		for _, reqTag := range filter.tags {
			found := false
			for _, itemTag := range item.Tags {
				if strings.EqualFold(itemTag, reqTag) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

// Search searches the indexed catalog items using the specified strategy (hybrid, bm25, tfidf, exact).
func (e *Engine) Search(query string, strategy string, limit int, threshold float32) []Result {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if len(e.items) == 0 {
		return nil
	}

	if limit <= 0 {
		limit = 10
	}

	filter := parseQueryFilters(query)
	activeQuery := filter.cleanQuery

	// Filter candidate documents
	candidateDocIDs := make([]uint32, 0, len(e.items))
	for docID := range e.items {
		if matchesFilter(&e.items[docID], &filter) {
			candidateDocIDs = append(candidateDocIDs, uint32(docID))
		}
	}

	if len(candidateDocIDs) == 0 {
		return nil
	}

	// If query text is empty after filtering directives, return matching candidate docs
	if len(strings.TrimSpace(activeQuery)) == 0 {
		results := make([]Result, 0, len(candidateDocIDs))
		for _, docID := range candidateDocIDs {
			results = append(results, Result{
				Item:        e.items[docID],
				Score:       1.0,
				Strategy:    strategy,
				MatchFields: []string{"category", "tags"},
			})
		}
		if len(results) > limit {
			results = results[:limit]
		}
		return results
	}

	strat := strings.ToLower(strings.TrimSpace(strategy))
	if strat == "" {
		strat = "hybrid"
	}

	switch strat {
	case "bm25":
		return e.searchBM25(activeQuery, candidateDocIDs, limit, threshold)
	case "tfidf":
		return e.searchTFIDF(activeQuery, candidateDocIDs, limit, threshold)
	case "exact":
		return e.searchExact(activeQuery, candidateDocIDs, limit, threshold)
	case "hybrid":
		fallthrough
	default:
		return e.searchHybrid(activeQuery, candidateDocIDs, limit, threshold)
	}
}

func (e *Engine) searchBM25(query string, candidateDocIDs []uint32, limit int, threshold float32) []Result {
	tokens := e.tokenizer.Tokenize(query)
	if len(tokens) == 0 {
		tokens = e.tokenizer.TokenizeRaw(query)
	}
	if len(tokens) == 0 {
		return nil
	}

	queryTermIDs := make([]uint32, 0, len(tokens))
	queryWeights := make([]float32, 0, len(tokens))
	for _, tok := range tokens {
		if tid, ok := e.termToID[tok]; ok {
			queryTermIDs = append(queryTermIDs, tid)
			queryWeights = append(queryWeights, 1.0)
		}
	}

	if len(queryTermIDs) == 0 {
		return nil
	}

	type docScore struct {
		docID uint32
		score float32
		mask  uint8
	}

	var scores []docScore
	for _, docID := range candidateDocIDs {
		tfs := e.docTFs[docID]
		masks := e.docMasks[docID]
		score := e.bm25Scorer.Score(docID, queryTermIDs, queryWeights, tfs)
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

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	if len(scores) > limit {
		scores = scores[:limit]
	}

	results := make([]Result, 0, len(scores))
	for _, ds := range scores {
		results = append(results, Result{
			Item:        e.items[ds.docID],
			Score:       ds.score,
			Strategy:    "bm25",
			MatchFields: maskToFieldNames(ds.mask),
		})
	}
	return results
}

func (e *Engine) searchTFIDF(query string, candidateDocIDs []uint32, limit int, threshold float32) []Result {
	tokens := e.tokenizer.Tokenize(query)
	if len(tokens) == 0 {
		tokens = e.tokenizer.TokenizeRaw(query)
	}
	if len(tokens) == 0 {
		return nil
	}

	queryTFs := make(map[uint32]float32)
	for _, tok := range tokens {
		if tid, ok := e.termToID[tok]; ok {
			queryTFs[tid] += 1.0
		}
	}

	if len(queryTFs) == 0 {
		return nil
	}

	type docScore struct {
		docID uint32
		score float32
		mask  uint8
	}

	var scores []docScore
	for _, docID := range candidateDocIDs {
		tfs := e.docTFs[docID]
		masks := e.docMasks[docID]
		score := e.tfidfScorer.Score(docID, queryTFs, tfs)
		if score > threshold {
			var combinedMask uint8
			for tid := range queryTFs {
				combinedMask |= masks[tid]
			}
			scores = append(scores, docScore{
				docID: docID,
				score: score,
				mask:  combinedMask,
			})
		}
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	if len(scores) > limit {
		scores = scores[:limit]
	}

	results := make([]Result, 0, len(scores))
	for _, ds := range scores {
		results = append(results, Result{
			Item:        e.items[ds.docID],
			Score:       ds.score,
			Strategy:    "tfidf",
			MatchFields: maskToFieldNames(ds.mask),
		})
	}
	return results
}

// normalizeKey converts delimiters (hyphens, underscores, dots, slashes, colons) to spaces and collapses whitespace.
func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' || r == '/' || r == ':' {
			sb.WriteRune(' ')
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			sb.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

// compactKey extracts all alphanumeric characters without whitespace or punctuation.
func compactKey(s string) string {
	s = strings.ToLower(s)
	var sb strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func normalizeRRFScore(rawScore float32) float32 {
	// Raw score >= 2.0 indicates an exact identity match (+2.0 boost + RRF)
	if rawScore >= 2.0 {
		val := 0.95 + 0.05*((rawScore-2.0)/0.0328)
		if val > 1.0 {
			val = 1.0
		}
		return val
	}
	// Raw score >= 1.5 indicates 1-edit distance fuzzy match (+1.6 boost)
	if rawScore >= 1.5 {
		val := 0.90 + 0.05*((rawScore-1.5)/0.5)
		if val > 0.95 {
			val = 0.95
		}
		return val
	}
	// Raw score >= 1.0 indicates prefix/substring or 2-edit distance match (+1.0 to +1.2 boost)
	if rawScore >= 1.0 {
		val := 0.80 + 0.10*((rawScore-1.0)/0.5)
		if val > 0.90 {
			val = 0.90
		}
		return val
	}
	// Raw score >= 0.7 indicates exact tag match (+0.8 boost)
	if rawScore >= 0.7 {
		val := 0.70 + 0.10*((rawScore-0.7)/0.3)
		if val > 0.80 {
			val = 0.80
		}
		return val
	}
	// Pure RRF ranking (max theoretical dual rank-1 is ~0.0328)
	scaled := (rawScore / 0.0328) * 0.70
	if scaled > 0.70 {
		scaled = 0.70
	}
	if scaled < 0.01 {
		scaled = 0.01
	}
	return scaled
}

func (e *Engine) searchExact(query string, candidateDocIDs []uint32, limit int, threshold float32) []Result {
	queryNorm := strings.ToLower(strings.TrimSpace(query))
	if len(queryNorm) == 0 {
		return nil
	}

	normQuery := normalizeKey(queryNorm)
	compQuery := compactKey(queryNorm)
	rawTokens := e.tokenizer.TokenizeRaw(queryNorm)

	type docScore struct {
		docID uint32
		score float32
		mask  uint8
	}

	var scores []docScore
	for _, docID := range candidateDocIDs {
		item := &e.items[docID]
		var score float32
		var mask uint8

		itemName := strings.ToLower(item.Name)
		itemTitle := strings.ToLower(item.Title)
		itemID := strings.ToLower(item.ID)
		itemCat := strings.ToLower(item.Category)
		itemDesc := strings.ToLower(item.Description)

		normName := normalizeKey(itemName)
		compName := compactKey(itemName)
		normTitle := normalizeKey(itemTitle)
		compTitle := compactKey(itemTitle)
		normID := normalizeKey(itemID)
		compID := compactKey(itemID)

		// 1. Exact full or normalized matches
		if itemID == queryNorm || itemName == queryNorm || itemTitle == queryNorm ||
			(normQuery != "" && (normName == normQuery || normID == normQuery || normTitle == normQuery)) ||
			(compQuery != "" && (compName == compQuery || compID == compQuery || compTitle == compQuery)) {
			score += 10.0 * WeightName
			mask |= FieldMaskName | FieldMaskTitle
		} else if strings.Contains(itemName, queryNorm) || strings.Contains(normName, normQuery) ||
			(len(normTitle) > 0 && strings.Contains(normTitle, normQuery)) {
			score += 5.0 * WeightName
			mask |= FieldMaskName
		} else {
			// Fuzzy match (edit distance <= 2) on name/id
			dist := Levenshtein(normQuery, normName)
			distComp := Levenshtein(compQuery, compName)
			minDist := dist
			if distComp < minDist {
				minDist = distComp
			}
			if minDist == 1 && len(compQuery) >= 3 {
				score += 8.0 * WeightName
				mask |= FieldMaskName
			} else if minDist == 2 && len(compQuery) >= 4 {
				score += 4.0 * WeightName
				mask |= FieldMaskName
			}
		}

		for _, tag := range item.Tags {
			tagLow := strings.ToLower(tag)
			normTag := normalizeKey(tag)
			compTag := compactKey(tag)
			if tagLow == queryNorm || (normQuery != "" && normTag == normQuery) || (compQuery != "" && compTag == compQuery) {
				score += 8.0 * WeightTags
				mask |= FieldMaskTags
			} else if strings.Contains(tagLow, queryNorm) || strings.Contains(normTag, normQuery) {
				score += 3.0 * WeightTags
				mask |= FieldMaskTags
			}
		}

		if itemCat == queryNorm || (normQuery != "" && normalizeKey(itemCat) == normQuery) {
			score += 6.0 * WeightCategory
			mask |= FieldMaskCategory
		} else if strings.Contains(itemCat, queryNorm) || (normQuery != "" && strings.Contains(normalizeKey(itemCat), normQuery)) {
			score += 2.0 * WeightCategory
			mask |= FieldMaskCategory
		}

		if strings.Contains(itemDesc, queryNorm) || (normQuery != "" && strings.Contains(normalizeKey(itemDesc), normQuery)) {
			score += 3.0 * WeightDescription
			mask |= FieldMaskDescription
		}

		// Token presence match
		matchedTokens := 0
		for _, tok := range rawTokens {
			tokNorm := normalizeKey(tok)
			tokFound := false
			if strings.Contains(itemName, tok) || (tokNorm != "" && strings.Contains(normName, tokNorm)) || strings.Contains(itemTitle, tok) {
				score += WeightName
				mask |= FieldMaskName
				tokFound = true
			}
			for _, tag := range item.Tags {
				if strings.Contains(strings.ToLower(tag), tok) || (tokNorm != "" && strings.Contains(normalizeKey(tag), tokNorm)) {
					score += WeightTags
					mask |= FieldMaskTags
					tokFound = true
				}
			}
			if strings.Contains(itemDesc, tok) {
				score += WeightDescription
				mask |= FieldMaskDescription
				tokFound = true
			}
			if tokFound {
				matchedTokens++
			}
		}

		if len(rawTokens) > 0 && matchedTokens == len(rawTokens) {
			score += 4.0 // full query token coverage bonus
		}

		if score > threshold {
			scores = append(scores, docScore{
				docID: docID,
				score: score,
				mask:  mask,
			})
		}
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	if len(scores) > limit {
		scores = scores[:limit]
	}

	results := make([]Result, 0, len(scores))
	for _, ds := range scores {
		results = append(results, Result{
			Item:        e.items[ds.docID],
			Score:       ds.score,
			Strategy:    "exact",
			MatchFields: maskToFieldNames(ds.mask),
		})
	}
	return results
}

func (e *Engine) searchHybrid(query string, candidateDocIDs []uint32, limit int, threshold float32) []Result {
	bm25Results := e.searchBM25(query, candidateDocIDs, len(candidateDocIDs), 0.0)
	exactResults := e.searchExact(query, candidateDocIDs, len(candidateDocIDs), 0.0)

	rrfScores := make(map[string]float32)
	matchMasks := make(map[string]uint8)
	itemMap := make(map[string]catalog.Item)

	// RRF contribution from BM25
	for rank, res := range bm25Results {
		id := res.Item.ID
		if id == "" {
			id = res.Item.Name
		}
		rrfScores[id] += 1.0 / (DefaultRRFK + float32(rank+1))
		itemMap[id] = res.Item
		matchMasks[id] |= fieldNamesToMask(res.MatchFields)
	}

	// RRF contribution from Exact
	for rank, res := range exactResults {
		id := res.Item.ID
		if id == "" {
			id = res.Item.Name
		}
		rrfScores[id] += 1.0 / (DefaultRRFK + float32(rank+1))
		itemMap[id] = res.Item
		matchMasks[id] |= fieldNamesToMask(res.MatchFields)
	}

	normQuery := normalizeKey(query)
	compQuery := compactKey(query)
	queryNorm := strings.ToLower(strings.TrimSpace(query))

	// Exact, Normalized, and Edit Distance boosts
	for id, item := range itemMap {
		normName := normalizeKey(item.Name)
		compName := compactKey(item.Name)
		normID := normalizeKey(item.ID)
		compID := compactKey(item.ID)
		normTitle := normalizeKey(item.Title)
		compTitle := compactKey(item.Title)

		isExact := false
		// 1. Literal, Normalized, or Compact Exact Match on Name/ID/Title
		if strings.EqualFold(item.Name, queryNorm) || strings.EqualFold(item.ID, queryNorm) || strings.EqualFold(item.Title, queryNorm) ||
			(normQuery != "" && (normName == normQuery || normID == normQuery || normTitle == normQuery)) ||
			(compQuery != "" && (compName == compQuery || compID == compQuery || compTitle == compQuery)) {
			rrfScores[id] += 2.0 // Top boost for exact identity match
			matchMasks[id] |= FieldMaskName
			isExact = true
		} else if normQuery != "" && (strings.Contains(normName, normQuery) || strings.Contains(normTitle, normQuery)) {
			rrfScores[id] += 1.2
			matchMasks[id] |= FieldMaskName
		} else {
			// Fuzzy match (edit distance 1 or 2)
			dist := Levenshtein(normQuery, normName)
			distComp := Levenshtein(compQuery, compName)
			minDist := dist
			if distComp < minDist {
				minDist = distComp
			}
			if minDist == 1 && len(compQuery) >= 3 {
				rrfScores[id] += 1.6 // 1 edit distance away
				matchMasks[id] |= FieldMaskName
			} else if minDist == 2 && len(compQuery) >= 4 {
				rrfScores[id] += 1.0 // 2 edit distance away
				matchMasks[id] |= FieldMaskName
			}
		}

		// Tags exact / normalized match
		for _, tag := range item.Tags {
			normTag := normalizeKey(tag)
			compTag := compactKey(tag)
			if strings.EqualFold(tag, queryNorm) || (normQuery != "" && normTag == normQuery) || (compQuery != "" && compTag == compQuery) {
				if !isExact {
					rrfScores[id] += 0.8
				}
				matchMasks[id] |= FieldMaskTags
				break
			}
		}
	}

	type scoredItem struct {
		id    string
		score float32
	}

	var ranked []scoredItem
	for id, score := range rrfScores {
		normScore := normalizeRRFScore(score)
		if normScore >= threshold {
			ranked = append(ranked, scoredItem{id: id, score: normScore})
		}
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	results := make([]Result, 0, len(ranked))
	for _, entry := range ranked {
		item := itemMap[entry.id]
		mask := matchMasks[entry.id]
		results = append(results, Result{
			Item:        item,
			Score:       entry.score,
			Strategy:    "hybrid",
			MatchFields: maskToFieldNames(mask),
		})
	}
	return results
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
	if mask&FieldMaskCategory != 0 {
		fields = append(fields, "category")
	}
	if mask&FieldMaskDescription != 0 {
		fields = append(fields, "description")
	}
	if mask&FieldMaskOther != 0 {
		fields = append(fields, "other")
	}
	return fields
}

func fieldNamesToMask(fields []string) uint8 {
	var mask uint8
	for _, f := range fields {
		switch f {
		case "name":
			mask |= FieldMaskName
		case "title":
			mask |= FieldMaskTitle
		case "tags":
			mask |= FieldMaskTags
		case "category":
			mask |= FieldMaskCategory
		case "description":
			mask |= FieldMaskDescription
		case "other":
			mask |= FieldMaskOther
		}
	}
	return mask
}
