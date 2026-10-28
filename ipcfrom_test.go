package ursus_test

// Step 404: an Arrow IPC file and stream read from bytes and from the caller's
// openers, as Parquet, CSV and NDJSON are (C4 of v0.8-scope.md).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// ipcOf is a frame written as an IPC file, or a stream.
func ipcOf(t *testing.T, df *ursus.DataFrame, stream bool) []byte {
	t.Helper()
	var b bytes.Buffer
	write := df.Lazy().WriteIPC
	if stream {
		write = df.Lazy().WriteIPCStream
	}
	if err := write(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// counting is a reader of bytes that counts its closes.
type counting struct {
	*bytes.Reader
	closed *atomic.Int64
}

func (c counting) Close() error { c.closed.Add(1); return nil }

type ctxKey struct{}

// TestAnIPCFileIsReadFromBytesAndOpeners: a frame written as an IPC file reads back
// from its bytes whole, filtered and projected; two read through openers are one
// frame in the order given, each reader closed, each Open given the query's
// context, again when collected again; a stream the same.
func TestAnIPCFileIsReadFromBytesAndOpeners(t *testing.T) {
	a, b := engineFrame(t, 3_000, 10), engineFrame(t, 2_000, 7)
	both, err := ursus.Concat([]*ursus.LazyFrame{a.Lazy(), b.Lazy()}).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c := ursus.Col
	for _, stream := range []bool{false, true} {
		fa, fb := ipcOf(t, a, stream), ipcOf(t, b, stream)
		var whole *ursus.LazyFrame
		if stream {
			whole = ursus.ScanIPCStreamBytes(fa, "a.arrows")
		} else {
			whole = ursus.ScanIPCBytes(fa, "a.arrow")
		}
		got, err := whole.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, a)
		got, err = whole.Filter(c("k").Eq(int64(3))).Select(c("v")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want, _ := a.Lazy().Filter(c("k").Eq(int64(3))).Select(c("v")).Collect(t.Context())
		ursustest.AssertFrameEqual(t, got, want)

		var opened, closed atomic.Int64
		var lost atomic.Int64 // an Open not given the query's context
		reader := func(data []byte) func(ctx context.Context) counting {
			return func(ctx context.Context) counting {
				opened.Add(1)
				if ctx.Value(ctxKey{}) != "q" {
					lost.Add(1)
				}
				return counting{bytes.NewReader(data), &closed}
			}
		}
		var two *ursus.LazyFrame
		if stream {
			two = ursus.ScanIPCStreamFrom([]ursus.IPCStreamFile{
				{Name: "a", Open: func(ctx context.Context) (io.ReadCloser, error) { return reader(fa)(ctx), nil }},
				{Name: "b", Open: func(ctx context.Context) (io.ReadCloser, error) { return reader(fb)(ctx), nil }},
			})
		} else {
			two = ursus.ScanIPCFrom([]ursus.IPCFile{
				{Name: "a", Open: func(ctx context.Context) (io.ReaderAt, int64, error) { return reader(fa)(ctx), int64(len(fa)), nil }},
				{Name: "b", Open: func(ctx context.Context) (io.ReaderAt, int64, error) { return reader(fb)(ctx), int64(len(fb)), nil }},
			})
		}
		ctx := context.WithValue(t.Context(), ctxKey{}, "q")
		for range 2 {
			got, err := two.Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, both)
		}
		if opened.Load() < 4 || closed.Load() != opened.Load() || lost.Load() != 0 {
			t.Errorf("stream %v: %d opened, %d closed, %d not given the query's context", stream, opened.Load(), closed.Load(), lost.Load())
		}
	}
}

// TestAnIPCOpenerIsRefusedByName: no files, a file with no Open, an Open that fails,
// and a stream's bytes read as a file are refused, naming the file and, for the
// stream, ScanIPCStream; files of other columns are refused as ScanIPCFiles refuses
// them.
func TestAnIPCOpenerIsRefusedByName(t *testing.T) {
	ctx := t.Context()
	a := engineFrame(t, 10, 2)
	k, err := a.Lazy().Select(ursus.Col("k")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failed := errors.New("the store is down")
	for _, c := range []struct {
		name string
		lf   *ursus.LazyFrame
		kind error
		want []string
	}{
		{"no files", ursus.ScanIPCFrom(nil), ursus.ErrValue, []string{"no files given"}},
		{"no Open", ursus.ScanIPCFrom([]ursus.IPCFile{{Name: "x.arrow"}}), ursus.ErrValue, []string{"x.arrow has no Open"}},
		{"a failed Open", ursus.ScanIPCFrom([]ursus.IPCFile{{Name: "s3://b/x.arrow",
			Open: func(context.Context) (io.ReaderAt, int64, error) { return nil, 0, failed }}}),
			ursus.ErrIO, []string{"opening s3://b/x.arrow", "the store is down"}},
		{"a stream read as a file", ursus.ScanIPCBytes(ipcOf(t, a, true), "s.arrows"),
			ursus.ErrIO, []string{"reading s.arrows as an Arrow IPC file", "ScanIPCStream"}},
		{"a file read as a stream", ursus.ScanIPCStreamBytes(ipcOf(t, a, false), "f.arrow"),
			ursus.ErrIO, []string{"reading f.arrow as an Arrow IPC stream", "ScanIPC's"}},
		{"another file's columns", ursus.ScanIPCFrom([]ursus.IPCFile{
			{Name: "a", Open: func(context.Context) (io.ReaderAt, int64, error) {
				f := ipcOf(t, a, false)
				return bytes.NewReader(f), int64(len(f)), nil
			}},
			{Name: "b", Open: func(context.Context) (io.ReaderAt, int64, error) {
				f := ipcOf(t, k, false)
				return bytes.NewReader(f), int64(len(f)), nil
			}},
		}), ursus.ErrSchema, []string{"b"}},
	} {
		_, err := c.lf.Collect(ctx)
		if !errors.Is(err, c.kind) {
			t.Errorf("%s: %v, want a %v", c.name, err, c.kind)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q does not say %q", c.name, err, w)
			}
		}
	}
}

// TestIPCFilesFromOpenersReadTheirHiveKeys: with WithIPCHivePartitioning, the files'
// names give their rows a key, as ScanIPCFiles' paths do.
func TestIPCFilesFromOpenersReadTheirHiveKeys(t *testing.T) {
	a := engineFrame(t, 4, 2)
	f := ipcOf(t, a, false)
	open := func(context.Context) (io.ReaderAt, int64, error) { return bytes.NewReader(f), int64(len(f)), nil }
	got, err := ursus.ScanIPCFrom([]ursus.IPCFile{{Name: "d/day=1/a.arrow", Open: open}, {Name: "d/day=2/a.arrow", Open: open}},
		ursus.WithIPCHivePartitioning(true)).Select(ursus.Col("day").Sum()).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s := got.String(); !strings.Contains(s, " 12 ") {
		t.Errorf("the days' sum over 4 rows of each: %s", s)
	}
}
