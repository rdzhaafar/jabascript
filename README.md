# JabaScript

A small statically typed, compiled language for teaching compilers, implemented in Go,
compiling to **WebAssembly** (WASI). C-shaped semantics, a grammar you can parse by
inspection, and a type system with no implicit rules — no implicit conversions, no
array decay, no truthiness.

```jaba
// hello.jaba
extern fn jaba_print_str(s: *u8);

fn main() -> i32 {
    jaba_print_str("kwak\n");
    return 0;
}
```

## Building and running

```sh
go build -o jabac ./cmd/jabac

./jabac hello.jaba        # writes hello.wat (readable WebAssembly text)
wasmtime hello.wat        # any WASI runtime works

./jabac -run hello.jaba   # compile and run in one step
```

The compiler emits a self-contained textual `.wat` module: string data, a shadow stack,
a small embedded runtime (printing via WASI `fd_write`, a bump allocator), and your
code — annotated with source line comments so you can read it next to the `.jaba` file.

## Documentation

- [docs/design.md](docs/design.md) — the language: goals, types, grammar, semantics
  (written against the original AArch64 target).
- [docs/design-wasm.md](docs/design-wasm.md) — this implementation's target: what
  changes when the backend is Wasm, and what deliberately doesn't.

## Layout

```
cmd/jabac          the CLI
internal/lexer     hand-written lexer
internal/parser    recursive descent + Pratt expression loop
internal/sema      name resolution and type checking
internal/codegen   typed AST → WAT, plus the embedded WAT runtime
internal/compile   the pipeline, end to end
examples/          small programs exercising the whole language
examples/jshost/   calling a JavaScript function from JabaScript, hosted in Node
```

Run the tests (execution tests are skipped if `wasmtime` is not installed):

```sh
go test ./...
```
