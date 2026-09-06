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
