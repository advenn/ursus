package ursus_test

import (
	"math"
	"strings"
	"testing"

	"ursus"
)

// mathCorpus is built to hit the cases where a naive implementation diverges.
//
// The adversarial half is not decoration: signed zero, NaN and the negative
// domain of sqrt/log are the three things a comparison written with `==` cannot
// see at all, and exact halves are the only inputs that distinguish
// round-half-away-from-zero from round-half-to-even.
var mathCorpus = []float64{
	0, math.Copysign(0, -1), // +0.0 and -0.0
	1, -1, 2, 4, 9,
	0.5, -0.5, 1.5, 2.5, -2.5, // exact halves — Round's tie-break
	0.125, 0.375, 2.675, 1.005, // near-halves at two decimals
	math.Pi, math.E,
	1e-300, 1e300, -1e300,
	math.SmallestNonzeroFloat64,     // subnormal
	math.SmallestNonzeroFloat64 * 3, // subnormal, not the minimum
	math.MaxFloat64,
	-4, -0.25, // the NEGATIVE domain of Sqrt and Ln
	-2, // Log1p(-2) is NaN; Log1p(-1) is -Inf
	math.Inf(1), math.Inf(-1), math.NaN(),
}

// TestMathMatchesStdlib is what licenses the hand-written maths kernels.
//
// Every op is compared against Go's own math package, BY BIT PATTERN. That is not
// pedantry: `-0.0 == 0.0` is true, so `==` cannot tell Sign(-0.0) from Sign(+0.0);
// `NaN == NaN` is false, so `==` calls every NaN a failure; and two NaNs with
// different payloads are indistinguishable to `==` but not to Float64bits, which
// is what pins that we return math.Sqrt's NaN rather than manufacturing our own.
func TestMathMatchesStdlib(t *testing.T) {
	ops := []struct {
		name  string
		build func(ursus.Expr) ursus.Expr
		want  func(float64) float64
	}{
		{"sqrt", ursus.Expr.Sqrt, math.Sqrt},
		{"cbrt", ursus.Expr.Cbrt, math.Cbrt},
		{"exp", ursus.Expr.Exp, math.Exp},
		{"ln", ursus.Expr.Ln, math.Log},
		{"log10", ursus.Expr.Log10, math.Log10},
		{"log1p", ursus.Expr.Log1p, math.Log1p},
		{"floor", ursus.Expr.Floor, math.Floor},
		{"ceil", ursus.Expr.Ceil, math.Ceil},
		{"abs", ursus.Expr.Abs, math.Abs},
		{"neg", ursus.Expr.Neg, func(v float64) float64 { return -v }},
		{"sign", ursus.Expr.Sign, goSign},
	}

	exprs := make([]ursus.Expr, len(ops))
	for i, op := range ops {
		exprs[i] = op.build(ursus.Col("v")).Alias(op.name)
	}
	df, err := ursus.Frame(ursus.Values("v", mathCorpus)).
		Select(exprs...).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	var sawNaN, sawNegZero, sawInf, sawSubnormal bool
	for _, op := range ops {
		for row, in := range mathCorpus {
			got, ok, err := df.At[float64](row, op.name)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("%s(%v) is null — these operations are TOTAL, so a NaN is a "+
					"value and nothing manufactures a null", op.name, in)
			}
			want := op.want(in)
			if math.Float64bits(got) != math.Float64bits(want) {
				t.Errorf("%s(%v) = %v (bits %#x), want %v (bits %#x)",
					op.name, in, got, math.Float64bits(got), want, math.Float64bits(want))
			}
			switch {
			case math.IsNaN(got):
				sawNaN = true
			case math.IsInf(got, 0):
				sawInf = true
			case got == 0 && math.Signbit(got):
				sawNegZero = true
			case got != 0 && math.Abs(got) < 2.3e-308:
				sawSubnormal = true
			}
		}
	}

	// The corpus-adequacy assertions. Without these a corpus that quietly lost its
	// NaN, or its negative zero, would still pass every comparison above while
	// proving nothing about the cases those comparisons exist for.
	if !sawNaN {
		t.Error("no corpus entry produced a NaN, so the negative domain of " +
			"Sqrt/Ln/Log1p proved nothing")
	}
	if !sawNegZero {
		t.Error("no corpus entry produced a negative zero, so the signed-zero cases " +
			"proved nothing — a Sign that mapped -0.0 to +0.0 would pass")
	}
	if !sawInf {
		t.Error("no corpus entry produced an infinity, so Ln(0) and Exp(1e300) " +
			"proved nothing")
	}
	if !sawSubnormal {
		t.Error("no corpus entry produced a subnormal, so the gradual-underflow " +
			"cases proved nothing")
	}
}

func goSign(v float64) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return v // ±0.0 and NaN keep themselves, matching NumPy
	}
}

// TestRoundTieBreakIsHalfAwayFromZero pins a DECISION, which is why it is separate
// from the differential: the oracle is not a library function but a choice between
// math.Round and math.RoundToEven.
func TestRoundTieBreakIsHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		in       float64
		decimals int
		want     float64
		why      string
	}{
		{0.5, 0, 1, "not 0 — this is what distinguishes it from round-half-to-even"},
		{1.5, 0, 2, "agrees with round-half-to-even, so it proves nothing alone"},
		{2.5, 0, 3, "not 2 — the second discriminating case"},
		{-0.5, 0, -1, "away from zero, not toward -0.0"},
		{-2.5, 0, -3, ""},
		{0.125, 2, 0.13, "an exactly representable half at two decimals"},
		{2.675, 2, 2.68, "2.675*100 rounds UP to exactly 267.5 before math.Round sees it"},
		{1.4, 0, 1, ""},
		{1.6, 0, 2, ""},
	}

	in := make([]float64, len(cases))
	for i, c := range cases {
		in[i] = c.in
	}
	// One expression per distinct decimal count, so this also exercises that two
	// different counts do not share a temporary.
	df, err := ursus.Frame(ursus.Values("v", in)).Select(
		ursus.Col("v").Round(0).Alias("d0"),
		ursus.Col("v").Round(2).Alias("d2"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		col := "d0"
		if c.decimals == 2 {
			col = "d2"
		}
		got, _, err := df.At[float64](i, col)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("Round(%v, %d) = %v, want %v  %s", c.in, c.decimals, got, c.want, c.why)
		}
	}
}

// TestRoundEdges: an integer column is untouched, and a decimal count too large to
// scale returns the value rather than an infinity.
func TestRoundEdges(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("i", []int64{7, -7}),
		ursus.Values("f", []float64{1.23456, math.NaN()}),
	).Select(
		ursus.Col("i").Round(3).Alias("ri"),
		ursus.Col("f").Round(400).Alias("huge"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Field(0).Type; got != ursus.Int64 {
		t.Errorf("Round on an integer column returned %s; it must be the identity", got)
	}
	if v, _, _ := df.At[float64](0, "huge"); v != 1.23456 {
		t.Errorf("Round(x, 400) = %v, want the value unchanged", v)
	}

	_, err = ursus.Frame(ursus.Values("f", []float64{1})).
		Select(ursus.Col("f").Round(-1)).CollectSchema(t.Context())
	if err == nil {
		t.Error("a negative decimal count must be refused")
	}
}

// TestAbsAndNegCoverEveryNumericType is the type hole this step was created to
// close.
//
// Before it, ResolveUnary accepted every IsNumeric() type while the kernel handled
// only the signed integers and the floats — so an ordinary query type-checked,
// planned, rendered in Explain, and failed at EXECUTION. Nothing anywhere called
// .Abs(), so nothing noticed for four steps.
func TestAbsAndNegCoverEveryNumericType(t *testing.T) {
	f := ursus.Frame(
		ursus.Values("i8", []int8{-3}),
		ursus.Values("i16", []int16{-3}),
		ursus.Values("i32", []int32{-3}),
		ursus.Values("i64", []int64{-3}),
		ursus.Values("u8", []uint8{3}),
		ursus.Values("u16", []uint16{3}),
		ursus.Values("u32", []uint32{3}),
		ursus.Values("u64", []uint64{3}),
		ursus.Values("f32", []float32{-3}),
		ursus.Values("f64", []float64{-3}),
	)
	signed := []string{"i8", "i16", "i32", "i64", "f32", "f64"}
	unsigned := []string{"u8", "u16", "u32", "u64"}

	var exprs []ursus.Expr
	for _, c := range append(append([]string{}, signed...), unsigned...) {
		exprs = append(exprs,
			ursus.Col(c).Abs().Alias("abs_"+c),
			ursus.Col(c).Sign().Alias("sign_"+c))
	}
	for _, c := range signed {
		exprs = append(exprs, ursus.Col(c).Neg().Alias("neg_"+c))
	}
	df, err := f.Select(exprs...).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("abs/neg/sign over every numeric type: %v", err)
	}
	// Types are preserved, which is the other half of the contract.
	src, err := f.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i, fld := range df.Schema().FieldSlice() {
		base := fld.Name[strings.IndexByte(fld.Name, '_')+1:]
		want, ok := src.ByName(base)
		if !ok {
			t.Fatalf("column %d %s has no source column %q", i, fld.Name, base)
		}
		if fld.Type != want.Type {
			t.Errorf("column %d %s is %s, want %s — these ops preserve the type",
				i, fld.Name, fld.Type, want.Type)
		}
	}

	// Int128 and Decimal reach the same kernel, and Int128 is not exotic: it is what
	// every integer Sum produces, so this is an ordinary query.
	agg, err := ursus.Frame(
		ursus.Values("g", []string{"a", "a"}),
		ursus.Values("v", []int64{-3, 1}),
	).GroupBy(ursus.Col("g")).Agg(
		ursus.Col("v").Sum().Abs().Alias("abs"),
		ursus.Col("v").Sum().Neg().Alias("neg"),
		ursus.Col("v").Sum().Sign().Alias("sign"),
		ursus.Col("v").Sum().Sqrt().Alias("sqrt"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("GroupBy(g).Agg(Col(\"v\").Sum().Abs()) — the query that used to "+
			"fail at execution: %v", err)
	}
	if got, _, _ := agg.At[ursus.Int128Value](0, "abs"); got.String() != "2" {
		t.Errorf("abs(sum) = %s, want 2", got)
	}
	if got, _, _ := agg.At[ursus.Int128Value](0, "sign"); got.String() != "-1" {
		t.Errorf("sign(sum) = %s, want -1", got)
	}
}

// TestUnsignedAbsIsTheIdentity: refusing abs on an unsigned column would have been
// an error where the right answer is free, which is why the kernel widened rather
// than ResolveUnary narrowing.
func TestUnsignedAbsIsTheIdentity(t *testing.T) {
	df, err := ursus.Frame(ursus.Values("u", []uint32{0, 7, 4294967295})).
		Select(ursus.Col("u").Abs().Alias("a"), ursus.Col("u").Sign().Alias("s")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []uint32{0, 7, 4294967295} {
		if got, _, _ := df.At[uint32](i, "a"); got != want {
			t.Errorf("abs row %d = %d, want %d", i, got, want)
		}
	}
	for i, want := range []uint32{0, 1, 1} {
		if got, _, _ := df.At[uint32](i, "s"); got != want {
			t.Errorf("sign row %d = %d, want %d", i, got, want)
		}
	}
}

// TestFloatModAndFloorDiv covers the twin defect, and the deliberate disagreement
// between integer and float division by zero.
//
// Neither Mod nor FloorDiv had a single test anywhere before this, which is how
// arithFloat came to accept % at plan time and refuse it at execution for nine
// steps.
func TestFloatModAndFloorDiv(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("a", []float64{7, -7, 7, 5.5}),
		ursus.Values("b", []float64{2, 2, 0, 2}),
		ursus.Values("ia", []int64{7, -7, 7, 5}),
		ursus.Values("ib", []int64{2, 2, 0, 2}),
	).Select(
		ursus.Col("a").Mod(ursus.Col("b")).Alias("fmod"),
		ursus.Col("a").FloorDiv(ursus.Col("b")).Alias("fdiv"),
		ursus.Col("ia").Mod(ursus.Col("ib")).Alias("imod"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("float Mod: %v", err)
	}

	// math.Mod takes the sign of the dividend, matching Go's % on integers.
	for i, want := range []float64{1, -1, math.NaN(), 1.5} {
		got, ok, _ := df.At[float64](i, "fmod")
		if !ok {
			t.Fatalf("fmod row %d is null; float mod is TOTAL, unlike integer mod", i)
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Errorf("fmod row %d = %v, want %v", i, got, want)
		}
	}
	// Integer mod by zero is NULL, and that disagreement with the float case is a
	// decision: Go panics on integer division by zero, IEEE does not.
	if _, ok, _ := df.At[int64](2, "imod"); ok {
		t.Error("integer mod by zero must be null")
	}
}

// TestPowIsNotIntegerTruncated: 2**-1 is 0.5. With the default binding it would
// have been 0, which is the reason Pow copies Div's arm rather than the default.
func TestPowIsNotIntegerTruncated(t *testing.T) {
	df, err := ursus.Frame(ursus.Values("v", []int64{2, 4, 10})).Select(
		ursus.Col("v").Pow(int64(-1)).Alias("inv"),
		ursus.Col("v").Pow(0.5).Alias("root"),
		ursus.Col("v").Pow(int64(2)).Alias("sq"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Field(0).Type; got != ursus.Float64 {
		t.Fatalf("Pow returned %s; it must always be a float", got)
	}
	if got, _, _ := df.At[float64](0, "inv"); got != 0.5 {
		t.Errorf("2**-1 = %v, want 0.5 — an integer binding would give 0", got)
	}
	if got, _, _ := df.At[float64](1, "root"); got != 2 {
		t.Errorf("4**0.5 = %v, want 2", got)
	}
	if got, _, _ := df.At[float64](2, "sq"); got != 100 {
		t.Errorf("10**2 = %v, want 100", got)
	}
}

// TestIntegerMathReturnsFloat pins the ResolveUnary/kernel agreement that
// WithVerify exists for: the schema the plan promises and the schema the batch
// carries must be the same one.
func TestIntegerMathReturnsFloat(t *testing.T) {
	lf := ursus.Frame(
		ursus.Values("i", []int64{4}),
		ursus.Values("f32", []float32{4}),
	).Select(
		ursus.Col("i").Sqrt().Alias("si"),
		ursus.Col("f32").Sqrt().Alias("sf"),
		ursus.Col("i").Floor().Alias("fi"),
	)
	want, err := lf.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if !want.Equal(got.Schema()) {
		t.Fatalf("CollectSchema says %s, Collect produced %s", want, got.Schema())
	}
	if want.Field(0).Type != ursus.Float64 {
		t.Errorf("sqrt(Int64) is %s, want Float64", want.Field(0).Type)
	}
	if want.Field(1).Type != ursus.Float32 {
		t.Errorf("sqrt(Float32) is %s, want Float32 — a narrow float was chosen "+
			"deliberately and must not silently double", want.Field(1).Type)
	}
	if want.Field(2).Type != ursus.Int64 {
		t.Errorf("floor(Int64) is %s, want Int64 — flooring an integer is the identity",
			want.Field(2).Type)
	}
}

// TestMathRefusals: every one lands at build time or logical plan time, never at
// physical plan time. A refusal only Collect notices lets Explain print a plan
// that cannot run.
func TestMathRefusals(t *testing.T) {
	f := ursus.Frame(
		ursus.Values("s", []string{"a"}),
		ursus.Values("b", []bool{true}),
	)
	cases := []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"sqrt of a string", ursus.Col("s").Sqrt(), "requires a numeric operand"},
		{"floor of a bool", ursus.Col("b").Floor(), "requires a numeric operand"},
		{"round of a string", ursus.Col("s").Round(2), "requires a numeric operand"},
		{"log base 0", ursus.Col("s").Log(0), "log base must be"},
		{"log base 1", ursus.Col("s").Log(1), "log base must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// CollectSchema, not Collect: the refusal must be visible without running.
			_, err := f.Select(c.e).CollectSchema(t.Context())
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should say %q:\n%v", c.want, err)
			}
		})
	}
}

// TestDecimalMathIsRefusedAtPlanTime: a decimal is stored as an unscaled integer,
// so flooring it would floor 1234 rather than 12.34. Refused where the user can
// see it rather than half-applied.
func TestDecimalMathIsRefusedAtPlanTime(t *testing.T) {
	dec := ursus.Decimal(10, 2)
	f := ursus.Frame(ursus.Values("g", []string{"a"}), ursus.Values("v", []int64{1}))
	casted := f.Select(ursus.Col("v").Cast(dec).Alias("d"))

	for _, c := range []struct {
		name string
		e    ursus.Expr
	}{
		{"floor", ursus.Col("d").Floor()},
		{"ceil", ursus.Col("d").Ceil()},
		{"sqrt", ursus.Col("d").Sqrt()},
		{"round", ursus.Col("d").Round(1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := casted.Select(c.e).CollectSchema(t.Context()); err == nil {
				t.Error("expected a refusal at plan time")
			}
		})
	}
	// Abs and Neg ARE defined for a decimal: negating or taking the magnitude of an
	// unscaled integer is the same operation as doing it to the decimal it encodes.
	// SIGN IS NOT, and this comment used to claim it was while exercising only two
	// of the three — see TestSignOnDecimalIsRefused.
	if _, err := casted.Select(
		ursus.Col("d").Abs(), ursus.Col("d").Neg().Alias("n"),
	).CollectSchema(t.Context()); err != nil {
		t.Errorf("abs/neg on a decimal are exact and must be allowed: %v", err)
	}
}

// TestSignOnDecimalIsRefused: sign() is worse on a decimal than floor() is, and it
// was the one that got through.
//
// unaryI128's Sign arm returns the unscaled integer 1, and the result carries the
// operand's type — so Col(price).Sign() on a Decimal(10,2) is 1 labelled
// Decimal(10,2), which RENDERS AS "0.01". No error, no null, a number that looks
// like a price. The guard three lines above it in ResolveUnary already excluded
// Duration for the identical reason ("a Duration of -1, 0 or 1 TICKS is not a sign,
// it is a nanosecond") and was not applied here.
//
// Through Collect, not CollectSchema: the old test reached the resolver only, so
// unaryI128 never ran on a Decimal column anywhere in the suite and the wrong VALUE
// was never observed.
func TestSignOnDecimalIsRefused(t *testing.T) {
	_, err := prices(t).Select(ursus.Col("price").Sign()).Collect(t.Context())
	if err == nil {
		t.Fatal("sign() on a decimal returned unscaled 1, which renders as 0.01")
	}
	for _, want := range []string{"sign", "Decimal", "unscaled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should say %q:\n%v", want, err)
		}
	}
	// The refusal is at PLAN time, so it costs nothing at execution and a user sees
	// it from CollectSchema.
	if _, err := prices(t).Select(ursus.Col("price").Sign()).
		CollectSchema(t.Context()); err == nil {
		t.Error("the refusal must be visible without running the query")
	}
}
