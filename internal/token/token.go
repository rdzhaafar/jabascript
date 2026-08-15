// Package token defines the lexical tokens of JabaScript and source positions.
package token

import "fmt"

type Kind int

const (
	EOF Kind = iota
	IDENT
	INT    // 42, 0xff
	CHAR   // 'a'
	STRING // "kwak"

	// Keywords.
	FN
	EXTERN
	EXPORT
	STRUCT
	VAR
	IF
	ELSE
	WHILE
	RETURN
	BREAK
	CONTINUE
	AS
	TRUE
	FALSE

	// Type keywords.
	I8
	I16
	I32
	I64
	U8
	U16
	U32
	U64
	BOOL

	// Operators and punctuation.
	PLUS    // +
	MINUS   // -
	STAR    // *
	SLASH   // /
	PERCENT // %
	EQ      // ==
	NEQ     // !=
	LT      // <
	LE      // <=
	GT      // >
	GE      // >=
	ANDAND  // &&
	OROR    // ||
	NOT     // !
	ASSIGN  // =
	AMP     // &
	DOT     // .
	COMMA   // ,
	SEMI    // ;
	COLON   // :
	ARROW   // ->
	LPAREN  // (
	RPAREN  // )
	LBRACK  // [
	RBRACK  // ]
	LBRACE  // {
	RBRACE  // }
)

var kindNames = map[Kind]string{
	EOF: "end of file", IDENT: "identifier", INT: "integer literal",
	CHAR: "character literal", STRING: "string literal",
	FN: "fn", EXTERN: "extern", EXPORT: "export", STRUCT: "struct", VAR: "var", IF: "if",
	ELSE: "else", WHILE: "while", RETURN: "return", BREAK: "break",
	CONTINUE: "continue", AS: "as", TRUE: "true", FALSE: "false",
	I8: "i8", I16: "i16", I32: "i32", I64: "i64",
	U8: "u8", U16: "u16", U32: "u32", U64: "u64", BOOL: "bool",
	PLUS: "+", MINUS: "-", STAR: "*", SLASH: "/", PERCENT: "%",
	EQ: "==", NEQ: "!=", LT: "<", LE: "<=", GT: ">", GE: ">=",
	ANDAND: "&&", OROR: "||", NOT: "!", ASSIGN: "=", AMP: "&",
	DOT: ".", COMMA: ",", SEMI: ";", COLON: ":", ARROW: "->",
	LPAREN: "(", RPAREN: ")", LBRACK: "[", RBRACK: "]", LBRACE: "{", RBRACE: "}",
}

func (k Kind) String() string { return kindNames[k] }

// Keywords maps identifier spellings to keyword kinds.
var Keywords = map[string]Kind{
	"fn": FN, "extern": EXTERN, "export": EXPORT, "struct": STRUCT, "var": VAR, "if": IF,
	"else": ELSE, "while": WHILE, "return": RETURN, "break": BREAK,
	"continue": CONTINUE, "as": AS, "true": TRUE, "false": FALSE,
	"i8": I8, "i16": I16, "i32": I32, "i64": I64,
	"u8": U8, "u16": U16, "u32": U32, "u64": U64, "bool": BOOL,
}

// IsTypeKeyword reports whether k names a builtin type.
func IsTypeKeyword(k Kind) bool { return k >= I8 && k <= BOOL }

type Pos struct {
	Line int // 1-based
	Col  int // 1-based, in bytes
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

type Token struct {
	Kind Kind
	Pos  Pos
	Lit  string // raw text for IDENT, INT, STRING, CHAR
	Val  uint64 // decoded value for INT and CHAR
	Str  string // decoded value for STRING
}
