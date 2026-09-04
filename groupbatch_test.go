package ursus_test

// A grouped aggregate whose result spans several OUTPUT batches, with arithmetic
// on top.
//
// This is the shape of h2o gb7 — `GroupBy(id3).Agg(Max(v1), Min(v2))` then
// `max - min` — which returned 96 spurious NULLs out of 100,000 groups and was
// the only query in the benchmark suite that failed validation. The cause was in
// bitmap.And, not in the aggregate: see TestCombinatorsEndingOnAByteBoundary for
// the unit-level version. This is the end-to-end guard, because the unit test
// cannot show that the two are connected.

import (
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// batchSpanningGroups is deliberately larger than the 8192-row output batch, and
// by more than one batch. At 8192 groups or fewer the aggregate emits a single
// batch, every validity view starts at bit offset 0, and the defect is invisible
// — which is exactly why nothing in the suite caught it.
const batchSpanningGroups = 20000

func groupSpanSource(t testing.TB) *memsrc.Source {
	t.Helper()
	schema, err := dtype.NewSchema(
		dtype.NotNull("k", dtype.Int64),
		dtype.NotNull("v", dtype.Int64),
		dtype.NotNull("w", dtype.Int64),
	)
	if err != nil {
		t.Fatal(err)
	}

	const perGroup, batch = 6, 8192
	rows := batchSpanningGroups * perGroup

	var batches []*data.Batch
	for start := 0; start < rows; start += batch {
		n := min(batch, rows-start)
		k := make([]int64, n)
		v := make([]int64, n)
		w := make([]int64, n)
		for i := range n {
			x := start + i
			k[i] = int64(x % batchSpanningGroups)
			v[i] = int64(x%97) + 1
			w[i] = int64(x%89) + 1
		}
		b, err := data.NewBatch(schema, []*data.Column{
			data.NewFixed("k", dtype.Int64, k, bitmap.AllSet(n)),
			data.NewFixed("v", dtype.Int64, v, bitmap.AllSet(n)),
			data.NewFixed("w", dtype.Int64, w, bitmap.AllSet(n)),
		})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	src, err := memsrc.New(schema, batches...)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// TestArithmeticOverAggregateSpanningBatches: no row may be null.
//
// Every group has six non-null values, so every max, every min and every
// difference is a real number. A single null anywhere is the bug.
func TestArithmeticOverAggregateSpanningBatches(t *testing.T) {
	src := groupSpanSource(t)

	df, err := ursus.Scan(src).
		GroupBy(ursus.Col("k")).
		Agg(
			ursus.Col("v").Max().Alias("mx"),
			ursus.Col("w").Min().Alias("mn"),
		).
		Select(ursus.Col("k"), ursus.Col("mx").Sub(ursus.Col("mn")).Alias("r")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != batchSpanningGroups {
		t.Fatalf("height = %d, want %d", df.Height(), batchSpanningGroups)
	}

	r, err := df.Column[int64]("r")
	if err != nil {
		t.Fatal(err)
	}
	var nulls []int
	for i := range df.Height() {
		if _, ok := r.Get(i); !ok {
			nulls = append(nulls, i)
		}
	}
	if len(nulls) > 0 {
		// The positions are the diagnosis: runs of 8 ending at multiples of 8192
		// mean a validity bitmap lost its final byte per output batch.
		t.Errorf("%d rows are null but every group has six values; first at %v",
			len(nulls), nulls[:min(16, len(nulls))])
	}
}

// TestValiditySurvivesEveryOutputBatch is the same defect stated as a property,
// over the operations that combine two validity bitmaps.
//
// The first output batch starts at bit offset 0 and was always correct; every
// later one starts at 8192k and was not. So a test that only looked at the head
// of the result — or used fewer than 8192 groups — saw nothing wrong.
func TestValiditySurvivesEveryOutputBatch(t *testing.T) {
	src := groupSpanSource(t)

	for _, c := range []struct {
		name string
		expr ursus.Expr
	}{
		{"sub", ursus.Col("mx").Sub(ursus.Col("mn"))},
		{"add", ursus.Col("mx").Add(ursus.Col("mn"))},
		{"mul", ursus.Col("mx").Mul(ursus.Col("mn"))},
		{"gt", ursus.Col("mx").Gt(ursus.Col("mn"))},
		{"and", ursus.Col("mx").Gt(0).And(ursus.Col("mn").Gt(0))},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, err := ursus.Scan(src).
				GroupBy(ursus.Col("k")).
				Agg(
					ursus.Col("v").Max().Alias("mx"),
					ursus.Col("w").Min().Alias("mn"),
				).
				Select(c.expr.Alias("out")).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			col := df.Batch().Column(0)
			bad := 0
			for i := range df.Height() {
				if !col.IsValid(i) {
					bad++
				}
			}
			if bad != 0 {
				t.Errorf("%d of %d rows null; no input was", bad, df.Height())
			}
		})
	}
}
