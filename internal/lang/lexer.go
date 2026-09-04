// Package lang is the commander query language: a pipeline of steps over the
// ConfigHub where grammar (`Unit | in * | where … | columns … | order by …`),
// with a SQL SELECT form accepted as an on-ramp. Both parse to the same AST. The lexer is deliberately permissive about what an
// identifier is, because the language has no arithmetic: `-`, `/` and `.` are
// identifier characters, so `ops-home/by-target`, `Values.Image/container-image`
// and `Data.spec.containers.?name=nginx.image` each lex as one token.
package lang

import (
	"fmt"
	"strings"
)

type TokKind int

const (
	EOF TokKind = iota
	IDENT
	STRING
	NUMBER
	OP    // comparison operator: = != < > <= >= ~ ~* !~ !~* ~~ !~~ ?
	PUNCT // ( ) , ; * |
)

type Token struct {
	Kind TokKind
	Text string
	Pos  int
}

func (t Token) String() string {
	switch t.Kind {
	case EOF:
		return "end of statement"
	case STRING:
		return fmt.Sprintf("'%s'", t.Text)
	default:
		return t.Text
	}
}

// Is reports whether the token is the given keyword or punctuation, case-insensitively.
func (t Token) Is(s string) bool {
	if t.Kind != IDENT && t.Kind != PUNCT && t.Kind != OP {
		return false
	}
	return strings.EqualFold(t.Text, s)
}

// ops in longest-match order.
var ops = []string{"!~~", "!~*", "~~", "~*", "!~", "<=", ">=", "!=", "~", "<", ">", "=", "?"}

type LexError struct {
	Pos int
	Msg string
}

func (e *LexError) Error() string { return fmt.Sprintf("at %d: %s", e.Pos, e.Msg) }

func Lex(src string) ([]Token, error) {
	var toks []Token
	i := 0
	n := len(src)
	for i < n {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && i+1 < n && src[i+1] == '-':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '\'':
			start := i
			i++
			for i < n && src[i] != '\'' {
				if src[i] == '"' || src[i] == '\\' {
					return nil, &LexError{i, "string literals may not contain quotes or backslashes (server rule)"}
				}
				i++
			}
			if i >= n {
				return nil, &LexError{start, "unterminated string literal"}
			}
			toks = append(toks, Token{STRING, src[start+1 : i], start})
			i++
		case c >= '0' && c <= '9':
			start := i
			for i < n && src[i] >= '0' && src[i] <= '9' {
				i++
			}
			toks = append(toks, Token{NUMBER, src[start:i], start})
		case isIdentStart(c):
			start := i
			i++
			for i < n {
				d := src[i]
				if isIdentCont(d) || (d == '|' && src[i-1] == '.') {
					i++
					continue
				}
				// `~0` / `~1` JSON-pointer escapes inside a data path.
				if d == '~' && i+1 < n && (src[i+1] == '0' || src[i+1] == '1') {
					i += 2
					continue
				}
				// `.?key=value` associative segment: runs to the next dot or whitespace.
				if d == '?' && src[i-1] == '.' {
					for i < n && src[i] != '.' && src[i] != ' ' && src[i] != ')' && src[i] != ',' && src[i] != ';' {
						i++
					}
					continue
				}
				break
			}
			toks = append(toks, Token{IDENT, src[start:i], start})
		case c == '(' || c == ')' || c == ',' || c == ';' || c == '*' || c == '|':
			toks = append(toks, Token{PUNCT, string(c), i})
			i++
		default:
			matched := false
			for _, op := range ops {
				if strings.HasPrefix(src[i:], op) {
					toks = append(toks, Token{OP, op, i})
					i += len(op)
					matched = true
					break
				}
			}
			if !matched {
				return nil, &LexError{i, fmt.Sprintf("unexpected character %q", c)}
			}
		}
	}
	toks = append(toks, Token{EOF, "", n})
	return toks, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '/' || c == '*' || c == ':' || c == '@'
}
