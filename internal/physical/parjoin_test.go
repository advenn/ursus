package physical

import (
	"context"
	"io"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
)

// The parallel probe's one non-obvious property, tested where a failure is a diff
// rather than a timeout.
//
// The end-to-end test (TestParallelIsOrderPreserving) is the real guarantee, but it
// answers slowly and, when the lane protocol is broken, it can hang instead of
// failing — a consumer that advances its turn at the wrong moment leaves messages
// unread in every lane. This asks the same question of the operator directly, over a
// fixture small enough to name every row.

// batchStream yields a fixed list of batches and then EOF.
//
// A multi-batch probe side is what makes the fleet do anything at all: memsrc hands
// back whatever batches it was given and ignores the batch size, so a frame built
// from one set of slices is ONE batch — one job, one lane, and every other worker
// idle. That is not hypothetical; it is why the end-to-end join cases had to be
// rebuilt on parChunked.
type batchStream struct {
	schema *dtype.Schema
	bs     []*data.Batch
	i      int
}

func (s *batchStream) Schema() *dtype.Schema { return s.schema }
func (s *batchStream) Close() error          { return nil }

func (s *batchStream) Next(context.Context) (*data.Batch, error) {
	if s.i >= len(s.bs) {
		return nil, io.EOF
	}
	b := s.bs[s.i]
	s.i++
	return b, nil
}

// probeStream builds nBatch probe batches of rows rows each, keyed 0..k-1 cyclically.
func probeStream(t *testing.T, s *dtype.Schema, nBatch, rows int, k int64) *batchStream {
	t.Helper()
	out := &batchStream{schema: s}
	for b := range nBatch {
		ks := make([]int64, rows)
		vs := make([]int64, rows)
		for i := range rows {
			n := int64(b*rows + i)
			ks[i] = n % k
			vs[i] = n
		}
		batch, err := data.NewBatch(s, []*data.Column{
			data.NewFixed(s.Field(0).Name, dtype.Int64, ks, bitmap.AllSet(rows)),
			data.NewFixed(s.Field(1).Name, dtype.Int64, vs, bitmap.AllSet(rows)),
		})
		if err != nil {
			t.Fatal(err)
		}
		out.bs = append(out.bs, batch)
	}
	return out
}

// drainCol reads an operator to EOF and returns every value of column col, in order.
func drainCol(t *testing.T, op Operator, col string) []int64 {
	t.Helper()
	var got []int64
	for {
		b, err := op.Next(t.Context())
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		c, ok := b.ByName(col)
		if !ok {
			t.Fatalf("no column %q", col)
		}
		vals, err := data.Values[int64](c)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, vals...)
	}
}

// TestParProbeKeepsInputOrder is the lane protocol, stated as a test.
//
// The probe is one-batch-in, MANY-batches-out — one probe row can match thousands of
// build rows — so a worker's outputs for job i are followed by a terminator, and the
// consumer must drain a lane until that terminator before moving to the next. Turning
// after the first result instead reads one batch from each lane in rotation, which
// interleaves rows from different input batches.
//
// The build side here gives every probe row four matches, so a job produces several
// output batches at this batch size. With fan-out 1 the two rules agree and this
// would pass either way.
func TestParProbeKeepsInputOrder(t *testing.T) {
	const (
		nBatch = 12
		rows   = 64
		keys   = 8
		dup    = 4 // build rows per key
	)

	for _, threads := range []int{2, 3, 4, 8} {
		serial := runProbe(t, 1, nBatch, rows, keys, dup)
		par := runProbe(t, threads, nBatch, rows, keys, dup)

		// Pinned, because `len(par) != len(serial)` is satisfied by 0 == 0 and the
		// ordering loop below then runs zero times — every claim this test makes
		// would be vacuous on a join that produced nothing.
		//
		// 12 batches x 64 rows = 768 probe rows, keys n%8 so every one matches, and
		// the build side holds dup=4 rows per key: 768 x 4.
		const wantRows = nBatch * rows * dup
		if len(serial) != wantRows {
			t.Fatalf("the serial probe produced %d rows, want %d — the fixture no "+
				"longer exercises what this test claims", len(serial), wantRows)
		}
		if len(par) != len(serial) {
			t.Fatalf("threads=%d: %d rows, want %d", threads, len(par), len(serial))
		}
		for i := range serial {
			if par[i] != serial[i] {
				t.Fatalf("threads=%d: row %d is lv=%d, want %d — the lanes were read "+
					"out of input order", threads, i, par[i], serial[i])
			}
		}
	}
}

// TestParProbeUsesEveryWorker guards the property the end-to-end test cannot see: that
// work is actually distributed. A fleet that runs every job on lane 0 is correct and
// pointless, and every ordering assertion still passes.
func TestParProbeUsesEveryWorker(t *testing.T) {
	sink := joinFixture(t, plan.JoinInner)
	sink.threads = 4
	if got := sink.parallelProbe(); got != 4 {
		t.Fatalf("parallelProbe = %d, want 4", got)
	}
	sink.spec.emitBuildUnmatched = true
	if got := sink.parallelProbe(); got != 1 {
		t.Errorf("a Right/Full join must stay serial: got %d workers", got)
	}
}

func runProbe(t *testing.T, threads, nBatch, rows, keys, dup int) []int64 {
	t.Helper()

	sink := joinFixture(t, plan.JoinInner)
	sink.threads = threads
	sink.spec.batchSize = 100 // smaller than one job's output, so a job spans batches

	ks := make([]*int64, 0, keys*dup)
	vs := make([]int64, 0, keys*dup)
	for k := range int64(keys) {
		for d := range dup {
			ks = append(ks, i64p(k))
			vs = append(vs, k*100+int64(d))
		}
	}
	if err := sink.Consume(t.Context(),
		buildBatch(t, sink.right, ks, vs)); err != nil {
		t.Fatal(err)
	}

	probe := probeStream(t, sink.left, nBatch, rows, int64(keys))
	op, err := sink.Probe(t.Context(), probe)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()

	// lv carries the probe row's ordinal, so the output sequence IS the input order.
	return drainCol(t, op, "lv")
}
