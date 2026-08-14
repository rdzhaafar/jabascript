package parser

import (
	"strings"
	"testing"

	"jabascript/internal/ast"
	"jabascript/internal/lexer"
	"jabascript/internal/token"
)

func parse(t *testing.T, src string) *ast.File {
	t.Helper()
	toks, err := lexer.Lex(src)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(toks)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// exprOf extracts the expression of the first `return` in the single
// function of src.
func exprOf(t *testing.T, expr string) ast.Expr {
	t.Helper()
	f := parse(t, "fn f() -> i32 { return "+expr+"; }")
	fn := f.Decls[0].(*ast.FuncDecl)
	return fn.Body.Stmts[0].(*ast.ReturnStmt).X
}

func TestPrecedence(t *testing.T) {
	// a + b * c parses as a + (b * c)
	e := exprOf(t, "a + b * c").(*ast.BinaryExpr)
	if e.Op != token.PLUS {
		t.Fatalf("root is %s, want +", e.Op)
	}
	if mul, ok := e.Y.(*ast.BinaryExpr); !ok || mul.Op != token.STAR {
		t.Errorf("right of + is not a multiplication")
	}

	// a as i64 * b parses as (a as i64) * b
	e2 := exprOf(t, "a as i64 * b").(*ast.BinaryExpr)
	if e2.Op != token.STAR {
		t.Fatalf("root is %s, want *", e2.Op)
	}
	if _, ok := e2.X.(*ast.CastExpr); !ok {
		t.Errorf("left of * is %T, want a cast", e2.X)
	}

	// -x as i64 parses as (-x) as i64
	e3 := exprOf(t, "-x as i64")
	if _, ok := e3.(*ast.CastExpr); !ok {
		t.Fatalf("root is %T, want a cast", e3)
	}

	// left associativity: a - b - c is (a - b) - c
	e4 := exprOf(t, "a - b - c").(*ast.BinaryExpr)
	if _, ok := e4.X.(*ast.BinaryExpr); !ok {
		t.Errorf("subtraction is not left-associative")
	}

	// || is lower than &&: a || b && c is a || (b && c)
	e5 := exprOf(t, "a || b && c").(*ast.BinaryExpr)
	if e5.Op != token.OROR {
		t.Fatalf("root is %s, want ||", e5.Op)
	}
}

func TestStructLitSuppressedInCondition(t *testing.T) {
	// The `{` after the condition must open the body, not a literal.
	f := parse(t, `fn f() { if p == q { } }`)
	fn := f.Decls[0].(*ast.FuncDecl)
	if _, ok := fn.Body.Stmts[0].(*ast.IfStmt); !ok {
		t.Fatal("if statement not parsed")
	}

	// Parenthesized, a struct literal is allowed in a condition.
	parse(t, `fn f() { if (p == Point{ x: 1 }) { } }`)

	// And struct literals inside index/call subexpressions of a condition
	// stay banned only at the top level.
	parse(t, `fn f() { while (Point{ x: 1 }.x) == 1 { } }`)
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ src, want string }{
		{`fn f() { x + 1; }`, "only calls may be used as statements"},
		{`fn f() { f() = 1; }`, "must be an lvalue"},
		{`fn f() { if x { } }` + "\x00", "unexpected character"}, // lexer error passthrough sanity
		{`fn f(x) { }`, "expected :"},
		{`var x i32;`, "expected :"},
		{`struct S { x: i32 }`, "expected ,"},
		{`fn f() { var a: [0]i32; }`, "positive"},
		{`extern fn g() { }`, "expected ;"},
		{`fn f() { return 1 }`, "expected ;"},
	}
	for _, tt := range tests {
		toks, lexErr := lexer.Lex(tt.src)
		var err error = lexErr
		if lexErr == nil {
			_, err = Parse(toks)
		}
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Parse(%q) error = %v, want it to mention %q", tt.src, err, tt.want)
		}
	}
}
