package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// seqFrame is Polars' frame: g [1, 1, 1, 2, 2, 2], a [1, 1, 2, None, None, 1], f
// [1.0, NaN, NaN, -0.0, 0.0, 2.0] and s ["x", "x", "y", "y", None, "x"].
func seqFrame() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}),
		ursus.ValuesNullable("a", []int64{1, 1, 2, 0, 0, 1}, []bool{true, true, true, false, false, true}),
		ursus.Values("f", []float64{1, math.NaN(), math.NaN(), math.Copysign(0, -1), 0, 2}),
		ursus.ValuesNullable("s", []string{"x", "x", "y", "y", "", "x"}, []bool{true, true, true, true, false, true}))
}

// TestRle is M4 of v0.6.6-scope.md (step 281): rle and rle_id. Every answer is
// Polars 1.44.1's.
func TestRle(t *testing.T) {
	c := ursus.Col
	lf := seqFrame()
	runs := func(col string) *ursus.LazyFrame {
		return lf.Select(c(col).Rle().Alias("r")).Select(c("r").Struct().Field("*"))
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"rle", runs("a"), "len,value\n2,1\n1,2\n2,\n1,1\n"},
		{"NaN and the zeros", runs("f"), "len,value\n1,1.0\n2,NaN\n2,-0.0\n1,2.0\n"},
		{"text", runs("s"), "len,value\n2,x\n2,y\n1,\n1,x\n"},
		{"rle_id", lf.Select(c("a").RleID()), "a\n0\n0\n1\n2\n2\n3\n"},
		{"rle_id of NaN and the zeros", lf.Select(c("f").RleID()), "f\n0\n1\n1\n2\n2\n3\n"},
		{"rle_id over g", lf.Select(c("a").RleID().Over(c("g"))), "a\n0\n0\n1\n0\n0\n1\n"},
		{"by group", lf.GroupBy(c("g")).Agg(c("a").Rle().List().Eval(ursus.ConcatStr(":",
			ursus.Element().Struct().Field("len").Cast(ursus.String),
			ursus.Element().Struct().Field("value").Cast(ursus.String).FillNullWith("null"))).List().Join("|")).
			Sort(ursus.Asc(c("g"))).Select(c("a")), "a\n2:1|1:2\n2:null|1:1\n"},
		{"no rows", lf.Filter(c("g").Gt(5)).Select(c("a").RleID()), "a\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	got, err := lf.Select(c("a").Rle()).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type.String() != "Struct(len: Uint32, value: Int64)" {
		t.Errorf("rle's type: %v, %v", got, err)
	}
	got, err = lf.Select(c("a").RleID()).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Uint32 {
		t.Errorf("rle_id's type: %v, %v", got, err)
	}
	// A Struct's runs, field by field, as Polars runs them (step 344).
	if got := csvOf(t, lf.Select(ursus.Struct(c("a"), c("s")).RleID())); got != "a\n0\n0\n1\n2\n3\n4\n" {
		t.Errorf("a Struct's rle_id: %q", got)
	}
}

// TestAppendAndExtend is M4's rest (step 282): append and extend_constant. Every
// answer is Polars 1.44.1's.
func TestAppendAndExtend(t *testing.T) {
	c := ursus.Col
	a := c("a")
	lf := seqFrame()
	text := func(e ursus.Expr) ursus.Expr {
		return e.List().Eval(ursus.Element().Cast(ursus.String).FillNullWith("null")).List().Join("|")
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"append", lf.Select(a.Append(c("g"), true)), "a\n1\n1\n2\n\n\n1\n1\n1\n1\n2\n2\n2\n"},
		{"a constant", lf.Select(a.Append(ursus.Lit(int64(9)), true)), "a\n1\n1\n2\n\n\n1\n9\n"},
		{"a constant by group", lf.GroupBy(c("g")).Agg(text(a.Append(ursus.Lit(int64(9)), true))).Sort(ursus.Asc(c("g"))).Select(a),
			"a\n1|1|2|9\nnull|null|1|9\n"},
		{"by group", lf.GroupBy(c("g")).Agg(text(a.Append(c("g"), true))).Sort(ursus.Asc(c("g"))).Select(a),
			"a\n1|1|2|1|1|1\nnull|null|1|2|2|2\n"},
		{"extend_constant", lf.Select(a.ExtendConstant(7, 2)), "a\n1\n1\n2\n\n\n1\n7\n7\n"},
		{"extend by a null", lf.Select(a.ExtendConstant(nil, 1)), "a\n1\n1\n2\n\n\n1\n\n"},
		{"a float truncated", lf.Select(a.ExtendConstant(1.5, 1)), "a\n1\n1\n2\n\n\n1\n1\n"},
		{"a negative truncated", ursus.Frame(ursus.Values("a", []int64{1})).Select(a.ExtendConstant(-1.5, 1)), "a\n1\n-1\n"},
		{"past an Int8", ursus.Frame(ursus.Values("a", []int64{1})).Select(a.Cast(ursus.Int8)).Select(a.ExtendConstant(300, 1)), "a\n1\n\n"},
		{"a number to text", ursus.Frame(ursus.Values("a", []string{"x"})).Select(a.ExtendConstant(5, 1)), "a\nx\n5\n"},
		{"n an expression", lf.Select(a.ExtendConstant(0, ursus.Lit(int64(2)))), "a\n1\n1\n2\n\n\n1\n0\n0\n"},
		{"extended by group", lf.GroupBy(c("g")).Agg(text(a.ExtendConstant(0, 1))).Sort(ursus.Asc(c("g"))).Select(a),
			"a\n1|1|2|0\nnull|null|1|0\n"},
		{"a float truncated by group", lf.GroupBy(c("g")).Agg(text(a.ExtendConstant(-1.5, 1))).Sort(ursus.Asc(c("g"))).Select(a),
			"a\n1|1|2|-1\nnull|null|1|-1\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		e    ursus.Expr
		want ursus.DataType
	}{
		{a.Append(c("f"), true), ursus.Float64}, {a.ExtendConstant(1.5, 1), ursus.Int64},
	} {
		got, err := lf.Select(tc.e).Collect(t.Context())
		if err != nil || got.Schema().Field(0).Type != tc.want {
			t.Errorf("%s: %v, %v, want %s", tc.e, got, err, tc.want)
		}
	}
	// The appended side's nulls are the answer's, which a verified run holds it to.
	nulls := lf.Select(c("g").Append(a, true))
	if s, err := nulls.CollectSchema(t.Context()); err != nil || !s.Field(0).Nullable {
		t.Errorf("g appended by a nullable a: %v, %v, want nullable", s, err)
	}
	if _, err := nulls.Collect(t.Context(), ursus.WithVerify()); err != nil {
		t.Errorf("g appended by a nullable a, verified: %v", err)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "type i8 is incompatible with expected type i64".
		"without upcast": {lf.Select(a.Append(a.Cast(ursus.Int8), false)), "do not meet"},
		// Polars meets text with a number as text; ursus's Promote and Concat do not.
		"text and a number": {lf.Select(c("s").Append(a, true)), "do not meet"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestSampleAndShuffle: a seeded draw cannot be Polars', whose generator is its own,
// so its properties are held. Its sizes are Polars': 3 of 6, half of 6, 10 with
// replacement, and too many without refused as Polars refuses it; all 6 unshuffled are
// the column as it was.
func TestSampleAndShuffle(t *testing.T) {
	c := ursus.Col
	x := c("x")
	lf := ursus.Frame(ursus.Values("x", []int64{5, 3, 8, 1, 9, 2}), ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}))
	rows := func(q *ursus.LazyFrame) []string {
		return strings.Split(strings.TrimSpace(csvOf(t, q)), "\n")[1:]
	}
	member := map[string]bool{"5": true, "3": true, "8": true, "1": true, "9": true, "2": true}
	for name, tc := range map[string]struct {
		q *ursus.LazyFrame
		n int
	}{
		"3 of 6":              {lf.Select(x.SampleN(3, false, false, 7)), 3},
		"half":                {lf.Select(x.SampleFraction(0.5, false, false, 7)), 3},
		"0.4 of 6, truncated": {lf.Select(x.SampleFraction(0.4, false, false, 7)), 2},
		"10 with replacement": {lf.Select(x.SampleN(10, true, false, 7)), 10},
		"shuffled":            {lf.Select(x.Shuffle(7)), 6},
	} {
		got := rows(tc.q)
		if len(got) != tc.n {
			t.Errorf("%s: %d rows, want %d", name, len(got), tc.n)
		}
		for _, v := range got {
			if !member[v] {
				t.Errorf("%s drew %s, which the column has not", name, v)
			}
		}
		if again := rows(tc.q); strings.Join(again, ",") != strings.Join(got, ",") {
			t.Errorf("%s drew %v, then %v, from one seed", name, got, again)
		}
	}
	shuffled := rows(lf.Select(x.Shuffle(7)))
	seen := map[string]int{}
	for _, v := range shuffled {
		seen[v]++
	}
	if len(seen) != 6 {
		t.Errorf("a shuffle is each value once: %v", shuffled)
	}
	if got := strings.Join(rows(lf.Select(x.SampleN(6, false, false, 7))), ","); got != "5,3,8,1,9,2" {
		t.Errorf("all of them in order: %s", got)
	}
	if got := strings.Join(shuffled, ","); got == "5,3,8,1,9,2" {
		t.Errorf("a shuffle by seed 7 left the order as it was")
	}
	if strings.Join(rows(lf.Select(x.SampleN(3, false, false, 7))), ",") ==
		strings.Join(rows(lf.Select(x.SampleN(3, false, false, 8))), ",") {
		t.Errorf("seeds 7 and 8 drew alike")
	}
	fractions := lf.GroupBy(c("g")).Agg(x.SampleFraction(0.4, false, false, 7).List().Len().Alias("n")).Sort(ursus.Asc(c("g"))).Select(c("n"))
	if got := csvOf(t, fractions); got != "n\n1\n1\n" {
		t.Errorf("0.4 of a group of 3: %q", got)
	}
	groups := lf.GroupBy(c("g")).Agg(x.SampleN(2, false, false, 7).List().Len().Alias("n")).Sort(ursus.Asc(c("g"))).Select(c("n"))
	if got := csvOf(t, groups); got != "n\n2\n2\n" {
		t.Errorf("2 a group: %q", got)
	}
	if _, err := lf.Select(x.SampleN(10, false, false, 7)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "without replacement") {
		t.Errorf("too many without replacement: %v", err)
	}
}
