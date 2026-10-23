package ndjson

import (
	"math/big"
	"slices"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Inference, as Polars infers an NDJSON file's schema from its first lines: a column a
// key, in the order the keys are first seen; each value's type, and a key's types met.

// typeOf is v's type: an integer Int64, or Int128 past it, as Polars reads one; a
// number with a fraction or an exponent Float64; text String; true and false Boolean;
// null Null; an array a List of its elements' types coerced; an object a Struct of
// its keys' types, in their order.
func typeOf(v value) (dtype.DataType, error) {
	switch v.kind {
	case kNull:
		return dtype.Null, nil
	case kBool:
		return dtype.Bool, nil
	case kInt:
		if fitsInt64(v.text) {
			return dtype.Int64, nil
		}
		return dtype.Int128, nil
	case kFloat:
		return dtype.Float64, nil
	case kString:
		return dtype.String, nil
	case kArray:
		var elems []dtype.DataType
		for _, e := range v.arr {
			t, err := typeOf(e)
			if err != nil {
				return dtype.Null, err
			}
			if !slices.Contains(elems, t) {
				elems = append(elems, t)
			}
		}
		return dtype.List(coerce(elems)), nil
	}
	var fields []dtype.Field
	seen := map[string]bool{}
	for i, k := range v.keys {
		if seen[k] {
			continue // a key's first value is the one read, as Polars reads it
		}
		seen[k] = true
		t, err := typeOf(v.vals[i])
		if err != nil {
			return dtype.Null, err
		}
		fields = append(fields, dtype.Field{Name: k, Type: t, Nullable: true})
	}
	return dtype.Struct(fields...), nil
}

// fitsInt64 reports whether an integer's text fits an Int64.
func fitsInt64(text string) bool {
	neg := text[0] == '-'
	digits := len(text)
	if neg {
		digits--
	}
	if digits < 19 {
		return true
	}
	n, ok := new(big.Int).SetString(text, 10)
	return ok && n.IsInt64()
}

// meet is the type a key's two types meet in, as Polars' inference meets them: a null
// takes the other; Int64 and Int128 Int128; an integer and a float Float64; text and
// anything String, as Polars writes the other's text; two Lists their elements'; two
// Structs their fields, the first's then the second's new ones. A Boolean and a
// number have no type in common, nor a List or a Struct and a number or a Boolean, as
// Polars refuses them.
func meet(a, b dtype.DataType) (dtype.DataType, error) {
	switch {
	case a == b:
		return a, nil
	case a.IsNull():
		return b, nil
	case b.IsNull():
		return a, nil
	case a.IsInteger() && b.IsInteger():
		return dtype.Int128, nil // Int64 and Int128, the two an integer is read as
	case a.IsNumeric() && b.IsNumeric():
		return dtype.Float64, nil
	case a == dtype.String || b == dtype.String:
		return dtype.String, nil
	case a.ID() == dtype.TypeList && b.ID() == dtype.TypeList:
		e, err := meet(a.Inner(), b.Inner())
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(e), nil
	case a.ID() == dtype.TypeStruct && b.ID() == dtype.TypeStruct:
		fields := append([]dtype.Field(nil), a.Fields()...)
		at := map[string]int{}
		for i, f := range fields {
			at[f.Name] = i
		}
		for _, f := range b.Fields() {
			i, ok := at[f.Name]
			if !ok {
				fields = append(fields, f)
				continue
			}
			t, err := meet(fields[i].Type, f.Type)
			if err != nil {
				return dtype.Null, err
			}
			fields[i].Type = t
		}
		return dtype.Struct(fields...), nil
	}
	return dtype.Null, uerr.New(uerr.KindSchema, "read_ndjson",
		"a key holds %s and %s, which have no type in common", a, b).
		Hint("give the schema with WithNDJSONSchema, a String reading either as its text")
}

// coerce is the type an array's elements meet in, ts their types, distinct and in the
// order first seen. Polars infers it by rules of its own, not meet's: a Boolean and an
// integer meet in Int64, a value and a list in a List, and two types no rule meets in
// String, of any value's text; more than two meet a pair at a time, in their order,
// so that [1, true, 2.5] is a List(Float64) and [2.5, true, 1] a List(String).
func coerce(ts []dtype.DataType) dtype.DataType {
	switch len(ts) {
	case 0:
		return dtype.Null
	case 1:
		return ts[0]
	}
	structs, lists := true, true
	for _, t := range ts {
		structs = structs && t.ID() == dtype.TypeStruct
		lists = lists && t.ID() == dtype.TypeList
	}
	switch {
	case structs:
		// Their fields, in the order first seen, each of its types coerced.
		var names []string
		types := map[string][]dtype.DataType{}
		for _, t := range ts {
			for _, f := range t.Fields() {
				if _, ok := types[f.Name]; !ok {
					names = append(names, f.Name)
				}
				if !slices.Contains(types[f.Name], f.Type) {
					types[f.Name] = append(types[f.Name], f.Type)
				}
			}
		}
		fields := make([]dtype.Field, len(names))
		for i, name := range names {
			fields[i] = dtype.Field{Name: name, Type: coerce(types[name]), Nullable: true}
		}
		return dtype.Struct(fields...)
	case lists:
		var inner []dtype.DataType
		for _, t := range ts {
			if !slices.Contains(inner, t.Inner()) {
				inner = append(inner, t.Inner())
			}
		}
		return dtype.List(coerce(inner))
	case len(ts) > 2:
		t := ts[0]
		for _, next := range ts[1:] {
			t = coerce(distinct(t, next))
		}
		return t
	}
	a, b := ts[0], ts[1]
	switch {
	case b.ID() == dtype.TypeList:
		return dtype.List(coerce(distinct(a, b.Inner())))
	case a.ID() == dtype.TypeList:
		return dtype.List(coerce(distinct(b, a.Inner())))
	case a == dtype.Float64 && b == dtype.Int64, a == dtype.Int64 && b == dtype.Float64:
		return dtype.Float64
	case a == dtype.Int64 && b == dtype.Bool, a == dtype.Bool && b == dtype.Int64:
		return dtype.Int64
	case a.IsNull():
		return b
	case b.IsNull():
		return a
	}
	return dtype.String
}

// distinct is a and b, or a alone when they are one type.
func distinct(a, b dtype.DataType) []dtype.DataType {
	if a == b {
		return []dtype.DataType{a}
	}
	return []dtype.DataType{a, b}
}

// inferSchema is the schema of lines, the first lines of a file, as Polars infers it:
// at is each line's number in the file, unit what a line is, "line " or a JSON
// document's "object ", and of the file's name for an error, "" for none.
func inferSchema(lines [][]byte, at []int, unit, of string) (*dtype.Schema, error) {
	where := func(n int) string {
		w := unit + strconv.Itoa(at[n])
		if of != "" {
			w += " of " + of
		}
		return w
	}
	var fields []dtype.Field
	field := map[string]int{}
	for n, line := range lines {
		v, err := parseObject(line)
		if err != nil {
			return nil, uerr.Annotate(err, "read_ndjson", where(n))
		}
		t, err := typeOf(v)
		if err != nil {
			return nil, uerr.Annotate(err, "read_ndjson", where(n))
		}
		for _, f := range t.Fields() {
			i, ok := field[f.Name]
			if !ok {
				field[f.Name] = len(fields)
				fields = append(fields, f)
				continue
			}
			if fields[i].Type, err = meet(fields[i].Type, f.Type); err != nil {
				return nil, uerr.Annotate(err, "read_ndjson", where(n)+", key "+f.Name)
			}
		}
	}
	return dtype.NewSchema(fields...)
}
