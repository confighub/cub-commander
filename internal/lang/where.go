package lang

import (
	"fmt"
	"strings"
)

// ServerWhere serialises a WHERE expression into the server's where string.
// It assumes checkServerWhere has passed.
func ServerWhere(e Expr) string {
	if e == nil {
		return ""
	}
	var terms []string
	collect(e, &terms)
	return strings.Join(terms, " AND ")
}

func collect(e Expr, out *[]string) {
	switch x := e.(type) {
	case And:
		collect(x.L, out)
		collect(x.R, out)
	case Cmp:
		*out = append(*out, cmpString(x))
	}
}

func cmpString(c Cmp) string {
	s := ExprString(c.Left) + " " + c.Op
	if c.Right != nil {
		s += " " + ExprString(c.Right)
	}
	if c.Truth != "" {
		s += " " + c.Truth
	}
	return s
}

// ExprString renders an expression in the dialect (which, for terms, is the server syntax).
func ExprString(e Expr) string {
	switch x := e.(type) {
	case Ref:
		if x.Len {
			return "LEN(" + x.Path + ")"
		}
		return x.Path
	case Lit:
		switch x.Kind {
		case LitString:
			return "'" + x.S + "'"
		case LitNumber:
			return fmt.Sprint(x.N)
		default:
			return fmt.Sprint(x.B)
		}
	case ListLit:
		parts := make([]string, len(x.Items))
		for i, it := range x.Items {
			parts[i] = ExprString(it)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case Call:
		parts := make([]string, len(x.Args))
		for i, a := range x.Args {
			parts[i] = ExprString(a)
		}
		return x.Name + "(" + strings.Join(parts, ", ") + ")"
	case Star:
		return "*"
	case Cmp:
		return cmpString(x)
	case And:
		return ExprString(x.L) + " AND " + ExprString(x.R)
	case Or:
		return "(" + ExprString(x.L) + " OR " + ExprString(x.R) + ")"
	case Not:
		return "NOT " + ExprString(x.X)
	}
	return "?"
}

// Refs returns every attribute reference in an expression.
func Refs(e Expr) []Ref {
	var out []Ref
	walk(e, func(x Expr) {
		if r, ok := x.(Ref); ok {
			out = append(out, r)
		}
	})
	return out
}

func walk(e Expr, f func(Expr)) {
	if e == nil {
		return
	}
	f(e)
	switch x := e.(type) {
	case Cmp:
		walk(x.Left, f)
		walk(x.Right, f)
	case And:
		walk(x.L, f)
		walk(x.R, f)
	case Or:
		walk(x.L, f)
		walk(x.R, f)
	case Not:
		walk(x.X, f)
	case Call:
		for _, a := range x.Args {
			walk(a, f)
		}
	}
}
