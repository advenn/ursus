package ursus_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// ints is a List(Int64) column of rows, an element nil where it is null, a row nil
// where the list is.
func ints(name string, rows [][]*int64) *data.Column {
	var vals []int64
	var ok []bool
	lens, present := make([]int, len(rows)), make([]bool, len(rows))
	for i, r := range rows {
		present[i], lens[i] = r != nil, len(r)
		for _, v := range r {
			if v == nil {
				vals, ok = append(vals, 0), append(ok, false)
			} else {
				vals, ok = append(vals, *v), append(ok, true)
			}
		}
	}
	return listColumn(name, data.NewFixed("e", dtype.Int64, vals, validOf(ok)), lens, present)
}

func iv(v int64) *int64 { return &v }

// floatList is a one-row List(Float64) column.
func floatList(name string, v ...float64) *data.Column {
	return listColumn(name, data.NewFixed("e", dtype.Float64, v, bitmap.View{}), []int{len(v)}, nil)
}

// assertOne checks got's one column against a frame of want.
func assertOne(t *testing.T, got *ursus.DataFrame, want *data.Column) {
	t.Helper()
	g, err := got.Select(t.Context(), ursus.Col(want.Name()))
	if err != nil {
		t.Fatal(err)
	}
	w, err := frameOf(t, want).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, g, w)
}

// TestAListsSetOperationsAnswerAsPolars: the four list set operations (step 223),
// against Polars 1.44.1's answers: the first list's order, then the second's; a null
// element as a value; an empty list; a null list on either side; and NaN and −0
// among floats, where the first list's −0 is kept.
func TestAListsSetOperationsAnswerAsPolars(t *testing.T) {
	n := (*int64)(nil)
	a := ints("a", [][]*int64{{iv(1), iv(2), iv(2), iv(3), n}, {}, nil, {iv(5), iv(4)},
		{iv(1), n}, {iv(7)}, {iv(3), iv(1), iv(2)}})
	b := ints("b", [][]*int64{{iv(3), iv(4), n, iv(4)}, {iv(1)}, {iv(1)}, nil,
		{n, n}, {}, {iv(2), iv(9), iv(1)}})
	c := ursus.Col
	got, err := frameOf(t, a, b).Select(
		c("a").List().SetUnion(c("b")).Alias("union"),
		c("a").List().SetIntersection(c("b")).Alias("inter"),
		c("a").List().SetDifference(c("b")).Alias("diff"),
		c("a").List().SetSymmetricDifference(c("b")).Alias("sym"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, rows := range map[string][][]*int64{
		"union": {{iv(1), iv(2), iv(3), n, iv(4)}, {iv(1)}, nil, nil, {iv(1), n}, {iv(7)},
			{iv(3), iv(1), iv(2), iv(9)}},
		"inter": {{iv(3), n}, {}, nil, nil, {n}, {}, {iv(1), iv(2)}},
		"diff":  {{iv(1), iv(2)}, {}, nil, nil, {iv(1)}, {iv(7)}, {iv(3)}},
		"sym":   {{iv(1), iv(2), iv(4)}, {iv(1)}, nil, nil, {iv(1)}, {iv(7)}, {iv(3), iv(9)}},
	} {
		assertOne(t, got, ints(name, rows))
	}

	// Floats: NaN equals NaN, −0 equals +0, and the first list's −0 is kept.
	negZero := math.Copysign(0, -1)
	fl, err := frameOf(t, floatList("a", math.NaN(), negZero, 1), floatList("b", 0, math.NaN())).
		Select(c("a").List().SetIntersection(c("b")).Alias("i"),
			c("a").List().SetUnion(c("b")).Alias("u")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertOne(t, fl, floatList("i", math.NaN(), negZero))
	assertOne(t, fl, floatList("u", math.NaN(), negZero, 1))

	// An Int32 list and a Float64 list meet at Float64, as Polars has it; an Int64
	// list and a Float64 list are refused, as a join of the two is.
	i32 := listColumn("a", data.NewFixed("e", dtype.Int32, []int32{1, 2}, bitmap.View{}), []int{2}, nil)
	mixed, err := frameOf(t, i32, floatList("b", 2.5, 1)).
		Select(c("a").List().SetUnion(c("b")).Alias("u")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertOne(t, mixed, floatList("u", 1, 2, 2.5))
	_, err = frameOf(t, ints("a", [][]*int64{{iv(1)}}), floatList("b", 1)).
		Select(c("a").List().SetUnion(c("b"))).Collect(t.Context())
	if !errors.Is(err, uerr.ErrType) || !strings.Contains(err.Error(), "list.set_union") {
		t.Errorf("an Int64 and a Float64 list should be refused, naming the function: %v", err)
	}
}

// TestAListsGatherAnswersAsPolars: list.gather (step 223), against Polars 1.44.1:
// negative indices from the end, a null index a null element, a null list or null
// indices a null list; out of range a null with nullOnOOB, and otherwise a refusal
// naming the function and the index.
func TestAListsGatherAnswersAsPolars(t *testing.T) {
	n := (*int64)(nil)
	a := ints("a", [][]*int64{{iv(10), iv(20), iv(30)}, {}, nil, {iv(1), iv(2)}, {iv(10), iv(20), iv(30)}})
	idx := ints("i", [][]*int64{{iv(2), iv(-1), iv(0), iv(5)}, {iv(0)}, {iv(0)}, nil, {n, iv(1), iv(-3)}})
	c := ursus.Col
	got, err := frameOf(t, a, idx).Select(c("a").List().Gather(c("i"), true).Alias("g")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := frameOf(t, ints("g", [][]*int64{{iv(30), iv(30), iv(10), n}, {n}, nil, nil,
		{n, iv(20), iv(10)}})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	_, err = frameOf(t, a, idx).Select(c("a").List().Gather(c("i"), false)).Collect(t.Context())
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "list.gather") ||
		!strings.Contains(err.Error(), "index 5") {
		t.Errorf("an index out of range should be refused, naming it: %v", err)
	}

	// In range, the strict gather answers as the other.
	inRange := ints("i", [][]*int64{{iv(2), iv(-1)}, {}, {iv(0)}, nil, {n, iv(1)}})
	strict, err := frameOf(t, a, inRange).Select(c("a").List().Gather(c("i"), false).Alias("g")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	lax, err := frameOf(t, a, inRange).Select(c("a").List().Gather(c("i"), true).Alias("g")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, strict, lax)
}

// TestAListsSetOperationsReadSlicedLists: read in batches of one and three, each a
// slice of the lists' children, the set operations and gather answer as in one
// batch.
func TestAListsSetOperationsReadSlicedLists(t *testing.T) {
	n := (*int64)(nil)
	a := ints("a", [][]*int64{{iv(1), iv(2), iv(2)}, {iv(3), n}, {}, nil, {iv(4), iv(5), iv(6)},
		{iv(7), iv(7)}, {iv(1)}})
	b := ints("b", [][]*int64{{iv(2), iv(9)}, {n}, {iv(1)}, {iv(2)}, {iv(6), iv(4)}, {}, {iv(1), iv(1)}})
	idx := ints("i", [][]*int64{{iv(-1)}, {iv(0), iv(1)}, {}, {iv(0)}, {iv(2), iv(0)}, {iv(1)}, {iv(0)}})
	c := ursus.Col
	q := frameOf(t, a, b, idx).Select(
		c("a").List().SetUnion(c("b")).Alias("u"), c("a").List().SetIntersection(c("b")).Alias("x"),
		c("a").List().SetDifference(c("b")).Alias("d"),
		c("a").List().SetSymmetricDifference(c("b")).Alias("s"),
		c("a").List().Gather(c("i"), false).Alias("g"))
	whole, err := q.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 3} {
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(size))
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, whole)
	}
}

// TestAGatherRefusalIsNotPushedPastAJoin: list.gather refuses an index out of range,
// so, like a strict cast, it must not run on rows the join removes before it
// (internal/plan/fallible.go). Pushed into the join's left side, it meets the row it
// refuses.
func TestAGatherRefusalIsNotPushedPastAJoin(t *testing.T) {
	left := frameOf(t, ints("a", [][]*int64{{iv(1), iv(2)}, {iv(3)}}),
		ints("i", [][]*int64{{iv(1)}, {iv(4)}}),
		data.NewFixed("k", dtype.Int64, []int64{1, 2}, bitmap.View{}))
	right := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("r", []int64{9}))
	c := ursus.Col
	df, err := left.Join(right, ursus.JoinOn(c("k"))).
		Filter(c("a").List().Gather(c("i"), false).List().Len().Gt(uint32(0))).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Errorf("%d rows, want 1", df.Height())
	}
}
