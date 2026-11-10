package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

func nullable[T ursus.Literal](name string, vals []T, ok []bool) *ursus.Column {
	return ursus.ValuesNullable(name, vals, ok)
}

// The fills' values are the fill group's, Polars 2.1.0's (oracle_fill_test.go): a
// literal's fill, fill_nan, the forward and backward fills and their limits, the
// strategies, a frame's fill_null, clip, is_close, diff and pct_change. What stays
// here is what the weak-literal machinery promises of types and plans, and the
// differentials.

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
// exists to avoid. Polars 2.1 widens to the narrowest type that holds the literal, an
// Int16, where ursus falls back to Int64: the fill group's fill_past_the_type records
// Polars' answer as a finding, so fill.go's rule is pinned here.
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
	// Both are the group's own maximum, 7, whatever the type.
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

// TestForwardFillLimitsAreNotCollapsed is the WinParams.args pin.
//
// Without an args case the limit lives nowhere in the rendering, so ForwardFill(1)
// and ForwardFill(3) become one string, share one window temporary, and one
// silently returns the other's answer (the fill group's forward_fill_limits_1_and_3
// holds the two answers). That is the third occurrence of this exact shape — the
// quantile parameters in step 7, the partition keys in step 8.
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

// TestFillStrategies: the seven strategies' types. The type is preserved for six;
// only the mean widens, to Float64 (the fill group's fill_strategy_mean records
// Polars 2.1 keeping the integer type, a finding).
func TestFillStrategies(t *testing.T) {
	f := ursus.Frame(nullable("v", []int32{0, 2, 0, 8}, []bool{false, true, false, true}))

	s, err := f.Select(
		ursus.Col("v").FillNull(ursus.FillZero).Alias("zero"),
		ursus.Col("v").FillNull(ursus.FillOne).Alias("one"),
		ursus.Col("v").FillNull(ursus.FillForward).Alias("fwd"),
		ursus.Col("v").FillNull(ursus.FillBackward).Alias("bwd"),
		ursus.Col("v").FillNull(ursus.FillMin).Alias("min"),
		ursus.Col("v").FillNull(ursus.FillMax).Alias("max"),
		ursus.Col("v").FillNull(ursus.FillMean).Alias("mean"),
	).CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"zero", "one", "fwd", "bwd", "min", "max"} {
		if fl, _ := s.ByName(col); fl.Type != ursus.Int32 {
			t.Errorf("%s is %s, want Int32", col, fl.Type)
		}
	}
	if fl, _ := s.ByName("mean"); fl.Type != ursus.Float64 {
		t.Errorf("mean is %s, want Float64", fl.Type)
	}
}

// TestFrameFillNullRestrictsColumns: the no-subset form fills what the value can
// fill (the fill group's frame_fill_null_0 holds the cells), and Explain shows which
// columns those were rather than skipping silently.
func TestFrameFillNullRestrictsColumns(t *testing.T) {
	f := ursus.Frame(
		nullable("i", []int32{0, 2}, []bool{false, true}),
		nullable("f", []float64{0, 1.5}, []bool{false, true}),
		nullable("s", []string{"", "b"}, []bool{false, true}),
	)
	// The types are unchanged, which is what the weak literal buys at frame level.
	s, err := f.FillNull(0).CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if fl, _ := s.ByName("i"); fl.Type != ursus.Int32 {
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

// TestDiffAndPctChange: the two shift mentions become ONE window (the fill group's
// diff_and_pct_change holds the values).
func TestDiffAndPctChange(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{10, 20, 40, 40}))
	// One window temporary, not two: the dedup keys on the rendered expression.
	pl, err := f.Select(ursus.Col("v").Diff(1).Alias("d")).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(pl, "__win") > 2 {
		t.Errorf("Diff should need ONE window temporary; the plan mentions more:\n%s", pl)
	}
}

// TestExprDropNullsIsRefused: refused while the plan is built, so CollectSchema and
// Explain fail too rather than printing a plan that cannot run. Since step 252
// DropNulls is an expression, a column of its own length, in an aggregation, and
// since steps 258 and 259 in Select and WithColumns; in Filter it is still refused,
// naming the frame-level form.
func TestExprDropNullsIsRefused(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []float64{1}))
	for _, c := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"DropNulls", ursus.Col("v").DropNulls(), "lf.DropNulls"},
		{"DropNans", ursus.Col("v").DropNans(), "IsNotNan().Or("},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.Filter(c.e.Gt(0)).CollectSchema(t.Context())
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !errors.Is(err, uerr.ErrUnsupported) {
				t.Errorf("want an unsupported error, got %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the message must name the working spelling %q:\n%v", c.want, err)
			}
			if _, err := f.Filter(c.e.Gt(0)).Explain(t.Context()); err == nil {
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
	for _, run := range []struct{ bs, th int }{{1, 1}, {3, 4}, {8192, 4}} {
		bs, th := run.bs, run.th
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

// TestClipPreservesType is the inconsistency weak literals were built to remove.
//
// Clip's bounds were lifted STRONGLY, so a Go 0 and 100 arrived as Int64 and
// ordinary promotion widened the whole conditional to hold them. Col("i32").Clip(0,
// 100) came out Int32 -> Int64 and Col("u64").Clip(0, 100) came out Uint64 ->
// Int128 — a schema change in exchange for a bound the column could already hold.
// FillNullWith(0) has preserved the type since step 11 and Clip did not, and both
// read as "a Go scalar bound on a column".
//
// The clip tests of old never asserted an output type, which is how it survived;
// the values are the fill group's clip cases now.
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
}
