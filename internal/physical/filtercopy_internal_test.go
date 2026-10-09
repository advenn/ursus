package physical

import (
	"math/rand/v2"
	"runtime"
	"runtime/metrics"
	"strconv"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// TestAFilterCopiesItsRowsOnce: three comparisons over 64,000 rows of eight Int64
// columns, keeping a half, then two thirds of that, then a half: a sixth of the
// rows. Against the bytes of the answer.
//
// Each conjunct used to gather every column of the rows it kept, so the batch was
// copied three times over, at a half, a third and a sixth of its rows: about six
// times the answer. Filters were 17 to 24% of PDS-H q7, q12, q15 and q19, much of
// it that copying (step 141). Now a conjunct gathers only the columns the next one
// reads, and every column is gathered once, at the end (step 142).
//
// Not parallel, so no other test allocates while it reads.
func TestAFilterCopiesItsRowsOnce(t *testing.T) {
	const n, cols = 64_000, 8
	rng := rand.New(rand.NewPCG(142, 1))
	fields := make([]dtype.Field, cols)
	columns := make([]*data.Column, cols)
	for c := range cols {
		name := "c" + strconv.Itoa(c)
		fields[c] = dtype.NotNull(name, dtype.Int64)
		vals := make([]int64, n)
		for i := range vals {
			vals[i] = rng.Int64N(1000)
		}
		columns[c] = data.NewFixed(name, dtype.Int64, vals, bitmap.AllSet(n))
	}
	schema := dtype.MustSchema(fields...)
	in, err := data.NewBatch(schema, columns)
	if err != nil {
		t.Fatal(err)
	}
	lit := func(v int64) *expr.Lit { return &expr.Lit{Value: v, DT: dtype.Int64} }
	f := &filterOp{schema: schema, preds: []expr.Node{
		&expr.Binary{Op: expr.OpLt, L: &expr.Col{Name: "c0"}, R: lit(500)},
		&expr.Binary{Op: expr.OpLt, L: &expr.Col{Name: "c1"}, R: lit(667)},
		&expr.Binary{Op: expr.OpLt, L: &expr.Col{Name: "c2"}, R: lit(500)},
	}}

	// The answer, the slow way, to check the fast one against.
	var want []int
	c0, _ := data.Values[int64](in.Column(0))
	c1, _ := data.Values[int64](in.Column(1))
	c2, _ := data.Values[int64](in.Column(2))
	for i := range n {
		if c0[i] < 500 && c1[i] < 667 && c2[i] < 500 {
			want = append(want, i)
		}
	}

	read := func() uint64 {
		m := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
		metrics.Read(m)
		return m[0].Value.Uint64()
	}
	runtime.GC()
	before := read()
	out, err := f.Apply(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	allocated := int64(read() - before)

	if out.Rows() != len(want) {
		t.Fatalf("%d rows kept, want %d", out.Rows(), len(want))
	}
	for c := range cols {
		got, _ := data.Values[int64](out.Column(c))
		all, _ := data.Values[int64](in.Column(c))
		for j, i := range want {
			if got[j] != all[i] {
				t.Fatalf("column %d, row %d: %d, want %d", c, j, got[j], all[i])
			}
		}
	}
	var answer int64
	for _, c := range out.Columns() {
		answer += c.NBytes()
	}
	t.Logf("allocated %.2f MB for a %.2f MB answer: %.2f", float64(allocated)/(1<<20),
		float64(answer)/(1<<20), float64(allocated)/float64(answer))
	if allocated > answer*5/2 {
		t.Errorf("filtering allocated %d bytes for a %d-byte answer", allocated, answer)
	}
}
