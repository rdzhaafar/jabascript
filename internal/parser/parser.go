// Package parser builds an AST from a token stream.
//
// Recursive descent for declarations and statements, a Pratt loop for
// expressions. Two spots where the parser is smarter than the grammar as
// written, per the design doc:
//
//   - SimpleStmt parses an expression, and only then decides between
//     assignment (a following `=`, LHS must be an lvalue) and an expression
//     statement (which must be a call).
//   - Struct literals are suppressed while parsing an `if` or `while`
//     condition (the noStructLit flag), resolving the `if p == Point{...} {`
//     ambiguity the same way Go does.
package parser

import (
	"fmt"

	"jabascript/internal/ast"
	"jabascript/internal/token"
)

type Error struct {
	Pos token.Pos
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Pos, e.Msg) }

type parser struct {
	toks        []token.Token
	pos         int
	noStructLit bool
}

// Parse parses a whole source file.
func Parse(toks []token.Token) (f *ast.File, err error) {
	p := &parser{toks: toks}
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(*Error); ok {
				f, err = nil, pe
				return
			}
			panic(r)
		}
	}()
	f = p.parseFile()
	return f, nil
}

func (p *parser) cur() token.Token     { return p.toks[p.pos] }
func (p *parser) at(k token.Kind) bool { return p.cur().Kind == k }

func (p *parser) next() token.Token {
	t := p.toks[p.pos]
	if t.Kind != token.EOF {
		p.pos++
	}
	return t
}

func (p *parser) errorf(pos token.Pos, format string, args ...any) {
	panic(&Error{Pos: pos, Msg: fmt.Sprintf(format, args...)})
}

func (p *parser) expect(k token.Kind) token.Token {
	if !p.at(k) {
		p.errorf(p.cur().Pos, "expected %s, found %s", k, p.describe(p.cur()))
	}
	return p.next()
}

func (p *parser) describe(t token.Token) string {
	switch t.Kind {
	case token.IDENT, token.INT:
		return fmt.Sprintf("%s %q", t.Kind, t.Lit)
	default:
		return t.Kind.String()
	}
}

// ---- Declarations ----

func (p *parser) parseFile() *ast.File {
	f := &ast.File{}
	for !p.at(token.EOF) {
		switch p.cur().Kind {
		case token.FN:
			f.Decls = append(f.Decls, p.parseFunc(false, false, p.cur().Pos))
		case token.EXTERN:
			pos := p.next().Pos
			if !p.at(token.FN) {
				p.errorf(p.cur().Pos, "expected fn after extern")
			}
			f.Decls = append(f.Decls, p.parseFunc(true, false, pos))
		case token.EXPORT:
			pos := p.next().Pos
			if !p.at(token.FN) {
				p.errorf(p.cur().Pos, "expected fn after export")
			}
			f.Decls = append(f.Decls, p.parseFunc(false, true, pos))
		case token.STRUCT:
			f.Decls = append(f.Decls, p.parseStruct())
		case token.VAR:
			f.Decls = append(f.Decls, p.parseVarDecl())
		default:
			p.errorf(p.cur().Pos, "expected a top-level declaration (fn, extern, export, struct, or var), found %s", p.describe(p.cur()))
		}
	}
	return f
}

func (p *parser) parseFunc(extern, exported bool, pos token.Pos) *ast.FuncDecl {
	p.expect(token.FN)
	name := p.expect(token.IDENT)
	d := &ast.FuncDecl{FnPos: pos, Name: name.Lit, Extern: extern, Export: exported}
	p.expect(token.LPAREN)
	for !p.at(token.RPAREN) {
		pname := p.expect(token.IDENT)
		p.expect(token.COLON)
		typ := p.parseType()
		d.Params = append(d.Params, &ast.Param{NamePos: pname.Pos, Name: pname.Lit, Type: typ})
		if !p.at(token.COMMA) {
			break
		}
		p.next()
	}
	p.expect(token.RPAREN)
	if p.at(token.ARROW) {
		p.next()
		d.RetType = p.parseType()
	}
	if extern {
		p.expect(token.SEMI)
	} else {
		d.Body = p.parseBlock()
	}
	return d
}

func (p *parser) parseStruct() *ast.StructDecl {
	pos := p.expect(token.STRUCT).Pos
	name := p.expect(token.IDENT)
	d := &ast.StructDecl{StructPos: pos, Name: name.Lit}
	p.expect(token.LBRACE)
	for !p.at(token.RBRACE) {
		fname := p.expect(token.IDENT)
		p.expect(token.COLON)
		typ := p.parseType()
		p.expect(token.COMMA)
		d.Fields = append(d.Fields, ast.FieldDecl{NamePos: fname.Pos, Name: fname.Lit, Type: typ})
	}
	p.expect(token.RBRACE)
	return d
}

func (p *parser) parseVarDecl() *ast.VarDecl {
	pos := p.expect(token.VAR).Pos
	name := p.expect(token.IDENT)
	p.expect(token.COLON)
	typ := p.parseType()
	d := &ast.VarDecl{VarPos: pos, Name: name.Lit, Type: typ}
	if p.at(token.ASSIGN) {
		p.next()
		d.Init = p.parseExpr()
	}
	p.expect(token.SEMI)
	return d
}

// ---- Types ----

func (p *parser) parseType() ast.TypeExpr {
	t := p.cur()
	switch {
	case token.IsTypeKeyword(t.Kind):
		p.next()
		return &ast.NamedType{NamePos: t.Pos, Name: t.Kind.String()}
	case t.Kind == token.IDENT:
		p.next()
		return &ast.NamedType{NamePos: t.Pos, Name: t.Lit}
	case t.Kind == token.STAR:
		p.next()
		return &ast.PtrType{StarPos: t.Pos, Elem: p.parseType()}
	case t.Kind == token.LBRACK:
		p.next()
		n := p.expect(token.INT)
		if n.Val == 0 {
			p.errorf(n.Pos, "array length must be a positive integer literal")
		}
		p.expect(token.RBRACK)
		return &ast.ArrayType{LbrackPos: t.Pos, Len: n.Val, Elem: p.parseType()}
	}
	p.errorf(t.Pos, "expected a type, found %s", p.describe(t))
	return nil
}

// ---- Statements ----

func (p *parser) parseBlock() *ast.Block {
	b := &ast.Block{LbracePos: p.expect(token.LBRACE).Pos}
	for !p.at(token.RBRACE) {
		if p.at(token.EOF) {
			p.errorf(p.cur().Pos, "unexpected end of file: unclosed block")
		}
		b.Stmts = append(b.Stmts, p.parseStmt())
	}
	p.expect(token.RBRACE)
	return b
}

func (p *parser) parseStmt() ast.Stmt {
	t := p.cur()
	switch t.Kind {
	case token.VAR:
		return p.parseVarDecl()
	case token.IF:
		return p.parseIf()
	case token.WHILE:
		pos := p.next().Pos
		cond := p.parseCond()
		body := p.parseBlock()
		return &ast.WhileStmt{WhilePos: pos, Cond: cond, Body: body}
	case token.RETURN:
		pos := p.next().Pos
		s := &ast.ReturnStmt{ReturnPos: pos}
		if !p.at(token.SEMI) {
			s.X = p.parseExpr()
		}
		p.expect(token.SEMI)
		return s
	case token.BREAK:
		p.next()
		p.expect(token.SEMI)
		return &ast.BreakStmt{KwPos: t.Pos}
	case token.CONTINUE:
		p.next()
		p.expect(token.SEMI)
		return &ast.ContinueStmt{KwPos: t.Pos}
	case token.LBRACE:
		return p.parseBlock()
	}
	return p.parseSimpleStmt()
}

func (p *parser) parseIf() ast.Stmt {
	pos := p.expect(token.IF).Pos
	cond := p.parseCond()
	then := p.parseBlock()
	s := &ast.IfStmt{IfPos: pos, Cond: cond, Then: then}
	if p.at(token.ELSE) {
		p.next()
		if p.at(token.IF) {
			s.Else = p.parseIf()
		} else {
			s.Else = p.parseBlock()
		}
	}
	return s
}

// parseCond parses an if/while condition with struct literals suppressed,
// so the `{` that follows is unambiguously the statement body.
func (p *parser) parseCond() ast.Expr {
	saved := p.noStructLit
	p.noStructLit = true
	e := p.parseExpr()
	p.noStructLit = saved
	return e
}

// parseSimpleStmt handles `Expr = Expr ;` (assignment) and `Expr ;` (which
// must be a call).
func (p *parser) parseSimpleStmt() ast.Stmt {
	lhs := p.parseExpr()
	if p.at(token.ASSIGN) {
		p.next()
		if !isLvalue(lhs) {
			p.errorf(lhs.Pos(), "left side of assignment must be an lvalue (a variable, field, index, or dereference)")
		}
		rhs := p.parseExpr()
		p.expect(token.SEMI)
		return &ast.AssignStmt{LHS: lhs, RHS: rhs}
	}
	if _, ok := lhs.(*ast.CallExpr); !ok {
		p.errorf(lhs.Pos(), "only calls may be used as statements")
	}
	p.expect(token.SEMI)
	return &ast.ExprStmt{X: lhs}
}

func isLvalue(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident, *ast.FieldExpr, *ast.IndexExpr:
		return true
	case *ast.UnaryExpr:
		return e.Op == token.STAR
	}
	return false
}

// ---- Expressions (Pratt loop) ----

// Binding powers, one entry per level of the precedence table. An operator
// binds in the loop when its power is >= minBP, and left associativity
// comes from recursing with power+1.
var infixBP = map[token.Kind]int{
	token.OROR:   1,
	token.ANDAND: 2,
	token.EQ:     3, token.NEQ: 3,
	token.LT: 4, token.LE: 4, token.GT: 4, token.GE: 4,
	token.PLUS: 5, token.MINUS: 5,
	token.STAR: 6, token.SLASH: 6, token.PERCENT: 6,
	token.AS: 7,
}

const unaryBP = 8

func (p *parser) parseExpr() ast.Expr { return p.parseExprBP(1) }

func (p *parser) parseExprBP(minBP int) ast.Expr {
	var lhs ast.Expr
	t := p.cur()
	switch t.Kind {
	case token.MINUS, token.NOT, token.AMP, token.STAR:
		p.next()
		x := p.parseExprBP(unaryBP)
		lhs = &ast.UnaryExpr{OpPos: t.Pos, Op: t.Kind, X: x}
	default:
		lhs = p.parsePostfix()
	}

	for {
		op := p.cur().Kind
		bp, ok := infixBP[op]
		if !ok || bp < minBP {
			return lhs
		}
		p.next()
		if op == token.AS {
			lhs = &ast.CastExpr{X: lhs, ToType: p.parseType()}
			continue
		}
		rhs := p.parseExprBP(bp + 1)
		lhs = &ast.BinaryExpr{Op: op, X: lhs, Y: rhs}
	}
}

func (p *parser) parsePostfix() ast.Expr {
	e := p.parsePrimary()
	for {
		switch p.cur().Kind {
		case token.LPAREN:
			fun, ok := e.(*ast.Ident)
			if !ok {
				p.errorf(p.cur().Pos, "only named functions can be called")
			}
			p.next()
			// The closing `)` disambiguates, so call arguments re-enable
			// struct literals inside a condition, exactly as parentheses do.
			saved := p.noStructLit
			p.noStructLit = false
			call := &ast.CallExpr{Fun: fun}
			for !p.at(token.RPAREN) {
				call.Args = append(call.Args, p.parseExpr())
				if !p.at(token.COMMA) {
					break
				}
				p.next()
			}
			p.noStructLit = saved
			p.expect(token.RPAREN)
			e = call
		case token.LBRACK:
			p.next()
			// As with call arguments, `]` disambiguates.
			saved := p.noStructLit
			p.noStructLit = false
			idx := p.parseExpr()
			p.noStructLit = saved
			p.expect(token.RBRACK)
			e = &ast.IndexExpr{X: e, Index: idx}
		case token.DOT:
			p.next()
			name := p.expect(token.IDENT)
			e = &ast.FieldExpr{X: e, Name: name.Lit, NamePos: name.Pos}
		default:
			return e
		}
	}
}

func (p *parser) parsePrimary() ast.Expr {
	t := p.cur()
	switch t.Kind {
	case token.INT:
		p.next()
		return &ast.IntLit{LitPos: t.Pos, Val: t.Val}
	case token.CHAR:
		p.next()
		return &ast.CharLit{LitPos: t.Pos, Val: byte(t.Val)}
	case token.STRING:
		p.next()
		return &ast.StrLit{LitPos: t.Pos, Val: t.Str}
	case token.TRUE, token.FALSE:
		p.next()
		return &ast.BoolLit{LitPos: t.Pos, Val: t.Kind == token.TRUE}
	case token.IDENT:
		p.next()
		if p.at(token.LBRACE) && !p.noStructLit {
			return p.parseStructLit(t)
		}
		return &ast.Ident{NamePos: t.Pos, Name: t.Lit}
	case token.LPAREN:
		p.next()
		// Parentheses re-enable struct literals inside a condition,
		// exactly as in Go: `if (p == Point{x: 1, y: 2}) { ... }`.
		saved := p.noStructLit
		p.noStructLit = false
		e := p.parseExpr()
		p.noStructLit = saved
		p.expect(token.RPAREN)
		return &ast.ParenExpr{LparenPos: t.Pos, X: e}
	}
	p.errorf(t.Pos, "expected an expression, found %s", p.describe(t))
	return nil
}

func (p *parser) parseStructLit(name token.Token) ast.Expr {
	lit := &ast.StructLit{NamePos: name.Pos, Name: name.Lit}
	p.expect(token.LBRACE)
	for !p.at(token.RBRACE) {
		fname := p.expect(token.IDENT)
		p.expect(token.COLON)
		saved := p.noStructLit
		p.noStructLit = false
		val := p.parseExpr()
		p.noStructLit = saved
		lit.Inits = append(lit.Inits, ast.FieldInit{NamePos: fname.Pos, Name: fname.Lit, Value: val})
		if !p.at(token.COMMA) {
			break
		}
		p.next()
	}
	p.expect(token.RBRACE)
	return lit
}
