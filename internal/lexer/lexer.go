// Package lexer turns JabaScript source text into tokens.
//
// The lexer is hand-written: one pass, one byte of lookahead, no
// generator. Comments are `//` to end of line and whitespace is
// insignificant.
package lexer

import (
	"fmt"
	"strconv"

	"jabascript/internal/token"
)

type Error struct {
	Pos token.Pos
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Pos, e.Msg) }

type Lexer struct {
	src  string
	off  int
	line int
	col  int
}

func New(src string) *Lexer {
	return &Lexer{src: src, line: 1, col: 1}
}

// Lex tokenizes the whole input. The returned slice always ends with EOF.
func Lex(src string) ([]token.Token, error) {
	l := New(src)
	var toks []token.Token
	for {
		t, err := l.Next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.Kind == token.EOF {
			return toks, nil
		}
	}
}

func (l *Lexer) peek() byte {
	if l.off >= len(l.src) {
		return 0
	}
	return l.src[l.off]
}

func (l *Lexer) peek2() byte {
	if l.off+1 >= len(l.src) {
		return 0
	}
	return l.src[l.off+1]
}

func (l *Lexer) advance() byte {
	c := l.src[l.off]
	l.off++
	if c == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return c
}

func (l *Lexer) pos() token.Pos { return token.Pos{Line: l.line, Col: l.col} }

func (l *Lexer) errorf(pos token.Pos, format string, args ...any) error {
	return &Error{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

func (l *Lexer) skipSpaceAndComments() {
	for l.off < len(l.src) {
		c := l.peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			l.advance()
		case c == '/' && l.peek2() == '/':
			for l.off < len(l.src) && l.peek() != '\n' {
				l.advance()
			}
		default:
			return
		}
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isIdentCont(c byte) bool { return isIdentStart(c) || isDigit(c) }

func (l *Lexer) Next() (token.Token, error) {
	l.skipSpaceAndComments()
	pos := l.pos()
	if l.off >= len(l.src) {
		return token.Token{Kind: token.EOF, Pos: pos}, nil
	}

	c := l.peek()
	switch {
	case isIdentStart(c):
		start := l.off
		for l.off < len(l.src) && isIdentCont(l.peek()) {
			l.advance()
		}
		lit := l.src[start:l.off]
		if kw, ok := token.Keywords[lit]; ok {
			return token.Token{Kind: kw, Pos: pos, Lit: lit}, nil
		}
		return token.Token{Kind: token.IDENT, Pos: pos, Lit: lit}, nil

	case isDigit(c):
		return l.lexNumber(pos)

	case c == '\'':
		return l.lexChar(pos)

	case c == '"':
		return l.lexString(pos)
	}

	l.advance()
	two := func(next byte, ifTwo, ifOne token.Kind) (token.Token, error) {
		if l.peek() == next {
			l.advance()
			return token.Token{Kind: ifTwo, Pos: pos}, nil
		}
		return token.Token{Kind: ifOne, Pos: pos}, nil
	}

	switch c {
	case '+':
		return token.Token{Kind: token.PLUS, Pos: pos}, nil
	case '-':
		return two('>', token.ARROW, token.MINUS)
	case '*':
		return token.Token{Kind: token.STAR, Pos: pos}, nil
	case '/':
		return token.Token{Kind: token.SLASH, Pos: pos}, nil
	case '%':
		return token.Token{Kind: token.PERCENT, Pos: pos}, nil
	case '=':
		return two('=', token.EQ, token.ASSIGN)
	case '!':
		return two('=', token.NEQ, token.NOT)
	case '<':
		return two('=', token.LE, token.LT)
	case '>':
		return two('=', token.GE, token.GT)
	case '&':
		return two('&', token.ANDAND, token.AMP)
	case '|':
		if l.peek() == '|' {
			l.advance()
			return token.Token{Kind: token.OROR, Pos: pos}, nil
		}
		return token.Token{}, l.errorf(pos, "unexpected character %q (there are no bitwise operators)", "|")
	case '.':
		return token.Token{Kind: token.DOT, Pos: pos}, nil
	case ',':
		return token.Token{Kind: token.COMMA, Pos: pos}, nil
	case ';':
		return token.Token{Kind: token.SEMI, Pos: pos}, nil
	case ':':
		return token.Token{Kind: token.COLON, Pos: pos}, nil
	case '(':
		return token.Token{Kind: token.LPAREN, Pos: pos}, nil
	case ')':
		return token.Token{Kind: token.RPAREN, Pos: pos}, nil
	case '[':
		return token.Token{Kind: token.LBRACK, Pos: pos}, nil
	case ']':
		return token.Token{Kind: token.RBRACK, Pos: pos}, nil
	case '{':
		return token.Token{Kind: token.LBRACE, Pos: pos}, nil
	case '}':
		return token.Token{Kind: token.RBRACE, Pos: pos}, nil
	}
	return token.Token{}, l.errorf(pos, "unexpected character %q", string(c))
}

func (l *Lexer) lexNumber(pos token.Pos) (token.Token, error) {
	start := l.off
	if l.peek() == '0' && (l.peek2() == 'x' || l.peek2() == 'X') {
		l.advance()
		l.advance()
		if !isHexDigit(l.peek()) {
			return token.Token{}, l.errorf(pos, "hexadecimal literal needs at least one digit")
		}
		for l.off < len(l.src) && isHexDigit(l.peek()) {
			l.advance()
		}
		lit := l.src[start:l.off]
		v, err := strconv.ParseUint(lit[2:], 16, 64)
		if err != nil {
			return token.Token{}, l.errorf(pos, "integer literal %s does not fit in 64 bits", lit)
		}
		return token.Token{Kind: token.INT, Pos: pos, Lit: lit, Val: v}, nil
	}
	for l.off < len(l.src) && isDigit(l.peek()) {
		l.advance()
	}
	lit := l.src[start:l.off]
	if len(lit) > 1 && lit[0] == '0' {
		return token.Token{}, l.errorf(pos, "leading zeros are not permitted (no octal literals)")
	}
	v, err := strconv.ParseUint(lit, 10, 64)
	if err != nil {
		return token.Token{}, l.errorf(pos, "integer literal %s does not fit in 64 bits", lit)
	}
	return token.Token{Kind: token.INT, Pos: pos, Lit: lit, Val: v}, nil
}

func (l *Lexer) escape(pos token.Pos) (byte, error) {
	l.advance() // backslash
	if l.off >= len(l.src) {
		return 0, l.errorf(pos, "unterminated escape sequence")
	}
	c := l.advance()
	switch c {
	case 'n':
		return '\n', nil
	case 't':
		return '\t', nil
	case 'r':
		return '\r', nil
	case '0':
		return 0, nil
	case '\\':
		return '\\', nil
	case '\'':
		return '\'', nil
	case '"':
		return '"', nil
	}
	return 0, l.errorf(pos, "unknown escape sequence \\%s", string(c))
}

func (l *Lexer) lexChar(pos token.Pos) (token.Token, error) {
	l.advance() // opening quote
	if l.off >= len(l.src) || l.peek() == '\n' {
		return token.Token{}, l.errorf(pos, "unterminated character literal")
	}
	var v byte
	if l.peek() == '\\' {
		b, err := l.escape(pos)
		if err != nil {
			return token.Token{}, err
		}
		v = b
	} else if l.peek() == '\'' {
		return token.Token{}, l.errorf(pos, "empty character literal")
	} else {
		v = l.advance()
	}
	if l.off >= len(l.src) || l.peek() != '\'' {
		return token.Token{}, l.errorf(pos, "unterminated character literal")
	}
	l.advance()
	return token.Token{Kind: token.CHAR, Pos: pos, Val: uint64(v)}, nil
}

func (l *Lexer) lexString(pos token.Pos) (token.Token, error) {
	l.advance() // opening quote
	var buf []byte
	for {
		if l.off >= len(l.src) || l.peek() == '\n' {
			return token.Token{}, l.errorf(pos, "unterminated string literal")
		}
		if l.peek() == '"' {
			l.advance()
			return token.Token{Kind: token.STRING, Pos: pos, Str: string(buf)}, nil
		}
		if l.peek() == '\\' {
			b, err := l.escape(pos)
			if err != nil {
				return token.Token{}, err
			}
			buf = append(buf, b)
			continue
		}
		buf = append(buf, l.advance())
	}
}
