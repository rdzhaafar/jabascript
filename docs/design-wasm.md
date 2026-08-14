# JabaScript → WebAssembly — Target Design

This is the companion to [design.md](design.md) for the WebAssembly backend. The
*language* is the one described there — same lexical structure, types, grammar, and
semantics. This document covers only what changes when the compilation target is Wasm
instead of macOS AArch64, because that swap is itself the lesson: the whole front end
(lexer, parser, resolver, type checker) survives untouched, and everything that has to
change lives behind the codegen boundary.

Two language-visible deltas fall out of the target and are listed up front:

1. **Pointers are 4 bytes** (wasm32), so a pointer's alignment is 4 and struct layouts
   involving pointers differ from the AArch64 build.
2. The runtime's allocator is declared **`extern fn malloc(n: u32) -> *u8`** — sizes are
   `u32` on a 32-bit target.

Everything else in the language spec applies verbatim.

## Target and toolchain

The target is **WASI preview 1** on wasm32. The compiler is written in Go and emits a
**textual WebAssembly module** (`.wat`), which any WASI runtime executes directly:

```
jabac hello.jaba          # writes hello.wat
wasmtime hello.wat

jabac -run hello.jaba     # both steps, if wasmtime is on PATH
```

Emitting WAT rather than the binary `.wasm` format is the same deliberate teaching
choice as emitting `.s` rather than Mach-O on the native target. The output is
human-readable, students can hand-edit it to test a hypothesis, and the runtime's WAT
parser plays the role clang's assembler played — we never write a binary encoder. There
is no link step at all: where the AArch64 build shells out to `clang` to get dyld stubs
and a C runtime, the Wasm build emits one self-contained module and gets its "OS" from
the WASI imports the runtime provides.

Because there is exactly one target, platform details are hardcoded rather than
abstracted, in the same spirit as `@PAGE`/`@PAGEOFF` were: the module always imports
`wasi_snapshot_preview1.fd_write` and `proc_exit`, always exports `memory` and `_start`,
and string literals are `(data ...)` segments at fixed addresses.

## What replaces what

| AArch64 build | Wasm build |
|---|---|
| textual `.s`, assembled by clang | textual `.wat`, executed by the runtime |
| clang links `libSystem` + C startup | WASI imports; emitted `_start` calls `main`, then `proc_exit` |
| `runtime/jaba_rt.c`, linked alongside | a runtime written in WAT, embedded in every module |
| registers `x0`–`x7`, stack slots in the frame | wasm value stack, slots on a shadow stack in linear memory |
| AAPCS64 | the "WasmJaba" convention below |
| `adrp`/`add` + `@PAGE` relocations | `i32.const <address>` — addresses are just integers |
| `__TEXT,__cstring`, `__DATA,__bss` | data segments; wasm memory is zero-initialized, so bss is free |
| `bl _name` resolved by the linker | `call $f_name`, or an `(import "env" ...)` for unknown externs |

## Types on wasm32

Wasm has exactly two integer value types, `i32` and `i64`, and that mismatch with the
language's eight is the central instruction-selection lesson of this backend (the analog
of learning `w`- vs `x`-registers and `ldrsb`/`ldrh` on AArch64):

- `i64`/`u64` values are wasm `i64`; everything else — including `bool` and pointers —
  is wasm `i32`.
- Narrow types exist *at the memory boundary*: loads pick the signedness
  (`i32.load8_s` for `i8`, `i32.load8_u` for `u8`/`bool`, ...), stores truncate for
  free (`i32.store8`).
- While in a wasm value, a narrow integer is kept in **canonical form**: sign-extended
  to 32 bits if signed, zero-extended if unsigned. After any arithmetic that can
  overflow the narrow width, codegen re-canonicalizes (`i32.extend8_s`, or masking with
  `0xff`), which is exactly how wrap-around semantics are implemented. Comparisons then
  come out right by picking the `_s`/`_u` instruction variant.
- `as` between integers: widening extends by the *source* type's signedness
  (`i64.extend_i32_s/u`), narrowing is `i32.wrap_i64` plus re-canonicalization. Pointer
  casts compile to nothing.

Division notes: wasm traps on division by zero and on `INT_MIN / -1`, so "undefined" in
the spec concretely means a trap here rather than whatever AArch64's `sdiv` returns.

## Memory layout and the shadow stack

Wasm gives the program one flat, zero-initialized linear memory and no stack you can
take addresses into — locals live in unaddressable wasm locals. Since JabaScript has
`&x`, every variable instead gets a slot on a **shadow stack** carved out of linear
memory, managed by a global `$__sp` exactly like a real stack pointer:

```
0    .. 96          runtime scratch (iovec, itoa buffer)
1024 .. strEnd      string literals, NUL-terminated
     .. globEnd     globals ("bss" is free: memory starts zeroed)
     .. stackTop    shadow stack, 1 MiB, growing DOWN  ($__sp)
stackTop ..         bump-allocator heap, growing UP    ($__heap)
```

Each function's prologue subtracts its frame size from `$__sp` and keeps the frame
pointer in a wasm local `$__fp`; every variable access is `$__fp + constant`. Parameters
arrive as wasm values and are spilled into frame slots immediately, so codegen has one
addressing story for everything — the same "every local gets a stack slot, no register
allocation" naivety as the native backend, and the same motivation for introducing an IR
and register (here: wasm-local) allocation later. Stack overflow is unchecked, exactly
as it is unguarded on the native target.

## Calling convention

AAPCS64's role is played by a convention the compiler defines itself, which is its own
lesson: inside one module you get to *choose* the ABI, and only the WASI boundary is
fixed.

1. **Scalar** arguments and returns (integers, `bool`, pointers) are wasm parameters and
   results.
2. A **struct or array argument** is passed as an `i32` address, and the **callee**
   copies it into its own frame. One copy total, by-value semantics preserved; mutation
   of a parameter is invisible to the caller.
3. A **struct or array return** is written through a hidden `i32` pointer passed as the
   first parameter — the direct analog of AArch64's indirect-result register `x8`. The
   caller reserves a frame temp for it.

There is no 16-byte small-aggregate special case: wasm has no register pairs to exploit,
so *all* aggregates take the indirect path. The three AAPCS64 rules collapse to these
three, and the 16-byte stack alignment survives out of habit and hygiene (frames are
16-byte aligned, and `malloc` returns 16-byte-aligned blocks so casting its result to
any struct pointer stays well-aligned, mirroring macOS).

## C interoperability becomes host interoperability

There is no C linker in this picture, so `extern fn` is reinterpreted: it declares a
function provided by *someone else's module or the host*.

- Four names are provided by the **embedded runtime** — the Wasm analog of
  `runtime/jaba_rt.c`, except written in WAT and emitted into every module rather than
  linked alongside it:

  ```jaba
  extern fn jaba_print_int(v: i64);
  extern fn jaba_print_str(s: *u8);
  extern fn malloc(n: u32) -> *u8;
  extern fn free(p: *u8);
  ```

  Declaring one of these with any other signature is a compile-time error. The printers
  sit on raw WASI `fd_write`; `malloc` is a bump allocator that `memory.grow`s on
  demand, and `free` is honestly a no-op. Students get working I/O and a heap on day
  one, and the runtime itself is ~120 lines of readable WAT at the top of their own
  output file.

- Any other `extern fn` becomes an `(import "env" "name" ...)`, resolved by whatever
  host embeds the module. This replaces "link any object file you like against it."

Variadic functions stop being an issue rather than being deferred: wasm signatures are
fixed-arity by construction, so the Apple-ABI `printf` detour has no Wasm counterpart.

`main` must be `fn main() -> i32`. The emitted `_start` (the WASI command entry point)
calls it and hands the result to `proc_exit`, so `wasmtime hello.wat; echo $?` shows the
return value — process setup and the exit path come from the runtime host, just as clang
provided them natively.

## Implementation

The pipeline is the straight line from the design doc, one package per stage, hand
written throughout:

```
source → lexer → tokens → parser → AST → resolver/checker (sema) → codegen → .wat → wasmtime
internal/lexer   internal/parser  internal/ast    internal/sema     internal/codegen
```

Codegen walks the typed AST directly — no IR — and the emitted WAT is annotated with
`;; line N` comments so it reads next to the source. Aggregate rvalues (calls returning
structs, struct literals) each get their own frame temp with no reuse; frames are fatter
than they need to be, visibly, which is the point. `&&`/`||` compile to wasm `if` blocks
with a result, `while` to the standard `block`/`loop`/`br_if` skeleton, and a
value-returning function that falls off its end hits an `unreachable` trap.

The future-extensions list from the design doc carries over with one addition at the
top of the value-per-cost ranking: **register allocation** here means allocating wasm
locals instead of shadow-stack slots for address-never-taken variables, which is both
the cheapest optimization to write and a dramatic one to see in the output.
