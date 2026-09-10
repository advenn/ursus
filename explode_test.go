package ursus_test

// Explode is what makes a List column useful rather than merely readable: after
// it, every kernel and aggregate that already exists applies to the elements.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

// TestExplodeRowShapes covers the three row shapes at once.
//
// The two that are easy to get wrong are the middle ones: an empty list and a
// null list each produce ONE row holding a null. Dropping them would lower the
// height silently — nothing errors, and the count is only wrong to someone who
// checks it.
func TestExplodeRowShapes(t *testing.T) {
	path := writeListFile(t,
		[]int64{1, 2, 3, 4},
		[][]int64{{10, 11}, {}, nil, {12}},
		[]bool{true, true, false, true},
	)

	for _, size := range []int{1, 2, 3, 8192} {
		df, err := ursus.ScanParquet(path).Explode("tags").
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		// 2 + 1 + 1 + 1: the empty and the null each contribute a row.
		if df.Height() != 5 {
			t.Fatalf("batch size %d: got %d rows, want 5\n%s", size, df.Height(), df)
		}

		// The element type replaces the list type, and `id` repeats per element.
		wantIDs := []int64{1, 1, 2, 3, 4}
		wantTags := []int64{10, 11, 0, 0, 12}
		wantNull := []bool{false, false, true, true, false}
		for i := range 5 {
			id, _, err := df.At[int64](i, "id")
			if err != nil {
				t.Fatal(err)
			}
			if id != wantIDs[i] {
				t.Errorf("batch %d row %d: id = %d, want %d — the row gather is "+
					"pairing elements with the wrong row\n%s", size, i, id, wantIDs[i], df)
			}
			tag, ok, err := df.At[int64](i, "tags")
			if err != nil {
				t.Fatal(err)
			}
			if ok == wantNull[i] {
				t.Errorf("batch %d row %d: tags valid = %v, want %v\n%s",
					size, i, ok, !wantNull[i], df)
			}
			if ok && tag != wantTags[i] {
				t.Errorf("batch %d row %d: tags = %d, want %d\n%s",
					size, i, tag, wantTags[i], df)
			}
		}
	}
}

// TestExplodeOfAllEmptyKeepsTheHeight: every row is an empty list, so every row
// becomes one null row and the height is UNCHANGED, not zero.
func TestExplodeOfAllEmptyKeepsTheHeight(t *testing.T) {
	path := writeListFile(t,
		[]int64{1, 2, 3},
		[][]int64{{}, {}, {}},
		[]bool{true, true, true},
	)
	df, err := ursus.ScanParquet(path).Explode("tags").Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 {
		t.Fatalf("got %d rows, want 3 — empty lists must not vanish\n%s",
			df.Height(), df)
	}
}

// TestExplodeFeedsTheRestOfTheEngine is the actual point of the operation: the
// elements are ordinary values now, so group-by and sum apply with nothing new.
func TestExplodeFeedsTheRestOfTheEngine(t *testing.T) {
	path := writeListFile(t,
		[]int64{1, 2, 3},
		[][]int64{{10, 20}, {10}, {30, 10}},
		[]bool{true, true, true},
	)
	df, err := ursus.ScanParquet(path).
		Explode("tags").
		DropNulls("tags").
		GroupBy(ursus.Col("tags")).
		Agg(ursus.Col("id").Count().Alias("n")).
		Sort(ursus.Asc(ursus.Col("tags"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("the engine must apply to exploded elements: %v", err)
	}
	// 10 appears three times, 20 once, 30 once.
	if df.Height() != 3 {
		t.Fatalf("got %d groups, want 3\n%s", df.Height(), df)
	}
	n, _, err := df.At[uint64]( /* tags == 10 */ 0, "n") // Count is Uint64
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("tag 10 counted %d times, want 3\n%s", n, df)
	}
}

// TestExplodeRefusesNonList and its siblings pin the errors a caller meets.
func TestExplodeRefusesNonList(t *testing.T) {
	path := writeListFile(t, []int64{1}, [][]int64{{1}}, []bool{true})

	t.Run("not a list", func(t *testing.T) {
		_, err := ursus.ScanParquet(path).Explode("id").Collect(t.Context())
		if err == nil {
			t.Fatal("exploding a non-list column must fail")
		}
		if !errors.Is(err, uerr.ErrType) {
			t.Errorf("kind should be Type: %v", err)
		}
	})

	t.Run("unknown column", func(t *testing.T) {
		_, err := ursus.ScanParquet(path).Explode("nope").Collect(t.Context())
		if err == nil {
			t.Fatal("exploding an unknown column must fail")
		}
		if errors.Is(err, uerr.ErrInternal) {
			t.Errorf("a user mistake reported as an ursus bug: %v", err)
		}
		if !errors.Is(err, uerr.ErrSchema) {
			t.Errorf("kind should be Schema: %v", err)
		}
	})

	t.Run("no columns", func(t *testing.T) {
		_, err := ursus.ScanParquet(path).Explode().Collect(t.Context())
		if err == nil {
			t.Fatal("Explode() with no columns must fail rather than guess")
		}
	})

	t.Run("as an expression", func(t *testing.T) {
		_, err := ursus.ScanParquet(path).
			Select(ursus.Col("tags").Explode()).Collect(t.Context())
		if err == nil {
			t.Fatal("Explode as an expression must be refused")
		}
		if !strings.Contains(err.Error(), "height") {
			t.Errorf("the refusal should say it changes the frame's height: %v", err)
		}
	})
}

// TestExplodeIsBelowAFilter pins the optimizer's fail-closed default.
//
// Pushing a predicate on the exploded column BELOW the explode would be wrong:
// down there the column is still a list, and the predicate is written against the
// element type. All three pushdown rules default to leaving a node they do not
// recognise alone, so this works today by construction — but "it happens to work"
// and "it is guaranteed" are different claims, and only a test makes it the
// second one.
func TestExplodeIsBelowAFilter(t *testing.T) {
	path := writeListFile(t,
		[]int64{1, 2},
		[][]int64{{10, 20}, {30}},
		[]bool{true, true},
	)
	df, err := ursus.ScanParquet(path).
		Explode("tags").
		Filter(ursus.Col("tags").Gt(int64(15))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("a filter above an explode must survive optimisation: %v", err)
	}
	if df.Height() != 2 {
		t.Fatalf("got %d rows, want 2 (20 and 30)\n%s", df.Height(), df)
	}
}

// writeTwoListFile builds two list columns side by side, so multi-column explode
// can be exercised. Both use the standard three-level encoding.
func writeTwoListFile(t *testing.T, a, b [][]int64) string {
	t.Helper()
	opt, req := parquet.Repetitions.Optional, parquet.Repetitions.Required

	listNode := func(name string) schema.Node {
		el, err := schema.NewPrimitiveNodeLogical("element", opt,
			schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
		if err != nil {
			t.Fatal(err)
		}
		l, err := schema.ListOf(el, opt, -1)
		if err != nil {
			t.Fatal(err)
		}
		g, err := schema.NewGroupNodeLogical(name, opt,
			schema.FieldList{l.Field(0)}, schema.NewListLogicalType(), -1)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	root, err := schema.NewGroupNode("schema", req,
		schema.FieldList{listNode("a"), listNode("b")}, -1)
	if err != nil {
		t.Fatal(err)
	}

	levels := func(rows [][]int64) ([]int64, []int16, []int16) {
		var vals []int64
		var defs, reps []int16
		for _, r := range rows {
			if len(r) == 0 {
				defs, reps = append(defs, 1), append(reps, 0)
				continue
			}
			for j, v := range r {
				vals = append(vals, v)
				defs = append(defs, 3)
				if j == 0 {
					reps = append(reps, 0)
				} else {
					reps = append(reps, 1)
				}
			}
		}
		return vals, defs, reps
	}

	path := filepath.Join(t.TempDir(), "two.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()
	for _, rows := range [][][]int64{a, b} {
		cw, err := rg.NextColumn()
		if err != nil {
			t.Fatal(err)
		}
		vals, defs, reps := levels(rows)
		if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(vals, defs, reps); err != nil {
			t.Fatal(err)
		}
		if err := cw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []interface{ Close() error }{rg, w} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestExplodeTwoColumnsZips: exploding two columns together pairs their elements
// positionally. Doing them one after another would give the cross product, which
// is why it is one operation.
func TestExplodeTwoColumnsZips(t *testing.T) {
	path := writeTwoListFile(t,
		[][]int64{{1, 2}, {3}},
		[][]int64{{10, 20}, {30}},
	)
	df, err := ursus.ScanParquet(path).Explode("a", "b").
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 {
		t.Fatalf("got %d rows, want 3 — a cross product would give 5\n%s",
			df.Height(), df)
	}
	for i, want := range []struct{ a, b int64 }{{1, 10}, {2, 20}, {3, 30}} {
		ga, _, err := df.At[int64](i, "a")
		if err != nil {
			t.Fatal(err)
		}
		gb, _, err := df.At[int64](i, "b")
		if err != nil {
			t.Fatal(err)
		}
		if ga != want.a || gb != want.b {
			t.Errorf("row %d = (%d, %d), want (%d, %d) — the columns are not zipped\n%s",
				i, ga, gb, want.a, want.b, df)
		}
	}
}

// TestExplodeUnequalLengthsIsAnError: truncating would lose data and padding
// would invent it, so a row where the lists disagree is the caller's question.
func TestExplodeUnequalLengthsIsAnError(t *testing.T) {
	path := writeTwoListFile(t,
		[][]int64{{1, 2}, {3}},
		[][]int64{{10, 20}, {30, 40}}, // row 1 has 1 vs 2
	)
	_, err := ursus.ScanParquet(path).Explode("a", "b").Collect(t.Context())
	if err == nil {
		t.Fatal("exploding columns of unequal length must fail, not truncate")
	}
	if !strings.Contains(err.Error(), "row 1") {
		t.Errorf("the error should name the offending row: %v", err)
	}
}
