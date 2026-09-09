package expr

import (
	"strconv"
	"strings"

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

	FnDtYear: "dt.year", FnDtMonth: "dt.month", FnDtDay: "dt.day",
	FnDtHour: "dt.hour", FnDtMinute: "dt.minute", FnDtSecond: "dt.second",
	FnDtMillisecond: "dt.millisecond", FnDtMicrosecond: "dt.microsecond",
	FnDtNanosecond: "dt.nanosecond", FnDtWeekday: "dt.weekday",
	FnDtOrdinalDay: "dt.ordinal_day", FnDtQuarter: "dt.quarter",
	FnDtWeek: "dt.week", FnDtEpoch: "dt.epoch", FnDtTruncate: "dt.truncate",
	FnDtTotalDays: "dt.total_days", FnDtTotalHours: "dt.total_hours",
	FnDtTotalMinutes: "dt.total_minutes", FnDtTotalSeconds: "dt.total_seconds",

	FnIsIn: "is_in",

	FnMathRound: "round",

	FnListLen: "list.len", FnListGet: "list.get",
	FnListContains: "list.contains", FnListMin: "list.min",
	FnListMax: "list.max", FnListSum: "list.sum", FnListMean: "list.mean",
	FnListReverse: "list.reverse", FnListHead: "list.head",
	FnListTail: "list.tail", FnListSlice: "list.slice",
	FnListSort: "list.sort", FnListUnique: "list.unique",
	FnListDropNulls: "list.drop_nulls",

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
	if len(parts) == 0 {
		return c.Fn.String() + "()"
	}
	return parts[0] + "." + c.Fn.String() + "(" + strings.Join(parts[1:], ", ") + ")"
}

func (c *Call) Field(in *dtype.Schema) (dtype.Field, error) {
	if len(c.Args) == 0 {
		return dtype.Field{}, uerr.Internalf("expr: %s has no receiver", c.Fn)
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
// and `field("city")` is String, from the same receiver. Every other family answers
// from the receiver's type alone.
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
		return strCallOut(fn), nil

	case fn.IsTemporal():
		return dtCallOut(fn, in)

	case fn.IsGeneral():
		return genCallOut(fn, in)

	case fn.IsMath():
		return mathCallOut(fn, in)

	case fn.IsList():
		return listCallOut(fn, in)

	case fn.IsStruct():
		return structCallOut(c, in)

	default:
		return dtype.Null, uerr.Internalf("expr: unknown call %d", fn)
	}
}

// genCallOut types the family that does not dispatch on the receiver's type.
func genCallOut(fn CallFn, in dtype.DataType) (dtype.DataType, error) {
	switch fn {
	case FnIsIn:
		// Membership is decided by the same encoding group_by and distinct use, so
		// the receiver must be a type that encoding accepts.
		if !in.IsHashable() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "is_in",
				"is_in is not defined for %s", in).
				Hint("the value must be hashable: numeric, temporal, string or boolean")
		}
		return dtype.Bool, nil
	default:
		return dtype.Null, uerr.Internalf("expr: unknown general call %d", fn)
	}
}

// mathCallOut types the parameterised maths family.
//
// Round PRESERVES the operand's type, like Floor and Ceil and for the same reason:
// rounding an integer to any number of decimals is the identity, and widening it
// to a float would change a schema in exchange for nothing.
func mathCallOut(fn CallFn, in dtype.DataType) (dtype.DataType, error) {
	switch fn {
	case FnMathRound:
		if in.IsNull() {
			return in, nil
		}
		if !in.IsNumeric() {
			return dtype.Null, uerr.New(uerr.KindType, "round",
				"round() requires a numeric operand, got %s", in).
				Hint("cast it first, e.g. .Cast(ursus.Float64)")
		}
		if in.ID() == dtype.TypeDecimal {
			return dtype.Null, uerr.New(uerr.KindType, "round",
				"round() is not defined for %s", in).
				Hint("a decimal is stored as an unscaled integer, so rounding it would " +
					"scale the wrong number").
				Hint("cast to Float64 first if approximate arithmetic is acceptable")
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

func dtCallOut(fn CallFn, in dtype.DataType) (dtype.DataType, error) {
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

	case FnDtTruncate:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return in, nil

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
func listCallOut(fn CallFn, in dtype.DataType) (dtype.DataType, error) {
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
		return dtype.Bool, nil

	case FnListGet, FnListMin, FnListMax:
		// The element type unchanged: picking one element out, or the smallest or
		// largest of them, cannot change what type it is.
		return elem, nil

	case FnListMean:
		return dtype.Float64, nil

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
