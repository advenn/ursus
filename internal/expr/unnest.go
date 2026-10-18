package expr

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Unnest is a Struct column's fields, a column each (step 260, E4 of
// v0.6.5-scope.md): Polars' struct.unnest(). Like a selection of several columns it
// is one expression that denotes several, and Expand writes it out: a copy of the
// expression for each field, the Unnest replaced by that field and the copy named
// after it. So Col("s").Unnest() is s's fields, in Select beside other columns and in
// WithColumns after the frame's, and Col("s").Unnest().Add(1) each field plus one.
//
// Fields selects the fields written out, Polars' struct.field of several names (step
// 275): each a field's name, a regex between ^ and $, or "*" for every field. None
// is every field, unnest's.
type Unnest struct {
	Child  Node
	Fields []string
}

func (u *Unnest) node()            {}
func (u *Unnest) Children() []Node { return []Node{u.Child} }
func (u *Unnest) String() string {
	if len(u.Fields) == 0 {
		return u.Child.String() + ".struct.unnest()"
	}
	quoted := make([]string, len(u.Fields))
	for i, f := range u.Fields {
		quoted[i] = strconv.Quote(f)
	}
	return u.Child.String() + ".struct.field(" + strings.Join(quoted, ", ") + ")"
}

// IsFieldPattern reports whether a name of struct.field is a regex, between ^ and $,
// or "*", every field, as Polars reads it.
func IsFieldPattern(name string) bool {
	return name == "*" || len(name) >= 2 && name[0] == '^' && name[len(name)-1] == '$'
}

// selectFields are the fields u selects, in the struct's order, each once, as
// Polars gives them. A name the struct does not have is refused; a regex may match
// none.
func (u *Unnest) selectFields(fields []dtype.Field) ([]dtype.Field, error) {
	if len(u.Fields) == 0 {
		return fields, nil
	}
	keep := make([]bool, len(fields))
	for _, s := range u.Fields {
		switch {
		case s == "*":
			for i := range keep {
				keep[i] = true
			}
		case IsFieldPattern(s):
			re, err := regexp.Compile(s)
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindValue, "struct.field", "%q is not a regex", s)
			}
			for i, f := range fields {
				keep[i] = keep[i] || re.MatchString(f.Name)
			}
		default:
			at := slices.IndexFunc(fields, func(f dtype.Field) bool { return f.Name == s })
			if at < 0 {
				names := make([]string, len(fields))
				for i, f := range fields {
					names[i] = f.Name
				}
				return nil, uerr.New(uerr.KindSchema, "struct.field", "the struct has no field %q", s).
					Hint("its fields are %s", strings.Join(names, ", "))
			}
			keep[at] = true
		}
	}
	var out []dtype.Field
	for i, f := range fields {
		if keep[i] {
			out = append(out, f)
		}
	}
	return out, nil
}

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
	fields, err := u.selectFields(f.Type.Fields())
	if err != nil {
		return nil, err
	}
	out := make([]Node, len(fields))
	seen := make(map[string]bool, len(fields))
	for i, fd := range fields {
		field := &Call{Fn: FnStructField, Args: []Node{u.Child, &Lit{Value: fd.Name, DT: dtype.String}}}
		x := replaceUnnest(n, u.String(), field)
		if !named {
			x = nameUnder(x, fd.Name)
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

// nameUnder names x after a field, below the renames at its top: an alias above a
// Suffix would undo it, so Unnest().Suffix("_x") is each field's name and _x (step
// 275).
func nameUnder(x Node, name string) Node {
	if r, ok := x.(*Rename); ok {
		c := *r
		c.Child = nameUnder(r.Child, name)
		return &c
	}
	return &Alias{Name: name, Child: x}
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
