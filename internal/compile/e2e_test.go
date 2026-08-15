package compile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runWat executes a compiled module under wasmtime and returns stdout and
// the exit code.
func runWat(t *testing.T, wat string) (string, int) {
	t.Helper()
	if _, err := exec.LookPath("wasmtime"); err != nil {
		t.Skip("wasmtime not installed; skipping execution test")
	}
	path := filepath.Join(t.TempDir(), "prog.wat")
	if err := os.WriteFile(path, []byte(wat), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("wasmtime", path)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("wasmtime: %v\nstderr: %s", err, errOut.String())
	}
	if errOut.Len() > 0 && code == 0 {
		t.Logf("wasmtime stderr: %s", errOut.String())
	}
	return out.String(), code
}

func compileAndRun(t *testing.T, src string) (string, int) {
	t.Helper()
	wat, err := Compile(src)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	return runWat(t, wat)
}

const printers = `
extern fn jaba_print_int(v: i64);
extern fn jaba_print_str(s: *u8);
`

// TestExports verifies that `export fn` produces a wasm export clause on the
// generated function, alongside the module's built-in memory/_start exports.
func TestExports(t *testing.T) {
	src := `export fn add(a: i32, b: i32) -> i32 { return a + b; }
		fn main() -> i32 { return 0; }`
	wat, err := Compile(src)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	for _, want := range []string{
		`(func $f_add (export "add")`,
		`(memory (export "memory")`,
		`(func $_start (export "_start")`,
	} {
		if !strings.Contains(wat, want) {
			t.Errorf("WAT does not contain %q:\n%s", want, wat)
		}
	}

	if _, err := Compile(`export fn memory() -> i32 { return 0; }
		fn main() -> i32 { return 0; }`); err == nil {
		t.Error("exporting a function named memory should be rejected")
	}
}

func TestPrograms(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string // expected stdout
		exit int
	}{
		{
			name: "exit code",
			src:  `fn main() -> i32 { return 42; }`,
			exit: 42,
		},
		{
			name: "hello",
			src: printers + `
				fn main() -> i32 { jaba_print_str("hello\n"); return 0; }`,
			want: "hello\n",
		},
		{
			name: "print int edges",
			src: printers + `
				fn p(v: i64) { jaba_print_int(v); jaba_print_str("\n"); }
				fn main() -> i32 {
					p(0); p(-1); p(9223372036854775807);
					p(-9223372036854775807 - 1);
					return 0;
				}`,
			want: "0\n-1\n9223372036854775807\n-9223372036854775808\n",
		},
		{
			name: "narrow int wrap",
			src: printers + `
				fn main() -> i32 {
					var a: i8 = 127;
					a = a + 1;                        // wraps to -128
					jaba_print_int(a as i64); jaba_print_str("\n");
					var b: u8 = 200;
					b = b + 100;                      // wraps to 44
					jaba_print_int(b as i64); jaba_print_str("\n");
					var c: u16 = 0;
					c = c - 1;                        // wraps to 65535
					jaba_print_int(c as i64); jaba_print_str("\n");
					return 0;
				}`,
			want: "-128\n44\n65535\n",
		},
		{
			name: "signedness of division and comparison",
			src: printers + `
				fn main() -> i32 {
					var s: i32 = -7;
					jaba_print_int((s / 2) as i64); jaba_print_str("\n");   // -3
					jaba_print_int((s % 2) as i64); jaba_print_str("\n");   // -1
					var u: u32 = 0;
					u = u - 7;                       // huge unsigned value
					jaba_print_int((u / 2) as u64 as i64); jaba_print_str("\n");
					if u > 7 { jaba_print_str("unsigned-compare\n"); }
					if s < 2 { jaba_print_str("signed-compare\n"); }
					return 0;
				}`,
			want: "-3\n-1\n2147483644\nunsigned-compare\nsigned-compare\n",
		},
		{
			name: "cast widening follows source signedness",
			src: printers + `
				fn main() -> i32 {
					var a: i8 = -1;
					jaba_print_int(a as i64); jaba_print_str("\n");            // -1
					jaba_print_int(a as u8 as i64); jaba_print_str("\n");      // 255
					jaba_print_int(a as u8 as u32 as i64); jaba_print_str("\n"); // 255
					var big: i64 = 4294967297;
					jaba_print_int(big as i32 as i64); jaba_print_str("\n");   // 1
					jaba_print_int(big as u8 as i64); jaba_print_str("\n");    // 1
					return 0;
				}`,
			want: "-1\n255\n255\n1\n1\n",
		},
		{
			name: "short circuit does not evaluate the right side",
			src: printers + `
				var calls: i32 = 0;
				fn bump() -> bool { calls = calls + 1; return true; }
				fn main() -> i32 {
					if false && bump() { }
					if true || bump() { }
					jaba_print_int(calls as i64); jaba_print_str("\n");  // 0
					if true && bump() { }
					jaba_print_int(calls as i64); jaba_print_str("\n");  // 1
					return 0;
				}`,
			want: "0\n1\n",
		},
		{
			name: "globals: initialized, zeroed, and mutated",
			src: printers + `
				var counter: i32;
				var start: i64 = -5;
				var greeting: *u8 = "hi\n";
				fn tick() { counter = counter + 1; }
				fn main() -> i32 {
					tick(); tick(); tick();
					jaba_print_int(counter as i64); jaba_print_str("\n");
					jaba_print_int(start); jaba_print_str("\n");
					jaba_print_str(greeting);
					return 0;
				}`,
			want: "3\n-5\nhi\n",
		},
		{
			name: "nested structs and arrays of structs",
			src: printers + `
				struct Inner { a: i16, b: i64, }
				struct Outer { tag: u8, in: Inner, pts: [3]Inner, }
				fn main() -> i32 {
					var o: Outer;
					o.tag = 7;
					o.in = Inner{ a: -2, b: 1000000000000 };
					var i: i32 = 0;
					while i < 3 {
						o.pts[i] = Inner{ a: i as i16, b: (i * 10) as i64 };
						i = i + 1;
					}
					jaba_print_int(o.tag as i64); jaba_print_str("\n");
					jaba_print_int(o.in.b); jaba_print_str("\n");
					jaba_print_int(o.pts[2].b); jaba_print_str("\n");
					return 0;
				}`,
			want: "7\n1000000000000\n20\n",
		},
		{
			name: "struct return feeding a field access",
			src: printers + `
				struct P { x: i32, y: i32, }
				fn make(x: i32, y: i32) -> P { return P{ x: x, y: y }; }
				fn main() -> i32 {
					jaba_print_int(make(11, 22).y as i64);
					jaba_print_str("\n");
					return 0;
				}`,
			want: "22\n",
		},
		{
			name: "struct assignment copies",
			src: printers + `
				struct P { x: i32, y: i32, }
				fn main() -> i32 {
					var a: P = P{ x: 1, y: 2 };
					var b: P = a;
					b.x = 100;
					jaba_print_int(a.x as i64); jaba_print_str("\n"); // still 1
					return 0;
				}`,
			want: "1\n",
		},
		{
			name: "pointers: address-of, deref, mutation through pointer",
			src: printers + `
				fn set(p: *i32, v: i32) { *p = v; }
				fn main() -> i32 {
					var x: i32 = 1;
					set(&x, 41);
					x = x + 1;
					jaba_print_int(x as i64); jaba_print_str("\n");
					var px: *i32 = &x;
					jaba_print_int((*px) as i64); jaba_print_str("\n");
					return 0;
				}`,
			want: "42\n42\n",
		},
		{
			name: "break and continue",
			src: printers + `
				fn main() -> i32 {
					var i: i32 = 0;
					var sum: i32 = 0;
					while true {
						i = i + 1;
						if i > 10 { break; }
						if i % 2 == 0 { continue; }
						sum = sum + i;   // 1+3+5+7+9
					}
					jaba_print_int(sum as i64); jaba_print_str("\n");
					return 0;
				}`,
			want: "25\n",
		},
		{
			name: "shadowing in nested blocks",
			src: printers + `
				var x: i32 = 1;
				fn main() -> i32 {
					var x: i32 = 2;
					{
						var x: i32 = 3;
						jaba_print_int(x as i64); jaba_print_str("\n");
					}
					jaba_print_int(x as i64); jaba_print_str("\n");
					return 0;
				}`,
			want: "3\n2\n",
		},
		{
			name: "char literals and string walking",
			src: printers + `
				fn count(s: *u8, c: u8) -> i32 {
					var n: i32 = 0;
					var i: i32 = 0;
					while s[i] != '\0' {
						if s[i] == c { n = n + 1; }
						i = i + 1;
					}
					return n;
				}
				fn main() -> i32 {
					jaba_print_int(count("abracadabra", 'a') as i64);
					jaba_print_str("\n");
					return 0;
				}`,
			want: "5\n",
		},
		{
			name: "malloc grows memory past the initial pages",
			src: printers + `
				extern fn malloc(n: u32) -> *u8;
				fn main() -> i32 {
					// 8 MiB, far beyond the initial memory size.
					var p: *u8 = malloc(8388608);
					p[8388607] = 7;
					jaba_print_int(p[8388607] as i64); jaba_print_str("\n");
					return 0;
				}`,
			want: "7\n",
		},
		{
			name: "struct literal in parenthesized condition",
			src: printers + `
				struct P { x: i32, y: i32, }
				fn get() -> P { return P{ x: 1, y: 2 }; }
				fn main() -> i32 {
					if (get().x == P{ x: 1, y: 2 }.x) {
						jaba_print_str("equal\n");
					}
					return 0;
				}`,
			want: "equal\n",
		},
		{
			name: "operator precedence and as binding",
			src: printers + `
				fn main() -> i32 {
					var a: i32 = 2;
					jaba_print_int((a + 3 * 4) as i64); jaba_print_str("\n"); // 14
					var b: i64 = a as i64 * 3;   // (a as i64) * 3
					jaba_print_int(b); jaba_print_str("\n");
					var c: i64 = -a as i64;      // (-a) as i64
					jaba_print_int(c); jaba_print_str("\n");
					return 0;
				}`,
			want: "14\n6\n-2\n",
		},
		{
			name: "call statements discard scalar and aggregate results",
			src: printers + `
				struct P { x: i32, y: i32, }
				fn scalar() -> i32 { return 7; }
				fn agg() -> P { return P{ x: 1, y: 2 }; }
				fn main() -> i32 {
					if true { scalar(); }
					agg();
					jaba_print_str("ok\n");
					return 0;
				}`,
			want: "ok\n",
		},
		{
			name: "folded negation keeps canonical form",
			src: printers + `
				fn main() -> i32 {
					var x: u8 = 56;
					// -(200) is constant-folded; at type u8 it must wrap to 56.
					if -(200) == x { jaba_print_str("wrap\n"); }
					return 0;
				}`,
			want: "wrap\n",
		},
		{
			name: "struct literal in call arguments inside a condition",
			src: printers + `
				struct P { x: i32, y: i32, }
				fn get(p: P) -> i32 { return p.x; }
				fn main() -> i32 {
					if get(P{ x: 3, y: 4 }) == 3 { jaba_print_str("lit\n"); }
					return 0;
				}`,
			want: "lit\n",
		},
		{
			name: "aggregate argument is copied by the callee once",
			src: printers + `
				struct Big { a: [10]i64, }
				fn sum(b: Big) -> i64 {
					var s: i64 = 0;
					var i: i32 = 0;
					while i < 10 { s = s + b.a[i]; i = i + 1; }
					b.a[0] = -999;      // stomp the copy; must not affect the caller
					return s;
				}
				fn main() -> i32 {
					var b: Big;
					var i: i32 = 0;
					while i < 10 { b.a[i] = (i + 1) as i64; i = i + 1; }
					jaba_print_int(sum(b)); jaba_print_str("\n");  // 55
					jaba_print_int(b.a[0]); jaba_print_str("\n");  // still 1
					return 0;
				}`,
			want: "55\n1\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code := compileAndRun(t, tt.src)
			if out != tt.want {
				t.Errorf("stdout = %q, want %q", out, tt.want)
			}
			if code != tt.exit {
				t.Errorf("exit code = %d, want %d", code, tt.exit)
			}
		})
	}
}

// TestExamples compiles and runs every program in examples/ to make sure
// the shipped samples stay green.
func TestExamples(t *testing.T) {
	files, err := filepath.Glob("../../examples/*.jaba")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples found: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			out, code := compileAndRun(t, string(src))
			if code != 0 {
				t.Errorf("exit code = %d, want 0 (stdout %q)", code, out)
			}
		})
	}
}
