package lang

// Stmt is any statement.
type Stmt interface{ stmt() }

// SelectStmt is a query in either surface form:
//
//	Unit | in * | where … | columns … | where … | group by … | order by … | limit n
//	SELECT … FROM Unit IN * WHERE … HAVING … GROUP BY … ORDER BY … LIMIT n
//
// Filters keep their step order. The compiler pushes the leading run of
// pushable filters to the server and evaluates the rest locally; a SQL WHERE
// must be pushable, a HAVING is forced local.
type SelectStmt struct {
	Columns    []Column // empty with Star=true means the entity's default columns
	Star       bool
	From       Source
	Scope      *Scope   // nil: session scope
	Filters    []Filter // in step order
	ColumnsPos int      // Filters[:ColumnsPos] come before the columns step when printed
	GroupBy    []Ref
	OrderBy    []OrderItem
	Limit      *int
	Browse     []Ref        // browse by axes; the TUI renders Finder columns over them
	Diff       *DiffStep    // diff A vs B [by …]: compare like units across two selections
	Rollout    *RolloutStep // rollout [stage <name>]: open one ChangeOrder as a rollout
}

// RolloutStep opens the one ChangeOrder the statement selects as a rollout:
// its ChangeWorkflow's stages, where the change has got to, the gates on the
// next hop. Stage preselects a stage in the view.
type RolloutStep struct {
	Stage string
}

// DiffStep compares the units matching A with those matching B, paired by By
// (default: the unit's identity plus the label dimensions both sides share).
type DiffStep struct {
	A, B Expr
	By   []Ref
}

// Filter is one where step.
type Filter struct {
	Expr  Expr
	Local bool // forced local (SQL HAVING)
}

type Column struct {
	Expr  Expr
	Alias string
}

// Source is either an entity type (Entity != "") or a saved view/filter reference `space/slug`.
type Source struct {
	Entity string
	Saved  string
}

// Scope is the IN clause: a space slug, or the whole org.
type Scope struct {
	Space string
	Org   bool
}

type OrderItem struct {
	Ref  Ref
	Desc bool
}

type ExplainStmt struct{ Inner Stmt }
type UseStmt struct {
	Space string
	Org   bool
}
type ShowStmt struct {
	What string // ENTITIES, COLUMNS, JOINS, LABELS, VALUES, FUNCTIONS, FILTERS, VIEWS, SPACES, TARGETS
	Arg  string // entity for COLUMNS/JOINS/LABELS, attribute for VALUES
}
type DescribeStmt struct{ Name string }

func (*SelectStmt) stmt()   {}
func (*ExplainStmt) stmt()  {}
func (*UseStmt) stmt()      {}
func (*ShowStmt) stmt()     {}
func (*DescribeStmt) stmt() {}

// Expr nodes.
type Expr interface{ expr() }

// Ref is an attribute path such as Slug, Space.Labels.env, LEN(ApprovedBy).
type Ref struct {
	Path string
	Len  bool
}

type LitKind int

const (
	LitString LitKind = iota
	LitNumber
	LitBool
)

type Lit struct {
	Kind LitKind
	S    string
	N    int64
	B    bool
}

type ListLit struct{ Items []Lit }

// Call is a function column such as get-replicas() or COUNT(*).
type Call struct {
	Name string
	Args []Expr
}

type Star struct{}

// Cmp is `left op right [IS [NOT] TRUE|FALSE]`. For IS NULL / IS NOT NULL, Right is nil.
type Cmp struct {
	Left  Expr
	Op    string
	Right Expr
	Truth string
}

type And struct{ L, R Expr }
type Or struct{ L, R Expr }
type Not struct{ X Expr }

func (Ref) expr()     {}
func (Lit) expr()     {}
func (ListLit) expr() {}
func (Call) expr()    {}
func (Star) expr()    {}
func (Cmp) expr()     {}
func (And) expr()     {}
func (Or) expr()      {}
func (Not) expr()     {}
