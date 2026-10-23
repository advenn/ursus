package ursus

import (
	"context"
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// A frame made to a schema (step 299, C2 of v0.6.7-scope.md): Polars'
// match_to_schema. The answer is a Select of the schema's columns, in its order: each
// the frame's column as it is, or widened, or a struct's fields matched by name; a
// missing one inserted as nulls or computed; an extra one dropped. Whatever cannot
// be met is refused while the plan is built, as Polars refuses it, in Polars' order:
// the first type that does not meet, then every missing column, then every extra one.
//
// It is a Select of per-row expressions, so it streams.

// MatchAction is how MatchToSchema answers one kind of difference between a frame
// and a schema. Each field of MatchOptions takes two of them; the zero value is the
// first, Polars' default.
type MatchAction uint8

const (
	matchUnset MatchAction = iota
	// MatchRaise refuses the difference: a missing column or field, or an extra one.
	MatchRaise
	// MatchInsert adds a missing column or struct field, as nulls of its type.
	MatchInsert
	// MatchIgnore drops an extra column or struct field.
	MatchIgnore
	// MatchForbid refuses a column of a narrower type than the schema's.
	MatchForbid
	// MatchUpcast widens it, when the schema's type holds every value of the
	// column's: an Int32 to an Int64, a UInt32 to an Int64, a Float32 to a Float64.
	MatchUpcast
)

func (a MatchAction) String() string {
	switch a {
	case MatchRaise:
		return "MatchRaise"
	case MatchInsert:
		return "MatchInsert"
	case MatchIgnore:
		return "MatchIgnore"
	case MatchForbid:
		return "MatchForbid"
	case MatchUpcast:
		return "MatchUpcast"
	}
	return "MatchAction(" + strconv.Itoa(int(a)) + ")"
}

// MatchOptions says how MatchToSchema meets the schema, as Polars' match_to_schema's
// keyword arguments do. The zero value refuses every difference, as Polars' defaults
// do.
type MatchOptions struct {
	// MissingColumns is a schema column the frame lacks: MatchRaise, or MatchInsert
	// as nulls of its type. Polars' missing_columns.
	MissingColumns MatchAction
	// ExtraColumns is a frame column the schema lacks: MatchRaise, or MatchIgnore to
	// drop it. Polars' extra_columns.
	ExtraColumns MatchAction
	// MissingStructFields and ExtraStructFields are the same for a struct's fields,
	// at any depth: MatchRaise, or MatchInsert and MatchIgnore.
	MissingStructFields, ExtraStructFields MatchAction
	// IntegerCast and FloatCast are a column narrower than the schema's type:
	// MatchForbid, or MatchUpcast to widen it. An integer never becomes a float, as
	// in Polars, and a type that cannot hold every value, an Int64 as an Int32 or a
	// UInt64 as an Int64, is refused either way. A List's element and a struct's
	// field are widened as a column is.
	IntegerCast, FloatCast MatchAction
	// Columns sets any of these for one column of the schema, by its name; a zero
	// field keeps the setting above. Polars' per-column mappings.
	Columns map[string]ColumnMatch
}

// ColumnMatch is MatchOptions for one column.
type ColumnMatch struct {
	// Missing is MissingColumns for this column.
	Missing MatchAction
	// Fill computes the column when the frame lacks it, from the frame's other
	// columns: Polars' missing_columns={"c": expr}. Its answer meets the schema's
	// type as a column of the frame would, widened only as IntegerCast and FloatCast
	// allow; Polars keeps the expression's own type instead.
	Fill Expr
	// The rest are MatchOptions' for this column.
	MissingStructFields, ExtraStructFields, IntegerCast, FloatCast MatchAction
}

// MatchToSchema makes the frame's columns the schema's, in its order and of its
// types: Polars' match_to_schema.
//
//	lf.MatchToSchema(ursus.MustSchema(
//		ursus.Of("id", ursus.Int64),
//		ursus.Of("amount", ursus.Float64),
//	), ursus.MatchOptions{IntegerCast: ursus.MatchUpcast, ExtraColumns: ursus.MatchIgnore})
//
// A column of the frame's own type is read as it is, and a frame that already is the
// schema is returned unchanged. A Null column meets any type. Only types are met: a
// schema field's nullability is not checked.
func (lf *LazyFrame) MatchToSchema(schema *Schema, o MatchOptions) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if schema == nil {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "match_to_schema", "the schema is nil")}
	}
	have, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	m, err := newSchemaMatch(o, schema)
	if err != nil {
		return &LazyFrame{err: err}
	}
	exprs := make([]Expr, 0, schema.Len())
	var missing []string
	same := schema.Len() == have.Len()
	for i, f := range schema.All() {
		c := m.column(f.Name)
		at := have.IndexOf(f.Name)
		if at < 0 {
			same = false
			switch {
			case c.fill != (Expr{}):
				t, err := lf.Select(c.fill.Alias(f.Name)).schemaNow()
				if err != nil {
					return &LazyFrame{err: err}
				}
				e, err := m.meet(c.fill, t.Field(0).Type, f.Type, c, f.Name)
				if err != nil {
					return &LazyFrame{err: err}
				}
				exprs = append(exprs, e.Alias(f.Name))
			case c.missing == MatchInsert:
				exprs = append(exprs, Null(f.Type).Alias(f.Name))
			default:
				missing = append(missing, f.Name)
			}
			continue
		}
		from := have.Field(at).Type
		e, err := m.meet(Col(f.Name), from, f.Type, c, f.Name)
		if err != nil {
			return &LazyFrame{err: err}
		}
		if from != f.Type || at != i {
			same = false
		}
		exprs = append(exprs, e.Alias(f.Name))
	}
	if len(missing) > 0 {
		return &LazyFrame{err: uerr.New(uerr.KindSchema, "match_to_schema",
			"the frame has no column %s, which the schema has", quoteNames(missing)).
			Hint("MatchOptions{MissingColumns: ursus.MatchInsert} adds them as nulls")}
	}
	var extra []string
	for _, f := range have.All() {
		if schema.IndexOf(f.Name) < 0 {
			extra = append(extra, f.Name)
		}
	}
	if len(extra) > 0 && o.ExtraColumns != MatchIgnore {
		return &LazyFrame{err: uerr.New(uerr.KindSchema, "match_to_schema",
			"the schema has no column %s, which the frame has", quoteNames(extra)).
			Hint("MatchOptions{ExtraColumns: ursus.MatchIgnore} drops them")}
	}
	if same {
		return lf
	}
	return lf.Select(exprs...)
}

// MatchToSchema makes a DataFrame's columns the schema's: LazyFrame's.
func (df *DataFrame) MatchToSchema(ctx context.Context, schema *Schema, o MatchOptions) (*DataFrame, error) {
	return df.Lazy().MatchToSchema(schema, o).Collect(ctx)
}

// schemaMatch is MatchOptions checked, with each column's settings resolved.
type schemaMatch struct {
	all     columnSettings
	columns map[string]columnSettings
}

type columnSettings struct {
	missing, missingFields, extraFields, intCast, floatCast MatchAction
	fill                                                    Expr
}

func newSchemaMatch(o MatchOptions, schema *Schema) (*schemaMatch, error) {
	check := func(what string, a MatchAction, ok ...MatchAction) error {
		if a == matchUnset {
			return nil
		}
		for _, k := range ok {
			if a == k {
				return nil
			}
		}
		return uerr.New(uerr.KindValue, "match_to_schema", "%s is %s, which it cannot be", what, a).
			Hint("%s takes %s or %s", what, ok[0], ok[1])
	}
	for _, c := range []struct {
		what string
		a    MatchAction
		ok   []MatchAction
	}{
		{"MissingColumns", o.MissingColumns, []MatchAction{MatchRaise, MatchInsert}},
		{"ExtraColumns", o.ExtraColumns, []MatchAction{MatchRaise, MatchIgnore}},
		{"MissingStructFields", o.MissingStructFields, []MatchAction{MatchRaise, MatchInsert}},
		{"ExtraStructFields", o.ExtraStructFields, []MatchAction{MatchRaise, MatchIgnore}},
		{"IntegerCast", o.IntegerCast, []MatchAction{MatchForbid, MatchUpcast}},
		{"FloatCast", o.FloatCast, []MatchAction{MatchForbid, MatchUpcast}},
	} {
		if err := check(c.what, c.a, c.ok...); err != nil {
			return nil, err
		}
	}
	m := &schemaMatch{
		all: columnSettings{missing: o.MissingColumns, missingFields: o.MissingStructFields,
			extraFields: o.ExtraStructFields, intCast: o.IntegerCast, floatCast: o.FloatCast},
		columns: make(map[string]columnSettings, len(o.Columns)),
	}
	for name, c := range o.Columns {
		if schema.IndexOf(name) < 0 {
			return nil, uerr.UnknownColumn("match_to_schema", name, schema.Names()).
				Hint("MatchOptions.Columns names the schema's columns")
		}
		for _, k := range []struct {
			what string
			a    MatchAction
			ok   []MatchAction
		}{
			{"Missing", c.Missing, []MatchAction{MatchRaise, MatchInsert}},
			{"MissingStructFields", c.MissingStructFields, []MatchAction{MatchRaise, MatchInsert}},
			{"ExtraStructFields", c.ExtraStructFields, []MatchAction{MatchRaise, MatchIgnore}},
			{"IntegerCast", c.IntegerCast, []MatchAction{MatchForbid, MatchUpcast}},
			{"FloatCast", c.FloatCast, []MatchAction{MatchForbid, MatchUpcast}},
		} {
			if err := check("column "+strconv.Quote(name)+"'s "+k.what, k.a, k.ok...); err != nil {
				return nil, err
			}
		}
		if c.Fill != (Expr{}) && c.Missing == MatchRaise {
			return nil, uerr.New(uerr.KindValue, "match_to_schema",
				"column %q has a Fill and Missing is MatchRaise", name).
				Hint("a Fill computes the column when it is missing; leave Missing unset")
		}
		s := m.all
		for _, f := range []struct {
			dst *MatchAction
			v   MatchAction
		}{
			{&s.missing, c.Missing}, {&s.missingFields, c.MissingStructFields},
			{&s.extraFields, c.ExtraStructFields}, {&s.intCast, c.IntegerCast}, {&s.floatCast, c.FloatCast},
		} {
			if f.v != matchUnset {
				*f.dst = f.v
			}
		}
		s.fill = c.Fill
		m.columns[name] = s
	}
	return m, nil
}

func (m *schemaMatch) column(name string) columnSettings {
	if c, ok := m.columns[name]; ok {
		return c
	}
	return m.all
}

// meet makes e, of type from, the schema's type to, or refuses: equal is e itself,
// and an integer or a float is widened, a List's element as a column, and a struct's
// fields matched by name. at names e in an error: a column, or a column's field.
func (m *schemaMatch) meet(e Expr, from, to DataType, c columnSettings, at string) (Expr, error) {
	switch {
	case from == to:
		return e, nil
	case from.IsNull():
		return e.Cast(to), nil
	case from.ID() == dtype.TypeStruct && to.ID() == dtype.TypeStruct:
		return m.meetStruct(e, from, to, c, at)
	case from.ID() == dtype.TypeList && to.ID() == dtype.TypeList,
		from.ID() == dtype.TypeArray && to.ID() == dtype.TypeArray && from.Size() == to.Size():
		// A List's element widens as a column does, and an Array's of one size.
		// A List of structs whose fields differ would need its elements rebuilt,
		// which a cast does not do.
		if from.Inner().ID() == dtype.TypeStruct || to.Inner().ID() == dtype.TypeStruct {
			return Expr{}, mismatch(at, from, to).
				Hint("a List of Structs whose fields differ is not matched yet")
		}
		if _, err := m.meet(e, from.Inner(), to.Inner(), c, at); err != nil {
			return Expr{}, err
		}
		return e.Cast(to), nil
	case from.IsInteger() && to.IsInteger():
		if !holdsEvery(from, to) {
			return Expr{}, mismatch(at, from, to).Hint("%s cannot hold every %s value", to, from)
		}
		if c.intCast != MatchUpcast {
			return Expr{}, mismatch(at, from, to).
				Hint("MatchOptions{IntegerCast: ursus.MatchUpcast} widens it")
		}
		return e.Cast(to), nil
	case from.IsFloat() && to.IsFloat():
		if from != dtype.Float32 || to != dtype.Float64 {
			return Expr{}, mismatch(at, from, to).Hint("%s cannot hold every %s value", to, from)
		}
		if c.floatCast != MatchUpcast {
			return Expr{}, mismatch(at, from, to).
				Hint("MatchOptions{FloatCast: ursus.MatchUpcast} widens it")
		}
		return e.Cast(to), nil
	}
	return Expr{}, mismatch(at, from, to)
}

// meetStruct matches a struct's fields by name: each of the schema's fields read,
// widened, inserted or refused, and then the schema's fields alone, in its order,
// through with_fields and select_fields, which keep a null struct null.
func (m *schemaMatch) meetStruct(e Expr, from, to DataType, c columnSettings, at string) (Expr, error) {
	have := make(map[string]DataType, len(from.Fields()))
	for _, f := range from.Fields() {
		have[f.Name] = f.Type
	}
	var with []Expr
	order := make([]string, 0, len(to.Fields()))
	for _, f := range to.Fields() {
		order = append(order, f.Name)
		t, ok := have[f.Name]
		switch {
		case !ok && c.missingFields == MatchInsert:
			with = append(with, Null(f.Type).Alias(f.Name))
		case !ok:
			return Expr{}, mismatch(at, from, to).
				Hint("its struct has no field %q, which the schema's has", f.Name).
				Hint("MatchOptions{MissingStructFields: ursus.MatchInsert} adds it as nulls")
		case t != f.Type:
			fe, err := m.meet(e.Struct().Field(f.Name), t, f.Type, c, at+"."+f.Name)
			if err != nil {
				return Expr{}, err
			}
			with = append(with, fe.Alias(f.Name))
		}
	}
	wanted := make(map[string]bool, len(order))
	for _, n := range order {
		wanted[n] = true
	}
	for _, f := range from.Fields() {
		if wanted[f.Name] {
			continue
		}
		if c.extraFields != MatchIgnore {
			return Expr{}, mismatch(at, from, to).
				Hint("its struct has a field %q, which the schema's has not", f.Name).
				Hint("MatchOptions{ExtraStructFields: ursus.MatchIgnore} drops it")
		}
	}
	out := e
	if len(with) > 0 {
		out = out.Struct().WithFields(with...)
	}
	// The fields with_fields leaves, in the schema's order and no others.
	args := make([]expr.Node, 0, len(order)+1)
	args = append(args, out.node())
	for _, n := range order {
		args = append(args, litNode(n))
	}
	return wrap(&expr.Call{Fn: expr.FnStructSelectFields, Args: args}), nil
}

func mismatch(at string, from, to DataType) *uerr.Error {
	return uerr.New(uerr.KindSchema, "match_to_schema", "%s is %s, and the schema's is %s", at, from, to)
}

// holdsEvery reports whether integer type to holds every value of integer type from:
// a signed type one as wide or wider, an unsigned one an unsigned as wide or wider,
// and a signed one an unsigned strictly narrower. A signed value is never unsigned.
func holdsEvery(from, to DataType) bool {
	fb, tb := from.BitWidth(), to.BitWidth()
	switch {
	case from.IsSignedInteger() && to.IsSignedInteger():
		return tb >= fb
	case from.IsUnsignedInteger() && to.IsUnsignedInteger():
		return tb >= fb
	case from.IsUnsignedInteger() && to.IsSignedInteger():
		return tb > fb
	}
	return false
}

func quoteNames(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}
