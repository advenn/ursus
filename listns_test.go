package ursus_test

// The `.list` namespace, which is what lets a caller work with a list without
// exploding it.
//
// Almost all of the risk here is one distinction: a NULL list is not an EMPTY
// list. They hold the same elements — none — and differ only in a validity bit,
// so working from "both are empty" gives wrong answers that never error. Every
// answer of the reducing and reshaping halves over the two fixtures below is Polars
// 2.1.0's, recorded in the listns group of testdata/oracle and spelled in
// oracle_listns_test.go since step 499, which took this file's hand-typed tables.
// What stays reads the fixtures from Parquet at several batch sizes, a differential;
// holds a Duration mean past int64 to math/big; and asserts the refusals.

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// nsFixture: [10,20] | [] | null | [5]
func nsFixture(t *testing.T) string {
	t.Helper()
	return writeListFile(t,
		[]int64{1, 2, 3, 4},
		[][]int64{{10, 20}, {}, nil, {5}},
		[]bool{true, true, false, true},
	)
}

// reshapeFixture:
//
//	row 0  [3, 1, 2]         ordering
//	row 1  [2, 1, 3, 2]      a duplicate that is NOT adjacent, and a tie to sort
//	row 2  [1, null]         a null ELEMENT
//	row 3  []                empty
//	row 4  null              a null LIST
//	row 5  [null, null, 4]   a REPEATED null
//
// Row 1's duplicate is deliberately not adjacent: with `[2, 1, 1]`, keeping the
// first occurrence and keeping the last give the same answer, and a Unique that
// kept the wrong one would pass.
func reshapeFixture(t *testing.T) string {
	t.Helper()
	return writeElemLists(t, []int64{1, 2, 3, 4, 5, 6}, []lrow{
		{vals: elems(3, 1, 2), ok: true},
		{vals: elems(2, 1, 3, 2), ok: true},
		{vals: []elem{val(1), nullElem}, ok: true},
		{ok: true}, // present but EMPTY
		{},         // NULL
		{vals: []elem{nullElem, nullElem, val(4)}, ok: true},
	})
}

func elems(vs ...int64) []elem {
	out := make([]elem, len(vs))
	for i, v := range vs {
		out[i] = val(v)
	}
	return out
}

// listCell reads one row of a List(Int64) column as its elements.
//
// Reading the column rather than matching rendered text is what tells a null list
// from an empty one — they render the same width and differ only in a validity bit,
// which is the whole difficulty.
func listCell(t *testing.T, df *ursus.DataFrame, name string, row int) ([]elem, bool) {
	t.Helper()
	c, found := df.Batch().ByName(name)
	if !found {
		t.Fatalf("no column %q in\n%s", name, df)
	}
	if c.DType().ID() != dtype.TypeList {
		t.Fatalf("%s came back as %s: a reshaping call must return a LIST", name, c.DType())
	}
	s, err := data.TypedColumn[int64](c.Child())
	if err != nil {
		t.Fatal(err)
	}
	start, end, valid := c.Lists().Get(row)
	out := []elem{}
	for e := start; e < end; e++ {
		if v, ok := s.Get(int(e)); ok {
			out = append(out, val(v))
		} else {
			out = append(out, nullElem)
		}
	}
	return out, valid
}

// TestListNamespaceBatchSizes: the whole namespace over the two fixtures, read from
// Parquet in batches of one to four rows and in one, answers alike — a List that
// spans batches is concatenated, and concatenation has to shift the child offsets.
// The answers themselves are the oracle's (listns/semantics, listns/reshape); this
// is the batch-size sweep the recorded cases cannot run.
func TestListNamespaceBatchSizes(t *testing.T) {
	L := ursus.Col("tags").List()
	for name, path := range map[string]string{"reducing": nsFixture(t), "reshaping": reshapeFixture(t)} {
		t.Run(name, func(t *testing.T) {
			q := ursus.ScanParquet(path).Select(
				L.Len().Alias("n"), L.Sum().Alias("sum"), L.Min().Alias("min"), L.Max().Alias("max"),
				L.Get(0).Alias("first"), L.Contains(int64(20)).Alias("has20"),
				L.Reverse().Alias("rev"), L.Head(2).Alias("head"), L.Tail(2).Alias("tail"), L.Slice(1, 2).Alias("slice"),
				L.Sort().Alias("sort"), L.SortDesc().Alias("desc"), L.Unique().Alias("uniq"), L.DropNulls().Alias("drop"),
				L.Sort().List().Head(2).List().Get(0).Alias("smallest"))
			whole, err := q.Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			for _, size := range []int{1, 2, 3, 4, 8192} {
				got, err := q.Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
				if err != nil {
					t.Fatalf("batch size %d: %v", size, err)
				}
				ursustest.AssertFrameEqual(t, got, whole, ursustest.CheckNullability())
			}
		})
	}
}

// TestListNamespaceRefusals: a non-List receiver is a type error, the way `.dt`
// on a non-temporal is.
func TestListNamespaceRefusals(t *testing.T) {
	path := nsFixture(t)

	_, err := ursus.ScanParquet(path).
		Select(ursus.Col("id").List().Len()).Collect(t.Context())
	if err == nil {
		t.Fatal("`.list` on a non-List column must be refused")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}

	// A value the element type cannot hold is a question with no answer, and
	// saying so beats matching nothing. Int64 elements, asked about a string.
	_, err = ursus.ScanParquet(path).
		Select(ursus.Col("tags").List().Contains("nope")).Collect(t.Context())
	if err == nil {
		t.Fatal("Contains with an incompatible value must be refused")
	}
}

// TestListReshapeRefusals: a count with no reading is refused rather than guessed
// at. Slice's OFFSET is the one place a negative number means something.
func TestListReshapeRefusals(t *testing.T) {
	path := reshapeFixture(t)

	for _, c := range []struct {
		what string
		e    ursus.Expr
	}{
		{"Head(-1)", ursus.Col("tags").List().Head(-1)},
		{"Tail(-1)", ursus.Col("tags").List().Tail(-1)},
		{"Slice(0, -1)", ursus.Col("tags").List().Slice(0, -1)},
	} {
		_, err := ursus.ScanParquet(path).Select(c.e).Collect(t.Context())
		if err == nil {
			t.Errorf("%s must be refused", c.what)
			continue
		}
		if !errors.Is(err, uerr.ErrValue) {
			t.Errorf("%s: kind should be Value: %v", c.what, err)
		}
	}

	// A non-List receiver, the same refusal the reducing half gives.
	_, err := ursus.ScanParquet(path).Select(ursus.Col("id").List().Reverse()).
		Collect(t.Context())
	if err == nil {
		t.Fatal("`.list.reverse` on a non-List column must be refused")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}
}

// TestListMeanKeepsTheElementType. list.mean answered Float64 for every element
// type without ever reading the element type, while the kernel derived the real one
// from ResolveAggBinding and ignored the declared one it was handed. They agree for
// an integer element — ResolveAggBinding(AggMean, Int64) IS Float64 — which is why
// every fixture in the repository agreed and the contract matrix, once it finally
// had a List column, did not.
//
// A List(Duration)'s mean is a Duration, and the mean of a list whose sum leaves
// int64 still fits, for the reason step 62 proved for the aggregate: min <= mean <=
// max. A hundred lists of three durations within a thousand ticks of MaxInt64, each
// list's three summing to a multiple of three, against math/big's sum over three.
// The ordinary means, Polars' own, are the oracle's listns/mean_duration_gen.
func TestListMeanKeepsTheElementType(t *testing.T) {
	r := rand.New(rand.NewPCG(499, 62))
	const groups = 100
	keys := make([]int64, 3*groups)
	vals := make([]time.Duration, 3*groups)
	want := make([]*big.Int, groups)
	for g := range groups {
		base := math.MaxInt64 - r.Int64N(1000)
		k := r.Int64N(math.MaxInt64 - base + 1)
		sum := new(big.Int)
		for j, v := range []int64{base - k, base, base + k} {
			keys[3*g+j], vals[3*g+j] = int64(g), time.Duration(v)
			sum.Add(sum, big.NewInt(v))
		}
		want[g] = sum.Quo(sum, big.NewInt(3))
	}
	c := ursus.Col
	got, err := ursus.Frame(ursus.Values("k", keys), ursus.Values("d", vals)).
		GroupBy(c("k")).Agg(c("d").Implode().Alias("ds")).
		Select(c("k"), c("ds").List().Mean().Alias("m")).Sort(ursus.Asc(c("k"))).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := got.Schema().ByName("m"); !ok || f.Type.ID() != dtype.TypeDuration {
		t.Fatalf("list.mean of a List(Duration) is %v, want a Duration", f.Type)
	}
	for g := range groups {
		v, ok, err := got.At[int64](g, "m")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || !want[g].IsInt64() || v != want[g].Int64() {
			t.Errorf("group %d: mean = %d (ok=%v), want %s", g, v, ok, want[g])
		}
	}
}

// TestNestedEqualityRefusesAtPlanTime. "Equality is defined for anything with a
// common type" was resolveComparison's rule, and Promote(List(T), List(T)) succeeds
// — so `a == b` over two List columns type-checked, promised a Bool, and then failed
// in the kernel, which dispatches on the PHYSICAL type and gets a List back
// unchanged. Field and Eval have to agree about what is LEGAL, not just about types.
//
// The refusal is at plan time now, so Explain and Collect answer the same way. It is
// a refusal rather than an implementation because element-wise nested equality has
// to decide what a null element does to a list's equality, and recurse: a feature,
// not this fix.
func TestNestedEqualityRefusesAtPlanTime(t *testing.T) {
	src := nsFixture(t)
	q := ursus.ScanParquet(src).Select(
		ursus.Col("tags").Eq(ursus.Col("tags")).Alias("same"))

	// Explain, so this is the planner refusing rather than a kernel failing.
	if _, err := q.Explain(t.Context()); err == nil {
		t.Error("equality over two List columns was planned")
	}
	_, err := q.Collect(t.Context())
	var ue *uerr.Error
	if !errors.As(err, &ue) || !errors.Is(&uerr.Error{Kind: ue.Kind}, uerr.ErrUnsupported) {
		t.Fatalf("want a KindUnsupported refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "List(Int64)") {
		t.Errorf("the refusal should name the type: %v", err)
	}

	// The complement, and the reason Promote was left alone rather than narrowed:
	// Concat of two frames carrying a List column unifies their schemas through
	// Promote(List(T), List(T)), and it must still work. Narrowing Promote would
	// have broken a working feature to fix a broken one.
	got, err := ursus.Concat([]*ursus.LazyFrame{
		ursus.ScanParquet(src), ursus.ScanParquet(src),
	}).Collect(t.Context())
	if err != nil {
		t.Fatalf("concatenating two frames with a List column must still work: %v", err)
	}
	if got.Height() != 8 {
		t.Errorf("concat produced %d rows, want 8", got.Height())
	}
}
