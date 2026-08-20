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

package catalog

import (
	"strings"
	"unicode"
)

// TokenType represents the lexical token type in a tag query expression.
type tokenType int

const (
	tokTag tokenType = iota
	tokAnd
	tokOr
	tokNot
	tokLParen
	tokRParen
	tokEOF
)

type tagToken struct {
	typ tokenType
	val string
}

// ParseTags extracts a clean slice of lowercase tags from a comma- or space-separated string.
func ParseTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	// Split by commas, semicolons, or newlines first.
	splitFn := func(c rune) bool {
		return c == ',' || c == ';' || c == '\n' || c == '\r'
	}
	parts := strings.FieldsFunc(raw, splitFn)
	var tags []string
	seen := make(map[string]bool)
	for _, p := range parts {
		cleaned := strings.ToLower(strings.TrimSpace(p))
		if cleaned != "" && !seen[cleaned] {
			seen[cleaned] = true
			tags = append(tags, cleaned)
		}
	}
	return tags
}

// MatchTags evaluates whether a set of item tags satisfies a boolean tag expression query.
// Supports AND (or +, space), OR (or ,), NOT (or -), parentheses, and case-insensitive matching.
func MatchTags(itemTags []string, query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return true
	}

	tagSet := make(map[string]bool, len(itemTags))
	for _, t := range itemTags {
		clean := strings.ToLower(strings.TrimSpace(t))
		if clean != "" {
			tagSet[clean] = true
		}
	}

	tokens := tokenizeTagQuery(q)
	if len(tokens) == 0 {
		return true
	}

	parser := &tagParser{
		tokens: tokens,
		pos:    0,
		tagSet: tagSet,
	}

	return parser.parseExpr()
}

func tokenizeTagQuery(query string) []tagToken {
	var tokens []tagToken
	runes := []rune(query)
	n := len(runes)
	i := 0

	for i < n {
		for i < n && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= n {
			break
		}

		ch := runes[i]
		switch ch {
		case '(':
			tokens = append(tokens, tagToken{typ: tokLParen, val: "("})
			i++
		case ')':
			tokens = append(tokens, tagToken{typ: tokRParen, val: ")"})
			i++
		case ',', '|':
			tokens = append(tokens, tagToken{typ: tokOr, val: "OR"})
			i++
		case '+':
			tokens = append(tokens, tagToken{typ: tokAnd, val: "AND"})
			i++
		case '!':
			tokens = append(tokens, tagToken{typ: tokNot, val: "NOT"})
			i++
		case '-':
			// If followed immediately by a word char, it's a negative tag like -3d
			if i+1 < n && !unicode.IsSpace(runes[i+1]) && runes[i+1] != '(' {
				tokens = append(tokens, tagToken{typ: tokNot, val: "NOT"})
				i++
			} else {
				i++
			}
		default:
			start := i
			for i < n && !unicode.IsSpace(runes[i]) && runes[i] != '(' && runes[i] != ')' && runes[i] != ',' && runes[i] != '|' && runes[i] != '+' {
				i++
			}
			word := string(runes[start:i])
			upper := strings.ToUpper(word)
			switch upper {
			case "AND", "&&":
				tokens = append(tokens, tagToken{typ: tokAnd, val: "AND"})
			case "OR", "||":
				tokens = append(tokens, tagToken{typ: tokOr, val: "OR"})
			case "NOT":
				tokens = append(tokens, tagToken{typ: tokNot, val: "NOT"})
			default:
				tokens = append(tokens, tagToken{typ: tokTag, val: strings.ToLower(word)})
			}
		}
	}

	// Insert implicit AND between consecutive tags, e.g. "gamedev ebiten" -> "gamedev AND ebiten"
	var out []tagToken
	for idx := 0; idx < len(tokens); idx++ {
		out = append(out, tokens[idx])
		if idx+1 < len(tokens) {
			curr := tokens[idx]
			next := tokens[idx+1]
			if (curr.typ == tokTag || curr.typ == tokRParen) &&
				(next.typ == tokTag || next.typ == tokLParen || next.typ == tokNot) {
				out = append(out, tagToken{typ: tokAnd, val: "AND"})
			}
		}
	}

	out = append(out, tagToken{typ: tokEOF, val: ""})
	return out
}

type tagParser struct {
	tokens []tagToken
	pos    int
	tagSet map[string]bool
}

func (p *tagParser) peek() tagToken {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return tagToken{typ: tokEOF}
}

func (p *tagParser) next() tagToken {
	t := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return t
}

func (p *tagParser) parseExpr() bool {
	return p.parseOr()
}

func (p *tagParser) parseOr() bool {
	res := p.parseAnd()
	for p.peek().typ == tokOr {
		p.next() // consume OR
		rhs := p.parseAnd()
		res = res || rhs
	}
	return res
}

func (p *tagParser) parseAnd() bool {
	res := p.parseNot()
	for p.peek().typ == tokAnd {
		p.next() // consume AND
		rhs := p.parseNot()
		res = res && rhs
	}
	return res
}

func (p *tagParser) parseNot() bool {
	if p.peek().typ == tokNot {
		p.next() // consume NOT
		return !p.parseNot()
	}
	return p.parsePrimary()
}

func (p *tagParser) parsePrimary() bool {
	tok := p.next()
	switch tok.typ {
	case tokTag:
		// Check exact tag match or tag prefix/suffix match
		if p.tagSet[tok.val] {
			return true
		}
		// Also match if tag is a substring or vice versa
		for t := range p.tagSet {
			if t == tok.val || strings.EqualFold(t, tok.val) {
				return true
			}
		}
		return false
	case tokLParen:
		val := p.parseExpr()
		if p.peek().typ == tokRParen {
			p.next() // consume ')'
		}
		return val
	default:
		return false
	}
}
