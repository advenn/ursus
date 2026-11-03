package files

import (
	"context"
	"errors"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
)

// hangingUp is a file whose first schema read sees its caller go away.
type hangingUp struct {
	reads  int
	cancel context.CancelFunc
}

func (h *hangingUp) Name() string     { return "fake" }
func (h *hangingUp) Describe() string { return "fake" }
func (h *hangingUp) Caps() plan.Caps  { return plan.Caps{} }
func (h *hangingUp) Open(context.Context, source.ScanSpec) (source.BatchSource, error) {
	return nil, errors.New("not read")
}
func (h *hangingUp) Schema(ctx context.Context) (*dtype.Schema, error) {
	if h.reads++; h.reads == 1 {
		h.cancel()
		return nil, ctx.Err()
	}
	return dtype.NewSchema(dtype.Field{Name: "a", Type: dtype.Int64})
}

// TestSchemaIsNotPoisonedByACancellation: a dataset whose first file's schema read is
// cut short by its caller's cancellation fails that call, and the next caller reads
// the schema, where it gave the first one's context.Canceled for good (step 418).
func TestSchemaIsNotPoisonedByACancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := &hangingUp{cancel: cancel}
	s := New("scan_ipc", "ipc", "fake", []File{{Src: f, Path: "a.arrow"}}, Shared{})
	if _, err := s.Schema(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled read: %v", err)
	}
	sc, err := s.Schema(t.Context())
	if err != nil || sc.Len() != 1 {
		t.Errorf("the next read: %v, %v", sc, err)
	}
}
