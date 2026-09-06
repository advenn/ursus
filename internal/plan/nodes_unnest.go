package plan

import (
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Unnest replaces a struct column with its fields.
//
// It is to Struct what Explode is to List: the operation after which the nested
// column is gone and every kernel, aggregate, join and sort that already exists
// applies to what was inside it. Nothing else has to be built first.
//
//	id | person                  ->   id | age  | city
//	 1 | {age: 30, city: "NY"}         1 | 30   | "NY"
//	 2 | {age: null, city: null}       2 | null | null
//	 3 | null                          3 | null | null
//
// # It is simpler than Explode in three ways
//
// The row count does not change, so there is no gather. There is no zip, so
// unnesting several columns needs no agreement between them. And what it changes is
// only the schema — the fields already have the right length, because a struct's
// fields are parallel to it rather than indexed by it.
//
// # Rows 2 and 3 come out the same, and that is the erasure
//
// A present struct holding nulls and an absent struct produce identical output, in
// the same way explode collapses an empty list into a null one. The distinction
// belongs to the struct, and unnest is what consumes the struct. Before it,
// `IsNull()` tells them apart; after it, nothing can.
type Unnest struct {
	Input   Node
	Columns []string
}

func (u *Unnest) planNode()        {}
func (u *Unnest) Children() []Node { return []Node{u.Input} }

func (u *Unnest) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Unnest takes exactly one child")
	}
	c := *u
	c.Input = kids[0]
	return &c
}

// Schema splices each struct's fields into the place the struct occupied.
//
// IN PLACE rather than appended: a caller who unnests a column in the middle of a
// frame did not ask for the rest of it to be reordered.
//
// The fields keep their OWN names, which is what makes the result ordinary — but it
// is also what lets them collide with a column that is already there, so the
// collision is refused here. dtype.NewSchema would catch it a moment later, and its
// message would tell the caller to alias one of the two columns, which cannot be done
// to a field that does not exist yet.
func (u *Unnest) Schema() (*dtype.Schema, error) {
	in, err := u.Input.Schema()
	if err != nil {
		return nil, err
	}

	want := make(map[string]bool, len(u.Columns))
	for _, name := range u.Columns {
		i := in.IndexOf(name)
		if i < 0 {
			return nil, uerr.New(uerr.KindSchema, "unnest",
				"unknown column %q", name).
				Hint("available: %s", strings.Join(in.Names(), ", "))
		}
		if in.Field(i).Type.ID() != dtype.TypeStruct {
			return nil, uerr.New(uerr.KindType, "unnest",
				"column %q is %s, not a Struct", name, in.Field(i).Type).
				Hint("unnest replaces a struct with its fields; use Explode for a list")
		}
		if want[name] {
			return nil, uerr.New(uerr.KindValue, "unnest",
				"column %q named twice", name).
				Hint("unnesting it once already removes it")
		}
		want[name] = true
	}

	// Collisions are detected against every name the OUTPUT will hold, not against
	// the ones emitted so far: a field can just as easily collide with a column that
	// comes after the struct as with one before it, and only one of those two is
	// visible to a running check.
	from := make(map[string]string, in.Len())
	claim := func(name, owner string) error {
		if prev, dup := from[name]; dup {
			return uerr.New(uerr.KindSchema, "unnest",
				"two columns would be called %q: %s, and %s", name, prev, owner).
				Hint("rename the existing column before unnesting, e.g. " +
					"WithColumns(Col(\"x\").Alias(\"y\"))")
		}
		from[name] = owner
		return nil
	}
	for _, f := range in.All() {
		if !want[f.Name] {
			if err := claim(f.Name, "the column already in the frame"); err != nil {
				return nil, err
			}
			continue
		}
		for _, sub := range f.Type.Fields() {
			if err := claim(sub.Name, "a field of "+strconv.Quote(f.Name)); err != nil {
				return nil, err
			}
		}
	}

	fields := make([]dtype.Field, 0, len(from))
	for _, f := range in.All() {
		if !want[f.Name] {
			fields = append(fields, f)
			continue
		}
		for _, sub := range f.Type.Fields() {
			// Nullable whatever the field said: a row whose struct is absent has no
			// field values, so any field of a nullable struct can come out null.
			fields = append(fields, dtype.Field{
				Name:     sub.Name,
				Type:     sub.Type,
				Nullable: true,
			})
		}
	}
	return dtype.NewSchema(fields...)
}

func (u *Unnest) Label() string {
	return "UNNEST " + strings.Join(u.Columns, ", ")
}
