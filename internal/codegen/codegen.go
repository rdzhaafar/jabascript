// Package codegen lowers a checked AST to textual WebAssembly (WAT).
//
// The output is deliberately naive and readable: every local lives in a
// shadow-stack slot in linear memory (so address-of works uniformly and the
// generated code maps one-to-one onto the AST), and no register — that is,
// wasm-local — allocation is done across statements. The module is a WASI
// command: run it with `wasmtime program.wat`.
//
// Memory layout (wasm32, one linear memory):
//
//	0     .. 96          reserved scratch for the runtime (iovec, itoa buffer)
//	1024  .. strEnd      string literal data, NUL-terminated
//	      .. globEnd     global variables ("bss"; wasm memory starts zeroed)
//	      .. stackTop    the shadow stack, 1 MiB, growing DOWN ($__sp)
//	stackTop ..          the bump-allocator heap, growing UP ($__heap)
//
// Calling convention ("WasmJaba"): scalar parameters and returns are wasm
// values (i32/i64). A struct or array argument is passed as an i32 address
// and the CALLEE copies it into its own frame, which preserves by-value
// semantics with a single copy. A struct or array return is written through
// a hidden pointer passed as the first parameter — the wasm analog of the
// AArch64 indirect-result register x8.
package codegen

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"jabascript/internal/ast"
	"jabascript/internal/token"
	"jabascript/internal/types"
)

const (
	scratchIovec = 8    // two i32s: buf, len
	scratchNW    = 16   // fd_write's nwritten out-param
	stringBase   = 1024 // string literals start here
	stackSize    = 1 << 20
)

// runtimeExterns are the `extern fn`s provided by the embedded runtime
// rather than imported from the host, keyed by name with their JabaScript
// signatures. Declaring one with a mismatched signature is a compile error;
// the comparison uses language-level types, not their wasm lowering, so
// e.g. `extern fn malloc(n: i32) -> *i8;` is rejected even though it lowers
// to the same wasm signature.
var ptrU8 = &types.Pointer{Elem: types.TypU8}

var runtimeExterns = map[string]struct {
	sig     *types.FuncSig
	jabaSig string // shown in error messages
}{
	"jaba_print_int": {&types.FuncSig{Params: []types.Type{types.TypI64}}, "extern fn jaba_print_int(v: i64);"},
	"jaba_print_str": {&types.FuncSig{Params: []types.Type{ptrU8}}, "extern fn jaba_print_str(s: *u8);"},
	"malloc":         {&types.FuncSig{Params: []types.Type{types.TypU32}, Ret: ptrU8}, "extern fn malloc(n: u32) -> *u8;"},
	"free":           {&types.FuncSig{Params: []types.Type{ptrU8}}, "extern fn free(p: *u8);"},
}

// sameSig reports whether two signatures are identical at the language level.
func sameSig(a, b *types.FuncSig) bool {
	if len(a.Params) != len(b.Params) {
		return false
	}
	for i := range a.Params {
		if !types.Same(a.Params[i], b.Params[i]) {
			return false
		}
	}
	if (a.Ret == nil) != (b.Ret == nil) {
		return false
	}
	return a.Ret == nil || types.Same(a.Ret, b.Ret)
}

type loopLabels struct{ brk, cont string }

type gen struct {
	out strings.Builder
	ind int
	err error

	strOff   map[string]int
	strOrder []string
	globOff  map[*ast.Object]int
	globals  []*ast.VarDecl
	stackTop int
	heapBase int

	// per-function state
	fn        *ast.FuncDecl
	slotOff   map[*ast.Object]int
	tempOff   map[ast.Expr]int
	frameSize int
	labelN    int
	loops     []loopLabels
}

// Generate lowers a checked file to a complete WAT module.
func Generate(f *ast.File) (string, error) {
	g := &gen{
		strOff:  map[string]int{},
		globOff: map[*ast.Object]int{},
	}
	g.layoutData(f)
	g.emitModule(f)
	return g.out.String(), g.err
}

func (g *gen) errorf(pos token.Pos, format string, args ...any) {
	if g.err == nil {
		g.err = fmt.Errorf("%s: %s", pos, fmt.Sprintf(format, args...))
	}
}

func (g *gen) w(format string, args ...any) {
	g.out.WriteString(strings.Repeat("  ", g.ind))
	fmt.Fprintf(&g.out, format, args...)
	g.out.WriteByte('\n')
}

// ---- Data layout ----

func (g *gen) layoutData(f *ast.File) {
	// Intern every string literal in the program, deduplicated.
	off := stringBase
	inspect(f, func(n ast.Node) {
		s, ok := n.(*ast.StrLit)
		if !ok {
			return
		}
		if _, seen := g.strOff[s.Val]; seen {
			return
		}
		g.strOff[s.Val] = off
		g.strOrder = append(g.strOrder, s.Val)
		off += len(s.Val) + 1 // NUL terminator
	})

	// Globals follow the strings, each at its natural alignment.
	off = types.AlignUp(off, 16)
	for _, d := range f.Decls {
		vd, ok := d.(*ast.VarDecl)
		if !ok || vd.Obj.Type == nil {
			continue
		}
		off = types.AlignUp(off, vd.Obj.Type.Align())
		g.globOff[vd.Obj] = off
		off += vd.Obj.Type.Size()
		g.globals = append(g.globals, vd)
	}

	g.stackTop = types.AlignUp(off, 16) + stackSize
	g.heapBase = g.stackTop
}

// ---- Module emission ----

func (g *gen) emitModule(f *ast.File) {
	g.w("(module")
	g.ind++

	g.w(";; WASI system interface")
	g.w(`(import "wasi_snapshot_preview1" "fd_write" (func $fd_write (param i32 i32 i32 i32) (result i32)))`)
	g.w(`(import "wasi_snapshot_preview1" "proc_exit" (func $proc_exit (param i32)))`)

	// Host imports: extern fns not provided by the embedded runtime.
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || !fd.Extern {
			continue
		}
		if rt, isRT := runtimeExterns[fd.Name]; isRT {
			if !sameSig(fd.Obj.Sig, rt.sig) {
				g.errorf(fd.Pos(), "%s is provided by the runtime and must be declared `%s`", fd.Name, rt.jabaSig)
			}
			continue
		}
		params, ret := g.wasmSig(fd.Obj.Sig)
		sig := ""
		if len(params) > 0 {
			sig = " (param " + strings.Join(params, " ") + ")"
		}
		if ret != "" {
			sig += " (result " + ret + ")"
		}
		g.w(`(import "env" %q (func $f_%s%s))`, fd.Name, fd.Name, sig)
	}

	pages := (g.heapBase+0xffff)/0x10000 + 1
	g.w("")
	g.w("(memory (export \"memory\") %d)", pages)
	g.w("(global $__sp (mut i32) (i32.const %d))   ;; shadow stack pointer, grows down", g.stackTop)
	g.w("(global $__heap (mut i32) (i32.const %d)) ;; bump allocator, grows up", g.heapBase)

	if len(g.strOrder) > 0 {
		g.w("")
		g.w(";; string literals")
		for _, s := range g.strOrder {
			g.w("(data (i32.const %d) %s) ;; %s", g.strOff[s], watString(s+"\x00"), watComment(s))
		}
	}

	// Non-zero global initializers become data segments; zero-valued
	// globals need nothing, since wasm memory starts zeroed.
	first := true
	for _, d := range g.globals {
		if d.Init == nil {
			continue
		}
		bytes := g.constBytes(d)
		if bytes == nil || allZero(bytes) {
			continue
		}
		if first {
			g.w("")
			g.w(";; initialized globals")
			first = false
		}
		g.w("(data (i32.const %d) %s) ;; %s", g.globOff[d.Obj], watString(string(bytes)), d.Name)
	}

	g.out.WriteString(runtimeWAT)

	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && !fd.Extern {
			g.w("")
			g.emitFunc(fd)
		}
	}

	g.w("")
	g.w(";; WASI entry point: run main, hand its result to the host as the exit code")
	g.w(`(func $_start (export "_start")`)
	g.ind++
	g.w("call $f_main")
	g.w("call $proc_exit")
	g.ind--
	g.w(")")

	g.ind--
	g.w(")")
}

// constBytes encodes a global's constant initializer as little-endian bytes.
func (g *gen) constBytes(d *ast.VarDecl) []byte {
	t := d.Obj.Type
	var v uint64
	switch e := unparen(d.Init).(type) {
	case *ast.IntLit:
		v = e.Val
	case *ast.CharLit:
		v = uint64(e.Val)
	case *ast.BoolLit:
		if e.Val {
			v = 1
		}
	case *ast.StrLit:
		v = uint64(g.strOff[e.Val])
	case *ast.UnaryExpr: // -literal, validated by the checker
		v = -(e.X.(*ast.IntLit).Val)
	default:
		return nil
	}
	b := make([]byte, t.Size())
	for i := range b {
		b[i] = byte(v >> (8 * i))
	}
	return b
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// wasmSig maps a JabaScript signature to wasm-level parameter and result
// types: aggregates become an i32 address, and an aggregate return becomes
// a hidden leading i32 pointer parameter.
func (g *gen) wasmSig(sig *types.FuncSig) (params []string, ret string) {
	if sig.Ret != nil && types.IsAggregate(sig.Ret) {
		params = append(params, "i32")
	} else if sig.Ret != nil {
		ret = types.Wasm(sig.Ret)
	}
	for _, p := range sig.Params {
		if types.IsAggregate(p) {
			params = append(params, "i32")
		} else {
			params = append(params, types.Wasm(p))
		}
	}
	return params, ret
}

// ---- Function emission ----

func (g *gen) emitFunc(d *ast.FuncDecl) {
	g.fn = d
	g.slotOff = map[*ast.Object]int{}
	g.tempOff = map[ast.Expr]int{}
	g.labelN = 0
	g.loops = nil

	// Frame layout: parameters first, then every declared local, then a
	// slot for every aggregate rvalue (call results and struct literals).
	off := 0
	slot := func(t types.Type) int {
		off = types.AlignUp(off, t.Align())
		o := off
		off += t.Size()
		return o
	}
	for _, p := range d.Params {
		g.slotOff[p.Obj] = slot(p.Obj.Type)
	}
	inspect(d.Body, func(n ast.Node) {
		switch n := n.(type) {
		case *ast.VarDecl:
			if n.Obj.Type != nil {
				g.slotOff[n.Obj] = slot(n.Obj.Type)
			}
		case *ast.CallExpr:
			if t := n.Type(); t != nil && types.IsAggregate(t) {
				g.tempOff[n] = slot(t)
			}
		case *ast.StructLit:
			if t := n.Type(); t != nil {
				g.tempOff[n] = slot(t)
			}
		}
	})
	g.frameSize = types.AlignUp(off, 16)

	// Signature.
	sig := d.Obj.Sig
	var head strings.Builder
	fmt.Fprintf(&head, "(func $f_%s", d.Name)
	if d.Export {
		fmt.Fprintf(&head, " (export %q)", d.Name)
	}
	aggRet := sig.Ret != nil && types.IsAggregate(sig.Ret)
	if aggRet {
		head.WriteString(" (param $__ret i32)")
	}
	for i, p := range sig.Params {
		wt := "i32"
		if !types.IsAggregate(p) {
			wt = types.Wasm(p)
		}
		fmt.Fprintf(&head, " (param $p%d %s)", i, wt)
	}
	if sig.Ret != nil && !aggRet {
		fmt.Fprintf(&head, " (result %s)", types.Wasm(sig.Ret))
	}
	g.w("%s", head.String())
	g.ind++
	g.w("(local $__fp i32)")

	// Prologue: claim the frame, then spill every parameter into its slot
	// so all variables are addressed uniformly as fp+offset.
	g.w(";; prologue: frame is %d bytes", g.frameSize)
	g.w("global.get $__sp")
	g.w("i32.const %d", g.frameSize)
	g.w("i32.sub")
	g.w("local.tee $__fp")
	g.w("global.set $__sp")
	for i, p := range d.Params {
		t := p.Obj.Type
		g.w(";; spill param %s", p.Name)
		g.pushFPOffset(g.slotOff[p.Obj])
		g.w("local.get $p%d", i)
		if types.IsAggregate(t) {
			g.w("i32.const %d", t.Size())
			g.w("memory.copy")
		} else {
			g.store(t)
		}
	}

	for _, s := range d.Body.Stmts {
		g.genStmt(s)
	}

	// Fall-off-the-end path. A value-returning function that reaches here
	// never returned; trap rather than return garbage (for an aggregate,
	// the hidden result buffer was never filled) or emit an invalid module.
	g.emitEpilogue()
	if sig.Ret != nil {
		g.w("unreachable ;; fell off the end of a value-returning function")
	}
	g.ind--
	g.w(")")
	g.fn = nil
}

func (g *gen) emitEpilogue() {
	g.w("local.get $__fp")
	g.w("i32.const %d", g.frameSize)
	g.w("i32.add")
	g.w("global.set $__sp")
}

func (g *gen) pushFPOffset(off int) {
	g.w("local.get $__fp")
	g.w("i32.const %d", off)
	g.w("i32.add")
}

// ---- Statements ----

func (g *gen) genStmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.Block:
		for _, inner := range s.Stmts {
			g.genStmt(inner)
		}

	case *ast.VarDecl:
		t := s.Obj.Type
		g.w(";; line %d: var %s: %s", s.Pos().Line, s.Name, t)
		if s.Init == nil {
			// Zero-initialize: the frame slot holds whatever the last
			// function left there.
			g.pushFPOffset(g.slotOff[s.Obj])
			if types.IsAggregate(t) {
				g.w("i32.const 0")
				g.w("i32.const %d", t.Size())
				g.w("memory.fill")
			} else {
				g.w("%s.const 0", types.Wasm(t))
				g.store(t)
			}
			return
		}
		g.pushFPOffset(g.slotOff[s.Obj])
		g.genStore(t, s.Init)

	case *ast.AssignStmt:
		g.w(";; line %d: assignment", s.Pos().Line)
		g.genAddr(s.LHS)
		g.genStore(s.LHS.Type(), s.RHS)

	case *ast.ExprStmt:
		call := s.X.(*ast.CallExpr)
		g.w(";; line %d: call %s", s.Pos().Line, call.Fun.Name)
		g.genCall(call)
		if t := call.Type(); t != nil && !types.IsAggregate(t) {
			g.w("drop")
		}

	case *ast.IfStmt:
		g.w(";; line %d: if", s.Pos().Line)
		g.genExpr(s.Cond)
		g.w("if")
		g.ind++
		g.genStmt(s.Then)
		if s.Else != nil {
			g.ind--
			g.w("else")
			g.ind++
			g.genStmt(s.Else)
		}
		g.ind--
		g.w("end")

	case *ast.WhileStmt:
		n := g.labelN
		g.labelN++
		brk, cont := fmt.Sprintf("$break%d", n), fmt.Sprintf("$continue%d", n)
		g.w(";; line %d: while", s.Pos().Line)
		g.w("block %s", brk)
		g.ind++
		g.w("loop %s", cont)
		g.ind++
		g.genExpr(s.Cond)
		g.w("i32.eqz")
		g.w("br_if %s", brk)
		g.loops = append(g.loops, loopLabels{brk, cont})
		g.genStmt(s.Body)
		g.loops = g.loops[:len(g.loops)-1]
		g.w("br %s", cont)
		g.ind--
		g.w("end")
		g.ind--
		g.w("end")

	case *ast.ReturnStmt:
		g.w(";; line %d: return", s.Pos().Line)
		if s.X != nil {
			ret := g.fn.Obj.Sig.Ret
			if types.IsAggregate(ret) {
				// Write the value through the hidden result pointer,
				// the wasm analog of AArch64's x8.
				g.w("local.get $__ret")
				g.genStore(ret, s.X)
			} else {
				g.genExpr(s.X)
			}
		}
		g.emitEpilogue()
		g.w("return")

	case *ast.BreakStmt:
		g.w("br %s", g.loops[len(g.loops)-1].brk)
	case *ast.ContinueStmt:
		g.w("br %s", g.loops[len(g.loops)-1].cont)
	}
}

// genStore expects a destination address on the stack and stores the value
// of e (of type t) to it — a typed store for scalars, a memory.copy for
// aggregates.
func (g *gen) genStore(t types.Type, e ast.Expr) {
	if types.IsAggregate(t) {
		g.genAddr(e)
		g.w("i32.const %d", t.Size())
		g.w("memory.copy")
		return
	}
	g.genExpr(e)
	g.store(t)
}

// ---- Expressions ----

// genExpr pushes the value of a scalar expression.
func (g *gen) genExpr(e ast.Expr) {
	t := e.Type()
	switch e := e.(type) {
	case *ast.IntLit:
		g.pushConst(t, e.Val)
	case *ast.CharLit:
		g.w("i32.const %d", e.Val)
	case *ast.BoolLit:
		v := 0
		if e.Val {
			v = 1
		}
		g.w("i32.const %d", v)
	case *ast.StrLit:
		g.w("i32.const %d ;; %s", g.strOff[e.Val], watComment(e.Val))
	case *ast.ParenExpr:
		g.genExpr(e.X)

	case *ast.Ident, *ast.FieldExpr, *ast.IndexExpr:
		g.genAddr(e)
		g.load(t)

	case *ast.UnaryExpr:
		switch e.Op {
		case token.MINUS:
			if lit, ok := unparen(e.X).(*ast.IntLit); ok {
				g.pushConst(t, -lit.Val)
				return
			}
			g.w("%s.const 0", types.Wasm(t))
			g.genExpr(e.X)
			g.w("%s.sub", types.Wasm(t))
			g.canon(t)
		case token.NOT:
			g.genExpr(e.X)
			g.w("i32.eqz")
		case token.AMP:
			g.genAddr(e.X)
		case token.STAR:
			g.genExpr(e.X)
			g.load(t)
		}

	case *ast.BinaryExpr:
		g.genBinary(e)

	case *ast.CastExpr:
		g.genExpr(e.X)
		g.castOps(e.X.Type(), t)

	case *ast.CallExpr:
		g.genCall(e)

	default:
		panic(fmt.Sprintf("genExpr: unexpected %T", e))
	}
}

func (g *gen) genBinary(e *ast.BinaryExpr) {
	// Short-circuit forms first: the right operand must not be evaluated
	// unless it is needed.
	switch e.Op {
	case token.ANDAND:
		g.genExpr(e.X)
		g.w("if (result i32)")
		g.ind++
		g.genExpr(e.Y)
		g.ind--
		g.w("else")
		g.ind++
		g.w("i32.const 0")
		g.ind--
		g.w("end")
		return
	case token.OROR:
		g.genExpr(e.X)
		g.w("if (result i32)")
		g.ind++
		g.w("i32.const 1")
		g.ind--
		g.w("else")
		g.ind++
		g.genExpr(e.Y)
		g.ind--
		g.w("end")
		return
	}

	g.genExpr(e.X)
	g.genExpr(e.Y)
	ot := e.X.Type() // operand type; e.Type() is bool for comparisons
	wt := types.Wasm(ot)
	signed := types.IsSigned(ot)

	switch e.Op {
	case token.PLUS:
		g.w("%s.add", wt)
		g.canon(ot)
	case token.MINUS:
		g.w("%s.sub", wt)
		g.canon(ot)
	case token.STAR:
		g.w("%s.mul", wt)
		g.canon(ot)
	case token.SLASH:
		g.w("%s.%s", wt, pick(signed, "div_s", "div_u"))
		g.canon(ot)
	case token.PERCENT:
		g.w("%s.%s", wt, pick(signed, "rem_s", "rem_u"))
		g.canon(ot)
	case token.EQ:
		g.w("%s.eq", wt)
	case token.NEQ:
		g.w("%s.ne", wt)
	case token.LT:
		g.w("%s.%s", wt, pick(signed, "lt_s", "lt_u"))
	case token.LE:
		g.w("%s.%s", wt, pick(signed, "le_s", "le_u"))
	case token.GT:
		g.w("%s.%s", wt, pick(signed, "gt_s", "gt_u"))
	case token.GE:
		g.w("%s.%s", wt, pick(signed, "ge_s", "ge_u"))
	}
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// genCall emits a call. For an aggregate-returning callee it pushes the
// hidden result pointer (a frame temp) first; the caller retrieves the
// address afterwards via genAddr.
func (g *gen) genCall(e *ast.CallExpr) {
	obj := e.Fun.Obj
	if t := e.Type(); t != nil && types.IsAggregate(t) {
		g.pushFPOffset(g.tempOff[e])
	}
	for i, arg := range e.Args {
		if types.IsAggregate(obj.Sig.Params[i]) {
			g.genAddr(arg) // callee copies; see the calling convention note
		} else {
			g.genExpr(arg)
		}
	}
	name := "$f_" + e.Fun.Name
	if obj.Extern {
		if _, isRT := runtimeExterns[e.Fun.Name]; isRT {
			name = "$rt_" + e.Fun.Name
		}
	}
	g.w("call %s", name)
}

// genAddr pushes the address of e. For aggregate rvalues (calls, struct
// literals) this is the address of a frame temp holding the value.
func (g *gen) genAddr(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Ident:
		switch e.Obj.Kind {
		case ast.ObjVar:
			g.pushFPOffset(g.slotOff[e.Obj])
		case ast.ObjGlobal:
			g.w("i32.const %d ;; &%s", g.globOff[e.Obj], e.Name)
		}

	case *ast.ParenExpr:
		g.genAddr(e.X)

	case *ast.FieldExpr:
		// `.` auto-dereferences one level: through a pointer the base
		// address is the pointer's value, otherwise the struct's address.
		var st *types.Struct
		if pt, ok := e.X.Type().(*types.Pointer); ok {
			st = pt.Elem.(*types.Struct)
			g.genExpr(e.X)
		} else {
			st = e.X.Type().(*types.Struct)
			g.genAddr(e.X)
		}
		f := st.Field(e.Name)
		g.w("i32.const %d ;; .%s", f.Offset, e.Name)
		g.w("i32.add")

	case *ast.IndexExpr:
		var elem types.Type
		if pt, ok := e.X.Type().(*types.Pointer); ok {
			elem = pt.Elem
			g.genExpr(e.X)
		} else {
			elem = e.X.Type().(*types.Array).Elem
			g.genAddr(e.X)
		}
		g.genExpr(e.Index)
		if types.Wasm(e.Index.Type()) == "i64" {
			g.w("i32.wrap_i64")
		}
		g.w("i32.const %d ;; sizeof(%s)", elem.Size(), elem)
		g.w("i32.mul")
		g.w("i32.add")

	case *ast.UnaryExpr: // *p as lvalue
		g.genExpr(e.X)

	case *ast.CallExpr:
		g.genCall(e) // fills the temp through the hidden pointer
		g.pushFPOffset(g.tempOff[e])

	case *ast.StructLit:
		st := e.Type().(*types.Struct)
		base := g.tempOff[e]
		for _, init := range e.Inits {
			f := st.Field(init.Name)
			g.pushFPOffset(base + f.Offset)
			g.genStore(f.Type, init.Value)
		}
		g.pushFPOffset(base)

	default:
		panic(fmt.Sprintf("genAddr: unexpected %T", e))
	}
}

// ---- Loads, stores, constants, conversions ----

// load expects an address on the stack and replaces it with the value.
// Values narrower than 32 bits are held in canonical form: sign-extended
// for signed types, zero-extended for unsigned and bool.
func (g *gen) load(t types.Type) {
	if b, ok := t.(*types.Basic); ok {
		switch b.Kind {
		case types.I8:
			g.w("i32.load8_s")
			return
		case types.U8, types.Bool:
			g.w("i32.load8_u")
			return
		case types.I16:
			g.w("i32.load16_s")
			return
		case types.U16:
			g.w("i32.load16_u")
			return
		case types.I64, types.U64:
			g.w("i64.load")
			return
		}
	}
	g.w("i32.load")
}

// store expects an address then a value on the stack.
func (g *gen) store(t types.Type) {
	switch t.Size() {
	case 1:
		g.w("i32.store8")
	case 2:
		g.w("i32.store16")
	case 8:
		g.w("i64.store")
	default:
		g.w("i32.store")
	}
}

func (g *gen) pushConst(t types.Type, v uint64) {
	if types.Wasm(t) == "i64" {
		g.w("i64.const %d", int64(v))
		return
	}
	// Constants obey the same canonical form as computed values: narrow
	// types are truncated to their width and re-extended by signedness,
	// which matters for folded negations like `-(200)` at type u8.
	if b, ok := t.(*types.Basic); ok {
		switch b.Kind {
		case types.I8:
			v = uint64(int64(int8(v)))
		case types.U8:
			v = uint64(uint8(v))
		case types.I16:
			v = uint64(int64(int16(v)))
		case types.U16:
			v = uint64(uint16(v))
		}
	}
	g.w("i32.const %d", int32(uint32(v)))
}

// canon re-establishes the canonical form of a value narrower than 32 bits
// after arithmetic may have overflowed it: wrap-around semantics come from
// truncating and re-extending.
func (g *gen) canon(t types.Type) {
	b, ok := t.(*types.Basic)
	if !ok {
		return
	}
	switch b.Kind {
	case types.I8:
		g.w("i32.extend8_s")
	case types.U8:
		g.w("i32.const 255")
		g.w("i32.and")
	case types.I16:
		g.w("i32.extend16_s")
	case types.U16:
		g.w("i32.const 65535")
		g.w("i32.and")
	}
}

// castOps converts the value on the stack from type `from` to type `to`.
// Widening extends according to the SOURCE type's signedness; narrowing
// truncates. Pointer-to-pointer casts are free.
func (g *gen) castOps(from, to types.Type) {
	if types.IsPointer(from) {
		return
	}
	from64 := types.Wasm(from) == "i64"
	to64 := types.Wasm(to) == "i64"
	switch {
	case from64 && to64:
		// i64 <-> u64: same bits
	case from64 && !to64:
		g.w("i32.wrap_i64")
		g.canon(to)
	case !from64 && to64:
		g.w("%s", pick(types.IsSigned(from), "i64.extend_i32_s", "i64.extend_i32_u"))
	default:
		g.canon(to)
	}
}

// ---- WAT string encoding ----

// watString encodes bytes as a WAT string literal, hex-escaping anything
// that is not printable ASCII.
func watString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 32 && c <= 126 && c != '"' && c != '\\' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "\\%02x", c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// watComment makes a string safe to place in a line comment. WAT source
// must be valid UTF-8, so invalid bytes are replaced and truncation never
// splits a multi-byte rune.
func watComment(s string) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	if len(s) > 40 {
		cut := 40
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}
