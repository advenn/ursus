RightOn`, `JoinHow(k JoinKind)`, `JoinSuffix`, **`JoinCoalesce`**, `JoinNullsEqual`, `JoinValidate`, `JoinMaintainOrder`. The type `JoinHow` is renamed **`JoinKind`** so `JoinHow(...)` can be the option constructor.
5. **A function may not share its name with a type in its own signature.** Fixes `ScanFunc`: the func type becomes **`SourceFunc`**, the constructor stays `ScanFunc(schema Schema, fn SourceFunc) *LazyFrame`.
6. **Different scopes are not collisions.** `Expr.Filter` vs `LazyFrame.Filter`, `ursus.All()` vs `selector.All()`, `ursus.Repeat` vs `Expr.Repeat` all stand.

Doc gap found while applying this: **`ursus.Len()`** is used in the §16 worked example but never declared. Add `func Len() Expr` (row count of the current context).

---

## 4. `internal/expr` — the expression IR

### 4.1 `node.go`

```go
// Package expr is the expression IR. ursus.Expr is a one-field façade over
// expr.Node; the optimizer and the evaluator both consume Node directly.
package expr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ursus/internal/dtype"
	"ursus/internal/uerr"
)

// ErrUnexpanded is returned when a node containing a *Match reaches type
// resolution. It always indicates a missing Expand call, i.e. an ursus bug.
var ErrUnexpanded = errors.New("expression contains an unexpanded multi-column selection")

// Node is one node of the expression IR.
//
// The interface is SEALED by the unexported equalTo method: every
// implementation lives in this package, so type switches over Node are
// exhaustive by construction.
//
// A Node is IMMUTABLE. Rewrites allocate new nodes; nothing ever mutates a node
// or a slice reachable from one.
type Node interface {
	// Field resolves this node's single output field — name, dtype and
	// nullability — against an input schema, without touching data.
	//
	// Field is defined only for RESOLVED nodes. A tree containing a *Match must
	// be run through Expand first; otherwise Field returns ErrUnexpanded.
	Field(in *dtype.Schema) (dtype.Field, error)

	// Children returns the operand sub-expressions in evaluation order. The
	// returned slice must not be mutated.
	//
	// Sub-expressions that open a NEW SCOPE — the body of List().Eval(), which
	// is evaluated against a synthetic element schema — are deliberately NOT
	// children, so generic walkers can never rewrite across a scope boundary.
	Children() []Node

	// WithChildren returns a copy of n with its children replaced.
	// len(cs) always equals len(n.Children()).
	WithChildren(cs []Node) Node

	// String returns a canonical, deterministic rendering, used by Explain,
	// golden tests and matcher dedup. It must never contain pointers,
	// addresses, or map-iteration-ordered content.
	String() string

	equalTo(other Node) bool
}

// Equal reports structural equality.
func Equal(a, b Node) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.equalTo(b)
}

// FirstErr returns the leftmost *Err among ns, or nil. Every ursus.Expr
// combinator calls it so that an error node short-circuits to the root.
func FirstErr(ns ...Node) Node {
	for _, n := range ns {
		if e, ok := n.(*Err); ok {
			return e
		}
	}
	return nil
}

// LiteralName is the output name of an expression rooted at a literal.
const LiteralName = "literal"
```

### 4.2 `nodes.go` — the skeleton node set

```go
// ---------------------------------------------------------------- Column ----

// Column references exactly one input column by name. Col("a") lowers here;
// Col("a","b") lowers to *Match. Keeping the single-name case distinct matters:
// it is the hot path, the optimizer pattern-matches on it, and it must ERROR
// when the column is absent, whereas a matcher that matches nothing is legal.
type Column struct{ Name string }

func (n *Column) Field(in *dtype.Schema) (dtype.Field, error) {
	f, ok := in.ByName(n.Name)
	if !ok {
		return dtype.Field{}, &uerr.ColumnNotFound{Name: n.Name, Available: in.Names()}
	}
	return f, nil
}
func (n *Column) Children() []Node         { return nil }
func (n *Column) WithChildren([]Node) Node { return n }
func (n *Column) String() string           { return "col(" + strconv.Quote(n.Name) + ")" }
func (n *Column) equalTo(o Node) bool      { x, ok := o.(*Column); return ok && x.Name == n.Name }

// ----------------------------------------------------------------- Match ----

// Match is the ONE multi-output leaf in the IR. Col(names...), ColRegex,
// ColDType, All, Exclude, Nth and every selector.Selector lower to it. Expand
// replaces it with one *Column per matched name.
type Match struct{ M Matcher }

func (n *Match) Field(*dtype.Schema) (dtype.Field, error) {
	return dtype.Field{}, fmt.Errorf("%w: %s", ErrUnexpanded, n.M.Key())
}
func (n *Match) Children() []Node         { return nil }
func (n *Match) WithChildren([]Node) Node { return n }
func (n *Match) String() string           { return n.M.Key() }
func (n *Match) equalTo(o Node) bool {
	x, ok := o.(*Match)
	return ok && x.M.Key() == n.M.Key()
}

// --------------------------------------------------------------- Literal ----

// Literal is a scalar constant. Value == nil means a typed null.
type Literal struct {
	Value any
	DT    dtype.DataType
}

func (n *Literal) Field(*dtype.Schema) (dtype.Field, error) {
	return dtype.Field{Name: LiteralName, Type: n.DT, Nullable: n.Value == nil}, nil
}
func (n *Literal) Children() []Node         { return nil }
func (n *Literal) WithChildren([]Node) Node { return n }
func (n *Literal) equalTo(o Node) bool {
	x, ok := o.(*Literal)
	return ok && x.DT.Equal(n.DT) && litEq(x.Value, n.Value)
}

// String renders the bare value when DT is the default type for that Go kind,
// and value:Type otherwise. This makes literal narrowing (Int64 -> Int32 next
// to an Int32 column) visible in optimizer golden files.
func (n *Literal) String() string {
	if n.Value == nil {
		return "null:" + n.DT.String()
	}
	s := litText(n.Value)
	if defaultDTypeFor(n.Value).Equal(n.DT) {
		return s
	}
	return s + ":" + n.DT.String()
}

// ----------------------------------------------------------------- Alias ----

// Alias sets the output name absolutely.
type Alias struct {
	Child Node
	Name  string
}

func (n *Alias) Field(in *dtype.Schema) (dtype.Field, error) {
	f, err := n.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	return f.Rename(n.Name), nil
}
func (n *Alias) Children() []Node           { return []Node{n.Child} }
func (n *Alias) WithChildren(cs []Node) Node { return &Alias{Child: cs[0], Name: n.Name} }
func (n *Alias) String() string              { return n.Child.String() + ".alias(" + strconv.Quote(n.Name) + ")" }
func (n *Alias) equalTo(o Node) bool {
	x, ok := o.(*Alias)
	return ok && x.Name == n.Name && Equal(x.Child, n.Child)
}

// ---------------------------------------------------------- RenameOutput ----

// RenameOutput backs the Name() namespace: it transforms the COMPUTED output
// name of its child. Unlike Alias it composes with expansion, because the
// transformation is applied once per expanded column.
//
// Label is a deterministic description of Fn; equality and String() use it,
// because func values are not comparable.
type RenameOutput struct {
	Child Node
	Label string
	Fn    func(string) string
}

func (n *RenameOutput) Field(in *dtype.Schema) (dtype.Field, error) {
	f, err := n.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	return f.Rename(n.Fn(f.Name)), nil
}
func (n *RenameOutput) Children() []Node { return []Node{n.Child} }
func (n *RenameOutput) WithChildren(cs []Node) Node {
	return &RenameOutput{Child: cs[0], Label: n.Label, Fn: n.Fn}
}
func (n *RenameOutput) String() string { return n.Child.String() + ".name." + n.Label }
func (n *RenameOutput) equalTo(o Node) bool {
	x, ok := o.(*RenameOutput)
	return ok && x.Label == n.Label && Equal(x.Child, n.Child)
}

// ------------------------------------------------------------------ Cast ----

type Cast struct {
	Child  Node
	To     dtype.DataType
	Strict bool // false: unconvertible values become null instead of erroring
}

func (n *Cast) Field(in *dtype.Schema) (dtype.Field, error) {
	f, err := n.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	if !CastAllowed(f.Type, n.To) {
		return dtype.Field{}, &uerr.CastError{From: f.Type, To: n.To, Expr: n.Child.String()}
	}
	return dtype.Field{Name: f.Name, Type: n.To, Nullable: f.Nullable || !n.Strict}, nil
}
func (n *Cast) Children() []Node { return []Node{n.Child} }
func (n *Cast) WithChildren(cs []Node) Node {
	return &Cast{Child: cs[0], To: n.To, Strict: n.Strict}
}
func (n *Cast) String() string { return n.Child.String() + ".cast(" + n.To.String() + ")" }
func (n *Cast) equalTo(o Node) bool {
	x, ok := o.(*Cast)
	return ok && x.To.Equal(n.To) && x.Strict == n.Strict && Equal(x.Child, n.Child)
}

// ----------------------------------------------------------------- Unary ----

type UnaryOp uint8

const (
	OpNot UnaryOp = iota
	OpNeg
	OpIsNull
	OpIsNotNull
	OpIsNan
	OpIsNotNan
	// … grows; each addition needs one row in unaryTable.
)

// unaryTable is the single source of truth for unary typing. The evaluator
// uses the same table for kernel dispatch.
var unaryTable = map[UnaryOp]struct {
	name     string
	accept   func(dtype.DataType) bool
	out      func(dtype.DataType) dtype.DataType
	nullFree bool // result is never null regardless of input nullability
}{
	OpNot:       {"not", isBoolish, constT(dtype.Bool), false},
	OpNeg:       {"neg", dtype.DataType.IsNumeric, identT, false},
	OpIsNull:    {"is_null", anyT, constT(dtype.Bool), true},
	OpIsNotNull: {"is_not_null", anyT, constT(dtype.Bool), true},
	OpIsNan:     {"is_nan", dtype.DataType.IsFloat, constT(dtype.Bool), false},
	OpIsNotNan:  {"is_not_nan", dtype.DataType.IsFloat, constT(dtype.Bool), false},
}

type Unary struct {
	Op    UnaryOp
	Child Node
}

func (n *Unary) Field(in *dtype.Schema) (dtype.Field, error) {
	f, err := n.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	e := unaryTable[n.Op]
	if !e.accept(f.Type) {
		return dtype.Field{}, &uerr.TypeError{Op: e.name, Left: f.Type, Expr: n.Child.String()}
	}
	return dtype.Field{Name: f.Name, Type: e.out(f.Type), Nullable: f.Nullable && !e.nullFree}, nil
}
func (n *Unary) Children() []Node            { return []Node{n.Child} }
func (n *Unary) WithChildren(cs []Node) Node { return &Unary{Op: n.Op, Child: cs[0]} }
func (n *Unary) String() string              { return n.Child.String() + "." + unaryTable[n.Op].name + "()" }
func (n *Unary) equalTo(o Node) bool {
	x, ok := o.(*Unary)
	return ok && x.Op == n.Op && Equal(x.Child, n.Child)
}

// ---------------------------------------------------------------- Binary ----

type BinaryOp uint8

const (
	OpAdd BinaryOp = iota
	OpSub
	OpMul
	OpDiv
	OpFloorDiv
	OpMod
	OpPow
	OpEq
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
	OpEqMissing
	OpNeMissing
	OpAnd
	OpOr
	OpXor
)

var binarySym = [...]string{
	OpAdd: "+", OpSub: "-", OpMul: "*", OpDiv: "/", OpFloorDiv: "//", OpMod: "%",
	OpPow: "**", OpEq: "==", OpNe: "!=", OpLt: "<", OpLe: "<=", OpGt: ">", OpGe: ">=",
	OpEqMissing: "<=>", OpNeMissing: "<!>", OpAnd: "and", OpOr: "or", OpXor: "xor",
}

func (op BinaryOp) String() string { return binarySym[op] }
func (op BinaryOp) IsComparison() bool {
	return op >= OpEq && op <= OpNeMissing
}
func (op BinaryOp) IsLogical() bool { return op >= OpAnd }

type Binary struct {
	Op       BinaryOp
	LHS, RHS Node
}

func (n *Binary) Field(in *dtype.Schema) (dtype.Field, error) {
	lf, err := n.LHS.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	rf, err := n.RHS.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	res, err := ResolveBinary(n.Op, n.LHS, lf, n.RHS, rf)
	if err != nil {
		return dtype.Field{}, err
	}
	// Output name is the LEFTMOST operand's name, unconditionally.
	return dtype.Field{Name: lf.Name, Type: res.Out, Nullable: lf.Nullable || rf.Nullable}, nil
}
func (n *Binary) Children() []Node { return []Node{n.LHS, n.RHS} }
func (n *Binary) WithChildren(cs []Node) Node {
	return &Binary{Op: n.Op, LHS: cs[0], RHS: cs[1]}
}

// String parenthesises only nested binaries, so the common case renders as
// col("x") > 5 rather than (col("x") > 5).
func (n *Binary) String() string {
	return operand(n.LHS) + " " + n.Op.String() + " " + operand(n.RHS)
}
func operand(n Node) string {
	if _, ok := n.(*Binary); ok {
		return "(" + n.String() + ")"
	}
	return n.String()
}
func (n *Binary) equalTo(o Node) bool {
	x, ok := o.(*Binary)
	return ok && x.Op == n.Op && Equal(x.LHS, n.LHS) && Equal(x.RHS, n.RHS)
}

// ------------------------------------------------------------------- Err ----

// Err carries a construction-time error (bad regex, zero-value Expr, unknown
// literal kind). Every combinator short-circuits on it via FirstErr, so an Err
// is always the ROOT of the expression it poisoned.
type Err struct{ E error }

func (n *Err) Field(*dtype.Schema) (dtype.Field, error) { return dtype.Field{}, n.E }
func (n *Err) Children() []Node                         { return nil }
func (n *Err) WithChildren([]Node) Node                 { return n }
func (n *Err) String() string                           { return "<error: " + n.E.Error() + ">" }
func (n *Err) equalTo(o Node) bool                      { x, ok := o.(*Err); return ok && x.E == n.E }
```

### 4.3 `walk.go`

```go
package expr

// Walk visits n and its descendants in preorder. f returns false to prune.
func Walk(n Node, f func(Node) bool) {
	if !f(n) {
		return
	}
	for _, c := range n.Children() {
		Walk(c, f)
	}
}

// TransformUp rewrites bottom-up. f returns (replacement, true) to replace.
func TransformUp(n Node, f func(Node) (Node, bool)) (Node, bool) {
	cs := n.Children()
	changed := false
	if len(cs) > 0 {
		out := make([]Node, len(cs))
		for i, c := range cs {
			nc, ch := TransformUp(c, f)
			out[i] = nc
			changed = changed || ch
		}
		if changed {
			n = n.WithChildren(out)
		}
	}
	if r, ok := f(n); ok {
		return r, true
	}
	return n, changed
}

// RootNames returns the input columns this expression reads, deduplicated,
// in first-appearance order. It backs both projection pushdown and
// MetaExpr.RootNames.
func RootNames(n Node) []string {
	var out []string
	seen := map[string]struct{}{}
	Walk(n, func(x Node) bool {
		if c, ok := x.(*Column); ok {
			if _, dup := seen[c.Name]; !dup {
				seen[c.Name] = struct{}{}
				out = append(out, c.Name)
			}
		}
		return true
	})
	return out
}

// OutputName resolves an expression's output name WITHOUT a schema. It is
// defined for expanded trees (no *Match) and is the naming half of Field.
//
// Rule: the name is the name of the LEFTMOST operand, recursively, unless
// overridden by Alias or transformed by RenameOutput. A literal is "literal".
func OutputName(n Node) (string, error) {
	for {
		switch x := n.(type) {
		case *Column:
			return x.Name, nil
		case *Literal:
			return LiteralName, nil
		case *Alias:
			return x.Name, nil
		case *RenameOutput:
			s, err := OutputName(x.Child)
			if err != nil {
				return "", err
			}
			return x.Fn(s), nil
		case *Match:
			return "", fmt.Errorf("%w: %s", ErrUnexpanded, x.M.Key())
		case *Err:
			return "", x.E
		default:
			cs := n.Children()
			if len(cs) == 0 {
				return LiteralName, nil
			}
			n = cs[0]
		}
	}
}

// HasMultipleOutputs reports whether n MAY expand to more than one column.
// It is conservative: a matcher whose width is not statically known counts.
func HasMultipleOutputs(n Node) bool {
	multi := false
	Walk(n, func(x Node) bool {
		if m, ok := x.(*Match); ok && !m.M.Width1() {
			multi = true
			return false
		}
		return true
	})
	return multi
}
```

---

## 5. Expansion (D12) — matchers, algorithm, and where it runs

### 5.1 `match.go`

```go
package expr

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"ursus/internal/dtype"
)

// Matcher selects a set of columns from a schema. It is the single mechanism
// behind Col(names...), ColRegex, ColDType, All, Exclude, Nth AND every
// selector.Selector — selectors are not a parallel universe, they are matchers
// with set algebra.
//
// Match must return names in SCHEMA ORDER, deduplicated.
// Key must be deterministic; it drives dedup, Explain and golden files.
type Matcher interface {
	Match(s *dtype.Schema) ([]string, error)
	Key() string
	// Width1 reports whether this matcher statically selects exactly one column.
	Width1() bool
}

type allColumns struct{}

func All() Matcher                              { return allColumns{} }
func (allColumns) Match(s *dtype.Schema) ([]string, error) { return s.Names(), nil }
func (allColumns) Key() string                  { return "*" }
func (allColumns) Width1() bool                 { return false }

// names matches explicit names. Strict (the Col(...) case) errors on a missing
// name; non-strict (selector.ByName) silently skips.
type names struct {
	N      []string
	Strict bool
}

func Names(n []string, strict bool) Matcher { return names{N: n, Strict: strict} }

func (m names) Match(s *dtype.Schema) ([]string, error) {
	out := make([]string, 0, len(m.N))
	for _, n := range m.N {
		if _, ok := s.ByName(n); !ok {
			if m.Strict {
				return nil, &uerr.ColumnNotFound{Name: n, Available: s.Names()}
			}
			continue
		}
		out = append(out, n)
	}
	return orderBySchema(s, out), nil
}
func (m names) Key() string {
	q := make([]string, len(m.N))
	for i, n := range m.N {
		q[i] = strconv.Quote(n)
	}
	return "cols(" + strings.Join(q, ", ") + ")"
}
func (m names) Width1() bool { return len(m.N) == 1 }

type regexMatcher struct {
	pat string
	re  *regexp.Regexp
}

// Regex compiles pattern eagerly, so a bad pattern becomes an *Err node at
// construction time rather than a mystery at Collect (D15).
func Regex(pattern string) (Matcher, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return regexMatcher{pat: pattern, re: re}, nil
}
func (m regexMatcher) Match(s *dtype.Schema) ([]string, error) {
	var out []string
	for _, n := range s.Names() {
		if m.re.MatchString(n) {
			out = append(out, n)
		}
	}
	return out, nil
}
func (m regexMatcher) Key() string  { return "^re(" + strconv.Quote(m.pat) + ")" }
func (m regexMatcher) Width1() bool { return false }

type dtypeMatcher struct{ Types []dtype.DataType }

func DTypes(ts []dtype.DataType) Matcher { return dtypeMatcher{Types: ts} }
func (m dtypeMatcher) Match(s *dtype.Schema) ([]string, error) {
	var out []string
	for _, f := range s.FieldSlice() {
		if slices.ContainsFunc(m.Types, f.Type.Equal) {
			out = append(out, f.Name)
		}
	}
	return out, nil
}
func (m dtypeMatcher) Key() string {
	p := make([]string, len(m.Types))
	for i, t := range m.Types {
		p[i] = t.String()
	}
	return "dtypes(" + strings.Join(p, ", ") + ")"
}
func (m dtypeMatcher) Width1() bool { return false }

// pred backs selector.StartsWith / EndsWith / Contains / Alpha / Numeric / …
// Label makes Key deterministic despite the func value.
type pred struct {
	Label string
	Fn    func(dtype.Field) bool
}

func Pred(label string, fn func(dtype.Field) bool) Matcher { return pred{label, fn} }

// Set algebra. All four operate on ordered, schema-ordered name lists.
type setOp struct {
	Op   string // "union" | "intersect" | "diff" | "symdiff"
	A, B Matcher
}

func Union(a, b Matcher) Matcher     { return setOp{"union", a, b} }
func Intersect(a, b Matcher) Matcher { return setOp{"intersect", a, b} }
func Difference(a, b Matcher) Matcher { return setOp{"diff", a, b} }
func SymDiff(a, b Matcher) Matcher   { return setOp{"symdiff", a, b} }
func Complement(m Matcher) Matcher   { return setOp{"diff", allColumns{}, m} }

func (m setOp) Match(s *dtype.Schema) ([]string, error) {
	an, err := m.A.Match(s)
	if err != nil {
		return nil, err
	}
	bn, err := m.B.Match(s)
	if err != nil {
		return nil, err
	}
	return orderBySchema(s, combine(m.Op, an, bn)), nil
}
func (m setOp) Key() string  { return m.Op + "(" + m.A.Key() + ", " + m.B.Key() + ")" }
func (m setOp) Width1() bool { return false }
```

### 5.2 `expand.go` — the algorithm

```go
package expr

// varargNode is the extension point for HORIZONTAL functions (SumHorizontal,
// Coalesce, ConcatStr, Fold, StructOf). A vararg node ABSORBS its arguments'
// expansions into its own argument list instead of fanning the enclosing
// expression out: SumHorizontal(All()) is one column, not N.
//
// No v0.1 node implements it; the hook exists so v0.2 does not rewrite Expand.
type varargNode interface {
	Node
	args() []Node
	withArgs([]Node) Node
}

// Expand rewrites one expression into the list of single-output expressions it
// denotes, resolving every *Match against in.
//
// Ordering guarantee: expansion happens BEFORE any type resolution, because you
// cannot type Col("^m_.*$").Add(1) until you know which columns it names.
func Expand(n Node, in *dtype.Schema) ([]Node, error) {
	if e, ok := n.(*Err); ok {
		return nil, e.E
	}

	// Phase A: splice horizontal-function arguments bottom-up.
	n, err := spliceVarargs(n, in)
	if err != nil {
		return nil, err
	}

	// Phase B: find the expansion source.
	ms := collectMatches(n)
	if len(ms) == 0 {
		return []Node{n}, nil
	}
	key := ms[0].M.Key()
	for _, m := range ms[1:] {
		if m.M.Key() != key {
			// v0.1: exactly one expansion source per expression. Identical
			// matchers are allowed and share one expansion. Relaxing this to
			// positional zipping later cannot break existing programs.
			return nil, &uerr.MultipleExpansions{First: key, Second: m.M.Key(), Expr: n.String()}
		}
	}
	cols, err := ms[0].M.Match(in)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		// A matcher that matches nothing contributes zero columns. This is NOT
		// an error — it is what makes All().Exclude(...) and selectors work on
		// heterogeneous schemas.
		return nil, nil
	}

	// Phase C: one clone per matched column, with per-clone name checking.
	out := make([]Node, len(cols))
	seen := make(map[string]int, len(cols))
	for i, name := range cols {
		col := &Column{Name: name}
		c, _ := TransformUp(n, func(x Node) (Node, bool) {
			if _, ok := x.(*Match); ok {
				return col, true
			}
			return x, false
		})
		on, err := OutputName(c)
		if err != nil {
			return nil, err
		}
		if j, dup := seen[on]; dup {
			// This is where "Alias on a multi-output expression" is caught, with
			// a precise message, plus every other way to collide (Name().Map to
			// a constant, etc.).
			return nil, &uerr.ExpansionNameCollision{
				Name:  on,
				Width: len(cols),
				Cols:  []string{cols[j], name},
				Expr:  n.String(),
				Hint:  aliasHint(n),
			}
		}
		seen[on] = i
		out[i] = c
	}
	return out, nil
}

// ExpandAll expands a context's expression list, flattening the results and
// annotating errors with the offending expression's position.
func ExpandAll(ns []Node, in *dtype.Schema) ([]Node, error) {
	out := make([]Node, 0, len(ns))
	for i, n := range ns {
		xs, err := Expand(n, in)
		if err != nil {
			return nil, fmt.Errorf("expression %d (%s): %w", i+1, n.String(), err)
		}
		out = append(out, xs...)
	}
	return out, nil
}

// collectMatches gathers *Match leaves in preorder. It does not descend into
// vararg nodes' arguments (already spliced) or into scope bodies (not children).
func collectMatches(n Node) []*Match {
	var out []*Match
	Walk(n, func(x Node) bool {
		if m, ok := x.(*Match); ok {
			out = append(out, m)
			return false
		}
		return true
	})
	return out
}

func aliasHint(n Node) string {
	found := false
	Walk(n, func(x Node) bool {
		if _, ok := x.(*Alias); ok {
			found = true
			return false
		}
		return true
	})
	if found {
		return "Alias sets one fixed name; use Name().Prefix / Name().Suffix / " +
			"Name().Map to rename an expanded selection"
	}
	return ""
}
```

The resulting error text:

```
ursus: SELECT: expression 1 (cols("a", "b") * 1.1.alias("x")): expression expands to
2 columns (a, b) but they all resolve to the output name "x"
  Alias sets one fixed name; use Name().Prefix / Name().Suffix / Name().Map to
  rename an expanded selection
```

### 5.3 Answers to the specific expansion questions

**Where expansion runs, and why there.** In `plan.Resolve`, per plan node, against that node's *input* schema. Not in `LazyFrame.Select` (would need a schema, which needs source I/O in a builder method, violating principle #4 and reporting a missing-file error from the wrong call). Not in the physical planner (the optimizer needs expanded, typed expressions to do projection pushdown at all — you cannot compute `RootNames` of `ColRegex`).

**Ordering vs type coercion.** Expansion → typing/implicit coercion (`Field`) → optimizer (constant folding, and in v0.2 explicit `Cast`-insertion). Implicit coercion is computed by `ResolveBinary` and *not* materialised as `Cast` nodes in v0.1; the evaluator calls the same function for kernel dispatch, so there is exactly one source of truth.

**`Col("a","b").Mul(1.1).Sum()` before expansion** is a single tree with a multi-output leaf:

```
Agg{Sum}
 └─ Binary{*}
     ├─ Match{cols("a","b")}
     └─ Literal{1.1, Float64}
```

`Expr.Meta().HasMultipleOutputs()` answers `true` by finding a non-`Width1` `Match`. `Meta().OutputName()` returns `ErrUnexpanded`. After `Expand` against `{a: Int64, b: Float32}` it becomes two trees, typed independently: `a*1.1 → Float64`, `b*1.1 → Float32` (float wins, literal narrows).

**`Col("a","b").Add(1).Alias("x")` is an error** — caught by the per-clone `OutputName` collision check, which subsumes and out-diagnoses a structural "is there an Alias above an expansion" test.

**Selectors.** `selector.Selector` holds an `expr.Matcher`. `Selector.Expand(schema)` is `m.Match(schema)`. `Selector.Expr()` is `ursus.FromNode(&expr.Match{M: m})`. Set algebra is `expr.Union`/`Intersect`/`Difference`/`SymDiff`/`Complement`. There is no second mechanism.

---

## 6. Type resolution: `ResolveBinary` and literal narrowing

```go
package expr

// BinaryResolution is the SINGLE SOURCE OF TRUTH for binary-operator typing.
// The evaluator must use it: cast LHS to CastL, RHS to CastR, produce Out.
// A differential test asserts Eval(n).DType() == n.Field(s).Type for every op.
type BinaryResolution struct {
	Out          dtype.DataType
	CastL, CastR dtype.DataType
}

// ResolveBinary takes the operand NODES as well as their fields so it can
// perform LITERAL NARROWING: Col("i32").Gt(5) stays Int32 instead of widening
// the whole column to Int64. That is the difference between a filter that
// pushes cleanly into Parquet row-group statistics and one that does not.
func ResolveBinary(op BinaryOp, l Node, lf dtype.Field, r Node, rf dtype.Field) (BinaryResolution, error) {
	lt, rt := lf.Type, rf.Type

	if op.IsLogical() {
		if !isBoolish(lt) || !isBoolish(rt) {
			return BinaryResolution{}, &uerr.TypeError{
				Op: op.String(), Left: lt, Right: rt,
				Hint: "logical operators require Boolean operands; use a comparison or .Cast(ursus.Bool)",
			}
		}
		return BinaryResolution{Out: dtype.Bool, CastL: dtype.Bool, CastR: dtype.Bool}, nil
	}

	common, ok := narrowLiteral(l, lt, r, rt)
	if !ok {
		var err error
		if common, err = CommonType(lt, rt); err != nil {
			return BinaryResolution{}, &uerr.TypeError{
				Op: op.String(), Left: lf.Type, Right: rf.Type,
				LeftExpr: l.String(), RightExpr: r.String(),
			}
		}
	}

	switch {
	case op.IsComparison():
		if !Comparable(common) {
			return BinaryResolution{}, &uerr.TypeError{Op: op.String(), Left: lf.Type, Right: rf.Type}
		}
		return BinaryResolution{Out: dtype.Bool, CastL: common, CastR: common}, nil

	case op == OpDiv:
		out := common
		if out.IsInteger() {
			out = dtype.Float64 // true division always yields a float
		}
		return BinaryResolution{Out: out, CastL: out, CastR: out}, nil

	case op == OpPow:
		return BinaryResolution{Out: dtype.Float64, CastL: dtype.Float64, CastR: dtype.Float64}, nil

	case op == OpAdd && common.ID == dtype.TypeString:
		return BinaryResolution{Out: dtype.Str, CastL: dtype.Str, CastR: dtype.Str}, nil

	default: // Add, Sub, Mul, FloorDiv, Mod
		if !common.IsNumeric() && !common.IsTemporal() {
			return BinaryResolution{}, &uerr.TypeError{Op: op.String(), Left: lf.Type, Right: rf.Type}
		}
		return BinaryResolution{Out: common, CastL: common, CastR: common}, nil
	}
}

// CommonType is the promotion lattice. It never allocates and never needs
// DataType to be comparable (D2): dispatch is on TypeID (a uint8) plus the
// Is* predicates, with explicit handling of parameterised types.
func CommonType(a, b dtype.DataType) (dtype.DataType, error) {
	switch {
	case a.Equal(b):
		return a, nil
	case a.ID == dtype.TypeNull:
		return b, nil
	case b.ID == dtype.TypeNull:
		return a, nil
	case a.IsFloat() && b.IsFloat():
		if a.ID == dtype.TypeFloat64 || b.ID == dtype.TypeFloat64 {
			return dtype.Float64, nil
		}
		return dtype.Float32, nil
	case a.IsFloat() && b.IsInteger():
		return a, nil // the requested float width wins
	case a.IsInteger() && b.IsFloat():
		return b, nil
	case a.IsInteger() && b.IsInteger():
		return commonInt(a, b), nil
	case a.IsTemporal() && b.IsTemporal():
		return commonTemporal(a, b)
	}
	return dtype.DataType{}, errNoCommonType
}

// commonInt: same signedness -> wider. Mixed -> the next signed width up
// (u8->i16, u16->i32, u32->i64); u64 with any signed -> Int64, which is lossy
// above 2^63 and is documented as such.
func commonInt(a, b dtype.DataType) dtype.DataType { /* small rank table */ }

// narrowLiteral: if exactly one side is a non-null *Literal whose value is
// EXACTLY representable in the other side's type, use the other side's type.
func narrowLiteral(l Node, lt dtype.DataType, r Node, rt dtype.DataType) (dtype.DataType, bool) {
	if lit, ok := r.(*Literal); ok && lit.Value != nil && fits(lit.Value, lt) {
		return lt, true
	}
	if lit, ok := l.(*Literal); ok && lit.Value != nil && fits(lit.Value, rt) {
		return rt, true
	}
	return dtype.DataType{}, false
}
```

Deliberate deviations from Polars, each because it prevents a silent wrong answer:
- **Bool in arithmetic is an error.** Use `.Cast(Int32)`.
- **Decimal, Categorical and List arithmetic error with "not supported in v0.1"**, never a silent fallback.
- **`u64 op i64 → Int64`**, documented lossy, rather than a float.

---

## 7. `internal/plan`

### 7.1 `node.go` + `source.go`

```go
package plan

import (
	"context"
	"errors"
	"sync/atomic"

	"ursus/internal/dtype"
	"ursus/internal/expr"
)

var ErrUnresolved = errors.New("plan node has not been resolved; call plan.Resolve first")

// Node is one node of the logical plan.
//
// SEALED by isPlanNode: only this package may add node types, so type switches
// are exhaustive and the optimizer surface is stable. Extension for users
// happens through Source (unsealed), not through Node.
//
// IMMUTABLE: every field, and every slice reachable from one, is read-only
// after construction. Rewrites allocate.
type Node interface {
	// Schema resolves the output schema WITHOUT touching data. It is total and
	// cheap only after Resolve; an unresolved Scan returns ErrUnresolved.
	Schema() (*dtype.Schema, error)

	Children() []Node
	// WithChildren returns a copy with new inputs; len(cs) == len(Children()).
	// Implementations construct explicitly — they never copy the schema cache.
	WithChildren(cs []Node) Node

	// Label is the single uppercase operator line for Explain.
	Label() string
	// Detail returns continuation lines rendered one indent below Label.
	Detail(o RenderOptions) []string

	isPlanNode()
}

// schemaCache memoises Schema. atomic.Pointer (not sync.Once) so that node
// structs stay copylocks-clean; a lost race just recomputes a pure function.
type schemaCache struct{ p atomic.Pointer[schemaResult] }

type schemaResult struct {
	s   *dtype.Schema
	err error
}

func (c *schemaCache) get(f func() (*dtype.Schema, error)) (*dtype.Schema, error) {
	if r := c.p.Load(); r != nil {
		return r.s, r.err
	}
	s, err := f()
	c.p.Store(&schemaResult{s, err})
	return s, err
}
```

```go
// Source is the logical face of a data source. It is NOT sealed: this is the
// user/IO extension point (parquet, csv, in-memory frames, ScanFunc).
//
// Schema takes a context because resolving it is I/O (a Parquet footer, an S3
// HEAD). That is precisely why Resolve exists as a separate phase, and why
// Node.Schema() can be pure.
type Source interface {
	Schema(ctx context.Context) (*dtype.Schema, error)
	// Kind is a short lowercase label: "parquet", "csv", "memory".
	Kind() string
	// Detail is one or more Explain lines: paths, row counts, options.
	Detail() []string
	Caps() SourceCaps
}

// SourceCaps reports what the source can absorb from the plan. It affects the
// PHYSICAL plan and the Explain wording only: pushdown always records its
// result on the Scan node, and the physical planner inserts a compensating
// operator when the source cannot honour it.
type SourceCaps struct {
	Projection bool
	Predicate  bool
	Limit      bool
}

// ScanSpec is what pushdown produces and what the physical reader consumes.
type ScanSpec struct {
	Projection []string  // nil = every column, in source order
	Predicate  expr.Node // nil = none; already type-checked, Boolean-valued
	Limit      int64     // -1 = unbounded
}
```

**`*Scan` is the only leaf node.** `DataFrame.Lazy()` produces a `memorySource`, `ScanParquet` produces a `parquetSource`. One node type, one pushdown case, one Explain rendering.

### 7.2 `nodes.go`

```go
// ------------------------------------------------------------------ Scan ----

type Scan struct {
	Source Source

	// full is the source's complete schema. nil before Resolve.
	full *dtype.Schema
	// Projection is nil (= all columns) or a source-ordered subset.
	Projection []string
	// Predicate and Limit are populated by v0.2 pushdown passes.
	Predicate expr.Node
	Limit     int64

	cache schemaCache
}

func NewScan(src Source) *Scan { return &Scan{Source: src, Limit: -1} }

func (n *Scan) Schema() (*dtype.Schema, error) {
	return n.cache.get(func() (*dtype.Schema, error) {
		if n.full == nil {
			return nil, ErrUnresolved
		}
		if n.Projection == nil {
			return n.full, nil
		}
		return n.full.Select(n.Projection)
	})
}
func (n *Scan) Children() []Node { return nil }
func (n *Scan) WithChildren([]Node) Node { return n }
func (n *Scan) Label() string { return "SCAN " + n.Source.Kind() }

func (n *Scan) Detail(o RenderOptions) []string {
	d := append([]string(nil), n.Source.Detail()...)
	if n.full != nil {
		if n.Projection == nil {
			d = append(d, fmt.Sprintf("projection: */%d", n.full.Len()))
		} else {
			d = append(d, fmt.Sprintf("projection: [%s] (%d/%d)",
				strings.Join(n.Projection, ", "), len(n.Projection), n.full.Len()))
		}
	}
	if n.Predicate != nil {
		d = append(d, "predicate: "+n.Predicate.String())
	}
	if n.Limit >= 0 {
		d = append(d, fmt.Sprintf("limit: %d", n.Limit))
	}
	return d
}
func (n *Scan) isPlanNode() {}

// derive returns a copy with a fresh (empty) schema cache. Every mutation-like
// operation on an immutable node goes through it.
func (n *Scan) derive(mut func(*Scan)) *Scan {
	c := &Scan{Source: n.Source, full: n.full, Projection: n.Projection,
		Predicate: n.Predicate, Limit: n.Limit}
	mut(c)
	return c
}

// ---------------------------------------------------------------- Filter ----

// Filter keeps rows for which EVERY predicate evaluates to TRUE.
//
// NULL SEMANTICS (D18): a row whose predicate is null is DROPPED — SQL
// three-valued logic. The physical operator keeps row i iff valid[i] && v[i].
//
// Preds is a slice, not a single AND-ed node, so that v0.2 predicate pushdown
// can push conjuncts independently without first splitting an And tree.
type Filter struct {
	Child Node
	Preds []expr.Node
	cache schemaCache
}

func NewFilter(child Node, preds []expr.Node) *Filter {
	return &Filter{Child: child, Preds: preds}
}
func (n *Filter) Schema() (*dtype.Schema, error) { return n.Child.Schema() }
func (n *Filter) Children() []Node               { return []Node{n.Child} }
func (n *Filter) WithChildren(cs []Node) Node    { return NewFilter(cs[0], n.Preds) }
func (n *Filter) Label() string {
	p := make([]string, len(n.Preds))
	for i, e := range n.Preds {
		p[i] = e.String()
	}
	return "FILTER [" + strings.Join(p, ", ") + "]"
}
func (n *Filter) Detail(RenderOptions) []string { return nil }
func (n *Filter) isPlanNode()                   {}

// --------------------------------------------------------------- Project ----

type ProjectKind uint8

const (
	ProjectSelect ProjectKind = iota // output == the expressions
	ProjectWith                      // output == input, expressions replacing/appending
)

// Project is Select and WithColumns. One node type, because schema resolution,
// pushdown and Explain are 90% shared and Kind switches the rest.
//
// After Resolve, Exprs contains only SINGLE-OUTPUT expressions.
type Project struct {
	Child Node
	Kind  ProjectKind
	Exprs []expr.Node
	cache schemaCache
}

func NewProject(child Node, k ProjectKind, es []expr.Node) *Project {
	return &Project{Child: child, Kind: k, Exprs: es}
}

func (n *Project) Schema() (*dtype.Schema, error) {
	return n.cache.get(func() (*dtype.Schema, error) {
		in, err := n.Child.Schema()
		if err != nil {
			return nil, err
		}
		var fs []dtype.Field
		if n.Kind == ProjectWith {
			fs = in.FieldSlice() // fresh slice, safe to mutate locally
		} else {
			fs = make([]dtype.Field, 0, len(n.Exprs))
		}
		for _, e := range n.Exprs {
			f, err := e.Field(in)
			if err != nil {
				return nil, err
			}
			if n.Kind == ProjectWith {
				if i := indexOfField(fs, f.Name); i >= 0 {
					fs[i] = f // replace in place: WithColumns preserves position
					continue
				}
			}
			fs = append(fs, f)
		}
		return dtype.NewSchema(fs...)
	})
}
func (n *Project) Children() []Node            { return []Node{n.Child} }
func (n *Project) WithChildren(cs []Node) Node { return NewProject(cs[0], n.Kind, n.Exprs) }
func (n *Project) Label() string {
	name := "SELECT"
	if n.Kind == ProjectWith {
		name = "WITH_COLUMNS"
	}
	p := make([]string, len(n.Exprs))
	for i, e := range n.Exprs {
		p[i] = e.String()
	}
	return name + " [" + strings.Join(p, ", ") + "]"
}
func (n *Project) Detail(RenderOptions) []string { return nil }
func (n *Project) isPlanNode()                   {}
```

**Node taxonomy, v0.1 implemented vs. reserved.** Implemented: `*Scan`, `*Filter`, `*Project`. Reserved with their shapes fixed now so v0.2 slots in: `*Sort{Child, Specs}`, `*Slice{Child, Offset, Len}`, `*Aggregate{Child, Keys, Aggs, MaintainOrder, Having}`, `*Join{Left, Right, LeftOn, RightOn, Kind, Opts}`, `*Union{Children, Mode}`, `*Distinct`, `*Explode`, `*Sink{Child, Target}`, `*Cache{Child}`. The projection-pushdown `default` case handles all of them conservatively and correctly on day one (§8.2).

### 7.3 `resolve.go`

```go
// Resolve turns a built plan into a RESOLVED plan:
//
//   1. every Source's schema is fetched (this is where I/O happens, hence ctx)
//   2. every expression is EXPANDED against its node's input schema
//   3. every expression is TYPE-CHECKED; its output field becomes known
//   4. output names are checked for uniqueness within each context
//
// After Resolve, Node.Schema() is total, pure and cheap, and the optimizer may
// assume every Project holds single-output expressions.
//
// Resolve is non-destructive: it returns a new tree and never touches lf.root.
func Resolve(ctx context.Context, n Node) (Node, error) {
	return resolve(ctx, n, nil)
}

func resolve(ctx context.Context, n Node, path []Node) (Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path = append(path, n)

	cs := n.Children()
	if len(cs) > 0 {
		out := make([]Node, len(cs))
		for i, c := range cs {
			r, err := resolve(ctx, c, path)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		n = n.WithChildren(out)
	}

	switch x := n.(type) {
	case *Scan:
		s, err := x.Source.Schema(ctx)
		if err != nil {
			return nil, fmt.Errorf("ursus: %s: %w", x.Label(), err)
		}
		return x.derive(func(c *Scan) { c.full = s }), nil

	case *Project:
		in, err := x.Child.Schema()
		if err != nil {
			return nil, err
		}
		es, err := expr.ExpandAll(x.Exprs, in)
		if err != nil {
			return nil, annotate(err, x, path, in)
		}
		if len(es) == 0 {
			return nil, annotate(errors.New("no columns selected"), x, path, in)
		}
		if err := checkOutputs(es, in); err != nil {
			return nil, annotate(err, x, path, in)
		}
		return NewProject(x.Child, x.Kind, es), nil

	case *Filter:
		in, err := x.Child.Schema()
		if err != nil {
			return nil, err
		}
		ps, err := expr.ExpandAll(x.Preds, in)
		if err != nil {
			return nil, annotate(err, x, path, in)
		}
		for _, p := range ps {
			f, err := p.Field(in)
			if err != nil {
				return nil, annotate(err, x, path, in)
			}
			if f.Type.ID != dtype.TypeBoolean && f.Type.ID != dtype.TypeNull {
				return nil, annotate(&uerr.PredicateType{Expr: p.String(), Got: f.Type}, x, path, in)
			}
		}
		return NewFilter(x.Child, ps), nil
	}
	return n, nil
}

func checkOutputs(ns []expr.Node, in *dtype.Schema) error {
	seen := make(map[string]int, len(ns))
	for i, n := range ns {
		f, err := n.Field(in)
		if err != nil {
			return err
		}
		if j, dup := seen[f.Name]; dup {
			return &uerr.DuplicateOutput{Name: f.Name, First: ns[j].String(), Second: n.String()}
		}
		seen[f.Name] = i
	}
	return nil
}
```

`annotate` produces the error shape the docs promise, with a **deterministic node address** (DFS preorder index, computed from `path` — no atomic counters, so golden files are stable):

```
ursus: SELECT (plan node #3): unknown column "typo_column"
  available: tenant_id, user_id, event, ts, payload
  did you mean "top_column"?
  source: SCAN parquet events/*.parquet
```

---

## 8. The optimizer

### 8.1 Framework

```go
package plan

type RunMode uint8

const (
	RunOnce RunMode = iota
	RunToFixedPoint
)

// Rule is one optimizer transformation over a whole plan.
type Rule interface {
	Name() string
	Mode() RunMode
	// Rewrite returns a possibly-new plan. `changed` drives fixed-point
	// iteration and tracing; it must be false when the plan is unmodified.
	Rewrite(root Node, c *Ctx) (out Node, changed bool, err error)
}

// LocalRule is the easy case: a pure node-to-node replacement, applied
// bottom-up with children already rewritten. Local adapts it to Rule.
type LocalRule interface {
	Name() string
	Match(n Node, c *Ctx) (Node, bool, error)
}

func Local(r LocalRule, mode RunMode) Rule { return localRule{r, mode} }

type Ctx struct {
	Flags   OptFlags
	MaxIter int // fixed-point cap; 0 => 16
	// Verify re-resolves the ROOT schema after every rule and fails if it
	// changed. On by default in tests; the single highest-value optimizer
	// safety net there is.
	Verify bool
	Trace  func(rule string, before, after Node)
}

type Optimizer struct{ rules []Rule }

func NewOptimizer(rules ...Rule) *Optimizer { return &Optimizer{rules: rules} }

// DefaultOptimizer honours OptFlags so every pass is individually switchable
// for debugging and benchmarking, per the doc's OptFlags contract.
func DefaultOptimizer(f OptFlags) *Optimizer {
	var rs []Rule
	if f.ProjectionPushdown {
		rs = append(rs, ProjectionPushdown())
	}
	if f.SimplifyExpressions {
		rs = append(rs, Local(removeIdentityProject{}, RunToFixedPoint))
	}
	return NewOptimizer(rs...)
}

func (o *Optimizer) Run(root Node, c *Ctx) (Node, error) {
	if c == nil {
		c = &Ctx{Flags: DefaultOptFlags()}
	}
	maxIter := cmp.Or(c.MaxIter, 16)

	var want *dtype.Schema
	if c.Verify {
		s, err := root.Schema()
		if err != nil {
			return nil, err
		}
		want = s
	}

	for _, r := range o.rules {
		iters := 1
		if r.Mode() == RunToFixedPoint {
			iters = maxIter
		}
		converged := false
		for i := 0; i < iters; i++ {
			before := root
			next, changed, err := r.Rewrite(root, c)
			if err != nil {
				return nil, fmt.Errorf("ursus: optimizer: rule %s: %w", r.Name(), err)
			}
			root = next
			if c.Trace != nil && changed {
				c.Trace(r.Name(), before, root)
			}
			if c.Verify {
				got, err := root.Schema()
				if err != nil {
					return nil, fmt.Errorf("ursus: optimizer: rule %s broke schema resolution: %w", r.Name(), err)
				}
				if !got.Equal(want) {
					return nil, fmt.Errorf(
						"ursus: optimizer: rule %s changed the output schema\n  before: %s\n  after:  %s",
						r.Name(), want, got)
				}
			}
			if !changed {
				converged = true
				break
			}
		}
		if !converged {
			// A rule that oscillates is a bug, not a user error.
			return nil, fmt.Errorf("ursus: optimizer: rule %s did not reach a fixed point in %d iterations",
				r.Name(), maxIter)
		}
	}
	return root, nil
}
```

Rules that are *local* (constant folding, `Not(Not(x)) → x`, identity-projection removal) implement `LocalRule` and run to a fixed point. Rules that need inherited context (projection pushdown, predicate pushdown, slice pushdown) implement `Rule` directly and run once. That split is exactly the "runs: once / until fixed point" column of the features doc.

### 8.2 Projection pushdown — the one implemented rule

```go
package plan

type projectionPushdown struct{}

func ProjectionPushdown() Rule { return projectionPushdown{} }

func (projectionPushdown) Name() string  { return "projection_pushdown" }
func (projectionPushdown) Mode() RunMode { return RunOnce }

func (r projectionPushdown) Rewrite(root Node, c *Ctx) (Node, bool, error) {
	// nil at the root means "keep producing exactly what you produce today".
	return r.push(root, nil, c)
}

// push rewrites n so that it still produces every column in need — and, where
// it can prove it is safe, nothing else. need == nil means "everything".
func (r projectionPushdown) push(n Node, need nameSet, c *Ctx) (Node, bool, error) {
	switch x := n.(type) {

	case *Scan:
		if need == nil || x.full == nil {
			return n, false, nil
		}
		keep := x.full.Names() // fresh, source-ordered
		keep = slices.DeleteFunc(keep, func(s string) bool { return !need.has(s) })
		if x.Projection == nil && len(keep) == x.full.Len() {
			return n, false, nil
		}
		if slices.Equal(x.Projection, keep) {
			return n, false, nil
		}
		return x.derive(func(s *Scan) { s.Projection = keep }), true, nil

	case *Filter:
		// A filter reads its predicates' roots in addition to whatever the
		// parent wants. This is what puts "x" into the scan's projection.
		child, ch, err := r.push(x.Child, union(need, rootsOf(x.Preds)), c)
		if err != nil || !ch {
			return n, false, err
		}
		return NewFilter(child, x.Preds), true, nil

	case *Project:
		keep, pruned := x.Exprs, false
		if need != nil && x.Kind == ProjectSelect {
			// Dead-column elimination falls out for free: drop expressions whose
			// output nobody upstream asked for. Safe in v0.1 because no v0.1
			// expression has side effects; v0.2 UDFs will carry a purity flag
			// consulted here.
			keep = slices.DeleteFunc(slices.Clone(x.Exprs), func(e expr.Node) bool {
				on, err := expr.OutputName(e)
				return err == nil && !need.has(on)
			})
			if len(keep) == 0 {
				keep = x.Exprs[:1] // a projection is never empty
			}
			pruned = len(keep) != len(x.Exprs)
		}
		childNeed := rootsOf(keep)
		if x.Kind == ProjectWith {
			in, err := x.Child.Schema()
			if err != nil {
				return nil, false, err
			}
			// WithColumns passes its input through, so it also needs whatever
			// the parent asked for that comes from below.
			childNeed = union(childNeed, intersect(need, in.Names()))
		}
		child, ch, err := r.push(x.Child, childNeed, c)
		if err != nil {
			return nil, false, err
		}
		if !ch && !pruned {
			return n, false, nil
		}
		return NewProject(child, x.Kind, keep), true, nil

	default:
		// CONSERVATIVE DEFAULT. An operator whose column dependencies this rule
		// does not model requires all of its inputs. This is what makes it safe
		// to add Sort / Join / Aggregate / Explode before teaching this rule
		// about them: the worst case is a missed optimisation, never a wrong
		// answer.
		cs := n.Children()
		if len(cs) == 0 {
			return n, false, nil
		}
		out := make([]Node, len(cs))
		changed := false
		for i, ch := range cs {
			nc, cch, err := r.push(ch, nil, c)
			if err != nil {
				return nil, false, err
			}
			out[i] = nc
			changed = changed || cch
		}
		if !changed {
			return n, false, nil
		}
		return n.WithChildren(out), true, nil
	}
}

func rootsOf[T interface{ ~[]expr.Node }](ns T) nameSet {
	s := nameSet{}
	for _, n := range ns {
		for _, r := range expr.RootNames(n) {
			s[r] = struct{}{}
		}
	}
	return s
}
```

Two invariants that make this rule correct and reviewable:
1. **`need == nil` propagates downward as "everything"** and is the only value the root ever passes. Narrowing only ever happens through a node that this rule explicitly models.
2. **Pruning may change an intermediate node's schema, never the root's.** `Ctx.Verify` compares only the root schema, deliberately.

`removeIdentityProject` is the `LocalRule` demonstration: drop a `ProjectSelect` whose expressions are exactly `col(name)` for the child's schema in order.

---

## 9. Explain

```go
package plan

type PlanStage uint8

const (
	StageRaw PlanStage = iota // as built: matchers unexpanded, no schemas
	StageResolved
	StageOptimized // default
)

type RenderOptions struct {
	Indent        string // default "  "
	Types         bool
	Streamability bool // v0.2; renders "streaming: ?" today
}

// Render is a pure, deterministic function of the tree. It contains no
// addresses, no timings and no map-iteration order, so its output is a valid
// golden file.
func Render(n Node, o RenderOptions) string {
	if o.Indent == "" {
		o.Indent = "  "
	}
	var b strings.Builder
	render(&b, n, 0, o)
	return b.String()
}

func render(b *strings.Builder, n Node, depth int, o RenderOptions) {
	pad := strings.Repeat(o.Indent, depth)
	b.WriteString(pad)
	b.WriteString(n.Label())
	if o.Types {
		if s, err := n.Schema(); err == nil {
			b.WriteString("  → ")
			b.WriteString(s.String())
		}
	}
	b.WriteByte('\n')
	for _, d := range n.Detail(o) {
		b.WriteString(pad)
		b.WriteString(o.Indent)
		b.WriteString(d)
		b.WriteByte('\n')
	}
	for _, c := range n.Children() {
		render(b, c, depth+1, o)
	}
}
```

`Label`/`Detail` are node methods rather than a type switch in `explain.go`, so it is **structurally impossible to add a node type without a rendering**. The tree-walking and indentation policy still live in one place.

For `f.parquet` with schema `{a: Int64, b: String, x: Int32, junk: Float64}` and the target query:

**`StageRaw`**
```
SELECT [col("a"), col("b")]
  FILTER [col("x") > 5]
    SCAN parquet
      paths: ["f.parquet"]
```

**`StageOptimized`** (default)
```
SELECT [col("a"), col("b")]
  FILTER [col("x") > 5:Int32]
    SCAN parquet
      paths: ["f.parquet"]
      projection: [a, b, x] (3/4)
```

**`StageOptimized` with `ShowTypes(true)`**
```
SELECT [col("a"), col("b")]  → {a: Int64, b: String}
  FILTER [col("x") > 5:Int32]  → {a: Int64, b: String, x: Int32}
    SCAN parquet  → {a: Int64, b: String, x: Int32}
      paths: ["f.parquet"]
      projection: [a, b, x] (3/4)
```

The `5:Int32` is literal narrowing made visible — exactly the class of change a golden file should lock down.

---

## 10. The public façade

### 10.1 `Literal` and `Operand` (D3) — the version that compiles

```go
package ursus

import "time"

// Literal is the set of Go types that lift to a column literal.
//
// Every accepted width is enumerated explicitly: Go infers a type parameter
// from the argument's type with NO implicit widening, so a.Gt(int32(5)) is a
// compile error unless ~int32 appears here.
//
// time.Duration is DELIBERATELY ABSENT: its underlying type is int64, so
// listing it alongside ~int64 is "overlapping terms" and does not compile.
// ~int64 already admits it, and a `switch any(v).(type) { case time.Duration: }`
// recovers the named type exactly.
//
// time.Time is a bare (non-~) term: it is a defined struct type, disjoint from
// every other term. A union term may be a type with methods; only interface
// terms with methods are forbidden.
type Literal interface {
	~bool |
		~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64 |
		~string | ~[]byte |
		time.Time
}

// Operand is either another Expr or a Go scalar that lifts to a literal.
type Operand interface{ Expr | Literal }
```

Disjointness audit (the check the compiler runs pairwise): `~int` / `~int64` are distinct on all platforms (`int`'s underlying type is `int`); `~uint8` is listed once (`byte` is an alias, listing both would overlap); `~int32` is listed once (`rune` is an alias); `~[]byte`'s type set contains only slice-underlying types; `time.Time`'s underlying type is a struct; `Expr`'s underlying type is a different struct. No pair overlaps.

Consequence of `~`: a user may pass `type UserID int64`. `any(v).(type)` will not match `case int64`, so `litNode` needs a reflect fallback. Build-time only, never per row.

```go
// lift converts an Operand to a node.
func lift[T Operand](v T) expr.Node {
	if e, ok := any(v).(Expr); ok {
		return e.node()
	}
	return litNode(any(v))
}

func litNode(v any) expr.Node {
	switch x := v.(type) {
	case bool:
		return &expr.Literal{Value: x, DT: dtype.Bool}
	case int:
		return &expr.Literal{Value: int64(x), DT: dtype.Int64}
	case int8:
		return &expr.Literal{Value: x, DT: dtype.Int8}
	// … int16 int32 int64 uint uint8 uint16 uint32 uint64 float32 float64 …
	case string:
		return &expr.Literal{Value: x, DT: dtype.Str}
	case []byte:
		return &expr.Literal{Value: x, DT: dtype.Bin}
	case time.Duration: // matched before int64 would ever be considered
		return &expr.Literal{Value: int64(x), DT: dtype.Duration(dtype.UnitNano)}
	case time.Time:
		// The instant is preserved in UTC. Use Dt().ConvertTimeZone for
		// wall-clock work; a literal never smuggles in a *time.Location.
		return &expr.Literal{Value: x.UTC(), DT: dtype.Datetime(dtype.UnitNano, "UTC")}
	}
	return litNodeReflect(v) // user-defined named types admitted by ~
}
```

### 10.2 `Expr`

```go
// Expr is a lazy description of a column transformation.
//
// It is a CONCRETE STRUCT, not an interface: Go 1.27 forbids generic methods on
// interfaces, and Gt[T Operand] is the whole point. Polymorphism lives in the
// unexported expr.Node.
//
// The zero value is invalid but never panics: it degrades to a sticky error
// surfaced at Collect.
type Expr struct{ n expr.Node }

func (e Expr) node() expr.Node {
	if e.n == nil {
		return &expr.Err{E: errors.New("ursus: zero-value Expr; construct with Col, Lit, All, …")}
	}
	return e.n
}

// Err returns the first construction error carried by this expression, if any.
func (e Expr) Err() error {
	if x, ok := e.node().(*expr.Err); ok {
		return x.E
	}
	return nil
}

// ---- constructors -------------------------------------------------------

// Col references columns by name. One name is a plain column reference; more
// than one expands, and every name must exist.
func Col(names ...string) Expr {
	switch len(names) {
	case 0:
		return errExpr("Col: at least one name is required")
	case 1:
		return Expr{n: &expr.Column{Name: names[0]}}
	default:
		return Expr{n: &expr.Match{M: expr.Names(slices.Clone(names), true)}}
	}
}

// ColRegex selects columns whose names match pattern. There is no ^…$ magic:
// the pattern is always a pattern. A bad pattern becomes a sticky error here,
// surfaced at Collect (D15).
func ColRegex(pattern string) Expr {
	m, err := expr.Regex(pattern)
	if err != nil {
		return errExpr("ColRegex(%q): %v", pattern, err)
	}
	return Expr{n: &expr.Match{M: m}}
}

func ColDType(types ...DataType) Expr { return Expr{n: &expr.Match{M: expr.DTypes(slices.Clone(types))}} }
func All() Expr                       { return Expr{n: &expr.Match{M: expr.All()}} }
func Exclude(names ...string) Expr    { return All().Exclude(names...) }
func Lit[T Literal](v T) Expr         { return Expr{n: litNode(any(v))} }
func LitNull(dt DataType) Expr        { return Expr{n: &expr.Literal{Value: nil, DT: dt}} }

// Exclude subtracts names from a multi-column selection. It is only meaningful
// on a selection, so applying it elsewhere is an error rather than a no-op.
func (e Expr) Exclude(names ...string) Expr {
	n := e.node()
	if bad := expr.FirstErr(n); bad != nil {
		return Expr{n: bad}
	}
	m, ok := n.(*expr.Match)
	if !ok {
		return errExpr("Exclude: applies only to a multi-column selection " +
			"(All, Col with several names, ColRegex, ColDType, a selector); got %s", n)
	}
	return Expr{n: &expr.Match{M: expr.Difference(m.M, expr.Names(slices.Clone(names), false))}}
}

// ---- operators: scalars lift automatically (D20) -------------------------

func (e Expr) Gt[T Operand](v T) Expr { return e.bin(expr.OpGt, lift(v)) }
func (e Expr) Ge[T Operand](v T) Expr { return e.bin(expr.OpGe, lift(v)) }
// Lt, Le, Eq, Ne, EqMissing, NeMissing, Add, Sub, Mul, Div, FloorDiv, Mod, Pow
// are byte-for-byte identical modulo the op constant.

func (e Expr) bin(op expr.BinaryOp, rhs expr.Node) Expr {
	l := e.node()
	if bad := expr.FirstErr(l, rhs); bad != nil {
		return Expr{n: bad}
	}
	return Expr{n: &expr.Binary{Op: op, LHS: l, RHS: rhs}}
}

// And / Or take Expr, not Operand: a bare Go bool is not a predicate.
func (e Expr) And(others ...Expr) Expr { return e.chain(expr.OpAnd, others) }
func (e Expr) Or(others ...Expr) Expr  { return e.chain(expr.OpOr, others) }

func (e Expr) Not() Expr       { return e.un(expr.OpNot) }
func (e Expr) IsNull() Expr    { return e.un(expr.OpIsNull) }
func (e Expr) IsNotNull() Expr { return e.un(expr.OpIsNotNull) }

// ---- manipulation --------------------------------------------------------

// Alias sets the output name. It cannot be applied to an expression that
// expands to more than one column; use Name().Prefix/Suffix/Map for that.
func (e Expr) Alias(name string) Expr {
	n := e.node()
	if bad := expr.FirstErr(n); bad != nil {
		return Expr{n: bad}
	}
	return Expr{n: &expr.Alias{Child: n, Name: name}}
}

func (e Expr) Cast(dt DataType, opts ...CastOption) Expr { /* &expr.Cast{...} */ }

// ---- multi-arg lifting, per D20 -----------------------------------------

func (e Expr) IsBetween[L, H Operand](lo L, hi H, closed Closed) Expr
func (e Expr) Clip[L, H Operand](lo L, hi H) Expr
func (e Expr) FillNullWith[T Operand](v T) Expr
func (w WhenBuilder) Then[T Operand](v T) ThenBuilder
func (t ThenBuilder) Otherwise[T Operand](v T) Expr

// Lits closes the one gap the convention leaves: a variadic list of values
// cannot be both generic and heterogeneous.
func Lits[T Literal](vs ...T) []Expr
func (e Expr) IsInValues[T Literal](vs ...T) Expr

// ---- name namespace ------------------------------------------------------

type NameExpr struct{ e Expr }

func (e Expr) Name() NameExpr { return NameExpr{e} }

func (n NameExpr) Prefix(p string) Expr {
	return n.rename("prefix("+strconv.Quote(p)+")", func(s string) string { return p + s })
}
func (n NameExpr) Suffix(s string) Expr { /* … */ }
func (n NameExpr) Map(fn func(string) string) Expr {
	// Label cannot describe an arbitrary func, so it is opaque but stable.
	return n.rename("map(fn)", fn)
}
func (n NameExpr) rename(label string, fn func(string) string) Expr {
	c := n.e.node()
	if bad := expr.FirstErr(c); bad != nil {
		return Expr{n: bad}
	}
	return Expr{n: &expr.RenameOutput{Child: c, Label: label, Fn: fn}}
}
```

### 10.3 `LazyFrame` (D10)

```go
// LazyFrame is an unexecuted query plan.
//
// PERSISTENCE (D10): a LazyFrame is IMMUTABLE. Every builder method returns a
// new one; the receiver is never modified. Sharing a base frame is therefore
// safe and cheap, including across goroutines:
//
//	base := ursus.ScanParquet("f.parquet")
//	a := base.Filter(ursus.Col("x").Gt(5))   // base unchanged
//	b := base.Filter(ursus.Col("y").Gt(5))   // a and b share the Scan node
//
// ERRORS (principle 3): builder methods never return an error. The first error
// sticks and is surfaced at Collect / Schema / Explain, or early via Err.
type LazyFrame struct {
	root plan.Node
	err  error
}

func ScanParquet(paths ...string) *LazyFrame { return ScanParquetOpts(paths) }

func ScanParquetOpts(paths []string, opts ...ParquetScanOption) *LazyFrame {
	src, err := parquet.NewSource(slices.Clone(paths), opts...)
	if err != nil {
		return &LazyFrame{err: fmt.Errorf("ursus: ScanParquet: %w", err)}
	}
	return &LazyFrame{root: plan.NewScan(src)}
}

func (lf *LazyFrame) Err() error { return lf.err }

func (lf *LazyFrame) Select(exprs ...Expr) *LazyFrame {
	return lf.project(plan.ProjectSelect, "Select", exprs)
}
func (lf *LazyFrame) WithColumns(exprs ...Expr) *LazyFrame {
	return lf.project(plan.ProjectWith, "WithColumns", exprs)
}

func (lf *LazyFrame) project(k plan.ProjectKind, what string, es []Expr) *LazyFrame {
	if lf.err != nil {
		return lf // safe to alias precisely because LazyFrame is immutable
	}
	ns, err := absorb(what, es)
	if err != nil {
		return lf.fail(err)
	}
	if len(ns) == 0 {
		return lf.fail(fmt.Errorf("ursus: %s: at least one expression is required", what))
	}
	return &LazyFrame{root: plan.NewProject(lf.root, k, ns)}
}

// Filter keeps rows for which every predicate is TRUE.
//
// NULL SEMANTICS (D18): a row whose predicate evaluates to null is DROPPED,
// following SQL three-valued logic. Use Col("x").IsNull().Or(pred) to keep
// them, or Remove for the complementary set.
func (lf *LazyFrame) Filter(preds ...Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	ns, err := absorb("Filter", preds)
	if err != nil {
		return lf.fail(err)
	}
	if len(ns) == 0 {
		return lf.fail(errors.New("ursus: Filter: at least one predicate is required"))
	}
	return &LazyFrame{root: plan.NewFilter(lf.root, ns)}
}

// Remove drops rows matching ANY of the predicates.
//
// A row is removed only when a predicate is TRUE; rows where every predicate is
// false OR NULL are kept. Filter(p) and Remove(p) therefore partition the input
// exactly. Adding predicates to Remove always removes more rows, mirroring the
// way adding predicates to Filter always keeps fewer.
func (lf *LazyFrame) Remove(preds ...Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(preds) == 0 {
		return lf.fail(errors.New("ursus: Remove: at least one predicate is required"))
	}
	var anyTrue Expr
	for i, p := range preds {
		t := p.IsNotNull().And(p) // COALESCE(p, false) using only skeleton nodes
		if i == 0 {
			anyTrue = t
		} else {
			anyTrue = anyTrue.Or(t)
		}
	}
	return lf.Filter(anyTrue.Not())
}

// Drop and Rename are pure sugar over Select, so they cost no new plan node.
func (lf *LazyFrame) Drop(names ...string) *LazyFrame { return lf.Select(All().Exclude(names...)) }

func (lf *LazyFrame) fail(err error) *LazyFrame { return &LazyFrame{root: lf.root, err: err} }

// absorb converts public Exprs to nodes, turning an expression-level sticky
// error into a frame-level one with call-site context (D15).
func absorb(what string, es []Expr) ([]expr.Node, error) {
	out := make([]expr.Node, len(es))
	for i, e := range es {
		n := e.node()
		if bad, ok := n.(*expr.Err); ok {
			return nil, fmt.Errorf("ursus: %s: expression %d: %w", what, i+1, bad.E)
		}
		out[i] = n
	}
	return out, nil
}

// ---- terminals: ctx at every execution boundary --------------------------

// Schema resolves the output schema without reading any row data. It takes a
// context because resolving a SOURCE schema is I/O (a Parquet footer, an S3
// request). The API doc's ctx-free signature is not implementable.
func (lf *LazyFrame) Schema(ctx context.Context) (*Schema, error) {
	if lf.err != nil {
		return nil, lf.err
	}
	n, err := plan.Resolve(ctx, lf.root)
	if err != nil {
		return nil, err
	}
	return n.Schema()
}

func (lf *LazyFrame) Explain(ctx context.Context, opts ...ExplainOption) (string, error) {
	if lf.err != nil {
		return "", lf.err
	}
	o := explainOptions(opts)
	n := lf.root
	var err error
	if o.Stage >= plan.StageResolved {
		if n, err = plan.Resolve(ctx, n); err != nil {
			return "", err
		}
	}
	if o.Stage >= plan.StageOptimized {
		if n, err = plan.DefaultOptimizer(o.Flags).Run(n, &plan.Ctx{Flags: o.Flags}); err != nil {
			return "", err
		}
	}
	return plan.Render(n, o.Render), nil
}

func (lf *LazyFrame) Collect(ctx context.Context, opts ...CollectOption) (*DataFrame, error) {
	if lf.err != nil {
		return nil, lf.err
	}
	o := collectOptions(opts)
	n, err := plan.Resolve(ctx, lf.root)
	if err != nil {
		return nil, err
	}
	if n, err = plan.DefaultOptimizer(o.Flags).Run(n, &plan.Ctx{Flags: o.Flags}); err != nil {
		return nil, err
	}
	op, err := physical.Plan(n, o.Physical) // agent 2
	if err != nil {
		return nil, err
	}
	return drain(ctx, op)
}
```

---

## 11. Contracts I require

### 11.1 From agent 3 — `ursus/internal/dtype`

The one structural requirement: **the type system must live in `ursus/internal/dtype` and be re-exported from `ursus` by type alias.** Without that, `internal/expr` → `ursus` → `internal/expr` is an import cycle and the whole design fails. Everything else is small.

```go
package dtype

type TypeID uint8
const TypeIDCount = /* one past the last TypeID; I index arrays by TypeID */

type DataType struct{ ID TypeID; /* … */ }   // need NOT be comparable

func (d DataType) Equal(o DataType) bool
func (d DataType) String() string      // STABLE across builds: golden files depend on it
func (d DataType) IsNumeric() bool
func (d DataType) IsInteger() bool
func (d DataType) IsSignedInteger() bool
func (d DataType) IsUnsignedInteger() bool
func (d DataType) IsFloat() bool
func (d DataType) IsTemporal() bool
func (d DataType) IsNested() bool
func (d DataType) BitWidth() int        // 0 for non-fixed-width; drives commonInt

type Field struct{ Name string; Type DataType; Nullable bool }
func (f Field) Equal(o Field) bool
func (f Field) Rename(name string) Field       // used on every naming path

type Schema struct{ /* fields []Field + index map[string]int */ }
func NewSchema(fields ...Field) (*Schema, error)   // ERRORS on duplicate names
func (s *Schema) Len() int
func (s *Schema) Field(i int) Field
func (s *Schema) ByName(name string) (Field, bool)
func (s *Schema) IndexOf(name string) int          // -1 if absent
func (s *Schema) Names() []string                  // FRESH slice, source order — I mutate it
func (s *Schema) FieldSlice() []Field              // FRESH slice — I mutate it
func (s *Schema) Select(names []string) (*Schema, error)
func (s *Schema) Equal(o *Schema) bool
func (s *Schema) String() string                   // STABLE: "{a: Int64, b: String}"
```

Notes:
- **`*Schema` everywhere internally**, immutable and shared. `Names()` and `FieldSlice()` must return fresh slices; projection pushdown and `Project.Schema` mutate them in place.
- **`DataType` need not be comparable** (D2). Keep `Fields []Field` and `Categories *Categories`. I need no `map[DataType]X`; my promotion lattice indexes an array by `TypeID`. If `Equal` must compare `Categories`, compare set *identity*, not contents.
- `String()` on both `DataType` and `Schema` is load-bearing: it appears in golden Explain files and in every type error.

### 11.2 From agent 2 — evaluator, `Column`, physical

```go
// The one contract that makes lazy schema resolution honest:
//
//   For every resolved expr.Node n and every batch b,
//       Eval(ctx, n, b).DType()  ==  n.Field(b.Schema()).Type
//
// A differential test enumerates the op × dtype matrix and asserts it.
package eval
func Eval(ctx context.Context, n expr.Node, in *Batch) (Column, error)
```

1. **`ResolveBinary` is the only promotion logic.** The evaluator dispatches kernels by casting LHS to `res.CastL` and RHS to `res.CastR` and producing `res.Out`. It must not re-derive types. Same for `expr.unaryTable` (I will export an accessor `expr.UnaryOut(op, in) (dtype.DataType, error)`).
2. **Scalar broadcast.** `Lit(5)` evaluates to a length-1 column. `Column` must expose `IsScalar() bool` and every kernel must broadcast it, because `Col("x").Gt(5)` depends on it and I do not emit a `Broadcast` node.
3. **Filter null semantics.** `FilterOp` keeps row `i` iff `valid[i] && value[i]` — null is dropped (D18).
4. **Sources are dual-faced.** Every concrete `plan.Source` also implements
   ```go
   package physical
   type Openable interface {
       Open(ctx context.Context, spec plan.ScanSpec) (BatchStream, error)
   }
   ```
   and `physical.Plan` asserts it. `plan.ScanSpec.Projection` is source-ordered; if `Source.Caps().Projection` is false, the physical planner inserts a trim operator itself — the logical plan always records the projection.
5. **`physical.Plan(n plan.Node, o Options) (Operator, error)`** consumes an *optimized, resolved* plan and may assume: every `Project` holds single-output expressions; every `Filter` predicate is Boolean-typed; every `Scan.full` is non-nil.
6. `Batch.Schema() *dtype.Schema`, and `Column.DType() dtype.DataType`.

---

## 12. What I deliberately left out, and where it plugs in

| Deferred | The seam that is already there |
|---|---|
| **Group-by / Agg** | `*Agg{Op, Child, Opts}` expression node (typing via an `aggTable` mirroring `unaryTable`) and a reserved `*Aggregate{Child, Keys, Aggs, MaintainOrder, Having}` plan node. Projection pushdown's conservative `default` handles it correctly the day it appears; teaching the rule about it is a 12-line case. |
| **Joins** | `*Join{Left, Right, LeftOn, RightOn, Kind, Opts}`. `Node.Children()` already returns N children and `push` already recurses over all of them. Join key expressions are `expr.Node`, so `On(Col("a"))` needs nothing new. |
| **Windows / `over`** | `*Window{Child, Spec}` expression node. `Spec.PartitionBy` and `OrderBy` are ordinary children, so `RootNames` and expansion pick them up automatically — `Col("x").Mean().Over(Col("g"))` contributes `g` to pushdown for free. |
| **Nested scopes (`List().Eval`, `Element()`)** | Already specified in `Node.Children()`'s doc comment: a scope body is *not* a child, so no walker can rewrite across it. `ListEval` will hold `Body Node` outside its `Children()`. |
| **Horizontal functions and `Fold`** | `varargNode` + `spliceVarargs` are wired into `Expand` today and are a no-op in v0.1. `SumHorizontal(All())` will collapse to one column without touching `Expand`. |
| **The other ~450 Expr methods** | `*Func{F Function, Args []Node}` with `Function{Name, Output([]Field) (Field,error), Arity, Variadic}`. 450 methods become 450 eight-line `Function` values, not 450 node types, and each gets `Explain` and typing for free. |
| **Predicate / slice pushdown, CSE, constant folding, type-coercion casts** | The `Rule`/`LocalRule`/`RunMode` framework and `OptFlags` wiring exist; each is one file. `Filter.Preds` is already a conjunct slice so predicate pushdown does not have to split `And` trees first. |
| **Streamability in Explain** | `RenderOptions.Streamability` is plumbed; nodes will add one line. |
| **SQL lowering** | `ursus/sql` imports `ursus/internal/plan` directly (legal under the internal rule) and returns frames via `ursus.FromPlan`. Most of SQL lowers to `Select`/`Filter`/`GroupBy` builder calls, not to raw nodes. |
| **Plan serialization** | Deliberately *not* attempted. Both `Node` interfaces are sealed, so a switch-based codec in `internal/plan` is exhaustive by construction. The one blocker I have already accounted for: `RenameOutput.Fn` and `Pred.Fn` are func values, which is exactly why both carry a `Label`/`Key` string — a serializable form will register named functions and reject anonymous ones with a clear error. |

---

## 13. Test strategy

Everything below runs **without an executor, without Arrow, and without a Parquet file**, using a `fakeSource{schema}` implementing `plan.Source`. That is the point of putting `Source.Schema(ctx)` behind an interface.

1. **Expression typing** — table tests `{schema, expr, wantFields | wantErrIs}`. Covers every `BinaryOp × dtype` pair, nullability propagation, literal narrowing (`Col("i32").Gt(5) → Int32`, `Col("u8").Gt(300) → Int64`), `Div` always float, Bool-in-arithmetic rejection.
2. **Naming** — `{expr, wantName}`: leftmost wins, `Lit(1).Add(Col("a")) → "literal"`, `Alias` overrides, `Name().Prefix` composes, `RenameOutput` over `Alias`.
3. **Expansion** — `{schema, expr, wantStrings []string}` asserting count *and* each node's canonical `String()`. Cases: `Col("a","b")`, `ColRegex`, `ColDType`, `All`, `All().Exclude`, selector set algebra, matcher-matches-nothing → zero columns (not an error), two different matchers → `MultipleExpansions`, two identical matchers → one expansion, `Col("a","b").Add(1).Alias("x")` → `ExpansionNameCollision` with the alias hint.
4. **Error-message golden strings.** Did-you-mean suggestions and the available-columns list are a headline feature; snapshot them as `testdata/errors/*.txt`. Regressions here are invisible otherwise.
5. **Plan schema resolution** — build over `fakeSource`, assert `root.Schema()`. Covers `WithColumns` replace-in-place vs append, `Select` duplicate-output detection, `Filter` non-Boolean predicate rejection.
6. **Optimizer golden files** — `ursustest.AssertPlan(t, lf, "testdata/plans/pushdown_basic.txt")` with a `-update` flag. One file per rule per interesting shape.
7. **The universal optimizer invariant** — a helper that runs every table case with `Ctx{Verify: true}`, asserting the *root* schema is byte-identical before and after each rule. This catches an entire class of optimizer bug for free and must be on in every optimizer test.
8. **Pushdown soundness property** — over a generated corpus of plans: for every `*Scan` in the optimized plan, its projection ⊇ the union of `RootNames` of every expression anywhere above it on the path to the root. This is the property that keeps the conservative `default` case honest as node types are added.
9. **Immutability** — `base := scan(); a := base.Filter(...); b := base.Select(...)`; assert `Explain(base)` is byte-identical before and after, that `a` and `b` differ, and that resolving `a` does not mutate `base` (compare `Explain(base, StageRaw)`). Plus a `-race` test hammering one `*LazyFrame` from N goroutines through `Schema` and `Explain`.
10. **Fuzz `FuzzExprResolve`** — generate an expression from a small grammar plus a random schema; assert no panic, and that the result is *either* an error *or* exactly one output field with a dtype that satisfies the op's declared output predicate.
11. **Constraint regression tests, which are the ones that silently rot.** A `testdata/nocompile/` directory outside the build, plus a test that shells out to `go build` and asserts specific failures:
    - `Literal` with `~int64 | time.Duration` fails with `overlapping terms` (locks D3 in).
    - `a.Gt(int32(5))` **succeeds** (so the enumeration is complete) while `a.Gt(myStruct{})` fails.
    - A generic method on an interface fails with `interface method must have no type parameters` (locks principle #2 in).
    These are cheap and they are the only defence against someone "simplifying" `Literal` back into something that does not compile.
12. **The end-to-end skeleton test**, once agent 2 lands: `ScanParquet(f).Filter(Col("x").Gt(5)).Select(Col("a"), Col("b")).Collect(ctx)` over a fixture file, asserting both the frame contents *and* the golden optimized plan — so that a regression in pushdown is caught even when the answer is still correct.

---

## 14. Doc defects found beyond the assigned list

- **`LazyFrame.Schema()` cannot be ctx-free** (§6 line 726). Resolving a source schema is I/O. Signature must be `Schema(ctx context.Context) (*Schema, error)`; same for `Explain`.
- **`ursus.Len()`** is used in the §16 worked example (line 1435) but never declared in §4.7.
- **`ursus.ManyToOne`** is used in the §8 example (line 923) but the constant is declared as `ValidateManyToOne` (line 904).
- **`Field` in §2 has `Nullable`, but no operation in §4 says how nullability propagates.** Specified here: comparisons and arithmetic propagate `l || r`; `IsNull`/`IsNotNull` are never null; non-strict `Cast` always yields nullable.
- **`Expr` has no `Err()`** even though `LazyFrame` does. Added.
- **`MetaExpr.OutputName() (string, error)`** is the only signature in §4.5 that admits an error, which is correct and confirms the multi-output design — but §4.5 never says `RootNames`/`HasMultipleOutputs` need expansion to be meaningful. They do: they are conservative before expansion.

---

### Critical Files for Implementation

- `/home/beck/GolandProjects/ursus/internal/expr/node.go` — `expr.Node` (sealed), the node set, `Field` implementations, `Err`, `FirstErr`
- `/home/beck/GolandProjects/ursus/internal/expr/expand.go` — `Expand`/`ExpandAll`, `Matcher`, the `varargNode` hook, the name-collision check (the heart of D11/D12)
- `/home/beck/GolandProjects/ursus/internal/plan/resolve.go` — the resolve phase: source schemas, expansion, type checking, error annotation
- `/home/beck/GolandProjects/ursus/internal/plan/rule_projection.go` — `Rule`/`LocalRule`/`Optimizer` driver plus projection pushdown with its conservative default
- `/home/beck/GolandProjects/ursus/lazy.go` — `LazyFrame` immutability, sticky errors, `Remove` lowering, the `Collect`/`Explain`/`Schema` pipeline
- `/home/beck/GolandProjects/ursus/expr.go` — `Expr`, `Literal`, `Operand`, `lift`/`litNode` (the D3/D20 surface)