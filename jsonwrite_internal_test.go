package ursus

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// TestJSONEncoderPanicIsAnError: a value whose text panics, on the goroutine that
// encodes its batch, comes back from WriteNDJSON as an internal error, the process
// alive. No constructor makes an Enum index past its categories; this one is made by
// hand to make the text panic.
func TestJSONEncoderPanicIsAnError(t *testing.T) {
	col := data.NewFixed("e", dtype.Enum("a"), []uint32{5}, bitmap.AllSet(1))
	df, err := frameOfColumns([]*data.Column{col}, 1)
	if err != nil {
		t.Fatal(err)
	}
	err = df.Lazy().WriteNDJSON(t.Context(), io.Discard)
	if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "index out of range") {
		t.Fatalf("want the panic as an internal error, got %v", err)
	}
}

// panicWriter is an io.Writer that panics, as a caller's might.
type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("writer boom") }

// TestJSONWriterPanicIsAnError: a panic in the caller's writer, on the goroutine that
// writes the batches' texts in order, comes back as an error.
func TestJSONWriterPanicIsAnError(t *testing.T) {
	lf := Frame(Values("a", make([]int64, 70_000)))
	err := lf.WriteNDJSON(t.Context(), panicWriter{})
	if err == nil || !strings.Contains(err.Error(), "writer boom") {
		t.Fatalf("want the panic as an error, got %v", err)
	}
}
