package kernel

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Cast converts a column to another type.
//
// Non-strict casts turn an unrepresentable value into a null, which is why the
// type checker marks every non-strict cast nullable regardless of its input.
// Strict casts fail the query and name the offending row.
//
// # A Time target is normalised, and that is a fix rather than tidiness
//
// Cast is one of the two producers of a Time value, and it relabels whenever the two
// sides carry the same tick length — so `Datetime(s) -> Time(s)` reinterpreted
// SECONDS SINCE THE EPOCH as a time of day. It printed correctly by accident, because
// ToTime rebuilds the real instant and the "15:04:05" layout throws the date away,
// while the stored tick stayed a full epoch offset. Sorting such a column therefore
// ordered by DATE, and equality across two days never matched.
//
// Normalising into [0, 24h) does not merely restore an invariant here; it makes
// `Cast(Datetime -> Time)` mean what the user asked for. The wrapping is done once,
// after every arm, rather than in each of the four that can produce a Time.
//
// # Only from an instant
//
// Folding is the time of day of an INSTANT. A bare tick count, or a Duration, has
// no day to take the time of, and folding it made 90000 seconds 01:00 (audit.md
// S18). From anything but a Datetime, a value outside the day is refused under a
// strict cast and null under a lossy one, as Polars does.
func Cast(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	out, err := castTo(name, to, strict, c)
	if err != nil {
		return nil, err
	}
	if to.ID() == dtype.TypeTime {
		if c.DType().ID() == dtype.TypeDatetime {
			return normaliseTime(out)
		}
		return timeInDay(out, c.DType(), strict)
	}
	return out, nil
}

// durationNegates refuses the one Duration with no negation, the minimum, which Neg
// and Abs wrapped to itself (audit.md S20). Every other overflowing temporal
// operation has been refused since step 61; an integer's Abs still wraps, as all
// integer arithmetic here does, and as Polars' does.
func durationNegates(op expr.UnaryOp, c *data.Column) error {
	v, err := data.Values[int64](c)
	if err != nil {
		return nil // payload-free: all nulls
	}
	name := "neg"
	if op == expr.OpAbs {
		name = "abs"
	}
	for i, t := range v {
		if t == math.MinInt64 && c.IsValid(i) {
			return uerr.New(uerr.KindValue, name,
				"%s at row %d has no negation in %s", dtype.FormatTemporal(c.DType(), t), i, c.DType()).
				Hint("%s holds tick counts from %s to %s, and the most negative has no positive "+
					"partner", c.DType(), dtype.FormatTemporal(c.DType(), math.MinInt64),
					dtype.FormatTemporal(c.DType(), math.MaxInt64)).
				Hint("a coarser unit covers a wider span: cast it with .Cast(ursus.Duration(ursus.Micro))")
		}
	}
	return nil
}

// timeInDay refuses, or nulls when lossy, every value of a Time column outside the
// day. It scans before it allocates, as normaliseTime does.
func timeInDay(c *data.Column, from dtype.DataType, strict bool) (*data.Column, error) {
	m, ok := dtype.TicksPerDay(c.DType())
	if !ok {
		return c, nil
	}
	v, err := data.Values[int64](c)
	if err != nil {
		return c, nil // payload-free: all nulls
	}
	bad := -1
	for i, t := range v {
		if (t < 0 || t >= m) && c.IsValid(i) {
			bad = i
			break
		}
	}
	if bad < 0 {
		return c, nil
	}
	if strict {
		return nil, uerr.New(uerr.KindValue, "cast",
			"%s %d at row %d is not a time of day: %s spans 0 to %d", from, v[bad], bad,
			c.DType(), m-1).
			Hint("use a lossy cast to turn values outside the day into nulls")
	}
	keep := bitmap.NewBuilder(len(v))
	for i, t := range v {
		keep.Append(c.IsValid(i) && t >= 0 && t < m)
	}
	return c.WithValidity(keep.Finish()), nil
}

// normaliseTime folds a Time column's ticks into [0, 24h).
//
// It SCANS before it allocates: every cast whose target is a Time comes through here,
// and the overwhelming majority are already in range, so the common case is one pass
// and no copy. It does not write through the input's buffer, which may be shared.
func normaliseTime(c *data.Column) (*data.Column, error) {
	m, ok := dtype.TicksPerDay(c.DType())
	if !ok {
		return c, nil
	}
	v, err := data.Values[int64](c)
	if err != nil {
		// A payload-free Time column is all nulls and has no ticks to fold.
		return c, nil
	}
	need := false
	for i, t := range v {
		if (t < 0 || t >= m) && c.IsValid(i) {
			need = true
			break
		}
	}
	if !need {
		return c, nil
	}
	buf, dst := newValuesBuffer[int64](len(v))
	for i, t := range v {
		dst[i] = ((t % m) + m) % m
	}
	return data.NewFixedBuffer(c.Name(), c.DType(), buf, len(v), c.Validity()), nil
}

func castTo(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	from := c.DType()
	if from == to {
		return c.Rename(name), nil
	}

	// --- from Null ----------------------------------------------------------------
	//
	// CanCast returns true for Null to ANY type — `if from == to || from.IsNull()` is
	// its first line — and the kernel refused every one of them. Same shape as the
	// string divergence below: `Null(NullT).Cast(Int64)` type-checked, planned, and
	// failed at execution with "not implemented yet".
	//
	// The answer is not a conversion at all. A Null column has no values to convert;
	// the result is n nulls that happen to be labelled `to`. It must be built with
	// NullColumn rather than relabelled with WithDType, because data.NewNull carries
	// no payload buffer and the relabelled column would fail in the first kernel that
	// tried to read one.
	if from.IsNull() {
		return NullColumn(name, to, c.Len())
	}
	// --- temporal ----------------------------------------------------------------
	//
	// A relabel is only sound when the TICK SCALE is unchanged. The obvious
	// "same physical layout, different logical meaning — free, just relabel" rule
	// is right for Int64 <-> Datetime, which is the documented way to reach the
	// underlying tick count, and catastrophically wrong for Datetime(ns) ->
	// Datetime(s): both are Int64, so the relabel passed, and 2024-01-01 silently
	// became the year 55,974,289. CastTimeUnit was a data-corruption bug wearing a
	// no-op's clothes.
	//
	// So: rescale when both sides carry a tick length, relabel only when they
	// agree or when one side is a bare integer.
	fn, fok := from.NanosPerTick()
	tn, tok := to.NanosPerTick()
	switch {
	case fok && tok:
		if fn == tn {
			return c.Rename(name).WithDType(to), nil
		}
		return rescaleTemporal(name, to, c, fn, tn, strict)

	case (fok || tok) && from.Physical() == to.Physical():
		// The tick-count escape hatch: Int64 <-> Datetime(u), Int32 <-> Date. The
		// integer side has no unit of its own, so there is nothing to rescale.
		return c.Rename(name).WithDType(to), nil
	}

	// A Categorical first, ahead of an Enum's routing: it is stored as its text
	// (step 351), and castCategorical relabels it or casts the text.
	if from.ID() == dtype.TypeCategorical || to.ID() == dtype.TypeCategorical {
		return castCategorical(name, to, strict, c)
	}

	// An Enum first, ahead of Decimal's routing too: its payload is indices, and
	// IsString had routed it into parseFromString, which read them as offsets and
	// panicked (v0.3-scope.md §2.3). castEnum decodes it to its text.
	if from.ID() == dtype.TypeEnum || to.ID() == dtype.TypeEnum {
		return castEnum(name, to, strict, c)
	}

	// A Decimal is stored as its UNSCALED integer, so every numeric cast below would
	// return the wrong number by a factor of 10^scale — Decimal(10,2) 12.34 would
	// cast to Int64 as 1234 — and castDecimal applies the scale instead. Decimal TO
	// STRING falls through to formatToString, which is exact: the scale is known, so
	// the unscaled digits can be pointed at rather than divided.
	if (from.ID() == dtype.TypeDecimal || to.ID() == dtype.TypeDecimal) &&
		to.ID() != dtype.TypeString {
		return castDecimal(name, to, strict, c)
	}

	// String <-> Binary is a RELABEL. Both are offsets plus a character buffer, so
	// there is nothing to convert; CanCast has a deliberate arm promising both
	// directions and the kernel refused both — "not implemented yet" one way and
	// "cannot format a Binary column as text" the other.
	//
	// Here the KERNEL moves, which is the opposite resolution from Bool <-> temporal
	// above, and that asymmetry is the point: which side is right is a judgement
	// about the types, not a lookup. Three of the four historical instances of this
	// divergence were also resolved by implementing what CanCast promised.
	if (from.ID() == dtype.TypeString && to.ID() == dtype.TypeBinary) ||
		(from.ID() == dtype.TypeBinary && to.ID() == dtype.TypeString) {
		return c.Rename(name).WithDType(to), nil
	}

	// --- parsing and formatting -----------------------------------------------------
	//
	// dtype.CanCast has always permitted string<->numeric and string<->temporal, and
	// the kernel has always refused them. That is a plan-accepts / kernel-rejects
	// divergence: Col("s").Cast(Date) type-checked, planned, and then failed at
	// execution — exactly the class of defect IsOrdered's doc comment was written to
	// prevent for a different predicate. The CSV reader even hinted at it: "read it
	// as String and cast, if a conversion exists".
	//
	// The two sides now agree because the kernel implements what CanCast promised.
	if from.HasStringStorage() && from.ID() != dtype.TypeBinary && !to.IsString() {
		return parseFromString(name, to, strict, c)
	}
	if to.ID() == dtype.TypeString && !from.IsString() {
		return formatToString(name, c)
	}

	// Bool is not numeric — dtype excludes it on purpose, because `true + true` is
	// a bug in every query it appears in — so it needs its own arms rather than
	// falling into the numeric path below. CanCast has promised both directions
	// since step 1 and this kernel refused both, which is the third instance of the
	// plan-accepts / kernel-rejects divergence this file records for null casts and
	// string casts. Step 11 made it reachable with no user-written cast at all:
	// IsClose casts its operands to Float64.
	if from.IsBool() != to.IsBool() {
		// TEMPORAL is excluded, and this is the direction where the KERNEL is
		// wrong rather than CanCast. Bool -> Date reached boolToNumeric, went to
		// Int64 and came back relabelled Date; Date -> Bool read the day count and
		// compared it to zero. Both are physical puns: a date is not a truth value,
		// and 1970-01-01 is not "false".
		//
		// CanCast has refused these all along — its temporal arms cover numeric and
		// string, never Bool — so the two answers disagreed in the SAFE direction,
		// with the planner refusing what the kernel would have done. Narrowing the
		// kernel is what makes them agree; see physical's TestEvaluatorContract,
		// whose cast arm sweeps every pair.
		if from.IsTemporal() || to.IsTemporal() {
			return nil, uerr.New(uerr.KindUnsupported, "cast",
				"cannot cast %s to %s", from, to).
				Hint("a temporal value is not a truth value; compare it instead, " +
					"e.g. .IsNotNull() or a comparison against an instant")
		}
		if from.IsBool() {
			return boolToNumeric(name, to, c)
		}
		return numericToBool(name, c)
	}

	// A List casts by casting its ELEMENTS. CanCast has promised List -> List all
	// along and the kernel refused it — the FOURTH plan-accepts / kernel-rejects
	// divergence this file records, after null casts, string casts and bool casts,
	// and the one that hid longest: the contract matrix had no List column and no
	// List cast target, and the TypeID-pair check of the day named List as an
	// unsamplable source, so neither cast instrument could reach it.
	//
	// It gathers through listGather rather than casting the child in place, and the
	// reason is STRICTNESS rather than indexing. A sliced List keeps absolute
	// offsets over a whole child, so casting that child and keeping the offsets
	// would in fact line up — but the child holds elements belonging to rows this
	// column does not have, and a strict cast fails on a value the target cannot
	// hold. Casting in place therefore rejects a slice because of a row outside it.
	// Measured: a strict cast of [[999],[1,2,3]] sliced to its second row refuses
	// with "value 999 at row 0 is not representable as Int8".
	//
	// An Array is a List each of whose rows has its size (step 341), so the same
	// gather casts between them, and a List becomes an Array only where every row
	// has the size, as Polars refuses it otherwise; an Array changes no size.
	if from.IsListLike() && to.IsListLike() {
		if to.ID() == dtype.TypeArray {
			if err := checkWidths(c, to); err != nil {
				return nil, err
			}
		}
		offs, elems, err := listGather(c, listElements)
		if err != nil {
			return nil, err
		}
		child, err := Cast(elems.Name(), to.Inner(), strict, elems)
		if err != nil {
			return nil, err
		}
		return data.NewList(name, offs, child, c.Validity()).WithDType(to), nil
	}

	fp, tp := from.Physical(), to.Physical()
	if !fp.IsNumeric() || !tp.IsNumeric() {
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cast from %s to %s is not implemented yet", from, to)
	}

	// Integer to integer never touches a float: a float64 holds every integer only
	// up to 2^53, and the widest integers here are 128 bits. See castInt.
	//
	// By PHYSICAL type, which is what makes a temporal tick count an integer here:
	// Datetime(ns) to Uint64 or Duration to Int128 are integer casts of the ticks.
	// Asked of the logical types, IsInteger is false for every temporal type, so
	// those pairs took the float path, and every nanosecond timestamp — all of them
	// past 2^53 — came back rounded to a multiple of 256.
	if fp.IsInteger() && tp.IsInteger() {
		return castInt(name, to, strict, c)
	}
	// To a float: rounded once, from the source's own type. See castToFloat.
	if to.IsFloat() {
		return castToFloat(name, to, strict, c)
	}
	// What is left has a float source, which a float64 holds exactly, and an integer
	// target: exact or refused.
	if tp.ID() == dtype.TypeInt128 {
		return floatToInt128(name, to, strict, c)
	}
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	return floatsToInt(name, to, src, c.Validity(), strict)
}

// checkWidths refuses a List whose valid rows are not all of to's size, to an
// Array's, as Polars refuses it: "not all elements have the specified width". An
// Array keeps its size, which the planner checks first (dtype.CanCast).
func checkWidths(c *data.Column, to dtype.DataType) error {
	from := c.DType()
	if from.ID() == dtype.TypeArray {
		if from.Size() != to.Size() {
			return uerr.New(uerr.KindType, "cast", "cannot cast %s to %s: an Array keeps its size", from, to)
		}
		return nil
	}
	lists, w := c.Lists(), int32(to.Size())
	for i := range c.Len() {
		if s, e, ok := lists.Get(i); ok && e-s != w {
			return uerr.New(uerr.KindValue, "cast",
				"not all elements have the specified width %d: row %d has %d", w, i, e-s).
				Hint("an Array's every row has its size; a List of other lengths stays a List")
		}
	}
	return nil
}

// rescaleTemporal converts a tick count from one resolution to another.
//
// # Why the division floors rather than truncating
//
// Go's integer division truncates toward zero, so -1 nanosecond before the epoch
// would divide to day 0 — 1970-01-01 — while +1 nanosecond after also gives day 0.
// Two different instants collapsing onto the same date, asymmetrically around the
// epoch, is the classic pre-1970 date bug. Flooring keeps the mapping monotone.
//
// Widening (a coarser unit to a finer one) can overflow: year 3000 in seconds is
// 3.2e10, which times 1e9 exceeds int64. Overflow produces a NULL rather than a
// wrapped instant, because a wrapped timestamp is a plausible-looking wrong answer
// and a null is not.
//
// # Strict, which this ignored until step 49
//
// The other lossy casts — floatToInt, floatToInt128, f64ToF32 and, since step 73
// stopped it dropping strict on the way down, parseFromString — all refuse under
// strict and name the offending row, and Cast's
// own doc states the contract as a fact about the kernel: "Strict casts fail the
// query and name the offending row." This one took no strict parameter at all, so
// Cast.Field's `Nullable: cf.Nullable || !c.Strict` declared a strict widening cast
// non-nullable and the kernel then handed it nulls.
//
// It matters beyond a user-written Cast: evalBinary casts operands to the binding
// with strict=true, and three arms of the temporal algebra widen — `Datetime(s) -
// Datetime(ns)` retimes the seconds column to nanoseconds — so an ordinary
// subtraction over two non-nullable columns could produce nulls with no cast
// anywhere in the query to explain them.
func rescaleTemporal(name string, to dtype.DataType, c *data.Column, fromNanos, toNanos int64, strict bool) (*data.Column, error) {
	src, err := readTicks(c)
	if err != nil {
		return nil, err
	}
	n := len(src)
	out := make([]int64, n)
	valid := c.Validity()
	overflowed := false

	if fromNanos > toNanos {
		// Coarser -> finer: multiply, and check for overflow.
		f := fromNanos / toNanos
		limit := math.MaxInt64 / f
		ok := bitmap.NewBuilder(n)
		for i, v := range src {
			fits := v <= limit && v >= -limit
			if !fits && strict && valid.Get(i) {
				return nil, uerr.New(uerr.KindValue, "cast",
					"instant %d at row %d overflows %s", v, i, to).
					Hint("widening to a finer unit multiplies the tick count, and " +
						"int64 nanoseconds only span about 1677 to 2262").
					Hint("use a non-strict cast to turn unrepresentable instants into nulls")
			}
			ok.Append(fits && valid.Get(i))
			overflowed = overflowed || !fits
			if fits {
				out[i] = v * f
			}
		}
		if overflowed {
			valid = ok.Finish()
		}
	} else {
		// Finer -> coarser: losing precision by definition. An instant FLOORS, so
		// the second before the epoch stays before it. A Duration TRUNCATES, as its
		// TotalSeconds does and Polars' cast does: -1.5s is -1s, the mirror of 1.5s
		// (audit.md S19).
		f := toNanos / fromNanos
		floor := to.ID() != dtype.TypeDuration
		for i, v := range src {
			q := v / f
			if floor && v%f != 0 && v < 0 {
				q--
			}
			out[i] = q
		}
	}

	if to.Physical().ID() == dtype.TypeInt32 {
		// Date is int32 days, and this used to be a bare int32(v).
		//
		// The finer-to-coarser branch above has no overflow guard because dividing
		// cannot overflow — but the NARROWING to int32 can, and did so silently.
		// Datetime(s) -> Date divides by 86400 and still leaves room for a tick
		// count far past int32, so the truncation produced a wrapped date: verbatim
		// the outcome this function's own doc says it exists to prevent, "a
		// plausible-looking wrong answer".
		d32 := make([]int32, n)
		ok := bitmap.NewBuilder(n)
		wrapped := false
		for i, v := range out {
			fits := v >= math.MinInt32 && v <= math.MaxInt32
			if !fits && strict && valid.Get(i) {
				return nil, uerr.New(uerr.KindValue, "cast",
					"day %d at row %d does not fit in %s", v, i, to).
					Hint("Date is a signed 32-bit day count, spanning about " +
						"5.88 million years either side of 1970")
			}
			ok.Append(fits && valid.Get(i))
			wrapped = wrapped || !fits
			if fits {
				d32[i] = int32(v)
			}
		}
		if wrapped {
			valid = ok.Finish()
		}
		return data.NewFixed(name, to, d32, valid), nil
	}
	return data.NewFixed(name, to, out, valid), nil
}

// readTicks reads a temporal column's storage as int64, whatever its width.
func readTicks(c *data.Column) ([]int64, error) {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt32:
		v, err := data.Values[int32](c)
		if err != nil {
			return nil, err
		}
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = int64(x)
		}
		return out, nil
	case dtype.TypeInt64:
		return data.Values[int64](c)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cannot read %s as a tick count", c.DType())
	}
}
