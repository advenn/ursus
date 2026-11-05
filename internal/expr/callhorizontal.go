package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// CutBreaks is how many of a cut's operands after the first are breaks: those before
// the first label, which is a String.
func CutBreaks(ops []dtype.Field) int {
	k := 0
	for _, f := range ops[1:] {
		if f.Type.ID() == dtype.TypeString {
			break
		}
		k++
	}
	return k
}

// ResolveHorizontal gives the output type of a horizontal call over operands of
// these fields. Like ResolveCall, it is the one authority, for the plan's schema and
// the kernel's output alike.
func ResolveHorizontal(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	if len(ops) == 0 {
		return dtype.Null, uerr.Internalf("expr: %s has no operands", fn)
	}
	switch fn {
	case FnHashRows:
		// Every operand but the seed is a value a group-by keys on, as hash's is.
		if ops[len(ops)-1].Type != dtype.Int64 {
			return dtype.Null, uerr.Internalf("expr: hash_rows has no seed")
		}
		if len(ops) < 2 {
			return dtype.Null, uerr.New(uerr.KindSchema, "hash_rows", "hash_rows has no column to hash").
				Hint("the frame has no columns, or the selection matched none")
		}
		for _, f := range ops[:len(ops)-1] {
			if !f.Type.IsHashable() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "hash_rows",
					"hash_rows reads values a group-by can key on, and %q is %s", f.Name, f.Type).
					Hint("a key's leaves are numbers, text, Booleans or instants; drop the column, or hash its fields")
			}
		}
		return dtype.Uint64, nil

	case FnConcatStr:
		// Any operand that formats as text is formatted, as Polars' concat_str
		// formats it: the kernel casts it to String, the cast every type here has.
		for i, f := range ops {
			if !dtype.CanCast(f.Type, dtype.String) {
				return dtype.Null, uerr.New(uerr.KindType, "concat_str",
					"concat_str cannot format operand %d (%s), a %s, as text", i+1, f.Name, f.Type).
					Hint("take a field or an element of it first, which can be formatted")
			}
		}
		return dtype.String, nil

	case FnStructOf:
		// A field is named after its operand, so two operands of one name would be
		// two fields Field("name") could not tell apart.
		fields := make([]dtype.Field, len(ops))
		seen := make(map[string]bool, len(ops))
		for i, f := range ops {
			if seen[f.Name] {
				return dtype.Null, uerr.New(uerr.KindSchema, "struct",
					"two fields of the struct are named %q", f.Name).
					Hint("alias one of them, e.g. .Alias(\"%s_2\")", f.Name)
			}
			seen[f.Name] = true
			fields[i] = dtype.Field{Name: f.Name, Type: f.Type, Nullable: true}
		}
		return dtype.Struct(fields...), nil

	case FnStructWithFields:
		return withFieldsOut(ops)

	case FnCut, FnCutLeftClosed:
		if !ops[0].Type.IsNumeric() {
			return dtype.Null, uerr.New(uerr.KindType, "cut",
				"%s requires a numeric operand, got %s", fn, ops[0].Type).
				Hint("cast it first, e.g. .Cast(ursus.Float64)")
		}
		k := CutBreaks(ops)
		for _, f := range ops[1 : 1+k] {
			if !f.Type.IsNumeric() && !f.Type.IsNull() {
				return dtype.Null, uerr.Internalf("expr: %s break of type %s", fn, f.Type)
			}
		}
		if labels := len(ops) - 1 - k; labels != 0 && labels != k+1 {
			return dtype.Null, uerr.Internalf("expr: %s with %d breaks and %d labels", fn, k, labels)
		}
		return dtype.String, nil

	case FnListSetUnion, FnListSetIntersection, FnListSetDifference,
		FnListSetSymmetricDifference:
		return listSetOut(fn, ops)

	case FnMaxHorizontal, FnMinHorizontal, FnSumHorizontal, FnMeanHorizontal:
		in, err := HorizontalInput(fn, ops)
		if err != nil {
			return dtype.Null, err
		}
		b, err := ResolveAggBinding(HorizontalAgg(fn), in)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case FnAnyHorizontal, FnAllHorizontal:
		for i, f := range ops {
			if !f.Type.IsBool() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, fn.String(),
					"%s requires Boolean operands, and operand %d (%s) is a %s", fn, i+1, f.Name, f.Type).
					Hint("compare it first, e.g. Col(%q).Gt(0)", f.Name)
			}
		}
		return dtype.Bool, nil

	case FnDtCombine:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if t := ops[1].Type; t.ID() != dtype.TypeTime && !t.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s takes a Time of day, got %s (%s)", fn, t, ops[1].Name).
				Hint("cast it first, e.g. .Cast(ursus.Time(ursus.Nano))")
		}
		// The receiver's own unit, a Date's microseconds, and its zone: Polars
		// answers microseconds unless asked, and a Cast gives another unit.
		switch in := ops[0].Type; in.ID() {
		case dtype.TypeDate:
			return dtype.Datetime(dtype.Micro, ""), nil
		case dtype.TypeDatetime:
			return in, nil
		default:
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, in).
				Hint("a Time has no day to combine with")
		}

	case FnListSortBy, FnListSortByDesc:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if ops[0].Type.ID() != dtype.TypeList || ops[1].Type.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires two Lists, got %s and %s", fn, ops[0].Type, ops[1].Type).
				Hint("each row's keys are a list of the row's length")
		}
		if !ops[1].Type.Inner().IsOrdered() {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s sorts by %s, which has no order", fn, ops[1].Type.Inner())
		}
		return ops[0].Type, nil

	case FnStrContainsExpr, FnStrStartsWithExpr, FnStrEndsWithExpr, FnStrStripPrefixExpr,
		FnStrStripSuffixExpr, FnStrSplitExpr, FnStrReplaceExpr, FnStrReplaceAllExpr:
		return strExprOut(fn, ops)

	case FnIntRanges:
		if len(ops) != 3 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		for _, f := range ops {
			if !f.Type.IsInteger() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "int_range",
					"int_range takes integers, and %s is a %s", f.Name, f.Type).
					Hint("cast it first, e.g. .Cast(ursus.Int64)")
			}
		}
		return dtype.List(dtype.Int64), nil

	case FnLinearSpaces:
		if len(ops) != 4 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if n := ops[2].Type; !n.IsInteger() && !n.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "linear_space",
				"linear_space takes its number of samples as an integer, got %s", n)
		}
		elem, err := linearElem(ops[0].Type, ops[1].Type)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(elem), nil

	case FnMakeDate, FnMakeDatetime, FnMakeTime:
		if want := map[CallFn]int{FnMakeDate: 3, FnMakeDatetime: 7, FnMakeTime: 4}[fn]; len(ops) != want {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		for _, f := range ops {
			if !f.Type.IsInteger() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, fn.String(),
					"%s takes integer components, and %s is a %s", fn, f.Name, f.Type).
					Hint("cast it first, e.g. .Cast(ursus.Int64)")
			}
		}
		switch fn {
		case FnMakeDate:
			return dtype.Date, nil
		case FnMakeDatetime:
			return dtype.Datetime(dtype.Micro, ""), nil
		}
		return dtype.Time(dtype.Nano), nil

	case FnDurationTicks, FnEpochTicks:
		if want := map[CallFn]int{FnDurationTicks: 9, FnEpochTicks: 2}[fn]; len(ops) != want {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		for _, f := range ops {
			if !f.Type.IsInteger() && !f.Type.IsFloat() && !f.Type.IsNull() {
				op := map[CallFn]string{FnDurationTicks: "duration", FnEpochTicks: "from_epoch"}[fn]
				return dtype.Null, uerr.New(uerr.KindType, op,
					"%s takes numbers, and %s is a %s", op, f.Name, f.Type)
			}
		}
		return dtype.Int64, nil

	case FnStrParseInt, FnStrParseUint:
		if len(ops) != 3 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if s := ops[0].Type; s.ID() != dtype.TypeString && !s.IsNull() && s.ID() != dtype.TypeEnum {
			return dtype.Null, uerr.New(uerr.KindType, "str", "%s requires a String operand, got %s", fn, s)
		}
		if b := ops[1].Type; !b.IsInteger() && !b.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "str.to_integer", "to_integer takes an integer base, got %s", b)
		}
		if ops[2].Type != dtype.Bool {
			return dtype.Null, uerr.Internalf("expr: %s with strict a %s", fn, ops[2].Type)
		}
		if fn == FnStrParseUint {
			return dtype.Uint64, nil
		}
		return dtype.Int64, nil

	case FnConcatStrIgnoreNulls:
		if len(ops) < 2 || !ops[len(ops)-1].Type.HasStringStorage() {
			return dtype.Null, uerr.Internalf("expr: %s without its separator last", fn)
		}
		for i, f := range ops[:len(ops)-1] {
			if !dtype.CanCast(f.Type, dtype.String) {
				return dtype.Null, uerr.New(uerr.KindType, "concat_str",
					"concat_str cannot format operand %d (%s), a %s, as text", i+1, f.Name, f.Type)
			}
		}
		return dtype.String, nil

	case FnArrShift:
		// list.shift of an Array, which keeps each row's length, so its Array.
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if ops[0].Type.ID() != dtype.TypeArray {
			return dtype.Null, uerr.New(uerr.KindType, "arr", "%s requires an Array operand, got %s", fn, ops[0].Type)
		}
		if v := ops[1].Type; !v.IsInteger() && !v.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "arr.shift", "arr.shift takes an integer count, got %s", v)
		}
		return ops[0].Type, nil

	case FnConcatArr:
		return ConcatArrType(ops)

	case FnArrDot:
		// The sum of each row's products, as list.sum sums them (step 342).
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		return ArrDotType(ops[0].Type, ops[1].Type)

	case FnListShift, FnListCountMatches:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		l, v := ops[0].Type, ops[1].Type
		if l.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list", "%s requires a List operand, got %s", fn, l)
		}
		if fn == FnListShift {
			if !v.IsInteger() && !v.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "list.shift", "list.shift takes an integer count, got %s", v)
			}
			return l, nil
		}
		if !v.IsNull() {
			if _, err := ResolveBinary(OpEq, l.Inner(), v); err != nil {
				return dtype.Null, uerr.New(uerr.KindType, "list.count_matches",
					"list.count_matches cannot compare a List of %s with a %s value", l.Inner(), v)
			}
		}
		return dtype.Uint32, nil

	case FnConcatList:
		elems := make([]dtype.Field, len(ops))
		for i, f := range ops {
			elems[i] = f
			if f.Type.ID() == dtype.TypeList {
				elems[i].Type = f.Type.Inner()
			}
		}
		elem, err := HorizontalInput(fn, elems)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(elem), nil

	case FnDtIsBusinessDay, FnDtAddBusinessDays:
		want := map[CallFn]int{FnDtIsBusinessDay: 3, FnDtAddBusinessDays: 5}[fn]
		if len(ops) != want {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		v := ops[0].Type
		if v.ID() != dtype.TypeDate && v.ID() != dtype.TypeDatetime && !v.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, v)
		}
		if fn == FnDtIsBusinessDay {
			return dtype.Bool, nil
		}
		if n := ops[1].Type; !n.IsInteger() && !n.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"add_business_days takes an integer count, got %s", n)
		}
		if v.IsNull() {
			return dtype.Date, nil
		}
		return v, nil

	case FnBinContains, FnBinStartsWith, FnBinEndsWith, FnBinGet, FnBinSlice:
		return binHorizontalOut(fn, ops)

	case FnListAppend:
		return dtype.Null, uerr.Internalf("expr: %s is typed by its call, whose mode is a literal", fn)

	case FnOrderCmp:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if _, err := OrderCmpType(ops[0].Type, ops[1].Type); err != nil {
			return dtype.Null, err
		}
		return dtype.Int8, nil

	case FnDtReplace:
		if len(ops) != 10 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		v := ops[0].Type
		if v.ID() != dtype.TypeDate && v.ID() != dtype.TypeDatetime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, v).
				Hint("a Time is replaced by building it again with MakeTime")
		}
		for i, f := range ops[1:8] {
			if !f.Type.IsInteger() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "dt",
					"replace's %s is an integer, got %s", DateTimeParts[i], f.Type)
			}
		}
		return v, nil

	case FnBusinessDayCount:
		if len(ops) != 4 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		for _, f := range ops[:2] {
			if f.Type.ID() != dtype.TypeDate && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "business_day_count",
					"business_day_count counts between Dates, and %s is a %s", f.Name, f.Type).
					Hint("take the date first, e.g. .Dt().Date()")
			}
		}
		return dtype.Int32, nil

	case FnDateRanges, FnDatetimeRanges, FnTimeRanges:
		if len(ops) != 6 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		elem, err := rangeElem(fn, ops[0].Type, ops[1].Type)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(elem), nil

	case FnRepeatBy:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if n := ops[1].Type; !n.IsInteger() && !n.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "repeat_by",
				"repeat_by takes its count as an integer, got %s", n)
		}
		return dtype.List(ops[0].Type), nil

	case FnDtOffsetByExpr:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		in := ops[0].Type
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			e := uerr.New(uerr.KindType, "dt", "%s requires a Date or Datetime operand, got %s", fn, in)
			if in.ID() == dtype.TypeDuration || in.ID() == dtype.TypeTime {
				e.Hint("add a Duration with Add instead")
			}
			return dtype.Null, e
		}
		if by := ops[1].Type; !by.HasStringStorage() && !by.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s takes its offsets as Strings such as \"1mo2d\", got %s", fn, by).
				Hint("a Duration column is added with Add")
		}
		return in, nil

	case FnIsInExpr:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		x, l := ops[0].Type, ops[1].Type
		if l.ID() != dtype.TypeList {
			e := uerr.New(uerr.KindType, "is_in",
				"is_in takes a List of each row's values, and %s is a %s", ops[1].Name, l)
			if l == x {
				// Polars 1.44 deprecates this spelling as ambiguous, and asks for implode.
				e.Hint("for membership among the whole column's values, Implode it: IsIn(Col(%q).Implode())",
					ops[1].Name)
			}
			return dtype.Null, e
		}
		if x.IsNull() {
			return dtype.Bool, nil
		}
		if !x.IsHashable() {
			return dtype.Null, uerr.New(uerr.KindType, "is_in", "is_in is not defined for %s", x).
				Hint("the value must be hashable: numeric, temporal, string or boolean")
		}
		if _, err := ResolveBinary(OpEq, x, l.Inner()); err != nil {
			return dtype.Null, uerr.New(uerr.KindType, "is_in",
				"is_in cannot compare %s with a List of %s", x, l.Inner()).
				Hint("it compares as == does, and == has no common type for these")
		}
		return dtype.Bool, nil

	case FnListContainsExpr:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		l, v := ops[0].Type, ops[1].Type
		if l.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List operand, got %s", fn, l).
				Hint("only a List column has elements to look among")
		}
		if !v.IsNull() {
			if _, err := ResolveBinary(OpEq, l.Inner(), v); err != nil {
				return dtype.Null, uerr.New(uerr.KindType, "list.contains",
					"list.contains cannot compare a List of %s with a %s value", l.Inner(), v).
					Hint("it compares as == does, and == has no common type for these")
			}
		}
		return dtype.Bool, nil

	case FnListGetExpr:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if ops[0].Type.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List operand, got %s", fn, ops[0].Type).
				Hint("only a List column has elements to get")
		}
		if i := ops[1].Type; !i.IsInteger() && !i.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "list.get",
				"list.get takes an integer index, got %s", i).
				Hint("cast it first, e.g. .Cast(ursus.Int64)")
		}
		return ops[0].Type.Inner(), nil

	case FnListGather, FnListGatherOrNull:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if ops[0].Type.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List operand, got %s", fn, ops[0].Type).
				Hint("only a List column has elements to gather")
		}
		if ops[1].Type.ID() != dtype.TypeList || !ops[1].Type.Inner().IsInteger() {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s takes its indices as a List of integers, got %s", fn, ops[1].Type).
				Hint("each row's indices are a list; Implode a column of them, or cast")
		}
		return ops[0].Type, nil
	}
	return dtype.Null, uerr.Internalf("expr: unknown horizontal call %d", fn)
}

// rangeElem is a temporal range's element type from ends of types s and e (step
// 264), as Polars types it: a Date for date_range, from Dates or Datetimes; for
// datetime_range the Datetime of the ends, a Date's taken as Datetime(us); a Time
// for time_range.
func rangeElem(fn CallFn, s, e dtype.DataType) (dtype.DataType, error) {
	switch {
	case s.IsNull():
		s = e
	case e.IsNull():
		e = s
	}
	date := func(t dtype.DataType) bool { return t.ID() == dtype.TypeDate }
	stamp := func(t dtype.DataType) bool { return t.ID() == dtype.TypeDatetime }
	switch fn {
	case FnDateRanges:
		if (date(s) || stamp(s)) && (date(e) || stamp(e)) {
			return dtype.Date, nil
		}
	case FnDatetimeRanges:
		switch {
		case date(s) && date(e):
			return dtype.Datetime(dtype.Micro, ""), nil
		case stamp(s) && (s == e || date(e)):
			return s, nil
		case date(s) && stamp(e):
			return e, nil
		}
	case FnTimeRanges:
		if s.ID() == dtype.TypeTime && s == e {
			return s, nil
		}
	}
	return dtype.Null, uerr.New(uerr.KindType, Generators[fn],
		"%s takes two ends of its type, got %s and %s", Generators[fn], s, e).
		Hint("cast one end to the other's type")
}

// linearElem is the element type of linear_space from ends of types s and e, as
// Polars types it: Float32 for two Float32 ends, Float64 for other numbers, and a
// temporal type for two ends of it, a Date's spaced as Datetime(us).
func linearElem(s, e dtype.DataType) (dtype.DataType, error) {
	switch {
	case s.IsNull() && e.IsNull():
		return dtype.Float64, nil
	case s.IsNull():
		s = e
	case e.IsNull():
		e = s
	}
	switch {
	case s.IsNumeric() && e.IsNumeric():
		if s == dtype.Float32 && e == dtype.Float32 {
			return dtype.Float32, nil
		}
		return dtype.Float64, nil
	case s.IsTemporal() && s == e:
		if s.ID() == dtype.TypeDate {
			return dtype.Datetime(dtype.Micro, ""), nil
		}
		return s, nil
	}
	return dtype.Null, uerr.New(uerr.KindType, "linear_space",
		"linear_space spaces two numbers or two values of one temporal type, got %s and %s", s, e).
		Hint("cast one end to the other's type")
}

// strConstant is each per-row string function's constant form, which types it.
var strConstant = map[CallFn]CallFn{
	FnStrContainsExpr: FnStrContains, FnStrStartsWithExpr: FnStrStartsWith,
	FnStrEndsWithExpr: FnStrEndsWith, FnStrStripPrefixExpr: FnStrStripPrefix,
	FnStrStripSuffixExpr: FnStrStripSuffix, FnStrSplitExpr: FnStrSplit,
	FnStrReplaceExpr: FnStrReplace, FnStrReplaceAllExpr: FnStrReplaceAll,
}

// strExprOut types a string function of a per-row pattern or value (step 255): the
// receiver, pattern and value Strings, the flags Bools, and the answer the constant
// form's.
func strExprOut(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	flag := -1 // the literal flag's operand
	want := 2
	switch fn {
	case FnStrContainsExpr:
		flag, want = 2, 3
	case FnStrReplaceExpr, FnStrReplaceAllExpr:
		flag, want = 3, 4
	}
	if len(ops) != want {
		return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
	}
	for i, f := range ops {
		if i == flag {
			if f.Type.ID() != dtype.TypeBool {
				return dtype.Null, uerr.Internalf("expr: %s's flag is a %s", fn, f.Type)
			}
			continue
		}
		if f.Type.ID() != dtype.TypeString && !f.Type.IsNull() && f.Type.ID() != dtype.TypeEnum {
			what := "pattern"
			switch {
			case i == 0:
				what = "operand"
			case want == 4 && i == 2:
				what = "value"
			}
			return dtype.Null, uerr.New(uerr.KindType, "str",
				"%s requires a String %s, got %s", fn, what, f.Type).
				Hint("cast it first, e.g. .Cast(ursus.String)")
		}
	}
	return strCallOut(strConstant[fn]), nil
}

// HorizontalAgg is the aggregate a horizontal reduction runs over each row's values.
func HorizontalAgg(fn CallFn) AggOp {
	switch fn {
	case FnMinHorizontal:
		return AggMin
	case FnSumHorizontal:
		return AggSum
	case FnMeanHorizontal:
		return AggMean
	}
	return AggMax
}

// HorizontalInput is the type a horizontal reduction's operands meet at, as a
// conditional's branches meet: Promote, so an Int32 and a Float64 are a Float64.
// The aggregate's binding then types the answer, as for a column.
func HorizontalInput(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	in := dtype.Null
	for _, f := range ops {
		t, ok := dtype.Promote(in, f.Type)
		if !ok {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s: its operands have no common type: %s and %s (%s)", fn, in, f.Type, f.Name).
				Hint("cast the operands so they agree")
		}
		in = t
	}
	return in, nil
}

// listSetOut types a list set operation: a List of the type both lists' elements
// meet at, compared by grouping equality.
//
// PromoteExact, as a join's keys and a Concat meet: an Int64 against a Float64 would
// be compared after rounding, so 2⁵³+1 would equal 2⁵³. Polars meets them at Float64;
// this refuses. Elements are compared as group_by compares keys, so they must be
// hashable.
func listSetOut(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	if len(ops) != 2 {
		return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
	}
	a, b := ops[0].Type, ops[1].Type
	if a.ID() != dtype.TypeList || b.ID() != dtype.TypeList {
		return dtype.Null, uerr.New(uerr.KindType, "list",
			"%s requires two Lists, got %s and %s", fn, a, b).
			Hint("only List columns have elements to compare")
	}
	elem, ok := dtype.PromoteExact(a.Inner(), b.Inner())
	if !ok {
		return dtype.Null, uerr.New(uerr.KindType, "list",
			"%s: the two lists' elements have no common type: %s and %s", fn, a.Inner(), b.Inner()).
			Hint("cast one of them, e.g. .Cast(ursus.List(%s))", a.Inner())
	}
	if !elem.IsHashable() && !elem.IsNull() {
		return dtype.Null, uerr.New(uerr.KindType, "list",
			"%s compares elements, and %s elements cannot be compared", fn, elem).
			Hint("the elements must be numeric, temporal, string or boolean")
	}
	return dtype.List(elem), nil
}

// OrderCmpType is the type order_cmp compares a and b in: their common one, which
// must have an order.
func OrderCmpType(a, b dtype.DataType) (dtype.DataType, error) {
	common, ok := dtype.Promote(a, b)
	if !ok || !common.IsOrdered() && !common.IsNull() {
		return dtype.Null, uerr.New(uerr.KindType, "", "%s and %s have no order to compare them in", a, b)
	}
	return common, nil
}
