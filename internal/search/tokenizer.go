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
	"sync"
	"unicode"
	"unicode/utf8"
)

// DefaultStopwords contains standard common English stopwords.
var DefaultStopwords = map[string]struct{}{
	"a": {}, "about": {}, "above": {}, "after": {}, "again": {}, "against": {}, "all": {},
	"am": {}, "an": {}, "and": {}, "any": {}, "are": {}, "aren't": {}, "as": {}, "at": {},
	"be": {}, "because": {}, "been": {}, "before": {}, "being": {}, "below": {}, "between": {},
	"both": {}, "but": {}, "by": {}, "can't": {}, "cannot": {}, "could": {}, "couldn't": {},
	"did": {}, "didn't": {}, "do": {}, "does": {}, "doesn't": {}, "doing": {}, "don't": {},
	"down": {}, "during": {}, "each": {}, "few": {}, "for": {}, "from": {}, "further": {},
	"had": {}, "hadn't": {}, "has": {}, "hasn't": {}, "have": {}, "haven't": {}, "having": {},
	"he": {}, "he'd": {}, "he'll": {}, "he's": {}, "her": {}, "here": {}, "here's": {},
	"hers": {}, "herself": {}, "him": {}, "himself": {}, "his": {}, "how": {}, "how's": {},
	"i": {}, "i'd": {}, "i'll": {}, "i'm": {}, "i've": {}, "if": {}, "in": {}, "into": {},
	"is": {}, "isn't": {}, "it": {}, "it's": {}, "its": {}, "itself": {}, "let's": {},
	"me": {}, "more": {}, "most": {}, "mustn't": {}, "my": {}, "myself": {}, "no": {},
	"nor": {}, "not": {}, "of": {}, "off": {}, "on": {}, "once": {}, "only": {}, "or": {},
	"other": {}, "ought": {}, "our": {}, "ours": {}, "ourselves": {}, "out": {}, "over": {},
	"own": {}, "same": {}, "shan't": {}, "she": {}, "she'd": {}, "she'll": {}, "she's": {},
	"should": {}, "shouldn't": {}, "so": {}, "some": {}, "such": {}, "than": {}, "that": {},
	"that's": {}, "the": {}, "their": {}, "theirs": {}, "them": {}, "themselves": {}, "then": {},
	"there": {}, "there's": {}, "these": {}, "they": {}, "they'd": {}, "they'll": {}, "they're": {},
	"they've": {}, "this": {}, "those": {}, "through": {}, "to": {}, "too": {}, "under": {},
	"until": {}, "up": {}, "very": {}, "was": {}, "wasn't": {}, "we": {}, "we'd": {},
	"we'll": {}, "we're": {}, "we've": {}, "were": {}, "weren't": {}, "what": {}, "what's": {},
	"when": {}, "when's": {}, "where": {}, "where's": {}, "which": {}, "while": {}, "who": {},
	"who's": {}, "whom": {}, "why": {}, "why's": {}, "with": {}, "won't": {}, "would": {},
	"wouldn't": {}, "you": {}, "you'd": {}, "you'll": {}, "you're": {}, "you've": {}, "your": {},
	"yours": {}, "yourself": {}, "yourselves": {},
}

var (
	byteBufPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 256)
			return &b
		},
	}
)

// Tokenizer provides high-throughput ASCII fast-path and UTF-8 tokenization,
// stopword elimination, and algorithmic Porter stemming.
type Tokenizer struct {
	stopwords map[string]struct{}
}

// NewTokenizer creates a Tokenizer initialized with standard English stopwords.
func NewTokenizer() *Tokenizer {
	return &Tokenizer{
		stopwords: DefaultStopwords,
	}
}

// SetStopwords overrides the active stopword dictionary.
func (t *Tokenizer) SetStopwords(words map[string]struct{}) {
	t.stopwords = words
}

// IsStopword checks if a word is in the active stopword filter.
func (t *Tokenizer) IsStopword(word string) bool {
	if t.stopwords == nil {
		return false
	}
	_, ok := t.stopwords[word]
	return ok
}

// Tokenize converts text into normalized, stemmed tokens with stopwords removed.
func (t *Tokenizer) Tokenize(text string) []string {
	raw := t.scan(text)
	if len(raw) == 0 {
		return nil
	}

	result := make([]string, 0, len(raw))
	for _, tok := range raw {
		if !t.IsStopword(tok) {
			stemmed := t.Stem(tok)
			if len(stemmed) > 0 {
				result = append(result, stemmed)
			}
		}
	}
	return result
}

// TokenizeRaw extracts normalized lowercase tokens without stopword removal or stemming.
func (t *Tokenizer) TokenizeRaw(text string) []string {
	return t.scan(text)
}

// scan performs high-throughput ASCII fast-path tokenization with UTF-8 fallback.
func (t *Tokenizer) scan(text string) []string {
	if len(text) == 0 {
		return nil
	}

	tokens := make([]string, 0, 16)
	n := len(text)
	i := 0

	bufPtr := byteBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	defer func() {
		*bufPtr = buf[:0]
		byteBufPool.Put(bufPtr)
	}()

	for i < n {
		b := text[i]

		// Fast-path: ASCII alphanumeric
		if b < 0x80 {
			if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') {
				buf = append(buf, b)
				i++
				continue
			}
			if b >= 'A' && b <= 'Z' {
				if len(buf) > 0 {
					prev := text[i-1]
					if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
						tokens = append(tokens, string(buf))
						buf = buf[:0]
					} else if len(buf) > 1 && i+1 < n && text[i+1] >= 'a' && text[i+1] <= 'z' {
						tokens = append(tokens, string(buf))
						buf = buf[:0]
					}
				}
				buf = append(buf, b+32)
				i++
				continue
			}

			// Boundary hit on ASCII non-alphanumeric
			if len(buf) > 0 {
				tokens = append(tokens, string(buf))
				buf = buf[:0]
			}
			i++
			continue
		}

		// Fallback: UTF-8 multibyte rune decoding
		r, size := utf8.DecodeRuneInString(text[i:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			low := unicode.ToLower(r)
			var rBuf [utf8.UTFMax]byte
			rlen := utf8.EncodeRune(rBuf[:], low)
			buf = append(buf, rBuf[:rlen]...)
		} else {
			if len(buf) > 0 {
				tokens = append(tokens, string(buf))
				buf = buf[:0]
			}
		}
		i += size
	}

	if len(buf) > 0 {
		tokens = append(tokens, string(buf))
	}

	return tokens
}

// Stem applies Martin Porter's 1980 stemming algorithm to an English word.
func (t *Tokenizer) Stem(word string) string {
	if len(word) <= 2 {
		return word
	}

	r := []rune(word)
	stemmer := &porterStemmer{b: r, k: len(r) - 1, k0: 0}
	return string(stemmer.stem())
}

type porterStemmer struct {
	b  []rune
	k  int
	k0 int
	j  int
}

func (s *porterStemmer) isConsonant(i int) bool {
	switch s.b[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	case 'y':
		if i == s.k0 {
			return true
		}
		return !s.isConsonant(i - 1)
	default:
		return true
	}
}

func (s *porterStemmer) measure() int {
	n := 0
	i := s.k0
	for {
		if i > s.j {
			return n
		}
		if !s.isConsonant(i) {
			break
		}
		i++
	}
	i++
	for {
		for {
			if i > s.j {
				return n
			}
			if s.isConsonant(i) {
				break
			}
			i++
		}
		i++
		n++
		for {
			if i > s.j {
				return n
			}
			if !s.isConsonant(i) {
				break
			}
			i++
		}
		i++
	}
}

func (s *porterStemmer) vowelInStem() bool {
	for i := s.k0; i <= s.j; i++ {
		if !s.isConsonant(i) {
			return true
		}
	}
	return false
}

func (s *porterStemmer) doubleConsonant(i int) bool {
	if i < s.k0+1 {
		return false
	}
	if s.b[i] != s.b[i-1] {
		return false
	}
	return s.isConsonant(i)
}

func (s *porterStemmer) cvc(i int) bool {
	if i < s.k0+2 || !s.isConsonant(i) || s.isConsonant(i-1) || !s.isConsonant(i-2) {
		return false
	}
	ch := s.b[i]
	if ch == 'w' || ch == 'x' || ch == 'y' {
		return false
	}
	return true
}

func (s *porterStemmer) ends(suffix string) bool {
	sufRunes := []rune(suffix)
	l := len(sufRunes)
	o := s.k - l + 1
	if o < s.k0 {
		return false
	}
	for i := 0; i < l; i++ {
		if s.b[o+i] != sufRunes[i] {
			return false
		}
	}
	s.j = s.k - l
	return true
}

func (s *porterStemmer) setto(suffix string) {
	sufRunes := []rune(suffix)
	l := len(sufRunes)
	o := s.j + 1
	for i := 0; i < l; i++ {
		if o+i < len(s.b) {
			s.b[o+i] = sufRunes[i]
		} else {
			s.b = append(s.b, sufRunes[i])
		}
	}
	s.k = s.j + l
	s.b = s.b[:s.k+1]
}

func (s *porterStemmer) r(suffix string) {
	if s.measure() > 0 {
		s.setto(suffix)
	}
}

func (s *porterStemmer) step1ab() {
	if s.b[s.k] == 's' {
		if s.ends("sses") {
			s.k -= 2
		} else if s.ends("ies") {
			s.setto("i")
		} else if s.k > s.k0 && s.b[s.k-1] != 's' {
			s.k--
		}
	}
	if s.ends("eed") {
		if s.measure() > 0 {
			s.k--
		}
	} else if (s.ends("ed") || s.ends("ing")) && s.vowelInStem() {
		s.k = s.j
		if s.ends("at") {
			s.setto("ate")
		} else if s.ends("bl") {
			s.setto("ble")
		} else if s.ends("iz") {
			s.setto("ize")
		} else if s.doubleConsonant(s.k) {
			s.k--
			ch := s.b[s.k]
			if ch == 'l' || ch == 's' || ch == 'z' {
				s.k++
			}
		} else if s.measure() == 1 && s.cvc(s.k) {
			s.setto("e")
		}
	}
}

func (s *porterStemmer) step1c() {
	if s.ends("y") && s.vowelInStem() {
		s.b[s.k] = 'i'
	}
}

func (s *porterStemmer) step2() {
	if s.k <= s.k0 {
		return
	}
	switch s.b[s.k-1] {
	case 'a':
		if s.ends("ational") {
			s.r("ate")
		} else if s.ends("tional") {
			s.r("tion")
		}
	case 'c':
		if s.ends("enci") {
			s.r("ence")
		} else if s.ends("anci") {
			s.r("ance")
		}
	case 'e':
		if s.ends("izer") {
			s.r("ize")
		}
	case 'l':
		if s.ends("bli") {
			s.r("ble")
		} else if s.ends("alli") {
			s.r("al")
		} else if s.ends("entli") {
			s.r("ent")
		} else if s.ends("eli") {
			s.r("e")
		} else if s.ends("ousli") {
			s.r("ous")
		}
	case 'o':
		if s.ends("ization") {
			s.r("ize")
		} else if s.ends("ation") {
			s.r("ate")
		} else if s.ends("ator") {
			s.r("ate")
		}
	case 's':
		if s.ends("alism") {
			s.r("al")
		} else if s.ends("iveness") {
			s.r("ive")
		} else if s.ends("fulness") {
			s.r("ful")
		} else if s.ends("ousness") {
			s.r("ous")
		}
	case 't':
		if s.ends("aliti") {
			s.r("al")
		} else if s.ends("iviti") {
			s.r("ive")
		} else if s.ends("biliti") {
			s.r("ble")
		}
	case 'g':
		if s.ends("logi") {
			s.r("log")
		}
	}
}

func (s *porterStemmer) step3() {
	switch s.b[s.k] {
	case 'e':
		if s.ends("icate") {
			s.r("ic")
		} else if s.ends("ative") {
			s.r("")
		} else if s.ends("alize") {
			s.r("al")
		}
	case 'i':
		if s.ends("iciti") {
			s.r("ic")
		}
	case 'l':
		if s.ends("ical") {
			s.r("ic")
		} else if s.ends("ful") {
			s.r("")
		}
	case 's':
		if s.ends("ness") {
			s.r("")
		}
	}
}

func (s *porterStemmer) step4() {
	if s.k <= s.k0 {
		return
	}
	switch s.b[s.k-1] {
	case 'a':
		if !s.ends("al") {
			return
		}
	case 'c':
		if !s.ends("ance") && !s.ends("ence") {
			return
		}
	case 'e':
		if !s.ends("er") {
			return
		}
	case 'i':
		if !s.ends("ic") {
			return
		}
	case 'l':
		if !s.ends("able") && !s.ends("ible") {
			return
		}
	case 'n':
		if !s.ends("ant") && !s.ends("ement") && !s.ends("ment") && !s.ends("ent") {
			return
		}
	case 'o':
		if s.ends("ion") && s.j >= s.k0 && (s.b[s.j] == 's' || s.b[s.j] == 't') {
		} else if s.ends("ou") {
		} else {
			return
		}
	case 's':
		if !s.ends("ism") {
			return
		}
	case 't':
		if !s.ends("ate") && !s.ends("iti") {
			return
		}
	case 'u':
		if !s.ends("ous") {
			return
		}
	case 'v':
		if !s.ends("ive") {
			return
		}
	case 'z':
		if !s.ends("ize") {
			return
		}
	default:
		return
	}
	if s.measure() > 1 {
		s.k = s.j
	}
}

func (s *porterStemmer) step5() {
	s.j = s.k
	if s.b[s.k] == 'e' {
		a := s.measure()
		if a > 1 || (a == 1 && !s.cvc(s.k-1)) {
			s.k--
		}
	}
	if s.b[s.k] == 'l' && s.doubleConsonant(s.k) && s.measure() > 1 {
		s.k--
	}
}

func (s *porterStemmer) stem() []rune {
	if s.k <= s.k0+1 {
		return s.b[:s.k+1]
	}
	s.step1ab()
	s.step1c()
	s.step2()
	s.step3()
	s.step4()
	s.step5()
	return s.b[:s.k+1]
}
