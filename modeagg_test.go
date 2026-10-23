package ursus_test

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// TestModeInAnAggregation is step 340: a group's Mode is an aggregate of its own,
// an entry a distinct value, where it was the list kernel over the group's values
// imploded. Every answer is Polars 1.44.1's, in the order the values first appear
// with maintainOrder; sorted, nulls first, without, where Polars' order is its hash
// table's.
func TestModeInAnAggregation(t *testing.T) {
	c := ursus.Col
	nan := math.NaN()
	lf := ursus.Frame(
		ursus.Values("g", []int64{1, 1, 1, 1, 2, 2, 2, 3, 3, 3, 3}),
		ursus.ValuesNullable("x", []float64{3, 0, 0, 3, math.Copysign(0, -1), 0, 2, nan, 1, nan, 1},
			[]bool{true, false, false, true, true, true, true, true, true, true, true}),
		ursus.ValuesNullable("s", []string{"b", "a", "b", "a", "z", "y", "z", "", "", "q", "q"},
			[]bool{true, true, true, true, true, true, true, false, false, true, true}),
		ursus.ValuesNullable("b", []bool{true, false, true, false, false, false, true, false, false, true, true},
			[]bool{true, true, true, false, false, false, true, true, true, true, true}),
		ursus.Values("i", []int64{5, 7, 7, 5, 1, 2, 3, 9, 9, 8, 8}),
	).WithColumns(c("i").Cast(ursus.Int32).Cast(ursus.Date).Alias("d"))
	text := func(e ursus.Expr) ursus.Expr {
		return e.List().Eval(ursus.Element().Cast(ursus.String).FillNullWith("null")).List().Join("|")
	}
	byG := func(es ...ursus.Expr) *ursus.LazyFrame {
		return lf.GroupBy(c("g")).Agg(es...).Sort(ursus.Asc(c("g"))).Drop("g")
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"as they come", byG(text(c("x").Mode(true)), text(c("s").Mode(true)), text(c("b").Mode(true)), text(c("d").Mode(true)),
			c("i").Mode(true).First().Alias("i1")),
			"x,s,b,d,i1\n3.0|null,b|a,true,1970-01-06|1970-01-08,5\n-0.0,z,null,1970-01-02|1970-01-03|1970-01-04,1\n" +
				"NaN|1.0,null|q,false|true,1970-01-10|1970-01-09,9\n"},
		{"sorted", byG(text(c("x").Mode(false)), text(c("s").Mode(false)), text(c("b").Mode(false)), text(c("d").Mode(false))),
			"x,s,b,d\nnull|3.0,a|b,true,1970-01-06|1970-01-08\n-0.0,z,null,1970-01-02|1970-01-03|1970-01-04\n" +
				"1.0|NaN,null|q,false|true,1970-01-09|1970-01-10\n"},
		{"over the frame", lf.Select(c("s").Mode(true).First(), c("i").Mode(true).Last()), "s,i\nb,8\n"},
		{"in a dynamic group", lf.WithRowIndex("r", 0).WithColumns(c("r").Cast(ursus.Int64)).GroupByDynamic(c("r"),
			ursus.DynamicOptions{Every: ursus.Every("4i")}).Agg(text(c("i").Mode(true))).Select(c("i")),
			"i\n5|7\n1|2|3|9\n8\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// The aggregate, not an implode.
	plan, err := lf.GroupBy(c("g")).Agg(c("x").Mode(true)).Explain(t.Context())
	if err != nil || !strings.Contains(plan, "mode(maintain_order=true)") || strings.Contains(plan, "implode") {
		t.Errorf("the plan of a mode by g:\n%s\n%v", plan, err)
	}
	// A mode of a reshape keeps the list kernel: it reads the reshape's list.
	got := csvOf(t, lf.GroupBy(c("g")).Agg(text(c("i").Sort().Mode(true))).Sort(ursus.Asc(c("g"))).Select(c("i")))
	if got != "i\n5|7\n1|2|3\n8|9\n" {
		t.Errorf("a mode of a sort: %q", got)
	}
	// The order the values first appear is the rows', on four threads too: each value
	// first in a batch of its own, and every value as frequent.
	const batches = 40
	vs := make([]int64, 0, batches*64)
	for b := range batches {
		for i := range 64 {
			vs = append(vs, int64((b+i/32)%batches))
		}
	}
	firsts := ursus.Frame(ursus.Values("v", vs)).GroupBy().Agg(c("v").Mode(true).Alias("m"))
	for _, threads := range []int{1, 4} {
		got, err := firsts.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithThreads(threads))
		if err != nil {
			t.Fatal(err)
		}
		rows, _ := got.ToMaps()
		if s := fmt.Sprint(rows[0]["m"]); !strings.HasPrefix(s, "[0 1 2 3 4 5 6 7 8 9 10 ") {
			t.Errorf("first-seen order on %d threads: %s", threads, s)
		}
	}
	if _, err := lf.GroupBy(c("g")).Agg(ursus.Struct(c("x")).Mode(true)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "cannot be a key") {
		t.Errorf("a mode of structs: %v", err)
	}
}

// TestAHotGroupsModeSpillsItsCounts is step 340: a mode over five hot keys, and over
// none, kept every value of a group, imploded, and refused past a limit. Now it keeps
// an entry a distinct value, which a few values a group keep under the limit, and
// past it the entries spill, sorted by group and value, and are read back twice:
// once for each group's modes, once for their values. Each answer is the one without
// a limit: over 997 integers and 1999 strings with ties of the most rows, all-
// distinct integers, every one a mode, integers and text with nulls, and a float with
// NaN, ±Inf and ±0; keyed, maintaining order or not, and keyless; on one thread and four,
// under 8 KiB; and in a dynamic group under 32 KiB.
func TestAHotGroupsModeSpillsItsCounts(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)
	c := ursus.Col
	f := c("seq").Mod(int64(5)).Sub(int64(2)).Cast(dtype.Float64).
		Div(c("seq").Mod(int64(3)).Sub(int64(1)).Cast(dtype.Float64))
	aggs := []ursus.Expr{
		c("seq").Mod(int64(997)).Mode(true).Alias("m997"),
		c("seq").Mod(int64(997)).Mode(false).Alias("s997"),
		c("seq").Mod(int64(1999)).Cast(ursus.String).Mode(true).First().Alias("t1999"),
		c("seq").Mode(false).List().Len().Alias("all"),
		c("opt").Mode(true).Alias("opt"),
		c("opt").Cast(ursus.String).Mode(true).Alias("topt"),
		f.Mode(true).Alias("f"),
		f.Mode(false).Alias("fs"),
		c("seq").Sum().Alias("sum"),
	}
	run := func(t *testing.T, name string, limit int64, q func() *ursus.LazyFrame, ordered bool) {
		t.Helper()
		want, err := q().Collect(t.Context(), ursus.WithBatchSize(512))
		if err != nil {
			t.Fatal(err)
		}
		for _, threads := range []int{1, 4} {
			var stats ursus.MemoryStats
			dir := t.TempDir()
			got, err := q().Collect(t.Context(), ursus.WithBatchSize(512),
				ursus.WithThreads(threads), ursus.WithVerify(),
				ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir),
				ursus.WithMemoryStats(&stats))
			if err != nil {
				t.Fatalf("%s on %d threads: %v", name, threads, err)
			}
			if stats.Spills == 0 {
				t.Errorf("%s on %d threads: nothing spilled under %d KiB", name, threads, limit>>10)
			}
			if ordered {
				ursustest.AssertFrameEqual(t, got, want)
			} else {
				ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
			}
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("%s on %d threads: %d entries left in the spill directory", name, threads, len(left))
			}
		}
	}
	run(t, "keyed", 8<<10, func() *ursus.LazyFrame { return ursus.Scan(src).GroupBy(c("grp")).Agg(aggs...) }, false)
	run(t, "ordered", 8<<10, func() *ursus.LazyFrame { return ursus.Scan(src).GroupBy(c("grp")).MaintainOrder().Agg(aggs...) }, true)
	run(t, "keyless", 8<<10, func() *ursus.LazyFrame { return ursus.Scan(src).GroupBy().Agg(aggs...) }, false)
	run(t, "in a dynamic group", 32<<10, func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupByDynamic(c("seq"), ursus.DynamicOptions{Every: ursus.Every("5000i")}).
			Agg(c("seq").Mod(int64(997)).Mode(true).Alias("m997"), f.Mode(false).Alias("f"))
	}, true)
}
