package ursus_test

// A query that fails while it is being planned closes what it already opened.
//
// A CSV source opens its stream when the operator tree is built, before the first
// batch. Planning goes child by child, so when a later child failed, the children
// already built were dropped without a Close: the first side of a join whose second
// side could not open kept its stream — a file handle, or an HTTP body — open for
// good. Step 60 recorded it at planJoin, step 64 counted seventeen sites, and
// planScan was an eighteenth: a source opened and then dropped on its own schema
// error.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/advenn/ursus"
)

// countedCSV serves one CSV body and counts the streams it opens and the streams
// closed. With failAfter > 0, every Open after the first failAfter fails — the first
// is schema inference, so the source resolves and then cannot open to be read.
type countedCSV struct {
	body           string
	failAfter      int32
	opened, closed atomic.Int32
}

type countedBody struct {
	io.Reader
	closed *atomic.Int32
	once   atomic.Bool
}

func (b *countedBody) Close() error {
	if b.once.CompareAndSwap(false, true) {
		b.closed.Add(1)
	}
	return nil
}

func (c *countedCSV) frame(name string) *ursus.LazyFrame {
	return ursus.ScanCSVFrom([]ursus.CSVFile{{Name: name, Open: func(context.Context) (io.ReadCloser, error) {
		if c.failAfter > 0 && c.opened.Load() >= c.failAfter {
			return nil, errors.New("the object is gone")
		}
		c.opened.Add(1)
		return &countedBody{Reader: strings.NewReader(c.body), closed: &c.closed}, nil
	}}})
}

func (c *countedCSV) leaked() int32 { return c.opened.Load() - c.closed.Load() }

func TestAFailedPlanClosesWhatItOpened(t *testing.T) {
	const body = "k,v\n1,10\n2,20\n"
	c := ursus.Col
	cases := []struct {
		name  string
		build func(ok, failing *ursus.LazyFrame) *ursus.LazyFrame
	}{
		{"join: the left side, when the right cannot open", func(ok, failing *ursus.LazyFrame) *ursus.LazyFrame {
			return ok.Join(failing, ursus.JoinOn(c("k")))
		}},
		{"join: a left side under a filter and a sort", func(ok, failing *ursus.LazyFrame) *ursus.LazyFrame {
			return ok.Filter(c("v").Gt(ursus.Lit(int64(0)))).Sort(ursus.Asc(c("k"))).
				Join(failing.Sort(ursus.Asc(c("k"))), ursus.JoinOn(c("k")))
		}},
		{"concat: the inputs before the failing one", func(ok, failing *ursus.LazyFrame) *ursus.LazyFrame {
			return ursus.Concat([]*ursus.LazyFrame{ok, ok.Select(c("k"), c("v")), failing})
		}},
		{"hstack: the frame before the failing one", func(ok, failing *ursus.LazyFrame) *ursus.LazyFrame {
			return ok.HStack(failing.Select(c("v").Alias("w")))
		}},
		{"as-of join: the left side", func(ok, failing *ursus.LazyFrame) *ursus.LazyFrame {
			return ok.JoinAsOf(failing, ursus.AsOfOn(c("k")))
		}},
		{"merge-sorted: the left side", func(ok, failing *ursus.LazyFrame) *ursus.LazyFrame {
			return ok.MergeSorted(failing, "k")
		}},
		{"a join inside a join", func(ok, failing *ursus.LazyFrame) *ursus.LazyFrame {
			return ok.Join(ok.Select(c("k"), c("v").Alias("w")), ursus.JoinOn(c("k"))).
				Join(failing.Select(c("k"), c("v").Alias("x")), ursus.JoinOn(c("k")))
		}},
	}
	for _, tc := range cases {
		for _, threads := range []int{1, 4} {
			t.Run(fmt.Sprintf("%s, %d threads", tc.name, threads), func(t *testing.T) {
				ok := &countedCSV{body: body}
				failing := &countedCSV{body: body, failAfter: 1}
				_, err := tc.build(ok.frame("ok.csv"), failing.frame("gone.csv")).
					Collect(t.Context(), ursus.WithThreads(threads))
				if err == nil {
					t.Fatal("the query ran, so nothing failed while planning")
				}
				if !strings.Contains(err.Error(), "the object is gone") {
					t.Fatalf("failed for another reason: %v", err)
				}
				if ok.opened.Load() < 2 {
					t.Fatalf("the healthy source was opened %d times: only for its schema, so its stream was never built", ok.opened.Load())
				}
				if n := ok.leaked(); n != 0 {
					t.Errorf("%d of the %d streams opened were never closed", n, ok.opened.Load())
				}
			})
		}
	}

	// The same query with nothing failing closes every stream too, so the cases
	// above are measuring the failure, not a leak of the healthy path.
	t.Run("control: the query succeeds and closes everything", func(t *testing.T) {
		ok, other := &countedCSV{body: body}, &countedCSV{body: body}
		df, err := ok.frame("a.csv").Join(other.frame("b.csv"), ursus.JoinOn(c("k"))).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 2 {
			t.Fatalf("%d rows, want 2", df.Height())
		}
		if n := ok.leaked() + other.leaked(); n != 0 {
			t.Errorf("%d streams left open after a successful query", n)
		}
	})
}
