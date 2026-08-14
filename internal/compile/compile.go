// Package compile runs the whole pipeline:
//
//	source → lexer → tokens → parser → AST → resolver/checker → codegen → .wat
package compile

import (
	"jabascript/internal/codegen"
	"jabascript/internal/lexer"
	"jabascript/internal/parser"
	"jabascript/internal/sema"
)

// Compile lowers JabaScript source to a complete WAT module.
func Compile(src string) (string, error) {
	toks, err := lexer.Lex(src)
	if err != nil {
		return "", err
	}
	file, err := parser.Parse(toks)
	if err != nil {
		return "", err
	}
	if err := sema.Check(file); err != nil {
		return "", err
	}
	return codegen.Generate(file)
}
