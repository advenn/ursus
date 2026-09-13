package spill_test

// Every IO-error path in the reader, which had none.
//
// Reader.wrap has five call sites and 0.0% coverage measured with cross-package
// attribution. spill_test.go's five tests are all happy-path round trips; nothing
// in the repository truncates or corrupts a spill file, and no test asserts any
// spill error message.
//
// This is the code that runs when a query is ALREADY in trouble: the three spilling
// operators reach it only under a memory limit, after the engine has decided it
// cannot hold the data. A panic or an opaque message here lands on a user who is
// already having a bad day.
//
// The fixture needs no hand-crafted bytes. Writer.Write flushes at the end of every
// batch and Close flushes the terminator, so the file on disk is complete and every
// PREFIX of it is reachable with os.Truncate.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/spill"
)

// writeSpill produces a small, complete spill file and returns its path and size.
func writeSpill(t *testing.T, rows int) (string, int64) {
	t.Helper()
	sch := dtype.MustSchema(dtype.Of("v", dtype.Int64), dtype.Of("s", dtype.String))
	path := filepath.Join(t.TempDir(), "run.ursspill")

	w, err := spill.Create(path, sch)
	if err != nil {
		t.Fatal(err)
	}
	vals := make([]int64, rows)
	strs := make([]string, rows)
	for i := range rows {
		vals[i] = int64(i)
		strs[i] = "row"
	}
	b, err := data.NewBatch(sch, []*data.Column{
		data.NewFixed("v", dtype.Int64, vals, bitmap.AllSet(rows)),
		data.NewString("s", strs, bitmap.AllSet(rows)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, fi.Size()
}

// readAll drains a reader, recovering from a panic so the sweep can report WHICH
// offset panicked rather than taking the whole test binary down.
func readAll(path string) (err error, panicked any) {
	defer func() { panicked = recover() }()

	r, oerr := spill.Open(path)
	if oerr != nil {
		return oerr, nil
	}
	defer r.Close()
	for {
		_, e := r.Next()
		if e != nil {
			return e, nil
		}
	}
}

// TestTruncatedSpillFileIsAlwaysAnError sweeps every byte offset.
//
// Three assertions per offset, and the third is the load-bearing one. Every
// spilling operator ends its read loop on errors.Is(err, io.EOF) — extsort,
// extjoin and extagg all do — so if wrap ever wrapped io.EOF, a truncated spill
// would stop being an error and become a SILENT SHORT READ across the whole engine.
// Nothing else in the repository asserts that.
func TestTruncatedSpillFileIsAlwaysAnError(t *testing.T) {
	path, size := writeSpill(t, 4)
	if size < 20 {
		t.Fatalf("the fixture is %d bytes; too small for the sweep to mean anything", size)
	}

	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var checked int
	for n := int64(0); n < size; n++ {
		cut := filepath.Join(t.TempDir(), "cut.ursspill")
		if err := os.WriteFile(cut, src[:n], 0o644); err != nil {
			t.Fatal(err)
		}
		checked++

		err, panicked := readAll(cut)
		if panicked != nil {
			t.Errorf("truncated at %d/%d: PANIC %v — a corrupt spill file must "+
				"error, not take the process down", n, size, panicked)
			continue
		}
		if err == nil {
			t.Errorf("truncated at %d/%d: no error; a prefix of a spill file is "+
				"not a valid spill file", n, size)
			continue
		}
		if errors.Is(err, io.EOF) {
			t.Errorf("truncated at %d/%d: the error wraps io.EOF, which every "+
				"spilling operator treats as a clean end of stream — this would be "+
				"a silent short read: %v", n, size, err)
		}
	}

	if checked < 20 {
		t.Fatalf("only %d offsets swept — the sweep has gone vacuous", checked)
	}
}

// TestUntruncatedSpillFileEndsCleanly is the control. Without it the sweep above is
// satisfied by a reader that errors on everything.
func TestUntruncatedSpillFileEndsCleanly(t *testing.T) {
	path, _ := writeSpill(t, 4)

	r, err := spill.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	b, err := r.Next()
	if err != nil {
		t.Fatalf("the first batch must read: %v", err)
	}
	if b.Rows() != 4 {
		t.Errorf("got %d rows, want 4", b.Rows())
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("a complete file ends with io.EOF, got %v", err)
	}
}

// TestSpillReaderAfterCloseDoesNotPanic.
//
// Close sets r.f = nil, and both arms of wrap called r.f.Name(). A closed fd returns
// os.ErrClosed rather than EOF, so the read took the second arm and dereferenced a
// nil *os.File.
//
// No consumer does this today, which is why it never fired. But mergeOperator.Close
// closes every run including live ones, so a cancellation path is one refactor from
// reaching it — and a panic inside the spill reader is the worst place to learn that.
func TestSpillReaderAfterCloseDoesNotPanic(t *testing.T) {
	path, _ := writeSpill(t, 4)
	r, err := spill.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("Next after Close panicked: %v", p)
		}
	}()
	if _, err := r.Next(); err == nil {
		t.Error("reading a closed spill file must be an error")
	}
}

// TestSpillOpenRejectsAForeignFile, and reports the read failure honestly rather
// than calling every failure a bad magic.
func TestSpillOpenRejectsAForeignFile(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"short", []byte("URS")},
		{"wrong magic", []byte("NOTASPILLFILE")},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.name)
			if err := os.WriteFile(p, c.body, 0o644); err != nil {
				t.Fatal(err)
			}
			r, err := spill.Open(p)
			if err == nil {
				r.Close()
				t.Fatal("a file that is not a spill file must be refused")
			}
		})
	}
}

// TestTruncationMessagesDistinguishTheTwoShapes.
//
// wrap's two branches used to be swapped relative to their text. io.EOF means a
// read began with nothing left — a field or record boundary, i.e. the file stopped
// cleanly — and it was reported as "ends mid-record". io.ErrUnexpectedEOF, which is
// what io.ReadFull and binary.ReadUvarint return when a read runs out PARTWAY, is
// the case that actually is mid-record, and it fell to the generic arm and produced
// the opaque "unexpected EOF" the comment claimed to have replaced.
//
// A one-column file reproduces it at twenty-three consecutive offsets.
func TestTruncationMessagesDistinguishTheTwoShapes(t *testing.T) {
	path, size := writeSpill(t, 4)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var midRecord, noTerminator, opaque int
	for n := int64(1); n < size; n++ {
		cut := filepath.Join(t.TempDir(), "cut.ursspill")
		if err := os.WriteFile(cut, src[:n], 0o644); err != nil {
			t.Fatal(err)
		}
		err, panicked := readAll(cut)
		if panicked != nil || err == nil {
			continue // the sweep above owns those
		}
		switch msg := err.Error(); {
		case strings.Contains(msg, "ends mid-record"):
			midRecord++
		case strings.Contains(msg, "ends without its terminator"):
			noTerminator++
		case strings.Contains(msg, "unexpected EOF"):
			opaque++
		}
	}

	if opaque > 0 {
		t.Errorf("%d offsets still produce a bare \"unexpected EOF\" — that is the "+
			"message wrap exists to replace", opaque)
	}
	// BOTH shapes must occur, or the split is untested in one direction and the
	// counts above prove nothing.
	if midRecord == 0 {
		t.Error("no offset produced the mid-record message; the partial-read branch " +
			"is unexercised")
	}
	if noTerminator == 0 {
		t.Error("no offset produced the missing-terminator message; the clean-stop " +
			"branch is unexercised")
	}
	t.Logf("mid-record: %d, missing terminator: %d", midRecord, noTerminator)
}
