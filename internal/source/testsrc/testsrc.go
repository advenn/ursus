// Package testsrc is a scan source that lies about its capabilities on purpose.
//
// # Why this exists
//
// Predicate pushdown into a scan had a soundness bug for the whole of v0.1 and no
// test could have caught it, because the only source in the tree declared no
// predicate support at all. The branch that mishandled an inexact source was
// never executed. A rule whose dangerous path is unreachable from the test suite
// is a rule that is not tested, however green the suite looks.
//
// So this package supplies the missing half of the contract: a source that claims
// Exact, Inexact, or something invalid, and then behaves accordingly — including
// badly. It is a DECORATOR rather than a bespoke in-memory table so that the same
// wrapper can be put around a real CSV or Parquet reader later and exercise the
// inexact path over real files.
//
// # What each mode does, and why
//
//	Inexact      returns EVERY row, ignoring keep entirely
//	Exact        returns exactly the rows keep accepts
//	Unsupported  returns every row; the engine's Filter does the work
//
// Inexact returning everything looks lazy and is the point. "A superset of the
// matching rows" is the whole of what Inexact promises, and the full set is the
// largest legal superset — the most adversarial answer a conforming source can
// give. If the optimizer deleted the Filter above an inexact scan, this returns
// unfiltered data and the soundness test fails loudly. A source that filtered
// most rows would pass a broken optimizer whenever the fixture happened not to
// contain a surviving non-match.
package testsrc

import (
	"context"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
)

// Inner is what this package decorates: a source that can both plan and produce.
type Inner interface {
	plan.Source
	source.Openable
}

// Source wraps an Inner and answers ClassifyPredicates however the test says.
type Source struct {
	inner    Inner
	classify func([]expr.Node) []plan.Pushdown
	keep     func(*data.Batch, int) bool

	mu    sync.Mutex
	specs []source.ScanSpec
}

// New builds a decorator with full control over both halves of the lie: what the
// source CLAIMS (classify) and what it DOES (keep).
//
// The two are deliberately independent. A source whose claim and behaviour are
// wired to the same value cannot express the case that matters most — claiming
// more than you deliver — and that case is exactly what the engine must survive.
func New(inner Inner, classify func([]expr.Node) []plan.Pushdown, keep func(*data.Batch, int) bool) *Source {
	return &Source{inner: inner, classify: classify, keep: keep}
}

// NewInexact claims every conjunct is Inexact and filters nothing.
//
// This is the regression test for the original bug in one constructor: if the
// Filter above the scan is dropped, unfiltered rows reach the user.
func NewInexact(inner Inner) *Source {
	return New(inner, all(plan.Inexact), nil)
}

// NewExact claims every conjunct is Exact and applies keep to honour that.
//
// keep must agree with the predicate the test filters on. That agreement is the
// test author's job, and stating it explicitly is better than embedding an
// expression evaluator here — internal/physical sits above this package, so
// reaching for Eval would be an import cycle, and a second evaluator written to
// dodge that is precisely the defect step 2's audit found four copies of.
func NewExact(inner Inner, keep func(*data.Batch, int) bool) *Source {
	return New(inner, all(plan.Exact), keep)
}

func all(p plan.Pushdown) func([]expr.Node) []plan.Pushdown {
	return func(preds []expr.Node) []plan.Pushdown {
		out := make([]plan.Pushdown, len(preds))
		for i := range out {
			out[i] = p
		}
		return out
	}
}

// --- plan.Source --------------------------------------------------------------

func (s *Source) Name() string                                      { return s.inner.Name() }
func (s *Source) Describe() string                                  { return s.inner.Describe() }
func (s *Source) Schema(ctx context.Context) (*dtype.Schema, error) { return s.inner.Schema(ctx) }
func (s *Source) Caps() plan.Caps                                   { return s.inner.Caps() }

// ClassifyPredicates delegates to the test's function verbatim, including when it
// returns a wrong-length or out-of-range answer. plan.ClassifyPredicates is what
// has to cope with that, and it cannot be tested by a wrapper that sanitises
// input first.
func (s *Source) ClassifyPredicates(preds []expr.Node) []plan.Pushdown {
	if s.classify == nil {
		return nil
	}
	return s.classify(preds)
}

// --- source.Openable ----------------------------------------------------------

func (s *Source) Open(ctx context.Context, spec source.ScanSpec) (source.BatchSource, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	s.mu.Unlock()

	full, err := s.inner.Schema(ctx)
	if err != nil {
		return nil, err
	}

	// Read the predicate's columns even though the projection no longer mentions
	// them, then emit only the projection. This is the read-set/emit-set split
	// every predicate-pushing source has to implement, and doing it here rather
	// than fudging it keeps this a worked example of ScanSpec rather than a fake
	// that only works because it cheats.
	inner, err := s.inner.Open(ctx, source.ScanSpec{
		Projection: spec.ReadSet(full),
		Predicate:  spec.Predicate,
		MaxRows:    spec.MaxRows,
		BatchSize:  spec.BatchSize,
	})
	if err != nil {
		return nil, err
	}

	emit := inner.Schema()
	if spec.Projection != nil {
		if emit, err = full.Select(spec.Projection); err != nil {
			return nil, err
		}
	}

	// Filter only when something was actually pushed. A source handed no predicate
	// must not invent one.
	keep := s.keep
	if len(spec.Predicate) == 0 {
		keep = nil
	}
	return &reader{inner: inner, keep: keep, emit: emit}, nil
}

// Specs returns the ScanSpecs this source has been opened with, so a test can
// assert that a predicate or a row limit reached the reader rather than being
// silently compensated for above it.
func (s *Source) Specs() []source.ScanSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]source.ScanSpec(nil), s.specs...)
}

// --- source.BatchSource -------------------------------------------------------

type reader struct {
	inner source.BatchSource
	keep  func(*data.Batch, int) bool
	emit  *dtype.Schema // what leaves this reader; a subset of inner's schema
}

func (r *reader) Schema() *dtype.Schema { return r.emit }
func (r *reader) Close() error          { return r.inner.Close() }

func (r *reader) Next(ctx context.Context) (*data.Batch, error) {
	for {
		b, err := r.inner.Next(ctx)
		if err != nil {
			return nil, err // includes io.EOF
		}

		if r.keep != nil {
			mask := bitmap.NewBuilder(b.Rows())
			for i := range b.Rows() {
				mask.Append(r.keep(b, i))
			}
			if b, err = kernel.FilterBatch(b,
				data.NewBool("__testsrc", mask.Finish(), bitmap.AllSet(b.Rows()))); err != nil {
				return nil, err
			}
			if b.Rows() == 0 {
				continue // a fully-filtered batch is not end-of-stream
			}
		}

		// Drop the predicate-only columns. Emitting them would change the scan's
		// schema out from under the plan.
		if r.emit.Equal(b.Schema()) {
			return b, nil
		}
		cols := make([]*data.Column, r.emit.Len())
		for i, f := range r.emit.All() {
			c, ok := b.ByName(f.Name)
			if !ok {
				return nil, uerr.UnknownColumn("scan", f.Name, b.Schema().Names())
			}
			cols[i] = c
		}
		if b, err = data.NewBatch(r.emit, cols); err != nil {
			return nil, err
		}
		return b, nil
	}
}
