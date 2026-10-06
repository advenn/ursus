package kernel_test

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// BenchmarkCompareInt32 is a batch of dates against a date literal, as PDS-H's
// filters are, and against another column: the shapes step 107 measured.
func BenchmarkCompareInt32(b *testing.B) {
	const n = 8192
	v := make([]int32, n)
	w := make([]int32, n)
	for i := range v {
		v[i], w[i] = int32(i*7%3000), int32(i*13%3000)
	}
	col := data.NewFixed("d", dtype.Date, v, bitmap.View{})
	other := data.NewFixed("e", dtype.Date, w, bitmap.View{})
	lit := data.NewFixed("lit", dtype.Date, []int32{1500}, bitmap.View{})
	for _, tc := range []struct {
		name string
		r    *data.Column
	}{{"against a literal", lit}, {"against a column", other}} {
		b.Run(tc.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := kernel.Binary(expr.OpLt, "x", dtype.Bool, col, tc.r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
