# JabaScript — Language Design

A small statically typed, compiled language for teaching compilers. C-shaped semantics,
but with a grammar you can parse by inspection and a type system with no implicit rules.

Source files use the extension `.jaba`.

## Goals and non-goals

The language exists to be *implemented in front of students*. Every feature has to earn
its place by teaching something: lexing, recursive descent, name resolution, type
checking, the AArch64 calling convention, or instruction selection. A feature that only
adds surface area is cut.

**Goals.** Small enough that the whole compiler is readable in a sitting. Statically
typed with no inference beyond literal typing. Compiles ahead of time to native code.
Can call C functions, so programs can do real I/O and allocation.

**Non-goals.** Portability (macOS AArch64 only, by design). Optimization. Memory safety.
Generics, closures, methods, interfaces, modules, garbage collection. A preprocessor.

## Target and toolchain

The only supported target is **macOS on AArch64 (Apple Silicon)**. The compiler is
written in Go and emits **textual AArch64 assembly**, then shells out to `clang` to
assemble and link:

```
jabac hello.jaba          # writes hello.s, then runs:
clang -o hello hello.s runtime/jaba_rt.c
```

Emitting assembly rather than Mach-O object files is a deliberate teaching choice. The
compiler's output is human-readable, students can hand-edit it to test a hypothesis, and
we get dyld stubs, `libSystem` linkage, and the C runtime startup for free instead of
writing an object-file writer and a linker.

Because there is exactly one target, platform details are hardcoded rather than
abstracted: symbols carry a leading underscore (`main` is emitted as `_main`), globals
and string literals are addressed with the `adrp`/`add` pair using `@PAGE` and
`@PAGEOFF` relocations, and read-only string data goes in `__TEXT,__cstring`.

## Lexical structure

Comments are `//` to end of line. There is no block comment form — a single comment
syntax keeps the lexer to one branch and sidesteps the "do they nest?" question that
block comments always raise. Commenting out a region is the editor's job.

Whitespace is insignificant; there is no semicolon insertion, so statement terminators
are explicit.

**Keywords.** `fn` `extern` `struct` `var` `if` `else` `while` `return` `break`
`continue` `as` `true` `false`, plus the type names `i8` `i16` `i32` `i64` `u8` `u16`
`u32` `u64` `bool`.

**Identifiers.** `[A-Za-z_][A-Za-z0-9_]*`.

**Integer literals.** Decimal (`42`) and hexadecimal (`0xff`). No octal — C's leading-zero
rule is a well-known trap and teaches nothing.

**Character literals.** `'a'`, type `u8`, with escapes `\n \t \r \0 \\ \' \"`.

**String literals.** `"kwak"`, type `*u8`, NUL-terminated and emitted into
`__TEXT,__cstring`. This makes strings interoperate with C for free.

**Operators and punctuation.** `+ - * / % == != < <= > >= && || ! = & . , ; : -> ( ) [ ] { }`

Note that `&` is address-of only and `*` is dereference or multiply depending on
position. There are no bitwise operators in v1 (see Future extensions).

## Types

```
i8  i16  i32  i64      signed integers
u8  u16  u32  u64      unsigned integers
bool                   true / false
*T                     pointer to T
[N]T                   array of N elements of T, N a positive integer literal
struct S { ... }       aggregate, nominally typed
```

There are no floating-point types in v1. Beyond keeping the type checker small, this
removes the entire second register class from codegen and — as noted under
[Calling convention](#calling-convention) — collapses the ABI's aggregate-passing rules
to three lines.

There is no `void` type. A function that returns nothing simply omits its `-> T` clause.

Sizes and alignments are natural: `iN`/`uN` are N/8 bytes and aligned to their size,
`bool` is 1 byte, pointers are 8 bytes. A struct's alignment is the maximum of its
fields' alignments and its size is rounded up to a multiple of that, with fields laid out
in declaration order. `[N]T` has T's alignment and size `N * sizeof(T)`.

Structs are nominally typed: two structs with identical fields are different types.

### Conversions

**There are no implicit conversions.** Not integer promotion, not widening, not
array-to-pointer decay, not int-to-bool. Every conversion is written with `as`.

```jaba
var a: i32 = 5;
var b: i64 = a as i64;      // required; `var b: i64 = a;` is an error
```

`as` is permitted between any two integer types (widening sign- or zero-extends
according to the *source* type; narrowing truncates) and between any two pointer types.
It is not permitted between integers and pointers, or to or from `bool`.

Free pointer-to-pointer casts are the escape hatch that makes the C heap usable:

```jaba
var raw: *u8 = malloc(16);
var p: *Point = raw as *Point;
p.x = 3;
free(p as *u8);
```

This is unchecked, exactly as in C. `malloc` on macOS returns 16-byte-aligned memory, so
casting its result to any JabaScript struct pointer is well-aligned in practice.

**No array decay.** An `[N]T` never silently becomes a `*T`. To pass an array to a
function, take the address of its first element explicitly with `&a[0]`. This deletes
what is probably C's most confusing implicit rule.

### Integer literal typing

An integer literal is untyped until it meets a context that gives it a type — a variable
declaration, an assignment, an argument position, a return. In the absence of any
context it defaults to `i32`. A literal that does not fit its inferred type is a
compile-time error. This is the only inference in the language.

## Declarations

```jaba
struct Point {
    x: i32,
    y: i32,
}

extern fn malloc(n: u64) -> *u8;
extern fn free(p: *u8);

var origin: Point;                 // global; zero-initialized

fn dist2(p: Point) -> i32 {
    return p.x * p.x + p.y * p.y;
}

fn main() -> i32 {
    var p: Point = Point{ x: 3, y: 4 };
    return dist2(p);
}
```

Types always follow the name after a colon; return types follow `->`. This is the single
biggest departure from C, and it is the point of the exercise: C's declarator grammar
(`int (*f[3])(char*)`) cannot be read left to right, and resolving it requires the parser
to know which identifiers are typedef names — the infamous "lexer hack," where the
symbol table feeds back into the lexer. Prefix-keyword declarations make the grammar
context-free and the parser a straightforward recursive descent.

Top-level declarations are order-independent: a function may call a function declared
later in the file. Name resolution is a separate pass over the whole AST, which is a
cleaner story to teach than C's forward declarations.

Globals are zero-initialized and go in `__DATA,__bss`; an explicit initializer must be a
constant expression.

`extern fn` declares a C function. It has no body, and arity is fixed — see
[C interoperability](#c-interoperability).

## Expressions

Precedence, lowest to highest, all binary operators left-associative:

| Level | Operators              | Notes                        |
|-------|------------------------|------------------------------|
| 1     | `\|\|`                 | short-circuit                |
| 2     | `&&`                   | short-circuit                |
| 3     | `==` `!=`              |                              |
| 4     | `<` `<=` `>` `>=`      |                              |
| 5     | `+` `-`                |                              |
| 6     | `*` `/` `%`            |                              |
| 7     | `as`                   | binds tighter than arithmetic, looser than unary |
| 8     | unary `-` `!` `&` `*`  | prefix, right to left        |
| 9     | `()` `[]` `.`          | postfix, left to right       |

So `-x as i64` parses as `(-x) as i64`, and `a as i64 * b` as `(a as i64) * b`.

This table *is* the expression parser. Implemented as Pratt parsing — a `parseExpr(minBP)`
loop that consumes a prefix operator or primary, then repeatedly folds in infix operators
whose binding power is at least `minBP` — the whole thing is well under 150 lines and
each level of the table is one entry in a lookup, not a separate function.

**Assignment is a statement, not an expression.** There is no value in `x = 1`, no
chaining `a = b = c`, and consequently `if x = 1` does not parse. That removes an entire
category of C bug and leaves the Pratt loop with no right-associative case to handle.

`&&` and `||` short-circuit, and their operands must be `bool`. Conditions in `if` and
`while` must be `bool`; integers and pointers are not implicitly truthy.

Division or remainder by zero is undefined; signed overflow wraps. Neither is checked.

### Member access, indexing, calls

`.` accesses a struct field and **auto-dereferences one level of pointer**, so `p.x`
works whether `p` is a `Point` or a `*Point`. There is no `->` operator. It exists in C
only because the language predates the idea that the compiler could figure this out, and
dropping it removes a token and a parse rule. Auto-deref applies to exactly one level:
`.` on a `**Point` is an error.

`[]` indexes both arrays and pointers: `a[i]` on `[N]T` and `p[i]` on `*T` both yield a
`T`, lowering to a scaled offset from a base address. No bounds are checked on either.

Pointer *arithmetic* is not supported — `p + 1` is an error. Indexing is the sanctioned
way to walk a buffer, which teaches the same scale-by-`sizeof` lowering while removing
the `p++` off-by-one foot-guns.

Struct literals are `Point{ x: 3, y: 4 }`. Every field must be given, in any order.

### The struct-literal ambiguity

Unparenthesized conditions plus brace-delimited struct literals create a real ambiguity:
in `if p == Point{ ... } { ... }`, the parser cannot tell whether the first `{` opens a
literal or the statement body. Go has exactly this problem and solves it by banning
composite literals in condition position unless parenthesized; JabaScript does the same.
The implementation is a `noStructLit` flag the parser sets while parsing a condition —
about three lines, and a good demonstration that a grammar's ambiguities are usually
resolved by a small amount of parser state rather than by redesigning the syntax.

## Statements

```jaba
var x: i32 = 0;        // declaration, initializer optional (else zero)
x = x + 1;             // assignment; LHS must be an lvalue
foo(1, 2);             // expression statement — calls only
if cond { } else { }   // braces mandatory, condition unparenthesized
while cond { }         // the only loop form
return expr;           // expr omitted iff the function has no return type
break;  continue;      // innermost loop only
{ }                    // nested block, introduces a scope
```

Braces are always required and conditions are never parenthesized. Together these delete
the dangling-`else` ambiguity and C's `if (x = 1)` trap without any special-casing.

`while` is the only loop; there is no `for` and no `do`/`while`. An lvalue is an
identifier, a field access, an index expression, or a dereference.

Deliberately absent, per the "must teach something" rule: `switch`, `goto`, the comma
operator, the ternary conditional, and `++`/`--`.

Local variables shadow globals and outer blocks. Every local is allocated a slot in its
function's frame — no register allocation across statements in the first implementation.

## C interoperability

C functions are declared with `extern fn` and called normally. The compiler emits a
`bl _name` and lets the linker resolve it against `libSystem` or any object file passed
to `clang`.

```jaba
extern fn malloc(n: u64) -> *u8;
extern fn free(p: *u8);
extern fn puts(s: *u8) -> i32;

fn main() -> i32 {
    puts("kwak");
    return 0;
}
```

**Variadic functions are not supported in v1.** This is the one place where Apple's ABI
diverges from stock AAPCS64: Apple passes variadic arguments on the stack rather than in
`x0`–`x7`, and packs them at natural size and alignment rather than promoting each to
8 bytes. Supporting `printf` correctly therefore means implementing a second, different
argument-marshalling path — a worthwhile lesson, but its own lesson.

Until then, formatted output comes from a small C shim compiled and linked alongside
every program:

```c
/* runtime/jaba_rt.c */
void jaba_print_int(long long v);
void jaba_print_str(const char *s);
```

declared on the JabaScript side as ordinary fixed-arity externs. Students get working I/O
on day one without the ABI detour.

`main` is the entry point, is emitted as `_main`, and should return `i32`. `clang` links
the C runtime startup, so process setup and the exit path are not our problem.

## Calling convention

JabaScript follows AAPCS64 as Apple implements it, for both its own functions and calls
into C — which is what makes `extern fn` work at all.

Integer and pointer arguments go in `x0`–`x7`, then on the stack. The return value comes
back in `x0`. `x8` is the indirect result register. `x9`–`x15` are caller-saved scratch,
`x19`–`x28` callee-saved, `x29` the frame pointer, `x30` the link register. **`x18` is
reserved by Apple and must never be touched.** The stack pointer must be 16-byte aligned
at every call.

Because there are no floating-point types, the ABI's aggregate rules — most of which
exist to handle homogeneous float aggregates — collapse to three cases:

1. A struct of **16 bytes or less** is passed in one or two general-purpose registers.
2. A struct **larger than 16 bytes** is passed indirectly: the caller makes a copy and
   passes its address.
3. A return value **larger than 16 bytes** is written through a hidden pointer that the
   caller places in `x8`.

Arrays passed by value follow the same rules as structs of the same size. This is the
entire by-value struct convention, and it is implementable in an afternoon.

## Grammar

EBNF. `{ x }` is zero or more, `[ x ]` optional, `|` alternation.

```ebnf
Program     = { TopDecl } .
TopDecl     = FuncDecl | ExternDecl | StructDecl | GlobalVar .

FuncDecl    = "fn" ident "(" [ ParamList ] ")" [ "->" Type ] Block .
ExternDecl  = "extern" "fn" ident "(" [ ParamList ] ")" [ "->" Type ] ";" .
StructDecl  = "struct" ident "{" { Field } "}" .
GlobalVar   = "var" ident ":" Type [ "=" Expr ] ";" .

ParamList   = Param { "," Param } [ "," ] .
Param       = ident ":" Type .
Field       = ident ":" Type "," .

Type        = IntType | "bool" | "*" Type | "[" int_lit "]" Type | ident .
IntType     = "i8" | "i16" | "i32" | "i64" | "u8" | "u16" | "u32" | "u64" .

Block       = "{" { Stmt } "}" .
Stmt        = VarDecl | SimpleStmt | If | While | Return
            | "break" ";" | "continue" ";" | Block .

VarDecl     = "var" ident ":" Type [ "=" Expr ] ";" .
SimpleStmt  = Expr [ "=" Expr ] ";" .
If          = "if" Expr Block [ "else" ( If | Block ) ] .
While       = "while" Expr Block .
Return      = "return" [ Expr ] ";" .

Expr        = OrExpr .
OrExpr      = AndExpr { "||" AndExpr } .
AndExpr     = EqExpr { "&&" EqExpr } .
EqExpr      = RelExpr { ( "==" | "!=" ) RelExpr } .
RelExpr     = AddExpr { ( "<" | "<=" | ">" | ">=" ) AddExpr } .
AddExpr     = MulExpr { ( "+" | "-" ) MulExpr } .
MulExpr     = CastExpr { ( "*" | "/" | "%" ) CastExpr } .
CastExpr    = UnaryExpr { "as" Type } .
UnaryExpr   = ( "-" | "!" | "&" | "*" ) UnaryExpr | PostfixExpr .
PostfixExpr = PrimaryExpr { "(" [ ArgList ] ")" | "[" Expr "]" | "." ident } .
PrimaryExpr = int_lit | char_lit | str_lit | "true" | "false"
            | ident | StructLit | "(" Expr ")" .

StructLit   = ident "{" [ FieldInit { "," FieldInit } [ "," ] ] "}" .
FieldInit   = ident ":" Expr .
ArgList     = Expr { "," Expr } [ "," ] .
```

Two places where the grammar as written is looser than the language, both resolved in
the parser rather than by contorting the syntax:

- `SimpleStmt` admits any expression on both sides of `=`. The parser parses an
  expression, checks for a following `=`, and then verifies the left side is an lvalue.
  A `SimpleStmt` with no `=` must be a call.
- `StructLit` is suppressed while parsing an `if` or `while` condition, per
  [the struct-literal ambiguity](#the-struct-literal-ambiguity).

## Implementation

No lexer or parser generator. The lexer is hand-written, and the parser is recursive
descent with a Pratt loop for expressions.

This is the standard choice in the teaching literature — *Crafting Interpreters* and
Thorsten Ball's *Writing an Interpreter in Go* both do it — and the reasoning is
pedagogical. A generator forces students to learn a second metalanguage before they learn
parsing, hides the recursion that is the actual subject, and produces error messages no
one wants to read. `goyacc` in particular would spend student attention on LALR
shift/reduce conflicts that are artifacts of the tool rather than properties of this
grammar. Hand-written recursive descent is more code, but every line of it teaches.

The pipeline is a straight line, one package per stage:

```
source → lexer → tokens → parser → AST → resolver → typechecker → codegen → .s → clang
```

The resolver binds every identifier to its declaration and builds scope chains. The type
checker annotates each expression node with a type, inserts nothing implicitly (there are
no implicit conversions to insert), and computes struct layouts. Codegen walks the typed
AST directly, with every local in a stack slot and no cross-statement register
allocation — naive, verbose output that maps one-to-one onto the AST, which is exactly
what you want when a student is reading the `.s` file next to the source.

An intermediate representation is deliberately deferred. Introducing one later, as a
motivated refactor once the naive backend's limits are visible, is a better lesson than
presenting it as something the design needed from the start.

## Future extensions

Roughly in order of teaching value per unit of implementation cost:

- **Bitwise operators** (`& | ^ ~ << >>`). Nearly free in codegen; omitted from v1 only
  to keep the precedence table short.
- **A real IR** (three-address or SSA), introduced as a refactor, followed by constant
  folding and dead-code elimination.
- **Register allocation** — linear scan over the IR, replacing stack slots.
- **Variadic `extern fn`**, as a dedicated lesson on Apple's ABI divergence, which then
  allows calling `printf` directly and retires the C shim.
- **`for` loops** as desugaring to `while`, demonstrating that syntax can be a front-end
  transformation.
- **Enums and `switch`**, together, since jump-table generation is the interesting part.
- **Multiple source files**, requiring a module/import story and separate compilation.
- **Floating point** (`f64`), which reintroduces the second register class and the
  ABI's HFA rules.
