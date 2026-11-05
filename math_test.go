package ursus_test

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// mathCorpus is built to hit the cases where a naive implementation diverges.
//
// The adversarial half is not decoration: signed zero, NaN and the negative
// domain of sqrt/log are the three things a comparison written with `==` cannot
// see at all, and exact halves are the only inputs that distinguish
// round-half-away-from-zero from round-half-to-even.
//
// Since step 497 the same corpus, and a generated one, are also answered by Polars
// 2.1.0 in the math group of testdata/oracle (oracle_math_test.go), which found
// Ln and Log10 of a subnormal wrong: Go's amd64 math.Log misreads one, and this
// file's oracle, math.Log itself, could not see it (goLog below).
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
// mathOps is every elementwise maths op with its oracle in Go's math package.
var mathOps = []struct {
	name  string
	build func(ursus.Expr) ursus.Expr
	want  func(float64) float64
}{
	{"sqrt", ursus.Expr.Sqrt, math.Sqrt},
	{"cbrt", ursus.Expr.Cbrt, math.Cbrt},
	{"exp", ursus.Expr.Exp, math.Exp},
	{"ln", ursus.Expr.Ln, goLog},
	{"log10", ursus.Expr.Log10, goLog10},
	{"log1p", ursus.Expr.Log1p, math.Log1p},
	{"floor", ursus.Expr.Floor, math.Floor},
	{"ceil", ursus.Expr.Ceil, math.Ceil},
	{"abs", ursus.Expr.Abs, math.Abs},
	{"neg", ursus.Expr.Neg, func(v float64) float64 { return -v }},
	{"sign", ursus.Expr.Sign, goSign},
}

func TestMathMatchesStdlib(t *testing.T) {
	ops := mathOps

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
			if math.Float64bits(got) != math.Float64bits(want) && !(subnormal(in) && ulpsApart(got, want) <= 2) {
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

// knownFloat32MathDefects names each op that answers a Float32 wrongly today.
var knownFloat32MathDefects = map[string]string{}

// float32Corpus is mathCorpus at Float32, with the Float32 limits and the inputs
// whose results are inexact at that width, which is almost all of them.
func float32Corpus() []float32 {
	var out []float32
	seen := map[uint32]bool{}
	add := func(f float32) {
		if !seen[math.Float32bits(f)] {
			seen[math.Float32bits(f)] = true
			out = append(out, f)
		}
	}
	for _, f := range mathCorpus {
		add(float32(f))
	}
	for _, f := range []float32{math.MaxFloat32, math.SmallestNonzeroFloat32, 0x1p-126, 89, -103, 0.1, 3, 10} {
		add(f)
	}
	return out
}

// TestFloat32MathMatchesStdlib is the Float32 twin of TestMathMatchesStdlib. A
// Float32 result is the Float64 result rounded once to a float32 — what Polars and
// PyArrow return — so the oracle is float32(math.F(float64(x))), compared by bit
// pattern. It is a computation, and a computation rounds: nothing here is a null.
//
// A trailing null row pins that a null input is still a null output, and that the
// rows before it are not.
func TestFloat32MathMatchesStdlib(t *testing.T) {
	corpus := float32Corpus()
	valid := make([]bool, len(corpus)+1)
	for i := range corpus {
		valid[i] = true
	}
	exprs := make([]ursus.Expr, len(mathOps))
	for i, op := range mathOps {
		exprs[i] = op.build(ursus.Col("v")).Alias(op.name)
	}
	df, err := ursus.Frame(ursus.ValuesNullable("v", append(slices.Clone(corpus), 0), valid)).
		Select(exprs...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var sawNaN, sawNegZero, sawInf, sawSubnormal, sawOverflow bool
	var inexact, rows int
	for _, op := range mathOps {
		var wrong []string
		if typ := df.Schema().Field(slices.Index(df.Columns(), op.name)).Type; typ != ursus.Float32 {
			t.Fatalf("%s of a Float32 is %s", op.name, typ)
		}
		for row, in := range corpus {
			rows++
			exact := op.want(float64(in))
			want := float32(exact)
			if !math.IsNaN(exact) && float64(want) != exact {
				inexact++
			}
			// What the corpus reaches is the oracle's, whatever the kernel answers.
			switch {
			case math.IsNaN(float64(want)):
				sawNaN = true
			case math.IsInf(float64(want), 0):
				sawInf = true
				if !math.IsInf(float64(in), 0) && !math.IsInf(exact, 0) {
					sawOverflow = true // finite at Float64, infinite at Float32
				}
			case want == 0 && math.Signbit(float64(want)):
				sawNegZero = true
			case want != 0 && math.Abs(float64(want)) < 0x1p-126:
				sawSubnormal = true
			}
			got, ok, err := df.At[float32](row, op.name)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				wrong = append(wrong, fmt.Sprintf("%v -> null", in))
				continue
			}
			if math.Float32bits(got) != math.Float32bits(want) {
				wrong = append(wrong, fmt.Sprintf("%v -> %v, want %v", in, got, want))
			}
		}
		if _, ok, _ := df.At[float32](len(corpus), op.name); ok {
			wrong = append(wrong, "a null input is not a null output")
		}
		judgeFloat32Op(t, op.name, wrong)
	}
	if !sawNaN || !sawNegZero || !sawInf || !sawSubnormal || !sawOverflow {
		t.Errorf("the corpus lost a case: NaN %v, -0 %v, Inf %v, subnormal %v, overflow from a finite input %v",
			sawNaN, sawNegZero, sawInf, sawSubnormal, sawOverflow)
	}
	if inexact*4 < rows {
		t.Errorf("only %d of %d results are inexact at Float32 — the corpus no longer "+
			"reaches the rounding this test is about", inexact, rows)
	}
}

// TestFloat32RoundIsTheFloat64RoundRounded: Round of a Float32 is Round of the same
// value as a Float64, rounded once to a float32 — so its tie-break and its
// arithmetic are Float64 Round's, which TestRoundEdges pins.
func TestFloat32RoundIsTheFloat64RoundRounded(t *testing.T) {
	corpus := float32Corpus()
	wide := make([]float64, len(corpus))
	for i, f := range corpus {
		wide[i] = float64(f)
	}
	var wrong []string
	for _, d := range []int{0, 1, 2, 7} {
		got, err := ursus.Frame(ursus.Values("v", corpus)).Select(ursus.Col("v").Round(d)).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want, err := ursus.Frame(ursus.Values("v", wide)).Select(ursus.Col("v").Round(d)).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for row, in := range corpus {
			w, _, err := want.At[float64](row, "v")
			if err != nil {
				t.Fatal(err)
			}
			g, ok, err := got.At[float32](row, "v")
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				wrong = append(wrong, fmt.Sprintf("Round(%v, %d) -> null", in, d))
			} else if math.Float32bits(g) != math.Float32bits(float32(w)) {
				wrong = append(wrong, fmt.Sprintf("Round(%v, %d) -> %v, want %v", in, d, g, float32(w)))
			}
		}
	}
	judgeFloat32Op(t, "round", wrong)
}

func judgeFloat32Op(t *testing.T, name string, wrong []string) {
	t.Helper()
	why, known := knownFloat32MathDefects[name]
	switch {
	case known && len(wrong) == 0:
		t.Errorf("%s answers correctly now; delete it from knownFloat32MathDefects (%s)", name, why)
	case known:
		t.Logf("%s: known wrong (%s): %d values", name, why, len(wrong))
	case len(wrong) > 0:
		t.Errorf("%s: %d wrong: %v", name, len(wrong), wrong[:min(len(wrong), 6)])
	}
}

// goLog and goLog10 are the math package's, but for a subnormal, which Go's amd64
// Log misreads: its assembly takes the exponent field apart, which a subnormal has
// not, so math.Log(5e-324) is -709.09 where ln(5e-324) is -744.44 (found by the
// recorded oracle, step 497). Log2 takes the exponent by Frexp first and reads a
// subnormal right, so its log is Log2's scaled; the kernel scales the value into the
// normal range instead, and the two can differ in the last bits, so a subnormal input
// is allowed two ulps.
func goLog(v float64) float64 {
	if subnormal(v) {
		return math.Log2(v) * math.Ln2
	}
	return math.Log(v)
}

func goLog10(v float64) float64 {
	if subnormal(v) {
		return math.Log2(v) * (math.Ln2 / math.Ln10)
	}
	return math.Log10(v)
}

func subnormal(v float64) bool { return v > 0 && v < 0x1p-1022 }

// ulpsApart is the number of float64s between a and b, which must have one sign.
func ulpsApart(a, b float64) uint64 {
	x, y := math.Float64bits(a), math.Float64bits(b)
	if x > y {
		return x - y
	}
	return y - x
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
// .Abs(), so nothing noticed for four steps. The values are the math group's
// (abs_sign_every_type, sum_abs_neg_sign_sqrt); what stays is that the types are
// preserved and that the query over a Sum runs at all.
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
	if _, err := ursus.Frame(
		ursus.Values("g", []string{"a", "a"}),
		ursus.Values("v", []int64{-3, 1}),
	).GroupBy(ursus.Col("g")).Agg(
		ursus.Col("v").Sum().Abs().Alias("abs"),
		ursus.Col("v").Sum().Neg().Alias("neg"),
		ursus.Col("v").Sum().Sign().Alias("sign"),
		ursus.Col("v").Sum().Sqrt().Alias("sqrt"),
	).Collect(t.Context(), ursus.WithVerify()); err != nil {
		t.Fatalf("GroupBy(g).Agg(Col(\"v\").Sum().Abs()) — the query that used to "+
			"fail at execution: %v", err)
	}
}

// TestPowIsAFloat: 2**-1 is 0.5 (the math group's pow). With the default binding it
// would have been 0, which is the reason Pow copies Div's arm rather than the default
// and always returns a float.
func TestPowIsAFloat(t *testing.T) {
	s, err := ursus.Frame(ursus.Values("v", []int64{2, 4, 10})).Select(
		ursus.Col("v").Pow(int64(-1)).Alias("inv"),
		ursus.Col("v").Pow(int64(2)).Alias("sq"),
	).CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range s.FieldSlice() {
		if f.Type != ursus.Float64 {
			t.Errorf("%s is %s; Pow must always be a float", f.Name, f.Type)
		}
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
// so its square root would be 1234's rather than 12.34's. Refused where the user can
// see it rather than half-applied. Floor, ceil and round left this list when step
// 490 rounded a Decimal at its scale (decimal_round_test.go).
func TestDecimalMathIsRefusedAtPlanTime(t *testing.T) {
	// The Decimal column is CONSTRUCTED, not cast into existence. It used to be
	// `Col("v").Cast(Decimal(10,2))`, and step 52 moved that refusal to plan time:
	// CanCast no longer promises numeric -> Decimal, because the kernel has always
	// refused it and said why ("wrong by a factor of 10^scale"). The tripwire in
	// castbool_test.go predicted this exact consequence — "it also changes the
	// fixture path decimal_test.go and TestDecimalMathIsRefusedAtPlanTime use to
	// build a Decimal column at all".
	casted := prices(t).Select(ursus.Col("price").Alias("d"))

	for _, c := range []struct {
		name string
		e    ursus.Expr
	}{
		{"sqrt", ursus.Col("d").Sqrt()},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := casted.Select(c.e).CollectSchema(t.Context()); err == nil {
				t.Error("expected a refusal at plan time")
			}
		})
	}
	// Abs and Neg ARE defined for a decimal: negating or taking the magnitude of an
	// unscaled integer is the same operation as doing it to the decimal it encodes.
	// Sign is not, which unaryI128 once answered as unscaled 1, rendered "0.01";
	// since step 490 a Decimal's sign is 1.00 at its scale (decimal_round_test.go).
	if _, err := casted.Select(
		ursus.Col("d").Abs(), ursus.Col("d").Neg().Alias("n"),
	).CollectSchema(t.Context()); err != nil {
		t.Errorf("abs/neg on a decimal are exact and must be allowed: %v", err)
	}
}
