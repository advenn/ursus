package ursus_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/ursustest"
)

// listColumn makes a List column of child's elements, lens[i] of them a row; a row
// not present is a null list.
func listColumn(name string, child *data.Column, lens []int, present []bool) *data.Column {
	offs := make([]int32, len(lens)+1)
	for i, n := range lens {
		offs[i+1] = offs[i] + int32(n)
	}
	ok := bitmap.NewBuilder(len(lens))
	for _, p := range present {
		ok.Append(p)
	}
	return data.NewList(name, offs, child, ok.Finish())
}

func validOf(ok []bool) bitmap.View {
	b := bitmap.NewBuilder(len(ok))
	for _, v := range ok {
		b.Append(v)
	}
	return b.Finish()
}

// frameOf scans one batch of cols.
func frameOf(t *testing.T, cols ...*data.Column) *ursus.LazyFrame {
	t.Helper()
	fields := make([]dtype.Field, len(cols))
	for i, c := range cols {
		fields[i] = dtype.Of(c.Name(), c.DType())
	}
	sch, err := dtype.NewSchema(fields...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(sch, cols)
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(sch, b)
	if err != nil {
		t.Fatal(err)
	}
	return ursus.Scan(src)
}

// TestAListsNUniqueAndArgExtremaAnswerAsPolars: list.n_unique, list.arg_min and
// list.arg_max (step 222), against Polars 1.44's answers, with the two places ursus
// differs because its column aggregates do: n_unique skips a null element, where
// Polars counts it, and NaN is the largest value, where Polars' arg_max skips it.
// Over integers with nulls, an empty list, a null list and a list of nulls; floats
// with NaN and ±0; strings.
func TestAListsNUniqueAndArgExtremaAnswerAsPolars(t *testing.T) {
	ints := listColumn("a", data.NewFixed("e", dtype.Int64,
		[]int64{3, 0, 1, 1, 0, 0, 0, 5, 4, 4},
		validOf([]bool{true, false, true, true, false, false, false, true, true, true})),
		[]int{5, 0, 0, 2, 3}, []bool{true, true, false, true, true})
	floats := listColumn("f", data.NewFixed("e", dtype.Float64,
		[]float64{2, math.NaN(), 1, math.NaN(), math.Copysign(0, -1), 0, 7, 7, 7, 7}, bitmap.View{}),
		[]int{3, 1, 2, 4, 0}, nil)
	strs := listColumn("s", data.NewString("e", []string{"b", "a", "", "a"},
		validOf([]bool{true, true, false, true})), []int{4, 0, 0, 0, 0},
		[]bool{true, false, true, true, true})
	c := ursus.Col
	got, err := frameOf(t, ints, floats, strs).Select(
		c("a").List().NUnique().Alias("a_nu"), c("a").List().ArgMin().Alias("a_min"),
		c("a").List().ArgMax().Alias("a_max"),
		c("f").List().NUnique().Alias("f_nu"), c("f").List().ArgMin().Alias("f_min"),
		c("f").List().ArgMax().Alias("f_max"),
		c("s").List().NUnique().Alias("s_nu"), c("s").List().ArgMin().Alias("s_min"),
		c("s").List().ArgMax().Alias("s_max"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	u := func(name string, v []uint64, ok []bool) *ursus.Column {
		if ok == nil {
			return ursus.Values(name, v)
		}
		return ursus.ValuesNullable(name, v, ok)
	}
	want, err := ursus.Frame(
		// Polars: [3, 0, null, 1, 2]; ursus skips the nulls of rows 0 and 3.
		u("a_nu", []uint64{2, 0, 0, 0, 2}, []bool{true, true, false, true, true}),
		u("a_min", []uint64{2, 0, 0, 0, 1}, []bool{true, false, false, false, true}),
		u("a_max", []uint64{0, 0, 0, 0, 0}, []bool{true, false, false, false, true}),
		// Polars: n_unique [3, 1, 1, 1, 0], arg_min [2, 0, 0, 0, null], arg_max
		// [0, 0, 0, 0, null]; ursus's NaN is the largest, so row 0's arg_max is 1.
		u("f_nu", []uint64{3, 1, 1, 1, 0}, nil),
		u("f_min", []uint64{2, 0, 0, 0, 0}, []bool{true, true, true, true, false}),
		u("f_max", []uint64{1, 0, 0, 0, 0}, []bool{true, true, true, true, false}),
		// Polars: [3, null, 0, 0, 0]; ursus skips row 0's null.
		u("s_nu", []uint64{2, 0, 0, 0, 0}, []bool{true, false, true, true, true}),
		u("s_min", []uint64{1, 0, 0, 0, 0}, []bool{true, false, false, false, false}),
		u("s_max", []uint64{0, 0, 0, 0, 0}, []bool{true, false, false, false, false}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// TestAListsAggregatesAreItsElementsAggregates: over random lists of floats with
// nulls, NaN, ±0 and repeats, sliced at random, list.n_unique, list.arg_min and
// list.arg_max of each present list answer as NUnique, ArgMin and ArgMax over its
// elements do, exploded and grouped by row.
func TestAListsAggregatesAreItsElementsAggregates(t *testing.T) {
	rng := rand.New(rand.NewPCG(222, 1))
	pool := []float64{math.NaN(), 0, math.Copysign(0, -1), 1, 1, 2, -3, math.Inf(1)}
	const rows = 300
	lens, present := make([]int, rows), make([]bool, rows)
	var vals []float64
	var ok []bool
	for i := range rows {
		present[i] = rng.IntN(10) != 0
		lens[i] = 1 + rng.IntN(8)
		if !present[i] {
			lens[i] = 0
		}
		for range lens[i] {
			vals = append(vals, pool[rng.IntN(len(pool))])
			ok = append(ok, rng.IntN(5) != 0)
		}
	}
	ids := make([]int64, rows)
	for i := range ids {
		ids[i] = int64(i)
	}
	lists := listColumn("l", data.NewFixed("e", dtype.Float64, vals, validOf(ok)), lens, present)
	f := frameOf(t, data.NewFixed("id", dtype.Int64, ids, bitmap.View{}), lists).
		Filter(ursus.Col("l").IsNotNull())
	c := ursus.Col
	byList, err := f.Select(c("id"), c("l").List().NUnique().Alias("nu"),
		c("l").List().ArgMin().Alias("amin"), c("l").List().ArgMax().Alias("amax")).
		Collect(t.Context(), ursus.WithBatchSize(17))
	if err != nil {
		t.Fatal(err)
	}
	byElem, err := f.Explode("l").GroupBy(c("id")).MaintainOrder().
		Agg(c("l").NUnique().Alias("nu"), c("l").ArgMin().Alias("amin"),
			c("l").ArgMax().Alias("amax")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if byList.Height() < 200 {
		t.Fatalf("%d lists, too few to mean anything", byList.Height())
	}
	ursustest.AssertFrameEqual(t, byList, byElem)
}
