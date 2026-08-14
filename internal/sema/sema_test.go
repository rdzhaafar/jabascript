package sema

import (
	"strings"
	"testing"

	"jabascript/internal/lexer"
	"jabascript/internal/parser"
)

func check(t *testing.T, src string) error {
	t.Helper()
	toks, err := lexer.Lex(src)
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	f, err := parser.Parse(toks)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Check(f)
}

func TestValidPrograms(t *testing.T) {
	srcs := []string{
		`fn main() -> i32 { return 0; }`,
		`struct S { p: *S, }  // self-reference through a pointer is fine
		 fn main() -> i32 { var s: S; return 0; }`,
		`fn main() -> i32 { var x: i8 = -128; return x as i32; }`,
		`fn f(p: *u8) -> *u8 { return p; }
		 fn main() -> i32 { return 0; }`,
		`fn later() -> i32 { return first(); }  // order-independent top level
		 fn first() -> i32 { return 1; }
		 fn main() -> i32 { return later(); }`,
	}
	for _, src := range srcs {
		if err := check(t, src); err != nil {
			t.Errorf("valid program rejected:\n%s\nerror: %v", src, err)
		}
	}
}

func TestCheckErrors(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"no main", `fn f() { }`, "no main function"},
		{"bad main signature", `fn main() { }`, "fn main() -> i32"},
		{"no implicit widening",
			`fn main() -> i32 { var a: i32 = 5; var b: i64 = a; return 0; }`,
			"cannot initialize"},
		{"no int to bool",
			`fn main() -> i32 { if 1 { } return 0; }`,
			"condition must be bool"},
		{"no int-pointer cast",
			`fn main() -> i32 { var p: *u8 = 0 as *u8; return 0; }`,
			"cannot convert"},
		{"no bool cast",
			`fn main() -> i32 { var b: bool = true; return b as i32; }`,
			"cannot convert"},
		{"no array decay",
			`fn f(p: *i32) { }
			 fn main() -> i32 { var a: [4]i32; f(a); return 0; }`,
			"must be *i32"},
		{"no pointer arithmetic",
			`fn main() -> i32 { var p: *u8 = "x"; p = p + 1; return 0; }`,
			"pointer arithmetic is not supported"},
		{"mixed operand types",
			`fn main() -> i32 { var a: i32 = 1; var b: i64 = 2; var c: i64 = a + b; return 0; }`,
			"mismatched operand types"},
		{"literal does not fit",
			`fn main() -> i32 { var x: i8 = 128; return 0; }`,
			"does not fit in i8"},
		{"negative literal fits exactly",
			`fn main() -> i32 { var x: i8 = -129; return 0; }`,
			"does not fit in i8"},
		{"default literal type is i32",
			`fn main() -> i32 { var x: i64 = 5000000000 as i64; return 0; }`,
			"does not fit in i32"},
		{"undefined name", `fn main() -> i32 { return x; }`, "undefined: x"},
		{"duplicate top-level", `var f: i32; fn f() { } fn main() -> i32 { return 0; }`,
			"already declared"},
		{"duplicate in same scope",
			`fn main() -> i32 { var x: i32; var x: i32; return 0; }`,
			"already declared in this scope"},
		{"recursive struct by value",
			`struct S { next: S, } fn main() -> i32 { return 0; }`,
			"contains itself by value"},
		{"struct literal missing field",
			`struct P { x: i32, y: i32, }
			 fn main() -> i32 { var p: P = P{ x: 1 }; return 0; }`,
			"missing field y"},
		{"unknown field",
			`struct P { x: i32, }
			 fn main() -> i32 { var p: P; p.z = 1; return 0; }`,
			"no field z"},
		{"auto-deref only one level",
			`struct P { x: i32, }
			 fn f(pp: **P) -> i32 { return pp.x; }
			 fn main() -> i32 { return 0; }`,
			"cannot access field"},
		{"nominal typing",
			`struct A { x: i32, } struct B { x: i32, }
			 fn f(a: A) { }
			 fn main() -> i32 { var b: B = B{ x: 1 }; f(b); return 0; }`,
			"must be A"},
		{"break outside loop", `fn main() -> i32 { break; return 0; }`, "break outside"},
		{"void call as value",
			`fn f() { }
			 fn main() -> i32 { var x: i32 = f(); return 0; }`,
			"returns nothing"},
		{"missing return value",
			`fn f() -> i32 { return; }
			 fn main() -> i32 { return 0; }`,
			"must return a value"},
		{"wrong arity",
			`fn f(a: i32) { }
			 fn main() -> i32 { f(); return 0; }`,
			"takes 1 argument"},
		{"function as value",
			`fn f() { }
			 fn main() -> i32 { var x: i32 = f; return 0; }`,
			"can only be called"},
		{"address of rvalue",
			`fn main() -> i32 { var p: *i32 = &(1 + 2); return 0; }`,
			"cannot take the address"},
		{"non-constant global initializer",
			`fn f() -> i32 { return 1; }
			 var g: i32 = f();
			 fn main() -> i32 { return 0; }`,
			"constant expression"},
		{"operands of && must be bool",
			`fn main() -> i32 { if 1 && 2 { } return 0; }`,
			"must be bool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := check(t, tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
