package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Unnest is a Struct column's fields, a column each (step 260, E4 of
// v0.6.5-scope.md): Polars' struct.unnest(). Like a selection of several columns it
// is one expression that denotes several, and Expand writes it out: a copy of the
// expression for each field, the Unnest replaced by that field and the copy named
// after it. So Col("s").Unnest() is s's fields, in Select beside other columns and in
// WithColumns after the frame's, and Col("s").Unnest().Add(1) each field plus one.
type Unnest struct{ Child Node }

func (u *Unnest) node()            {}
func (u *Unnest) Children() []Node { return []Node{u.Child} }
func (u *Unnest) String() string   { return u.Child.String() + ".struct.unnest()" }

// Field refuses: Expand writes an Unnest out before anything types it, and one that
// reaches here stands where one column is wanted.
func (u *Unnest) Field(*dtype.Schema) (dtype.Field, error) {
	return dtype.Field{}, uerr.New(uerr.KindUnsupported, "unnest",
		"%s is a column a field, where one column is wanted", u).
		Hint("for one field, Col(\"s\").Struct().Field(\"name\")")
}

// unnests are the distinct Unnests in n, by their text.
func unnests(n Node) []*Unnest {
	var out []*Unnest
	seen := map[string]bool{}
	Walk(n, func(x Node) bool {
		if u, ok := x.(*Unnest); ok && !seen[u.String()] {
			seen[u.String()] = true
			out = append(out, u)
		}
		return true
	})
	return out
}

// expandUnnest writes n out over u's fields: n with u replaced by each field, named
// after it unless n names itself.
func expandUnnest(n Node, u *Unnest, in *dtype.Schema) ([]Node, error) {
	f, err := u.Child.Field(in)
	if err != nil {
		return nil, err
	}
	if f.Type.ID() != dtype.TypeStruct {
		return nil, uerr.New(uerr.KindType, "unnest",
			"unnest requires a Struct column, got %s", f.Type).
			Hint("a List's elements are rows of their own: Explode")
	}
	_, named := n.(*Alias)
	fields := f.Type.Fields()
	out := make([]Node, len(fields))
	seen := make(map[string]bool, len(fields))
	for i, fd := range fields {
		field := &Call{Fn: FnStructField, Args: []Node{u.Child, &Lit{Value: fd.Name, DT: dtype.String}}}
		x := replaceUnnest(n, u.String(), field)
		if !named {
			x = &Alias{Name: fd.Name, Child: x}
		}
		name := OutputName(x)
		if seen[name] {
			return nil, uerr.New(uerr.KindSchema, "unnest",
				"%s writes out %d columns that would all be named %q", n, len(fields), name).
				Hint("an Alias sets one name; leave it off, and each column is named after its field")
		}
		seen[name] = true
		out[i] = x
	}
	return out, nil
}

// replaceUnnest is n with each Unnest of the text key replaced by with.
func replaceUnnest(n Node, key string, with Node) Node {
	if u, ok := n.(*Unnest); ok && u.String() == key {
		return with
	}
	kids := n.Children()
	if len(kids) == 0 {
		return n
	}
	out := make([]Node, len(kids))
	for i, c := range kids {
		out[i] = replaceUnnest(c, key, with)
	}
	return Rebuild(n, out)
}
