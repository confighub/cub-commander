package lang

import (
	"fmt"
	"strconv"
	"strings"
)

type ParseError struct {
	Pos  int
	Msg  string
	Hint string
}

func (e *ParseError) Error() string {
	where := ""
	if e.Pos >= 0 {
		where = fmt.Sprintf("at %d: ", e.Pos)
	}
	if e.Hint != "" {
		return fmt.Sprintf("%s%s (%s)", where, e.Msg, e.Hint)
	}
	return where + e.Msg
}

type parser struct {
	toks []Token
	i    int
}

// Parse parses one or more `;`-separated statements.
func Parse(src string) ([]Stmt, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	var out []Stmt
	for {
		for p.peek().Is(";") {
			p.next()
		}
		if p.peek().Kind == EOF {
			return out, nil
		}
		st, err := p.statement()
		if err != nil {
			return nil, err
		}
		out = append(out, st)
		if !p.peek().Is(";") && p.peek().Kind != EOF {
			return nil, p.errf("unexpected %s", p.peek())
		}
	}
}

// ParseOne parses exactly one statement.
func ParseOne(src string) (Stmt, error) {
	ss, err := Parse(src)
	if err != nil {
		return nil, err
	}
	if len(ss) != 1 {
		return nil, fmt.Errorf("expected one statement, got %d", len(ss))
	}
	return ss[0], nil
}

func (p *parser) peek() Token { return p.toks[p.i] }
func (p *parser) next() Token {
	t := p.toks[p.i]
	if t.Kind != EOF {
		p.i++
	}
	return t
}
func (p *parser) errf(format string, a ...any) error {
	return &ParseError{Pos: p.peek().Pos, Msg: fmt.Sprintf(format, a...)}
}
func (p *parser) accept(kw string) bool {
	if p.peek().Is(kw) {
		p.next()
		return true
	}
	return false
}
func (p *parser) expect(kw string) error {
	if !p.accept(kw) {
		return p.errf("expected %s, got %s", kw, p.peek())
	}
	return nil
}
func (p *parser) ident() (string, error) {
	t := p.peek()
	if t.Kind != IDENT {
		return "", p.errf("expected a name, got %s", t)
	}
	p.next()
	return t.Text, nil
}

func (p *parser) statement() (Stmt, error) {
	t := p.peek()
	switch {
	case t.Is("EXPLAIN"):
		p.next()
		inner, err := p.statement()
		if err != nil {
			return nil, err
		}
		return &ExplainStmt{Inner: inner}, nil
	case t.Is("SELECT"):
		p.next()
		return p.selectStmt(true)
	case t.Is("USE"):
		p.next()
		if p.accept("*") {
			return &UseStmt{Org: true}, nil
		}
		s, err := p.ident()
		if err != nil {
			return nil, err
		}
		return &UseStmt{Space: s}, nil
	case t.Is("SHOW"):
		p.next()
		return p.showStmt()
	case t.Is("DESCRIBE") || t.Is("DESC"):
		p.next()
		s, err := p.ident()
		if err != nil {
			return nil, err
		}
		return &DescribeStmt{Name: s}, nil
	case t.Kind == IDENT:
		return p.pipelineStmt()
	}
	return nil, p.errf("unexpected %s at start of statement", t)
}

func (p *parser) showStmt() (Stmt, error) {
	what, err := p.ident()
	if err != nil {
		return nil, err
	}
	st := &ShowStmt{What: strings.ToUpper(what)}
	switch st.What {
	case "COLUMNS", "JOINS", "LABELS":
		if p.accept("FROM") {
			if st.Arg, err = p.ident(); err != nil {
				return nil, err
			}
		}
	case "VALUES":
		if err := p.expect("OF"); err != nil {
			return nil, err
		}
		if st.Arg, err = p.ident(); err != nil {
			return nil, err
		}
	case "FUNCTIONS":
		if p.peek().Kind == IDENT && !p.peek().Is("LIKE") {
			st.Arg, _ = p.ident()
		}
	}
	return st, nil
}

// pipelineStmt: source [| in scope] [| where …] [| columns …] [| group by …] [| order by …] [| limit n]
// The pipes are optional; every step starts with a keyword.
func (p *parser) pipelineStmt() (Stmt, error) {
	st := &SelectStmt{Star: true}
	if err := p.source(st); err != nil {
		return nil, err
	}
	seenColumns := false
	for {
		p.accept("|")
		t := p.peek()
		switch {
		case t.Is("in"):
			p.next()
			if err := p.scope(st); err != nil {
				return nil, err
			}
		case t.Is("where"):
			p.next()
			e, err := p.orExpr()
			if err != nil {
				return nil, err
			}
			st.Filters = append(st.Filters, Filter{Expr: e})
			if !seenColumns {
				st.ColumnsPos = len(st.Filters)
			}
		case t.Is("columns") || t.Is("select"):
			p.next()
			seenColumns = true
			st.ColumnsPos = len(st.Filters)
			if p.accept("*") {
				st.Star = true
				st.Columns = nil
				continue
			}
			st.Star = false
			st.Columns = nil
			for {
				c, err := p.column()
				if err != nil {
					return nil, err
				}
				st.Columns = append(st.Columns, c)
				if !p.accept(",") {
					break
				}
			}
		case t.Is("browse"):
			p.next()
			if err := p.expect("BY"); err != nil {
				return nil, err
			}
			st.Browse = nil
			for {
				r, err := p.ref()
				if err != nil {
					return nil, err
				}
				st.Browse = append(st.Browse, r)
				if !p.accept(",") {
					break
				}
			}
		case t.Is("diff"):
			p.next()
			a, err := p.orExpr()
			if err != nil {
				return nil, err
			}
			if err := p.expect("vs"); err != nil {
				return nil, err
			}
			b, err := p.orExpr()
			if err != nil {
				return nil, err
			}
			d := &DiffStep{A: a, B: b}
			if p.accept("by") {
				for {
					r, err := p.ref()
					if err != nil {
						return nil, err
					}
					d.By = append(d.By, r)
					if !p.accept(",") {
						break
					}
				}
			}
			st.Diff = d
		case t.Is("group"), t.Is("order"), t.Is("limit"):
			if err := p.tail(st); err != nil {
				return nil, err
			}
		default:
			if t.Kind == EOF || t.Is(";") {
				return st, nil
			}
			return nil, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("unexpected %s", t), Hint: "steps are: in, where, columns, browse by, diff … vs …, group by, order by, limit"}
		}
	}
}

func (p *parser) source(st *SelectStmt) error {
	src, err := p.ident()
	if err != nil {
		return err
	}
	if strings.Contains(src, "/") {
		st.From.Saved = src
	} else {
		st.From.Entity = src
	}
	return nil
}

func (p *parser) scope(st *SelectStmt) error {
	if p.accept("*") {
		st.Scope = &Scope{Org: true}
		return nil
	}
	s, err := p.ident()
	if err != nil {
		return err
	}
	st.Scope = &Scope{Space: s}
	return nil
}

// selectStmt is the SQL on-ramp: SELECT cols FROM source [IN scope] [WHERE] [HAVING] [GROUP BY] [ORDER BY] [LIMIT].
func (p *parser) selectStmt(hasSelect bool) (Stmt, error) {
	st := &SelectStmt{}
	if p.accept("*") {
		st.Star = true
	} else {
		for {
			c, err := p.column()
			if err != nil {
				return nil, err
			}
			st.Columns = append(st.Columns, c)
			if !p.accept(",") {
				break
			}
		}
	}
	if err := p.expect("FROM"); err != nil {
		return nil, err
	}
	if err := p.source(st); err != nil {
		return nil, err
	}
	if p.accept("IN") {
		if err := p.scope(st); err != nil {
			return nil, err
		}
	}
	if p.accept("WHERE") {
		e, err := p.orExpr()
		if err != nil {
			return nil, err
		}
		if err := CheckServerWhere(e); err != nil {
			return nil, err
		}
		st.Filters = append(st.Filters, Filter{Expr: e})
		st.ColumnsPos = 1
	}
	if p.accept("HAVING") {
		e, err := p.orExpr()
		if err != nil {
			return nil, err
		}
		st.Filters = append(st.Filters, Filter{Expr: e, Local: true})
	}
	if err := p.tail(st); err != nil {
		return nil, err
	}
	return st, nil
}

// tail parses [GROUP BY] [ORDER BY] [LIMIT] in either form.
func (p *parser) tail(st *SelectStmt) error {
	if p.accept("GROUP") {
		if err := p.expect("BY"); err != nil {
			return err
		}
		for {
			r, err := p.ref()
			if err != nil {
				return err
			}
			st.GroupBy = append(st.GroupBy, r)
			if !p.accept(",") {
				break
			}
		}
		p.accept("|")
	}
	if p.accept("ORDER") {
		if err := p.expect("BY"); err != nil {
			return err
		}
		for {
			r, err := p.ref()
			if err != nil {
				return err
			}
			it := OrderItem{Ref: r}
			if p.accept("DESC") {
				it.Desc = true
			} else {
				p.accept("ASC")
			}
			st.OrderBy = append(st.OrderBy, it)
			if !p.accept(",") {
				break
			}
		}
		p.accept("|")
	}
	if p.accept("LIMIT") {
		t := p.next()
		if t.Kind != NUMBER {
			return p.errf("LIMIT expects a number, got %s", t)
		}
		n, _ := strconv.Atoi(t.Text)
		st.Limit = &n
	}
	return nil
}

func (p *parser) column() (Column, error) {
	e, err := p.primary()
	if err != nil {
		return Column{}, err
	}
	c := Column{Expr: e}
	if p.accept("AS") {
		if c.Alias, err = p.ident(); err != nil {
			return Column{}, err
		}
	}
	return c, nil
}

func (p *parser) ref() (Ref, error) {
	if p.peek().Is("LEN") && p.toks[p.i+1].Is("(") {
		p.next()
		p.next()
		name, err := p.ident()
		if err != nil {
			return Ref{}, err
		}
		if err := p.expect(")"); err != nil {
			return Ref{}, err
		}
		return Ref{Path: name, Len: true}, nil
	}
	name, err := p.ident()
	if err != nil {
		return Ref{}, err
	}
	return Ref{Path: name}, nil
}

// Boolean expression: OR < AND < NOT < comparison.
func (p *parser) orExpr() (Expr, error) {
	l, err := p.andExpr()
	if err != nil {
		return nil, err
	}
	for p.accept("OR") {
		r, err := p.andExpr()
		if err != nil {
			return nil, err
		}
		l = Or{l, r}
	}
	return l, nil
}

func (p *parser) andExpr() (Expr, error) {
	l, err := p.notExpr()
	if err != nil {
		return nil, err
	}
	for p.accept("AND") {
		r, err := p.notExpr()
		if err != nil {
			return nil, err
		}
		l = And{l, r}
	}
	return l, nil
}

func (p *parser) notExpr() (Expr, error) {
	if p.accept("NOT") {
		x, err := p.notExpr()
		if err != nil {
			return nil, err
		}
		return Not{x}, nil
	}
	if p.peek().Is("(") {
		// Parenthesised boolean expression. A parenthesis after IN is handled in comparison().
		p.next()
		x, err := p.orExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
		return x, nil
	}
	return p.comparison()
}

func (p *parser) comparison() (Expr, error) {
	left, err := p.primary()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	c := Cmp{Left: left}
	switch {
	case t.Kind == OP:
		c.Op = p.next().Text
	case t.Is("IS"):
		p.next()
		neg := p.accept("NOT")
		if p.accept("NULL") {
			if neg {
				c.Op = "IS NOT NULL"
			} else {
				c.Op = "IS NULL"
			}
			return c, nil
		}
		return nil, p.errf("expected NULL after IS")
	case t.Is("IN"):
		p.next()
		c.Op = "IN"
	case t.Is("LIKE"):
		p.next()
		c.Op = "LIKE"
	case t.Is("ILIKE"):
		p.next()
		c.Op = "ILIKE"
	case t.Is("NOT"):
		p.next()
		if p.accept("IN") {
			c.Op = "NOT IN"
		} else if p.accept("LIKE") {
			c.Op = "NOT LIKE"
		} else {
			return nil, p.errf("expected IN or LIKE after NOT")
		}
	default:
		// A bare boolean column in HAVING (e.g. an alias of a vet function).
		return left, nil
	}
	if c.Op == "IN" || c.Op == "NOT IN" {
		if err := p.expect("("); err != nil {
			return nil, err
		}
		var items []Lit
		for {
			l, err := p.literal()
			if err != nil {
				return nil, err
			}
			items = append(items, l)
			if !p.accept(",") {
				break
			}
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
		c.Right = ListLit{items}
	} else {
		if c.Right, err = p.primary(); err != nil {
			return nil, err
		}
	}
	if p.peek().Is("IS") {
		p.next()
		neg := p.accept("NOT")
		var v string
		if p.accept("TRUE") {
			v = "TRUE"
		} else if p.accept("FALSE") {
			v = "FALSE"
		} else {
			return nil, p.errf("expected TRUE or FALSE after IS")
		}
		if neg {
			c.Truth = "IS NOT " + v
		} else {
			c.Truth = "IS " + v
		}
	}
	return c, nil
}

func (p *parser) literal() (Lit, error) {
	t := p.next()
	switch {
	case t.Kind == STRING:
		return Lit{Kind: LitString, S: t.Text}, nil
	case t.Kind == NUMBER:
		n, _ := strconv.ParseInt(t.Text, 10, 64)
		return Lit{Kind: LitNumber, N: n}, nil
	case t.Is("TRUE"):
		return Lit{Kind: LitBool, B: true}, nil
	case t.Is("FALSE"):
		return Lit{Kind: LitBool, B: false}, nil
	}
	return Lit{}, &ParseError{Pos: t.Pos, Msg: fmt.Sprintf("expected a literal, got %s", t)}
}

// primary: literal | LEN(ref) | name(args) | name | *
func (p *parser) primary() (Expr, error) {
	t := p.peek()
	switch {
	case t.Kind == STRING || t.Kind == NUMBER || t.Is("TRUE") || t.Is("FALSE"):
		return p.literal()
	case t.Is("*"):
		p.next()
		return Star{}, nil
	case t.Kind == IDENT:
		if p.toks[p.i+1].Is("(") {
			if t.Is("LEN") {
				return p.ref()
			}
			p.next()
			p.next()
			call := Call{Name: t.Text}
			for !p.peek().Is(")") {
				if p.accept("DISTINCT") {
					call.Args = append(call.Args, Ref{Path: "DISTINCT"})
				}
				a, err := p.primary()
				if err != nil {
					return nil, err
				}
				call.Args = append(call.Args, a)
				if !p.accept(",") {
					break
				}
			}
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			return call, nil
		}
		p.next()
		return Ref{Path: t.Text}, nil
	}
	return nil, p.errf("unexpected %s", t)
}

// CheckServerWhere reports why an expression cannot go to the server's where
// grammar. The SQL form raises it as an error; the pipeline form uses it to
// decide that a where step runs locally, and EXPLAIN shows the reason.
func CheckServerWhere(e Expr) error {
	switch x := e.(type) {
	case And:
		if err := CheckServerWhere(x.L); err != nil {
			return err
		}
		return CheckServerWhere(x.R)
	case Or:
		return &ParseError{Pos: -1, Msg: "OR is not in the server where grammar", Hint: "in a pipeline this step runs locally; in SQL move it to HAVING"}
	case Not:
		return &ParseError{Pos: -1, Msg: "NOT is not in the server where grammar", Hint: "use a negated operator (!=, NOT IN, NOT LIKE, !~); in a pipeline this step runs locally, in SQL move it to HAVING"}
	case Cmp:
		if _, ok := x.Left.(Call); ok {
			return &ParseError{Pos: -1, Msg: "a function result cannot be filtered server-side", Hint: "in a pipeline this step runs locally; in SQL move it to HAVING"}
		}
		if _, ok := x.Right.(Call); ok {
			return &ParseError{Pos: -1, Msg: "a function result cannot be filtered server-side", Hint: "in a pipeline this step runs locally; in SQL move it to HAVING"}
		}
		return nil
	case Ref, Call:
		return &ParseError{Pos: -1, Msg: "a where term must be a comparison", Hint: "write `<attribute> <op> <value>`"}
	}
	return nil
}
