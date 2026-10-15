package ursus_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

func generateFixture() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("a", []int64{1, 2, 3}),
		ursus.ValuesNullable("s", []int64{0, 5, 0}, []bool{true, true, false}),
		ursus.Values("e", []int64{3, 2, 4}), ursus.Values("st", []int64{1, -1, 2}),
		ursus.Values("g", []int64{1, 1, 2}))
}

// TestGenerators is G1 of v0.6.6-scope.md (step 263): int_range, linear_space,
// repeat, ones and zeros, a column of their own length. Every answer is Polars
// 1.44.1's, at batch sizes 1, 2 and 8192.
func TestGenerators(t *testing.T) {
	c := ursus.Col
	lf := generateFixture()
	at := func(day, h int) time.Time { return time.Date(2024, 1, day, h, 0, 0, 0, time.UTC) }
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"int_range", lf.Select(ursus.IntRange(0, 5, 1)), "literal\n0\n1\n2\n3\n4\n"},
		{"by two", lf.Select(ursus.IntRange(0, 5, 2)), "literal\n0\n2\n4\n"},
		{"counting down", lf.Select(ursus.IntRange(5, 0, -2)), "literal\n5\n3\n1\n"},
		{"empty", lf.Select(ursus.IntRange(5, 0, 1)), "literal\n"},
		{"a row index", lf.WithColumns(ursus.IntRange(0, ursus.Len(), 1).Alias("i")).Select(c("a", "i")),
			"a,i\n1,0\n2,1\n3,2\n"},
		{"from aggregates", lf.Select(ursus.IntRange(c("a").Min(), c("a").Max().Add(2), 1)), "a\n1\n2\n3\n4\n"},
		{"beside a column", lf.Select(c("a"), ursus.IntRange(10, 13, 1).Alias("r")), "a,r\n1,10\n2,11\n3,12\n"},
		{"summed", lf.Select(ursus.IntRange(0, 5, 1).Sum()), "literal\n10\n"},
		{"linear_space", lf.Select(ursus.LinearSpace(0, 1, 5)), "literal\n0.0\n0.25\n0.5\n0.75\n1.0\n"},
		{"the far end exactly", lf.Select(ursus.LinearSpace(0, 0.3, 4)),
			"literal\n0.0\n0.09999999999999999\n0.19999999999999998\n0.3\n"},
		{"the far end, not a step past it", lf.Select(ursus.LinearSpace(0.2, 0.9, 8)),
			"literal\n0.2\n0.3\n0.4\n0.5\n0.6\n0.7\n0.8\n0.9\n"},
		{"closed left", lf.Select(ursus.LinearSpace(0.1, 0.7, 7, ursus.ClosedLeft)),
			"literal\n0.1\n0.18571428571428572\n0.27142857142857146\n0.3571428571428571\n" +
				"0.44285714285714284\n0.5285714285714286\n0.6142857142857142\n"},
		{"closed right", lf.Select(ursus.LinearSpace(0.1, 0.7, 7, ursus.ClosedRight)),
			"literal\n0.18571428571428572\n0.27142857142857146\n0.35714285714285715\n" +
				"0.44285714285714284\n0.5285714285714286\n0.6142857142857143\n0.7\n"},
		{"closed none", lf.Select(ursus.LinearSpace(0.1, 0.7, 7, ursus.ClosedNone)),
			"literal\n0.175\n0.25\n0.32499999999999996\n0.39999999999999997\n0.475\n0.55\n0.625\n"},
		{"one sample, open", lf.Select(ursus.LinearSpace(0, 1, 1, ursus.ClosedNone)), "literal\n0.5\n"},
		{"no sample", lf.Select(ursus.LinearSpace(0, 1, 0)), "literal\n"},
		{"Float32", lf.Select(ursus.LinearSpace(float32(0), float32(1), 3)), "literal\n0.0\n0.5\n1.0\n"},
		{"dates", lf.Select(ursus.LinearSpace(ursus.Lit(at(1, 0)).Cast(ursus.Date), ursus.Lit(at(2, 0)).Cast(ursus.Date), 4).
			Dt().Strftime("%Y-%m-%d %H:%M")),
			"literal\n2024-01-01 00:00\n2024-01-01 08:00\n2024-01-01 16:00\n2024-01-02 00:00\n"},
		{"instants", lf.Select(ursus.LinearSpace(ursus.Lit(at(1, 0)), ursus.Lit(at(2, 0)), 3).Dt().Strftime("%d %H:%M")),
			"literal\n01 00:00\n01 12:00\n02 00:00\n"},
		{"truncated toward zero", lf.Select(ursus.LinearSpace(
			ursus.Lit(at(1, 0).Add(3*time.Microsecond)).Cast(ursus.Datetime(ursus.Micro, "")),
			ursus.Lit(at(1, 0)).Cast(ursus.Datetime(ursus.Micro, "")), 3).Dt().Microsecond()),
			"literal\n3\n1\n0\n"},
		{"durations", lf.Select(ursus.LinearSpace(ursus.Lit(time.Duration(0)), ursus.Lit(time.Second), 3).
			Cast(ursus.Int64)), "literal\n0\n500000000\n1000000000\n"},
		{"repeat", lf.Select(ursus.Repeat(1, 3)), "repeat\n1\n1\n1\n"},
		{"repeat a string", lf.Select(ursus.Repeat("x", 2)), "repeat\nx\nx\n"},
		{"repeat none", lf.Select(ursus.Repeat(1, 0)), "repeat\n"},
		{"repeat a total", lf.Select(ursus.Repeat(c("a").Sum(), ursus.Len())), "repeat\n6\n6\n6\n"},
		{"ones", lf.Select(ursus.Ones(2)), "ones\n1.0\n1.0\n"},
		{"zeros", lf.Select(ursus.Zeros(2)), "zeros\n0.0\n0.0\n"},
	} {
		for _, size := range []int{1, 2, 8192} {
			var got bytes.Buffer
			if err := tc.q.WriteCSV(t.Context(), &got, ursus.WithBatchSize(size)); err != nil {
				t.Fatalf("%s, batch %d: %v", tc.name, size, err)
			}
			if got.String() != tc.want {
				t.Errorf("%s, batch %d:\ngot  %q\nwant %q", tc.name, size, got.String(), tc.want)
			}
		}
	}
}

// TestGeneratorsAsLists: the per-row forms, and a generator in an aggregation, a list a
// group. Polars: int_ranges [[0, 1, 2], [5, 4, 3], None]; linear_spaces [[0.0, 1.5,
// 3.0], [5.0, 3.5, 2.0], None] and by a count a row [[0.0], [0.0, 1.0], [0.0, 0.5,
// 1.0]]; repeat_by [[0], [5, 5], [None, None, None]] and by a null count [[], [2, 2,
// 2, 2, 2], None]; in an aggregation [[0, 1], [0]].
func TestGeneratorsAsLists(t *testing.T) {
	c := ursus.Col
	lf := generateFixture()
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want []string
	}{
		"int_ranges":              {lf.Select(ursus.IntRanges(c("s"), c("e"), c("st"))), []string{"[0, 1, 2]", "[5, 4, 3]", "null"}},
		"linear_spaces":           {lf.Select(ursus.LinearSpaces(c("s"), c("e"), 3)), []string{"[0, 1.5, 3]", "[5, 3.5, 2]", "null"}},
		"linear_spaces by counts": {lf.Select(ursus.LinearSpaces(0, 1, c("a"))), []string{"[0]", "[0, 1]", "[0, 0.5, 1]"}},
		"repeat_by":               {lf.Select(c("s").RepeatBy(c("a"))), []string{"[0]", "[5, 5]", "[null, null, null]"}},
		"repeat_by a null count":  {lf.Select(c("a").RepeatBy(c("s"))), []string{"[]", "[2, 2, 2, 2, 2]", "null"}},
		"in an aggregation": {lf.GroupBy(c("g")).Agg(ursus.IntRange(0, ursus.Len(), 1).Alias("r")).
			Sort(ursus.Asc(c("g"))).Select(c("r")), []string{"[0, 1]", "[0]"}},
	} {
		df, err := tc.q.Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if df.Height() != len(tc.want) {
			t.Errorf("%s: %d rows, want %d\n%s", name, df.Height(), len(tc.want), df)
		}
		for _, w := range tc.want {
			if !strings.Contains(df.String(), w) {
				t.Errorf("%s lacks Polars' %s:\n%s", name, w, df)
			}
		}
	}
}

// TestGeneratorsRefuse: a step of zero (Polars panics on int_ranges'), a negative
// count, a float where integers are taken (Polars casts it), ends of two kinds, a
// generator of another length beside a column, and one in .Over.
func TestGeneratorsRefuse(t *testing.T) {
	c := ursus.Col
	lf := generateFixture()
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"a step of zero":     {lf.Select(ursus.IntRange(0, 5, 0)), "step is zero"},
		"a negative repeat":  {lf.Select(ursus.Repeat(1, -1)), "-1 times"},
		"a negative count":   {lf.Select(c("a").RepeatBy(c("st"))), "-1 times in row 1"},
		"a float start":      {lf.Select(ursus.IntRange(0.5, 3, 1)), "takes integers"},
		"ends of two kinds":  {lf.Select(ursus.LinearSpace(ursus.Lit(time.Now()), 1, 3)), "two values of one temporal type"},
		"another length":     {lf.Select(c("a"), ursus.IntRange(0, 2, 1)), "columns side by side are of one length"},
		"over its partition": {lf.Select(ursus.IntRange(0, ursus.Len(), 1).Over(c("g"))), "CumCount(false)"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
