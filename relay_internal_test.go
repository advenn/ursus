package ursus

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// TestAFrameOperationsPanicIsAnError (step 393): the eager frame operations that
// work a part or a column a goroutine, PartitionBy, ToDummies and Corr through
// inParallel, return a panic on one of their goroutines as an internal error, where
// it ended the process: a row of text past its column's characters, and a panic in
// inParallel's function.
func TestAFrameOperationsPanicIsAnError(t *testing.T) {
	n := 4000
	offs := make([]int32, n+1)
	for i := range offs {
		offs[i] = int32(i)
	}
	offs[n] = 1 << 24 // the last row's text runs past the characters
	keys := make([]int64, n)
	for i := range keys {
		keys[i] = int64(i % 50)
	}
	s := data.NewStringParts("s", offs, make([]byte, n), bitmap.View{})
	k := data.NewFixed("k", Int64, keys, bitmap.AllSet(n))
	df, err := frameOfColumns([]*data.Column{k, s}, n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := df.PartitionBy(t.Context(), PartitionOptions{By: []string{"k"}}); !errors.Is(err, uerr.ErrInternal) {
		t.Fatalf("PartitionBy: want the panic as an internal error, got %v", err)
	}
	if _, err := df.ToDummies(t.Context(), DummiesOptions{Columns: []string{"s"}}); !errors.Is(err, uerr.ErrInternal) {
		t.Fatalf("ToDummies: want the panic as an internal error, got %v", err)
	}
	err = uerr.GuardErr("corr", func() error {
		return inParallel(t.Context(), 64, func(i int) error {
			if i == 40 {
				panic("column boom")
			}
			return nil
		})
	})
	if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "column boom") {
		t.Fatalf("inParallel: want the panic as an internal error, got %v", err)
	}
}
