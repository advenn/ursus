package plan

import (
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// DefaultVariableName and DefaultValueName are the output column names when the
// caller does not choose. They are Polars' names, and the reason to match is that
// this is the one operation whose output columns are INVENTED rather than carried
// through — a reader of the query has nothing else to go on.
const (
	DefaultVariableName = "variable"
	DefaultValueName    = "value"
)

// Unpivot turns several value columns into two: which column, and what it held.
//
//	id  jan  feb            id  variable  value
//	1   10   20     ->      1   jan       10
//	2   30   40             1   feb       20
//	                        2   jan       30
//	                        2   feb       40
//
// # Row-major, which is NOT Polars' order
//
// Polars stacks k frames vertically, so it emits every row's `jan`, then every
// row's `feb`. This emits every variable for row 0, then row 1.
//
// The reason is batch-size invariance. This is a BatchOp — one batch in, one batch
// out — so a vertical stack would give batch 1's `jan`s, then batch 1's `feb`s,
// then batch 2's `jan`s: the row ORDER would depend on the batch size, which is a
// property of how the query ran rather than of what it asked for. Row-major is the
// order a streaming operator can produce, and it is the same order at every batch
// size.
//
// The alternative was to desugar into Union{Project, Project, ...}, one arm per
// value column, which gives Polars' order and gets projection pushdown for free.
// It is wrong here for a reason that has nothing to do with order: all k arms share
// one input subtree, the physical planner has no memoisation and there is no CTE
// node, so the input would be SCANNED K TIMES. Twelve month columns would read the
// file twelve times.
//
// # Index defaults to the rest, and Unnest's rule does not apply
//
// Unnest refuses to default its column list because "naming them keeps the frame's
// width a property of the query rather than of whatever the file happens to
// contain". The same argument would apply to defaulting On — an added source column
// would silently start being melted. It does not apply to Index: an added column is
// KEPT, which is what anyone would want. So On is required and Index is not.
type Unpivot struct {
	Input Node

	// On is the value columns, melted into the variable/value pair. Required.
	On []string

	// Index is the columns carried through unchanged. Empty means every column
	// that is not in On, computed against the input schema.
	Index []string

	// VariableName and ValueName are the two invented columns. "" means the
	// defaults above, so the zero value is the ordinary behaviour.
	VariableName string
	ValueName    string
}

func (u *Unpivot) planNode()        {}
func (u *Unpivot) Children() []Node { return []Node{u.Input} }

func (u *Unpivot) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Unpivot takes exactly one child")
	}
	c := *u
	c.Input = kids[0]
	return &c
}

func (u *Unpivot) variableName() string {
	if u.VariableName == "" {
		return DefaultVariableName
	}
	return u.VariableName
}

func (u *Unpivot) valueName() string {
	if u.ValueName == "" {
		return DefaultValueName
	}
	return u.ValueName
}

// IndexColumns is Index, or every column not in On when Index is empty.
//
// It is a method rather than something resolveUnpivot bakes into the node, because
// projection pushdown narrows the input through WithChildren and a baked list would
// go stale exactly then — the argument Join.Layout makes for not caching itself.
func (u *Unpivot) IndexColumns(in *dtype.Schema) []string {
	if len(u.Index) > 0 {
		return u.Index
	}
	on := make(map[string]struct{}, len(u.On))
	for _, name := range u.On {
		on[name] = struct{}{}
	}
	out := make([]string, 0, in.Len())
	for _, f := range in.All() {
		if _, melted := on[f.Name]; !melted {
			out = append(out, f.Name)
		}
	}
	return out
}

// Schema is the index columns, then the two invented ones.
//
// The value column's type is the PROMOTION of every On column's, which is the same
// question a vertical concat asks and answers — an Int32 beside an Int64 is an
// Int64, and two types with no common one is an error naming both. It is nullable
// if any On column is, because a row takes its value from one of them and the
// schema cannot know which.
func (u *Unpivot) Schema() (*dtype.Schema, error) {
	in, err := u.Input.Schema()
	if err != nil {
		return nil, err
	}
	if len(u.On) == 0 {
		return nil, uerr.New(uerr.KindValue, "unpivot",
			"unpivot needs at least one column to melt").
			Hint("On names the columns that become variable/value pairs")
	}

	fields := make([]dtype.Field, 0, len(u.Index)+2)
	seen := map[string]struct{}{}
	for _, name := range u.IndexColumns(in) {
		i := in.IndexOf(name)
		if i < 0 {
			return nil, unknownUnpivotColumn("index", name, in)
		}
		fields = append(fields, in.Field(i))
		seen[name] = struct{}{}
	}

	var valType dtype.DataType
	nullable := false
	for i, name := range u.On {
		j := in.IndexOf(name)
		if j < 0 {
			return nil, unknownUnpivotColumn("On", name, in)
		}
		if _, dup := seen[name]; dup {
			return nil, uerr.New(uerr.KindValue, "unpivot",
				"column %q is both melted and kept", name).
				Hint("a column cannot be in On and in Index at once")
		}
		f := in.Field(j)
		nullable = nullable || f.Nullable
		if i == 0 {
			valType = f.Type
			continue
		}
		t, ok := dtype.Promote(valType, f.Type)
		if !ok {
			return nil, uerr.New(uerr.KindType, "unpivot",
				"cannot melt %q with %q: no common type for %s and %s",
				u.On[0], name, valType, f.Type).
				Hint("every melted column shares one value column, so their types " +
					"must promote").
				Hint("cast one side explicitly, e.g. .Cast(ursus.Float64)")
		}
		valType = t
	}

	fields = append(fields,
		dtype.Field{Name: u.variableName(), Type: dtype.String},
		dtype.Field{Name: u.valueName(), Type: valType, Nullable: nullable})
	return dtype.NewSchema(fields...)
}

func unknownUnpivotColumn(role, name string, in *dtype.Schema) error {
	return uerr.New(uerr.KindSchema, "unpivot",
		"unknown %s column %q", role, name).
		Hint("available: %s", strings.Join(in.Names(), ", "))
}

func (u *Unpivot) Label() string {
	var b strings.Builder
	b.WriteString("UNPIVOT ")
	b.WriteString(strings.Join(u.On, ", "))
	if len(u.Index) > 0 {
		b.WriteString(" index ")
		b.WriteString(strings.Join(u.Index, ", "))
	}
	if u.VariableName != "" || u.ValueName != "" {
		b.WriteString(" into ")
		b.WriteString(u.variableName())
		b.WriteString("/")
		b.WriteString(u.valueName())
	}
	return b.String()
}
