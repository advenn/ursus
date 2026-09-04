package physical

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/expr"
	"ursus/internal/kernel"
	"ursus/internal/plan"
)

// internal/physical had no tests at all before step 5.
//
// That is a gap worth naming rather than quietly filling: the three Sink.Merge
// implementations exist specifically to make parallel and spilling execution
// possible later, and step 2 argued for building Merge early because "a Merge
// written later, against forgotten invariants, is a Merge that is wrong". Two of
// the three were correct and unverified; the third is documented-broken and was
// also unverified, which meant nothing distinguished them.

func testSchema(t *testing.T) *dtype.Schema {
	t.Helper()
	s, err := dtype.NewSchema(
		dtype.Field{Name: "k", Type: dtype.Int64},
		dtype.Field{Name: "v", Type: dtype.Int64},
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testBatch(t *testing.T, s *dtype.Schema, ks, vs []int64) *data.Batch {
	t.Helper()
	b, err := data.NewBatch(s, []*data.Column{
		data.NewFixed("k", dtype.Int64, ks, bitmap.AllSet(len(ks))),
		data.NewFixed("v", dtype.Int64, vs, bitmap.AllSet(len(vs))),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func drain(t *testing.T, op Operator) []*data.Batch {
	t.Helper()
	var out []*data.Batch
	for {
		b, err := op.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
}

func totalRows(bs []*data.Batch) int {
	n := 0
	for _, b := range bs {
		n += b.Rows()
	}
	return n
}

// TestSortSinkMergeEquivalence: splitting the input across two sinks and merging
// must equal one sink over the whole input, INCLUDING the tie order, because the
// sort is documented stable.
func TestSortSinkMergeEquivalence(t *testing.T) {
	s := testSchema(t)
	// Deliberate ties on k, so stability is observable through v.
	b1 := testBatch(t, s, []int64{3, 1, 2}, []int64{10, 11, 12})
	b2 := testBatch(t, s, []int64{1, 3, 2}, []int64{13, 14, 15})

	keySchema, err := dtype.NewSchema(dtype.Field{Name: "__key0", Type: dtype.Int64})
	if err != nil {
		t.Fatal(err)
	}
	newSink := func() *sortSink {
		return &sortSink{
			schema: s,
			keys:   []plan.SortKey{{Expr: &expr.Col{Name: "k"}}},
			kSch:   keySchema,
		}
	}

	ctx := context.Background()
	single := newSink()
	for _, b := range []*data.Batch{b1, b2} {
		if err := single.Consume(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	wantOp, err := single.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := drain(t, wantOp)

	a, bb := newSink(), newSink()
	if err := a.Consume(ctx, b1); err != nil {
		t.Fatal(err)
	}
	// `other` must have consumed a LATER portion — that precondition is what makes
	// stability well-defined under merging.
	if err := bb.Consume(ctx, b2); err != nil {
		t.Fatal(err)
	}
	if err := a.Merge(bb); err != nil {
		t.Fatal(err)
	}
	gotOp, err := a.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, gotOp)

	if totalRows(got) != totalRows(want) {
		t.Fatalf("merged %d rows, single pass %d", totalRows(got), totalRows(want))
	}
	if renderAll(t, got) != renderAll(t, want) {
		t.Errorf("merging changed the sort, including tie order\n got: %s\nwant: %s",
			renderAll(t, got), renderAll(t, want))
	}
}

// newTestAggSink builds a sink that computes max(v) grouped by k.
//
// Max rather than Sum because renderAll reads Int64 and a sum of Int64 widens to
// Int128 — the overflow guard step 2 chose deliberately. Max keeps the output type
// equal to the input's, which is all this fixture needs.
func newTestAggSink(t *testing.T) *hashAggSink {
	t.Helper()
	in := testSchema(t)
	keySchema, err := dtype.NewSchema(dtype.Field{Name: "__key0", Type: dtype.Int64})
	if err != nil {
		t.Fatal(err)
	}
	out, err := dtype.NewSchema(
		dtype.Field{Name: "__key0", Type: dtype.Int64},
		dtype.Field{Name: "__agg0", Type: dtype.Int64, Nullable: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	bind, err := expr.ResolveAggBinding(expr.AggMax, dtype.Int64)
	if err != nil {
		t.Fatal(err)
	}
	spec := aggSpec{op: expr.AggMax, input: &expr.Col{Name: "v"}, name: "__agg0",
		bind: bind, inType: dtype.Int64}
	acc, err := kernel.NewAccumulator(spec.op, spec.inType, spec.bind, spec.params)
	if err != nil {
		t.Fatal(err)
	}
	return &hashAggSink{
		schema:    out,
		keys:      []expr.Node{&expr.Col{Name: "k"}},
		specs:     []aggSpec{spec},
		ids:       map[string]int32{},
		accs:      []kernel.Accumulator{acc},
		keySchema: keySchema,
		inSchema:  in,
	}
}

// TestHashAggSinkMergeRemapsGroupIds is what the guard it replaced asked for.
//
// Until step 17 this function refused: two sinks fed different data assign ids by
// first appearance IN THEIR OWN STREAM, so worker A's group 0 may be "1" while
// worker B's group 0 is "9", and Accumulator.Merge folds positionally by ordinal.
// Merging without a remap adds one group's total to another — silently, with no
// length mismatch to catch it, which is why the old test said "make the
// unsupported case FAIL rather than corrupt".
//
// The fixture is built so a missing remap CANNOT pass: the two sinks see the same
// three keys in reversed order, so every id disagrees.
func TestHashAggSinkMergeRemapsGroupIds(t *testing.T) {
	ctx := context.Background()
	s := testSchema(t)

	// Same keys, opposite order — 1,2,3 against 3,2,1 — so every id disagrees. The
	// values are chosen so a positional fold cannot coincide with the right answer:
	// correct gives k=1→300, k=2→200, k=3→100, and dropping the remap gives exactly
	// the reverse.
	b1 := testBatch(t, s, []int64{1, 2, 3}, []int64{1, 2, 3})
	b2 := testBatch(t, s, []int64{3, 2, 1}, []int64{100, 200, 300})

	single := newTestAggSink(t)
	for _, b := range []*data.Batch{b1, b2} {
		if err := single.Consume(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	wantOp, err := single.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := drain(t, wantOp)

	a, b := newTestAggSink(t), newTestAggSink(t)
	if err := a.Consume(ctx, b1); err != nil {
		t.Fatal(err)
	}
	if err := b.Consume(ctx, b2); err != nil {
		t.Fatal(err)
	}
	if err := a.Merge(b); err != nil {
		t.Fatalf("merging divergently-numbered sinks must now work: %v", err)
	}
	gotOp, err := a.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, gotOp)

	if renderAll(t, got) != renderAll(t, want) {
		t.Errorf("merge disagreed with a single pass\n got: %s\nwant: %s",
			renderAll(t, got), renderAll(t, want))
	}
}

// TestHashAggSinkMergeAppendsNewGroupKeys is the half joinBuildSink.Merge never
// needed.
//
// A join's per-key state is a row COUNT, so its keys never have to be materialised
// again. Here Finish concatenates keyParts and pairs the result positionally with
// one aggregate row per group — so a group merged in from another sink without its
// key row shifts every key after it, and the output silently attributes each total
// to the wrong key.
func TestHashAggSinkMergeAppendsNewGroupKeys(t *testing.T) {
	ctx := context.Background()
	s := testSchema(t)

	// DISJOINT keys: every group of the second sink is new to the first.
	b1 := testBatch(t, s, []int64{1, 2}, []int64{10, 20})
	b2 := testBatch(t, s, []int64{8, 9}, []int64{80, 90})

	single := newTestAggSink(t)
	for _, b := range []*data.Batch{b1, b2} {
		if err := single.Consume(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	wantOp, err := single.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := drain(t, wantOp)

	a, b := newTestAggSink(t), newTestAggSink(t)
	if err := a.Consume(ctx, b1); err != nil {
		t.Fatal(err)
	}
	if err := b.Consume(ctx, b2); err != nil {
		t.Fatal(err)
	}
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	gotOp, err := a.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, gotOp)

	if renderAll(t, got) != renderAll(t, want) {
		t.Errorf("merged-in groups lost their key values\n got: %s\nwant: %s",
			renderAll(t, got), renderAll(t, want))
	}
}

// TestHashAggSinkMergeRefusesMaintainOrder pins the case the remap cannot fix.
//
// firstSeen holds each group's input ordinal, and two sinks that consumed
// INTERLEAVED portions numbered their rows independently — worker 1's ordinal 5 may
// precede worker 0's ordinal 3. There is no way to reconcile that without a global
// sequence the driver does not carry, so it is refused rather than approximated.
func TestHashAggSinkMergeRefusesMaintainOrder(t *testing.T) {
	a, b := newTestAggSink(t), newTestAggSink(t)
	a.ordered = true
	err := a.Merge(b)
	if err == nil {
		t.Fatal("merging an ordered sink must fail rather than invent an ordering")
	}
	if !strings.Contains(err.Error(), "MaintainOrder") {
		t.Errorf("the error should name what it refuses: %v", err)
	}
}

func renderAll(t *testing.T, bs []*data.Batch) string {
	t.Helper()
	var sb strings.Builder
	for _, b := range bs {
		for r := range b.Rows() {
			for c := range b.NumCols() {
				col := b.Column(c)
				s, err := data.TypedColumn[int64](col)
				if err != nil {
					t.Fatal(err)
				}
				v, ok := s.Get(r)
				if !ok {
					sb.WriteString("null,")
					continue
				}
				sb.WriteString(itoa(int(v)))
				sb.WriteString(",")
			}
			sb.WriteString(";")
		}
	}
	return sb.String()
}

// TestHashAggSinkMergeDoesNotDuplicateKeyRows is the first test to call Merge
// rather than sameNumbering, which is why the bug it pins survived.
//
// sameNumbering ACCEPTS only when every key of `other` is already present in this
// sink at the same id — so every row of other.keyParts duplicates one this sink
// already has. Merge appended them anyway, and Finish then concatenated ~2x
// nGroups key rows to pair positionally with nGroups aggregate rows, which
// data.NewBatch rejects.
//
// The only non-test call to .Merge( anywhere in the module is the Accumulator
// merge inside this same function, so nothing but a test would ever have run it.
func TestHashAggSinkMergeDoesNotDuplicateKeyRows(t *testing.T) {
	ks, err := dtype.NewSchema(dtype.Field{Name: "__key0", Type: dtype.Int64})
	if err != nil {
		t.Fatal(err)
	}
	keyRows := func() *data.Batch {
		b, err := data.NewBatch(ks, []*data.Column{
			data.NewFixed("__key0", dtype.Int64, []int64{1, 2}, bitmap.AllSet(2)),
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	ids := map[string]int32{"a": 0, "b": 1}
	a := &hashAggSink{ids: ids, keySchema: ks, keyParts: []*data.Batch{keyRows()}}
	b := &hashAggSink{ids: ids, keySchema: ks, keyParts: []*data.Batch{keyRows()}}

	if err := a.Merge(b); err != nil {
		t.Fatalf("merging a sink whose groups this one already has: %v", err)
	}

	rows := 0
	for _, p := range a.keyParts {
		rows += p.Rows()
	}
	if rows != len(ids) {
		t.Errorf("after Merge the sink holds %d key rows for %d groups; Finish pairs "+
			"them positionally with one aggregate row per group", rows, len(ids))
	}
}
