package ursus_test

// The `.list` namespace, which is what lets a caller work with a list without
// exploding it.
//
// Almost all of the risk here is one distinction: a NULL list is not an EMPTY
// list. They hold the same elements — none — and differ only in a validity bit,
// so working from "both are empty" gives wrong answers that never error.

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// nsFixture: [10,20] | [] | null | [5]
func nsFixture(t *testing.T) string {
	t.Helper()
	return writeListFile(t,
		[]int64{1, 2, 3, 4},
		[][]int64{{10, 20}, {}, nil, {5}},
		[]bool{true, true, false, true},
	)
}

// TestListNamespaceSemantics is the table from the package doc, every cell.
//
// It is one test rather than several because the cells are only meaningful
// against each other: `Len` of an empty list being 0 matters precisely because
// `Len` of a null list is null, and checking either alone proves little.
func TestListNamespaceSemantics(t *testing.T) {
	path := nsFixture(t)

	for _, size := range []int{1, 2, 3, 8192} {
		df, err := ursus.ScanParquet(path).Select(
			ursus.Col("tags").List().Len().Alias("n"),
			ursus.Col("tags").List().Sum().Alias("sum"),
			ursus.Col("tags").List().Min().Alias("min"),
			ursus.Col("tags").List().Max().Alias("max"),
			ursus.Col("tags").List().Get(0).Alias("first"),
			ursus.Col("tags").List().Contains(int64(20)).Alias("has20"),
		).Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}

		// row 0 [10,20] | row 1 [] | row 2 null | row 3 [5]
		wantLen := []struct {
			v  uint32
			ok bool
		}{{2, true}, {0, true}, {0, false}, {1, true}}
		for i, w := range wantLen {
			v, ok, err := df.At[uint32](i, "n")
			if err != nil {
				t.Fatal(err)
			}
			if ok != w.ok || (ok && v != w.v) {
				t.Errorf("batch %d row %d: len = (%d, valid %v), want (%d, valid %v)\n%s",
					size, i, v, ok, w.v, w.ok, df)
			}
		}

		// Sum of an EMPTY list is NULL, not 0 — see the note in expr_list.go. polars
		// answers 0 here; ursus's own Sum answers null for a group with no values,
		// and agreeing with the aggregate beside it matters more than agreeing with
		// polars.
		for i, w := range []struct {
			v  int64
			ok bool
		}{{30, true}, {0, false}, {0, false}, {5, true}} {
			v, ok, err := df.At[ursus.Int128Value](i, "sum")
			if err != nil {
				t.Fatal(err)
			}
			if ok != w.ok {
				t.Errorf("batch %d row %d: sum valid = %v, want %v\n%s",
					size, i, ok, w.ok, df)
				continue
			}
			if got, fits := v.Int64(); ok && (!fits || got != w.v) {
				t.Errorf("batch %d row %d: sum = %s, want %d\n%s", size, i, v, w.v, df)
			}
		}

		// Min of an EMPTY list is null — there is no smallest element of nothing —
		// which is where it parts company with Sum.
		for i, w := range []struct {
			v  int64
			ok bool
		}{{10, true}, {0, false}, {0, false}, {5, true}} {
			v, ok, err := df.At[int64](i, "min")
			if err != nil {
				t.Fatal(err)
			}
			if ok != w.ok || (ok && v != w.v) {
				t.Errorf("batch %d row %d: min = (%d, valid %v), want (%d, valid %v)\n%s",
					size, i, v, ok, w.v, w.ok, df)
			}
		}

		// Get(0): null for both the empty and the null list, for different reasons.
		for i, w := range []struct {
			v  int64
			ok bool
		}{{10, true}, {0, false}, {0, false}, {5, true}} {
			v, ok, err := df.At[int64](i, "first")
			if err != nil {
				t.Fatal(err)
			}
			if ok != w.ok || (ok && v != w.v) {
				t.Errorf("batch %d row %d: get(0) = (%d, valid %v), want (%d, valid %v)\n%s",
					size, i, v, ok, w.v, w.ok, df)
			}
		}

		// Contains: an EMPTY list answers FALSE — a real answer to a real question —
		// where a NULL list answers null.
		for i, w := range []struct {
			v  bool
			ok bool
		}{{true, true}, {false, true}, {false, false}, {false, true}} {
			v, ok, err := df.At[bool](i, "has20")
			if err != nil {
				t.Fatal(err)
			}
			if ok != w.ok || (ok && v != w.v) {
				t.Errorf("batch %d row %d: contains(20) = (%v, valid %v), want (%v, valid %v)\n%s",
					size, i, v, ok, w.v, w.ok, df)
			}
		}
	}
}

// TestListGetIndexing pins the index arithmetic, where an off-by-one is silent.
func TestListGetIndexing(t *testing.T) {
	path := writeListFile(t,
		[]int64{1, 2},
		[][]int64{{10, 20, 30}, {7}},
		[]bool{true, true},
	)
	df, err := ursus.ScanParquet(path).Select(
		ursus.Col("tags").List().Get(1).Alias("at1"),
		ursus.Col("tags").List().Get(-1).Alias("last"),
		ursus.Col("tags").List().Get(-3).Alias("backthree"),
		ursus.Col("tags").List().Get(9).Alias("past"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	// A fixture of single-element rows would pass with Get(-1) indexing from the
	// front, which is why row 0 has three elements.
	for _, c := range []struct {
		col  string
		want []int64
		ok   []bool
	}{
		{"at1", []int64{20, 0}, []bool{true, false}},       // row 1 has no index 1
		{"last", []int64{30, 7}, []bool{true, true}},       // -1 is the END
		{"backthree", []int64{10, 0}, []bool{true, false}}, // -3 of a 1-list is out
		{"past", []int64{0, 0}, []bool{false, false}},      // past the end is null
	} {
		for i := range 2 {
			v, ok, err := df.At[int64](i, c.col)
			if err != nil {
				t.Fatal(err)
			}
			if ok != c.ok[i] || (ok && v != c.want[i]) {
				t.Errorf("%s row %d = (%d, valid %v), want (%d, valid %v)\n%s",
					c.col, i, v, ok, c.want[i], c.ok[i], df)
			}
		}
	}
}

// TestListNamespaceOnStrings: the namespace is not integer-only. Min and Max
// order strings, and Contains compares them.
func TestListNamespaceOnStrings(t *testing.T) {
	path := writeStringListFile(t,
		[]int64{1, 2},
		[][]string{{"go", "rust", "c"}, {}},
	)
	df, err := ursus.ScanParquet(path).Select(
		ursus.Col("tags").List().Len().Alias("n"),
		ursus.Col("tags").List().Min().Alias("min"),
		ursus.Col("tags").List().Contains("rust").Alias("hasrust"),
		ursus.Col("tags").List().Get(0).Alias("first"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if n, _, _ := df.At[uint32](0, "n"); n != 3 {
		t.Errorf("len = %d, want 3\n%s", n, df)
	}
	if v, _, _ := df.At[string](0, "min"); v != "c" {
		t.Errorf("min = %q, want \"c\"\n%s", v, df)
	}
	if v, _, _ := df.At[bool](0, "hasrust"); !v {
		t.Errorf("contains(\"rust\") = false\n%s", df)
	}
	if v, _, _ := df.At[string](0, "first"); v != "go" {
		t.Errorf("get(0) = %q, want \"go\"\n%s", v, df)
	}
	// The empty row: length 0, min null, contains false.
	if _, ok, _ := df.At[string](1, "min"); ok {
		t.Errorf("min of an empty list should be null\n%s", df)
	}
}

// TestListNamespaceWithoutExploding is the point of the namespace: filtering on a
// property of the list, with the list still intact afterwards.
func TestListNamespaceWithoutExploding(t *testing.T) {
	df, err := ursus.ScanParquet(nsFixture(t)).
		Filter(ursus.Col("tags").List().Len().Gt(uint32(1))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Fatalf("got %d rows, want 1 (only [10,20] has more than one element)\n%s",
			df.Height(), df)
	}
	// The list survives the filter — that is what "without exploding" means.
	if !strings.Contains(df.String(), "[10, 20]") {
		t.Errorf("the list column should still be a list:\n%s", df)
	}
}

// TestListNamespaceRefusals: a non-List receiver is a type error, the way `.dt`
// on a non-temporal is.
func TestListNamespaceRefusals(t *testing.T) {
	path := nsFixture(t)

	_, err := ursus.ScanParquet(path).
		Select(ursus.Col("id").List().Len()).Collect(t.Context())
	if err == nil {
		t.Fatal("`.list` on a non-List column must be refused")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}

	// A value the element type cannot hold is a question with no answer, and
	// saying so beats matching nothing. Int64 elements, asked about a string.
	_, err = ursus.ScanParquet(path).
		Select(ursus.Col("tags").List().Contains("nope")).Collect(t.Context())
	if err == nil {
		t.Fatal("Contains with an incompatible value must be refused")
	}
}

// The reshaping half of the namespace: the seven functions that hand back a LIST.
//
// A null list is the case that keeps needing defence, and here it is the easy one
// — the helper never asks a null list which elements to keep, so it stays null
// without anything having to say so. The table checks that for all seven, since
// "easy" is not the same as "checked".

// reshapeFixture:
//
//	row 0  [3, 1, 2]         ordering
//	row 1  [2, 1, 3, 2]      a duplicate that is NOT adjacent, and a tie to sort
//	row 2  [1, null]         a null ELEMENT
//	row 3  []                empty
//	row 4  null              a null LIST
//	row 5  [null, null, 4]   a REPEATED null
//
// Row 1's duplicate is deliberately not adjacent: with `[2, 1, 1]`, keeping the
// first occurrence and keeping the last give the same answer, and a Unique that
// kept the wrong one would pass.
func reshapeFixture(t *testing.T) string {
	t.Helper()
	return writeElemLists(t, []int64{1, 2, 3, 4, 5, 6}, []lrow{
		{vals: elems(3, 1, 2), ok: true},
		{vals: elems(2, 1, 3, 2), ok: true},
		{vals: []elem{val(1), nullElem}, ok: true},
		{ok: true}, // present but EMPTY
		{},         // NULL
		{vals: []elem{nullElem, nullElem, val(4)}, ok: true},
	})
}

func elems(vs ...int64) []elem {
	out := make([]elem, len(vs))
	for i, v := range vs {
		out[i] = val(v)
	}
	return out
}

// listCell reads one row of a List(Int64) column as its elements.
//
// Reading the column rather than matching rendered text is what lets the table
// below distinguish a null list from an empty one — they render the same width and
// differ only in a validity bit, which is the whole difficulty.
func listCell(t *testing.T, df *ursus.DataFrame, name string, row int) ([]elem, bool) {
	t.Helper()
	c, found := df.Batch().ByName(name)
	if !found {
		t.Fatalf("no column %q in\n%s", name, df)
	}
	if c.DType().ID() != dtype.TypeList {
		t.Fatalf("%s came back as %s: a reshaping call must return a LIST", name, c.DType())
	}
	s, err := data.TypedColumn[int64](c.Child())
	if err != nil {
		t.Fatal(err)
	}
	start, end, valid := c.Lists().Get(row)
	out := []elem{}
	for e := start; e < end; e++ {
		if v, ok := s.Get(int(e)); ok {
			out = append(out, val(v))
		} else {
			out = append(out, nullElem)
		}
	}
	return out, valid
}

func checkList(t *testing.T, df *ursus.DataFrame, col string, row int, want lrow) {
	t.Helper()
	got, valid := listCell(t, df, col, row)
	if valid != want.ok {
		t.Errorf("%s row %d: valid = %v, want %v — a null list and an empty one are "+
			"NOT the same\n%s", col, row, valid, want.ok, df)
		return
	}
	if !valid {
		return // a null list has no elements to compare
	}
	if len(got) != len(want.vals) {
		t.Errorf("%s row %d: %v, want %v\n%s", col, row, got, want.vals, df)
		return
	}
	for i := range got {
		if got[i] != want.vals[i] {
			t.Errorf("%s row %d: %v, want %v\n%s", col, row, got, want.vals, df)
			return
		}
	}
}

// TestListReshapeSemantics is the whole table, every function against every row
// shape, at four batch sizes — the batch sizes because a List that spans batches is
// concatenated, and concatenation has to shift the child offsets.
func TestListReshapeSemantics(t *testing.T) {
	path := reshapeFixture(t)

	cases := []struct {
		col  string
		e    ursus.Expr
		want []lrow
	}{
		{"rev", ursus.Col("tags").List().Reverse(), []lrow{
			{vals: elems(2, 1, 3), ok: true},
			{vals: elems(2, 3, 1, 2), ok: true},
			{vals: []elem{nullElem, val(1)}, ok: true},
			{ok: true},
			{},
			{vals: []elem{val(4), nullElem, nullElem}, ok: true},
		}},
		{"head", ursus.Col("tags").List().Head(2), []lrow{
			{vals: elems(3, 1), ok: true},
			{vals: elems(2, 1), ok: true},
			{vals: []elem{val(1), nullElem}, ok: true},
			{ok: true},
			{},
			{vals: []elem{nullElem, nullElem}, ok: true},
		}},
		{"tail", ursus.Col("tags").List().Tail(2), []lrow{
			{vals: elems(1, 2), ok: true},
			{vals: elems(3, 2), ok: true},
			{vals: []elem{val(1), nullElem}, ok: true},
			{ok: true},
			{},
			{vals: []elem{nullElem, val(4)}, ok: true},
		}},
		{"slice", ursus.Col("tags").List().Slice(1, 2), []lrow{
			{vals: elems(1, 2), ok: true},
			{vals: elems(1, 3), ok: true},
			{vals: []elem{nullElem}, ok: true},
			{ok: true},
			{},
			{vals: []elem{nullElem, val(4)}, ok: true},
		}},
		// Nulls come FIRST, and they come first in BOTH directions: placement is
		// applied before direction, so SortDesc does not silently move them.
		{"sort", ursus.Col("tags").List().Sort(), []lrow{
			{vals: elems(1, 2, 3), ok: true},
			{vals: elems(1, 2, 2, 3), ok: true},
			{vals: []elem{nullElem, val(1)}, ok: true},
			{ok: true},
			{},
			{vals: []elem{nullElem, nullElem, val(4)}, ok: true},
		}},
		{"desc", ursus.Col("tags").List().SortDesc(), []lrow{
			{vals: elems(3, 2, 1), ok: true},
			{vals: elems(3, 2, 2, 1), ok: true},
			{vals: []elem{nullElem, val(1)}, ok: true},
			{ok: true},
			{},
			{vals: []elem{nullElem, nullElem, val(4)}, ok: true},
		}},
		// Unique keeps the FIRST occurrence and leaves the order alone. Row 1 is
		// where that is visible: keeping the last would give [1, 3, 2].
		{"uniq", ursus.Col("tags").List().Unique(), []lrow{
			{vals: elems(3, 1, 2), ok: true},
			{vals: elems(2, 1, 3), ok: true},
			{vals: []elem{val(1), nullElem}, ok: true},
			{ok: true},
			{},
			{vals: []elem{nullElem, val(4)}, ok: true}, // one null survives
		}},
		{"drop", ursus.Col("tags").List().DropNulls(), []lrow{
			{vals: elems(3, 1, 2), ok: true},
			{vals: elems(2, 1, 3, 2), ok: true},
			{vals: elems(1), ok: true},
			{ok: true},
			{}, // still NULL, not emptied
			{vals: elems(4), ok: true},
		}},
	}

	for _, size := range []int{1, 2, 4, 8192} {
		sel := make([]ursus.Expr, len(cases))
		for i, c := range cases {
			sel[i] = c.e.Alias(c.col)
		}
		df, err := ursus.ScanParquet(path).Select(sel...).
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		if df.Height() != 6 {
			t.Fatalf("batch size %d: %d rows, want 6\n%s", size, df.Height(), df)
		}
		for _, c := range cases {
			for row, want := range c.want {
				checkList(t, df, c.col, row, want)
			}
		}
	}
}

// TestListReshapeEdges: the clamping, which is where an off-by-one hides.
func TestListReshapeEdges(t *testing.T) {
	path := writeListFile(t, []int64{1}, [][]int64{{10, 20, 30}}, []bool{true})

	df, err := ursus.ScanParquet(path).Select(
		ursus.Col("tags").List().Head(0).Alias("h0"),
		ursus.Col("tags").List().Head(99).Alias("h99"),
		ursus.Col("tags").List().Tail(0).Alias("t0"),
		ursus.Col("tags").List().Tail(99).Alias("t99"),
		ursus.Col("tags").List().Slice(-2, 1).Alias("back"),
		ursus.Col("tags").List().Slice(1, 99).Alias("rest"),
		ursus.Col("tags").List().Slice(9, 2).Alias("past"),
		ursus.Col("tags").List().Slice(-99, 2).Alias("before"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		col  string
		want []elem
	}{
		{"h0", nil},                // asking for none gives an EMPTY list, not null
		{"h99", elems(10, 20, 30)}, // past the end is the whole list, not an error
		{"t0", nil},                //
		{"t99", elems(10, 20, 30)}, //
		{"back", elems(20)},        // -2 is measured from the end
		{"rest", elems(20, 30)},    // a length past the end stops there
		{"past", nil},              // an offset past the end selects nothing
		{"before", elems(10, 20)},  // an offset before the start clamps to it
	} {
		checkList(t, df, c.col, 0, lrow{vals: c.want, ok: true})
	}
}

// TestListSortOnStrings: sorting goes through the same Comparator a column-wide
// Sort uses, which is why it is not integer-only — the order-key path the radix
// sort takes cannot handle strings at all.
func TestListSortOnStrings(t *testing.T) {
	path := writeStringListFile(t,
		[]int64{1, 2},
		[][]string{{"rust", "c", "go"}, {}},
	)
	df, err := ursus.ScanParquet(path).Select(
		ursus.Col("tags").List().Sort().Alias("s"),
		ursus.Col("tags").List().SortDesc().Alias("d"),
		ursus.Col("tags").List().Reverse().Alias("r"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		col  string
		want []string
	}{
		{"s", []string{"c", "go", "rust"}},
		{"d", []string{"rust", "go", "c"}},
		{"r", []string{"go", "c", "rust"}},
	} {
		col, ok := df.Batch().ByName(c.col)
		if !ok {
			t.Fatalf("no column %q\n%s", c.col, df)
		}
		s, err := data.TypedColumn[string](col.Child())
		if err != nil {
			t.Fatal(err)
		}
		start, end, _ := col.Lists().Get(0)
		var got []string
		for e := start; e < end; e++ {
			v, _ := s.Get(int(e))
			got = append(got, v)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s = %v, want %v\n%s", c.col, got, c.want, df)
		}
	}
}

// TestListReshapeChains: the output is a real List, so the reducing half of the
// namespace works on it and the functions compose with each other.
//
// Each step reopens the namespace — `.List().Sort().List().Head(2)` — because a
// method has to return Expr for the rest of the language to reach it. polars reads
// the same way for the same reason.
func TestListReshapeChains(t *testing.T) {
	df, err := ursus.ScanParquet(reshapeFixture(t)).Select(
		ursus.Col("tags").List().Sort().List().Head(2).List().Len().Alias("n"),
		ursus.Col("tags").List().Sort().List().Head(2).List().Get(0).Alias("smallest"),
		ursus.Col("tags").List().DropNulls().List().Len().Alias("present"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	// row 0 [3,1,2] | 1 [2,1,3,2] | 2 [1,null] | 3 [] | 4 null | 5 [null,null,4]
	for i, w := range []struct {
		n  uint32
		ok bool
	}{{2, true}, {2, true}, {2, true}, {0, true}, {0, false}, {2, true}} {
		v, ok, err := df.At[uint32](i, "n")
		if err != nil {
			t.Fatal(err)
		}
		if ok != w.ok || (ok && v != w.n) {
			t.Errorf("row %d: len = (%d, valid %v), want (%d, valid %v)\n%s",
				i, v, ok, w.n, w.ok, df)
		}
	}
	// Sorted ascending with nulls first, so element 0 is null wherever the row has
	// one — Head did not drop it and Get did not skip it.
	for i, w := range []struct {
		v  int64
		ok bool
	}{{1, true}, {1, true}, {0, false}, {0, false}, {0, false}, {0, false}} {
		v, ok, err := df.At[int64](i, "smallest")
		if err != nil {
			t.Fatal(err)
		}
		if ok != w.ok || (ok && v != w.v) {
			t.Errorf("row %d: smallest = (%d, valid %v), want (%d, valid %v)\n%s",
				i, v, ok, w.v, w.ok, df)
		}
	}
	for i, w := range []struct {
		n  uint32
		ok bool
	}{{3, true}, {4, true}, {1, true}, {0, true}, {0, false}, {1, true}} {
		v, ok, err := df.At[uint32](i, "present")
		if err != nil {
			t.Fatal(err)
		}
		if ok != w.ok || (ok && v != w.n) {
			t.Errorf("row %d: present = (%d, valid %v), want (%d, valid %v)\n%s",
				i, v, ok, w.n, w.ok, df)
		}
	}
}

// TestListReshapeRefusals: a count with no reading is refused rather than guessed
// at. Slice's OFFSET is the one place a negative number means something.
func TestListReshapeRefusals(t *testing.T) {
	path := reshapeFixture(t)

	for _, c := range []struct {
		what string
		e    ursus.Expr
	}{
		{"Head(-1)", ursus.Col("tags").List().Head(-1)},
		{"Tail(-1)", ursus.Col("tags").List().Tail(-1)},
		{"Slice(0, -1)", ursus.Col("tags").List().Slice(0, -1)},
	} {
		_, err := ursus.ScanParquet(path).Select(c.e).Collect(t.Context())
		if err == nil {
			t.Errorf("%s must be refused", c.what)
			continue
		}
		if !errors.Is(err, uerr.ErrValue) {
			t.Errorf("%s: kind should be Value: %v", c.what, err)
		}
	}

	// A non-List receiver, the same refusal the reducing half gives.
	_, err := ursus.ScanParquet(path).Select(ursus.Col("id").List().Reverse()).
		Collect(t.Context())
	if err == nil {
		t.Fatal("`.list.reverse` on a non-List column must be refused")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}
}
