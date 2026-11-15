// Package source is the runtime half of ursus's scan contract.
//
// The logical half lives in internal/plan: plan.Source knows only how to report a
// schema and its pushdown capabilities, which is all the optimizer needs. This
// package adds the data side.
//
// They are split so the dependency runs the right way. A concrete source
// (in-memory, Parquet, a user's own storage) implements both interfaces, and it
// must be able to do so without importing the physical planner — otherwise every
// new source would create an import cycle. Keeping the runtime contract in a low
// package means the physical layer imports sources, never the reverse.
package source

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// ScanSpec is what the physical planner asks a source to produce.
//
// Every field is a REQUEST, not a contract the source must meet. A source that
// ignores all of them is correct and merely slow: the engine trims columns,
// re-applies predicates and enforces limits above the scan regardless. Nothing
// below is load-bearing for correctness — with one exception, spelled out under
// Predicate, which is load-bearing in the other direction.
type ScanSpec struct {
	// Projection is the exact column list to EMIT, in this order. nil means every
	// column.
	//
	// Honouring it is optional: a source that ignores it gets a trim operator
	// inserted above the scan. Honouring it is where the win is, though — the
	// point of projection pushdown is that the reader never touches the bytes.
	Projection []string

	// Predicate holds conjuncts the source said it could use, via
	// plan.PredicatePushdown.ClassifyPredicates. A source that never claimed a
	// capability never sees this non-empty.
	//
	// # The read set is Projection PLUS these columns
	//
	// A conjunct classified Exact is removed from the plan, so nothing above the
	// scan references its columns and they are absent from Projection. The source
	// must still read them to evaluate the predicate, and must NOT emit them.
	//
	//	Projection: [a]   Predicate: [c > 5]
	//	  =>  decode {a, c}, filter on c, emit {a}
	//
	// A reader that decodes only Projection and then evaluates Predicate is
	// reading a column it never loaded. This is the one place where ignoring a
	// field is not the safe option: having claimed Exact, the source is now the
	// only thing applying that filter.
	//
	// Dropping rows the predicate rejects is required only for a conjunct the
	// source classified Exact. For Inexact, returning a superset is the definition
	// — skip what you can prove cannot match and let the Filter above finish.
	Predicate []expr.Node

	// MaxRows is an upper bound on the rows that will be consumed; 0 means
	// unlimited. Stopping early is the entire value of Head(n) on a large file.
	//
	// Overshooting is fine — a Parquet reader stopping at a row-group boundary,
	// say. A Limit operator above the scan always enforces the real bound.
	MaxRows int

	// Threads is how many workers the query is running on, so a source that can
	// use more than one core knows whether it may. 0 or 1 means stay serial.
	//
	// A source is free to ignore it — most have nothing to parallelise, because
	// reading is I/O and the work above them is where the CPU goes. The CSV and
	// Parquet readers are the exceptions: turning text into typed columns, and
	// decompressing and decoding pages, is real compute, and each used to do all of
	// it on one core while eight sat idle. The Parquet reader decodes row groups in
	// parallel (step 102), and a profile had found it at nearly half the CPU of the
	// queries it measured.
	//
	// It must be honoured in the direction that matters: WithThreads(1) is
	// documented as reproducing the serial operator tree exactly, so a source that
	// parallelised regardless would make that knob a lie.
	Threads int

	// BatchSize is the requested rows per batch. A source may return fewer.
	//
	// Operators rely on it: a filter's or a join's selection is an []int32, an
	// expression allocates its answer a batch at a time, and a stream holds a batch
	// at a time; so a source never returns more, but where Whole allows it.
	BatchSize int

	// Whole says the scan's batches reach only what holds a Null column in constant
	// space, whatever its rows (step 529): a projection of columns by name, a limit,
	// a slice and a concatenation, up to Collect's result or Count. A batch of only
	// Null columns, or of none, may then hold any number of rows, past BatchSize: a
	// record's, which an Arrow IPC stream may declare 2^40 of in 232 bytes and which
	// cut at BatchSize was 2^27 batches. Set by the physical planner, never where any
	// other operator is above the scan; a source may ignore it.
	Whole bool

	// ReadAhead is how many bytes of decoded batches a source may hold ahead of its
	// consumer; 0 is the source's own default. The Parquet reader decodes several
	// row groups at once and returns them in order, and before step 214 a row group
	// not at the head stopped three batches in, so most of each decoded only once it
	// was the head, one at a time.
	//
	// It is not charged to the memory budget: it is transient and bounded, and
	// charging it would make sinks spill for bytes about to be consumed. The engine
	// derives the bound from the budget instead.
	ReadAhead int64

	// Pages is told the bytes a source holds for a moment to read with, more as it
	// takes them and fewer as it gives them back: the Parquet reader's decompressed
	// pages (step 400). Not charged to the query's budget either, for ReadAhead's
	// reason: they count against the engine's other queries (execopt.Budget.Pressure).
	// nil tells no one.
	Pages func(d int64)

	// Eval evaluates an expression over a batch, as the engine does. A source that
	// evaluates a pushed conjunct itself uses it; the engine sets it, and a source
	// handed none evaluates nothing. The Parquet reader evaluates a string
	// conjunct once per dictionary value, to drop the rows that fail it before
	// copying them (step 193).
	Eval func(ctx context.Context, n expr.Node, b *data.Batch) (*data.Column, error)
}

// NoData says whether a batch of schema s holds nothing a row: its columns, if any,
// are all of the Null type, each a length (bitmap.Zeros). Such a batch is what a
// source may hand whole under ScanSpec.Whole (step 529).
func NoData(s *dtype.Schema) bool {
	for _, f := range s.All() {
		if !f.Type.IsNull() {
			return false
		}
	}
	return true
}

// ReadSet returns the columns this scan must decode: Projection plus every column
// referenced by Predicate, in full's order.
//
// It is here rather than left to each reader because getting it wrong is silent.
// A source that decodes only Projection and then evaluates Predicate reads a
// column it never loaded, and every source that pushes predicates needs this
// identical derivation — the kind of duplication that has already diverged once in
// this codebase.
//
// A nil Projection means "everything", and stays nil: there is nothing to widen.
func (s ScanSpec) ReadSet(full *dtype.Schema) []string {
	if s.Projection == nil || len(s.Predicate) == 0 {
		return s.Projection
	}
	want := make(map[string]struct{}, len(s.Projection))
	for _, n := range s.Projection {
		want[n] = struct{}{}
	}
	extra := false
	for _, p := range s.Predicate {
		for _, n := range expr.RootNames(p) {
			if _, ok := want[n]; !ok {
				want[n] = struct{}{}
				extra = true
			}
		}
	}
	if !extra {
		return s.Projection
	}
	out := make([]string, 0, len(want))
	for _, n := range full.Names() {
		if _, ok := want[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// BatchSource produces batches.
//
// Implementations MUST be safe for concurrent Next. v0.1 has a single consumer,
// but v0.2's morsel scheduler will have several, and retrofitting the
// synchronisation into a shipped reader is much worse than putting it in now.
type BatchSource interface {
	Schema() *dtype.Schema

	// Next returns the next batch, or (nil, io.EOF) when the stream is exhausted:
	// io.EOF itself, as io.Reader's end is. Any other error wrapping io.EOF is the
	// source's failure, which the scan reads as one (step 534), its io.EOF made
	// io.ErrUnexpectedEOF; a source keeps io.EOF out of its errors where it can say
	// what was cut short (uerr.Truncated).
	// An empty non-nil batch is allowed and is not end-of-stream.
	Next(ctx context.Context) (*data.Batch, error)

	Close() error
}

// Openable is implemented by a plan.Source that can produce data.
//
// The physical planner type-asserts to this. A plan.Source that does not
// implement it can still be type-checked and explained — useful for planning
// against a catalog — but not executed.
type Openable interface {
	Open(ctx context.Context, spec ScanSpec) (BatchSource, error)
}

// RowsOnly is implemented by a source that may answer a scan of no columns, an
// empty non-nil Projection, with batches of its rows and no columns: a count's,
// which reads no column (step 370). RowsOnly says whether it answers spec's; where
// it does not, or the source is not one, it is asked for one column, its cheapest,
// which the scan trims away.
type RowsOnly interface {
	RowsOnly(spec ScanSpec) bool
}
