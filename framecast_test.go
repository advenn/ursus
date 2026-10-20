package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFrameCast is C1 of v0.6.7-scope.md (step 298): Polars' DataFrame.cast by name,
// by type and for every column, strict and not. Every answer is Polars 1.44.1's but
// where the step's record says an Expr.Cast answers otherwise.
func TestFrameCast(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	lf := ursus.Frame(
		ursus.Values("a", []int64{1, 2}),
		ursus.Values("b", []float64{1.5, 2.5}),
		ursus.Values("s", []string{"1", "x"}),
		ursus.Values("t", []time.Time{at(1), at(2)}),
	).WithColumns(ursus.Col("t").Cast(ursus.Datetime(ursus.Micro, "")))
	us := ursus.Datetime(ursus.Micro, "")

	types := func(t *testing.T, q *ursus.LazyFrame) string {
		t.Helper()
		s, err := q.CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range s.All() {
			out = append(out, f.Name+":"+f.Type.String())
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"by name", lf.Cast(map[string]ursus.DataType{"a": ursus.Float32}),
			"a:Float32,b:Float64,s:String,t:Datetime(us)"},
		{"by names, in the frame's order", lf.Cast(map[string]ursus.DataType{"t": ursus.Date, "a": ursus.Int8}),
			"a:Int8,b:Float64,s:String,t:Date"},
		{"by type", lf.CastTypes(map[ursus.DataType]ursus.DataType{ursus.Int64: ursus.Int32}),
			"a:Int32,b:Float64,s:String,t:Datetime(us)"},
		{"a type with no column", lf.CastTypes(map[ursus.DataType]ursus.DataType{ursus.Int8: ursus.Int16}),
			"a:Int64,b:Float64,s:String,t:Datetime(us)"},
		// A Datetime key matches its unit and zone: Polars' Datetime("us") matches the
		// column and Datetime("ns") does not.
		{"a Datetime of the unit", lf.CastTypes(map[ursus.DataType]ursus.DataType{us: ursus.Date}),
			"a:Int64,b:Float64,s:String,t:Date"},
		{"a Datetime of another unit", lf.CastTypes(map[ursus.DataType]ursus.DataType{
			ursus.Datetime(ursus.Nano, ""): ursus.Date}),
			"a:Int64,b:Float64,s:String,t:Datetime(us)"},
		{"every column", lf.CastAll(ursus.String), "a:String,b:String,s:String,t:String"},
	} {
		if got := types(t, tc.q); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}

	t.Run("values", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			{"every column as text", lf.Select(ursus.Col("a"), ursus.Col("b")).CastAll(ursus.String),
				"a,b\n1,1.5\n2,2.5\n"},
			{"not strict", lf.Select(ursus.Col("s")).Cast(map[string]ursus.DataType{"s": ursus.Int64},
				ursus.CastStrict(false)), "s\n1\n\n"},
			{"an overflow not strict", ursus.Frame(ursus.Values("a", []int64{1, 300})).
				Cast(map[string]ursus.DataType{"a": ursus.Int8}, ursus.CastStrict(false)), "a\n1\n\n"},
			{"a float of no fraction as an integer", ursus.Frame(ursus.Values("f", []float64{1, -2})).
				CastTypes(map[ursus.DataType]ursus.DataType{ursus.Float64: ursus.Int64}), "f\n1\n-2\n"},
		} {
			if got := csvOf(t, tc.q); got != tc.want {
				t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want []string
		}{
			// Polars: unable to find column "nope"; valid columns: ["a", "b", "s", "t"].
			{"an unknown name", lf.Cast(map[string]ursus.DataType{"a": ursus.Int8, "nope": ursus.Int32}),
				[]string{`unknown column "nope"`, "available: a, b, s, t"}},
			{"a typo", lf.Cast(map[string]ursus.DataType{"b ": ursus.Int32}), []string{`did you mean: "b"`}},
			// Polars: conversion from `str` to `i64` failed in column 's' ... ["x"].
			{"strict", lf.Cast(map[string]ursus.DataType{"s": ursus.Int64}), []string{`"x"`}},
			{"an overflow, strict", ursus.Frame(ursus.Values("a", []int64{1, 300})).
				Cast(map[string]ursus.DataType{"a": ursus.Int8}), []string{"300"}},
			// Polars truncates 1.5 to 1; an Expr.Cast refuses a fraction (§9).
			{"a fraction as an integer", lf.CastTypes(map[ursus.DataType]ursus.DataType{ursus.Float64: ursus.Int64}),
				[]string{"1.5", "not representable"}},
		} {
			_, err := tc.q.Collect(t.Context())
			for _, w := range tc.want {
				if err == nil || !strings.Contains(err.Error(), w) {
					t.Errorf("%s: got %v, want an error with %q", tc.name, err, w)
				}
			}
		}
	})

	t.Run("plan", func(t *testing.T) {
		explain := func(q *ursus.LazyFrame) string {
			t.Helper()
			p, err := q.Explain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		// A column already of its type, or a type no column has, adds nothing, as
		// Polars' plan shows col("a") alone.
		base := explain(lf)
		if got := explain(lf.Cast(map[string]ursus.DataType{"a": ursus.Int64})); got != base {
			t.Errorf("a cast to the column's own type is in the plan:\n%s", got)
		}
		if got := explain(lf.CastTypes(map[ursus.DataType]ursus.DataType{ursus.Int8: ursus.Int16})); got != base {
			t.Errorf("a type with no column is in the plan:\n%s", got)
		}
		// The plan is the frame's order, not the map's.
		m := map[string]ursus.DataType{"t": ursus.Date, "s": ursus.Binary, "b": ursus.Float32, "a": ursus.Int8}
		want := explain(lf.Cast(m))
		for range 20 {
			if got := explain(lf.Cast(m)); got != want {
				t.Fatalf("the plan moved with the map's order:\n%s\nagainst\n%s", got, want)
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
			"Cast": {func() (*ursus.DataFrame, error) {
				return df.Cast(t.Context(), map[string]ursus.DataType{"a": ursus.Float32})
			}, lf.Cast(map[string]ursus.DataType{"a": ursus.Float32})},
			"CastTypes": {func() (*ursus.DataFrame, error) {
				return df.CastTypes(t.Context(), map[ursus.DataType]ursus.DataType{ursus.Int64: ursus.Int32})
			}, lf.CastTypes(map[ursus.DataType]ursus.DataType{ursus.Int64: ursus.Int32})},
			"CastAll": {func() (*ursus.DataFrame, error) {
				return df.CastAll(t.Context(), ursus.String, ursus.CastStrict(false))
			}, lf.CastAll(ursus.String, ursus.CastStrict(false))},
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

// TestFrameCastUnderAMemoryLimit holds a frame's cast to its pieces: a WithColumns of
// casts streams, so it answers under a limit far below its input as without one.
func TestFrameCastUnderAMemoryLimit(t *testing.T) {
	const n = 20_000
	a, s := make([]int64, n), make([]string, n)
	for i := range n {
		a[i], s[i] = int64(i%300), []string{"7", "x", "12"}[i%3]
	}
	q := ursus.Frame(ursus.Values("a", a), ursus.Values("s", s)).
		Cast(map[string]ursus.DataType{"a": ursus.Int8, "s": ursus.Int32}, ursus.CastStrict(false))
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
		ursus.WithSpillDir(t.TempDir()))
	if err != nil {
		t.Fatalf("under 16 KiB: %v", err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}
