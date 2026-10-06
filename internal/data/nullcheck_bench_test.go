package data_test

import (
	"fmt"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// BenchmarkNonNullableCheck is what CheckNonNullable costs a batch: ten non-nullable
// Int64 columns of 8192 rows, each with a MATERIALISED all-set bitmap — the case the
// O(1) IsAllSet fast path misses, so the check pays a popcount per column. It is the
// measurement step 98 decided I24 on.
func BenchmarkNonNullableCheck(b *testing.B) {
	const rows, ncols = 8192, 10
	fields := make([]dtype.Field, ncols)
	cols := make([]*data.Column, ncols)
	for i := range ncols {
		name := fmt.Sprintf("c%d", i)
		fields[i] = dtype.Field{Name: name, Type: dtype.Int64, Nullable: false}
		bb := bitmap.NewBuilder(rows)
		bb.AppendMany(true, rows)
		cols[i] = data.NewFixed(name, dtype.Int64, make([]int64, rows), bb.Finish())
	}
	schema, err := dtype.NewSchema(fields...)
	if err != nil {
		b.Fatal(err)
	}
	if cols[0].Validity().IsAllSet() {
		b.Fatal("the bitmap took the O(1) fast path; this would measure nothing")
	}
	defer func(was bool) { data.CheckNonNullable = was }(data.CheckNonNullable)
	for _, on := range []bool{false, true} {
		b.Run(fmt.Sprintf("check=%v", on), func(b *testing.B) {
			data.CheckNonNullable = on
			for b.Loop() {
				if _, err := data.NewBatch(schema, cols); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
