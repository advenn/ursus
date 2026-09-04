package dtype_test

import (
	"errors"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// TestComparable is the load-bearing test for the whole type system.
//
// The design documents originally declared DataType comparable while giving it a
// []Field member, which does not compile. The fix is interning: nested payloads
// live behind a canonical pointer, so `==` is exact type equality and DataType
// works as a map key. If interning ever breaks, this test fails and the kernel
// registry and promotion table silently stop finding their entries.
func TestComparable(t *testing.T) {
	// The compile-time half of the claim: DataType must be usable as a map key.
	seen := map[dtype.DataType]int{}

	equal := [][2]dtype.DataType{
		{dtype.Int64, dtype.Int64},
		{dtype.List(dtype.Int64), dtype.List(dtype.Int64)},
		{dtype.List(dtype.List(dtype.String)), dtype.List(dtype.List(dtype.String))},
		{dtype.Array(dtype.Float64, 3), dtype.Array(dtype.Float64, 3)},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Micro, "UTC")},
		{dtype.Datetime(dtype.Micro, ""), dtype.Datetime(dtype.Micro, "")},
		{dtype.Decimal(18, 2), dtype.Decimal(18, 2)},
		{dtype.Enum("a", "b"), dtype.Enum("a", "b")},
		{
			dtype.Struct(dtype.Of("x", dtype.Int64), dtype.Of("y", dtype.String)),
			dtype.Struct(dtype.Of("x", dtype.Int64), dtype.Of("y", dtype.String)),
		},
	}
	for _, p := range equal {
		if p[0] != p[1] {
			t.Errorf("%s != %s, want equal", p[0], p[1])
		}
		seen[p[0]]++
		seen[p[1]]++
		if got := seen[p[0]]; got != 2 {
			t.Errorf("%s: map lookups did not collapse (count %d, want 2)", p[0], got)
		}
	}

	distinct := [][2]dtype.DataType{
		{dtype.Int64, dtype.Int32},
		{dtype.Int64, dtype.Uint64},
		{dtype.List(dtype.Int64), dtype.List(dtype.Int32)},
		{dtype.List(dtype.Int64), dtype.Array(dtype.Int64, 2)},
		{dtype.Array(dtype.Int64, 2), dtype.Array(dtype.Int64, 3)},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Nano, "UTC")},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Micro, "")},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Micro, "Asia/Tashkent")},
		{dtype.Decimal(18, 2), dtype.Decimal(18, 3)},
		{dtype.Enum("a", "b"), dtype.Enum("b", "a")},
		{dtype.Enum("a", "b"), dtype.Enum("a")},
		// The interning-key ambiguity trap: one category containing a comma must
		// not collide with two categories.
		{dtype.Enum("a,b"), dtype.Enum("a", "b")},
		{
			dtype.Struct(dtype.Of("x", dtype.Int64)),
			dtype.Struct(dtype.Of("y", dtype.Int64)),
		},
		{
			dtype.Struct(dtype.Of("x", dtype.Int64)),
			dtype.Struct(dtype.NotNull("x", dtype.Int64)),
		},
		// Field-name concatenation ambiguity: {ab: T} vs {a: T, b: T}.
		{
			dtype.Struct(dtype.Of("ab", dtype.Int64)),
			dtype.Struct(dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.Int64)),
		},
	}
	for _, p := range distinct {
		if p[0] == p[1] {
			t.Errorf("%s == %s, want distinct", p[0], p[1])
		}
	}
}

func TestString(t *testing.T) {
	cases := []struct {
		dt   dtype.DataType
		want string
	}{
		{dtype.Int64, "Int64"},
		{dtype.Null, "Null"},
		{dtype.Datetime(dtype.Micro, ""), "Datetime(us)"},
		{dtype.Datetime(dtype.Nano, "UTC"), "Datetime(ns, UTC)"},
		{dtype.Duration(dtype.Milli), "Duration(ms)"},
		{dtype.Time(dtype.Nano), "Time(ns)"},
		{dtype.Decimal(18, 2), "Decimal(18, 2)"},
		{dtype.List(dtype.Int64), "List(Int64)"},
		{dtype.Array(dtype.Float64, 3), "Array(Float64, 3)"},
		{dtype.Enum("lo", "hi"), "Enum(lo, hi)"},
		{
			dtype.Struct(dtype.Of("x", dtype.Int64), dtype.NotNull("y", dtype.String)),
			"Struct(x: Int64, y: String!)",
		},
	}
	for _, c := range cases {
		if got := c.dt.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}

func TestPredicatesAndPhysical(t *testing.T) {
	// Bool must NOT be numeric: rejecting `true + true` at type-resolution time is
	// the whole reason the predicate exists.
	if dtype.Bool.IsNumeric() {
		t.Error("Bool.IsNumeric() = true; arithmetic on Bool must be rejected")
	}
	if !dtype.Int64.IsNumeric() || !dtype.Float32.IsNumeric() {
		t.Error("integers and floats must be numeric")
	}
	if dtype.String.IsNumeric() {
		t.Error("String must not be numeric")
	}

	physical := []struct{ from, want dtype.DataType }{
		{dtype.Date, dtype.Int32},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Int64},
		{dtype.Duration(dtype.Nano), dtype.Int64},
		{dtype.Time(dtype.Nano), dtype.Int64},
		{dtype.Enum("a"), dtype.Uint32},
		{dtype.Int64, dtype.Int64},
		{dtype.String, dtype.String},
	}
	for _, c := range physical {
		if got := c.from.Physical(); got != c.want {
			t.Errorf("%s.Physical() = %s, want %s", c.from, got, c.want)
		}
	}

	if dtype.String.IsFixedWidth() || dtype.List(dtype.Int64).IsFixedWidth() {
		t.Error("String and List are not fixed width")
	}
	if !dtype.Int32.IsFixedWidth() || dtype.Int32.BitWidth() != 32 {
		t.Error("Int32 must be fixed width, 32 bits")
	}
}

func TestPromote(t *testing.T) {
	cases := []struct {
		a, b dtype.DataType
		want dtype.DataType
		ok   bool
	}{
		{dtype.Int64, dtype.Int64, dtype.Int64, true},
		{dtype.Int8, dtype.Int64, dtype.Int64, true},
		{dtype.Uint8, dtype.Uint32, dtype.Uint32, true},
		{dtype.Float32, dtype.Float64, dtype.Float64, true},

		// A null literal adopts the other operand's type.
		{dtype.Null, dtype.Int32, dtype.Int32, true},
		{dtype.Int32, dtype.Null, dtype.Int32, true},

		// Mixed signedness widens to a signed type that holds both ranges.
		{dtype.Int8, dtype.Uint8, dtype.Int16, true},
		{dtype.Int64, dtype.Uint8, dtype.Int64, true},
		{dtype.Int16, dtype.Uint32, dtype.Int64, true},

		// Float widening is mantissa-aware: Float32 cannot hold every Int32.
		{dtype.Float32, dtype.Int16, dtype.Float32, true},
		{dtype.Float32, dtype.Int32, dtype.Float64, true},
		{dtype.Float64, dtype.Int64, dtype.Float64, true},

		// Uint64 with a signed type promotes to Int128, which holds both ranges
		// losslessly. Before Int128 existed this was rejected outright, because the
		// only 64-bit answers were to wrap or to lose precision above 2^53.
		{dtype.Uint64, dtype.Int64, dtype.Int128, true},
		{dtype.Int8, dtype.Uint64, dtype.Int128, true},

		// Int128 absorbs every other integer type.
		{dtype.Int128, dtype.Int64, dtype.Int128, true},
		{dtype.Int128, dtype.Uint64, dtype.Int128, true},
		{dtype.Int128, dtype.Int8, dtype.Int128, true},

		// Non-numeric types promote only to themselves.
		{dtype.String, dtype.Int64, dtype.Null, false},
		{dtype.Bool, dtype.Int64, dtype.Null, false},
		{dtype.String, dtype.String, dtype.String, true},
		{dtype.Enum("a"), dtype.String, dtype.Null, false},
		{dtype.Date, dtype.Datetime(dtype.Micro, ""), dtype.Null, false},
	}
	for _, c := range cases {
		got, ok := dtype.Promote(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Promote(%s, %s) = (%s, %v), want (%s, %v)",
				c.a, c.b, got, ok, c.want, c.ok)
		}
		// Promotion must be commutative, or `a+b` and `b+a` get different types.
		if rev, revOK := dtype.Promote(c.b, c.a); revOK != ok || rev != got {
			t.Errorf("Promote is not commutative for (%s, %s): %s/%v vs %s/%v",
				c.a, c.b, got, ok, rev, revOK)
		}
	}
}

func TestSchema(t *testing.T) {
	s := dtype.MustSchema(
		dtype.Of("id", dtype.Int64),
		dtype.Of("name", dtype.String),
		dtype.Of("score", dtype.Float64),
	)

	if s.Len() != 3 {
		t.Fatalf("Len = %d", s.Len())
	}
	if got := s.String(); got != "{id: Int64, name: String, score: Float64}" {
		t.Errorf("String = %q", got)
	}
	if f, ok := s.ByName("name"); !ok || f.Type != dtype.String {
		t.Errorf("ByName(name) = %v, %v", f, ok)
	}
	if s.IndexOf("score") != 2 || s.IndexOf("missing") != -1 {
		t.Error("IndexOf")
	}

	// Names and FieldSlice must hand back fresh slices: projection pushdown
	// mutates the result in place, and aliasing the schema would corrupt it.
	n1, n2 := s.Names(), s.Names()
	n1[0] = "clobbered"
	if n2[0] != "id" || s.Field(0).Name != "id" {
		t.Error("Names() aliases schema storage")
	}
	f1, f2 := s.FieldSlice(), s.FieldSlice()
	f1[0].Name = "clobbered"
	if f2[0].Name != "id" || s.Field(0).Name != "id" {
		t.Error("FieldSlice() aliases schema storage")
	}

	sub, err := s.Select([]string{"score", "id"})
	if err != nil {
		t.Fatal(err)
	}
	if got := sub.String(); got != "{score: Float64, id: Int64}" {
		t.Errorf("Select preserved wrong order: %q", got)
	}

	if got := s.Drop("name").String(); got != "{id: Int64, score: Float64}" {
		t.Errorf("Drop = %q", got)
	}
}

func TestSchemaRejectsDuplicates(t *testing.T) {
	_, err := dtype.NewSchema(dtype.Of("x", dtype.Int64), dtype.Of("x", dtype.String))
	if err == nil {
		t.Fatal("NewSchema accepted a duplicate column name")
	}
	if !errors.Is(err, uerr.ErrSchema) {
		t.Errorf("error kind = %v, want schema", err)
	}
}

// TestUnknownColumnMessage pins the diagnostic quality of the single most common
// error in the library. If someone simplifies UnknownColumn, this fails.
func TestUnknownColumnMessage(t *testing.T) {
	s := dtype.MustSchema(
		dtype.Of("tenant_id", dtype.Int64),
		dtype.Of("user_id", dtype.Int64),
		dtype.Of("revenue", dtype.Float64),
	)
	_, err := s.Select([]string{"reveune"})
	if err == nil {
		t.Fatal("expected an error")
	}
	want := `ursus: select: unknown column "reveune"
  did you mean: "revenue"?
  available: tenant_id, user_id, revenue`
	if got := err.Error(); got != want {
		t.Errorf("error message:\n%s\n\nwant:\n%s", got, want)
	}
}

// TestFormatDecimal covers the cases where naive digit-slicing gets it wrong:
// a value shorter than its own scale needs left-padding, and the sign must stay
// outside the padding or -5 at scale 2 renders as "0.-05".
func TestFormatDecimal(t *testing.T) {
	cases := []struct {
		unscaled string
		scale    uint8
		want     string
	}{
		{"1234", 2, "12.34"},
		{"500", 2, "5.00"},
		{"10000", 2, "100.00"},
		{"0", 2, "0.00"},
		{"5", 3, "0.005"},
		{"5", 1, "0.5"},
		{"-5", 2, "-0.05"},
		{"-1234", 2, "-12.34"},
		{"-500", 3, "-0.500"},
		{"42", 0, "42"},
		{"170141183460469231731687303715884105727", 38, "1.70141183460469231731687303715884105727"},
	}
	for _, c := range cases {
		if got := dtype.FormatDecimal(c.unscaled, c.scale); got != c.want {
			t.Errorf("FormatDecimal(%q, %d) = %q, want %q", c.unscaled, c.scale, got, c.want)
		}
	}
}

// TestPromoteTemporalUnits: two temporal columns of the same kind differing only in
// resolution promote to the finer one, so no precision is silently discarded.
//
// Before step 15 this returned (Null, false) for every pair, which meant
// `Col("ts").Gt(someTime)` failed on any column that was not nanosecond-UTC — a
// time.Time literal always lifts to Datetime(ns, UTC) — and an as-of join between two
// files written at different resolutions refused at plan time.
//
// TimeUnit.Finer's own doc has said "Used by type promotion" since step 2; until this
// change its only caller was temporal ARITHMETIC, so `a - b` worked across units and
// `a > b` did not.
func TestPromoteTemporalUnits(t *testing.T) {
	for _, c := range []struct {
		a, b dtype.DataType
		want dtype.DataType
		ok   bool
		why  string
	}{
		// Same kind, same zone, different unit: the finer one wins.
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Nano, "UTC"),
			dtype.Datetime(dtype.Nano, "UTC"), true, "ns is finer than us"},
		{dtype.Datetime(dtype.Second, ""), dtype.Datetime(dtype.Milli, ""),
			dtype.Datetime(dtype.Milli, ""), true, "naive, ms is finer"},
		{dtype.Duration(dtype.Milli), dtype.Duration(dtype.Nano),
			dtype.Duration(dtype.Nano), true, "durations too"},
		{dtype.Time(dtype.Micro), dtype.Time(dtype.Nano),
			dtype.Time(dtype.Nano), true, "and times"},

		// A ZONE mismatch is not a resolution mismatch and stays refused.
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Micro, ""),
			dtype.Null, false, "an instant is not a wall clock"},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Micro, "Europe/Paris"),
			dtype.Null, false, "the result's own zone would be undecidable"},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Nano, "Europe/Paris"),
			dtype.Null, false, "and a unit difference does not rescue it"},

		// Different KINDS stay refused, including the one the table above pins.
		{dtype.Date, dtype.Datetime(dtype.Micro, ""), dtype.Null, false,
			"a date is not an instant until someone picks a zone for the midnight"},
		{dtype.Duration(dtype.Nano), dtype.Datetime(dtype.Nano, ""), dtype.Null, false,
			"a span is not an instant"},
		{dtype.Time(dtype.Nano), dtype.Datetime(dtype.Nano, ""), dtype.Null, false,
			"a wall clock is not an instant"},
		{dtype.Datetime(dtype.Nano, ""), dtype.Int64, dtype.Null, false,
			"a temporal type and a bare integer have no common meaning"},
	} {
		t.Run(c.a.String()+"_"+c.b.String(), func(t *testing.T) {
			got, ok := dtype.Promote(c.a, c.b)
			if ok != c.ok || (ok && got != c.want) {
				t.Errorf("Promote(%s, %s) = (%s, %v), want (%s, %v) — %s",
					c.a, c.b, got, ok, c.want, c.ok, c.why)
			}
			// Commutative, or `a op b` and `b op a` get different types.
			if rev, revOK := dtype.Promote(c.b, c.a); revOK != ok || rev != got {
				t.Errorf("Promote is not commutative for (%s, %s): %s/%v vs %s/%v",
					c.a, c.b, got, ok, rev, revOK)
			}
		})
	}
}
