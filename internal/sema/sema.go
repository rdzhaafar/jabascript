// Package sema resolves names and type-checks a parsed file.
//
// It runs the two middle stages of the pipeline as separate passes over the
// AST, exactly as the design doc lays them out:
//
//	resolve: bind every top-level name to an Object, compute struct
//	         layouts (rejecting cycles), and resolve function signatures.
//	check:   walk every function body, binding local names and annotating
//	         every expression node with its type. Nothing is inserted
//	         implicitly — there are no implicit conversions to insert.
//
// The only inference in the language is integer literal typing: an untyped
// literal adopts the type its context wants and defaults to i32 otherwise.
// That is implemented here as the `want` parameter threaded through
// checkExpr.
package sema

import (
	"errors"
	"fmt"
	"strings"

	"jabascript/internal/ast"
	"jabascript/internal/token"
	"jabascript/internal/types"
)

// Check resolves and type-checks f. On failure it returns an error listing
// every diagnostic, one per line.
func Check(f *ast.File) error {
	c := &checker{
		global:      map[string]*ast.Object{},
		structDecls: map[*types.Struct]*ast.StructDecl{},
		layoutState: map[*ast.StructDecl]int{},
	}
	c.resolve(f)
	if len(c.errs) == 0 {
		c.checkBodies(f)
	}
	if len(c.errs) > 0 {
		msgs := make([]string, len(c.errs))
		for i, e := range c.errs {
			msgs[i] = e.Error()
		}
		return errors.New(strings.Join(msgs, "\n"))
	}
	return nil
}

type semaError struct {
	pos token.Pos
	msg string
}

func (e *semaError) Error() string { return fmt.Sprintf("%s: %s", e.pos, e.msg) }

type checker struct {
	errs        []error
	global      map[string]*ast.Object
	structDecls map[*types.Struct]*ast.StructDecl
	layoutState map[*ast.StructDecl]int // 0 new, 1 visiting, 2 done

	scopes  []map[string]*ast.Object // innermost last; nil when not in a function
	curFunc *ast.FuncDecl
	loops   int
}

func (c *checker) errorf(pos token.Pos, format string, args ...any) {
	c.errs = append(c.errs, &semaError{pos: pos, msg: fmt.Sprintf(format, args...)})
}

var builtinTypes = map[string]types.Type{
	"i8": types.TypI8, "i16": types.TypI16, "i32": types.TypI32, "i64": types.TypI64,
	"u8": types.TypU8, "u16": types.TypU16, "u32": types.TypU32, "u64": types.TypU64,
	"bool": types.TypBool,
}

// ---- Pass 1: resolve ----

func (c *checker) resolve(f *ast.File) {
	// Top-level declarations are order-independent, so collect every name
	// before resolving any type or signature.
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.StructDecl:
			obj := &ast.Object{Kind: ast.ObjStruct, Name: d.Name, Decl: d,
				Struct: &types.Struct{Name: d.Name}}
			d.Obj = obj
			c.declareGlobal(d.Pos(), obj)
			c.structDecls[obj.Struct] = d
		case *ast.FuncDecl:
			obj := &ast.Object{Kind: ast.ObjFunc, Name: d.Name, Decl: d, Extern: d.Extern}
			d.Obj = obj
			c.declareGlobal(d.Pos(), obj)
		case *ast.VarDecl:
			obj := &ast.Object{Kind: ast.ObjGlobal, Name: d.Name, Decl: d}
			d.Obj = obj
			c.declareGlobal(d.Pos(), obj)
		}
	}

	for _, d := range f.Decls {
		if sd, ok := d.(*ast.StructDecl); ok {
			c.layoutStruct(sd)
		}
	}

	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			sig := &types.FuncSig{}
			for _, p := range d.Params {
				pt := c.typeOf(p.Type)
				p.Obj = &ast.Object{Kind: ast.ObjVar, Name: p.Name, Decl: p, Type: pt}
				sig.Params = append(sig.Params, pt)
			}
			if d.RetType != nil {
				sig.Ret = c.typeOf(d.RetType)
			}
			d.Obj.Sig = sig
		case *ast.VarDecl:
			d.Obj.Type = c.typeOf(d.Type)
		}
	}

	mainObj := c.global["main"]
	switch {
	case mainObj == nil:
		c.errorf(token.Pos{Line: 1, Col: 1}, "no main function: every program needs `fn main() -> i32`")
	case mainObj.Kind != ast.ObjFunc || mainObj.Extern:
		c.errorf(mainObj.Decl.Pos(), "main must be a function defined in this file")
	case mainObj.Sig != nil && (len(mainObj.Sig.Params) != 0 || !types.Same(mainObj.Sig.Ret, types.TypI32)):
		c.errorf(mainObj.Decl.Pos(), "main must have the signature `fn main() -> i32`")
	}

	// An exported function's export name shares a namespace with the module's
	// own exports (`memory` and `_start`); colliding with either would make the
	// emitted module invalid.
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Export {
			switch fd.Name {
			case "memory", "_start":
				c.errorf(fd.Pos(), "cannot export %q: the name is taken by the module's own exports", fd.Name)
			}
		}
	}
}

func (c *checker) declareGlobal(pos token.Pos, obj *ast.Object) {
	if prev, ok := c.global[obj.Name]; ok {
		c.errorf(pos, "%s is already declared at %s", obj.Name, prev.Decl.Pos())
		return
	}
	c.global[obj.Name] = obj
}

func (c *checker) layoutStruct(d *ast.StructDecl) {
	switch c.layoutState[d] {
	case 1:
		c.errorf(d.Pos(), "struct %s contains itself by value (use a pointer to break the cycle)", d.Name)
		c.layoutState[d] = 2
		return
	case 2:
		return
	}
	c.layoutState[d] = 1

	var fields []types.Field
	seen := map[string]bool{}
	for _, f := range d.Fields {
		if seen[f.Name] {
			c.errorf(f.NamePos, "duplicate field %s in struct %s", f.Name, d.Name)
			continue
		}
		seen[f.Name] = true
		ft := c.typeOf(f.Type)
		if ft == nil {
			continue
		}
		c.ensureLaidOut(ft)
		fields = append(fields, types.Field{Name: f.Name, Type: ft})
	}
	d.Obj.Struct.SetFields(fields)
	c.layoutState[d] = 2
}

// ensureLaidOut forces the layout of any struct that t contains by value,
// so that t.Size() is meaningful. Pointers deliberately stop the recursion:
// a struct may point to itself.
func (c *checker) ensureLaidOut(t types.Type) {
	switch t := t.(type) {
	case *types.Struct:
		if d, ok := c.structDecls[t]; ok {
			c.layoutStruct(d)
		}
	case *types.Array:
		c.ensureLaidOut(t.Elem)
	}
}

func (c *checker) typeOf(te ast.TypeExpr) types.Type {
	switch te := te.(type) {
	case *ast.NamedType:
		if t, ok := builtinTypes[te.Name]; ok {
			return t
		}
		obj, ok := c.global[te.Name]
		if !ok {
			c.errorf(te.Pos(), "undefined type %s", te.Name)
			return nil
		}
		if obj.Kind != ast.ObjStruct {
			c.errorf(te.Pos(), "%s is not a type", te.Name)
			return nil
		}
		return obj.Struct
	case *ast.PtrType:
		elem := c.typeOf(te.Elem)
		if elem == nil {
			return nil
		}
		return &types.Pointer{Elem: elem}
	case *ast.ArrayType:
		elem := c.typeOf(te.Elem)
		if elem == nil {
			return nil
		}
		c.ensureLaidOut(elem)
		if te.Len > 1<<28 || uint64(elem.Size())*te.Len > 1<<28 {
			c.errorf(te.Pos(), "array type is too large")
			return nil
		}
		return &types.Array{Len: int(te.Len), Elem: elem}
	}
	panic("unreachable")
}

// ---- Pass 2: check bodies ----

func (c *checker) checkBodies(f *ast.File) {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.VarDecl:
			c.checkGlobalInit(d)
		case *ast.FuncDecl:
			if d.Extern {
				continue
			}
			c.checkFunc(d)
		}
	}
}

// checkGlobalInit enforces that a global's initializer is a constant
// expression: an integer, character, bool, or string literal, optionally a
// negated integer literal.
func (c *checker) checkGlobalInit(d *ast.VarDecl) {
	if d.Init == nil || d.Obj.Type == nil {
		return
	}
	if !isConstExpr(d.Init) {
		c.errorf(d.Init.Pos(), "global initializer must be a constant expression (a literal)")
		return
	}
	t := c.checkExpr(d.Init, d.Obj.Type)
	if t != nil && !types.Same(t, d.Obj.Type) {
		c.errorf(d.Init.Pos(), "cannot initialize %s (type %s) with a value of type %s", d.Name, d.Obj.Type, t)
	}
}

func isConstExpr(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.IntLit, *ast.CharLit, *ast.BoolLit, *ast.StrLit:
		return true
	case *ast.UnaryExpr:
		if e.Op == token.MINUS {
			_, ok := e.X.(*ast.IntLit)
			return ok
		}
	case *ast.ParenExpr:
		return isConstExpr(e.X)
	}
	return false
}

func (c *checker) checkFunc(d *ast.FuncDecl) {
	c.curFunc = d
	c.pushScope()
	for _, p := range d.Params {
		c.declareLocal(p.NamePos, p.Obj)
	}
	c.checkBlock(d.Body)
	c.popScope()
	c.curFunc = nil
}

func (c *checker) pushScope() { c.scopes = append(c.scopes, map[string]*ast.Object{}) }
func (c *checker) popScope()  { c.scopes = c.scopes[:len(c.scopes)-1] }

func (c *checker) declareLocal(pos token.Pos, obj *ast.Object) {
	scope := c.scopes[len(c.scopes)-1]
	if _, ok := scope[obj.Name]; ok {
		c.errorf(pos, "%s is already declared in this scope", obj.Name)
		return
	}
	scope[obj.Name] = obj
}

func (c *checker) lookup(name string) *ast.Object {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if obj, ok := c.scopes[i][name]; ok {
			return obj
		}
	}
	return c.global[name]
}

func (c *checker) checkBlock(b *ast.Block) {
	c.pushScope()
	for _, s := range b.Stmts {
		c.checkStmt(s)
	}
	c.popScope()
}

func (c *checker) checkStmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.Block:
		c.checkBlock(s)

	case *ast.VarDecl:
		t := c.typeOf(s.Type)
		s.Obj = &ast.Object{Kind: ast.ObjVar, Name: s.Name, Decl: s, Type: t}
		if t != nil && s.Init != nil {
			it := c.checkExpr(s.Init, t)
			if it != nil && !types.Same(it, t) {
				c.errorf(s.Init.Pos(), "cannot initialize %s (type %s) with a value of type %s", s.Name, t, it)
			}
		}
		c.declareLocal(s.Pos(), s.Obj)

	case *ast.AssignStmt:
		lt := c.checkExpr(s.LHS, nil)
		rt := c.checkExpr(s.RHS, lt)
		if lt != nil && rt != nil && !types.Same(lt, rt) {
			c.errorf(s.LHS.Pos(), "cannot assign a value of type %s to a location of type %s", rt, lt)
		}

	case *ast.ExprStmt:
		// The parser guarantees this is a call; a call statement may
		// discard its result. The node is still annotated, because codegen
		// needs the type to drop a discarded scalar result and to pass the
		// hidden result pointer for a discarded aggregate one.
		call := s.X.(*ast.CallExpr)
		call.SetType(c.checkCall(call))

	case *ast.IfStmt:
		c.checkCond(s.Cond)
		c.checkBlock(s.Then)
		if s.Else != nil {
			c.checkStmt(s.Else)
		}

	case *ast.WhileStmt:
		c.checkCond(s.Cond)
		c.loops++
		c.checkBlock(s.Body)
		c.loops--

	case *ast.ReturnStmt:
		ret := c.curFunc.Obj.Sig.Ret
		if s.X == nil {
			if ret != nil {
				c.errorf(s.Pos(), "%s must return a value of type %s", c.curFunc.Name, ret)
			}
			return
		}
		if ret == nil {
			c.errorf(s.Pos(), "%s has no return type but returns a value", c.curFunc.Name)
			return
		}
		t := c.checkExpr(s.X, ret)
		if t != nil && !types.Same(t, ret) {
			c.errorf(s.X.Pos(), "cannot return %s from a function returning %s", t, ret)
		}

	case *ast.BreakStmt:
		if c.loops == 0 {
			c.errorf(s.Pos(), "break outside a loop")
		}
	case *ast.ContinueStmt:
		if c.loops == 0 {
			c.errorf(s.Pos(), "continue outside a loop")
		}
	}
}

func (c *checker) checkCond(e ast.Expr) {
	t := c.checkExpr(e, nil)
	if t != nil && !types.IsBool(t) {
		c.errorf(e.Pos(), "condition must be bool, not %s (integers and pointers are not truthy)", t)
	}
}

// ---- Expressions ----

// checkExpr type-checks e and annotates it. want is the type the context
// asks for; only untyped integer literals consult it. A nil result means an
// error was already reported.
func (c *checker) checkExpr(e ast.Expr, want types.Type) types.Type {
	t := c.checkExprInner(e, want)
	e.SetType(t)
	return t
}

func (c *checker) checkExprInner(e ast.Expr, want types.Type) types.Type {
	switch e := e.(type) {
	case *ast.IntLit:
		t := types.Type(types.TypI32)
		if want != nil && types.IsInteger(want) {
			t = want
		}
		c.checkFit(e.Pos(), e.Val, false, t)
		return t

	case *ast.CharLit:
		return types.TypU8

	case *ast.StrLit:
		return &types.Pointer{Elem: types.TypU8}

	case *ast.BoolLit:
		return types.TypBool

	case *ast.Ident:
		obj := c.lookup(e.Name)
		if obj == nil {
			c.errorf(e.Pos(), "undefined: %s", e.Name)
			return nil
		}
		e.Obj = obj
		switch obj.Kind {
		case ast.ObjVar, ast.ObjGlobal:
			return obj.Type
		case ast.ObjFunc:
			c.errorf(e.Pos(), "%s is a function; it can only be called (there are no function values)", e.Name)
		case ast.ObjStruct:
			c.errorf(e.Pos(), "%s is a type, not a value", e.Name)
		}
		return nil

	case *ast.ParenExpr:
		return c.checkExpr(e.X, want)

	case *ast.UnaryExpr:
		return c.checkUnary(e, want)

	case *ast.BinaryExpr:
		return c.checkBinary(e, want)

	case *ast.CastExpr:
		return c.checkCast(e)

	case *ast.CallExpr:
		t := c.checkCall(e)
		if t == nil && e.Fun.Obj != nil {
			c.errorf(e.Pos(), "%s returns nothing and cannot be used as a value", e.Fun.Name)
		}
		return t

	case *ast.IndexExpr:
		bt := c.checkExpr(e.X, nil)
		it := c.checkExpr(e.Index, nil)
		if it != nil && !types.IsInteger(it) {
			c.errorf(e.Index.Pos(), "index must be an integer, not %s", it)
		}
		if bt == nil {
			return nil
		}
		switch bt := bt.(type) {
		case *types.Array:
			return bt.Elem
		case *types.Pointer:
			return bt.Elem
		}
		c.errorf(e.Pos(), "cannot index a value of type %s", bt)
		return nil

	case *ast.FieldExpr:
		bt := c.checkExpr(e.X, nil)
		if bt == nil {
			return nil
		}
		st, ok := bt.(*types.Struct)
		if !ok {
			// `.` auto-dereferences exactly one level of pointer.
			if pt, isPtr := bt.(*types.Pointer); isPtr {
				st, ok = pt.Elem.(*types.Struct)
			}
		}
		if !ok || st == nil {
			c.errorf(e.NamePos, "cannot access field %s on a value of type %s", e.Name, bt)
			return nil
		}
		f := st.Field(e.Name)
		if f == nil {
			c.errorf(e.NamePos, "struct %s has no field %s", st.Name, e.Name)
			return nil
		}
		return f.Type

	case *ast.StructLit:
		return c.checkStructLit(e)
	}
	panic("unreachable")
}

func (c *checker) checkUnary(e *ast.UnaryExpr, want types.Type) types.Type {
	switch e.Op {
	case token.MINUS:
		// A negated integer literal is checked as one negative number, so
		// `var x: i8 = -128;` fits even though +128 alone would not.
		if lit, ok := e.X.(*ast.IntLit); ok {
			t := types.Type(types.TypI32)
			if want != nil && types.IsInteger(want) {
				t = want
			}
			c.checkFit(e.Pos(), lit.Val, true, t)
			lit.SetType(t)
			return t
		}
		t := c.checkExpr(e.X, want)
		if t == nil {
			return nil
		}
		if !types.IsInteger(t) {
			c.errorf(e.Pos(), "unary - requires an integer, not %s", t)
			return nil
		}
		return t

	case token.NOT:
		t := c.checkExpr(e.X, nil)
		if t != nil && !types.IsBool(t) {
			c.errorf(e.Pos(), "! requires a bool, not %s", t)
			return nil
		}
		return types.TypBool

	case token.AMP:
		if !isAddressable(e.X) {
			c.errorf(e.Pos(), "cannot take the address of this expression")
			return nil
		}
		t := c.checkExpr(e.X, nil)
		if t == nil {
			return nil
		}
		return &types.Pointer{Elem: t}

	case token.STAR:
		t := c.checkExpr(e.X, nil)
		if t == nil {
			return nil
		}
		pt, ok := t.(*types.Pointer)
		if !ok {
			c.errorf(e.Pos(), "cannot dereference a value of type %s", t)
			return nil
		}
		return pt.Elem
	}
	panic("unreachable")
}

func isAddressable(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident, *ast.FieldExpr, *ast.IndexExpr:
		return true
	case *ast.UnaryExpr:
		return e.Op == token.STAR
	case *ast.ParenExpr:
		return isAddressable(e.X)
	}
	return false
}

func (c *checker) checkBinary(e *ast.BinaryExpr, want types.Type) types.Type {
	switch e.Op {
	case token.ANDAND, token.OROR:
		for _, operand := range []ast.Expr{e.X, e.Y} {
			t := c.checkExpr(operand, nil)
			if t != nil && !types.IsBool(t) {
				c.errorf(operand.Pos(), "operands of %s must be bool, not %s", e.Op, t)
			}
		}
		return types.TypBool

	case token.PLUS, token.MINUS, token.STAR, token.SLASH, token.PERCENT:
		var arithWant types.Type
		if want != nil && types.IsInteger(want) {
			arithWant = want
		}
		lt, rt := c.checkOperandPair(e.X, e.Y, arithWant)
		if lt == nil || rt == nil {
			return nil
		}
		if !types.IsInteger(lt) {
			if types.IsPointer(lt) && e.Op == token.PLUS {
				c.errorf(e.Pos(), "pointer arithmetic is not supported; index with [] instead")
			} else {
				c.errorf(e.Pos(), "operands of %s must be integers, not %s", e.Op, lt)
			}
			return nil
		}
		if !types.Same(lt, rt) {
			c.errorf(e.Pos(), "mismatched operand types %s and %s (there are no implicit conversions; use `as`)", lt, rt)
			return nil
		}
		return lt

	case token.LT, token.LE, token.GT, token.GE:
		lt, rt := c.checkOperandPair(e.X, e.Y, nil)
		if lt == nil || rt == nil {
			return nil
		}
		if !types.IsInteger(lt) {
			c.errorf(e.Pos(), "operands of %s must be integers, not %s", e.Op, lt)
			return nil
		}
		if !types.Same(lt, rt) {
			c.errorf(e.Pos(), "mismatched operand types %s and %s (there are no implicit conversions; use `as`)", lt, rt)
			return nil
		}
		return types.TypBool

	case token.EQ, token.NEQ:
		lt, rt := c.checkOperandPair(e.X, e.Y, nil)
		if lt == nil || rt == nil {
			return nil
		}
		if !types.IsInteger(lt) && !types.IsBool(lt) && !types.IsPointer(lt) {
			c.errorf(e.Pos(), "%s is not defined on values of type %s", e.Op, lt)
			return nil
		}
		if !types.Same(lt, rt) {
			c.errorf(e.Pos(), "mismatched operand types %s and %s (there are no implicit conversions; use `as`)", lt, rt)
			return nil
		}
		return types.TypBool
	}
	panic("unreachable")
}

// checkOperandPair checks both operands of a binary operator, letting an
// untyped literal on one side adopt the type of the other side.
func (c *checker) checkOperandPair(x, y ast.Expr, want types.Type) (types.Type, types.Type) {
	if isUntypedLit(x) && !isUntypedLit(y) {
		rt := c.checkExpr(y, want)
		lt := c.checkExpr(x, rt)
		return lt, rt
	}
	lt := c.checkExpr(x, want)
	rt := c.checkExpr(y, lt)
	return lt, rt
}

func isUntypedLit(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.IntLit:
		return true
	case *ast.UnaryExpr:
		return e.Op == token.MINUS && isUntypedLit(e.X)
	case *ast.ParenExpr:
		return isUntypedLit(e.X)
	}
	return false
}

func (c *checker) checkCast(e *ast.CastExpr) types.Type {
	st := c.checkExpr(e.X, nil)
	tt := c.typeOf(e.ToType)
	if st == nil || tt == nil {
		return nil
	}
	intToInt := types.IsInteger(st) && types.IsInteger(tt)
	ptrToPtr := types.IsPointer(st) && types.IsPointer(tt)
	if !intToInt && !ptrToPtr {
		c.errorf(e.Pos(), "`as` converts between integer types or between pointer types; cannot convert %s to %s", st, tt)
		return nil
	}
	return tt
}

func (c *checker) checkCall(e *ast.CallExpr) types.Type {
	obj := c.lookup(e.Fun.Name)
	if obj == nil {
		c.errorf(e.Fun.Pos(), "undefined: %s", e.Fun.Name)
		return nil
	}
	e.Fun.Obj = obj
	if obj.Kind != ast.ObjFunc {
		c.errorf(e.Fun.Pos(), "%s is not a function", e.Fun.Name)
		return nil
	}
	sig := obj.Sig
	if len(e.Args) != len(sig.Params) {
		c.errorf(e.Pos(), "%s takes %d argument(s), got %d", e.Fun.Name, len(sig.Params), len(e.Args))
		return sig.Ret
	}
	for i, arg := range e.Args {
		pt := sig.Params[i]
		at := c.checkExpr(arg, pt)
		if at != nil && pt != nil && !types.Same(at, pt) {
			c.errorf(arg.Pos(), "argument %d of %s must be %s, not %s", i+1, e.Fun.Name, pt, at)
		}
	}
	return sig.Ret
}

func (c *checker) checkStructLit(e *ast.StructLit) types.Type {
	obj := c.lookup(e.Name)
	if obj == nil {
		c.errorf(e.Pos(), "undefined: %s", e.Name)
		return nil
	}
	if obj.Kind != ast.ObjStruct {
		c.errorf(e.Pos(), "%s is not a struct type", e.Name)
		return nil
	}
	e.Obj = obj
	st := obj.Struct

	given := map[string]bool{}
	for _, init := range e.Inits {
		f := st.Field(init.Name)
		if f == nil {
			c.errorf(init.NamePos, "struct %s has no field %s", st.Name, init.Name)
			c.checkExpr(init.Value, nil)
			continue
		}
		if given[init.Name] {
			c.errorf(init.NamePos, "field %s given twice", init.Name)
			continue
		}
		given[init.Name] = true
		vt := c.checkExpr(init.Value, f.Type)
		if vt != nil && !types.Same(vt, f.Type) {
			c.errorf(init.Value.Pos(), "field %s has type %s, cannot assign %s", init.Name, f.Type, vt)
		}
	}
	for _, f := range st.Fields {
		if !given[f.Name] {
			c.errorf(e.Pos(), "struct literal is missing field %s (every field must be given)", f.Name)
		}
	}
	return st
}

// checkFit reports an error when an integer literal does not fit its type.
func (c *checker) checkFit(pos token.Pos, val uint64, neg bool, t types.Type) {
	min, max := types.MinMax(t)
	if neg {
		// The magnitude of the most negative value is -min.
		limit := uint64(0)
		if min < 0 {
			limit = uint64(-(min + 1)) + 1
		}
		if val > limit {
			c.errorf(pos, "constant -%d does not fit in %s", val, t)
		}
		return
	}
	if val > max {
		c.errorf(pos, "constant %d does not fit in %s", val, t)
	}
}
