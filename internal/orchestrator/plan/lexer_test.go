package plan

import "testing"

func TestLexer_Punctuation(t *testing.T) {
	src := `:= -> => | : , = . @ - { } [ ] ( )`
	want := []TokKind{
		TokColEq, TokArrow, TokFatArrow,
		TokPipe, TokColon, TokComma, TokEq, TokDot, TokAt, TokDash,
		TokLBrace, TokRBrace, TokLBracket, TokRBracket, TokLParen, TokRParen,
		TokEOF,
	}
	l := NewLexer(src)
	for i, w := range want {
		got := l.Next()
		if got.Kind != w {
			t.Errorf("token %d: got %v, want %v", i, got.Kind, w)
		}
	}
}

func TestLexer_IdentAndKeywords(t *testing.T) {
	src := `plan meta s1 _foo Bar123`
	l := NewLexer(src)
	for _, want := range []string{"plan", "meta", "s1", "_foo", "Bar123"} {
		got := l.Next()
		if got.Kind != TokIdent || got.Str != want {
			t.Errorf("got %v %q, want IDENT %q", got.Kind, got.Str, want)
		}
	}
	if got := l.Next(); got.Kind != TokEOF {
		t.Errorf("expected EOF, got %v", got.Kind)
	}
}

func TestLexer_Numbers(t *testing.T) {
	l := NewLexer(`42 3.14 0`)
	cases := []float64{42, 3.14, 0}
	for i, want := range cases {
		got := l.Next()
		if got.Kind != TokNumber || got.Num != want {
			t.Errorf("token %d: got %v %v, want NUMBER %v", i, got.Kind, got.Num, want)
		}
	}
}

func TestLexer_Hex(t *testing.T) {
	l := NewLexer(`0xa1b2c3d4 0xDEADBEEF`)
	cases := []uint64{0xa1b2c3d4, 0xDEADBEEF}
	for i, want := range cases {
		got := l.Next()
		if got.Kind != TokHex || got.Hex != want {
			t.Errorf("token %d: got %v %x, want HEX %x", i, got.Kind, got.Hex, want)
		}
	}
}

func TestLexer_String(t *testing.T) {
	l := NewLexer(`"hello world" "with \"escapes\" and ${interp.field}"`)
	t1 := l.Next()
	if t1.Kind != TokString || t1.Str != "hello world" {
		t.Errorf("got %v %q", t1.Kind, t1.Str)
	}
	t2 := l.Next()
	if t2.Kind != TokString {
		t.Fatalf("got %v, want STRING", t2.Kind)
	}
	want := `with "escapes" and ${interp.field}`
	if t2.Str != want {
		t.Errorf("got %q, want %q", t2.Str, want)
	}
}

func TestLexer_StringMultiline(t *testing.T) {
	l := NewLexer(`"line one
line two"`)
	tok := l.Next()
	if tok.Kind != TokString {
		t.Fatalf("got %v", tok.Kind)
	}
	if tok.Str != "line one\nline two" {
		t.Errorf("got %q", tok.Str)
	}
}

func TestLexer_Var(t *testing.T) {
	l := NewLexer(`$x $foo_bar`)
	for _, want := range []string{"x", "foo_bar"} {
		tok := l.Next()
		if tok.Kind != TokVar || tok.Str != want {
			t.Errorf("got %v %q, want VAR %q", tok.Kind, tok.Str, want)
		}
	}
}

func TestLexer_VarFieldChain(t *testing.T) {
	// Field chain is the parser's job; lexer emits VAR DOT IDENT DOT IDENT...
	l := NewLexer(`$bash.stdout.length`)
	want := []TokKind{TokVar, TokDot, TokIdent, TokDot, TokIdent}
	for i, w := range want {
		tok := l.Next()
		if tok.Kind != w {
			t.Errorf("token %d: got %v, want %v", i, tok.Kind, w)
		}
	}
}

func TestLexer_Comments(t *testing.T) {
	src := `# leading comment
local llama # trailing
# another
foo`
	l := NewLexer(src)
	wantStrs := []string{"local", "llama", "foo"}
	for i, w := range wantStrs {
		tok := l.Next()
		if tok.Kind != TokIdent || tok.Str != w {
			t.Errorf("token %d: got %v %q, want IDENT %q", i, tok.Kind, tok.Str, w)
		}
	}
}

func TestLexer_LineColTracking(t *testing.T) {
	l := NewLexer("foo\n  bar")
	t1 := l.Next()
	if t1.Span.Line != 1 || t1.Span.Col != 1 {
		t.Errorf("foo at %d:%d, want 1:1", t1.Span.Line, t1.Span.Col)
	}
	t2 := l.Next()
	if t2.Span.Line != 2 || t2.Span.Col != 3 {
		t.Errorf("bar at %d:%d, want 2:3", t2.Span.Line, t2.Span.Col)
	}
}

func TestLexer_PeekDoesNotConsume(t *testing.T) {
	l := NewLexer(`foo bar`)
	p1 := l.Peek()
	p2 := l.Peek()
	if p1.Str != "foo" || p2.Str != "foo" {
		t.Errorf("peek not idempotent: %q %q", p1.Str, p2.Str)
	}
	n := l.Next()
	if n.Str != "foo" {
		t.Errorf("next after peek = %q, want foo", n.Str)
	}
	if l.Next().Str != "bar" {
		t.Error("expected bar")
	}
}

func TestLexer_UnterminatedString(t *testing.T) {
	l := NewLexer(`"oops`)
	tok := l.Next()
	if tok.Kind != TokError {
		t.Errorf("got %v, want ERROR", tok.Kind)
	}
	if l.Err() == nil {
		t.Fatal("Err() returned nil for error token")
	}
}

func TestLexer_BadEscape(t *testing.T) {
	l := NewLexer(`"bad \q escape"`)
	tok := l.Next()
	if tok.Kind != TokError {
		t.Errorf("got %v, want ERROR", tok.Kind)
	}
}

func TestLexer_PlanShapeTokenStream(t *testing.T) {
	// One realistic step line tokenizes to the expected shape.
	src := `s1 := internal_tool Bash {cmd: "git status"} -> $stat => s2`
	want := []TokKind{
		TokIdent, TokColEq, TokIdent, TokIdent,
		TokLBrace, TokIdent, TokColon, TokString, TokRBrace,
		TokArrow, TokVar, TokFatArrow, TokIdent, TokEOF,
	}
	l := NewLexer(src)
	for i, w := range want {
		got := l.Next()
		if got.Kind != w {
			t.Errorf("tok %d: got %v (%q), want %v", i, got.Kind, got.Str, w)
		}
	}
}
