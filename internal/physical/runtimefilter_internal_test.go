package physical

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
)

// TestARuntimeFilterKeepsExactlyTheRowsThatCanJoin: the filter's own answer, which a
// query's cannot show, since the join drops what the filter keeps. With the join's
// keys dense, tested against a bitmap (step 202), and spread 2^33 apart, looked up in
// the tables: a row is kept when its key is among them and it is not null.
func TestARuntimeFilterKeepsExactlyTheRowsThatCanJoin(t *testing.T) {
	for _, scale := range []int64{1, 1 << 33} {
		table := kernel.NewIntKeyTable()
		for k := int64(0); k < 100; k += 2 {
			table.GetOrInsert(k * scale)
		}
		keys := kernel.NewIntKeyParts([]*kernel.IntKeyTable{table})
		if dense := keys.Bits() != nil; dense != (scale == 1) {
			t.Fatalf("scale %d: a bitmap is %v", scale, dense)
		}
		slot := &plan.RuntimeSlot{}
		slot.Publish(keys)
		sch := dtype.MustSchema(dtype.Of("k", dtype.Int64))
		const n = 300
		vals := make([]int64, n)
		valid := bitmap.NewBuilder(n)
		var want []int64
		for i := range n {
			vals[i] = int64(i-50) * scale
			null := i%7 == 0
			valid.Append(!null)
			if !null && i-50 >= 0 && i-50 < 100 && (i-50)%2 == 0 {
				want = append(want, vals[i])
			}
		}
		in, err := data.NewBatch(sch, []*data.Column{data.NewFixed("k", dtype.Int64, vals, valid.Finish())})
		if err != nil {
			t.Fatal(err)
		}
		op := &runtimeFilterOp{schema: sch, key: "k", slot: slot}
		out, err := op.Apply(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := data.Values[int64](out.Column(0))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("scale %d: %d rows kept, want %d", scale, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] || !out.Column(0).IsValid(i) {
				t.Fatalf("scale %d: row %d is %d, want %d", scale, i, got[i], want[i])
			}
		}
	}
}
