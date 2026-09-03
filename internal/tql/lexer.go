package tql

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TokenKind classifies one lexeme.
type TokenKind int

const (
	TEOF TokenKind = iota
	TIdent          // bare word: a keyword, collection/field/edge-type name, or bare key
	TNumber         // 123, -4.5
	TString         // "quoted text" — Text holds the UNESCAPED value
	TJSON           // a whole {...} object — Text holds the raw JSON text
	TSlash          // /
	TEq             // =
	TNeq            // !=
	TLt             // <
	TLte            // <=
	TGt             // >
	TGte            // >=
	TMinus          // -
	TArrow          // ->
	TLBracket       // [
	TRBracket       // ]
	TComma          // ,
)

// Token is one lexeme. Pos is a byte offset into the source, for error
// messages.
type Token struct {
	Kind TokenKind
	Text string
	Pos  int
}

func (t Token) String() string {
	if t.Kind == TEOF {
		return "end of input"
	}
	return fmt.Sprintf("%q", t.Text)
}

type lexer struct {
	src string
	pos int
}

// tokenize turns TQL source into a token stream ending in a single TEOF
// token. It never returns fewer than one token.
func tokenize(src string) ([]Token, error) {
	l := &lexer{src: src}
	var toks []Token
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.Kind == TEOF {
			return toks, nil
		}
	}
}

func (l *lexer) peekByte(offset int) byte {
	i := l.pos + offset
	if i < 0 || i >= len(l.src) {
		return 0
	}
	return l.src[i]
}

func (l *lexer) skipSpace() {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t', '\r', '\n':
			l.pos++
		default:
			return
		}
	}
}

func (l *lexer) next() (Token, error) {
	l.skipSpace()
	if l.pos >= len(l.src) {
		return Token{Kind: TEOF, Pos: l.pos}, nil
	}

	start := l.pos
	c := l.src[l.pos]

	switch {
	case c == '"':
		return l.lexString()
	case c == '{':
		return l.lexJSON()
	case c == '/':
		l.pos++
		return Token{Kind: TSlash, Text: "/", Pos: start}, nil
	case c == '[':
		l.pos++
		return Token{Kind: TLBracket, Text: "[", Pos: start}, nil
	case c == ']':
		l.pos++
		return Token{Kind: TRBracket, Text: "]", Pos: start}, nil
	case c == ',':
		l.pos++
		return Token{Kind: TComma, Text: ",", Pos: start}, nil
	case c == '=':
		l.pos++
		return Token{Kind: TEq, Text: "=", Pos: start}, nil
	case c == '!':
		if l.peekByte(1) == '=' {
			l.pos += 2
			return Token{Kind: TNeq, Text: "!=", Pos: start}, nil
		}
		return Token{}, fmt.Errorf("tql: unexpected %q at position %d (did you mean !=?)", c, start)
	case c == '<':
		if l.peekByte(1) == '=' {
			l.pos += 2
			return Token{Kind: TLte, Text: "<=", Pos: start}, nil
		}
		l.pos++
		return Token{Kind: TLt, Text: "<", Pos: start}, nil
	case c == '>':
		if l.peekByte(1) == '=' {
			l.pos += 2
			return Token{Kind: TGte, Text: ">=", Pos: start}, nil
		}
		l.pos++
		return Token{Kind: TGt, Text: ">", Pos: start}, nil
	case c == '-':
		if l.peekByte(1) == '>' {
			l.pos += 2
			return Token{Kind: TArrow, Text: "->", Pos: start}, nil
		}
		if isDigit(l.peekByte(1)) {
			return l.lexNumber()
		}
		l.pos++
		return Token{Kind: TMinus, Text: "-", Pos: start}, nil
	case isDigit(c):
		return l.lexNumber()
	case isIdentStart(c):
		return l.lexIdent()
	default:
		return Token{}, fmt.Errorf("tql: unexpected character %q at position %d", c, start)
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// isIdentCont deliberately excludes '-': a bare identifier never contains a
// hyphen, so RELATE's "-<edge-type>->" arrow syntax is never ambiguous with
// a hyphenated name. A key that needs a hyphen (a UUID, say) is written as
// a quoted string instead — see parsePath.
func isIdentCont(c byte) bool {
	return isIdentStart(c) || isDigit(c) || c == '.'
}

func (l *lexer) lexIdent() (Token, error) {
	start := l.pos
	for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
		l.pos++
	}
	return Token{Kind: TIdent, Text: l.src[start:l.pos], Pos: start}, nil
}

func (l *lexer) lexNumber() (Token, error) {
	start := l.pos
	if l.src[l.pos] == '-' {
		l.pos++
	}
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' && isDigit(l.peekByte(1)) {
		l.pos++
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	return Token{Kind: TNumber, Text: l.src[start:l.pos], Pos: start}, nil
}

// lexString scans a JSON-escaped quoted string and unescapes it via
// encoding/json, so a TQL string literal is exactly a JSON string literal
// (same \", \\, \n, \uXXXX rules) rather than a second, hand-rolled dialect.
func (l *lexer) lexString() (Token, error) {
	start := l.pos
	l.pos++ // opening quote
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\\':
			if l.pos+1 >= len(l.src) {
				l.pos++
				continue
			}
			l.pos += 2
		case '"':
			l.pos++
			raw := l.src[start:l.pos]
			var s string
			if err := json.Unmarshal([]byte(raw), &s); err != nil {
				return Token{}, fmt.Errorf("tql: invalid string literal at position %d: %w", start, err)
			}
			return Token{Kind: TString, Text: s, Pos: start}, nil
		default:
			l.pos++
		}
	}
	return Token{}, fmt.Errorf("tql: unterminated string starting at position %d", start)
}

// lexJSON scans exactly one JSON value starting at '{', using the stdlib
// decoder to find the boundary rather than hand-rolling brace/quote
// matching — json.Decoder.Decode consumes exactly one value and
// InputOffset reports how much of the input that took.
func (l *lexer) lexJSON() (Token, error) {
	start := l.pos
	dec := json.NewDecoder(strings.NewReader(l.src[l.pos:]))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return Token{}, fmt.Errorf("tql: invalid JSON value at position %d: %w", start, err)
	}
	l.pos += int(dec.InputOffset())
	return Token{Kind: TJSON, Text: string(raw), Pos: start}, nil
}
