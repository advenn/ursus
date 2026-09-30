package csv

// A panic while reading a CSV file is an error, in the two places this package
// could lose one: a conversion goroutine of its own, and the Once that infers the
// schema.

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// panicBuilder panics on the first field it is given.
type panicBuilder struct{ colBuilder }

func (panicBuilder) appendField([]byte) error { panic("convert boom") }

// TestConvertAllRecoversAPanic: a conversion goroutine is this reader's own, and a
// panic in it could be recovered by nothing else.
func TestConvertAllRecoversAPanic(t *testing.T) {
	r := &reader{
		threads:  2,
		wanted:   []int{0, 1},
		stage:    make([]colStage, 2),
		builders: make([]colBuilder, 2),
	}
	for i := range 2 {
		r.stage[i].reset()
		r.stage[i].add([]byte("x"), false)
		inner, err := newBuilder(dtype.String)
		if err != nil {
			t.Fatal(err)
		}
		r.builders[i] = inner
	}
	r.builders[1] = panicBuilder{r.builders[1]}

	err := r.convertAll()
	if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "convert boom") {
		t.Fatalf("want the panic as an internal error, got %v", err)
	}
}

// TestSchemaPanicIsNotForgotten: recovered inside the function Once runs. Were it
// recovered outside, the Once would still be done, and the second call would
// return no schema and no error.
func TestSchemaPanicIsNotForgotten(t *testing.T) {
	s := New(func() (io.ReadCloser, error) { panic("open boom") }, "boom.csv", Options{})
	for i := range 2 {
		sc, err := s.Schema(t.Context())
		if sc != nil || !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "open boom") {
			t.Errorf("call %d: got %v, %v; want the panic as an internal error", i+1, sc, err)
		}
	}
}
