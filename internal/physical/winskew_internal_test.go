package physical

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
)

// TestASplitWindowsOtherKeysSpillTheirOwnWay: a sink a level down that holds one
// key when it goes over splits that key off (step 209), and the file's other keys
// go to a sink of their own, which may spill too. Driven directly, since a query
// cannot choose which keys share a file: 15,360 rows of key 0 alone under 256 KiB,
// then 40,960 rows, half key 0, half of 300 others. The answer is every row, in
// ordinal order, with its key's sum, and nothing is left on disk.
//
// 256 KiB, because a sink that routes to disk holds sixteen partition writers of
// 8 KiB, and under 32 KiB they alone were over.
func TestASplitWindowsOtherKeysSpillTheirOwnWay(t *testing.T) {
	ctx := t.Context()
	in := dtype.MustSchema(dtype.Of("k", dtype.Int64), dtype.Of("v", dtype.Int64), dtype.NotNull("ord", dtype.Int64))
	exprs := []expr.Node{&expr.Alias{Name: "s", Child: &expr.Window{
		Child:       &expr.Agg{Op: expr.AggSum, Child: &expr.Col{Name: "v"}},
		PartitionBy: []expr.Node{&expr.Col{Name: "k"}},
	}}}
	out, err := passSchema(in, exprs)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	budget := execopt.NewBudget(256<<10, dir)
	sink, err := newWindowSink(in, out, exprs, budget.Account("over"))
	if err != nil {
		t.Fatal(err)
	}
	sink.budget, sink.batch, sink.level, sink.ord, sink.spillSchema = budget, 512, 1, "ord", in

	sums := map[int64]int64{}
	var keyOf []int64
	ord := int64(0)
	for b := range 110 {
		k, v, o := make([]int64, 512), make([]int64, 512), make([]int64, 512)
		for i := range 512 {
			if b >= 30 && i%2 == 1 {
				k[i] = int64(1 + (b*512+i)%300)
			}
			v[i], o[i] = ord%13, ord
			sums[k[i]] += v[i]
			keyOf = append(keyOf, k[i])
			ord++
		}
		batch, err := data.NewBatch(in, []*data.Column{
			data.NewFixed("k", dtype.Int64, k, bitmap.View{}),
			data.NewFixed("v", dtype.Int64, v, bitmap.View{}),
			data.NewFixed("ord", dtype.Int64, o, bitmap.View{}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.Consume(ctx, batch); err != nil {
			t.Fatalf("batch %d: %v", b, err)
		}
	}
	if sink.rest == nil || !sink.rest.spilling {
		t.Fatalf("split %v; the other keys spilled %v", sink.rest != nil, sink.rest != nil && sink.rest.spilling)
	}
	op, err := sink.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := int64(0)
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ords, ss := data.MustValues[int64](b.Column(2)), data.MustValues[i128.Int128](b.Column(3))
		for i, o := range ords {
			if o != next {
				t.Fatalf("row %d: ordinal %d", next, o)
			}
			if want := sums[keyOf[o]]; ss[i] != i128.FromInt64(want) {
				t.Fatalf("row %d, key %d: sum %d, want %d", o, keyOf[o], ss[i], want)
			}
			next++
		}
	}
	if next != ord {
		t.Fatalf("%d rows, want %d", next, ord)
	}
	if err := errors.Join(op.Close(), sink.Close()); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) > 0 {
		t.Errorf("%d entries left in the spill directory", len(left))
	}
}
