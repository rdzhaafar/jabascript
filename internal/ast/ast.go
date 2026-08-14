// Package ast defines the abstract syntax tree for JabaScript.
//
// Nodes carry two kinds of annotation filled in by later passes: identifier
// nodes are linked to an Object by the resolver, and every expression node
// gets its type from the checker (via the embedded exprBase).
package ast

import (
	"jabascript/internal/token"
	"jabascript/internal/types"
)

type Node interface {
	Pos() token.Pos
}

// ---- Objects (what names resolve to) ----

type ObjKind int

const (
	ObjVar    ObjKind = iota // local variable or parameter
	ObjGlobal                // global variable
	ObjFunc                  // fn or extern fn
	ObjStruct                // struct type name
)

// Object is a named entity created by a declaration. The resolver links
// every identifier use to the Object of its declaration.
type Object struct {
	Kind ObjKind
	Name string
	Decl Node // *VarDecl, *Param, *FuncDecl, or *StructDecl

	Type   types.Type     // for vars and globals
	Sig    *types.FuncSig // for functions
	Struct *types.Struct  // for struct names
	Extern bool           // for functions: declared with `extern`
}

// ---- Declarations ----

type File struct {
	Decls []Node // *FuncDecl, *StructDecl, *VarDecl (globals)
}

func (f *File) Pos() token.Pos { return token.Pos{Line: 1, Col: 1} }

type Param struct {
	NamePos token.Pos
	Name    string
	Type    TypeExpr
	Obj     *Object
}

func (p *Param) Pos() token.Pos { return p.NamePos }

type FuncDecl struct {
	FnPos   token.Pos
	Name    string
	Params  []*Param
	RetType TypeExpr // nil when no `-> T`
	Body    *Block   // nil for extern fn
	Extern  bool
	Obj     *Object
}

func (d *FuncDecl) Pos() token.Pos { return d.FnPos }

type FieldDecl struct {
	NamePos token.Pos
	Name    string
	Type    TypeExpr
}

type StructDecl struct {
	StructPos token.Pos
	Name      string
	Fields    []FieldDecl
	Obj       *Object
}

func (d *StructDecl) Pos() token.Pos { return d.StructPos }

// VarDecl is both a global variable declaration and the `var` statement.
type VarDecl struct {
	VarPos token.Pos
	Name   string
	Type   TypeExpr
	Init   Expr // nil when omitted (zero-initialized)
	Obj    *Object
}

func (d *VarDecl) Pos() token.Pos { return d.VarPos }

// ---- Type expressions ----

type TypeExpr interface {
	Node
	typeExpr()
}

// NamedType is a builtin type keyword (i8..u64, bool) or a struct name.
type NamedType struct {
	NamePos token.Pos
	Name    string
}

type PtrType struct {
	StarPos token.Pos
	Elem    TypeExpr
}

type ArrayType struct {
	LbrackPos token.Pos
	Len       uint64
	Elem      TypeExpr
}

func (t *NamedType) Pos() token.Pos { return t.NamePos }
func (t *PtrType) Pos() token.Pos   { return t.StarPos }
func (t *ArrayType) Pos() token.Pos { return t.LbrackPos }
func (*NamedType) typeExpr()        {}
func (*PtrType) typeExpr()          {}
func (*ArrayType) typeExpr()        {}

// ---- Statements ----

type Stmt interface {
	Node
	stmt()
}

type Block struct {
	LbracePos token.Pos
	Stmts     []Stmt
}

type AssignStmt struct {
	LHS Expr
	RHS Expr
}

type ExprStmt struct {
	X Expr // must be a call
}

type IfStmt struct {
	IfPos token.Pos
	Cond  Expr
	Then  *Block
	Else  Stmt // *IfStmt, *Block, or nil
}

type WhileStmt struct {
	WhilePos token.Pos
	Cond     Expr
	Body     *Block
}

type ReturnStmt struct {
	ReturnPos token.Pos
	X         Expr // nil for bare return
}

type BreakStmt struct{ KwPos token.Pos }
type ContinueStmt struct{ KwPos token.Pos }

func (s *Block) Pos() token.Pos        { return s.LbracePos }
func (s *AssignStmt) Pos() token.Pos   { return s.LHS.Pos() }
func (s *ExprStmt) Pos() token.Pos     { return s.X.Pos() }
func (s *IfStmt) Pos() token.Pos       { return s.IfPos }
func (s *WhileStmt) Pos() token.Pos    { return s.WhilePos }
func (s *ReturnStmt) Pos() token.Pos   { return s.ReturnPos }
func (s *BreakStmt) Pos() token.Pos    { return s.KwPos }
func (s *ContinueStmt) Pos() token.Pos { return s.KwPos }

func (*Block) stmt()        {}
func (*VarDecl) stmt()      {}
func (*AssignStmt) stmt()   {}
func (*ExprStmt) stmt()     {}
func (*IfStmt) stmt()       {}
func (*WhileStmt) stmt()    {}
func (*ReturnStmt) stmt()   {}
func (*BreakStmt) stmt()    {}
func (*ContinueStmt) stmt() {}

// ---- Expressions ----

type Expr interface {
	Node
	Type() types.Type
	SetType(types.Type)
	expr()
}

// exprBase carries the type annotation the checker attaches to every
// expression node.
type exprBase struct {
	typ types.Type
}

func (b *exprBase) Type() types.Type     { return b.typ }
func (b *exprBase) SetType(t types.Type) { b.typ = t }
func (*exprBase) expr()                  {}

type IntLit struct {
	exprBase
	LitPos token.Pos
	Val    uint64
}

type CharLit struct {
	exprBase
	LitPos token.Pos
	Val    byte
}

type StrLit struct {
	exprBase
	LitPos token.Pos
	Val    string
}

type BoolLit struct {
	exprBase
	LitPos token.Pos
	Val    bool
}

type Ident struct {
	exprBase
	NamePos token.Pos
	Name    string
	Obj     *Object
}

type ParenExpr struct {
	exprBase
	LparenPos token.Pos
	X         Expr
}

type UnaryExpr struct {
	exprBase
	OpPos token.Pos
	Op    token.Kind // MINUS, NOT, AMP, STAR
	X     Expr
}

type BinaryExpr struct {
	exprBase
	Op   token.Kind
	X, Y Expr
}

type CastExpr struct {
	exprBase
	X      Expr
	ToType TypeExpr
}

type CallExpr struct {
	exprBase
	Fun  *Ident
	Args []Expr
}

type IndexExpr struct {
	exprBase
	X     Expr
	Index Expr
}

type FieldExpr struct {
	exprBase
	X       Expr
	Name    string
	NamePos token.Pos
}

type FieldInit struct {
	NamePos token.Pos
	Name    string
	Value   Expr
}

type StructLit struct {
	exprBase
	NamePos token.Pos
	Name    string
	Inits   []FieldInit
	Obj     *Object // resolved struct name
}

func (e *IntLit) Pos() token.Pos     { return e.LitPos }
func (e *CharLit) Pos() token.Pos    { return e.LitPos }
func (e *StrLit) Pos() token.Pos     { return e.LitPos }
func (e *BoolLit) Pos() token.Pos    { return e.LitPos }
func (e *Ident) Pos() token.Pos      { return e.NamePos }
func (e *ParenExpr) Pos() token.Pos  { return e.LparenPos }
func (e *UnaryExpr) Pos() token.Pos  { return e.OpPos }
func (e *BinaryExpr) Pos() token.Pos { return e.X.Pos() }
func (e *CastExpr) Pos() token.Pos   { return e.X.Pos() }
func (e *CallExpr) Pos() token.Pos   { return e.Fun.Pos() }
func (e *IndexExpr) Pos() token.Pos  { return e.X.Pos() }
func (e *FieldExpr) Pos() token.Pos  { return e.X.Pos() }
func (e *StructLit) Pos() token.Pos  { return e.NamePos }
