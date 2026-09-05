package plan

import (
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Explode turns each row's list into one row per element.
//
// It is the operation that makes a List column useful rather than merely
// readable: after it, every kernel, aggregate and join that already exists
// applies to the elements. Nothing else about lists has to be built first.
//
// # An empty list produces a row
//
//	[10, 11]   ->  two rows, 10 and 11
//	[]         ->  ONE row, null
//	null       ->  ONE row, null
//
// An empty list vanishing would be the other plausible rule, and it is wrong for
// two reasons. It would silently drop rows — the height only looks wrong if
// someone checks it — and it would make exploding two columns together
// incoherent, because the two would disagree about how many rows a pair of empty
// lists produces. Polars makes the same choice.
//
// The consequence is worth stating because it looks like a bug from either side:
// **explode ERASES the empty-versus-null distinction.** A List column keeps them
// apart carefully — they differ only in the validity bit, and data.NewList's doc
// is mostly about not confusing them — and after this node they are the same row.
// That is correct. The distinction is a property of a list, and once the list is
// gone there is nothing left to carry it.
//
// # Several columns explode together
//
// They are exploded as ONE operation rather than one after another, because
// exploding twice would give the cross product where the caller means the zip.
// Every named column must therefore have the same length in a given row; a
// mismatch is an error naming the row rather than a truncation.
type Explode struct {
	Input   Node
	Columns []string
}

func (e *Explode) planNode()        {}
func (e *Explode) Children() []Node { return []Node{e.Input} }

func (e *Explode) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Explode takes exactly one child")
	}
	c := *e
	c.Input = kids[0]
	return &c
}

// Schema retypes each exploded column to its element type.
//
// It is the first node to change BOTH the row count and the schema. The result is
// nullable whatever the element type was, because an empty list produces a null
// and nothing about the input's nullability says whether that can happen.
func (e *Explode) Schema() (*dtype.Schema, error) {
	in, err := e.Input.Schema()
	if err != nil {
		return nil, err
	}
	fields := make([]dtype.Field, in.Len())
	for i, f := range in.All() {
		fields[i] = f
	}

	for _, name := range e.Columns {
		i := in.IndexOf(name)
		if i < 0 {
			return nil, uerr.New(uerr.KindSchema, "explode",
				"unknown column %q", name).
				Hint("available: %s", strings.Join(in.Names(), ", "))
		}
		if fields[i].Type.ID() != dtype.TypeList {
			return nil, uerr.New(uerr.KindType, "explode",
				"column %q is %s, not a List", name, fields[i].Type).
				Hint("explode turns a list into one row per element")
		}
		fields[i] = dtype.Field{
			Name:     name,
			Type:     fields[i].Type.Inner(),
			Nullable: true,
		}
	}
	return dtype.NewSchema(fields...)
}

func (e *Explode) Label() string {
	return "EXPLODE " + strings.Join(e.Columns, ", ")
}
