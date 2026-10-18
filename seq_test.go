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
	// Polars runs a Struct; it cannot be a key here.
	if _, err := lf.Select(ursus.Struct(c("a"), c("s")).RleID()).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "cannot be a key") {
		t.Errorf("a Struct's rle_id: %v", err)
	}
}
