package exec

import (
	"context"
	"io"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// spyOp yields n one-row batches and records Close.
type spyOp struct {
	n, sent int
	closed  bool
}

var spySchema = dtype.MustSchema(dtype.Of("x", dtype.Int64))

func (s *spyOp) Schema() *dtype.Schema { return spySchema }
func (s *spyOp) Close() error          { s.closed = true; return nil }
func (s *spyOp) Next(context.Context) (*data.Batch, error) {
	if s.sent == s.n {
		return nil, io.EOF
	}
	s.sent++
	return data.NewBatch(spySchema, []*data.Column{
		data.NewFixed("x", dtype.Int64, []int64{int64(s.sent)}, bitmap.View{})})
}

// TestDrainClosesTheTreeBeforeTheResultIsAssembled: the tree's state — a join's
// table, a sort's runs — is dead once the stream ends, and Collect held it through
// the concatenation of the result (step 128).
func TestDrainClosesTheTreeBeforeTheResultIsAssembled(t *testing.T) {
	op := &spyOp{n: 3}
	batches, err := drain(t.Context(), op)
	if err != nil {
		t.Fatal(err)
	}
	if !op.closed {
		t.Error("the tree is still open when the batches are handed back")
	}
	if len(batches) != 3 {
		t.Errorf("%d batches, want 3", len(batches))
	}
	b, err := Collect(t.Context(), &spyOp{n: 3})
	if err != nil || b.Rows() != 3 {
		t.Fatalf("Collect: %v rows, %v", b, err)
	}
}
