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

import "math"

const (
	// DefaultBM25K1 controls term frequency saturation.
	DefaultBM25K1 = 1.2
	// DefaultBM25B controls document length normalization penalty.
	DefaultBM25B = 0.75
)

// BM25Scorer calculates Okapi BM25 scores with Lucene Robertson-Spärck Jones non-negative IDF
// and precomputed document length normalization tables.
type BM25Scorer struct {
	k1        float32
	b         float32
	normTable []float32
	idfCache  map[uint32]float32
}

// NewBM25Scorer initializes a BM25 scorer with validated k1 and b parameters.
func NewBM25Scorer(k1, b float32) *BM25Scorer {
	if math.IsNaN(float64(k1)) || math.IsInf(float64(k1), 0) || k1 <= 0 {
		k1 = DefaultBM25K1
	}
	if math.IsNaN(float64(b)) || math.IsInf(float64(b), 0) || b < 0 || b > 1 {
		b = DefaultBM25B
	}
	return &BM25Scorer{
		k1:       k1,
		b:        b,
		idfCache: make(map[uint32]float32),
	}
}

// ComputeIDF computes the Lucene Robertson-Spärck Jones non-negative IDF:
// ln(1 + (N - n(q) + 0.5) / (n(q) + 0.5))
func ComputeIDF(docFreq, totalDocs int) float32 {
	if totalDocs <= 0 || docFreq <= 0 {
		return 0
	}
	n := float64(docFreq)
	N := float64(totalDocs)
	if n > N {
		n = N
	}
	val := math.Log(1.0 + (N-n+0.5)/(n+0.5))
	if val < 0 {
		return 0
	}
	return float32(val)
}

// Build precomputes document length normalizations and caches IDFs for the given corpus statistics.
func (s *BM25Scorer) Build(numDocs int, avgDocLen float32, docLengths []float32, docFreqs map[uint32]uint32) {
	if numDocs <= 0 {
		s.normTable = nil
		s.idfCache = make(map[uint32]float32)
		return
	}
	s.normTable = make([]float32, numDocs)
	s.idfCache = make(map[uint32]float32, len(docFreqs))

	if avgDocLen <= 0 {
		avgDocLen = 1.0
	}

	for i := range numDocs {
		docLen := float32(0)
		if i < len(docLengths) {
			docLen = docLengths[i]
		}
		s.normTable[i] = (1.0 - s.b) + s.b*(docLen/avgDocLen)
	}

	for termID, df := range docFreqs {
		s.idfCache[termID] = ComputeIDF(int(df), numDocs)
	}
}

// ScoreTerm computes the BM25 contribution for a single term in a document.
func (s *BM25Scorer) ScoreTerm(termID uint32, tf float32, docID uint32) float32 {
	if tf <= 0 || uint64(docID) >= uint64(len(s.normTable)) {
		return 0
	}

	idf, ok := s.idfCache[termID]
	if !ok {
		return 0
	}

	norm := s.normTable[docID]
	num := tf * (s.k1 + 1.0)
	den := tf + s.k1*norm
	if den <= 0 {
		return 0
	}

	return idf * (num / den)
}

// Score computes the cumulative BM25 score for a document given unique query term IDs.
func (s *BM25Scorer) Score(docID uint32, queryTermIDs []uint32, termWeights []float32, docTFs map[uint32]float32) float32 {
	var total float32
	for i, termID := range queryTermIDs {
		tf, ok := docTFs[termID]
		if !ok || tf <= 0 {
			continue
		}
		weight := float32(1.0)
		if i < len(termWeights) && termWeights[i] > 0 {
			weight = termWeights[i]
		}
		total += weight * s.ScoreTerm(termID, tf, docID)
	}
	return total
}
