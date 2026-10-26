package physical

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// plainSource is a source that does not answer a scan of no columns: memsrc's
// methods, without RowsOnly, recording what it was asked for.
type plainSource struct {
	m     *memsrc.Source
	asked [][]string
}

func (p *plainSource) Name() string     { return p.m.Name() }
func (p *plainSource) Describe() string { return p.m.Describe() }
func (p *plainSource) Caps() plan.Caps  { return p.m.Caps() }
func (p *plainSource) Schema(ctx context.Context) (*dtype.Schema, error) {
	return p.m.Schema(ctx)
}
func (p *plainSource) Open(ctx context.Context, spec source.ScanSpec) (source.BatchSource, error) {
	p.asked = append(p.asked, spec.Projection)
	return p.m.Open(ctx, spec)
}

// TestAScanOfNoColumnsKeepsItsRows: a scan of no columns over a source that cannot
// answer one asks it for its cheapest column, a fixed-width one before text, and
// trims it away keeping the rows (step 370); a source that can is asked for none.
func TestAScanOfNoColumnsKeepsItsRows(t *testing.T) {
	sch := dtype.MustSchema(dtype.Of("s", dtype.String), dtype.Of("x", dtype.Int64), dtype.Of("b", dtype.Bool))
	var batches []*data.Batch
	for i := range 5 {
		b, err := data.NewBatch(sch, []*data.Column{
			data.NewString("s", []string{"a", "b", "c"}, bitmap.View{}),
			data.NewFixed("x", dtype.Int64, []int64{int64(i), 1, 2}, bitmap.View{}),
			data.NewBool("b", bitmap.AllSet(3), bitmap.View{}),
		})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	m, err := memsrc.New(sch, batches...)
	if err != nil {
		t.Fatal(err)
	}
	rows := func(src plan.Source) int {
		t.Helper()
		op, err := Plan(t.Context(), &plan.Scan{Src: src, Full: sch, Projection: []string{}},
			Options{BatchSize: 2, Threads: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer op.Close()
		if op.Schema().Len() != 0 {
			t.Fatalf("a scan of no columns has %d", op.Schema().Len())
		}
		n := 0
		for {
			b, err := op.Next(t.Context())
			if errors.Is(err, io.EOF) {
				return n
			}
			if err != nil {
				t.Fatal(err)
			}
			n += b.Rows()
		}
	}
	plain := &plainSource{m: m}
	if got := rows(plain); got != 15 {
		t.Errorf("a source that cannot answer it: %d rows, want 15", got)
	}
	if len(plain.asked) != 1 || !slices.Equal(plain.asked[0], []string{"b"}) {
		t.Errorf("asked for %v, want its cheapest column, b", plain.asked)
	}
	if got := rows(m); got != 15 {
		t.Errorf("a source that answers it: %d rows, want 15", got)
	}
	// Its log has the plain source's open of it first, and records no column as nil.
	if p := m.Projections(); len(p) != 2 || len(p[1]) != 0 {
		t.Errorf("a source that answers it was asked for %v, want no column", p)
	}
}
