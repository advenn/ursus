package expr

import (
	"strconv"
	"strings"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// CallFn names a namespaced function — the `.str` and `.dt` families.
//
// # Why these are not UnaryOps
//
// `Contains(pattern, literal)` and `Truncate(every)` carry ARGUMENTS, and
// Unary{Op, Child} has nowhere to put them. Three shapes were possible: one op per
// (function, argument) pair, which does not terminate; arguments smuggled into the
// op enum, which is the same thing wearing a hat; or one node that holds a
// function and its operands.
//
// This is the third. It is also the shape every remaining namespace will need —
// `.list`, `.struct`, `.arr` — so paying for it once here is cheaper than three
// more bespoke node types later.
//
// # Appending only
//
// Like UnaryOp, the classifiers below are range comparisons over declaration
// order. Insert a constant mid-enum and a neighbouring function silently changes
// family. Append, and add a name.
type CallFn uint16

const (
	// --- string ---
	FnStrContains CallFn = iota
	FnStrStartsWith
	FnStrEndsWith
	FnStrFind
	FnStrCountMatches
	FnStrExtract
	FnStrReplace
	FnStrReplaceAll
	FnStrToLower
	FnStrToUpper
	FnStrLenBytes
	FnStrLenChars
	FnStrSlice
	FnStrStripChars
	FnStrStripPrefix
	FnStrStripSuffix
	FnStrReverse
	FnStrPadStart
	FnStrPadEnd
	FnStrZFill
	FnStrStripCharsStart
	FnStrStripCharsEnd
	FnStrEscapeRegex
	FnStrSplit
	FnStrSplitN
	FnStrExtractAll
	// The three parse a string with a strftime-style format (step 113): args are
	// the format, strict, and for a Datetime the unit and zone, for a Time the
	// unit. One function per target, because a type is not a literal.
	FnStrToDateFmt
	FnStrToDatetimeFmt
	FnStrToTimeFmt
	fnStrEnd

	// --- temporal ---
	FnDtYear CallFn = iota + 100
	FnDtMonth
	FnDtDay
	FnDtHour
	FnDtMinute
	FnDtSecond
	FnDtMillisecond
	FnDtMicrosecond
	FnDtNanosecond
	FnDtWeekday
	FnDtOrdinalDay
	FnDtQuarter
	FnDtWeek
	FnDtEpoch
	FnDtTruncate
	FnDtTotalDays
	FnDtTotalHours
	FnDtTotalMinutes
	FnDtTotalSeconds
	FnDtStrftime // formats with a strftime-style format; the one arg is the format
	// The calendar (step 145). offset_by's args are an interval's months, days and
	// nanos; round's are truncate's; convert_time_zone's one arg is the zone.
	FnDtOffsetBy
	FnDtRound
	FnDtMonthStart
	FnDtMonthEnd
	FnDtIsLeapYear
	FnDtConvertTimeZone
	fnDtEnd

	// --- type-agnostic ---
	//
	// A third family, and the first that is not keyed to a receiver type. The two
	// above dispatch on what the receiver IS — a String, an instant — but IsIn
	// applies to anything hashable, so it needs its own classifier rather than
	// widening either of theirs.
	FnIsIn CallFn = iota + 200
	fnGenEnd

	// --- maths ---
	//
	// A fourth family, for the maths operations that carry a PARAMETER. The
	// parameterless ones are UnaryOps; Round needs a decimal count, and a Call is
	// where a parameter belongs.
	//
	// Not a field on expr.Unary, which was the obvious alternative: three places
	// rebuild a Unary by hand — physical/agg.go's extractAggs and both predicate
	// pushdown walkers — as `&Unary{Op: t.Op, Child: ...}`, so a new field would be
	// silently DROPPED the moment a predicate moved, and Round(2) would quietly
	// become Round(0). That is step 7's Agg.Params bug, three sites over. Call.String()
	// renders its arguments structurally, so the same class is impossible here.
	FnMathRound CallFn = iota + 300
	// FnMathAsFloat takes a number, or a Duration's ticks, to a float without
	// changing its value: an integer or a Decimal to the nearest Float64, a float as
	// itself. It is how PctChange subtracts in float, and is not public.
	FnMathAsFloat
	// FnMathDiffWiden takes an unsigned integer to the signed type that holds the
	// difference of any two of its values, and leaves every other type as it is. It
	// is how Diff subtracts without wrapping, and is not public.
	FnMathDiffWiden
	// The bit counts of an integer (step 146), Polars' bitwise_count_ones and its
	// five siblings, each a Uint32.
	FnMathCountOnes
	FnMathCountZeros
	FnMathLeadingOnes
	FnMathLeadingZeros
	FnMathTrailingOnes
	FnMathTrailingZeros
	fnMathEnd

	// --- list ---
	//
	// A fifth family, keyed to a List receiver. These all reduce a list to a
	// SCALAR: nothing here builds a list, which is the seam the step was cut on —
	// sort, unique, reverse, slice and the set operations construct a new List
	// column and are a different problem.
	FnListLen CallFn = iota + 400
	FnListGet
	FnListContains
	FnListMin
	FnListMax
	FnListSum
	FnListMean
	FnListJoin // the elements of a List of String, joined; the one arg is the separator

	// list -> LIST. These reshape the list and hand back a list, which is the
	// distinction that split the namespace across two steps: everything above
	// answers a question ABOUT a list, everything here returns one.
	FnListReverse
	FnListHead
	FnListTail
	FnListSlice
	FnListSort
	FnListUnique
	FnListDropNulls
	fnListEnd
)

// The `.struct` family.
const (
	FnStructField CallFn = iota + 500
	fnStructEnd
)

// The horizontal family (step 146): every argument is an OPERAND, a column read
// row by row, where each family above takes a receiver and literal parameters.
//
// concat_str joins its operands' text, and a separator is an operand like any
// other, a literal between each two; struct builds a Struct whose fields are its
// operands, named as they are. Neither has a parameter, so Call's rule — Args[1:]
// literal — does not apply to them, and they are typed from every operand's type
// (ResolveHorizontal) rather than from a receiver's.
const (
	FnConcatStr CallFn = iota + 600
	FnStructOf
	fnHorizontalEnd
)

// IsString, IsTemporal and IsGeneral classify a function by family.
//
// These are range comparisons over declaration order, which is why the enum is
// append-only within each block and the blocks are spaced 100 apart.
func (f CallFn) IsString() bool   { return f < fnStrEnd }
func (f CallFn) IsTemporal() bool { return f >= FnDtYear && f < fnDtEnd }
func (f CallFn) IsGeneral() bool  { return f >= FnIsIn && f < fnGenEnd }
func (f CallFn) IsMath() bool     { return f >= FnMathRound && f < fnMathEnd }
func (f CallFn) IsList() bool     { return f >= FnListLen && f < fnListEnd }
func (f CallFn) IsStruct() bool   { return f >= FnStructField && f < fnStructEnd }
func (f CallFn) IsHorizontal() bool {
	return f >= FnConcatStr && f < fnHorizontalEnd
}

var callNames = map[CallFn]string{
	FnStrContains: "str.contains", FnStrStartsWith: "str.starts_with",
	FnStrEndsWith: "str.ends_with", FnStrFind: "str.find",
	FnStrCountMatches: "str.count_matches", FnStrExtract: "str.extract",
	FnStrReplace: "str.replace", FnStrReplaceAll: "str.replace_all",
	FnStrToLower: "str.to_lower", FnStrToUpper: "str.to_upper",
	FnStrLenBytes: "str.len_bytes", FnStrLenChars: "str.len_chars",
	FnStrSlice: "str.slice", FnStrStripChars: "str.strip_chars",
	FnStrStripPrefix: "str.strip_prefix", FnStrStripSuffix: "str.strip_suffix",
	FnStrReverse: "str.reverse", FnStrPadStart: "str.pad_start",
	FnStrPadEnd: "str.pad_end", FnStrZFill: "str.zfill",
	FnStrStripCharsStart: "str.strip_chars_start",
	FnStrStripCharsEnd:   "str.strip_chars_end",
	FnStrEscapeRegex:     "str.escape_regex", FnStrSplit: "str.split",
	FnStrSplitN: "str.splitn", FnStrExtractAll: "str.extract_all",
	FnStrToDateFmt: "str.to_date", FnStrToDatetimeFmt: "str.to_datetime",
	FnStrToTimeFmt: "str.to_time",

	FnDtYear: "dt.year", FnDtMonth: "dt.month", FnDtDay: "dt.day",
	FnDtHour: "dt.hour", FnDtMinute: "dt.minute", FnDtSecond: "dt.second",
	FnDtMillisecond: "dt.millisecond", FnDtMicrosecond: "dt.microsecond",
	FnDtNanosecond: "dt.nanosecond", FnDtWeekday: "dt.weekday",
	FnDtOrdinalDay: "dt.ordinal_day", FnDtQuarter: "dt.quarter",
	FnDtWeek: "dt.week", FnDtEpoch: "dt.epoch", FnDtTruncate: "dt.truncate",
	FnDtTotalDays: "dt.total_days", FnDtTotalHours: "dt.total_hours",
	FnDtTotalMinutes: "dt.total_minutes", FnDtTotalSeconds: "dt.total_seconds",
	FnDtStrftime: "dt.strftime", FnDtOffsetBy: "dt.offset_by", FnDtRound: "dt.round",
	FnDtMonthStart: "dt.month_start", FnDtMonthEnd: "dt.month_end",
	FnDtIsLeapYear: "dt.is_leap_year", FnDtConvertTimeZone: "dt.convert_time_zone",

	FnIsIn: "is_in",

	FnMathRound: "round", FnMathAsFloat: "as_float", FnMathDiffWiden: "diff_widen",
	FnMathCountOnes: "bitwise_count_ones", FnMathCountZeros: "bitwise_count_zeros",
	FnMathLeadingOnes: "bitwise_leading_ones", FnMathLeadingZeros: "bitwise_leading_zeros",
	FnMathTrailingOnes: "bitwise_trailing_ones", FnMathTrailingZeros: "bitwise_trailing_zeros",
	FnConcatStr: "concat_str", FnStructOf: "struct",

	FnListLen: "list.len", FnListGet: "list.get",
	FnListContains: "list.contains", FnListMin: "list.min",
	FnListMax: "list.max", FnListSum: "list.sum", FnListMean: "list.mean",
	FnListReverse: "list.reverse", FnListHead: "list.head",
	FnListTail: "list.tail", FnListSlice: "list.slice",
	FnListSort: "list.sort", FnListUnique: "list.unique",
	FnListDropNulls: "list.drop_nulls", FnListJoin: "list.join",

	FnStructField: "struct.field",
}

func (f CallFn) String() string {
	if s, ok := callNames[f]; ok {
		return s
	}
	return "call(" + strconv.Itoa(int(f)) + ")"
}

// Call applies a namespaced function.
//
// Args[0] is the RECEIVER — the column the method was called on. Args[1:] are the
// function's parameters and must resolve to literals; a column-valued pattern is
// representable in this IR and simply not implemented yet, which is the right way
// round for a constraint that may lift later.
//
// Args are ordinary children, so RootNames, expansion and projection pushdown all
// see through a Call with no special case. That is the property the design docs
// predicted for window nodes and it holds here for the same reason.
type Call struct {
	Fn   CallFn
	Args []Node
}

func (c *Call) node()            {}
func (c *Call) Children() []Node { return c.Args }

func (c *Call) String() string {
	parts := make([]string, len(c.Args))
	for i, a := range c.Args {
		parts[i] = a.String()
	}
	if len(parts) == 0 || c.Fn.IsHorizontal() {
		return c.Fn.String() + "(" + strings.Join(parts, ", ") + ")"
	}
	return parts[0] + "." + c.Fn.String() + "(" + strings.Join(parts[1:], ", ") + ")"
}

func (c *Call) Field(in *dtype.Schema) (dtype.Field, error) {
	if len(c.Args) == 0 {
		return dtype.Field{}, uerr.Internalf("expr: %s has no receiver", c.Fn)
	}
	if c.Fn.IsHorizontal() {
		fields := make([]dtype.Field, len(c.Args))
		for i, a := range c.Args {
			f, err := a.Field(in)
			if err != nil {
				return dtype.Field{}, err
			}
			fields[i] = f
		}
		out, err := ResolveHorizontal(c.Fn, fields)
		if err != nil {
			return dtype.Field{}, err
		}
		return dtype.Field{Name: fields[0].Name, Type: out, Nullable: true}, nil
	}
	recv, err := c.Args[0].Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	out, err := ResolveCall(c, recv.Type)
	if err != nil {
		return dtype.Field{}, err
	}
	// The name follows the receiver — leftmost-column-wins, same as everywhere
	// else — so `Col("s").Str().ToLower()` is still called "s" unless aliased.
	return dtype.Field{Name: recv.Name, Type: out, Nullable: true}, nil
}

// ResolveCall gives the output type of a call applied to a receiver of type in.
//
// It is the single authority, consulted by Field for the plan's schema and by the
// evaluator for the kernel's output type. Two copies would drift, which is the
// Binding lesson.
//
// It takes the whole Call rather than just the function because `.struct.field` is
// the first call whose output type depends on an ARGUMENT — `field("age")` is Int64
// and `field("city")` is String, from the same receiver.
//
// It used to add "Every other family answers from the receiver's type alone", and
// that was false. `.dt.truncate` is the second, and the divergence lived exactly in
// the sentence: the kernel routes on the interval — `truncate(Time, 1 month)` has no
// answer while `truncate(Time, 1 hour)` does — so a resolver that saw only the
// receiver promised both. The argument is a CONSTANT of the expression, not data, so
// there was never a reason the planner could not see it.
func ResolveCall(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	switch {
	case fn.IsString():
		// HasStringStorage, not IsString. IsString is TRUE FOR ENUM, whose values are
		// logically strings but are stored as a Uint32 index — so a column that
		// passed this gate reached kernel.StrCall, which calls Strings() and gets a
		// zero accessor, and PANICKED on the first row. dtype.IsString's own doc warns
		// about exactly this misuse and names HasStringStorage as the storage
		// question's predicate. Latent today only because Enum columns cannot yet be
		// built from the public API.
		if !in.HasStringStorage() && in.ID() != dtype.TypeNull {
			return dtype.Null, uerr.New(uerr.KindType, "str",
				"%s requires a String operand, got %s", fn, in).
				Hint("cast the column first, e.g. .Cast(ursus.String)")
		}
		if fn.isStrptime() {
			return strptimeOut(c)
		}
		return strCallOut(fn), nil

	case fn.IsTemporal():
		return dtCallOut(c, in)

	case fn.IsGeneral():
		return genCallOut(c, in)

	case fn.IsMath():
		return mathCallOut(fn, in)

	case fn.IsList():
		return listCallOut(c, in)

	case fn.IsStruct():
		return structCallOut(c, in)

	case fn.IsHorizontal():
		// Typed from every operand, which a receiver's type is not: Field and the
		// evaluator call ResolveHorizontal.
		return dtype.Null, uerr.Internalf("expr: %s is typed by ResolveHorizontal", fn)

	default:
		return dtype.Null, uerr.Internalf("expr: unknown call %d", fn)
	}
}

// ResolveHorizontal gives the output type of a horizontal call over operands of
// these fields. Like ResolveCall, it is the one authority, for the plan's schema and
// the kernel's output alike.
func ResolveHorizontal(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	if len(ops) == 0 {
		return dtype.Null, uerr.Internalf("expr: %s has no operands", fn)
	}
	switch fn {
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
	}
	return dtype.Null, uerr.Internalf("expr: unknown horizontal call %d", fn)
}

// genCallOut types the family that does not dispatch on the receiver's type.
func genCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	switch fn := c.Fn; fn {
	case FnIsIn:
		// Membership is decided by the same encoding group_by and distinct use, so
		// the receiver must be a type that encoding accepts.
		if !in.IsHashable() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "is_in",
				"is_in is not defined for %s", in).
				Hint("the value must be hashable: numeric, temporal, string or boolean")
		}
		if !in.IsNull() {
			if _, err := MembershipType(c, in); err != nil {
				return dtype.Null, err
			}
		}
		return dtype.Bool, nil
	default:
		return dtype.Null, uerr.Internalf("expr: unknown general call %d", fn)
	}
}

// MembershipType is the type is_in and list.contains compare at: the type Eq would
// compare the receiver — for list.contains, its element — and each value at. So
// `x.IsIn(v)` refuses exactly the pairs `x.Eq(v)` refuses, and answers the same
// rows wherever both are defined.
//
// The values used to be cast to the receiver's type, which answered a different
// question. A value the column cannot hold — 5000 against an Int8, −1 against a
// Uint64 — was an error where Eq says false; a string, a bool or an instant was
// parsed or converted, so an Int64 matched "1", a Bool matched 1 and a Date matched
// 13:00 on that day, all pairs Eq refuses; and a value was rounded to the column's
// precision, so a Datetime(s) matched 00:00:01.5 and a Float32 matched 0.1.
//
// Equality is still grouping equality, so NaN matches NaN — the one place the two
// differ, and on purpose.
func MembershipType(c *Call, against dtype.DataType) (dtype.DataType, error) {
	common := against
	for _, a := range c.Args[1:] {
		l, ok := a.(*Lit)
		if !ok {
			return dtype.Null, uerr.Internalf("expr: %s argument %s is not a literal", c.Fn, a)
		}
		b, err := ResolveBinary(OpEq, common, l.DT)
		if err != nil {
			return dtype.Null, uerr.New(uerr.KindType, c.Fn.String(),
				"%s cannot compare %s with a %s value", c.Fn, against, l.DT).
				Hint("it compares as == does, and == has no common type for these").
				Hint("cast one side so the two meet, e.g. .Cast(ursus.%s)", l.DT)
		}
		common = b.CastL
	}
	return common, nil
}

// mathCallOut types the parameterised maths family.
//
// Round PRESERVES the operand's type, like Floor and Ceil and for the same reason:
// rounding an integer to any number of decimals is the identity, and widening it
// to a float would change a schema in exchange for nothing.
func mathCallOut(fn CallFn, in dtype.DataType) (dtype.DataType, error) {
	switch fn {
	case FnMathRound:
		// Null is refused, as it is for floor() and ceil() — round is their
		// Call-surface twin and shared their old mistake of promising `in` for a
		// Null operand, which is a type no rounding kernel can dispatch on. The
		// matrix cannot see this one: it has no Call arm, so it was found by
		// following floor and ceil rather than by measurement.
		if !in.IsNumeric() {
			return dtype.Null, uerr.New(uerr.KindType, "round",
				"round() requires a numeric operand, got %s", in).
				Hint("cast it first, e.g. .Cast(ursus.Float64)")
		}
		if in.ID() == dtype.TypeDecimal {
			return dtype.Null, decimalRemedy(uerr.New(uerr.KindType, "round",
				"round() is not defined for %s", in).
				Hint("a decimal is stored as an unscaled integer, so rounding it would " +
					"scale the wrong number"))
		}
		return in, nil
	case FnMathAsFloat:
		// Only PctChange builds this, so its refusal speaks for PctChange: the
		// change between two instants is a Duration, which has no ratio to an
		// instant.
		switch {
		case in.IsFloat():
			return in, nil
		case in.IsNumeric() || in.ID() == dtype.TypeDuration:
			return dtype.Float64, nil
		}
		return dtype.Null, uerr.New(uerr.KindType, "pct_change",
			"pct_change is not defined for %s", in).
			Hint("it is defined for numbers and Durations; for an instant, take Diff, a Duration")
	case FnMathCountOnes, FnMathCountZeros, FnMathLeadingOnes, FnMathLeadingZeros,
		FnMathTrailingOnes, FnMathTrailingZeros:
		// Counted in the type's own width, so the leading zeros of an Int8 1 are 7.
		// Int128 has no kernel here, and a Bool is not a run of bits.
		if !in.IsInteger() || in.ID() == dtype.TypeInt128 {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s requires an integer operand of 8 to 64 bits, got %s", fn, in)
		}
		return dtype.Uint32, nil
	case FnMathDiffWiden:
		// One width up, which holds every difference exactly: a UInt8 difference
		// is within ±255. UInt64's is within ±(2^64−1), which only Int128 holds;
		// Polars gives Int64 there, and nulls where it overflows.
		switch in.ID() {
		case dtype.TypeUint8:
			return dtype.Int16, nil
		case dtype.TypeUint16:
			return dtype.Int32, nil
		case dtype.TypeUint32:
			return dtype.Int64, nil
		case dtype.TypeUint64:
			return dtype.Int128, nil
		}
		return in, nil
	default:
		return dtype.Null, uerr.Internalf("expr: unknown maths call %d", fn)
	}
}

func strCallOut(fn CallFn) dtype.DataType {
	switch fn {
	case FnStrContains, FnStrStartsWith, FnStrEndsWith:
		return dtype.Bool
	case FnStrFind, FnStrCountMatches, FnStrLenBytes, FnStrLenChars:
		return dtype.Uint32
	case FnStrSplit, FnStrSplitN, FnStrExtractAll:
		// The first calls in the namespace whose output is a NESTED type, and the
		// only place that fact is written down. The kernel never names List(String)
		// — data.NewList derives the element type from the child column it is
		// handed, deliberately, so that the declared type cannot disagree with the
		// data. This is the other half of that contract, and the two agreeing is
		// what ResolveCall means by being the single authority.
		//
		// Constant for every receiver, because the family has already normalised
		// Enum and Binary receivers down to String by the time this is reached.
		return dtype.List(dtype.String)
	default:
		return dtype.String
	}
}

// dtCallOut takes the Call rather than the CallFn because truncate's answer depends
// on its INTERVAL and not only on its receiver. See ResolveCall's doc.
func dtCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	isDur := in.ID() == dtype.TypeDuration
	isInstant := in.ID() == dtype.TypeDate || in.ID() == dtype.TypeDatetime ||
		in.ID() == dtype.TypeTime

	switch fn {
	case FnDtTotalDays, FnDtTotalHours, FnDtTotalMinutes, FnDtTotalSeconds:
		if !isDur {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Duration operand, got %s", fn, in).
				Hint("subtract two instants to get a Duration")
		}
		return dtype.Int64, nil

	case FnDtTruncate, FnDtRound:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return truncateOut(c, in)

	case FnDtOffsetBy, FnDtMonthStart, FnDtMonthEnd, FnDtIsLeapYear:
		// A Time has no date to move or to read a year from.
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			e := uerr.New(uerr.KindType, "dt", "%s requires a Date or Datetime operand, got %s", fn, in)
			if fn == FnDtOffsetBy && (isDur || in.ID() == dtype.TypeTime) {
				e.Hint("add a Duration with Add instead")
			}
			return dtype.Null, e
		}
		switch fn {
		case FnDtIsLeapYear:
			return dtype.Bool, nil
		case FnDtOffsetBy:
			args, err := CallArgs(c)
			if err != nil {
				return dtype.Null, err
			}
			iv := dtype.IntervalOf(
				int32(callLitInt(args, 0)), int32(callLitInt(args, 1)), callLitInt(args, 2))
			if err := OffsetRefusal(iv, in); err != nil {
				return dtype.Null, err
			}
		}
		return in, nil

	case FnDtConvertTimeZone:
		return convertZoneOut(c, in)

	case FnDtStrftime:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return strftimeOut(c, in)

	case FnDtEpoch:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return dtype.Int64, nil

	default:
		if !isInstant {
			e := uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
			if isDur {
				e.Hint("a Duration is a length, not a point in time; it has no " +
					"calendar components — use TotalDays, TotalHours and friends")
			} else {
				e.Hint("cast the column first, e.g. .Cast(ursus.Datetime(ursus.Micro, \"UTC\"))")
			}
			return dtype.Null, e
		}
		// Components are small non-negative numbers except the year, which can be
		// negative. Int32 throughout keeps one kernel shape.
		return dtype.Int32, nil
	}
}

// truncateOut types dt.truncate and dt.round, whose refusals depend on their
// argument rather than only on their receiver.
//
// # It asks the kernel's rule, it does not paraphrase it
//
// The interval is rebuilt with dtype.IntervalOf exactly as truncateTemporal does, and
// the refusal is GridRefusal, which the kernel asks too. A plan-time check that
// drifted from the kernel would be worse than no check at all, which is the Binding
// lesson one file over. Until step 145 the two were copies, held together by
// TestTruncateRefusalsAgree in internal/kernel, which still runs.
//
// A missing argument reads as zero, which the first refusal then catches — the same
// answer the kernel's argInt gives, and a truncate node with no interval is not a
// thing any builder produces.
func truncateOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	args, err := CallArgs(c)
	if err != nil {
		return dtype.Null, err
	}
	iv := dtype.IntervalOf(
		int32(callLitInt(args, 0)), int32(callLitInt(args, 1)), callLitInt(args, 2))

	if err := GridRefusal(c.Fn, iv, in); err != nil {
		return dtype.Null, err
	}
	return in, nil
}

// GridRefusal is dt.truncate's and dt.round's refusal of their interval, for a
// receiver of type in. truncateOut asks it at plan time and the kernel at run time,
// which kernel.DtCall's callers reach without resolving, so the two give one answer
// (TestTruncateRefusalsAgree).
func GridRefusal(fn CallFn, iv dtype.Interval, in dtype.DataType) error {
	verb, method := "truncate", "Truncate"
	if fn == FnDtRound {
		verb, method = "round", "Round"
	}
	// Reachable from the public API, which is why it is checked at plan time.
	// dtype.FromDuration does no validation, so `Truncate(time.Duration(0))` and
	// `Truncate(-time.Hour)` carry no error out of DtExpr.Truncate's iv.Err() check —
	// they planned, rendered in Explain, and failed at Collect.
	//
	// Err() is checked first, because an interval that failed to BUILD renders as
	// <invalid> and the message below would then say nothing useful. Ordering is the
	// whole of it: after, this would be unreachable, since a refused interval is
	// zero-valued and IsZero fires first.
	if err := iv.Err(); err != nil {
		return err
	}
	if iv.IsZero() || iv.Negative() {
		return uerr.New(uerr.KindValue, "dt",
			"%s needs a positive interval, got %s", verb, iv).
			Hint(`pass a time.Duration or an interval, e.g. .Dt().%s(time.Hour) `+
				`or .Dt().%s(ursus.Every("1mo"))`, method, method)
	}
	if in.ID() != dtype.TypeTime {
		return nil
	}
	// A Time rounded up from 23:59 to the hour is 24:00, which a Time cannot hold, and
	// wrapping it to 00:00 would put it before the value it rounds.
	if fn == FnDtRound {
		return uerr.New(uerr.KindType, "dt", "round is not defined for %s", in).
			Hint("a Time can round up to 24:00, which it cannot hold").
			Hint("use Truncate, or cast to Datetime first")
	}
	// A Time is a wall clock with no date. A sub-day interval on the same column is
	// fine, which is exactly why the receiver's type alone could never decide it.
	if iv.IsCalendar() {
		return uerr.New(uerr.KindType, "dt",
			"truncate by %s is not defined for %s", iv, in).
			Hint("a Time has no date, so it cannot be floored to a day or a month").
			Hint("use a sub-day interval, or cast to Datetime first")
	}
	return nil
}

// OffsetRefusal is dt.offset_by's refusal of its interval, for a Date or Datetime of
// type in, asked at plan time and by the kernel as GridRefusal is.
//
// The answer has the receiver's type, so an offset it cannot hold is refused rather
// than rounded: an hour is no number of days, and a nanosecond no number of
// microseconds.
func OffsetRefusal(iv dtype.Interval, in dtype.DataType) error {
	if err := iv.Err(); err != nil {
		return err
	}
	npt, ok := in.NanosPerTick()
	if !ok {
		return uerr.Internalf("dt: offset_by on %s", in)
	}
	if iv.Nanos()%npt == 0 {
		return nil
	}
	e := uerr.New(uerr.KindType, "dt", "offset_by %s is finer than %s holds", iv, in)
	if in.ID() == dtype.TypeDate {
		return e.Hint("a Date moves by whole days; cast it to a Datetime first")
	}
	return e.Hint("cast it to a finer unit first, e.g. .Cast(ursus.Datetime(ursus.Nano, zone))")
}

// convertZoneOut types dt.convert_time_zone: the same instants, labelled with another
// zone, so their wall clocks are read there. A naive Datetime is a wall clock in no
// zone and names no instant, so it is refused, as Polars refuses it.
func convertZoneOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	if in.ID() != dtype.TypeDatetime {
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime operand, got %s", c.Fn, in)
	}
	if in.TimeZone() == "" {
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime with a time zone, got %s", c.Fn, in).
			Hint("a naive Datetime names no instant to convert; if its wall clocks " +
				"are UTC, cast it first: .Cast(ursus.Datetime(unit, \"UTC\"))")
	}
	tz, ok := callLitString(c, 1)
	if !ok || tz == "" {
		return dtype.Null, uerr.New(uerr.KindValue, "dt",
			"%s needs a time zone, such as \"Europe/London\"", c.Fn)
	}
	if tz != "UTC" {
		if _, err := time.LoadLocation(tz); err != nil {
			return dtype.Null, uerr.New(uerr.KindValue, "dt",
				"the time zone %q is not known: %v", tz, err)
		}
	}
	return dtype.Datetime(in.TimeUnit(), tz), nil
}

func callLitInt(args []any, i int) int64 {
	if i >= len(args) {
		return 0
	}
	v, _ := args[i].(int64)
	return v
}

// CallArgs extracts the literal arguments of a Call, requiring each to be a
// literal rather than a computed expression.
//
// Column-valued arguments are expressible in the IR and rejected here, so the
// error names the limitation rather than the evaluator failing on a shape it did
// not expect.
func CallArgs(c *Call) ([]any, error) {
	out := make([]any, 0, len(c.Args)-1)
	for _, a := range c.Args[1:] {
		l, ok := a.(*Lit)
		if !ok {
			return nil, uerr.New(uerr.KindUnsupported, "call",
				"%s requires constant arguments, got %s", c.Fn, a.String()).
				Hint("a column-valued argument is not implemented yet")
		}
		out = append(out, l.Value)
	}
	return out, nil
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
	}
	return dtype.Null, uerr.Internalf("expr: unknown struct call %d", c.Fn)
}

// callLitString reads a constant string argument.
func callLitString(c *Call, i int) (string, bool) {
	if i < 0 || i >= len(c.Args) {
		return "", false
	}
	l, ok := c.Args[i].(*Lit)
	if !ok {
		return "", false
	}
	s, ok := l.Value.(string)
	return s, ok
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

	case FnListReverse, FnListHead, FnListTail, FnListSlice,
		FnListSort, FnListUnique, FnListDropNulls:
		// The receiver's type, unchanged. Reshaping a list cannot change what it is
		// a list OF — and returning `in` rather than rebuilding List(elem) means a
		// future parameterised list type carries through without this needing to
		// know about it.
		return in, nil

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
