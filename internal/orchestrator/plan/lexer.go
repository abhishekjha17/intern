package plan

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokKind enumerates the lexical token kinds produced by the lexer.
type TokKind uint8

const (
	TokEOF TokKind = iota
	TokError

	TokIdent  // [a-zA-Z][a-zA-Z0-9_]*  — extended forms (model_id, tool_ref) are stitched by the parser
	TokString // "..." (escapes processed; ${...} preserved verbatim for runtime interp)
	TokNumber // [-]?[0-9]+(\.[0-9]+)?
	TokHex    // 0x[0-9a-fA-F]+
	TokVar    // $name (just $name; field chain is parsed as TokDot TokIdent ... by the parser)

	TokColEq    // :=
	TokArrow    // ->
	TokFatArrow // =>

	TokCmpOp // ==, !=, <=, >=, <, > (Str carries the op text)

	TokPipe     // |
	TokColon    // :
	TokComma    // ,
	TokEq       // =
	TokDot      // .
	TokAt       // @
	TokDash     // - (only emitted when not part of -> or a number prefix; primarily inside extended idents the parser sees as Ident Dash Ident)

	TokLBrace   // {
	TokRBrace   // }
	TokLBracket // [
	TokRBracket // ]
	TokLParen   // (
	TokRParen   // )
)

func (k TokKind) String() string {
	switch k {
	case TokEOF:
		return "EOF"
	case TokError:
		return "ERROR"
	case TokIdent:
		return "IDENT"
	case TokString:
		return "STRING"
	case TokNumber:
		return "NUMBER"
	case TokHex:
		return "HEX"
	case TokVar:
		return "VAR"
	case TokColEq:
		return ":="
	case TokArrow:
		return "->"
	case TokFatArrow:
		return "=>"
	case TokCmpOp:
		return "CMP_OP"
	case TokPipe:
		return "|"
	case TokColon:
		return ":"
	case TokComma:
		return ","
	case TokEq:
		return "="
	case TokDot:
		return "."
	case TokAt:
		return "@"
	case TokDash:
		return "-"
	case TokLBrace:
		return "{"
	case TokRBrace:
		return "}"
	case TokLBracket:
		return "["
	case TokRBracket:
		return "]"
	case TokLParen:
		return "("
	case TokRParen:
		return ")"
	}
	return "?"
}

// Token is one lexical unit. Str carries the literal value for IDENT, STRING,
// VAR, NUMBER (raw text), HEX (raw text); Num carries the parsed value for
// numeric kinds; Hex carries the parsed hex value.
type Token struct {
	Kind TokKind
	Str  string
	Num  float64
	Hex  uint64
	Span Span
}

// Lexer is a streaming tokenizer with up to 2-token lookahead. EOF is sticky.
type Lexer struct {
	src  string
	pos  int
	line int
	col  int

	// Lookahead queue. peeked[0] is the next token Next would return.
	peeked []Token

	// Last error (set when emitting a TokError).
	err *Error
}

// NewLexer creates a streaming lexer over src.
func NewLexer(src string) *Lexer {
	return &Lexer{src: src, line: 1, col: 1}
}

// Err returns the lexer error if Next produced a TokError, else nil.
func (l *Lexer) Err() *Error { return l.err }

// Peek returns the next token without consuming it.
func (l *Lexer) Peek() Token { return l.PeekN(0) }

// PeekN returns the token n positions ahead (n=0 means the next token,
// n=1 the one after that). The lookahead queue grows as needed; tokens
// already produced are reused for both Peek and Next.
func (l *Lexer) PeekN(n int) Token {
	for len(l.peeked) <= n {
		l.peeked = append(l.peeked, l.scan())
	}
	return l.peeked[n]
}

// Next consumes and returns the next token.
func (l *Lexer) Next() Token {
	if len(l.peeked) > 0 {
		t := l.peeked[0]
		l.peeked = l.peeked[1:]
		return t
	}
	return l.scan()
}

// scan produces the next token, advancing pos/line/col.
func (l *Lexer) scan() Token {
	l.skipWhitespaceAndComments()

	if l.pos >= len(l.src) {
		return Token{Kind: TokEOF, Span: l.spanAt(l.pos, l.pos)}
	}

	startPos, startLine, startCol := l.pos, l.line, l.col
	c := l.src[l.pos]

	// Multi-char operators first.
	if c == ':' && l.peekByte(1) == '=' {
		l.advance(2)
		return Token{Kind: TokColEq, Span: l.span(startPos, l.pos, startLine, startCol)}
	}
	if c == '-' && l.peekByte(1) == '>' {
		l.advance(2)
		return Token{Kind: TokArrow, Span: l.span(startPos, l.pos, startLine, startCol)}
	}
	if c == '=' && l.peekByte(1) == '>' {
		l.advance(2)
		return Token{Kind: TokFatArrow, Span: l.span(startPos, l.pos, startLine, startCol)}
	}
	if c == '=' && l.peekByte(1) == '=' {
		l.advance(2)
		return Token{Kind: TokCmpOp, Str: "==", Span: l.span(startPos, l.pos, startLine, startCol)}
	}
	if c == '!' && l.peekByte(1) == '=' {
		l.advance(2)
		return Token{Kind: TokCmpOp, Str: "!=", Span: l.span(startPos, l.pos, startLine, startCol)}
	}
	if c == '<' && l.peekByte(1) == '=' {
		l.advance(2)
		return Token{Kind: TokCmpOp, Str: "<=", Span: l.span(startPos, l.pos, startLine, startCol)}
	}
	if c == '>' && l.peekByte(1) == '=' {
		l.advance(2)
		return Token{Kind: TokCmpOp, Str: ">=", Span: l.span(startPos, l.pos, startLine, startCol)}
	}
	if c == '<' {
		l.advance(1)
		return Token{Kind: TokCmpOp, Str: "<", Span: l.span(startPos, l.pos, startLine, startCol)}
	}
	if c == '>' {
		l.advance(1)
		return Token{Kind: TokCmpOp, Str: ">", Span: l.span(startPos, l.pos, startLine, startCol)}
	}

	// Hex literal: 0x[0-9a-fA-F]+
	if c == '0' && l.peekByte(1) == 'x' {
		return l.scanHex(startPos, startLine, startCol)
	}

	// Number (digits, optional decimal). Negatives are produced only inside
	// the explicit "value" parser context; here a leading '-' is TokDash.
	if isDigit(c) {
		return l.scanNumber(startPos, startLine, startCol, false)
	}

	// String literal
	if c == '"' {
		return l.scanString(startPos, startLine, startCol)
	}

	// Variable reference: $ident
	if c == '$' {
		return l.scanVar(startPos, startLine, startCol)
	}

	// Identifier
	if isIdentStart(c) {
		return l.scanIdent(startPos, startLine, startCol)
	}

	// Single-byte punctuation
	switch c {
	case '|':
		l.advance(1)
		return Token{Kind: TokPipe, Span: l.span(startPos, l.pos, startLine, startCol)}
	case ':':
		l.advance(1)
		return Token{Kind: TokColon, Span: l.span(startPos, l.pos, startLine, startCol)}
	case ',':
		l.advance(1)
		return Token{Kind: TokComma, Span: l.span(startPos, l.pos, startLine, startCol)}
	case '=':
		l.advance(1)
		return Token{Kind: TokEq, Span: l.span(startPos, l.pos, startLine, startCol)}
	case '.':
		l.advance(1)
		return Token{Kind: TokDot, Span: l.span(startPos, l.pos, startLine, startCol)}
	case '@':
		l.advance(1)
		return Token{Kind: TokAt, Span: l.span(startPos, l.pos, startLine, startCol)}
	case '-':
		l.advance(1)
		return Token{Kind: TokDash, Span: l.span(startPos, l.pos, startLine, startCol)}
	case '{':
		l.advance(1)
		return Token{Kind: TokLBrace, Span: l.span(startPos, l.pos, startLine, startCol)}
	case '}':
		l.advance(1)
		return Token{Kind: TokRBrace, Span: l.span(startPos, l.pos, startLine, startCol)}
	case '[':
		l.advance(1)
		return Token{Kind: TokLBracket, Span: l.span(startPos, l.pos, startLine, startCol)}
	case ']':
		l.advance(1)
		return Token{Kind: TokRBracket, Span: l.span(startPos, l.pos, startLine, startCol)}
	case '(':
		l.advance(1)
		return Token{Kind: TokLParen, Span: l.span(startPos, l.pos, startLine, startCol)}
	case ')':
		l.advance(1)
		return Token{Kind: TokRParen, Span: l.span(startPos, l.pos, startLine, startCol)}
	}

	// Anything else is an error. Consume one rune so we don't loop forever.
	r, w := utf8.DecodeRuneInString(l.src[l.pos:])
	l.advance(w)
	return l.errTok(startPos, l.pos, startLine, startCol,
		fmt.Sprintf("unexpected character %q", r),
		"only ascii letters, digits, and the punctuation := -> => | : , = . @ - { } [ ] ( ) $ are valid here")
}

// scanIdent reads [a-zA-Z][a-zA-Z0-9_]*. Extended forms like `llama3-8b` or
// `Bash:client` are stitched by the parser from IDENT (DASH | COLON) IDENT...
// sequences in the small set of contexts that allow them (model_id, tool_ref).
func (l *Lexer) scanIdent(startPos, startLine, startCol int) Token {
	for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
		l.advance(1)
	}
	return Token{
		Kind: TokIdent,
		Str:  l.src[startPos:l.pos],
		Span: l.span(startPos, l.pos, startLine, startCol),
	}
}

// scanVar reads $ident. Field chain ($x.y.z) is left to the parser.
func (l *Lexer) scanVar(startPos, startLine, startCol int) Token {
	l.advance(1) // consume $
	if l.pos >= len(l.src) || !isIdentStart(l.src[l.pos]) {
		return l.errTok(startPos, l.pos, startLine, startCol,
			"expected identifier after '$'", "variable references look like $name or $name.field")
	}
	nameStart := l.pos
	for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
		l.advance(1)
	}
	return Token{
		Kind: TokVar,
		Str:  l.src[nameStart:l.pos], // name only; '$' is implicit
		Span: l.span(startPos, l.pos, startLine, startCol),
	}
}

// scanNumber reads an integer or decimal literal. negative is true when
// the caller already consumed a leading '-' (currently unused; kept for
// future use by the parser).
func (l *Lexer) scanNumber(startPos, startLine, startCol int, negative bool) Token {
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.advance(1)
	}
	// Optional decimal part.
	if l.pos < len(l.src) && l.src[l.pos] == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]) {
		l.advance(1) // consume '.'
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.advance(1)
		}
	}
	raw := l.src[startPos:l.pos]
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return l.errTok(startPos, l.pos, startLine, startCol,
			fmt.Sprintf("invalid number %q", raw), "")
	}
	if negative {
		v = -v
	}
	return Token{
		Kind: TokNumber,
		Str:  raw,
		Num:  v,
		Span: l.span(startPos, l.pos, startLine, startCol),
	}
}

// scanHex reads 0x[0-9a-fA-F]+.
func (l *Lexer) scanHex(startPos, startLine, startCol int) Token {
	l.advance(2) // consume 0x
	hexStart := l.pos
	for l.pos < len(l.src) && isHexDigit(l.src[l.pos]) {
		l.advance(1)
	}
	if l.pos == hexStart {
		return l.errTok(startPos, l.pos, startLine, startCol,
			"expected hex digits after '0x'", "")
	}
	raw := l.src[hexStart:l.pos]
	v, err := strconv.ParseUint(raw, 16, 64)
	if err != nil {
		return l.errTok(startPos, l.pos, startLine, startCol,
			fmt.Sprintf("invalid hex literal %q", "0x"+raw), "")
	}
	return Token{
		Kind: TokHex,
		Str:  l.src[startPos:l.pos],
		Hex:  v,
		Span: l.span(startPos, l.pos, startLine, startCol),
	}
}

// scanString reads "..." with escape sequences. ${...} sequences are NOT
// expanded here — they're preserved verbatim in the produced string for
// runtime interpolation. Recognized escapes: \" \\ \n \t \$.
func (l *Lexer) scanString(startPos, startLine, startCol int) Token {
	l.advance(1) // opening "
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '"':
			l.advance(1)
			return Token{
				Kind: TokString,
				Str:  b.String(),
				Span: l.span(startPos, l.pos, startLine, startCol),
			}
		case '\\':
			if l.pos+1 >= len(l.src) {
				return l.errTok(startPos, l.pos, startLine, startCol,
					"unterminated escape sequence at end of input", "")
			}
			next := l.src[l.pos+1]
			switch next {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '$':
				b.WriteByte('$')
			default:
				return l.errTok(l.pos, l.pos+2, l.line, l.col,
					fmt.Sprintf("unrecognized escape sequence \\%c", next),
					`only \" \\ \n \t \$ are recognized`)
			}
			l.advance(2)
		case '\n':
			// Newlines inside strings are allowed; tracker advances line.
			b.WriteByte('\n')
			l.advance(1)
		default:
			b.WriteByte(c)
			l.advance(1)
		}
	}
	return l.errTok(startPos, l.pos, startLine, startCol,
		"unterminated string literal",
		"strings must be closed with a matching double-quote on the same logical line or via a multiline literal")
}

// skipWhitespaceAndComments advances past spaces, tabs, newlines, CR, and
// '#'-to-end-of-line comments.
func (l *Lexer) skipWhitespaceAndComments() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			l.advance(1)
		case c == '\n':
			l.advance(1)
		case c == '#':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.advance(1)
			}
		default:
			return
		}
	}
}

// advance consumes n bytes, updating line/col.
func (l *Lexer) advance(n int) {
	for i := 0; i < n && l.pos < len(l.src); i++ {
		if l.src[l.pos] == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
		l.pos++
	}
}

// peekByte returns the byte at pos+offset, or 0 if out of range.
func (l *Lexer) peekByte(offset int) byte {
	if l.pos+offset >= len(l.src) {
		return 0
	}
	return l.src[l.pos+offset]
}

// span builds a Span for a closed range.
func (l *Lexer) span(start, end, startLine, startCol int) Span {
	return Span{Start: start, End: end, Line: startLine, Col: startCol}
}

// spanAt builds a zero-width span at the given position.
func (l *Lexer) spanAt(start, end int) Span {
	return Span{Start: start, End: end, Line: l.line, Col: l.col}
}

// errTok produces a TokError and stashes the diagnostic on the lexer.
func (l *Lexer) errTok(start, end, line, col int, msg, hint string) Token {
	sp := Span{Start: start, End: end, Line: line, Col: col}
	l.err = &Error{
		Category: "syntax",
		Message:  msg,
		Hint:     hint,
		Span:     sp,
		Source:   l.src,
	}
	return Token{Kind: TokError, Span: sp, Str: msg}
}

// ---- Character classes --------------------------------------------------

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || isDigit(c)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isSpaceRune is exposed for completeness; not used internally.
func isSpaceRune(r rune) bool { return unicode.IsSpace(r) }
