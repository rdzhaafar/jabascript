// Package types defines JabaScript's type representations and memory layout.
//
// The compilation target is wasm32, so pointers are 4 bytes. Everything else
// follows the design document: iN/uN are N/8 bytes aligned to their size,
// bool is 1 byte, struct fields are laid out in declaration order with
// natural alignment, and structs are nominally typed (two *Struct values are
// the same type only if they are the same object).
package types

import (
	"fmt"
	"strings"
)

const PointerSize = 4 // wasm32

type Type interface {
	Size() int
	Align() int
	String() string
}

type BasicKind int

const (
	I8 BasicKind = iota
	I16
	I32
	I64
	U8
	U16
	U32
	U64
	Bool
)

type Basic struct {
	Kind BasicKind
	name string
	size int
}

var (
	TypI8   = &Basic{I8, "i8", 1}
	TypI16  = &Basic{I16, "i16", 2}
	TypI32  = &Basic{I32, "i32", 4}
	TypI64  = &Basic{I64, "i64", 8}
	TypU8   = &Basic{U8, "u8", 1}
	TypU16  = &Basic{U16, "u16", 2}
	TypU32  = &Basic{U32, "u32", 4}
	TypU64  = &Basic{U64, "u64", 8}
	TypBool = &Basic{Bool, "bool", 1}
)

func (b *Basic) Size() int      { return b.size }
func (b *Basic) Align() int     { return b.size }
func (b *Basic) String() string { return b.name }

type Pointer struct {
	Elem Type
}

func (p *Pointer) Size() int      { return PointerSize }
func (p *Pointer) Align() int     { return PointerSize }
func (p *Pointer) String() string { return "*" + p.Elem.String() }

type Array struct {
	Len  int
	Elem Type
}

func (a *Array) Size() int      { return a.Len * a.Elem.Size() }
func (a *Array) Align() int     { return a.Elem.Align() }
func (a *Array) String() string { return fmt.Sprintf("[%d]%s", a.Len, a.Elem.String()) }

type Field struct {
	Name   string
	Type   Type
	Offset int
}

type Struct struct {
	Name   string
	Fields []Field
	size   int
	align  int
}

func (s *Struct) Size() int      { return s.size }
func (s *Struct) Align() int     { return s.align }
func (s *Struct) String() string { return s.Name }

func (s *Struct) Field(name string) *Field {
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i]
		}
	}
	return nil
}

// SetFields lays out the given fields in declaration order with natural
// alignment and records the resulting size and alignment.
func (s *Struct) SetFields(fields []Field) {
	off, align := 0, 1
	for i := range fields {
		fa := fields[i].Type.Align()
		off = AlignUp(off, fa)
		fields[i].Offset = off
		off += fields[i].Type.Size()
		if fa > align {
			align = fa
		}
	}
	s.Fields = fields
	s.align = align
	s.size = AlignUp(off, align)
}

func AlignUp(n, a int) int { return (n + a - 1) &^ (a - 1) }

// Same reports type identity. Structs are nominal: identity is pointer
// equality. Everything else is structural.
func Same(a, b Type) bool {
	switch a := a.(type) {
	case *Basic:
		b, ok := b.(*Basic)
		return ok && a.Kind == b.Kind
	case *Pointer:
		b, ok := b.(*Pointer)
		return ok && Same(a.Elem, b.Elem)
	case *Array:
		b, ok := b.(*Array)
		return ok && a.Len == b.Len && Same(a.Elem, b.Elem)
	case *Struct:
		return a == b
	}
	return false
}

func IsInteger(t Type) bool {
	b, ok := t.(*Basic)
	return ok && b.Kind != Bool
}

func IsSigned(t Type) bool {
	b, ok := t.(*Basic)
	return ok && b.Kind <= I64
}

func IsBool(t Type) bool    { return t == TypBool }
func IsPointer(t Type) bool { _, ok := t.(*Pointer); return ok }

// IsAggregate reports whether t is a struct or array — a type that lives in
// memory and is passed and returned by address rather than as a wasm value.
func IsAggregate(t Type) bool {
	switch t.(type) {
	case *Struct, *Array:
		return true
	}
	return false
}

// Wasm returns the wasm value type ("i32" or "i64") that holds a scalar of
// type t. Aggregates are represented by an i32 address.
func Wasm(t Type) string {
	if b, ok := t.(*Basic); ok && (b.Kind == I64 || b.Kind == U64) {
		return "i64"
	}
	return "i32"
}

// MinMax returns the value range of an integer type as (min, max) where min
// is the signed lower bound and max is the unsigned upper bound magnitude.
func MinMax(t Type) (min int64, max uint64) {
	switch t.(*Basic).Kind {
	case I8:
		return -1 << 7, 1<<7 - 1
	case I16:
		return -1 << 15, 1<<15 - 1
	case I32:
		return -1 << 31, 1<<31 - 1
	case I64:
		return -1 << 63, 1<<63 - 1
	case U8:
		return 0, 1<<8 - 1
	case U16:
		return 0, 1<<16 - 1
	case U32:
		return 0, 1<<32 - 1
	case U64:
		return 0, 1<<64 - 1
	}
	panic("not an integer type")
}

// FuncSig describes a function's type for checking and codegen.
type FuncSig struct {
	Params []Type
	Ret    Type // nil when the function returns nothing
}

func (f *FuncSig) String() string {
	var b strings.Builder
	b.WriteString("fn(")
	for i, p := range f.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.String())
	}
	b.WriteString(")")
	if f.Ret != nil {
		b.WriteString(" -> ")
		b.WriteString(f.Ret.String())
	}
	return b.String()
}
