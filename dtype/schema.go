package dtype

import (
	"iter"
	"strings"

	"github.com/advenn/ursus/internal/uerr"
)

// Schema is an ordered, name-unique sequence of Fields.
//
// A *Schema is IMMUTABLE once constructed and is shared freely between plan
// nodes, batches and goroutines. Nothing in ursus mutates one. Accessors that
// hand back a slice therefore return a fresh copy — see Names and FieldSlice.
//
// Column order is meaningful (it is the output column order) but there is no row
// index: ursus follows Polars in treating row order as a property of the data
// rather than an addressable label space.
type Schema struct {
	fields []Field
	index  map[string]int
}

// NewSchema builds a schema, rejecting duplicate column names.
//
// Duplicates are an error rather than a silent last-wins or auto-suffix because
// every downstream lookup is by name: tolerating them would make Col("x")
// ambiguous and the ambiguity would surface far from its cause.
func NewSchema(fields ...Field) (*Schema, error) {
	idx := make(map[string]int, len(fields))
	for i, f := range fields {
		if prev, dup := idx[f.Name]; dup {
			return nil, uerr.DuplicateColumn("schema", f.Name, prev, i)
		}
		idx[f.Name] = i
	}
	fs := make([]Field, len(fields))
	copy(fs, fields)
	return &Schema{fields: fs, index: idx}, nil
}

// MustSchema is NewSchema for tests and package-level vars, panicking on duplicates.
func MustSchema(fields ...Field) *Schema {
	s, err := NewSchema(fields...)
	if err != nil {
		panic(err)
	}
	return s
}

// EmptySchema is the schema with no columns.
var EmptySchema = &Schema{index: map[string]int{}}

// Len returns the number of columns.
func (s *Schema) Len() int {
	if s == nil {
		return 0
	}
	return len(s.fields)
}

// Field returns the i'th field. It panics if i is out of range, matching slice
// indexing: an out-of-range column ordinal is always an ursus bug, never user error.
func (s *Schema) Field(i int) Field { return s.fields[i] }

// ByName returns the named field.
func (s *Schema) ByName(name string) (Field, bool) {
	if s == nil {
		return Field{}, false
	}
	i, ok := s.index[name]
	if !ok {
		return Field{}, false
	}
	return s.fields[i], true
}

// IndexOf returns the position of name, or -1.
func (s *Schema) IndexOf(name string) int {
	if s == nil {
		return -1
	}
	if i, ok := s.index[name]; ok {
		return i
	}
	return -1
}

// Has reports whether the schema contains name.
func (s *Schema) Has(name string) bool { return s.IndexOf(name) >= 0 }

// Names returns the column names in order, as a FRESH slice the caller may mutate.
// Projection pushdown sorts and filters the result in place.
func (s *Schema) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.fields))
	for i, f := range s.fields {
		out[i] = f.Name
	}
	return out
}

// FieldSlice returns the fields in order, as a FRESH slice the caller may mutate.
func (s *Schema) FieldSlice() []Field {
	if s == nil {
		return nil
	}
	out := make([]Field, len(s.fields))
	copy(out, s.fields)
	return out
}

// All iterates the schema in column order.
func (s *Schema) All() iter.Seq2[int, Field] {
	return func(yield func(int, Field) bool) {
		if s == nil {
			return
		}
		for i, f := range s.fields {
			if !yield(i, f) {
				return
			}
		}
	}
}

// Select returns a schema containing only the named columns, in the order given.
// Unknown names produce an UnknownColumn error listing what is available.
func (s *Schema) Select(names []string) (*Schema, error) {
	out := make([]Field, 0, len(names))
	for _, n := range names {
		f, ok := s.ByName(n)
		if !ok {
			return nil, uerr.UnknownColumn("select", n, s.Names())
		}
		out = append(out, f)
	}
	return NewSchema(out...)
}

// Drop returns a schema without the named columns. Unknown names are ignored,
// because Drop is used to express "remove these if present" during rewrites.
func (s *Schema) Drop(names ...string) *Schema {
	rm := make(map[string]struct{}, len(names))
	for _, n := range names {
		rm[n] = struct{}{}
	}
	out := make([]Field, 0, s.Len())
	for _, f := range s.fields {
		if _, drop := rm[f.Name]; !drop {
			out = append(out, f)
		}
	}
	return MustSchema(out...) // cannot collide: a subset of an already-unique set
}

// Equal reports whether both schemas have the same fields in the same order.
func (s *Schema) Equal(o *Schema) bool {
	if s.Len() != o.Len() {
		return false
	}
	for i := range s.fields {
		if s.fields[i] != o.fields[i] {
			return false
		}
	}
	return true
}

// String renders "{a: Int64, b: String}". Stable: golden Explain files and every
// schema mismatch message depend on it.
func (s *Schema) String() string {
	if s.Len() == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, f := range s.fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(f.String())
	}
	b.WriteByte('}')
	return b.String()
}
