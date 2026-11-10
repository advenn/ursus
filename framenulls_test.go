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
// Polars' fill_null(strategy=, limit=); drop_nans; and interpolate. The frames are the
// framenulls group's, Polars 2.1.0's (oracle_framenulls_test.go); here stay the
// refusals, the strategies' names, the plans and the eager forms.
func TestFrameNullsAndNaN(t *testing.T) {
	nan := math.NaN()
	lf := ursus.Frame(
		ursus.ValuesNullable("i", []int64{1, 0, 0, 0, 5}, []bool{true, false, false, false, true}),
		ursus.ValuesNullable("f", []float64{1, nan, 0, 4, nan}, []bool{true, true, false, true, true}),
		ursus.ValuesNullable("s", []string{"a", "", "", "d", ""}, []bool{true, false, false, true, false}),
		ursus.ValuesNullable("g", []float64{nan, 2, 3, 0, 5}, []bool{true, true, true, false, true}),
	)

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
// on its ticks in integers, keeping its type, as Polars' is, which the framenulls
// group's interpolate_temporal records. Over one partition too big for a limit, the
// interpolation spills and walks the file forward and back (step 238): the same
// ticks, by the same integers.
func TestInterpolateTemporal(t *testing.T) {
	c := ursus.Col
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
}
