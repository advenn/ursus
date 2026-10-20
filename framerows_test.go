package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestRowShortcuts is R5 of v0.6.7-scope.md (step 293): Polars' shift, gather_every,
// clear, first, last, is_empty and equals of a frame. Every answer is Polars 1.44.1's.
func TestRowShortcuts(t *testing.T) {
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2, 3, 4, 5}),
		ursus.Values("s", []string{"p", "q", "r", "s", "t"}),
		ursus.ValuesNullable("f", []float64{1.5, 2.5, 0, 4.5, 5.5}, []bool{true, true, false, true, true}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"shift 2", lf.Shift(2), "a,s,f\n,,\n,,\n1,p,1.5\n2,q,2.5\n3,r,\n"},
		{"shift -1", lf.Shift(-1), "a,s,f\n2,q,2.5\n3,r,\n4,s,4.5\n5,t,5.5\n,,\n"},
		{"shift 0", lf.Shift(0), "a,s,f\n1,p,1.5\n2,q,2.5\n3,r,\n4,s,4.5\n5,t,5.5\n"},
		{"shift 9", lf.Shift(9), "a,s,f\n,,\n,,\n,,\n,,\n,,\n"},
		// A text column takes the fill's text, as Polars' does.
		{"shift 2, 0", lf.ShiftFill(2, 0), "a,s,f\n0,0,0.0\n0,0,0.0\n1,p,1.5\n2,q,2.5\n3,r,\n"},
		{"shift 1, 1.5", lf.ShiftFill(1, 1.5), "a,s,f\n1.5,1.5,1.5\n1.0,p,1.5\n2.0,q,2.5\n3.0,r,\n4.0,s,4.5\n"},
		{"every 2", lf.GatherEvery(2, 0), "a,s,f\n1,p,1.5\n3,r,\n5,t,5.5\n"},
		{"every 2 from 1", lf.GatherEvery(2, 1), "a,s,f\n2,q,2.5\n4,s,4.5\n"},
		{"every 3 from 9", lf.GatherEvery(3, 9), "a,s,f\n"},
		{"clear", lf.Clear(0), "a,s,f\n"},
		{"clear 2", lf.Clear(2), "a,s,f\n,,\n,,\n"},
		{"first", lf.First(), "a,s,f\n1,p,1.5\n"},
		{"last", lf.Last(), "a,s,f\n5,t,5.5\n"},
		{"first of none", lf.Head(0).First(), "a,s,f\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// The types: a fill of 1.5 makes an Int64 column a Float64, as Polars; clear keeps
	// the schema.
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"shift 2, 0":   {lf.ShiftFill(2, 0), "Int64,String,Float64"},
		"shift 1, 1.5": {lf.ShiftFill(1, 1.5), "Float64,String,Float64"},
		"clear 2":      {lf.Clear(2), "Int64,String,Float64"},
	} {
		s, err := tc.q.CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		for _, f := range s.All() {
			types = append(types, f.Type.String())
		}
		if got := strings.Join(types, ","); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "gather_every(n): n should be positive".
		"every 0": {lf.GatherEvery(0, 0), "positive n"},
		// Polars makes a and f text; ursus's numbers do not meet text.
		"a text fill": {lf.ShiftFill(1, "x"), `column "a"`},
		"clear -1":    {lf.Clear(-1), "-1"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := lf.Head(0).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.IsEmpty() || !empty.IsEmpty() {
		t.Errorf("is_empty: %v, %v", df.IsEmpty(), empty.IsEmpty())
	}
	if got, err := df.Shift(t.Context(), 1); err != nil || got.Height() != 5 {
		t.Errorf("eager shift: %v, %v", got, err)
	}
}

func TestFrameEquals(t *testing.T) {
	c := ursus.Col
	nan, negz := math.NaN(), math.Copysign(0, -1)
	frame := func(lf *ursus.LazyFrame) *ursus.DataFrame {
		t.Helper()
		df, err := lf.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return df
	}
	e1 := frame(ursus.Frame(ursus.ValuesNullable("a", []int64{1, 0}, []bool{true, false}), ursus.Values("f", []float64{nan, negz})))
	e2 := frame(ursus.Frame(ursus.ValuesNullable("a", []int64{1, 0}, []bool{true, false}), ursus.Values("f", []float64{nan, 0})))
	d := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("s", []string{"x", "y"}))
	nested := ursus.Frame(ursus.Values("x", []int64{1, 2})).WithColumns(
		ursus.ConcatList(c("x"), c("x")).Alias("l"), ursus.Struct(c("x")).Alias("st"))
	for _, tc := range []struct {
		name      string
		a, b      *ursus.DataFrame
		nullEqual bool
		want      bool
	}{
		{"NaN and the zeros", e1, e2, true, true},
		{"itself", e1, e1, true, true},
		{"nulls unequal", e1, e2, false, false},
		{"itself, nulls unequal", e1, e1, false, false},
		{"another name", frame(d), frame(d.Rename(map[string]string{"a": "b"})), true, false},
		{"Int32 and Int64", frame(d), frame(d.WithColumns(c("a").Cast(ursus.Int32))), true, true},
		{"Int64 and Float64", frame(d), frame(d.WithColumns(c("a").Cast(ursus.Float64))), true, true},
		{"text and a number", frame(d), frame(d.WithColumns(c("a").Cast(ursus.String))), true, false},
		{"another order", frame(d), frame(d.Select(c("s"), c("a"))), true, false},
		{"fewer rows", frame(d), frame(d.Head(1)), true, false},
		{"another value", frame(d), frame(d.WithColumns(c("a").Add(int64(1)))), true, false},
		{"lists and structs", frame(nested), frame(nested), true, true},
		{"lists and structs, nulls unequal", frame(nested), frame(nested), false, true},
		{"none of none", frame(d.Head(0)), frame(d.Head(0)), true, true},
	} {
		got, err := tc.a.Equals(t.Context(), tc.b, tc.nullEqual)
		if err != nil || got != tc.want {
			t.Errorf("%s: %v, %v, want %v", tc.name, got, err, tc.want)
		}
	}
}
