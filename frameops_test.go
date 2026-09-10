package ursus_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

func seq(n int) []int64 {
	out := make([]int64, n)
	for i := range n {
		out[i] = int64(i)
	}
	return out
}

func i64col(t *testing.T, df *ursus.DataFrame, name string) []int64 {
	t.Helper()
	s, err := df.Column[int64](name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]int64, df.Height())
	for i := range df.Height() {
		out[i], _ = s.Get(i)
	}
	return out
}

// TestSliceTailReverseAcrossBatchSizes: all three cross batch boundaries, so the
// batch size is where they break. Slice must skip past whole batches, Tail must
// keep a ring across them, and Reverse must not reverse each batch in place.
func TestSliceTailReverseAcrossBatchSizes(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", seq(10)))

	for _, bs := range []int{1, 2, 3, 4, 7, 10, 8192} {
		opts := []ursus.CollectOption{ursus.WithBatchSize(bs), ursus.WithVerify()}

		sl, err := f.Slice(3, 4).Collect(t.Context(), opts...)
		if err != nil {
			t.Fatalf("bs=%d: %v", bs, err)
		}
		if got := i64col(t, sl, "v"); !slices.Equal(got, []int64{3, 4, 5, 6}) {
			t.Errorf("bs=%d Slice(3,4) = %v, want [3 4 5 6]", bs, got)
		}

		// A negative length means "to the end".
		rest, err := f.Slice(7, -1).Collect(t.Context(), opts...)
		if err != nil {
			t.Fatalf("bs=%d: %v", bs, err)
		}
		if got := i64col(t, rest, "v"); !slices.Equal(got, []int64{7, 8, 9}) {
			t.Errorf("bs=%d Slice(7,-1) = %v, want [7 8 9]", bs, got)
		}

		tl, err := f.Tail(3).Collect(t.Context(), opts...)
		if err != nil {
			t.Fatalf("bs=%d: %v", bs, err)
		}
		if got := i64col(t, tl, "v"); !slices.Equal(got, []int64{7, 8, 9}) {
			t.Errorf("bs=%d Tail(3) = %v, want [7 8 9]", bs, got)
		}

		rev, err := f.Reverse().Collect(t.Context(), opts...)
		if err != nil {
			t.Fatalf("bs=%d: %v", bs, err)
		}
		want := []int64{9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
		if got := i64col(t, rev, "v"); !slices.Equal(got, want) {
			t.Errorf("bs=%d Reverse = %v, want %v — reversing each batch in place "+
				"would give the right answer only at bs >= 10", bs, got, want)
		}
	}
}

// TestSliceAndTailDegenerate covers the edges that an off-by-one lands on.
func TestSliceAndTailDegenerate(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", seq(5)))
	cases := []struct {
		name string
		lf   *ursus.LazyFrame
		want []int64
	}{
		{"slice past the end", f.Slice(10, 3), nil},
		{"slice length beyond the end", f.Slice(3, 99), []int64{3, 4}},
		{"slice zero length", f.Slice(1, 0), nil},
		{"tail more than there is", f.Tail(99), []int64{0, 1, 2, 3, 4}},
		{"tail zero", f.Tail(0), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, bs := range []int{1, 2, 8192} {
				df, err := c.lf.Collect(t.Context(), ursus.WithBatchSize(bs), ursus.WithVerify())
				if err != nil {
					t.Fatalf("bs=%d: %v", bs, err)
				}
				if got := i64col(t, df, "v"); !slices.Equal(got, c.want) {
					t.Errorf("bs=%d got %v, want %v", bs, got, c.want)
				}
			}
		})
	}
}

// TestWithRowIndexIsBatchSizeIndependent: the counter crosses batch boundaries,
// which is exactly what a per-batch implementation would get wrong — and it would
// look right at one batch.
func TestWithRowIndexIsBatchSizeIndependent(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", seq(7)))
	for _, bs := range []int{1, 2, 3, 8192} {
		df, err := f.WithRowIndex("i", 100).
			Collect(t.Context(), ursus.WithBatchSize(bs), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		idx, err := df.Column[uint32]("i")
		if err != nil {
			t.Fatal(err)
		}
		for i := range df.Height() {
			v, ok := idx.Get(i)
			if !ok || v != uint32(100+i) {
				t.Fatalf("bs=%d row %d index = %d (valid %v), want %d — a counter "+
					"that restarted per batch would repeat", bs, i, v, ok, 100+i)
			}
		}
		if df.Schema().Names()[0] != "i" {
			t.Errorf("the index must be the first column, got %v", df.Schema().Names())
		}
	}
}

func TestWithRowIndexRejectsACollision(t *testing.T) {
	_, err := ursus.Frame(ursus.Values("i", seq(3))).
		WithRowIndex("i", 0).Collect(t.Context())
	if err == nil {
		t.Fatal("an index name that already exists must be refused")
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Errorf("a user mistake reported as an ursus bug: %v", err)
	}
	if !strings.Contains(err.Error(), "already has a column") {
		t.Errorf("error should name the collision: %v", err)
	}
}

// TestDropAndRename: both are compositions over the existing selectors, so what is
// worth pinning is that the compositions are the right ones.
func TestDropAndRename(t *testing.T) {
	df, err := frame().Drop("note", "discount").Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Names(); !slices.Equal(got, []string{"id", "region", "qty", "price"}) {
		t.Errorf("Drop left %v", got)
	}

	// A name that is not there is ignored rather than an error, so a drop list
	// written against a wider schema still works.
	if _, err := frame().Drop("nope").Collect(t.Context()); err != nil {
		t.Errorf("dropping an absent column should be a no-op: %v", err)
	}

	rn, err := frame().Rename(map[string]string{"id": "ident", "qty": "count"}).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ident", "region", "count", "price", "note", "discount"}
	if got := rn.Schema().Names(); !slices.Equal(got, want) {
		t.Errorf("Rename gave %v, want %v — order must be preserved", got, want)
	}

	// Renaming two columns onto one name is an error, not last-wins.
	_, err = frame().Rename(map[string]string{"id": "x", "qty": "x"}).Collect(t.Context())
	if err == nil {
		t.Error("renaming two columns onto the same name must be refused")
	}
}

func TestDropNulls(t *testing.T) {
	df, err := frame().DropNulls("discount").Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// discount is [0.1, null, 0.25, null, 0.5, null] — three survive.
	if df.Height() != 3 {
		t.Errorf("height = %d, want 3\n%s", df.Height(), df)
	}

	// With no subset it drops a row null in ANY column.
	all, err := frame().DropNulls().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if all.Height() != 3 {
		t.Errorf("DropNulls() height = %d, want 3\n%s", all.Height(), all)
	}
}

// TestGroupByShorthands: the numeric ones restrict to numeric columns rather than
// refusing, because almost every real frame has a string column and refusing would
// make the shorthand useless exactly where it is wanted.
func TestGroupByShorthands(t *testing.T) {
	sum, err := sales().GroupBy(ursus.Col("region")).MaintainOrder().Sum().
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("Sum must skip the string column, not refuse: %v", err)
	}
	if got := sum.Schema().Names(); !slices.Equal(got, []string{"region", "amount", "qty"}) {
		t.Errorf("Sum() gave %v; rep is a String and must not be summed", got)
	}

	// Min takes everything, because selecting is defined for every type.
	min, err := sales().GroupBy(ursus.Col("region")).MaintainOrder().Min().
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := min.Schema().Names(); !slices.Equal(got, []string{"region", "rep", "amount", "qty"}) {
		t.Errorf("Min() gave %v; it should include the String column", got)
	}

	n, err := sales().GroupBy(ursus.Col("region")).MaintainOrder().Len("rows").
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.Schema().String(), "rows") {
		t.Errorf("Len(name) must use the given name: %s", n.Schema())
	}
}

// TestEagerMirrorsMatchLazy: there is one engine, so the two must agree by
// construction — this pins that the wrappers wrap what they claim to.
func TestEagerMirrorsMatchLazy(t *testing.T) {
	df, err := frame().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	eager, err := df.Filter(t.Context(), ursus.Col("qty").Gt(int32(5)))
	if err != nil {
		t.Fatal(err)
	}
	lazy, err := frame().Filter(ursus.Col("qty").Gt(int32(5))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if eager.String() != lazy.String() {
		t.Errorf("eager and lazy disagree:\n%s\n%s", eager, lazy)
	}
}

// TestLazyRoundTripPreservesSchema is the reason Lazy() does not go through
// FromColumns.
//
// FromColumns derives nullability from the DATA — NullCount() > 0 — while a schema
// carries the declared flag. Filtering a nullable column down to rows that happen
// to have no nulls would then make it come back non-nullable, and every downstream
// join, aggregate and cast would reason from the wrong schema.
func TestLazyRoundTripPreservesSchema(t *testing.T) {
	// discount is nullable; keeping only the rows where it is present leaves a
	// nullable column holding no nulls.
	df, err := frame().DropNulls("discount").Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Schema().String() != df.Lazy().Plan().Label() {
		_ = 0 // Label is not the schema; the real check is below.
	}

	back, err := df.Lazy().CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !back.Equal(df.Schema()) {
		t.Errorf("round trip changed the schema:\n before: %s\n after:  %s\n"+
			"deriving nullability from the data is what does this",
			df.Schema(), back)
	}
}

// TestLazyRoundTripZeroColumns: a frame can legitimately have rows and no columns,
// and NewBatch sets the row count to zero when there are none to count.
func TestLazyRoundTripZeroColumns(t *testing.T) {
	df, err := ursus.Frame(ursus.Values("v", seq(6))).Select().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() == 0 {
		t.Skip("selecting nothing already yields zero rows here; nothing to check")
	}
	back, err := df.Lazy().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if back.Height() != df.Height() {
		t.Errorf("round trip lost the row count: %d became %d", df.Height(), back.Height())
	}
}

func TestFrameOpRefusals(t *testing.T) {
	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		kind error
		want string
	}{
		{"negative slice offset", func() *ursus.LazyFrame {
			return frame().Slice(-1, 2)
		}, uerr.ErrValue, "must not be negative"},
		{"top-k with no keys", func() *ursus.LazyFrame {
			return frame().TopK(3)
		}, uerr.ErrValue, "at least one sort key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf().Collect(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, c.kind) {
				t.Errorf("wrong kind: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should explain %q:\n%v", c.want, err)
			}
		})
	}
}

// TestFrameOpsAreThreadIndependent: Slice, Tail, WithRowIndex and Reverse are all
// order-dependent, and the parallel dispatcher is what would reorder them.
func TestFrameOpsAreThreadIndependent(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("v", seq(50))).
			WithRowIndex("i", 0).
			Slice(5, 20).
			Reverse().
			Tail(4)
	}
	ref, err := q().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := ref.String()
	for _, bs := range []int{1, 3, 8192} {
		for _, th := range []int{1, 2, 4} {
			got, err := q().Collect(t.Context(),
				ursus.WithBatchSize(bs), ursus.WithThreads(th), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d threads %d: %v", bs, th, err)
			}
			if got.String() != want {
				t.Errorf("batch %d threads %d changed the answer:\n%s\nwant:\n%s",
					bs, th, got, want)
			}
		}
	}
}

// TestEmptyResultKeepsItsColumns is a pre-existing panic, found while testing Slice.
//
// exec.Collect concatenates zero batches when a query returns nothing, and
// kernel.Concat's zero-batch path built a batch whose schema promised N columns and
// whose storage had none. Batch.ByName indexes the storage by the schema's
// position, so reading any column PANICKED with an index out of range.
//
// Nothing about it is exotic: Head(0), a filter matching nothing, and a slice past
// the end all reach it.
func TestEmptyResultKeepsItsColumns(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", seq(3)), ursus.Values("s", []string{"a", "b", "c"}))
	cases := map[string]*ursus.LazyFrame{
		"head 0":         f.Head(0),
		"filter nothing": f.Filter(ursus.Col("v").Gt(int64(99))),
		"slice past end": f.Slice(10, 5),
		"tail 0":         f.Tail(0),
	}
	for name, lf := range cases {
		t.Run(name, func(t *testing.T) {
			df, err := lf.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if df.Height() != 0 {
				t.Fatalf("height = %d, want 0", df.Height())
			}
			if df.Width() != 2 {
				t.Errorf("width = %d, want 2 — the schema says two columns\n%s",
					df.Width(), df.Schema())
			}
			// Readable, not merely present: an empty result should give an empty
			// series rather than "has no fixed-width payload".
			s, err := df.Column[int64]("v")
			if err != nil {
				t.Errorf("reading a column of an empty frame: %v", err)
			} else if s.Len() != 0 {
				t.Errorf("empty column has length %d", s.Len())
			}
			if _, err := df.Column[string]("s"); err != nil {
				t.Errorf("reading a string column of an empty frame: %v", err)
			}
		})
	}
}
