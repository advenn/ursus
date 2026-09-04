package ursus_test

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"ursus"
	"ursus/internal/uerr"
	"ursus/ursustest"
)

func nullable[T ursus.Literal](name string, vals []T, ok []bool) *ursus.Column {
	return ursus.ValuesNullable(name, vals, ok)
}

// TestWeakLiteralsDoNotWiden is the point of the weak-literal machinery.
//
// Col("i32").FillNull(FillZero) contains no literal in the source at all, so a
// widening to Int64 would have nothing in the query to explain it.
func TestWeakLiteralsDoNotWiden(t *testing.T) {
	f := ursus.Frame(
		nullable("i8", []int8{0, 3}, []bool{false, true}),
		nullable("i32", []int32{0, 3}, []bool{false, true}),
		nullable("u64", []uint64{0, 3}, []bool{false, true}),
		nullable("f32", []float32{0, 1.5}, []bool{false, true}),
	)
	s, err := f.Select(
		ursus.Col("i8").FillNullWith(0).Alias("i8"),
		ursus.Col("i32").FillNullWith(0).Alias("i32"),
		ursus.Col("u64").FillNullWith(0).Alias("u64"),
		ursus.Col("f32").FillNullWith(0.0).Alias("f32"),
		ursus.Col("i32").FillNull(ursus.FillZero).Alias("strategy"),
	).CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []ursus.DataType{
		ursus.Int8, ursus.Int32, ursus.Uint64, ursus.Float32, ursus.Int32,
	}
	for i, w := range want {
		if got := s.Field(i).Type; got != w {
			t.Errorf("%s is %s, want %s", s.Field(i).Name, got, w)
		}
	}

	// Uint64 is the case that changes most: promotion against an Int64 literal
	// routes it through Int128, because that is the only type holding both ranges.
	if s.Field(2).Type != ursus.Uint64 {
		t.Errorf("u64 fill produced %s; ordinary promotion would give Int128",
			s.Field(2).Type)
	}

	// And the values are right, not merely the types.
	df, err := f.Select(ursus.Col("i32").FillNullWith(-1).Alias("v")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := df.At[int32](0, "v"); got != -1 {
		t.Errorf("filled value = %d, want -1", got)
	}
	if got, _, _ := df.At[int32](1, "v"); got != 3 {
		t.Errorf("unfilled value = %d, want 3", got)
	}
}

// TestStrongLiteralsStillWiden is the fence.
//
// Weakness must not leak out of the fill family: arithmetic keeps widening,
// because addition can overflow and filling a null cannot, and a hand-written
// Coalesce or Otherwise keeps today's behaviour because the caller wrote the
// literal themselves.
func TestStrongLiteralsStillWiden(t *testing.T) {
	f := ursus.Frame(
		nullable("i32", []int32{0, 3}, []bool{false, true}),
		nullable("f32", []float32{0, 1.5}, []bool{false, true}),
	)
	s, err := f.Select(
		ursus.Col("i32").Add(1).Alias("add"),
		ursus.Col("f32").Add(1.0).Alias("addf"),
		ursus.Coalesce(ursus.Col("i32"), ursus.Lit(0)).Alias("coalesce"),
		ursus.When(ursus.Col("i32").Gt(int32(0))).Then(ursus.Col("i32")).
			Otherwise(0).Alias("otherwise"),
		// An Expr operand is used as written, so this is the escape hatch back to
		// the promotion for anyone who wants it.
		ursus.Col("i32").FillNullWith(ursus.Lit(0)).Alias("explicit"),
	).CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []ursus.DataType{
		ursus.Int64, ursus.Float64, ursus.Int64, ursus.Int64, ursus.Int64,
	} {
		if got := s.Field(i).Type; got != want {
			t.Errorf("%s is %s, want %s — weakness leaked out of the fill family",
				s.Field(i).Name, got, want)
		}
	}
}

// TestWeakLiteralThatDoesNotFit: falling back is right, truncating is not.
// int8(5000) is -120, which is the silent wrong answer this whole mechanism
// exists to avoid.
func TestWeakLiteralThatDoesNotFit(t *testing.T) {
	f := ursus.Frame(
		nullable("i8", []int8{0, 3}, []bool{false, true}),
		nullable("u32", []uint32{0, 3}, []bool{false, true}),
	)
	df, err := f.Select(
		ursus.Col("i8").FillNullWith(int64(5000)).Alias("big"),
		ursus.Col("u32").FillNullWith(int64(-1)).Alias("neg"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Field(0).Type; got != ursus.Int64 {
		t.Errorf("big is %s, want Int64 — it must fall back rather than truncate", got)
	}
	if got, _, _ := df.At[int64](0, "big"); got != 5000 {
		t.Errorf("filled value = %d, want 5000 (int8 would give -120)", got)
	}
	if got, _, _ := df.At[int64](0, "neg"); got != -1 {
		t.Errorf("filled value = %d, want -1 (uint32 would give 4294967295)", got)
	}
}

// TestWeakAndStrongDoNotShareATemporary is the String() collision guard.
//
// The two fills resolve to different TYPES, so if they rendered alike the
// aggregate dedup would collapse them into one spec and the batch's column type
// would disagree with the schema the plan promised. That is step 7's quantile bug
// in a new setting.
func TestWeakAndStrongDoNotShareATemporary(t *testing.T) {
	f := ursus.Frame(
		ursus.Values("g", []string{"a", "a"}),
		nullable("v", []int32{0, 7}, []bool{false, true}),
	)
	df, err := f.GroupBy(ursus.Col("g")).Agg(
		ursus.Col("v").FillNullWith(0).Max().Alias("weak"),
		ursus.Coalesce(ursus.Col("v"), ursus.Lit(0)).Max().Alias("strong"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	w, _ := df.Schema().ByName("weak")
	s, _ := df.Schema().ByName("strong")
	if w.Type != ursus.Int32 || s.Type != ursus.Int64 {
		t.Fatalf("weak=%s strong=%s, want Int32 and Int64 — they collapsed into one "+
			"temporary", w.Type, s.Type)
	}
	if a, _, _ := df.At[int32](0, "weak"); a != 7 {
		t.Errorf("weak max = %d, want 7", a)
	}
	if b, _, _ := df.At[int64](0, "strong"); b != 7 {
		t.Errorf("strong max = %d, want 7", b)
	}

	// And they render differently, which is what makes the above true.
	pl, err := f.Select(
		ursus.Col("v").FillNullWith(0).Alias("weak"),
		ursus.Coalesce(ursus.Col("v"), ursus.Lit(0)).Alias("strong"),
	).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pl, "weak_lit(0:Int64)") || !strings.Contains(pl, "lit(0)") {
		t.Errorf("a weak literal must not render like a strong one:\n%s", pl)
	}
}

// TestFillNanLeavesNullsAlone pins the thing that looks accidental: IsNotNan on a
// null row is NULL, and a null mask takes NEITHER branch, so a null keeps its null
// and the fill value never lands on it.
func TestFillNanLeavesNullsAlone(t *testing.T) {
	f := ursus.Frame(nullable("f", []float64{1, 0, 0}, []bool{true, true, false}))
	df, err := f.Select(
		// 0/0 is NaN, which is a value; the third row is null, which is not.
		ursus.Col("f").Div(ursus.Col("f")).FillNan(-1.0).Alias("v"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := df.At[float64](0, "v"); got != 1 {
		t.Errorf("row 0 = %v, want 1", got)
	}
	if got, _, _ := df.At[float64](1, "v"); got != -1 {
		t.Errorf("row 1 (NaN) = %v, want -1", got)
	}
	if _, ok, _ := df.At[float64](2, "v"); ok {
		t.Error("row 2 was null and must stay null; FillNan must not touch it")
	}
}

// TestForwardAndBackwardFill, including the limit and the leading-null case.
func TestForwardAndBackwardFill(t *testing.T) {
	f := ursus.Frame(nullable("v",
		[]int64{0, 1, 0, 0, 4, 0},
		[]bool{false, true, false, false, true, false}))

	df, err := f.Select(
		ursus.Col("v").ForwardFill(0).Alias("ff"),
		ursus.Col("v").ForwardFill(1).Alias("ff1"),
		ursus.Col("v").BackwardFill(0).Alias("bf"),
		ursus.Col("v").FillNull(ursus.FillForward).Alias("strategy"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	read := func(col string) []string {
		out := make([]string, df.Height())
		for i := range out {
			if v, ok, _ := df.At[int64](i, col); ok {
				out[i] = string(rune('0' + v))
			} else {
				out[i] = "-"
			}
		}
		return out
	}
	// A leading run of nulls has nothing to carry, so it stays null.
	if got, want := read("ff"), []string{"-", "1", "1", "1", "4", "4"}; !slices.Equal(got, want) {
		t.Errorf("forward fill = %v, want %v", got, want)
	}
	// The limit stops the carry after one row.
	if got, want := read("ff1"), []string{"-", "1", "1", "-", "4", "4"}; !slices.Equal(got, want) {
		t.Errorf("forward fill limit 1 = %v, want %v", got, want)
	}
	if got, want := read("bf"), []string{"1", "1", "4", "4", "4", "-"}; !slices.Equal(got, want) {
		t.Errorf("backward fill = %v, want %v", got, want)
	}
	if got, want := read("strategy"), read("ff"); !slices.Equal(got, want) {
		t.Errorf("FillForward = %v, want the same as ForwardFill(0) %v", got, want)
	}
}

// TestForwardFillLimitsAreNotCollapsed is the WinParams.args pin.
//
// Without an args case the limit lives nowhere in the rendering, so ForwardFill(1)
// and ForwardFill(3) become one string, share one window temporary, and one
// silently returns the other's answer. That is the third occurrence of this exact
// shape — the quantile parameters in step 7, the partition keys in step 8.
func TestForwardFillLimitsAreNotCollapsed(t *testing.T) {
	f := ursus.Frame(nullable("v",
		[]int64{0, 1, 0, 0, 0, 0},
		[]bool{false, true, false, false, false, false}))

	pl, err := f.Select(
		ursus.Col("v").ForwardFill(1).Alias("a"),
		ursus.Col("v").ForwardFill(3).Alias("b"),
	).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pl, "forward_fill(1)") || !strings.Contains(pl, "forward_fill(3)") {
		t.Fatalf("the limit must appear in the rendering:\n%s", pl)
	}

	df, err := f.Select(
		ursus.Col("v").ForwardFill(1).Alias("a"),
		ursus.Col("v").ForwardFill(3).Alias("b"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// Row 3 is two nulls past the value: limit 1 gives up, limit 3 does not.
	if _, ok, _ := df.At[int64](3, "a"); ok {
		t.Error("limit 1 filled a row two nulls past the last value")
	}
	if v, ok, _ := df.At[int64](3, "b"); !ok || v != 1 {
		t.Errorf("limit 3 row 3 = %v ok=%v, want 1 — the two limits collapsed", v, ok)
	}
}

// TestKeylessWindowMatchesGlobalAggregate covers a path with ZERO call sites
// anywhere in the suite before this step, and which FillMin/FillMax/FillMean all
// stand on: `.Over()` with no partition keys is one partition over the whole frame.
func TestKeylessWindowMatchesGlobalAggregate(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{3, 1, 4, 1, 5}))

	global, err := f.GroupBy().Agg(
		ursus.Col("v").Sum().Alias("sum"),
		ursus.Col("v").Min().Alias("min"),
		ursus.Col("v").Max().Alias("max"),
		ursus.Col("v").Mean().Alias("mean"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	over, err := f.Select(
		ursus.Col("v").Sum().Over().Alias("sum"),
		ursus.Col("v").Min().Over().Alias("min"),
		ursus.Col("v").Max().Over().Alias("max"),
		ursus.Col("v").Mean().Over().Alias("mean"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("a keyless window is the whole frame as one partition: %v", err)
	}
	if over.Height() != 5 {
		t.Fatalf("a window puts the answer on EVERY row: height = %d, want 5",
			over.Height())
	}
	for _, col := range []string{"min", "max"} {
		want, _, _ := global.At[int64](0, col)
		for row := range over.Height() {
			got, _, _ := over.At[int64](row, col)
			if got != want {
				t.Errorf("%s row %d = %d, want %d (the global aggregate)", col, row, got, want)
			}
		}
	}
	wantSum, _, _ := global.At[ursus.Int128Value](0, "sum")
	gotSum, _, _ := over.At[ursus.Int128Value](0, "sum")
	if gotSum.String() != wantSum.String() {
		t.Errorf("sum over the whole frame = %s, want %s", gotSum, wantSum)
	}
	wantMean, _, _ := global.At[float64](0, "mean")
	gotMean, _, _ := over.At[float64](0, "mean")
	if gotMean != wantMean {
		t.Errorf("mean over the whole frame = %v, want %v", gotMean, wantMean)
	}
}

// TestFillStrategies checks all seven against hand-computed answers.
func TestFillStrategies(t *testing.T) {
	f := ursus.Frame(nullable("v", []int32{0, 2, 0, 8}, []bool{false, true, false, true}))

	df, err := f.Select(
		ursus.Col("v").FillNull(ursus.FillZero).Alias("zero"),
		ursus.Col("v").FillNull(ursus.FillOne).Alias("one"),
		ursus.Col("v").FillNull(ursus.FillForward).Alias("fwd"),
		ursus.Col("v").FillNull(ursus.FillBackward).Alias("bwd"),
		ursus.Col("v").FillNull(ursus.FillMin).Alias("min"),
		ursus.Col("v").FillNull(ursus.FillMax).Alias("max"),
		ursus.Col("v").FillNull(ursus.FillMean).Alias("mean"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]int32{
		"zero": {0, 2, 0, 8},
		"one":  {1, 2, 1, 8},
		"fwd":  {0, 2, 2, 8}, // row 0 has nothing to carry — checked as null below
		"bwd":  {2, 2, 8, 8},
		"min":  {2, 2, 2, 8},
		"max":  {8, 2, 8, 8},
	}
	for col, vals := range want {
		// The type is preserved for all six; only the mean widens.
		fl, _ := df.Schema().ByName(col)
		if fl.Type != ursus.Int32 {
			t.Errorf("%s is %s, want Int32", col, fl.Type)
		}
		for i, w := range vals {
			got, ok, _ := df.At[int32](i, col)
			if col == "fwd" && i == 0 {
				if ok {
					t.Errorf("fwd row 0 = %d; a leading null has nothing to carry", got)
				}
				continue
			}
			if !ok || got != w {
				t.Errorf("%s row %d = %d ok=%v, want %d", col, i, got, ok, w)
			}
		}
	}
	// A mean is a float even over an integer column.
	if fl, _ := df.Schema().ByName("mean"); fl.Type != ursus.Float64 {
		t.Errorf("mean is %s, want Float64", fl.Type)
	}
	if got, _, _ := df.At[float64](0, "mean"); got != 5 {
		t.Errorf("mean fill = %v, want 5", got)
	}
}

// TestFrameFillNullRestrictsColumns: the no-subset form fills what the value can
// fill, and Explain shows which columns those were rather than skipping silently.
func TestFrameFillNullRestrictsColumns(t *testing.T) {
	f := ursus.Frame(
		nullable("i", []int32{0, 2}, []bool{false, true}),
		nullable("f", []float64{0, 1.5}, []bool{false, true}),
		nullable("s", []string{"", "b"}, []bool{false, true}),
	)
	df, err := f.FillNull(0).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := df.At[int32](0, "i"); got != 0 {
		t.Errorf("i row 0 = %d, want 0", got)
	}
	if got, _, _ := df.At[float64](0, "f"); got != 0 {
		t.Errorf("f row 0 = %v, want 0", got)
	}
	if _, ok, _ := df.At[string](0, "s"); ok {
		t.Error("the String column must be left alone by an integer fill")
	}
	// The types are unchanged, which is what the weak literal buys at frame level.
	if fl, _ := df.Schema().ByName("i"); fl.Type != ursus.Int32 {
		t.Errorf("i became %s", fl.Type)
	}

	pl, err := f.FillNull(0).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pl, `col("i")`) || strings.Contains(pl, `col("s")`) {
		t.Errorf("Explain must show which columns were filled:\n%s", pl)
	}

	// Naming a column the value cannot fill IS an error: the user asked for it.
	if _, err := f.FillNull(0, "s").CollectSchema(t.Context()); err == nil {
		t.Error("naming a String column with an integer fill must be refused")
	}
}

// TestClipSemantics: each edge case falls out of the conditional rather than being
// coded, so each is worth pinning.
func TestClipSemantics(t *testing.T) {
	f := ursus.Frame(nullable("v",
		[]float64{-5, 0, 5, 50, math.NaN()},
		[]bool{true, true, true, true, true}))
	df, err := f.Select(ursus.Col("v").Clip(0.0, 10.0).Alias("c")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{0, 0, 5, 10, math.NaN()} {
		got, ok, _ := df.At[float64](i, "c")
		if !ok {
			t.Fatalf("row %d is null", i)
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Errorf("clip row %d = %v, want %v", i, got, want)
		}
	}

	// A null value clips to null: the comparisons are null, so neither branch runs.
	g := ursus.Frame(nullable("v", []float64{0}, []bool{false}))
	d2, err := g.Select(ursus.Col("v").Clip(0.0, 10.0).Alias("c")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d2.At[float64](0, "c"); ok {
		t.Error("clipping a null must give null")
	}

	// Expr-valued bounds, which is what makes this sugar rather than a Call.
	h := ursus.Frame(
		ursus.Values("v", []int64{5, 5}),
		ursus.Values("lo", []int64{0, 6}),
		ursus.Values("hi", []int64{10, 10}),
	)
	d3, err := h.Select(
		ursus.Col("v").Clip(ursus.Col("lo"), ursus.Col("hi")).Alias("c"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("column-valued bounds: %v", err)
	}
	for i, want := range []int64{5, 6} {
		if got, _, _ := d3.At[int64](i, "c"); got != want {
			t.Errorf("row %d = %d, want %d", i, got, want)
		}
	}
}

// TestIsCloseHandlesTheInfinities: the equality prefix is what makes
// IsClose(Inf, Inf) true — the tolerance term alone gives NaN <= x, which is false.
func TestIsCloseHandlesTheInfinities(t *testing.T) {
	f := ursus.Frame(
		ursus.Values("a", []float64{1.0, 1.0, math.Inf(1), math.Inf(1), math.NaN(), 100}),
		ursus.Values("b", []float64{1.0 + 1e-12, 2.0, math.Inf(1), math.Inf(-1), math.NaN(), 100.0000001}),
	)
	df, err := f.Select(
		ursus.Col("a").IsClose(ursus.Col("b"), 1e-9, 0).Alias("c"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false, true, false, false, true} {
		got, ok, _ := df.At[bool](i, "c")
		if !ok {
			t.Fatalf("row %d is null", i)
		}
		if got != want {
			t.Errorf("is_close row %d = %v, want %v", i, got, want)
		}
	}
}

// TestDiffAndPctChange, including that the two shift mentions become ONE window.
func TestDiffAndPctChange(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{10, 20, 40, 40}))
	df, err := f.Select(
		ursus.Col("v").Diff(1).Alias("d"),
		ursus.Col("v").PctChange(1).Alias("p"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if fl := df.Schema().Field(0); fl.Type != ursus.Int64 {
		t.Errorf("Diff is %s, want the operand's own type", fl.Type)
	}
	if _, ok, _ := df.At[int64](0, "d"); ok {
		t.Error("the first row has nothing to diff against and must be null")
	}
	for i, want := range []int64{10, 20, 0} {
		if got, _, _ := df.At[int64](i+1, "d"); got != want {
			t.Errorf("diff row %d = %d, want %d", i+1, got, want)
		}
	}
	if got, _, _ := df.At[float64](1, "p"); got != 1 {
		t.Errorf("pct_change row 1 = %v, want 1", got)
	}

	// One window temporary, not two: the dedup keys on the rendered expression.
	pl, err := f.Select(ursus.Col("v").Diff(1).Alias("d")).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(pl, "__win") > 2 {
		t.Errorf("Diff should need ONE window temporary; the plan mentions more:\n%s", pl)
	}
}

// TestExprDropNullsIsRefused: refused at BUILD time, so CollectSchema and Explain
// fail too rather than printing a plan that cannot run.
func TestExprDropNullsIsRefused(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{1}))
	for _, c := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"DropNulls", ursus.Col("v").DropNulls(), "lf.DropNulls"},
		{"DropNans", ursus.Col("v").DropNans(), "IsNotNan().Or("},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.Select(c.e).CollectSchema(t.Context())
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !errors.Is(err, uerr.ErrUnsupported) {
				t.Errorf("want an unsupported error, got %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the message must name the working spelling %q:\n%v", c.want, err)
			}
			if _, err := f.Select(c.e).Explain(t.Context()); err == nil {
				t.Error("Explain must fail too; a refusal only Collect notices lets " +
					"Explain print a plan that cannot run")
			}
		})
	}
}

// TestFillIsBatchSizeInvariant: the fills are windows, and a window that is not a
// proper Sink would give different answers at different batch sizes.
func TestFillIsBatchSizeInvariant(t *testing.T) {
	f := ursus.Frame(nullable("v",
		[]int64{0, 1, 0, 0, 4, 0, 0, 9},
		[]bool{false, true, false, false, true, false, false, true}))

	var want *ursus.DataFrame
	for _, bs := range []int{1, 2, 3, 8192} {
		for _, th := range []int{1, 4} {
			df, err := f.Select(
				ursus.Col("v").ForwardFill(0).Alias("ff"),
				ursus.Col("v").BackwardFill(1).Alias("bf"),
				ursus.Col("v").FillNull(ursus.FillMean).Alias("mean"),
				ursus.Col("v").Diff(1).Alias("d"),
			).Collect(t.Context(), ursus.WithBatchSize(bs), ursus.WithThreads(th),
				ursus.WithVerify())
			if err != nil {
				t.Fatalf("bs=%d threads=%d: %v", bs, th, err)
			}
			if want == nil {
				want = df
				continue
			}
			ursustest.AssertFrameEqual(t, df, want)
		}
	}
}

// TestClipPreservesType is the inconsistency weak literals were built to remove.
//
// Clip's bounds were lifted STRONGLY, so a Go 0 and 100 arrived as Int64 and
// ordinary promotion widened the whole conditional to hold them. Col("i32").Clip(0,
// 100) came out Int32 -> Int64 and Col("u64").Clip(0, 100) came out Uint64 ->
// Int128 — a schema change in exchange for a bound the column could already hold.
// FillNullWith(0) has preserved the type since step 11 and Clip did not, and both
// read as "a Go scalar bound on a column".
//
// TestClipSemantics never asserted an output type, which is how it survived.
func TestClipPreservesType(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{5}))
	for _, c := range []struct {
		name string
		to   ursus.DataType
	}{
		{"i8", ursus.Int8}, {"i32", ursus.Int32}, {"i64", ursus.Int64},
		{"u32", ursus.Uint32}, {"u64", ursus.Uint64},
		{"f32", ursus.Float32}, {"f64", ursus.Float64},
	} {
		t.Run(c.name, func(t *testing.T) {
			sch, err := f.Select(ursus.Col("v").Cast(c.to).Alias("x")).
				Select(ursus.Col("x").Clip(0, 100).Alias("c")).
				CollectSchema(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := sch.Field(0).Type; !got.Equal(c.to) {
				t.Errorf("clip(%s, 0, 100) is %s", c.to, got)
			}
		})
	}

	// A bound the column CANNOT hold still widens, which is the fallback weak typing
	// is built around: an Int8 clipped to 5000 is Int64 holding 5000, not Int8 holding
	// -120. Truncating silently is the failure this rule exists to avoid.
	sch, err := f.Select(ursus.Col("v").Cast(ursus.Int8).Alias("x")).
		Select(ursus.Col("x").Clip(0, 5000).Alias("c")).
		CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := sch.Field(0).Type; got.ID() == ursus.Int8.ID() {
		t.Errorf("clip(Int8, 0, 5000) stayed Int8, so 5000 was truncated to %s", got)
	}

	// And the VALUES still clip, so type preservation did not buy correctness away.
	df, err := ursus.Frame(ursus.Values("v", []int32{-5, 5, 500})).
		Select(ursus.Col("v").Clip(0, 100).Alias("c")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int32{0, 5, 100} {
		if got, _, _ := df.At[int32](i, "c"); got != want {
			t.Errorf("row %d = %d, want %d", i, got, want)
		}
	}
}
