package exec_test

// Each serial loop pulls through physical.Pull. With one thread every operator runs
// on the caller's goroutine, and before step 72 a panic there escaped every entry
// point; the public ones now recover too, which is why these ask exec directly.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/uerr"
)

type panicOp struct{}

func (panicOp) Schema() *dtype.Schema                     { return dtype.MustSchema() }
func (panicOp) Next(context.Context) (*data.Batch, error) { panic("next boom") }
func (panicOp) Close() error                              { return nil }

func TestEachLoopReturnsAPanicAsAnError(t *testing.T) {
	want := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "next boom") {
			t.Errorf("%s: want the panic as an internal error, got %v", name, err)
		}
	}
	_, err := exec.Collect(t.Context(), panicOp{})
	want("Collect", err)
	_, err = exec.Count(t.Context(), panicOp{})
	want("Count", err)
	for _, err := range exec.Batches(t.Context(), panicOp{}) {
		want("Batches", err)
	}
}
