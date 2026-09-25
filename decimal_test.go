package ursus_test

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/internal/uerr"
)

// Decimal was declarable before step 3 and had nowhere to be stored: DataType
// carried the precision and scale, but Physical() had no case for it, so a Decimal
// column could not be read by any kernel. Worse, IsOrdered() already returned true,
// so Sort(Col("price")) passed plan-time validation and then failed in the sort
// comparator — the plan-accepts / kernel-rejects divergence IsOrdered's own doc
// warns about.
//
// Adding `case TypeDecimal: return Int128` to Physical() fixes all of it at once,
// because everything that dispatches on the physical type already handles Int128.
// The tests below are in two halves: what that unlocks, and the three guards on
// the operations for which the unscaled integer is the WRONG answer.

// dec makes an unscaled Int128 from a whole number of units.
func dec(units int64) i128.Int128 { return i128.FromInt64(units) }

// prices is a Decimal(10,2) column: 12.34, 5.00, null, -0.05, 100.00.
func prices(t *testing.T) *ursus.LazyFrame {
	t.Helper()

	dt := dtype.Decimal(10, 2)
	schema, err := dtype.NewSchema(
		dtype.Field{Name: "price", Type: dt, Nullable: true},
		dtype.Field{Name: "sku", Type: dtype.String},
	)
	if err != nil {
		t.Fatal(err)
	}

	valid := bitmap.NewBuilder(5)
	for _, v := range []bool{true, true, false, true, true} {
		valid.Append(v)
	}
	b, err := data.NewBatch(schema, []*data.Column{
		data.NewFixed("price", dt,
			[]i128.Int128{dec(1234), dec(500), dec(0), dec(-5), dec(10000)},
			valid.Finish()),
		data.NewString("sku", []string{"a", "b", "c", "d", "e"}, bitmap.AllSet(5)),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	return ursus.Scan(src)
}

// TestDecimalRendersScaled is the one that would fail most invisibly. A Decimal is
// stored as its unscaled integer, so without a render case 12.34 prints as "1234"
// — not a rounding error but a factor of 100, and entirely plausible on screen.
func TestDecimalRendersScaled(t *testing.T) {
	df, err := prices(t).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := df.String()

	for _, want := range []string{"12.34", "5.00", "null", "-0.05", "100.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered frame is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "1234 ") {
		t.Errorf("a Decimal printed as its unscaled storage:\n%s", got)
	}
	if !strings.Contains(got, "Decimal(10, 2)") {
		t.Errorf("the dtype header should show precision and scale:\n%s", got)
	}
}

// TestDecimalSortsAndFilters covers what the Physical() case unlocks. Ordering
// works on the unscaled integers precisely because every value in a column shares
// one scale, so comparing units is comparing values.
func TestDecimalSortsAndFilters(t *testing.T) {
	df, err := prices(t).
		Sort(ursus.Desc(ursus.Col("price")).NullsLast()).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("sorting a Decimal must work now that it has a physical type: %v", err)
	}

	skus, err := df.Column[string]("sku")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range skus.Len() {
		v, _ := skus.Get(i)
		got = append(got, v)
	}
	// 100.00, 12.34, 5.00, -0.05, then the null.
	want := []string{"e", "a", "b", "d", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sort order = %v, want %v\n%s", got, want, df)
	}
}

// TestDecimalMinMaxAndCount: Min, Max, First, Last, Count and
// NUnique all work, because they select or count rather than compute — the value
// they carry keeps its own scale.
func TestDecimalMinMaxAndCount(t *testing.T) {
	df, err := prices(t).
		GroupBy(ursus.Lit(1).Alias("k")).
		Agg(
			ursus.Col("price").Min().Alias("lo"),
			ursus.Col("price").Max().Alias("hi"),
			ursus.Col("price").Count().Alias("n"),
			ursus.Col("price").NUnique().Alias("distinct"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	got := df.String()
	if !strings.Contains(got, "-0.05") || !strings.Contains(got, "100.00") {
		t.Errorf("min/max should keep the Decimal type and scale:\n%s", got)
	}
	n, _, err := df.At[uint64](0, "n")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 { // count skips the null
		t.Errorf("count = %d, want 4\n%s", n, got)
	}
}

// TestDecimalGuards covers the narrowing guards. Each one blocks a path that became
// REACHABLE the moment Decimal got a physical type, and each would return a number
// wrong by a factor of 10^scale rather than merely imprecise.
//
// Each fails with an actionable message. That matters more than usual here: the
// alternative is not an error, it is a plausible wrong number.
//
// sum and mean were guards here until step 69 committed to their precision rule;
// TestDecimalAggregatesMatchTheReferenceEngines holds them now.
func TestDecimalGuards(t *testing.T) {
	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		kind error
		want string
	}{
		{
			// 12.34 * 12.34 = 152.2756, scale 4. Promote returns the operand type
			// unchanged, so the result would be labelled scale 2 and print as
			// 15227.56.
			name: "multiplication changes the scale",
			lf:   func() *ursus.LazyFrame { return prices(t).Select(ursus.Col("price").Mul(ursus.Col("price"))) },
			kind: uerr.ErrType,
			want: "scale",
		},
		{
			// Would hand back the unscaled integer: 12.34 as 1234.
			//
			// The refusal has moved twice. Step 52 took it from the kernel to plan
			// time, when CanCast stopped promising what the kernel refused. Step 69
			// implemented the cast, exact or refused, so it is a VALUE refusal now:
			// 12.34 has a fraction an integer cannot hold, while 5.00 would convert.
			name: "cast to Int64 refuses a fraction rather than unscaling",
			lf:   func() *ursus.LazyFrame { return prices(t).Select(ursus.Col("price").Cast(dtype.Int64)) },
			kind: uerr.ErrValue,
			want: "value 12.34 at row 0 is not representable as Int64",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf().Collect(t.Context())
			if err == nil {
				t.Fatal("expected an error; the unscaled integer would have leaked as a plausible wrong number")
			}
			if !errors.Is(err, c.kind) {
				t.Errorf("kind = %v, want %v: %v", err, c.kind, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("message should explain %q:\n%v", c.want, err)
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("must be a user-facing refusal, not a reported ursus bug:\n%v", err)
			}
		})
	}
}

// TestDecimalAdditionIsExact: + and - ARE allowed, because equal scales mean the
// unscaled integers add directly. Refusing them alongside * would be over-broad.
func TestDecimalAdditionIsExact(t *testing.T) {
	df, err := prices(t).
		Select(ursus.Col("price").Add(ursus.Col("price")).Alias("doubled")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("Decimal + Decimal at the same scale is exact and must be allowed: %v", err)
	}
	if got := df.Schema().Field(0).Type; got != dtype.Decimal(10, 2) {
		t.Errorf("output type = %s, want Decimal(10, 2)", got)
	}
	if !strings.Contains(df.String(), "24.68") {
		t.Errorf("12.34 + 12.34 should be 24.68:\n%s", df)
	}
}

// A refusal must not recommend something the library refuses.
//
// Five Decimal refusals across three files ended with "cast to Float64 first if
// approximate arithmetic is acceptable", and CanCast permits exactly one cast out of
// a Decimal — to String. Every refusal was correct and every remedy was impossible,
// which is the shape step 63 found in the as-of join when a not-null column was told
// to filter its nulls out. The refusals are the engine being careful; advice that
// cannot be followed turns that care into a dead end.

// castTargetsNamed returns every type a message recommends casting to.
//
// It matches the two forms the codebase actually writes — "cast to Float64" and
// ".Cast(ursus.Float64)" — over the types a numeric refusal might plausibly name.
// A name it does not know is skipped, which is why the test below asserts the
// scanner found something rather than trusting an empty result.
func castTargetsNamed(msg string) []dtype.DataType {
	known := map[string]dtype.DataType{
		"Float64": dtype.Float64, "Float32": dtype.Float32,
		"Int64": dtype.Int64, "Int32": dtype.Int32, "Int16": dtype.Int16,
		"Int8": dtype.Int8, "Int128": dtype.Int128,
		"Uint64": dtype.Uint64, "Uint32": dtype.Uint32,
		"Bool": dtype.Bool, "String": dtype.String,
	}
	var out []dtype.DataType
	for name, dt := range known {
		if strings.Contains(msg, "cast to "+name) ||
			strings.Contains(msg, ".Cast(ursus."+name+")") {
			out = append(out, dt)
		}
	}
	return out
}

func TestDecimalRefusalsRecommendOnlyPossibleCasts(t *testing.T) {
	d := dtype.Decimal(10, 2)
	price := ursus.Col("price")

	sel := func(e ursus.Expr) func() *ursus.LazyFrame {
		return func() *ursus.LazyFrame { return prices(t).Select(e) }
	}
	cases := map[string]func() *ursus.LazyFrame{
		"mul":      sel(price.Mul(price)),
		"div":      sel(price.Div(price)),
		"floor":    sel(price.Floor()),
		"ceil":     sel(price.Ceil()),
		"sign":     sel(price.Sign()),
		"sqrt":     sel(price.Sqrt()),
		"round":    sel(price.Round(1)),
		"cast_i64": sel(price.Cast(dtype.Int64)),
	}

	refused := 0
	for name, build := range cases {
		_, err := build().Collect(t.Context())
		if err == nil {
			continue // the operation is supported; not this test's business
		}
		refused++
		for _, to := range castTargetsNamed(err.Error()) {
			if !dtype.CanCast(d, to) {
				t.Errorf("%s: the refusal recommends casting %s to %s, and CanCast "+
					"refuses that cast — the remedy does not exist:\n%v",
					name, d, to, err)
			}
		}
	}
	// Anti-vacuity. A sweep where nothing refused, or where every message was
	// scanned and none named a type, proves nothing either way.
	//
	// Every one of these refuses today. The six computing aggregates left this list
	// when step 69 implemented them, which is the move this floor exists to force:
	// an operation that starts working leaves the list on purpose, rather than
	// quietly thinning the sweep.
	if refused < len(cases) {
		t.Errorf("only %d of %d operations refused; the fixture has stopped "+
			"reaching the Decimal guards", refused, len(cases))
	}
}

// TestCastScannerFindsARecommendation is the control for the test above.
//
// After the fix no Decimal refusal names a cast target at all, so that sweep would
// pass against a scanner that always returned nothing. This drives a refusal that
// SHOULD name one — Int128 arithmetic, where "cast to Int64 or Float64 first" is
// sound because CanCast permits both — and asserts the scanner sees it.
func TestCastScannerFindsARecommendation(t *testing.T) {
	_, err := ursus.Frame(ursus.Values("a", []int64{1, 2})).
		Select(ursus.Col("a").Cast(dtype.Int128).Mul(ursus.Col("a").Cast(dtype.Int128))).
		Collect(t.Context())
	if err == nil {
		t.Fatal("Int128 multiplication is implemented now; this control needs a new op")
	}
	found := castTargetsNamed(err.Error())
	if len(found) == 0 {
		t.Fatalf("the scanner found no cast recommendation in:\n%v", err)
	}
	for _, to := range found {
		if !dtype.CanCast(dtype.Int128, to) {
			t.Errorf("Int128 refusal recommends %s, which CanCast refuses:\n%v", to, err)
		}
	}
}

// TestDecimalRemedyIsFollowable does what the refusal now tells a caller to do, and
// checks the answer. Advice nobody has run is a claim.
//
// Until step 69 the advice was to read the unscaled integers with Int128Value and
// apply the scale by hand, because the cast every refusal used to recommend did not
// exist. It does now, so the advice is the cast again — and multiplication is the
// refusal followed, because it stays refused: its precision rule is still undecided.
func TestDecimalRemedyIsFollowable(t *testing.T) {
	price := ursus.Col("price")
	_, err := prices(t).Select(price.Mul(price)).Collect(t.Context())
	if err == nil {
		t.Fatal("Decimal multiplication is implemented; follow a refusal that still exists")
	}
	if !strings.Contains(err.Error(), ".Cast(ursus.Float64)") {
		t.Fatalf("the refusal no longer names the cast:\n%v", err)
	}

	f := price.Cast(ursus.Float64)
	df, err := prices(t).Select(f.Mul(f).Alias("sq")).Collect(t.Context())
	if err != nil {
		t.Fatalf("the recommended cast does not work: %v", err)
	}
	sq, err := df.Column[float64]("sq")
	if err != nil {
		t.Fatal(err)
	}
	// The float64 product of the double nearest 12.34 with itself — computed at run
	// time, because a Go constant expression would be exact. A missing scale would
	// give 1234 * 1234 = 1522756.
	x := 12.34
	if v, _ := sq.Get(0); v != x*x {
		t.Errorf("12.34 squared = %v, want %v", v, x*x)
	}
}

// TestFloatToDecimalIsExactlyWhenRoundIsANoOp: a strict cast from a float keeps
// every digit or refuses, and the condition is exactly "rounding it to the target's
// scale changes nothing". So the rounding a caller may want is one they write — and
// Round(2) then Cast gives the digits Polars and DuckDB give, 2.675 -> 2.68, because
// both are Round's own arithmetic. The oracle is the public Round, not a copy of it.
func TestFloatToDecimalIsExactlyWhenRoundIsANoOp(t *testing.T) {
	floats := []float64{0, 0.1, 0.12, 0.123, 2.675, 1.005, 12.34, -0.05, 1.0 / 3,
		99999999.99, 999999999.99, 123456.789}
	var kept, refused int
	for _, s := range []int{0, 1, 2, 3} {
		dt := ursus.Decimal(10, uint8(s))
		f := ursus.Col("f")
		df, err := ursus.Frame(ursus.Values("f", floats)).Select(
			f.Round(s).Alias("r"),
			f.CastLossy(dt).Alias("d"),
			f.CastLossy(dt).Cast(ursus.Float64).Alias("back"),
		).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		r, _ := df.Column[float64]("r")
		back, _ := df.Column[float64]("back")
		for i, v := range floats {
			rv, _ := r.Get(i)
			bv, ok := back.Get(i)
			inRange := math.Abs(v) < math.Pow10(10-s)
			if want := rv == v && inRange; ok != want {
				t.Errorf("%v -> %s: kept = %v, but Round(%d) == v is %v", v, dt, ok, s, rv == v)
				continue
			}
			if ok && bv != v {
				t.Errorf("%v -> %s -> Float64 = %v, want it back unchanged", v, dt, bv)
			}
			if ok {
				kept++
			} else {
				refused++
			}
		}
	}
	if kept < 10 || refused < 10 {
		t.Fatalf("%d kept, %d refused — the sweep has gone vacuous", kept, refused)
	}

	df, err := ursus.Frame(ursus.Values("f", []float64{2.675, 1.005})).
		Select(ursus.Col("f").Round(2).Cast(ursus.Decimal(10, 2))).Collect(t.Context())
	if err != nil {
		t.Fatalf("Round(2) then Cast must convert: %v", err)
	}
	for i, want := range []string{"2.68", "1.00"} {
		if got := df.String(); !strings.Contains(got, want) {
			t.Errorf("row %d: want %s in\n%s", i, want, got)
		}
	}
	_, err = ursus.Frame(ursus.Values("f", []float64{2.675})).
		Select(ursus.Col("f").Cast(ursus.Decimal(10, 2))).Collect(t.Context())
	if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), ".Round(2)") {
		t.Errorf("a strict cast of 2.675 should refuse and name .Round(2): %v", err)
	}
}

// TestCastToADecimalThatCannotExist: dtype.Decimal returns no error, so
// Decimal(200, 3) is a value a caller can hold, and the cast is where it is refused —
// at plan time, saying why.
func TestCastToADecimalThatCannotExist(t *testing.T) {
	_, err := prices(t).Select(ursus.Col("price").Cast(ursus.Decimal(200, 3))).
		CollectSchema(t.Context())
	if !errors.Is(err, ursus.ErrType) || !strings.Contains(err.Error(), "1 to 38 digits") {
		t.Errorf("Decimal(200, 3) = %v, want ErrType naming the precision range", err)
	}
}

// TestDecimalAggregatesMatchTheReferenceEngines pins every computing aggregate over
// a Decimal to the answer Polars 1.44, DuckDB 1.5 and PyArrow 25 were MEASURED to
// give on this fixture — 12.34, 5.00, null, -0.05, 100.00 — and records where they
// disagree and which of them ursus follows.
//
// These are absolute values, and that is the point: a scale applied twice, or not
// at all, is invisible to any test that compares a Decimal aggregate with another
// path through the same reader. 100x wrong looks exactly like right to a
// consistency check.
func TestDecimalAggregatesMatchTheReferenceEngines(t *testing.T) {
	p := ursus.Col("price")
	df, err := prices(t).GroupBy().Agg(
		p.Sum().Alias("sum"),
		p.Mean().Alias("mean"),
		p.Median().Alias("median"),
		p.Quantile(0.5, ursus.InterpLinear).Alias("q50"),
		p.Var(1).Alias("var"),
		p.Std(1).Alias("std"),
		p.Product().Alias("product"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// sum: Decimal(38, 2) in all three engines, and exact.
	if got := df.Schema().String(); !strings.Contains(got, "sum: Decimal(38, 2)") {
		t.Errorf("schema = %s, want sum: Decimal(38, 2)", got)
	}
	sum, _, err := df.At[ursus.Int128Value](0, "sum")
	if err != nil {
		t.Fatal(err)
	}
	if sum.String() != "11729" {
		t.Errorf("sum = %s unscaled, want 11729 (117.29)", sum)
	}

	// The rest are Float64: Polars returns Float64 for all of them; DuckDB for all
	// but median. mean is the ONE division of the exact sum, so it is exactly the
	// double nearest 29.3225 — PyArrow's Decimal answer, 29.32, truncates.
	for _, c := range []struct {
		col  string
		want float64
		tol  float64
	}{
		{"mean", 29.3225, 0},
		{"median", 8.67, 0},
		{"q50", 8.67, 0},
		// Sample variance: 6738.042075 / 3. PyArrow reports the population
		// variance, 1684.51, by default; Polars and DuckDB agree with this.
		{"var", 2246.014025, 1e-12},
		{"std", math.Sqrt(2246.014025), 1e-12},
		// -308.5, which is DuckDB's answer. Polars returns Decimal -308.00 and
		// PyArrow Decimal -309.00: keeping the input's scale for a product is the
		// mistake, since its scale is really 4*2.
		{"product", -308.5, 1e-12},
	} {
		got, _, err := df.At[float64](0, c.col)
		if err != nil {
			t.Fatalf("%s: %v", c.col, err)
		}
		if math.Abs(got-c.want) > c.tol*math.Abs(c.want) {
			t.Errorf("%s = %v, want %v", c.col, got, c.want)
		}
	}
}

// TestDecimalSumOfNothingIsNull: sku "c" holds the one null price, so its group
// has nothing to sum. That is NULL, as for every other sum in ursus, and as in
// DuckDB and PyArrow — Polars answers 0.00, which cannot be told apart from values
// that summed to zero.
func TestDecimalSumOfNothingIsNull(t *testing.T) {
	p := ursus.Col("price")
	df, err := prices(t).Filter(ursus.Col("sku").Eq(ursus.Lit("c"))).
		GroupBy(ursus.Col("sku")).Agg(p.Sum().Alias("sum"), p.Mean().Alias("mean")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := df.At[ursus.Int128Value](0, "sum"); ok {
		t.Error("sum over only a null is not null")
	}
	if _, ok, _ := df.At[float64](0, "mean"); ok {
		t.Error("mean over only a null is not null")
	}
}

// TestDecimalCumSumIsExact: cum_sum borrows sum's rule, so it is Decimal(38, 2) and
// exact — and its last row equals sum by construction. Before its gate keyed on
// the PHYSICAL type, a Decimal output fell to the float path and panicked.
func TestDecimalCumSumIsExact(t *testing.T) {
	df, err := prices(t).Select(ursus.Col("price").CumSum(false).Alias("run")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().String(); got != "{run: Decimal(38, 2)}" {
		t.Errorf("schema = %s", got)
	}
	run, err := df.Column[ursus.Int128Value]("run")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"1234", "1734", "null", "1729", "11729"} {
		v, ok := run.Get(i)
		got := "null"
		if ok {
			got = v.String()
		}
		if got != want {
			t.Errorf("row %d = %s, want %s", i, got, want)
		}
	}
}

// decimals38 is one Decimal(38, 0) column, "v".
func decimals38(t *testing.T, vals ...string) *ursus.LazyFrame {
	t.Helper()
	dt := dtype.Decimal(38, 0)
	schema, err := dtype.NewSchema(dtype.Field{Name: "v", Type: dt})
	if err != nil {
		t.Fatal(err)
	}
	u := make([]i128.Int128, len(vals))
	for i, s := range vals {
		u[i] = i128Of(t, s)
	}
	b, err := data.NewBatch(schema, []*data.Column{data.NewFixed("v", dt, u, bitmap.AllSet(len(u)))})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	return ursus.Scan(src)
}

// TestDecimalSumNeedsAThirtyNinthDigitIsRefused: 10^38 - 1 is the largest
// Decimal(38, 0), and it fits 128 bits with room to spare — so the refusal is the
// PRECISION's, not the accumulator's. The mean of the same rows is not refused: a
// Float64 exists when the sum would not.
func TestDecimalSumNeedsAThirtyNinthDigitIsRefused(t *testing.T) {
	const nines = "99999999999999999999999999999999999999" // 10^38 - 1
	v := ursus.Col("v")

	_, err := decimals38(t, nines, "1").GroupBy().Agg(v.Sum()).Collect(t.Context())
	if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "38 digits") {
		t.Errorf("sum to 10^38 = %v, want ErrValue naming the 38 digits", err)
	}
	_, err = decimals38(t, nines, "1").Select(v.CumSum(false)).Collect(t.Context())
	if !errors.Is(err, ursus.ErrValue) {
		t.Errorf("cum_sum to 10^38 = %v, want ErrValue", err)
	}

	// The controls: the largest total is kept, and so is a sum that passes 10^38
	// on the way and comes back.
	for _, vals := range [][]string{{nines}, {nines, "1", "-1"}} {
		got, err := only(t, decimals38(t, vals...).GroupBy().Agg(v.Sum()),
			func(x i128.Int128) string { return x.String() })
		if err != nil || got != nines {
			t.Errorf("sum %v = %s, %v; want %s", vals, got, err, nines)
		}
	}
	mean, err := only(t, decimals38(t, nines, nines).GroupBy().Agg(v.Mean()),
		func(x float64) string { return strconv.FormatFloat(x, 'g', -1, 64) })
	if err != nil || mean != "1e+38" {
		t.Errorf("mean of two 10^38-1 = %s, %v; want 1e+38 — a Float64 mean exists", mean, err)
	}
}

// TestDecimalListAggregatesFollowTheRule: list.sum and list.mean borrow sum's and
// mean's rules, so a List(Decimal) — which a Parquet file can hold — sums exactly
// to Decimal(38, s) and averages to the nearest Float64.
func TestDecimalListAggregatesFollowTheRule(t *testing.T) {
	dt := dtype.Decimal(10, 2)
	lt := dtype.List(dt)
	schema, err := dtype.NewSchema(dtype.Field{Name: "l", Type: lt})
	if err != nil {
		t.Fatal(err)
	}
	child := data.NewFixed("item", dt, []i128.Int128{dec(1234), dec(500), dec(-5)}, bitmap.AllSet(3))
	b, err := data.NewBatch(schema, []*data.Column{
		data.NewList("l", []int32{0, 2, 3}, child, bitmap.AllSet(2)),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	l := ursus.Col("l").List()
	df, err := ursus.Scan(src).Select(l.Sum().Alias("s"), l.Mean().Alias("m")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().String(); !strings.Contains(got, "s: Decimal(38, 2)") ||
		!strings.Contains(got, "m: Float64") {
		t.Errorf("schema = %s", got)
	}
	for i, c := range []struct {
		sum  string
		mean float64
	}{{"1734", 8.67}, {"-5", -0.05}} {
		s, _, _ := df.At[ursus.Int128Value](i, "s")
		m, _, _ := df.At[float64](i, "m")
		if s.String() != c.sum || m != c.mean {
			t.Errorf("row %d: sum %s, mean %v; want %s, %v", i, s, m, c.sum, c.mean)
		}
	}
}
