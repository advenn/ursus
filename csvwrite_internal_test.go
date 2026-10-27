package ursus

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// TestCSVFormatterPanicIsAnError: since step 390 a batch's CSV text is made on a
// goroutine of its own; a panic there, a row of text past its column's characters,
// comes back as an internal error, and the caller's writer, which panics on the
// goroutine that writes the texts in order, as an error too.
func TestCSVFormatterPanicIsAnError(t *testing.T) {
	col := data.NewStringParts("s", []int32{0, 1 << 20}, []byte("ab"), bitmap.View{})
	df, err := frameOfColumns([]*data.Column{col}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	err = df.Lazy().WriteCSV(t.Context(), &sb, WithThreads(4))
	if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("want the panic as an internal error, got %v", err)
	}
	err = Frame(Values("a", make([]int64, 70_000))).WriteCSV(t.Context(), panicWriter{}, WithThreads(4))
	if err == nil || !strings.Contains(err.Error(), "writer boom") {
		t.Fatalf("want the writer's panic as an error, got %v", err)
	}
}
