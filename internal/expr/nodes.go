package expr

import (
	"fmt"
	"strconv"
	"strings"

	"ursus/dtype"
	"ursus/internal/uerr"
)

// Resolve is the entry point for expression type resolution.
//
// It validates the tree once — deferred errors, leftover matchers — and then
// delegates to Field. Field implementations assume a resolvable tree, so the
// validation is not repeated at every level; doing it inside Field would make
// resolution quadratic in tree depth.
func Resolve(n Node, in *dtype.Schema) (dtype.Field, error) {
	if err := FirstErr(n); err != nil {
		return dtype.Field{}, err
	}
	if HasMatch(n) {
		return dtype.Field{}, uerr.Internalf("%v: %s", ErrUnexpanded, n.String())
	}
	return n.Field(in)
}

// --- Col ---------------------------------------------------------------------

// Col reads a column by name.
type Col struct{ Name string }

func (c *Col) node()            {}
func (c *Col) Children() []Node { return nil }
func (c *Col) String() string   { return "col(" + strconv.Quote(c.Name) + ")" }

func (c *Col) Field(in *dtype.Schema) (dtype.Field, error) {
	f, ok := in.ByName(c.Name)
	if !ok {
		return dtype.Field{}, uerr.UnknownColumn("", c.Name, in.Names())
	}
	return f, nil
}

// --- Lit ---------------------------------------------------------------------

// Lit is a scalar constant, broadcast to the length of the context.
//
// A typed null is Value == nil with DT set; an untyped null is Value == nil with
// DT == Null, which Promote lets adopt the other operand's type.
type Lit struct {
	Value any
	DT    dtype.DataType

	// Weak marks a literal whose type is a DEFAULT rather than a choice.
	//
	// ursus.Lit(0) infers Go int and fixes DT = Int64 at BUILD time. For arithmetic
	// that is right — Promote(Int32, Int64) is Int64 because addition can overflow.
	// For a null fill it is wrong, and worse, INVISIBLE: Col("i32").FillNull(FillZero)
	// contains no literal in the source at all, yet the same promotion would widen
	// the column to Int64 with nothing in the query to explain it.
	//
	// A weak literal asks the CONTEXT to adopt the other operand's type when the
	// value fits it exactly. Only ResolveCond consults it; Field still reports DT,
	// because a weak literal standing alone has no other operand to defer to.
	//
	// The zero value is STRONG, which is what keeps this change small: every
	// existing construction site keeps today's meaning untouched, and arithmetic
	// stays weak-free by construction because nothing on that path sets it.
	Weak bool
}

func (l *Lit) node()            {}
func (l *Lit) Children() []Node { return nil }

func (l *Lit) Field(*dtype.Schema) (dtype.Field, error) {
	return dtype.Field{Name: "literal", Type: l.DT, Nullable: l.Value == nil}, nil
}

// String renders the literal.
//
// # A weak literal MUST NOT render like a strong one
//
// Three maps key on a rendered String() — window temporaries, aggregate
// temporaries and partition-key sharing — so two expressions that render alike
// become ONE computation and both names get the first one's answer.
//
// Weak versus strong is a live instance of that: Coalesce(col_i32, Lit(0))
// resolves to Int64 and col_i32.FillNull(FillZero) resolves to Int32, and without
// the marker below they render identically. Put both under .Max() in one Agg and
// the aggregate dedup collapses them, after which the batch's column type and the
// plan's promised schema disagree.
//
// The TYPE is in there for a second collision. Weak literals normally converge on
// the column's type and so agree — but when the value does not fit they fall back
// to ordinary promotion, and a weak int64 5000 and a weak float64 5000 fall back
// to Int64 and Float64 respectively while printing the same digits.
func (l *Lit) String() string {
	if l.Value == nil {
		if l.DT.IsNull() {
			return "lit(null)"
		}
		return "lit(null:" + l.DT.String() + ")"
	}
	var v string
	switch x := l.Value.(type) {
	case string:
		v = strconv.Quote(x)
	default:
		v = fmt.Sprint(x)
	}
	if l.Weak {
		return "weak_lit(" + v + ":" + l.DT.String() + ")"
	}
	return "lit(" + v + ")"
}

// --- Binary ------------------------------------------------------------------

// Binary applies a two-operand operation.
//
// The resolved cast targets are NOT stored here: Binary is immutable and shared,
// and caching a resolution would tie a node to one input schema. The evaluator
// calls ResolveBinary itself, which is cheap and keeps exactly one copy of the
// promotion rules in the codebase.
type Binary struct {
	Op   BinaryOp
	L, R Node
}

func (b *Binary) node()            {}
func (b *Binary) Children() []Node { return []Node{b.L, b.R} }

func (b *Binary) String() string {
	return "(" + b.L.String() + " " + b.Op.String() + " " + b.R.String() + ")"
}

func (b *Binary) Field(in *dtype.Schema) (dtype.Field, error) {
	lf, err := b.L.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	rf, err := b.R.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}

	res, err := ResolveBinary(b.Op, lf.Type, rf.Type)
	if err != nil {
		return dtype.Field{}, err
	}

	// Nullability propagates conservatively: the result may be null if either
	// operand may be. The exception is a missing-comparison, whose whole point is
	// that it is total.
	nullable := lf.Nullable || rf.Nullable
	if b.Op.IsMissingComparison() {
		nullable = false
	}
	return dtype.Field{Name: OutputName(b), Type: res.Out, Nullable: nullable}, nil
}

// --- Unary -------------------------------------------------------------------

type Unary struct {
	Op    UnaryOp
	Child Node
}

func (u *Unary) node()            {}
func (u *Unary) Children() []Node { return []Node{u.Child} }
func (u *Unary) String() string   { return u.Child.String() + "." + u.Op.String() + "()" }

func (u *Unary) Field(in *dtype.Schema) (dtype.Field, error) {
	cf, err := u.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	out, err := ResolveUnary(u.Op, cf.Type)
	if err != nil {
		return dtype.Field{}, err
	}

	// A null predicate answers a question ABOUT nullness, so it is itself total.
	// A NaN predicate is not: null.is_nan() is null, because null is not a float
	// that could be tested. Conflating the two is the single most common way a
	// dataframe library gets null-vs-NaN wrong.
	nullable := cf.Nullable
	if u.Op.IsNullPredicate() {
		nullable = false
	}
	return dtype.Field{Name: OutputName(u), Type: out, Nullable: nullable}, nil
}

// --- Alias -------------------------------------------------------------------

// Alias renames the output column.
type Alias struct {
	Child Node
	Name  string
}

func (a *Alias) node()            {}
func (a *Alias) Children() []Node { return []Node{a.Child} }
func (a *Alias) String() string {
	return a.Child.String() + ".alias(" + strconv.Quote(a.Name) + ")"
}

func (a *Alias) Field(in *dtype.Schema) (dtype.Field, error) {
	f, err := a.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	return f.Rename(a.Name), nil
}

// --- Cast --------------------------------------------------------------------

// Cast converts to another type.
//
// Strict casts fail the query on the first unrepresentable value. Non-strict
// casts turn it into a null — which is why a non-strict cast is ALWAYS nullable
// regardless of its input, even when casting a non-nullable column.
type Cast struct {
	Child  Node
	To     dtype.DataType
	Strict bool
}

func (c *Cast) node()            {}
func (c *Cast) Children() []Node { return []Node{c.Child} }

func (c *Cast) String() string {
	if c.Strict {
		return c.Child.String() + ".cast(" + c.To.String() + ")"
	}
	return c.Child.String() + ".cast(" + c.To.String() + ", non-strict)"
}

func (c *Cast) Field(in *dtype.Schema) (dtype.Field, error) {
	cf, err := c.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	if !dtype.CanCast(cf.Type, c.To) {
		return dtype.Field{}, uerr.New(uerr.KindType, "cast",
			"cannot cast %s to %s", cf.Type, c.To).
			Hint("column %q has type %s", cf.Name, cf.Type)
	}
	return dtype.Field{
		Name:     OutputName(c),
		Type:     c.To,
		Nullable: cf.Nullable || !c.Strict,
	}, nil
}

// --- Err ---------------------------------------------------------------------

// Err carries a construction-time error to the point where an error can be
// returned. Expression builders cannot return errors without destroying method
// chaining, so `ColRegex("(")` produces an *Err instead of panicking.
type Err struct{ E error }

func (e *Err) node()            {}
func (e *Err) Children() []Node { return nil }
func (e *Err) String() string   { return "<error: " + e.E.Error() + ">" }

func (e *Err) Field(*dtype.Schema) (dtype.Field, error) { return dtype.Field{}, e.E }

// --- Match -------------------------------------------------------------------

// Match is a multi-column selection: Col("a","b"), ColRegex, ColDType, All,
// Exclude. It is a placeholder that Expand replaces with one concrete tree per
// matched column.
//
// It exists as a node rather than as a separate expression kind so that
// `Col(pl.Float64).Mul(1.1).Alias(...)` is an ordinary tree that expansion
// rewrites, rather than a special case threaded through every builder.
type Match struct{ M Matcher }

func (m *Match) node()            {}
func (m *Match) Children() []Node { return nil }
func (m *Match) String() string   { return m.M.String() }

func (m *Match) Field(*dtype.Schema) (dtype.Field, error) {
	return dtype.Field{}, uerr.Internalf("%v: %s", ErrUnexpanded, m.M.String())
}

// --- helpers -----------------------------------------------------------------

// StringAll renders a slice of nodes for Explain output.
func StringAll(ns []Node) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = n.String()
	}
	return strings.Join(parts, ", ")
}
