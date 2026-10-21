package plan

import (
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Pivot spreads a long table wide (step 303, P1 of v0.6.7-scope.md): Polars' pivot,
// lazy as Polars' LazyFrame.pivot is, its new columns given.
//
//	k  on  v                k  x  y
//	1  x   10      ->       1  10 20
//	1  y   20               2  30 null
//	2  x   30
//
// # Its input is an aggregate
//
// The root package builds Input as GroupBy(Index..., On...).MaintainOrder(): a row
// for each index and On values that meet, in the order each pair first appears, its
// cells reduced over the pair's rows. Pivot only lays those rows out, a column for
// each row of OnColumns, so the group-by does the work, and spills past a limit as
// any group-by does. Pivot holds the pairs.
//
// Polars builds its pivot as one aggregate by the index, a cell a new column, each
// a filter of the value by its On values. Built so here, that is a pass over the
// input for every new column: 27 times the time at a thousand columns.
//
// # OnColumns, a second child
//
// The new columns' On values are a frame, a column for each of On, typed as it, a
// row for each new column of one value: an in-memory scan the root package builds,
// as HStack's frames are. A child rather than a field, because this package holds
// no data (design/logical.md §13); the physical planner reads it.
//
// # A cell the index and its On values never meet
//
// Polars' cell is the aggregate of the values that fall in it, so one that none fall
// in is the aggregate of none: a sum's 0, a count's 0, a mean's null. With Empty,
// each value's cells are followed by its cell over no values, which the group-by
// computes as the aggregate of a filter that keeps nothing, and a cell never met is
// that. Without an aggregate, a count follows the cells instead: a cell never met
// is null, and one of two rows or more is refused, as Polars' item refuses it.
//
// Input's columns are therefore, in order: Index, On, a cell for each of Values,
// then either an empty cell for each of Values or the count.
type Pivot struct {
	Input     Node
	OnColumns Node
	Index     []string
	On        []string
	Values    []string

	// Names are the new columns' names, value by value, and within one value a row
	// of OnColumns at a time.
	Names []string

	// Empty says each value's empty cell follows the cells; otherwise the count
	// does.
	Empty bool
}

func (p *Pivot) planNode()        {}
func (p *Pivot) Children() []Node { return []Node{p.Input, p.OnColumns} }

func (p *Pivot) WithChildren(kids []Node) Node {
	if len(kids) != 2 {
		panic("plan: Pivot takes exactly two children")
	}
	c := *p
	c.Input, c.OnColumns = kids[0], kids[1]
	return &c
}

// Width is the number of new columns of one value: the rows of OnColumns.
func (p *Pivot) Width() int {
	if len(p.Values) == 0 {
		return 0
	}
	return len(p.Names) / len(p.Values)
}

// InputWidth is the number of columns Input holds.
func (p *Pivot) InputWidth() int {
	n := len(p.Index) + len(p.On) + len(p.Values)
	if p.Empty {
		return n + len(p.Values)
	}
	return n + 1
}

// Schema is the index columns, then a column for each value and row of OnColumns,
// of the value's cell type, every one nullable.
func (p *Pivot) Schema() (*dtype.Schema, error) {
	in, err := p.Input.Schema()
	if err != nil {
		return nil, err
	}
	on, err := p.OnColumns.Schema()
	if err != nil {
		return nil, err
	}
	if in.Len() != p.InputWidth() || len(p.Values) == 0 || len(p.Names) != len(p.Values)*p.Width() ||
		on.Len() != len(p.On) {
		return nil, uerr.Internalf("plan: pivot of %d columns, %d On columns of %d, %d values and %d names",
			in.Len(), on.Len(), len(p.On), len(p.Values), len(p.Names))
	}
	for i, name := range p.On {
		f, g := on.Field(i), in.Field(len(p.Index)+i)
		if f.Name != name || g.Name != name || f.Type != g.Type {
			return nil, uerr.Internalf("plan: pivot's On column %q is %s %s beside %s %s",
				name, f.Name, f.Type, g.Name, g.Type)
		}
	}
	fields := make([]dtype.Field, 0, len(p.Index)+len(p.Names))
	for i := range p.Index {
		fields = append(fields, in.Field(i))
	}
	k := p.Width()
	for j := range p.Values {
		t := in.Field(len(p.Index) + len(p.On) + j).Type
		for u := range k {
			fields = append(fields, dtype.Field{Name: p.Names[j*k+u], Type: t, Nullable: true})
		}
	}
	return dtype.NewSchema(fields...)
}

func (p *Pivot) Label() string {
	var b strings.Builder
	b.WriteString("PIVOT ")
	b.WriteString(strings.Join(p.On, ", "))
	if len(p.Index) > 0 {
		b.WriteString(" index ")
		b.WriteString(strings.Join(p.Index, ", "))
	}
	b.WriteString(" values ")
	b.WriteString(strings.Join(p.Values, ", "))
	// The names, so that two pivots into different columns render apart, but not
	// thousands of them.
	const shown = 8
	b.WriteString(" -> [")
	for i, name := range p.Names {
		if i == shown {
			b.WriteString(", +" + strconv.Itoa(len(p.Names)-shown))
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
	}
	b.WriteString("]")
	if !p.Empty {
		b.WriteString(" (one value a cell)")
	}
	return b.String()
}
