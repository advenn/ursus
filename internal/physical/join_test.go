package physical

import (
	"context"
	"testing"

	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/execopt"
	"ursus/internal/expr"
	"ursus/internal/kernel"
	"ursus/internal/plan"
)

// internal/physical/join.go had no tests at all before step 13, and the two this
// file adds are both properties the code DOCUMENTS and nothing asserted.
//
// That distinction matters more than the coverage does. A claim in a doc comment
// that no test exercises is not a weaker guarantee than a tested one — it is a
// different kind of thing, because the next reader believes it either way.

// joinFixture builds a build-side sink by hand.
//
// Layout comes from plan.Join.Layout() rather than being written out here, because
// join_layout.go's own doc calls itself "the single authority on a join's output"
// and a hand-written layout in a test is a second implementation of the collision
// algorithm — the exact thing that doc exists to prevent.
func joinFixture(t *testing.T, kind plan.JoinKind) *joinBuildSink {
	t.Helper()

	left := dtype.MustSchema(
		dtype.Of("k", dtype.Int64),
		dtype.NotNull("lv", dtype.Int64),
	)
	right := dtype.MustSchema(
		dtype.Of("k", dtype.Int64),
		dtype.NotNull("rv", dtype.Int64),
	)

	j := &plan.Join{
		Left:    &plan.Scan{Full: left},
		Right:   &plan.Scan{Full: right},
		LeftOn:  []expr.Node{&expr.Col{Name: "k"}},
		RightOn: []expr.Node{&expr.Col{Name: "k"}},
		Kind:    kind,
	}
	layout, err := j.Layout()
	if err != nil {
		t.Fatal(err)
	}
	return &joinBuildSink{
		out:      layout.Schema,
		layout:   layout,
		left:     left,
		right:    right,
		keys:     j.RightOn,
		leftKeys: j.LeftOn,
		spec:     newJoinSpec(j, 1024),
		ids:      make(map[string]int32),
		mem:      (*execopt.Budget)(nil).Account("join"),
	}
}

// buildBatch makes a right-side batch; a nil key value means null.
func buildBatch(t *testing.T, s *dtype.Schema, ks []*int64, vs []int64) *data.Batch {
	t.Helper()
	raw := make([]int64, len(ks))
	valid := bitmap.NewBuilder(len(ks))
	for i, k := range ks {
		if k != nil {
			raw[i] = *k
		}
		valid.Append(k != nil)
	}
	b, err := data.NewBatch(s, []*data.Column{
		data.NewFixed(s.Field(0).Name, dtype.Int64, raw, valid.Finish()),
		data.NewFixed(s.Field(1).Name, dtype.Int64, vs, bitmap.AllSet(len(vs))),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func i64p(v int64) *int64 { return &v }

// emptyBatches yields n zero-row batches and then EOF.
//
// NOT emptyOperator, which returns io.EOF immediately. The difference is the whole
// point of TestJoinFinishEqualsProbeOfEmpty: Finish is IMPLEMENTED as
// Probe(ctx, emptyOperator{}), so comparing it against that same operator would be
// true by construction and would assert nothing. A stream that produces batches and
// no rows is a different input that must give the same answer, and it is also the
// realistic one — a filtered-to-nothing probe side reaches EOF this way.
type emptyBatches struct {
	schema *dtype.Schema
	n      int
}

func (e *emptyBatches) Schema() *dtype.Schema { return e.schema }
func (e *emptyBatches) Close() error          { return nil }

func (e *emptyBatches) Next(ctx context.Context) (*data.Batch, error) {
	if e.n == 0 {
		return emptyOperator{schema: e.schema}.Next(ctx)
	}
	e.n--
	return emptyBatch(e.schema)
}

// TestJoinFinishEqualsProbeOfEmpty asserts the equivalence ProbeBuilder's doc has
// claimed since step 4 and no test has ever run:
//
//	Finish(ctx)  ==  Probe(ctx, an operator that yields no rows)
//
// The doc calls it the reason Finish is kept total rather than panic("use Probe"),
// and says it "gives the suite a free equivalence — see TestJoinFinishEqualsProbe
// OfEmpty". That test did not exist; repo-wide the only occurrence of the name was
// the comment naming it.
//
// It is worth having for its own sake and it is worth having NOW: the interesting
// half is Right and Full, where "no probe rows" means every build row is emitted
// null-padded, which is the same code path a spilled partition with no probe file
// takes.
func TestJoinFinishEqualsProbeOfEmpty(t *testing.T) {
	ctx := context.Background()

	for _, kind := range []plan.JoinKind{
		plan.JoinInner, plan.JoinLeft, plan.JoinRight,
		plan.JoinFull, plan.JoinSemi, plan.JoinAnti,
	} {
		t.Run(kind.String(), func(t *testing.T) {
			// A null key among the build rows, because Right and Full must emit it
			// unmatched and it is the row least likely to survive a rewrite.
			rows := func(s *joinBuildSink) {
				t.Helper()
				b := buildBatch(t, s.right,
					[]*int64{i64p(1), i64p(2), nil, i64p(2)},
					[]int64{10, 20, 30, 40})
				if err := s.Consume(ctx, b); err != nil {
					t.Fatal(err)
				}
			}

			a := joinFixture(t, kind)
			rows(a)
			finOp, err := a.Finish(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := drain(t, finOp)

			b := joinFixture(t, kind)
			rows(b)
			probeOp, err := b.Probe(ctx, &emptyBatches{schema: b.left, n: 3})
			if err != nil {
				t.Fatal(err)
			}
			got := drain(t, probeOp)

			if totalRows(got) != totalRows(want) {
				t.Fatalf("Probe(empty) gave %d rows, Finish gave %d",
					totalRows(got), totalRows(want))
			}
			if renderAll(t, got) != renderAll(t, want) {
				t.Errorf("Probe(empty) and Finish disagree\n got: %s\nwant: %s",
					renderAll(t, got), renderAll(t, want))
			}
			// And the shape is the documented one rather than "both happened to be
			// empty", which every kind but Right and Full legitimately is.
			wantRows := 0
			if kind == plan.JoinRight || kind == plan.JoinFull {
				wantRows = 4
			} else if kind == plan.JoinAnti {
				wantRows = 0 // no probe rows to keep
			}
			if totalRows(want) != wantRows {
				t.Errorf("Finish over an empty probe side gave %d rows, want %d",
					totalRows(want), wantRows)
			}
		})
	}
}

// TestJoinBuildSinkMergeRemapsIds runs a method that has never executed.
//
// joinBuildSink.Merge is fully written and fully documented — it carries the exact
// group-id remap hashAggSink.Merge records as the missing prerequisite for parallel
// aggregation — and it has no production caller and had no test. So its remap, its
// noKey pass-through and its cross-sink RequiresRightUnique check were all unverified.
//
// The two sinks are fed DIFFERENT key sequences on purpose, so their local id
// numbering genuinely diverges and the remap has something to do. A test that fed
// both the same keys would pass with the remap deleted.
func TestJoinBuildSinkMergeRemapsIds(t *testing.T) {
	ctx := context.Background()

	// Sink A sees 1, 2 (ids 0, 1). Sink B sees 2, 3 (ids 0, 1) — so B's id 0 is A's
	// id 1, and folding positionally would attribute B's rows to the wrong key.
	first := func(s *joinBuildSink) *data.Batch {
		return buildBatch(t, s.right, []*int64{i64p(1), i64p(2), nil}, []int64{10, 20, 30})
	}
	second := func(s *joinBuildSink) *data.Batch {
		return buildBatch(t, s.right, []*int64{i64p(2), i64p(3), nil}, []int64{40, 50, 60})
	}

	// A REAL probe side, not an empty one. This is the difference between a test
	// that runs Merge and a test that checks it: with no probe rows a Full join
	// emits every build row unmatched whatever its id, so deleting the remap
	// entirely still passes. The probe keys are 1, 2 and 3 — one per build key —
	// so a mis-numbered fold pairs the wrong rows and the values change.
	probe := func(s *joinBuildSink) Operator {
		return newBatchOperator(s.left, buildBatch(t, s.left,
			[]*int64{i64p(1), i64p(2), i64p(3)}, []int64{100, 200, 300}))
	}

	// The reference: one sink over the concatenation, which is what `other consumed
	// a LATER portion of the input` means.
	single := joinFixture(t, plan.JoinFull)
	if err := single.Consume(ctx, first(single)); err != nil {
		t.Fatal(err)
	}
	if err := single.Consume(ctx, second(single)); err != nil {
		t.Fatal(err)
	}
	wantOp, err := single.Probe(ctx, probe(single))
	if err != nil {
		t.Fatal(err)
	}
	want := drain(t, wantOp)

	a, b := joinFixture(t, plan.JoinFull), joinFixture(t, plan.JoinFull)
	if err := a.Consume(ctx, first(a)); err != nil {
		t.Fatal(err)
	}
	if err := b.Consume(ctx, second(b)); err != nil {
		t.Fatal(err)
	}
	if b.ids[string(encodeOne(t, 2))] != 0 {
		t.Fatal("the fixture no longer diverges: b assigned key 2 an id other than 0")
	}
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	gotOp, err := a.Probe(ctx, probe(a))
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, gotOp)

	if totalRows(got) != totalRows(want) {
		t.Fatalf("merged gave %d rows, single pass %d", totalRows(got), totalRows(want))
	}
	if renderAll(t, got) != renderAll(t, want) {
		t.Errorf("Merge changed the answer\n got: %s\nwant: %s",
			renderAll(t, got), renderAll(t, want))
	}
	// Probe 1 matches build {1}; probe 2 matches build {2, 2}; probe 3 matches
	// build {3}; the two null-keyed build rows match nothing and a Full join emits
	// them. 1 + 2 + 1 + 2 = 6.
	if totalRows(want) != 6 {
		t.Fatalf("the fixture emits %d rows, want 6", totalRows(want))
	}
}

// encodeOne returns the encoded group key for a single Int64 value, so a test can
// look one up in a sink's id map without duplicating the encoding.
func encodeOne(t *testing.T, v int64) []byte {
	t.Helper()
	c := data.NewFixed("k", dtype.Int64, []int64{v}, bitmap.AllSet(1))
	enc, err := kernel.NewGroupKeyEncoder("join", []*data.Column{c})
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), enc.Encode(0)...)
}
