package ursus_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFrameNullsAndNaN is C4 of v0.6.7-scope.md (step 301): a fill strategy's limit,
// Polars' fill_null(strategy=, limit=); drop_nans; and interpolate. Every answer is
// Polars 1.44.1's on the same frame but where the step's record says otherwise.
func TestFrameNullsAndNaN(t *testing.T) {
	nan := math.NaN()
	lf := ursus.Frame(
		ursus.ValuesNullable("i", []int64{1, 0, 0, 0, 5}, []bool{true, false, false, false, true}),
		ursus.ValuesNullable("f", []float64{1, nan, 0, 4, nan}, []bool{true, true, false, true, true}),
		ursus.ValuesNullable("s", []string{"a", "", "", "d", ""}, []bool{true, false, false, true, false}),
		ursus.ValuesNullable("g", []float64{nan, 2, 3, 0, 5}, []bool{true, true, true, false, true}),
	)
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: fill_null(strategy="forward", limit=1). NaN is a value, and carried.
		{"forward, limit 1", lf.FillNull(ursus.FillForward.Limit(1)),
			"i,f,s,g\n1,1.0,a,NaN\n1,NaN,a,2.0\n,NaN,,3.0\n,4.0,d,3.0\n5,NaN,d,5.0\n"},
		// Polars: fill_null(strategy="backward", limit=2).
		{"backward, limit 2", lf.FillNull(ursus.FillBackward.Limit(2)),
			"i,f,s,g\n1,1.0,a,NaN\n,NaN,d,2.0\n5,4.0,d,3.0\n5,4.0,d,5.0\n5,NaN,,5.0\n"},
		{"forward, no limit", lf.FillNull(ursus.FillForward),
			"i,f,s,g\n1,1.0,a,NaN\n1,NaN,a,2.0\n1,NaN,a,3.0\n1,4.0,d,3.0\n5,NaN,d,5.0\n"},
		{"forward of one column, limit 1", lf.FillNull(ursus.FillForward.Limit(1), "i").Select(ursus.Col("i")),
			"i\n1\n1\n\n\n5\n"},
		{"an expression's, limit 1", lf.Select(ursus.Col("i").FillNull(ursus.FillForward.Limit(1))),
			"i\n1\n1\n\n\n5\n"},
		{"a value's strategy, limit 2", lf.Select(ursus.Col("i").FillNullWith(ursus.FillBackward.Limit(2))),
			"i\n1\n\n5\n5\n5\n"},
		// Polars: drop_nans(), every float column; a null is kept.
		{"drop_nans", lf.DropNans(), "i,f,s,g\n,,,3.0\n,4.0,d,\n"},
		{"drop_nans of f", lf.DropNans("f"), "i,f,s,g\n1,1.0,a,NaN\n,,,3.0\n,4.0,d,\n"},
		// Polars: an integer holds no NaN, and drops nothing.
		{"drop_nans of i", lf.DropNans("i").Select(ursus.Col("i")), "i\n1\n\n\n\n5\n"},
		// Polars: interpolate(); an integer becomes a Float64, text stays, and a line
		// to a NaN is NaN.
		{"interpolate", lf.Interpolate(), "i,f,s,g\n1.0,1.0,a,NaN\n2.0,NaN,,2.0\n3.0,NaN,,3.0\n4.0,4.0,d,4.0\n5.0,NaN,,5.0\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			// Polars: can only specify `limit` when strategy is 'backward' or 'forward'.
			{"a mean's limit", lf.FillNull(ursus.FillMean.Limit(1)), "a limit is for FillForward and FillBackward, not mean"},
			{"an expression's mean's limit", lf.Select(ursus.Col("i").FillNull(ursus.FillZero.Limit(2))), "not zero"},
			// Polars' limit=0 fills nothing; here it is refused, ForwardFill(0) being no limit.
			{"a limit of 0", lf.FillNull(ursus.FillForward.Limit(0)), "at least 1"},
			{"a limit twice", lf.FillNull(ursus.FillForward.Limit(1).Limit(2)), "set once"},
			// Polars: `is_nan` operation not supported for dtype `str`.
			{"drop_nans of text", lf.DropNans("s"), `column "s" is String, which has no NaN`},
			{"drop_nans of no column", lf.DropNans("h"), `unknown column "h"`},
		} {
			_, err := tc.q.Collect(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("names", func(t *testing.T) {
		for s, want := range map[ursus.FillStrategy]string{
			ursus.FillForward: "forward", ursus.FillForward.Limit(3): "forward(limit=3)",
			ursus.FillBackward.Limit(1): "backward(limit=1)", ursus.FillMean: "mean",
		} {
			if got := s.String(); got != want {
				t.Errorf("%d: %q, want %q", uint64(s), got, want)
			}
		}
	})

	t.Run("plan", func(t *testing.T) {
		// A frame with no float column is not filtered, and one with nothing to
		// interpolate is not rebuilt.
		ints := ursus.Frame(ursus.Values("i", []int64{1, 2}), ursus.Values("s", []string{"a", "b"}))
		want, err := ints.Explain(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		texts := ints.Select(ursus.Col("s"))
		textPlan, err := texts.Explain(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for name, tc := range map[string]struct {
			q    *ursus.LazyFrame
			want string
		}{
			"drop_nans of no float": {ints.DropNans(), want},
			"interpolate of text":   {texts.Interpolate(), textPlan},
		} {
			got, err := tc.q.Explain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("%s: the plan changed:\n%s", name, got)
			}
		}
	})

	t.Run("eager", func(t *testing.T) {
		df, err := lf.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for name, tc := range map[string]struct {
			got  func() (*ursus.DataFrame, error)
			want *ursus.LazyFrame
		}{
			"DropNans":    {func() (*ursus.DataFrame, error) { return df.DropNans(t.Context(), "f") }, lf.DropNans("f")},
			"Interpolate": {func() (*ursus.DataFrame, error) { return df.Interpolate(t.Context()) }, lf.Interpolate()},
			"FillNull": {func() (*ursus.DataFrame, error) {
				return df.FillNull(t.Context(), ursus.FillBackward.Limit(1))
			}, lf.FillNull(ursus.FillBackward.Limit(1))},
		} {
			got, err := tc.got()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			want, err := tc.want.Collect(t.Context())
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
	})
}

// TestInterpolateTemporal (step 301): an instant, a duration or a time interpolated
// on its ticks in integers, keeping its type, as Polars' is: a + step·(b−a)/span,
// the quotient truncated toward zero, so between day −2 and day −1 the middle is day
// −2. An Int128 is a Float64, and a Boolean is left as it is. Every answer is Polars
// 1.44.1's.
func TestInterpolateTemporal(t *testing.T) {
	c := ursus.Col
	at := func(y, mo, d, h, mi, us int) time.Time {
		return time.Date(y, time.Month(mo), d, h, mi, 0, us*1000, time.UTC)
	}
	two := []bool{true, false, false, true}
	lf := ursus.Frame(
		ursus.ValuesNullable("tm", []time.Time{at(2026, 1, 1, 1, 0, 0), {}, {}, at(2026, 1, 1, 2, 0, 0)}, two),
		ursus.ValuesNullable("t", []time.Time{at(2026, 1, 1, 0, 0, 0), {}, {}, at(2026, 1, 1, 0, 0, 10)}, two),
		ursus.ValuesNullable("d", []time.Time{at(2026, 1, 1, 0, 0, 0), {}, {}, at(2026, 1, 2, 0, 0, 0)}, two),
		ursus.ValuesNullable("neg", []time.Time{at(1969, 12, 30, 0, 0, 0), {}, at(1969, 12, 31, 0, 0, 0), {}},
			[]bool{true, false, true, false}),
		ursus.ValuesNullable("u", []time.Duration{time.Hour, 0, 0, 2 * time.Hour}, two),
		ursus.ValuesNullable("b", []bool{true, false, false, false}, two),
		ursus.ValuesNullable("i128", []int64{1, 0, 0, 4}, two),
	).Select(c("tm").Dt().Time(), c("t").Cast(ursus.Datetime(ursus.Micro, "")), c("d").Cast(ursus.Date),
		c("neg").Cast(ursus.Date), c("u"), c("b"), c("i128").Cast(ursus.Int128))
	want := map[string]string{
		"tm":   "01:00:00.000000000 01:20:00.000000000 01:40:00.000000000 02:00:00.000000000",
		"t":    "2026-01-01T00:00:00.000000Z 2026-01-01T00:00:00.000003Z 2026-01-01T00:00:00.000006Z 2026-01-01T00:00:00.000010Z",
		"d":    "2026-01-01 2026-01-01 2026-01-01 2026-01-02",
		"neg":  "1969-12-30 1969-12-30 1969-12-31 ∅",
		"u":    "1h0m0s 1h20m0s 1h40m0s 2h0m0s",
		"b":    "true ∅ ∅ false",
		"i128": "1.0 2.0 3.0 4.0",
	}
	types := "tm:Time(ns) t:Datetime(us) d:Date neg:Date u:Duration(ns) b:Bool i128:Float64"
	check := func(t *testing.T, df *ursus.DataFrame) {
		t.Helper()
		var got []string
		for _, f := range df.Schema().All() {
			got = append(got, f.Name+":"+f.Type.String())
			if v := strings.TrimSpace(rendered172(t, df, f.Name)); v != want[f.Name] {
				t.Errorf("%s: got %s, want %s", f.Name, v, want[f.Name])
			}
		}
		if g := strings.Join(got, " "); g != types {
			t.Errorf("types %s, want %s", g, types)
		}
	}
	df, err := lf.Interpolate().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check(t, df)

	// Over one partition too big for a limit, the interpolation spills and walks the
	// file forward and back (step 238): the same ticks, by the same integers.
	t.Run("spilled", func(t *testing.T) {
		const n = 20_000
		ts, ok := make([]time.Time, n), make([]bool, n)
		for i := range n {
			ts[i], ok[i] = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i*i)*time.Microsecond), i%7 == 0
		}
		q := ursus.Frame(ursus.ValuesNullable("t", ts, ok)).
			Select(c("t").Cast(ursus.Datetime(ursus.Micro, "")), c("t").Cast(ursus.Date).Alias("d")).
			Interpolate()
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(32<<10),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("under 32 KiB: %v", err)
		}
		if st.Spills == 0 {
			t.Fatal("answered without spilling, so it proves nothing")
		}
		ursustest.AssertFrameEqual(t, got, want)
	})
}
