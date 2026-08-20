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
	"math"
)

// TFIDFScorer calculates vector space TF-IDF scores with sublinear term-frequency scaling
// and cosine similarity vector normalization.
type TFIDFScorer struct {
	numDocs  int
	docNorms []float32
	idfCache map[uint32]float32
}

// NewTFIDFScorer creates a new TFIDFScorer.
func NewTFIDFScorer() *TFIDFScorer {
	return &TFIDFScorer{
		idfCache: make(map[uint32]float32),
	}
}

// SublinearTF calculates sublinear term frequency: 1 + ln(tf) if tf > 0, else 0.
func SublinearTF(tf float32) float32 {
	if tf <= 0 {
		return 0
	}
	return float32(1.0 + math.Log(float64(tf)))
}

// ComputeTFIDFIDF computes smooth non-negative IDF for TF-IDF: ln(1 + N / n(q)).
func ComputeTFIDFIDF(docFreq, totalDocs int) float32 {
	if totalDocs <= 0 || docFreq <= 0 {
		return 0
	}
	n := float64(docFreq)
	N := float64(totalDocs)
	if n > N {
		n = N
	}
	val := math.Log(1.0 + (N / n))
	if val < 0 {
		return 0
	}
	return float32(val)
}

// Build precomputes document norms and caches IDFs.
func (s *TFIDFScorer) Build(numDocs int, docTermFreqs []map[uint32]float32, docFreqs map[uint32]uint32) {
	s.numDocs = numDocs
	s.docNorms = make([]float32, numDocs)
	s.idfCache = make(map[uint32]float32, len(docFreqs))

	for termID, df := range docFreqs {
		s.idfCache[termID] = ComputeTFIDFIDF(int(df), numDocs)
	}

	for docID := 0; docID < numDocs; docID++ {
		if docID >= len(docTermFreqs) {
			continue
		}
		var sumSq float64
		for termID, tf := range docTermFreqs[docID] {
			if tf <= 0 {
				continue
			}
			idf := float64(s.idfCache[termID])
			weight := float64(SublinearTF(tf)) * idf
			sumSq += weight * weight
		}
		if sumSq > 0 {
			s.docNorms[docID] = float32(math.Sqrt(sumSq))
		} else {
			s.docNorms[docID] = 1.0
		}
	}
}

// Score computes the cosine similarity TF-IDF score for a document against a query.
func (s *TFIDFScorer) Score(docID uint32, queryTFs map[uint32]float32, docTFs map[uint32]float32) float32 {
	if int(docID) >= len(s.docNorms) || len(queryTFs) == 0 || len(docTFs) == 0 {
		return 0
	}

	docNorm := s.docNorms[docID]
	if docNorm <= 0 {
		return 0
	}

	var dotProduct float64
	var querySumSq float64

	for termID, qTF := range queryTFs {
		if qTF <= 0 {
			continue
		}
		idf := float64(s.idfCache[termID])
		qWeight := float64(SublinearTF(qTF)) * idf
		querySumSq += qWeight * qWeight

		if dTF, ok := docTFs[termID]; ok && dTF > 0 {
			dWeight := float64(SublinearTF(dTF)) * idf
			dotProduct += qWeight * dWeight
		}
	}

	if dotProduct <= 0 || querySumSq <= 0 {
		return 0
	}

	queryNorm := float32(math.Sqrt(querySumSq))
	return float32(dotProduct) / (docNorm * queryNorm)
}
