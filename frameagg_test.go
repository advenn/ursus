package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFrameAggregates is C3 of v0.6.7-scope.md (step 300): Polars' DataFrame.sum,
// mean, median, min, max, std, var, quantile, product and null_count. Every column
// is kept, a null where the aggregate does not take it. Every value is Polars
// 1.44.1's on the same frame but where feature-gaps.md §9 says ursus's aggregate
// answers otherwise: an Int128 sum, a Float64 product, a Uint64 null count, and a
// float var or std an ulp apart.
func TestFrameAggregates(t *testing.T) {
	at := func(d, h int) time.Time { return time.Date(2026, 1, d, h, 0, 0, 0, time.UTC) }
	ok := []bool{true, true, false, true}
	lf := ursus.Frame(
		ursus.ValuesNullable("i", []int64{1, 2, 0, 4}, ok),
		ursus.Values("i8", []int8{1, 2, 3, 4}),
		ursus.ValuesNullable("f", []float64{1.5, 0, 2.5, 4}, []bool{true, false, true, true}),
		ursus.ValuesNullable("b", []bool{true, false, true, false}, []bool{true, true, true, false}),
		ursus.ValuesNullable("s", []string{"p", "q", "", "r"}, ok),
		ursus.ValuesNullable("d", []time.Time{at(1, 0), at(3, 0), {}, at(8, 0)}, ok),
		ursus.ValuesNullable("t", []time.Time{at(1, 6), {}, at(2, 6), at(3, 6)}, []bool{true, false, true, true}),
		ursus.ValuesNullable("u", []time.Duration{time.Hour, 2 * time.Hour, 0, 4 * time.Hour}, ok),
		ursus.Values("l", []string{"1", "2,3", "4", "5"}),
	).WithColumns(ursus.Col("d").Cast(ursus.Date), ursus.Col("t").Cast(ursus.Datetime(ursus.Micro, "")),
		ursus.Col("l").Str().Split(","))
	const (
		jan3 = "2026-01-03T00:00:00.000000Z"
		jan2 = "2026-01-02T06:00:00.000000Z"
	)
	for _, tc := range []struct {
		name  string
		q     *ursus.LazyFrame
		types string
		want  string // every column but l, which is null for each but null_count
	}{
		{"sum", lf.Sum(), "Int128 Int128 Float64 Int128 String Date Datetime(us) Duration(ns) List(String)",
			"7 10 8.0 2 ∅ ∅ ∅ 7h0m0s"},
		{"mean", lf.Mean(), "Float64 Float64 Float64 Float64 String Datetime(us) Datetime(us) Duration(ns) List(String)",
			"2.3333333333333335 2.5 2.6666666666666665 0.6666666666666666 ∅ 2026-01-04T00:00:00.000000Z " + jan2 + " 2h20m0s"},
		{"median", lf.Median(), "Float64 Float64 Float64 Float64 String Datetime(us) Datetime(us) Duration(ns) List(String)",
			"2.0 2.5 2.5 1.0 ∅ " + jan3 + " " + jan2 + " 2h0m0s"},
		{"min", lf.Min(), "Int64 Int8 Float64 Bool String Date Datetime(us) Duration(ns) List(String)",
			"1 1 1.5 false p 2026-01-01 2026-01-01T06:00:00.000000Z 1h0m0s"},
		{"max", lf.Max(), "Int64 Int8 Float64 Bool String Date Datetime(us) Duration(ns) List(String)",
			"4 4 4.0 true r 2026-01-08 2026-01-03T06:00:00.000000Z 4h0m0s"},
		// Polars' f is 1.2583057392117916 and 1.5833333333333333: §9's ulp.
		{"std", lf.Std(1), "Float64 Float64 Float64 Float64 String Date Datetime(us) Duration(ns) List(String)",
			"1.5275252316519465 1.2909944487358056 1.2583057392117918 0.5773502691896258 ∅ ∅ ∅ ∅"},
		{"var", lf.Var(1), "Float64 Float64 Float64 Float64 String Date Datetime(us) Duration(ns) List(String)",
			"2.333333333333333 1.6666666666666667 1.5833333333333335 0.33333333333333337 ∅ ∅ ∅ ∅"},
		{"var, ddof 0", lf.Var(0).Select(ursus.Col("i")), "Float64", "1.5555555555555554"},
		// Polars: a column a product does not take is of the Null type.
		{"product", lf.Product(), "Float64 Float64 Float64 Float64 Null Null Null Null Null", "8.0 24.0 15.0 0.0 ∅ ∅ ∅ ∅"},
		// Polars: a Boolean's quantile is null, a Date's a Datetime.
		{"quantile", lf.Quantile(0.5, ursus.InterpNearest),
			"Float64 Float64 Float64 Bool String Datetime(us) Datetime(us) Duration(ns) List(String)",
			"2.0 3.0 2.5 ∅ ∅ " + jan3 + " " + jan2 + " 2h0m0s"},
		{"null count", lf.NullCount(), "Uint64 Uint64 Uint64 Uint64 Uint64 Uint64 Uint64 Uint64 Uint64",
			"1 0 1 1 1 1 1 1"},
	} {
		df, err := tc.q.Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if df.Height() != 1 {
			t.Errorf("%s: %d rows, want one", tc.name, df.Height())
		}
		var types, values []string
		for _, f := range df.Schema().All() {
			types = append(types, f.Type.String())
			if f.Name == "l" {
				continue
			}
			values = append(values, strings.TrimSpace(rendered172(t, df, f.Name)))
		}
		if got := strings.Join(types, " "); got != tc.types {
			t.Errorf("%s: types %s, want %s", tc.name, got, tc.types)
		}
		if got := strings.Join(values, " "); got != tc.want {
			t.Errorf("%s:\ngot  %s\nwant %s", tc.name, got, tc.want)
		}
		if df.Width() > 1 && tc.name != "null count" {
			nulls, err := df.Select(t.Context(), ursus.Col("l").IsNull())
			if err != nil {
				t.Fatal(err)
			}
			if v, _, _ := nulls.At[bool](0, "l"); !v {
				t.Errorf("%s: the List column is not null", tc.name)
			}
		}
	}

	t.Run("no rows", func(t *testing.T) {
		// Polars: sum [(0, None)], mean and min [(None, None)], null_count [(0, 0)],
		// product [(1, None)]. An empty sum or product is null here (§9).
		empty := lf.Select(ursus.Col("i"), ursus.Col("s")).Head(0)
		for name, tc := range map[string]struct {
			q    *ursus.LazyFrame
			want string
		}{
			"sum": {empty.Sum(), "∅ ∅"}, "mean": {empty.Mean(), "∅ ∅"}, "min": {empty.Min(), "∅ ∅"},
			"null count": {empty.NullCount(), "0 0"}, "product": {empty.Product(), "∅ ∅"},
		} {
			df, err := tc.q.Collect(t.Context())
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got := strings.TrimSpace(rendered172(t, df, "i")) + " " + strings.TrimSpace(rendered172(t, df, "s"))
			if got != tc.want || df.Height() != 1 {
				t.Errorf("%s: got %s in %d rows, want %s in one", name, got, df.Height(), tc.want)
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
			"Sum":       {func() (*ursus.DataFrame, error) { return df.Sum(t.Context()) }, lf.Sum()},
			"Mean":      {func() (*ursus.DataFrame, error) { return df.Mean(t.Context()) }, lf.Mean()},
			"Median":    {func() (*ursus.DataFrame, error) { return df.Median(t.Context()) }, lf.Median()},
			"Min":       {func() (*ursus.DataFrame, error) { return df.Min(t.Context()) }, lf.Min()},
			"Max":       {func() (*ursus.DataFrame, error) { return df.Max(t.Context()) }, lf.Max()},
			"Std":       {func() (*ursus.DataFrame, error) { return df.Std(t.Context(), 0) }, lf.Std(0)},
			"Var":       {func() (*ursus.DataFrame, error) { return df.Var(t.Context(), 1) }, lf.Var(1)},
			"Product":   {func() (*ursus.DataFrame, error) { return df.Product(t.Context()) }, lf.Product()},
			"NullCount": {func() (*ursus.DataFrame, error) { return df.NullCount(t.Context()) }, lf.NullCount()},
			"Quantile": {func() (*ursus.DataFrame, error) {
				return df.Quantile(t.Context(), 0.25, ursus.InterpLinear)
			}, lf.Quantile(0.25, ursus.InterpLinear)},
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

// TestFrameAggregatesUnderAMemoryLimit holds the aggregates to their pieces: a Select
// of aggregates is a keyless group-by, which streams its input. A median and a
// quantile hold their values, as they do by key, and spill them. A float aggregate's
// last bits are the thread count's (feature-gaps.md §1), and a group-by under a limit
// runs on one, so the answer without a limit is one thread's.
func TestFrameAggregatesUnderAMemoryLimit(t *testing.T) {
	const n = 20_000
	a, s := make([]int64, n), make([]string, n)
	for i := range n {
		a[i], s[i] = int64(i*7919%10007), []string{"p", "q", "r"}[i%3]
	}
	lf := ursus.Frame(ursus.Values("a", a), ursus.Values("s", s))
	for name, q := range map[string]*ursus.LazyFrame{
		"sum": lf.Sum(), "mean": lf.Mean(), "min": lf.Min(), "std": lf.Std(1), "null count": lf.NullCount(),
		"median": lf.Median(), "quantile": lf.Quantile(0.9, ursus.InterpLinear),
	} {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(err)
		}
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(64<<10),
			ursus.WithSpillDir(t.TempDir()))
		if err != nil {
			t.Fatalf("%s under 64 KiB: %v", name, err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}
