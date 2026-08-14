package lexer

import (
	"strings"
	"testing"

	"jabascript/internal/token"
)

func TestTokens(t *testing.T) {
	src := `fn main() -> i32 { // comment
	var x: i32 = 0xff;
	x = x + 'a';
	jaba_print_str("kwak\n");
}`
	toks, err := Lex(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []token.Kind{
		token.FN, token.IDENT, token.LPAREN, token.RPAREN, token.ARROW, token.I32, token.LBRACE,
		token.VAR, token.IDENT, token.COLON, token.I32, token.ASSIGN, token.INT, token.SEMI,
		token.IDENT, token.ASSIGN, token.IDENT, token.PLUS, token.CHAR, token.SEMI,
		token.IDENT, token.LPAREN, token.STRING, token.RPAREN, token.SEMI,
		token.RBRACE, token.EOF,
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d", len(toks), len(want))
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("token %d: got %s, want %s", i, toks[i].Kind, k)
		}
	}
}

func TestValues(t *testing.T) {
	toks, err := Lex(`42 0xff 'a' '\n' '\0' "a\tb"`)
	if err != nil {
		t.Fatal(err)
	}
	if toks[0].Val != 42 || toks[1].Val != 255 {
		t.Errorf("int values: got %d, %d", toks[0].Val, toks[1].Val)
	}
	if toks[2].Val != 'a' || toks[3].Val != '\n' || toks[4].Val != 0 {
		t.Errorf("char values: got %d, %d, %d", toks[2].Val, toks[3].Val, toks[4].Val)
	}
	if toks[5].Str != "a\tb" {
		t.Errorf("string value: got %q", toks[5].Str)
	}
}

func TestPositions(t *testing.T) {
	toks, err := Lex("fn\n  var")
	if err != nil {
		t.Fatal(err)
	}
	if toks[0].Pos != (token.Pos{Line: 1, Col: 1}) {
		t.Errorf("fn at %s", toks[0].Pos)
	}
	if toks[1].Pos != (token.Pos{Line: 2, Col: 3}) {
		t.Errorf("var at %s", toks[1].Pos)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct{ src, want string }{
		{"042", "leading zeros"},
		{`"unterminated`, "unterminated string"},
		{"'ab'", "unterminated character"},
		{"''", "empty character"},
		{"'\\q'", "unknown escape"},
		{"a | b", "no bitwise operators"},
		{"$", "unexpected character"},
		{"0x", "at least one digit"},
	}
	for _, tt := range tests {
		if _, err := Lex(tt.src); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Lex(%q) error = %v, want it to mention %q", tt.src, err, tt.want)
		}
	}
}
