package dtype_test

import (
	"errors"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

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

// promoteTypes is every kind of type Promote meets, for the laws below.
var promoteTypes = []dtype.DataType{
	dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64, dtype.Int128,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64,
	dtype.Float32, dtype.Float64, dtype.Decimal(10, 2),
	dtype.Null, dtype.Bool, dtype.String, dtype.Enum("a"), dtype.Enum("b"),
	dtype.Date, dtype.Datetime(dtype.Micro, ""), dtype.Datetime(dtype.Nano, "UTC"), dtype.Datetime(dtype.Micro, "UTC"),
	dtype.Duration(dtype.Milli), dtype.Duration(dtype.Nano), dtype.Time(dtype.Nano),
	dtype.List(dtype.Int64), dtype.Struct(dtype.Of("x", dtype.Int64)),
}

// TestPromote: the type two types meet at is Polars 2.1.0's, recorded in the dtype
// group of testdata/oracle as the type When/Then/Otherwise gives its branches and
// spelled in oracle_dtype_test.go since step 503, which took this test's table of
// types. What stays are the laws of the meet and the refusals.
func TestPromote(t *testing.T) {
	for _, a := range promoteTypes {
		// A type meets itself at itself.
		if got, ok := dtype.Promote(a, a); !ok || got != a {
			t.Errorf("Promote(%s, %s) = (%s, %v)", a, a, got, ok)
		}
		// A null literal adopts the other operand's type.
		if got, ok := dtype.Promote(dtype.Null, a); !ok || got != a {
			t.Errorf("Promote(Null, %s) = (%s, %v), want %s", a, got, ok, a)
		}
		for _, b := range promoteTypes {
			// Commutative, or `a+b` and `b+a` get different types.
			got, ok := dtype.Promote(a, b)
			if rev, revOK := dtype.Promote(b, a); revOK != ok || rev != got {
				t.Errorf("Promote is not commutative for (%s, %s): %s/%v vs %s/%v", a, b, got, ok, rev, revOK)
			}
		}
	}
	// Two Enums of different categories meet at the String, which holds both exactly
	// (step 79), where Polars 2.1 refuses them: dtype/when_enum_a_enum_b, a finding of
	// step 503, pinned here until the maintainer decides.
	if got, ok := dtype.Promote(dtype.Enum("a"), dtype.Enum("b")); !ok || got != dtype.String {
		t.Errorf("Promote(Enum(a), Enum(b)) = (%s, %v), want String", got, ok)
	}
	// The refusals. Text, a Boolean, a Date and a Datetime beside a number, and a
	// Date beside a Datetime, are refused here where Polars 2.1's supertype makes
	// text, the number and the Datetime: recorded in the dtype group and left to the
	// maintainer (step 503's record, Found). An Enum beside a number is refused in
	// both.
	for _, c := range [][2]dtype.DataType{
		{dtype.String, dtype.Int64}, {dtype.Bool, dtype.Int64}, {dtype.Enum("a"), dtype.Int64},
		{dtype.Date, dtype.Datetime(dtype.Micro, "")}, {dtype.Datetime(dtype.Nano, ""), dtype.Int64},
		{dtype.List(dtype.Int64), dtype.Int64}, {dtype.Struct(dtype.Of("x", dtype.Int64)), dtype.String},
	} {
		if got, ok := dtype.Promote(c[0], c[1]); ok {
			t.Errorf("Promote(%s, %s) = %s, want a refusal", c[0], c[1], got)
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

// TestFormatDecimal holds FormatDecimal to math/big over the cases where naive
// digit-slicing gets it wrong — a value shorter than its own scale needs
// left-padding, and the sign must stay outside the padding or -5 at scale 2 renders
// as "0.-05" — and over 300 seeded unscaled values of up to 39 digits at every scale
// (step 503). The oracle is big.Rat's FloatString of unscaled/10^scale at the scale,
// exact for every value here.
func TestFormatDecimal(t *testing.T) {
	type in struct {
		unscaled string
		scale    uint8
	}
	cases := []in{
		{"1234", 2}, {"500", 2}, {"10000", 2}, {"0", 2}, {"5", 3}, {"5", 1}, {"-5", 2}, {"-1234", 2}, {"-500", 3},
		{"42", 0}, {"170141183460469231731687303715884105727", 38}, {"-170141183460469231731687303715884105728", 38},
		{"-1", 38}, {"1", 0}, {"0", 0}, {"-0", 2},
	}
	rng := rand.New(rand.NewPCG(503, 1))
	for range 300 {
		digits := 1 + rng.IntN(39)
		b := make([]byte, digits)
		for i := range b {
			b[i] = '0' + byte(rng.IntN(10))
		}
		if digits > 1 && b[0] == '0' {
			b[0] = '1' + byte(rng.IntN(9)) // an unscaled integer's text has no leading zero
		}
		s := string(b)
		if rng.IntN(2) == 0 {
			s = "-" + s
		}
		cases = append(cases, in{s, uint8(rng.IntN(39))})
	}
	for _, c := range cases {
		u, ok := new(big.Int).SetString(c.unscaled, 10)
		if !ok {
			t.Fatalf("%q is not an integer", c.unscaled)
		}
		want := new(big.Rat).SetFrac(u, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(c.scale)), nil)).FloatString(int(c.scale))
		if u.Sign() == 0 && strings.HasPrefix(c.unscaled, "-") {
			want = "-" + want // big.Rat has no negative zero; the text keeps its sign
		}
		if got := dtype.FormatDecimal(c.unscaled, c.scale); got != want {
			t.Errorf("FormatDecimal(%q, %d) = %q, want %q", c.unscaled, c.scale, got, want)
		}
	}
}

// TestPromoteTemporalUnits: two temporal columns of the same kind and zone differing
// only in resolution promote to the finer one, so no precision is silently discarded,
// `Col("ts").Gt(someTime)` works on a column that is not nanosecond-UTC — a time.Time
// literal always lifts to Datetime(ns, UTC) — and an as-of join between two files
// written at different resolutions resolves at plan time; before step 15 every such
// pair was refused. Every pair of the four units, in every kind and in a zone, is held
// to Go's time constants: the unit met at is the one whose tick is the shorter (step
// 503). Polars 2.1 meets two units at the coarser, recorded in the dtype group and a
// finding of step 503 left to the maintainer; this pins Promote's documented rule
// meanwhile. A zone mismatch and a mismatch of kinds stay refused.
func TestPromoteTemporalUnits(t *testing.T) {
	tick := map[dtype.TimeUnit]time.Duration{
		dtype.Second: time.Second, dtype.Milli: time.Millisecond, dtype.Micro: time.Microsecond, dtype.Nano: time.Nanosecond,
	}
	kinds := map[string]func(dtype.TimeUnit) dtype.DataType{
		"naive":    func(u dtype.TimeUnit) dtype.DataType { return dtype.Datetime(u, "") },
		"UTC":      func(u dtype.TimeUnit) dtype.DataType { return dtype.Datetime(u, "UTC") },
		"Paris":    func(u dtype.TimeUnit) dtype.DataType { return dtype.Datetime(u, "Europe/Paris") },
		"duration": dtype.Duration,
		"time":     dtype.Time,
	}
	for name, kind := range kinds {
		for u := range tick {
			for v := range tick {
				finer := u
				if tick[v] < tick[u] {
					finer = v
				}
				got, ok := dtype.Promote(kind(u), kind(v))
				if !ok || got != kind(finer) {
					t.Errorf("%s: Promote(%s, %s) = (%s, %v), want %s, the shorter tick", name, kind(u), kind(v), got, ok, kind(finer))
				}
				if exact, ok := dtype.PromoteExact(kind(u), kind(v)); !ok || exact != got {
					t.Errorf("%s: PromoteExact(%s, %s) = (%s, %v), Promote %s", name, kind(u), kind(v), exact, ok, got)
				}
			}
		}
	}
	for _, c := range []struct {
		a, b dtype.DataType
		why  string
	}{
		// A ZONE mismatch is not a resolution mismatch.
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Micro, ""), "an instant is not a wall clock"},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Micro, "Europe/Paris"), "the result's own zone would be undecidable"},
		{dtype.Datetime(dtype.Micro, "UTC"), dtype.Datetime(dtype.Nano, "Europe/Paris"), "and a unit difference does not rescue it"},
		// Different KINDS.
		{dtype.Date, dtype.Datetime(dtype.Micro, ""), "a date is not an instant until someone picks a zone for the midnight"},
		{dtype.Duration(dtype.Nano), dtype.Datetime(dtype.Nano, ""), "a span is not an instant"},
		{dtype.Time(dtype.Nano), dtype.Datetime(dtype.Nano, ""), "a wall clock is not an instant"},
		{dtype.Datetime(dtype.Nano, ""), dtype.Int64, "a temporal type and a bare integer have no common meaning"},
	} {
		if got, ok := dtype.Promote(c.a, c.b); ok {
			t.Errorf("Promote(%s, %s) = %s, want a refusal — %s", c.a, c.b, got, c.why)
		}
		if _, ok := dtype.Promote(c.b, c.a); ok {
			t.Errorf("Promote(%s, %s) resolves the other way round", c.b, c.a)
		}
	}
}

// TestPromoteExact holds PromoteExact to its rule over every pair of promoteTypes:
// Promote's answer, except where that answer rounds one side — a float met by an
// integer of 64 bits or more, or by a Decimal, neither of which a float holds exactly
// — where it refuses, and every cast MeetHint then recommends is one CanCast permits.
// The types it resolves are recorded in the dtype group as the type Unpivot melts
// two columns to (step 503).
func TestPromoteExact(t *testing.T) {
	inexact := func(d dtype.DataType) bool { return d.IsInteger() && d.BitWidth() >= 64 || d.ID() == dtype.TypeDecimal }
	for _, a := range promoteTypes {
		for _, b := range promoteTypes {
			got, ok := dtype.PromoteExact(a, b)
			meet, meets := dtype.Promote(a, b)
			rounds := meets && meet.IsFloat() && (inexact(a) || inexact(b))
			switch {
			case rounds && ok:
				t.Errorf("PromoteExact(%s, %s) = %s, want a refusal: the float rounds the integer", a, b, got)
			case rounds:
				if h := dtype.MeetHint(a, b); !strings.Contains(h, "exactly") || !dtype.CanCast(b, a) {
					t.Errorf("MeetHint(%s, %s) = %q", a, b, h)
				}
			case ok != meets || got != meet:
				t.Errorf("PromoteExact(%s, %s) = (%s, %v), Promote (%s, %v)", a, b, got, ok, meet, meets)
			}
		}
	}
}
