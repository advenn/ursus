package spill

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// TestAFailedWriteSaysItWasSpilling: Write returned its flush's error bare, so a
// full disk reached the caller with no kind and no word of spilling, where Close
// wrapped the same flush. A file closed under the writer fails the flush the same
// way.
func TestAFailedWriteSaysItWasSpilling(t *testing.T) {
	sch := dtype.MustSchema(dtype.Field{Name: "v", Type: dtype.Int64})
	w, err := Create(filepath.Join(t.TempDir(), "run.spill"), sch)
	if err != nil {
		t.Fatal(err)
	}
	w.f.Close()
	b, err := data.NewBatch(sch, []*data.Column{data.NewFixed("v", dtype.Int64, []int64{1, 2, 3}, bitmap.View{})})
	if err != nil {
		t.Fatal(err)
	}
	err = w.Write(b)
	if !errors.Is(err, uerr.ErrIO) {
		t.Fatalf("want an I/O error, got %v", err)
	}
	if !strings.Contains(err.Error(), "spill") || !strings.Contains(err.Error(), "run.spill") {
		t.Errorf("does not say it was spilling, or to which file:\n%v", err)
	}
}
