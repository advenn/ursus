package expr

import (
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// listAppendOut is list.append's type: a List of its two lists' elements met by its
// mode, a literal.
func listAppendOut(c *Call, ops []dtype.Field) (dtype.DataType, error) {
	if len(ops) != 3 {
		return dtype.Null, uerr.Internalf("expr: %s of %d operands", c.Fn, len(ops))
	}
	mode, ok := c.Args[2].(*Lit)
	m, isInt := mode.Value.(int64)
	if !ok || !isInt {
		return dtype.Null, uerr.Internalf("expr: %s without its mode", c.Fn)
	}
	a, b := ops[0].Type, ops[1].Type
	if a.ID() != dtype.TypeList || b.ID() != dtype.TypeList && !b.IsNull() {
		return dtype.Null, uerr.Internalf("expr: %s of %s and %s", c.Fn, a, b)
	}
	inner := dtype.Null
	if !b.IsNull() {
		inner = b.Inner()
	}
	t, err := AppendType(m, a.Inner(), inner)
	if err != nil {
		return dtype.Null, err
	}
	return dtype.List(t), nil
}

// structCallOut gives the output type of a `.struct` call.
//
// `field(name)` answers with the FIELD's type, which is why ResolveCall takes the
// whole Call: the answer is in the argument, not the receiver. Resolving it here
// rather than at execution time is what lets an unknown field name be refused while
// the query is still being planned, with the names that do exist listed.
func structCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	if in.ID() != dtype.TypeStruct {
		return dtype.Null, uerr.New(uerr.KindType, "struct",
			"%s requires a Struct operand, got %s", c.Fn, in).
			Hint("only a Struct column has fields")
	}
	switch c.Fn {
	case FnStructField:
		name, ok := callLitString(c, 1)
		if !ok {
			return dtype.Null, uerr.Internalf("expr: struct.field has no name argument")
		}
		for _, f := range in.Fields() {
			if f.Name == name {
				return f.Type, nil
			}
		}
		names := make([]string, 0, len(in.Fields()))
		for _, f := range in.Fields() {
			names = append(names, f.Name)
		}
		return dtype.Null, uerr.New(uerr.KindSchema, "struct",
			"no field %q in %s", name, in).
			Hint("the fields are: %s", strings.Join(names, ", "))

	case FnStructRenameFields:
		names, err := callFieldNames(c, "struct")
		if err != nil {
			return dtype.Null, err
		}
		old := in.Fields()
		if len(names) != len(old) {
			// Polars renames as many as it is given and drops the rest, and warns that
			// 2.0 will refuse it; this refuses it now.
			return dtype.Null, uerr.New(uerr.KindSchema, "struct",
				"rename_fields was given %d names for %d fields", len(names), len(old)).
				Hint("name every field, in order; select a field with Field to keep fewer")
		}
		fields := make([]dtype.Field, len(old))
		for i, f := range old {
			fields[i] = dtype.Field{Name: names[i], Type: f.Type, Nullable: true}
		}
		return dtype.Struct(fields...), nil

	case FnStructSelectFields:
		// MatchToSchema's: a struct of the fields named, in their order, the others
		// dropped. A null struct stays null, as a cast keeps it. No method of Polars'
		// answers so; with_fields keeps every field, and field gives columns.
		names, err := callFieldNames(c, "struct")
		if err != nil {
			return dtype.Null, err
		}
		if len(names) == 0 {
			// A struct of no fields has no length of its own to keep.
			return dtype.Null, uerr.New(uerr.KindSchema, "struct", "%s names no field", c.Fn)
		}
		have := make(map[string]dtype.DataType, len(in.Fields()))
		for _, f := range in.Fields() {
			have[f.Name] = f.Type
		}
		fields := make([]dtype.Field, len(names))
		for i, name := range names {
			t, ok := have[name]
			if !ok {
				return dtype.Null, uerr.New(uerr.KindSchema, "struct", "no field %q in %s", name, in)
			}
			fields[i] = dtype.Field{Name: name, Type: t, Nullable: true}
		}
		return dtype.Struct(fields...), nil

	case FnStructDropFields:
		// Polars' struct.drop: the fields not named, in their order; strict, the
		// argument after the struct, refuses a name that is no field. A struct of no
		// fields has no length of its own, so dropping every one is refused, where
		// Polars makes one.
		if len(c.Args) < 2 {
			return dtype.Null, uerr.Internalf("expr: struct.drop has no strict argument")
		}
		var strict bool
		lit, ok := c.Args[1].(*Lit)
		if ok {
			strict, ok = lit.Value.(bool)
		}
		if !ok {
			return dtype.Null, uerr.Internalf("expr: struct.drop has no strict argument")
		}
		have := make(map[string]bool, len(in.Fields()))
		for _, f := range in.Fields() {
			have[f.Name] = true
		}
		drop := make(map[string]bool, len(c.Args)-2)
		for i := 2; i < len(c.Args); i++ {
			name, ok := callLitString(c, i)
			if !ok {
				return dtype.Null, uerr.Internalf("expr: struct.drop argument %d is not a name", i)
			}
			if strict && !have[name] {
				return dtype.Null, uerr.New(uerr.KindSchema, "struct", "no field %q in %s", name, in).
					Hint("drop with strict false to pass over a name that is not there")
			}
			drop[name] = true
		}
		var fields []dtype.Field
		for _, f := range in.Fields() {
			if !drop[f.Name] {
				fields = append(fields, dtype.Field{Name: f.Name, Type: f.Type, Nullable: true})
			}
		}
		if len(fields) == 0 {
			return dtype.Null, uerr.New(uerr.KindSchema, "struct",
				"struct.drop drops every field of %s, and a struct of no fields has no length", in).
				Hint("drop the column itself")
		}
		return dtype.Struct(fields...), nil

	case FnStructJSONEncode:
		if bad, ok := jsonUnencodable(in); ok {
			return dtype.Null, uerr.New(uerr.KindType, "struct",
				"%s cannot write a %s, which JSON has no value for", c.Fn, bad).
				Hint("cast it to String first, or drop the field")
		}
		return dtype.String, nil
	}
	return dtype.Null, uerr.Internalf("expr: unknown struct call %d", c.Fn)
}

// jsonUnencodable finds a type within t that json_encode cannot write: a Binary,
// whose bytes JSON has no value for. Polars panics on one.
func jsonUnencodable(t dtype.DataType) (dtype.DataType, bool) {
	switch t.ID() {
	case dtype.TypeBinary:
		return t, true
	case dtype.TypeList:
		return jsonUnencodable(t.Inner())
	case dtype.TypeStruct:
		for _, f := range t.Fields() {
			if bad, ok := jsonUnencodable(f.Type); ok {
				return bad, true
			}
		}
	}
	return dtype.Null, false
}

// arrCallOut gives the output type of an `.arr` call (step 341): an Array is a List
// each of whose rows has its size, so its functions are a List's, read as one.
func arrCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	if in.ID() != dtype.TypeArray {
		return dtype.Null, uerr.New(uerr.KindType, "arr",
			"%s requires an Array operand, got %s", c.Fn, in).
			Hint("a List becomes an Array of a width by List().ToArray(width)")
	}
	switch c.Fn {
	case FnArrToList:
		return dtype.List(in.Inner()), nil
	case FnArrReverse, FnArrSort:
		// A List's reverse and sort, which keep each row's length, so its Array.
		if _, err := listCallOut(&Call{Fn: map[CallFn]CallFn{FnArrReverse: FnListReverse,
			FnArrSort: FnListSort}[c.Fn], Args: c.Args}, dtype.List(in.Inner())); err != nil {
			return dtype.Null, err
		}
		return in, nil
	case FnArrToStruct:
		names, err := arrFieldNames(c, in)
		if err != nil {
			return dtype.Null, err
		}
		fs := make([]dtype.Field, len(names))
		for i, n := range names {
			fs[i] = dtype.Field{Name: n, Type: in.Inner(), Nullable: true}
		}
		return dtype.Struct(fs...), nil
	}
	return dtype.Null, uerr.Internalf("expr: no type for %s", c.Fn)
}

// ArrFieldNames is arr.to_struct's fields' names over an Array of in: those given, as
// many as its size, or field_0 on, as Polars names them.
func ArrFieldNames(c *Call, in dtype.DataType) ([]string, error) { return arrFieldNames(c, in) }

func arrFieldNames(c *Call, in dtype.DataType) ([]string, error) {
	if len(c.Args) == 1 {
		names := make([]string, in.Size())
		for i := range names {
			names[i] = "field_" + strconv.Itoa(i)
		}
		return names, nil
	}
	names, err := callFieldNames(c, "arr.to_struct")
	if err != nil {
		return nil, err
	}
	if len(names) != in.Size() {
		return nil, uerr.New(uerr.KindValue, "arr.to_struct",
			"%s names %d fields of an Array of %d", c.Fn, len(names), in.Size()).
			Hint("a field an element: name each, or none for field_0 on")
	}
	return names, nil
}

// listCallOut gives the output type of a `.list` call.
//
// The namespace has two halves and the output type is what tells them apart. One
// reduces a list to a SCALAR, giving a fixed type or the ELEMENT type; the other
// reshapes it and gives back the RECEIVER'S type, unchanged. A caller can read
// which half a function is in from this switch alone, which is the point of
// keeping the rule in one place.
func listCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	if in.ID() != dtype.TypeList {
		return dtype.Null, uerr.New(uerr.KindType, "list",
			"%s requires a List operand, got %s", fn, in).
			Hint("only a List column has elements to reduce")
	}
	elem := in.Inner()

	switch fn {
	case FnListLen:
		// Uint32, matching str.len_bytes and str.len_chars. A length is never
		// negative and never needs 64 bits.
		return dtype.Uint32, nil

	case FnListContains:
		if _, err := MembershipType(c, elem); err != nil {
			return dtype.Null, err
		}
		return dtype.Bool, nil

	case FnListJoin:
		if elem.ID() != dtype.TypeString && elem.ID() != dtype.TypeNull {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List of String, got %s", fn, in).
				Hint("cast the elements first, e.g. .Cast(ursus.List(ursus.String))")
		}
		if _, ok := callLitString(c, 1); !ok {
			return dtype.Null, uerr.Internalf("expr: %s without a separator", fn)
		}
		return dtype.String, nil

	case FnListGet, FnListMin, FnListMax:
		// The element type unchanged: picking one element out, or the smallest or
		// largest of them, cannot change what type it is.
		return elem, nil

	case FnListMean:
		// Delegated for the reason FnListSum gives below, and it was not: this arm
		// answered Float64 for every element type without reading elem at all.
		// listReduce has always derived the real type from ResolveAggBinding, so the
		// two disagreed wherever the aggregate's own rule is not Float64 — a
		// Duration mean is a Duration since step 62, and a Float32 mean has been a
		// Float32 for as long as the function has existed. Both are the dangerous
		// direction: Eval SUCCEEDS and hands back a column of the wrong type.
		//
		// Int64 is why it survived. ResolveAggBinding(AggMean, Int64) is Float64,
		// so every integer element agreed, and an integer element is what every
		// fixture had.
		b, err := ResolveAggBinding(AggMean, elem)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case FnListAny, FnListAll:
		if elem.ID() != dtype.TypeBool && !elem.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "list", "%s takes a List of Booleans, got %s", fn, in)
		}
		return dtype.Bool, nil

	case FnListItem:
		// allow_empty, which the kernel reads and cannot run without.
		if args, err := CallArgs(c); err != nil || len(args) != 1 {
			return dtype.Null, uerr.Internalf("expr: %s without allow_empty", fn)
		} else if _, ok := args[0].(bool); !ok {
			return dtype.Null, uerr.Internalf("expr: %s with allow_empty %v", fn, args[0])
		}
		return elem, nil

	case FnListSample:
		return in, nil

	case FnListReshape:
		// Each list's values in Arrays of the shape, as a list (step 343).
		args, err := CallArgs(c)
		if err != nil {
			return dtype.Null, err
		}
		shape := make([]int64, 0, len(args))
		for _, a := range args {
			d, ok := a.(int64)
			if !ok {
				return dtype.Null, uerr.Internalf("expr: %s with a shape of %v", fn, args)
			}
			shape = append(shape, d)
		}
		t, err := ReshapeType(elem, shape)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(t), nil

	case FnListToArray:
		// An Array of the width, which every row is checked to have, as Polars
		// checks it (step 341).
		w, ok := callLitInt64(c, 1)
		if !ok || w < 0 {
			return dtype.Null, uerr.New(uerr.KindValue, "list",
				"%s takes a constant width of at least 0", fn).
				Hint("an Array's width is the elements in each of its rows")
		}
		return dtype.Array(elem, int(w)), nil

	case FnListHist:
		args, err := CallArgs(c)
		if err != nil || len(args) < 3 {
			return dtype.Null, uerr.Internalf("expr: %s without its arguments", fn)
		}
		category, _ := args[1].(bool)
		breakpoint, _ := args[2].(bool)
		t, err := HistType(elem, category, breakpoint)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(t), nil

	case FnListMode, FnListUniqueCounts, FnListValueCounts, FnListRle:
		// A Struct or a List cannot be a key here, so its mode is refused, where
		// Polars counts them.
		if !elem.IsHashable() && !elem.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "mode", "%s counts values, and %s cannot be a key", fn, elem)
		}
		switch fn {
		case FnListRle:
			return dtype.List(RleType(elem)), nil
		case FnListUniqueCounts:
			return dtype.List(dtype.Uint32), nil
		case FnListValueCounts:
			args, err := CallArgs(c)
			if err != nil || len(args) != 4 {
				return dtype.Null, uerr.Internalf("expr: %s without its arguments", fn)
			}
			label, _ := args[2].(string)
			value, _ := args[3].(string)
			normalize, _ := args[1].(bool)
			return dtype.List(ValueCountsType(dtype.Field{Name: value, Type: elem}, label, normalize)), nil
		}
		return in, nil

	case FnListArgSort, FnListArgTrue, FnListArgUnique:
		switch {
		case fn == FnListArgSort && !elem.IsOrdered() && !elem.IsNull():
			return dtype.Null, uerr.New(uerr.KindType, "arg_sort", "arg_sort orders values, and %s has no order", elem)
		case fn == FnListArgTrue && elem.ID() != dtype.TypeBool && !elem.IsNull():
			return dtype.Null, uerr.New(uerr.KindType, "arg_true", "arg_true takes Booleans, got %s", elem)
		case fn == FnListArgUnique && !elem.IsHashable() && !elem.IsNull():
			return dtype.Null, uerr.New(uerr.KindType, "arg_unique", "arg_unique compares values, and %s cannot be", elem)
		}
		return dtype.List(dtype.Uint32), nil

	case FnListExplode:
		if elem.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "explode",
				"explode requires a List column, got %s", elem).
				Hint("a column that is not a List has nothing to explode")
		}
		return elem, nil

	case FnListDropNans:
		if !elem.IsFloat() {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List of floats, got %s", fn, in).
				Hint("only a float is ever NaN")
		}
		return in, nil

	case FnListMedian, FnListStd, FnListVar, FnListQuantile:
		// The aggregate's rule, as for Sum and Mean.
		op := map[CallFn]AggOp{FnListMedian: AggMedian, FnListStd: AggStd, FnListVar: AggVar,
			FnListQuantile: AggQuantile}[fn]
		b, err := ResolveAggBinding(op, elem)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case FnListReverse, FnListHead, FnListTail, FnListSlice,
		FnListSort, FnListUnique, FnListDropNulls, FnListGatherEvery:
		// The receiver's type, unchanged. Reshaping a list cannot change what it is
		// a list OF — and returning `in` rather than rebuilding List(elem) means a
		// future parameterised list type carries through without this needing to
		// know about it.
		return in, nil

	case FnListNUnique, FnListArgMin, FnListArgMax:
		// The aggregate's rule, as for Sum below: a count is a Uint64, a position
		// too, and arg_min refuses an element type with no ordering, naming itself.
		op := AggNUnique
		switch fn {
		case FnListArgMin:
			op = AggArgMin
		case FnListArgMax:
			op = AggArgMax
		}
		b, err := ResolveAggBinding(op, elem)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case FnListToStruct:
		names, err := callFieldNames(c, "list")
		if err != nil {
			return dtype.Null, err
		}
		if len(names) == 0 {
			return dtype.Null, uerr.New(uerr.KindSchema, "list",
				"to_struct needs at least one field name").
				Hint("name the fields the first elements become, e.g. ToStruct(\"x\", \"y\")")
		}
		fields := make([]dtype.Field, len(names))
		for i, name := range names {
			fields[i] = dtype.Field{Name: name, Type: elem, Nullable: true}
		}
		return dtype.Struct(fields...), nil

	case FnListSum:
		// Delegated to the AGGREGATE's rule rather than restated, so a per-row sum
		// and a column-wide sum agree about their type. That rule widens an integer
		// to Int128, which step 2 chose deliberately: a per-row sum that overflowed
		// where the column-wide one does not would be the worse surprise.
		b, err := ResolveAggBinding(AggSum, elem)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	default:
		return dtype.Null, uerr.Internalf("expr: unknown list call %d", fn)
	}
}

// withFieldsOut types struct.with_fields (step 171): the first operand's fields, each
// replaced in place by an operand of its name, then the operands of new names, in
// order. Two operands of one name would set one field twice, so they are refused.
func withFieldsOut(ops []dtype.Field) (dtype.DataType, error) {
	in := ops[0].Type
	if in.ID() != dtype.TypeStruct {
		return dtype.Null, uerr.New(uerr.KindType, "struct.with_fields",
			"with_fields requires a Struct, got %s", in).
			Hint("only a Struct column has fields")
	}
	set := make(map[string]dtype.DataType, len(ops)-1)
	var added []dtype.Field
	for _, f := range ops[1:] {
		if _, dup := set[f.Name]; dup {
			return dtype.Null, uerr.New(uerr.KindSchema, "struct.with_fields",
				"two new fields are named %q", f.Name).
				Hint("alias one of them, e.g. .Alias(\"%s_2\")", f.Name)
		}
		set[f.Name] = f.Type
	}
	fields := make([]dtype.Field, 0, len(in.Fields())+len(ops)-1)
	for _, f := range in.Fields() {
		if t, ok := set[f.Name]; ok {
			f.Type, f.Nullable = t, true
			delete(set, f.Name)
		}
		fields = append(fields, f)
	}
	for _, f := range ops[1:] {
		if _, ok := set[f.Name]; ok {
			added = append(added, dtype.Field{Name: f.Name, Type: f.Type, Nullable: true})
		}
	}
	return dtype.Struct(append(fields, added...)...), nil
}
