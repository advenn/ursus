package ursus_test

// Step 415: a stream's read of no bytes and no error is not its end. io.Reader's
// contract has it mean nothing happened; the CSV reader took it for the end and cut a
// caller's stream short, and a stream returning it forever hung NDJSON's reader.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// pausing hands back its text a few bytes a read, with a read of nothing between.
type pausing struct {
	text  string
	pos   int
	pause bool
}

func (p *pausing) Read(b []byte) (int, error) {
	if p.pause = !p.pause; p.pause {
		return 0, nil
	}
	if p.pos >= len(p.text) {
		return 0, io.EOF
	}
	n := copy(b[:min(len(b), 3)], p.text[p.pos:])
	p.pos += n
	return n, nil
}

func (p *pausing) Close() error { return nil }

// stuck reads nothing and no error, forever.
type stuck struct{}

func (stuck) Read([]byte) (int, error) { return 0, nil }
func (stuck) Close() error             { return nil }

// TestAnEmptyReadIsNotTheEnd: CSV and NDJSON from a stream that pauses with empty
// reads come back whole, on all threads and on one, where CSV stopped at its first; a stream that never moves is
// an error wrapping io.ErrNoProgress, after bufio's hundred empty reads in a row,
// where CSV read it as empty and NDJSON spun for good.
func TestAnEmptyReadIsNotTheEnd(t *testing.T) {
	var csvText, ndjson strings.Builder
	csvText.WriteString("a,b\n")
	for i := range 1000 {
		fmt.Fprintf(&csvText, "%d,%d\n", i, 2*i)
		fmt.Fprintf(&ndjson, `{"a":%d}`+"\n", i)
	}
	frames := []struct {
		name string
		lf   func(open func(context.Context) (io.ReadCloser, error)) *ursus.LazyFrame
		text string
	}{
		{"CSV", func(open func(context.Context) (io.ReadCloser, error)) *ursus.LazyFrame {
			return ursus.ScanCSVFrom([]ursus.CSVFile{{Name: "s.csv", Open: open}})
		}, csvText.String()},
		{"NDJSON", func(open func(context.Context) (io.ReadCloser, error)) *ursus.LazyFrame {
			return ursus.ScanNDJSONFrom([]ursus.NDJSONFile{{Name: "s.ndjson", Open: open}})
		}, ndjson.String()},
	}
	for _, f := range frames {
		text := f.text
		for _, threads := range []int{0, 1} { // one thread reads CSV by its scanner
			var opts []ursus.CollectOption
			if threads > 0 {
				opts = append(opts, ursus.WithThreads(threads))
			}
			df, err := f.lf(func(context.Context) (io.ReadCloser, error) { return &pausing{text: text}, nil }).
				Collect(t.Context(), opts...)
			if err != nil || df.Height() != 1000 {
				t.Errorf("%s pausing, threads %d: %v rows, %v; want 1000", f.name, threads, height(df), err)
			}
		}

		done := make(chan error, 1)
		go func() {
			_, err := f.lf(func(context.Context) (io.ReadCloser, error) { return stuck{}, nil }).Collect(t.Context())
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, io.ErrNoProgress) {
				t.Errorf("%s stuck: got %v, want io.ErrNoProgress", f.name, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s stuck: no answer in 30 s", f.name)
		}
	}
}

func height(df *ursus.DataFrame) int {
	if df == nil {
		return -1
	}
	return df.Height()
}
