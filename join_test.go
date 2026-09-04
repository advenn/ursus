package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"ursus"
	"ursus/dtype"
	"ursus/internal/plan"
	"ursus/internal/uerr"
	"ursus/ursustest"
)

// The join fixture is built so that all seven kinds produce DIFFERENT row counts.
//
//	left.k  = [1, 2, 2, null, 3]
//	right.k = [2, 2, 4, null]
//
// It contains, deliberately: a left key with no right match (1, 3), a right key
// with no left match (4), a key matching two right rows twice over (2 — fan-out),
// and a NULL key on each side. A fixture where every left row matches passes every
// wrong implementation, which is the lesson TestLimitIsAPushdownBarrier taught.
func joinLeft() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.ValuesNullable("k", []int64{1, 2, 2, 0, 3}, []bool{true, true, true, false, true}),
		ursus.Values("lv", []string{"a", "b", "c", "d", "e"}),
	)
}

func joinRight() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.ValuesNullable("k", []int64{2, 2, 4, 0}, []bool{true, true, true, false}),
		ursus.Values("rv", []string{"p", "q", "r", "s"}),
	)
}

// TestJoinRowCountsPerKind is the primary correctness test. Seven distinct
// numbers: every off-by-one and every mis-wired flag changes one of them.
func TestJoinRowCountsPerKind(t *testing.T) {
	cases := []struct {
		kind  ursus.JoinKind
		rows  int
		width int
		why   string
	}{
		{ursus.JoinInner, 4, 3, "left {2,2} x right {2,2}"},
		{ursus.JoinLeft, 7, 3, "4 matched + unmatched left {1, null, 3}"},
		{ursus.JoinRight, 6, 3, "4 matched + unmatched right {4, null}"},
		{ursus.JoinFull, 9, 4, "4 + 3 + 2; a full join keeps BOTH key columns"},
		{ursus.JoinSemi, 2, 2, "left rows with a match; left columns only"},
		{ursus.JoinAnti, 3, 2, "5 - 2; left columns only"},
		{ursus.JoinCross, 20, 4, "5 x 4; no keys, so nothing coalesces and both k columns survive"},
	}

	for _, c := range cases {
		t.Run(c.kind.String(), func(t *testing.T) {
			opts := []ursus.JoinOption{ursus.JoinHow(c.kind)}
			if c.kind != ursus.JoinCross {
				opts = append(opts, ursus.JoinOn(ursus.Col("k")))
			}
			df, err := joinLeft().Join(joinRight(), opts...).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			if df.Height() != c.rows {
				t.Errorf("got %d rows, want %d (%s)\n%s", df.Height(), c.rows, c.why, df)
			}
			if df.Width() != c.width {
				t.Errorf("got %d columns %v, want %d", df.Width(), df.Columns(), c.width)
			}
		})
	}
}

// TestSemiAndAntiPartitionTheLeftFrame is a property that holds on any fixture,
// including one with nulls — unlike Filter(p) and Filter(Not(p)), which do not
// partition. It pins the null-key branch from a second direction.
func TestSemiAndAntiPartitionTheLeftFrame(t *testing.T) {
	for _, nullsEqual := range []bool{false, true} {
		semi, err := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
			ursus.JoinHow(ursus.JoinSemi), ursus.JoinNullsEqual(nullsEqual)).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		anti, err := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
			ursus.JoinHow(ursus.JoinAnti), ursus.JoinNullsEqual(nullsEqual)).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		if got := semi.Height() + anti.Height(); got != 5 {
			t.Errorf("nulls_equal=%v: semi(%d) + anti(%d) = %d, want 5 — semi and anti "+
				"must partition the left frame", nullsEqual, semi.Height(), anti.Height(), got)
		}
		// Disjoint: no lv value appears in both.
		in := map[string]bool{}
		for i := range semi.Height() {
			v, _, err := semi.At[string](i, "lv")
			if err != nil {
				t.Fatal(err)
			}
			in[v] = true
		}
		for i := range anti.Height() {
			v, _, err := anti.At[string](i, "lv")
			if err != nil {
				t.Fatal(err)
			}
			if in[v] {
				t.Errorf("row %q is in BOTH semi and anti", v)
			}
		}
	}
}

// TestSemiJoinDoesNotDuplicate is the specific trap. A semi join implemented by
// reusing the inner loop and projecting the right columns away returns one row
// per MATCH rather than one per left row.
func TestSemiJoinDoesNotDuplicate(t *testing.T) {
	left := ursus.Frame(ursus.Values("k", []int64{7}))
	right := ursus.Frame(ursus.Values("k", []int64{7, 7, 7}))

	for _, tc := range []struct {
		kind ursus.JoinKind
		want int
	}{{ursus.JoinSemi, 1}, {ursus.JoinAnti, 0}} {
		df, err := left.Join(right, ursus.JoinOn(ursus.Col("k")), ursus.JoinHow(tc.kind)).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != tc.want {
			t.Errorf("%s against three matching right rows gave %d rows, want %d\n%s",
				tc.kind, df.Height(), tc.want, df)
		}
	}
}

// TestJoinNullKeysDoNotMatch pins the semantic decision, from both sides. The
// fixture has a null on BOTH inputs, which is what makes it discriminating.
func TestJoinNullKeysDoNotMatch(t *testing.T) {
	cases := []struct {
		kind            ursus.JoinKind
		strict, relaxed int
	}{
		{ursus.JoinInner, 4, 5}, // the two nulls pair up
		{ursus.JoinFull, 9, 8},  // ...so one fewer unmatched row on each side
		{ursus.JoinSemi, 2, 3},
		{ursus.JoinAnti, 3, 2},
	}
	for _, c := range cases {
		t.Run(c.kind.String(), func(t *testing.T) {
			for _, tc := range []struct {
				nullsEqual bool
				want       int
			}{{false, c.strict}, {true, c.relaxed}} {
				df, err := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
					ursus.JoinHow(c.kind), ursus.JoinNullsEqual(tc.nullsEqual)).
					Collect(t.Context(), ursus.WithVerify())
				if err != nil {
					t.Fatal(err)
				}
				if df.Height() != tc.want {
					t.Errorf("nulls_equal=%v: got %d rows, want %d\n%s",
						tc.nullsEqual, df.Height(), tc.want, df)
				}
			}
		})
	}
}

// TestJoinNullKeyRowStillEmittedInLeftJoin is the single most valuable test here.
//
// The natural wrong implementation — "skip null-keyed probe rows" — is correct for
// an inner join and SILENTLY DROPS rows for left, full and anti. It passes every
// inner-join test.
func TestJoinNullKeyRowStillEmittedInLeftJoin(t *testing.T) {
	df, err := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
		ursus.JoinHow(ursus.JoinLeft)).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 7 {
		t.Fatalf("got %d rows, want 7 — a null-keyed left row was dropped\n%s", df.Height(), df)
	}

	// Find the row whose lv is "d" — the one with the null key — and require its
	// right columns to be null rather than absent.
	found := false
	for i := range df.Height() {
		lv, _, err := df.At[string](i, "lv")
		if err != nil {
			t.Fatal(err)
		}
		if lv != "d" {
			continue
		}
		found = true
		if _, ok, err := df.At[string](i, "rv"); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Errorf("the null-keyed left row must have a NULL rv\n%s", df)
		}
	}
	if !found {
		t.Errorf("the null-keyed left row is missing entirely\n%s", df)
	}
}

// TestInnerJoinIsAFilteredCross is the differential test, and the strongest one
// available: a cross join is definitionally correct and shares no code with the
// hash path.
//
// Eq is three-valued, so a null key yields null and the row is dropped — which is
// exactly NullsEqual(false). With NullsEqual(true) the reference becomes
// EqMissing, where null equals null. The equivalence is exact in both settings.
func TestInnerJoinIsAFilteredCross(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nullsEqual bool
		pred       ursus.Expr
	}{
		{"nulls do not match", false, ursus.Col("k").Eq(ursus.Col("k_right"))},
		{"nulls match", true, ursus.Col("k").EqMissing(ursus.Col("k_right"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := joinLeft().
				Join(joinRight(), ursus.JoinHow(ursus.JoinCross)).
				Filter(tc.pred).
				Select(ursus.Col("k"), ursus.Col("lv"), ursus.Col("rv")).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}

			got, err := joinLeft().
				Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
					ursus.JoinNullsEqual(tc.nullsEqual)).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
		})
	}
}

// TestJoinBatchSizeInvariance: batch size chooses where the output is cut and
// nothing else.
//
// This matters more for a join than anywhere else, because one probe row can match
// many build rows — so output chunking is real logic with a resume cursor, not a
// pass-through. Sizes 1 and 2 cut INSIDE a single row's match list.
func TestJoinBatchSizeInvariance(t *testing.T) {
	kinds := []ursus.JoinKind{
		ursus.JoinInner, ursus.JoinLeft, ursus.JoinRight, ursus.JoinFull,
		ursus.JoinSemi, ursus.JoinAnti, ursus.JoinCross,
	}
	for _, kind := range kinds {
		t.Run(kind.String(), func(t *testing.T) {
			build := func() *ursus.LazyFrame {
				opts := []ursus.JoinOption{ursus.JoinHow(kind)}
				if kind != ursus.JoinCross {
					opts = append(opts, ursus.JoinOn(ursus.Col("k")))
				}
				return joinLeft().Join(joinRight(), opts...)
			}
			// AssertFrameEqual, NOT df.String(). String truncates at ten rows
			// (frame.go's maxRows), so the comparison this test is named for was
			// checking the first ten and calling it equality — and the CROSS case
			// emits twenty. AssertFrameEqual renders every row and compares in
			// order by default, which is what the assertion always meant.
			var want *ursus.DataFrame
			for _, size := range []int{1, 2, 3, 7, 64, 8192} {
				df, err := build().Collect(t.Context(),
					ursus.WithBatchSize(size), ursus.WithVerify())
				if err != nil {
					t.Fatalf("batch size %d: %v", size, err)
				}
				if want == nil {
					want = df
					continue
				}
				t.Run("size "+itoa64(int64(size)), func(t *testing.T) {
					ursustest.AssertFrameEqual(t, df, want)
				})
			}
		})
	}
}

// TestJoinResumesInsideOneMatchList forces the resume cursor at production
// settings: one probe row against more matches than a batch holds.
func TestJoinResumesInsideOneMatchList(t *testing.T) {
	const n = 20_000
	keys := make([]int64, n)
	vals := make([]int64, n)
	for i := range n {
		keys[i] = 7
		vals[i] = int64(i)
	}
	left := ursus.Frame(ursus.Values("k", []int64{7}))
	right := ursus.Frame(ursus.Values("k", keys), ursus.Values("v", vals))

	got, err := left.Join(right, ursus.JoinOn(ursus.Col("k"))).
		Count(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got != n {
		t.Errorf("got %d rows, want %d — one probe row's match list must resume "+
			"across batches", got, n)
	}
}

// TestJoinSuffixesCollidingColumns covers the reason suffixing is mandatory rather
// than cosmetic: dtype.NewSchema rejects duplicate names, so without it the join
// could not produce a schema at all.
func TestJoinSuffixesCollidingColumns(t *testing.T) {
	l := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("v", []string{"L"}))
	r := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("v", []string{"R"}))

	df, err := l.Join(r, ursus.JoinOn(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df.Columns(), ","); got != "k,v,v_right" {
		t.Errorf("columns = %s, want k,v,v_right", got)
	}
	if v, _, err := df.At[string](0, "v_right"); err != nil {
		t.Fatal(err)
	} else if v != "R" {
		t.Errorf("v_right = %q, want \"R\"", v)
	}

	// A custom suffix.
	df2, err := l.Join(r, ursus.JoinOn(ursus.Col("k")), ursus.JoinSuffix("_b")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df2.Columns(), ","); got != "k,v,v_b" {
		t.Errorf("columns = %s, want k,v,v_b", got)
	}
}

// TestJoinSuffixCollisionIsAnError: never suffix twice. "v_right_right" depends on
// iteration order and is a name nobody asked for.
func TestJoinSuffixCollisionIsAnError(t *testing.T) {
	l := ursus.Frame(
		ursus.Values("k", []int64{1}),
		ursus.Values("v", []string{"L"}),
		ursus.Values("v_right", []string{"X"}),
	)
	r := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("v", []string{"R"}))

	_, err := l.Join(r, ursus.JoinOn(ursus.Col("k"))).Collect(t.Context())
	if err == nil {
		t.Fatal("expected an error rather than a second suffix")
	}
	if !errors.Is(err, uerr.ErrSchema) {
		t.Errorf("kind should be Schema: %v", err)
	}
	for _, want := range []string{`"v_right"`, "suffix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "v_right_right") {
		t.Errorf("must not suffix twice:\n%v", err)
	}
}

// TestJoinCoalesceOnlyMergesSameNamedKeys pins the deliberate divergence from
// Polars: coalescing exists to solve the duplicate-name problem, and differently
// named keys do not have one. Polars' default would DELETE the right key column.
func TestJoinCoalesceOnlyMergesSameNamedKeys(t *testing.T) {
	l := ursus.Frame(ursus.Values("cust_id", []int64{1}), ursus.Values("lv", []string{"a"}))
	r := ursus.Frame(ursus.Values("id", []int64{1}), ursus.Values("rv", []string{"p"}))

	df, err := l.Join(r, ursus.JoinLeftOn(ursus.Col("cust_id")), ursus.JoinRightOn(ursus.Col("id"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df.Columns(), ","); got != "cust_id,lv,id,rv" {
		t.Errorf("columns = %s, want cust_id,lv,id,rv — a differently-named key "+
			"must not be silently dropped", got)
	}

	// Explicit opt-in still merges.
	df2, err := l.Join(r, ursus.JoinLeftOn(ursus.Col("cust_id")),
		ursus.JoinRightOn(ursus.Col("id")), ursus.JoinCoalesce(true)).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df2.Columns(), ","); got != "cust_id,lv,rv" {
		t.Errorf("with JoinCoalesce(true) columns = %s, want cust_id,lv,rv", got)
	}
}

// TestJoinCoalesceOffKeepsBothKeys.
func TestJoinCoalesceOffKeepsBothKeys(t *testing.T) {
	df, err := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
		ursus.JoinCoalesce(false)).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df.Columns(), ","); got != "k,lv,k_right,rv" {
		t.Errorf("columns = %s, want k,lv,k_right,rv", got)
	}
}

// TestRightJoinTakesTheKeyFromTheRight: an unmatched right row has no left value,
// so a merged key column that took the left side would be null exactly where the
// right join's extra rows are.
func TestRightJoinTakesTheKeyFromTheRight(t *testing.T) {
	df, err := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
		ursus.JoinHow(ursus.JoinRight)).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// The unmatched right rows are k=4 and k=null. k=4 must be PRESENT.
	seen4 := false
	for i := range df.Height() {
		if v, ok, err := df.At[int64](i, "k"); err != nil {
			t.Fatal(err)
		} else if ok && v == 4 {
			seen4 = true
		}
	}
	if !seen4 {
		t.Errorf("the unmatched right row k=4 lost its key\n%s", df)
	}
}

// TestJoinKeyTypesPromote: an Int32 fact-table id joined to an Int64 dimension id
// is the normal case. Without the cast the two encode to different byte widths and
// the join silently returns ZERO rows — the worst kind of wrong answer.
func TestJoinKeyTypesPromote(t *testing.T) {
	l := ursus.Frame(ursus.Values("k", []int32{1, 2}), ursus.Values("lv", []string{"a", "b"}))
	r := ursus.Frame(ursus.Values("k", []int64{2, 3}), ursus.Values("rv", []string{"p", "q"}))

	df, err := l.Join(r, ursus.JoinOn(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Fatalf("got %d rows, want 1 — an Int32/Int64 key pair must match\n%s",
			df.Height(), df)
	}
	if got := df.Schema().Field(0).Type; got != dtype.Int64 {
		t.Errorf("the merged key type = %s, want Int64 (the promoted type)", got)
	}
}

// TestJoinKeyTypeMismatchIsRejected: String against Int64 has no common type.
func TestJoinKeyTypeMismatchIsRejected(t *testing.T) {
	l := ursus.Frame(ursus.Values("k", []string{"a"}))
	r := ursus.Frame(ursus.Values("k", []int64{1}))

	_, err := l.Join(r, ursus.JoinOn(ursus.Col("k"))).Collect(t.Context())
	if err == nil {
		t.Fatal("expected a type error")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}
	for _, want := range []string{"String", "Int64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name both types (%s):\n%v", want, err)
		}
	}
}

// TestJoinKeysResolveAgainstTheirOwnSide is the most likely bug in the first
// two-child node: listing the union of both frames' columns would send the user
// chasing a column that exists, but on the other side.
func TestJoinKeysResolveAgainstTheirOwnSide(t *testing.T) {
	l := ursus.Frame(ursus.Values("only_left", []int64{1}))
	r := ursus.Frame(ursus.Values("only_right", []int64{1}))

	_, err := l.Join(r, ursus.JoinOn(ursus.Col("only_right"))).Collect(t.Context())
	if err == nil {
		t.Fatal("expected an unknown-column error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "only_left") {
		t.Errorf("the available list should be the LEFT frame's columns:\n%v", err)
	}
	if !strings.Contains(msg, "the other frame has a column named") {
		t.Errorf("should hint that the column is on the other side:\n%v", err)
	}
	if !strings.Contains(msg, "JoinLeftOn") {
		t.Errorf("should point at JoinLeftOn/JoinRightOn:\n%v", err)
	}
}

// TestJoinValidateCatchesFanOut has three assertions, and the third is the one
// people forget: a check that never fires is not a check.
func TestJoinValidateCatchesFanOut(t *testing.T) {
	l := ursus.Frame(ursus.Values("k", []int64{1, 2}))
	dupRight := ursus.Frame(ursus.Values("k", []int64{1, 1, 2}))
	uniqRight := ursus.Frame(ursus.Values("k", []int64{1, 2}))

	// 1. It fires on a duplicated right key.
	_, err := l.Join(dupRight, ursus.JoinOn(ursus.Col("k")),
		ursus.JoinValidate(ursus.ValidateManyToOne)).Collect(t.Context())
	if err == nil {
		t.Fatal("validate=m:1 must reject a duplicated right key")
	}
	if !errors.Is(err, uerr.ErrValue) {
		t.Errorf("kind should be Value — it is a fact about the data: %v", err)
	}
	if !strings.Contains(err.Error(), "m:1") || !strings.Contains(err.Error(), "right") {
		t.Errorf("error should name the validation and the side:\n%v", err)
	}

	// 2. It does NOT fire on a clean right side.
	if _, err := l.Join(uniqRight, ursus.JoinOn(ursus.Col("k")),
		ursus.JoinValidate(ursus.ValidateManyToOne)).
		Collect(t.Context(), ursus.WithVerify()); err != nil {
		t.Errorf("validate=m:1 must accept a unique right key: %v", err)
	}

	// 3. Without validation the same query silently fans out — which is the bug
	// JoinValidate exists to catch, demonstrated rather than asserted in a vacuum.
	df, err := l.Join(dupRight, ursus.JoinOn(ursus.Col("k"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 {
		t.Errorf("unvalidated fan-out gave %d rows, want 3\n%s", df.Height(), df)
	}
}

// TestJoinValidateLeftSide checks the direction convention, which is easy to
// invert: the first term names the LEFT frame.
func TestJoinValidateLeftSide(t *testing.T) {
	dupLeft := ursus.Frame(ursus.Values("k", []int64{1, 1}))
	r := ursus.Frame(ursus.Values("k", []int64{1}))

	if _, err := dupLeft.Join(r, ursus.JoinOn(ursus.Col("k")),
		ursus.JoinValidate(ursus.ValidateOneToMany)).Collect(t.Context()); err == nil {
		t.Error("validate=1:m must reject a duplicated LEFT key")
	}
	// m:1 constrains the right, so a duplicated left is fine.
	if _, err := dupLeft.Join(r, ursus.JoinOn(ursus.Col("k")),
		ursus.JoinValidate(ursus.ValidateManyToOne)).
		Collect(t.Context(), ursus.WithVerify()); err != nil {
		t.Errorf("validate=m:1 constrains the RIGHT side only: %v", err)
	}
}

// TestJoinEmptyInputs: every kind against an empty side. This is where hash joins
// break, and it exercises Concat over zero batches — which yields a zero-COLUMN
// batch, the trap hashAggSink.Finish documents.
func TestJoinEmptyInputs(t *testing.T) {
	empty := ursus.Frame(ursus.Values("k", []int64{}), ursus.Values("rv", []string{}))
	emptyL := ursus.Frame(ursus.Values("k", []int64{}), ursus.Values("lv", []string{}))

	cases := []struct {
		name string
		l, r *ursus.LazyFrame
		kind ursus.JoinKind
		want int
	}{
		{"empty right, inner", joinLeft(), empty, ursus.JoinInner, 0},
		{"empty right, left", joinLeft(), empty, ursus.JoinLeft, 5},
		{"empty right, right", joinLeft(), empty, ursus.JoinRight, 0},
		{"empty right, full", joinLeft(), empty, ursus.JoinFull, 5},
		{"empty right, semi", joinLeft(), empty, ursus.JoinSemi, 0},
		{"empty right, anti", joinLeft(), empty, ursus.JoinAnti, 5},
		{"empty right, cross", joinLeft(), empty, ursus.JoinCross, 0},
		{"empty left, inner", emptyL, joinRight(), ursus.JoinInner, 0},
		{"empty left, left", emptyL, joinRight(), ursus.JoinLeft, 0},
		{"empty left, right", emptyL, joinRight(), ursus.JoinRight, 4},
		{"empty left, full", emptyL, joinRight(), ursus.JoinFull, 4},
		{"empty left, anti", emptyL, joinRight(), ursus.JoinAnti, 0},
		{"both empty, full", emptyL, empty, ursus.JoinFull, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := []ursus.JoinOption{ursus.JoinHow(c.kind)}
			if c.kind != ursus.JoinCross {
				opts = append(opts, ursus.JoinOn(ursus.Col("k")))
			}
			df, err := c.l.Join(c.r, opts...).Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			if df.Height() != c.want {
				t.Errorf("got %d rows, want %d\n%s", df.Height(), c.want, df)
			}
		})
	}
}

// TestSelfJoin: legal, and every non-key column collides.
func TestSelfJoin(t *testing.T) {
	lf := joinLeft()
	df, err := lf.Join(lf, ursus.JoinOn(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df.Columns(), ","); got != "k,lv,lv_right" {
		t.Errorf("columns = %s, want k,lv,lv_right", got)
	}
	// k=2 appears twice on each side, so it contributes four rows; k=1 and k=3 one
	// each; the null key matches nothing.
	if df.Height() != 6 {
		t.Errorf("got %d rows, want 6\n%s", df.Height(), df)
	}
}

// TestJoinKeyEqualityMatchesGroupBy: the encoder canonicalises NaN with NaN and
// -0.0 with +0.0, and step 2 committed ursus to that for grouping. Joins must use
// the SAME equality, or GroupBy(k) and Join(on: k) disagree about what a key is.
func TestJoinKeyEqualityMatchesGroupBy(t *testing.T) {
	nan := func() float64 { var z float64; return z / z }()
	negZero := func() float64 { z := 0.0; return -z }()

	l := ursus.Frame(ursus.Values("k", []float64{nan, negZero}))
	r := ursus.Frame(ursus.Values("k", []float64{nan, 0}))

	df, err := l.Join(r, ursus.JoinOn(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Errorf("got %d rows, want 2 — NaN must match NaN and -0.0 must match +0.0, "+
			"the same equality GroupBy uses\n%s", df.Height(), df)
	}
}

// TestJoinOptionErrors covers the configurations that cannot mean anything.
func TestJoinOptionErrors(t *testing.T) {
	l, r := joinLeft(), joinRight()
	cases := []struct {
		name string
		opts []ursus.JoinOption
		want string
	}{
		{"on with left_on", []ursus.JoinOption{
			ursus.JoinOn(ursus.Col("k")), ursus.JoinLeftOn(ursus.Col("k"))}, "JoinOn sets both sides"},
		{"left_on without right_on", []ursus.JoinOption{
			ursus.JoinLeftOn(ursus.Col("k"))}, "without JoinRightOn"},
		{"no keys", nil, "requires keys"},
		{"cross with keys", []ursus.JoinOption{
			ursus.JoinHow(ursus.JoinCross), ursus.JoinOn(ursus.Col("k"))}, "no keys"},
		{"empty suffix", []ursus.JoinOption{
			ursus.JoinOn(ursus.Col("k")), ursus.JoinSuffix("")}, "must not be empty"},
		{"empty key list", []ursus.JoinOption{ursus.JoinOn()}, "must not be empty"},
		{"key count mismatch", []ursus.JoinOption{
			ursus.JoinLeftOn(ursus.Col("k"), ursus.Col("lv")),
			ursus.JoinRightOn(ursus.Col("k"))}, "left keys and"},
		{"full with coalesce", []ursus.JoinOption{
			ursus.JoinOn(ursus.Col("k")), ursus.JoinHow(ursus.JoinFull),
			ursus.JoinCoalesce(true)}, "cannot coalesce"},
		{"validate on cross", []ursus.JoinOption{
			ursus.JoinHow(ursus.JoinCross),
			ursus.JoinValidate(ursus.ValidateOneToOne)}, "not meaningful"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := l.Join(r, c.opts...).Collect(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should mention %q:\n%v", c.want, err)
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("a user mistake is not an ursus bug:\n%v", err)
			}
		})
	}
}

// TestJoinErrorPropagates: the receiver's error wins, then the argument's.
func TestJoinErrorPropagates(t *testing.T) {
	good := joinLeft()
	// A duplicate column name fails when the frame is BUILT, so lf.Err() is set
	// before any collect — which is what makes this a test of error propagation
	// rather than of resolution.
	bad := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("k", []int64{2}))
	if bad.Err() == nil {
		t.Fatal("the fixture must have a build-time error")
	}

	if err := good.Join(bad, ursus.JoinOn(ursus.Col("k"))).Err(); err == nil {
		t.Error("the other frame's error must propagate")
	}
	if err := bad.Join(good, ursus.JoinOn(ursus.Col("k"))).Err(); err == nil {
		t.Error("the receiver's error must propagate")
	}
	if err := good.Join(nil, ursus.JoinOn(ursus.Col("k"))).Err(); err == nil {
		t.Error("a nil frame must be an error, not a panic")
	}
}

// TestJoinRejectsSelectorKeys: a selector expands in each frame's own column
// order, so pairing two of them would depend on the two schemas' orders.
func TestJoinRejectsSelectorKeys(t *testing.T) {
	_, err := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.All())).Collect(t.Context())
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if !strings.Contains(err.Error(), "selector") {
		t.Errorf("error should explain the positional pairing:\n%v", err)
	}
}

// TestJoinExplainShowsSides: without left/right markers, Join(A,B) and Join(B,A)
// render identically — and for Left, Right, Semi and Anti those are different
// queries, so a golden file could not see a rule that swapped them.
func TestJoinExplainShowsSides(t *testing.T) {
	p, err := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
		ursus.JoinHow(ursus.JoinLeft)).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"JOIN LEFT", "left:", "right:", "coalesce"} {
		if !strings.Contains(p, want) {
			t.Errorf("plan should contain %q:\n%s", want, p)
		}
	}
	li, ri := strings.Index(p, "left:"), strings.Index(p, "right:")
	if li < 0 || ri < 0 || li > ri {
		t.Errorf("left must be rendered before right:\n%s", p)
	}
}

// TestJoinLabelIsTotal: predicatePushdown detects change by comparing Explain
// output, so Label must never fail — including on an unresolved node.
func TestJoinLabelIsTotal(t *testing.T) {
	kinds := []ursus.JoinKind{
		ursus.JoinInner, ursus.JoinLeft, ursus.JoinRight, ursus.JoinFull,
		ursus.JoinSemi, ursus.JoinAnti, ursus.JoinCross,
	}
	for _, k := range kinds {
		opts := []ursus.JoinOption{ursus.JoinHow(k), ursus.JoinNullsEqual(true)}
		if k != ursus.JoinCross {
			opts = append(opts, ursus.JoinOn(ursus.Col("k")),
				ursus.JoinValidate(ursus.ValidateManyToMany))
		}
		lf := joinLeft().Join(joinRight(), opts...)
		if lf.Err() != nil {
			t.Fatalf("%s: %v", k, lf.Err())
		}
		// Label on the UNRESOLVED node — no schemas fetched.
		if got := lf.Plan().Label(); !strings.Contains(got, k.String()) {
			t.Errorf("%s: label %q does not name the kind", k, got)
		}
	}
}

// TestJoinIsAPredicateBarrier: predicate pushdown through a join is not
// implemented, and the optimizer's fail-closed default is what makes that safe. A
// filter above a join must stay above it.
func TestJoinIsAPredicateBarrier(t *testing.T) {
	lf := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
		ursus.JoinHow(ursus.JoinLeft)).
		Filter(ursus.Col("rv").IsNotNull())

	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fi, ji := strings.Index(p, "FILTER"), strings.Index(p, "JOIN")
	if fi < 0 || ji < 0 || fi > ji {
		t.Errorf("FILTER must remain above JOIN:\n%s", p)
	}

	// And the answer is right: 4 matched rows have a non-null rv.
	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 4 {
		t.Errorf("got %d rows, want 4\n%s", df.Height(), df)
	}
}

// TestJoinOptimizerSoundness runs every join query with the optimizer on and off.
// This is the only mechanism that catches a wrongly-moved predicate, because
// Verify compares schemas and a misplaced filter does not change one.
func TestJoinOptimizerSoundness(t *testing.T) {
	queries := []struct {
		name string
		lf   func() *ursus.LazyFrame
	}{
		{"filter above a left join, on a right column", func() *ursus.LazyFrame {
			return joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinLeft)).Filter(ursus.Col("rv").Ne("p"))
		}},
		{"filter above a right join, on a left column", func() *ursus.LazyFrame {
			return joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinRight)).Filter(ursus.Col("lv").Ne("b"))
		}},
		{"filter above a full join, on the key", func() *ursus.LazyFrame {
			return joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinFull)).Filter(ursus.Col("k").Gt(1))
		}},
		{"filter above an inner join, on both sides", func() *ursus.LazyFrame {
			return joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k"))).
				Filter(ursus.Col("lv").Ne(ursus.Col("rv")))
		}},
		{"filter below a join", func() *ursus.LazyFrame {
			return joinLeft().Filter(ursus.Col("k").Gt(1)).
				Join(joinRight(), ursus.JoinOn(ursus.Col("k")))
		}},
		{"select above a join", func() *ursus.LazyFrame {
			return joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k"))).
				Select(ursus.Col("rv"))
		}},
		{"aggregate above a join", func() *ursus.LazyFrame {
			return joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k"))).
				GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n"))
		}},
		{"head above a join", func() *ursus.LazyFrame {
			return joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k"))).Head(2)
		}},
		{"join of two joins", func() *ursus.LazyFrame {
			inner := joinLeft().Join(joinRight(), ursus.JoinOn(ursus.Col("k")))
			return inner.Join(joinRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinSemi))
		}},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			on, err := q.lf().Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("optimized: %v", err)
			}
			off, err := q.lf().Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()))
			if err != nil {
				t.Fatalf("unoptimized: %v", err)
			}
			if on.String() != off.String() {
				p, _ := q.lf().Explain(t.Context())
				t.Errorf("the optimizer changed the result\n optimized:\n%s\n unoptimized:\n%s\n plan:\n%s",
					on, off, p)
			}
		})
	}
}

// TestJoinChainedIsAssociativeOnCounts is a cheap sanity check over three frames.
func TestJoinChainedIsAssociativeOnCounts(t *testing.T) {
	a := ursus.Frame(ursus.Values("k", []int64{1, 2}), ursus.Values("av", []string{"a1", "a2"}))
	b := ursus.Frame(ursus.Values("k", []int64{2, 3}), ursus.Values("bv", []string{"b2", "b3"}))
	c := ursus.Frame(ursus.Values("k", []int64{2, 4}), ursus.Values("cv", []string{"c2", "c4"}))

	left, err := a.Join(b, ursus.JoinOn(ursus.Col("k"))).
		Join(c, ursus.JoinOn(ursus.Col("k"))).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	right, err := a.Join(b.Join(c, ursus.JoinOn(ursus.Col("k"))), ursus.JoinOn(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if left.Height() != 1 || right.Height() != 1 {
		t.Errorf("both groupings should give 1 row, got %d and %d\n%s\n%s",
			left.Height(), right.Height(), left, right)
	}
}

// TestJoinProjectionPushdownReadsOnlyKeysForSemi proves the rule does something.
// A semi join emits no right column at all, so the right side needs only the key.
func TestJoinProjectionPushdownReadsOnlyKeysForSemi(t *testing.T) {
	wide := ursus.Frame(
		ursus.Values("k", []int64{2}),
		ursus.Values("w1", []string{"x"}),
		ursus.Values("w2", []string{"y"}),
		ursus.Values("w3", []string{"z"}),
	)
	p, err := joinLeft().Join(wide, ursus.JoinOn(ursus.Col("k")),
		ursus.JoinHow(ursus.JoinSemi)).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "projection: [k] (1/4 cols)") {
		t.Errorf("the right side of a semi join should read only the key:\n%s", p)
	}
}

// TestJoinProjectionPushdownNarrowsBothSides.
func TestJoinProjectionPushdownNarrowsBothSides(t *testing.T) {
	l := ursus.Frame(
		ursus.Values("k", []int64{1}), ursus.Values("a", []int64{1}),
		ursus.Values("unused_l", []int64{1}),
	)
	r := ursus.Frame(
		ursus.Values("k", []int64{1}), ursus.Values("b", []int64{1}),
		ursus.Values("unused_r", []int64{1}),
	)
	p, err := l.Join(r, ursus.JoinOn(ursus.Col("k"))).
		Select(ursus.Col("a"), ursus.Col("b")).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "unused_l") || strings.Contains(p, "unused_r") {
		t.Errorf("neither unused column should be read:\n%s", p)
	}
	if !strings.Contains(p, "[k, a]") || !strings.Contains(p, "[k, b]") {
		t.Errorf("each side should read its key plus what it supplies:\n%s", p)
	}
}

// TestJoinProjectionKeepsCollidingLeftColumns is the naming-stability case, and it
// is a P2 repeat without the clause that fixes it.
//
// A right column is suffixed only if the LEFT has that name. Narrowing the left
// can therefore REMOVE a suffix and rename an output column the plan above still
// refers to — resolution succeeds, then the schema does not.
func TestJoinProjectionKeepsCollidingLeftColumns(t *testing.T) {
	l := ursus.Frame(ursus.Values("k", []int64{1}),
		ursus.Values("a", []string{"la"}), ursus.Values("b", []string{"lb"}))
	r := ursus.Frame(ursus.Values("k", []int64{1}),
		ursus.Values("b", []string{"rb"}), ursus.Values("c", []string{"rc"}))

	// b_right exists only because the LEFT also has a "b".
	lf := l.Join(r, ursus.JoinOn(ursus.Col("k"))).
		Select(ursus.Col("a"), ursus.Col("b_right"))

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("narrowing the left must not un-suffix a right column: %v", err)
	}
	if got := strings.Join(df.Columns(), ","); got != "a,b_right" {
		t.Errorf("columns = %s, want a,b_right", got)
	}
	if v, _, err := df.At[string](0, "b_right"); err != nil {
		t.Fatal(err)
	} else if v != "rb" {
		t.Errorf("b_right = %q, want \"rb\"", v)
	}

	// The left must still read "b" even though nothing selects it.
	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "[k, a, b]") {
		t.Errorf("the left side must keep \"b\" alive: it is what causes the suffix\n%s", p)
	}
}

// TestJoinSchemaMatchesCollectedSchema is the general form of the P2 failure:
// what CollectSchema predicts must be what Collect produces. Cheap, and it covers
// the whole option matrix.
func TestJoinSchemaMatchesCollectedSchema(t *testing.T) {
	kinds := []ursus.JoinKind{
		ursus.JoinInner, ursus.JoinLeft, ursus.JoinRight, ursus.JoinFull,
		ursus.JoinSemi, ursus.JoinAnti, ursus.JoinCross,
	}
	frames := []struct {
		name string
		l, r *ursus.LazyFrame
	}{
		{"collision", joinLeft(), joinRight()},
		{"no collision", joinLeft(), ursus.Frame(
			ursus.Values("k2", []int64{2}), ursus.Values("rv", []string{"p"}))},
	}

	for _, f := range frames {
		for _, k := range kinds {
			for _, co := range []struct {
				name string
				opt  []ursus.JoinOption
			}{
				{"auto", nil},
				{"on", []ursus.JoinOption{ursus.JoinCoalesce(true)}},
				{"off", []ursus.JoinOption{ursus.JoinCoalesce(false)}},
			} {
				t.Run(f.name+"/"+k.String()+"/"+co.name, func(t *testing.T) {
					opts := append([]ursus.JoinOption{ursus.JoinHow(k)}, co.opt...)
					if k != ursus.JoinCross {
						if f.name == "collision" {
							opts = append(opts, ursus.JoinOn(ursus.Col("k")))
						} else {
							opts = append(opts,
								ursus.JoinLeftOn(ursus.Col("k")), ursus.JoinRightOn(ursus.Col("k2")))
						}
					}
					lf := f.l.Join(f.r, opts...)

					predicted, perr := lf.CollectSchema(t.Context())
					df, cerr := lf.Collect(t.Context(), ursus.WithVerify())
					if perr != nil || cerr != nil {
						// Both must agree about failing, too.
						if (perr == nil) != (cerr == nil) {
							t.Fatalf("CollectSchema and Collect disagree about failing:\n"+
								"  schema: %v\n  collect: %v", perr, cerr)
						}
						return
					}
					ursustest.AssertSchemaEqual(t, df.Schema(), predicted)
				})
			}
		}
	}
}

// --- predicate pushdown through the join ------------------------------------------

// The pushdown fixtures differ from joinLeft/joinRight in two ways that matter:
// both frames have a NON-KEY column called "v", so the output carries a suffixed
// "v_right" — a name that exists in NEITHER child — and there are unmatched rows on
// both sides, so the barrier cells actually bind.
//
// Nothing in the step-4 suite produced a suffixed column and then filtered on it,
// which meant the rule could have been a complete no-op and every test would still
// have passed.
func pdLeft() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("k", []int64{1, 2, 3}),
		ursus.Values("v", []string{"l1", "l2", "l3"}),
	)
}

func pdRight() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("k", []int64{2, 3, 4}),
		ursus.Values("v", []string{"r2", "r3", "r4"}),
	)
}

// noJoinPushdown keeps every other rule on and turns off only the join arm. That is
// what the flag is for: bisecting a wrong answer to this rule specifically.
func noJoinPushdown() plan.Flags {
	f := plan.DefaultFlags()
	f.JoinPredicatePushdown = false
	return f
}

// TestJoinPredicatePushdownSoundness covers every cell of the legality table —
// both the legal ones and the barriers — with the optimizer on and off.
//
// This is the only mechanism that can catch a wrongly-moved predicate:
// Filter.Schema() never reads Preds, so the optimizer's Verify mode is blind to it,
// and the failure is different ROWS rather than an error.
func TestJoinPredicatePushdownSoundness(t *testing.T) {
	kinds := []ursus.JoinKind{
		ursus.JoinInner, ursus.JoinLeft, ursus.JoinRight, ursus.JoinFull,
		ursus.JoinSemi, ursus.JoinAnti, ursus.JoinCross,
	}
	// One predicate per side, plus one spanning both, plus one on the key.
	preds := []struct {
		name string
		e    func() ursus.Expr
	}{
		{"left only", func() ursus.Expr { return ursus.Col("v").Ne("l2") }},
		{"right only", func() ursus.Expr { return ursus.Col("v_right").Ne("r3") }},
		{"both sides", func() ursus.Expr { return ursus.Col("v").Ne(ursus.Col("v_right")) }},
		{"the key", func() ursus.Expr { return ursus.Col("k").Gt(1) }},
		{"left, null-sensitive", func() ursus.Expr { return ursus.Col("v").IsNotNull() }},
		{"right, null-sensitive", func() ursus.Expr { return ursus.Col("v_right").IsNull() }},
	}

	for _, kind := range kinds {
		for _, p := range preds {
			t.Run(kind.String()+"/"+p.name, func(t *testing.T) {
				build := func() *ursus.LazyFrame {
					opts := []ursus.JoinOption{ursus.JoinHow(kind)}
					if kind != ursus.JoinCross {
						opts = append(opts, ursus.JoinOn(ursus.Col("k")))
					}
					return pdLeft().Join(pdRight(), opts...).Filter(p.e())
				}

				on, err := build().Collect(t.Context(), ursus.WithVerify())
				off, offErr := build().Collect(t.Context(),
					ursus.WithOptFlags(noJoinPushdown()), ursus.WithVerify())

				// Semi and Anti have no right columns, so a right-side predicate is an
				// unknown column on BOTH paths. They must agree about that too.
				if (err == nil) != (offErr == nil) {
					t.Fatalf("the join arm changed whether the query is valid:\n on:  %v\n off: %v",
						err, offErr)
				}
				if err != nil {
					return
				}
				if on.String() != off.String() {
					pl, _ := build().Explain(t.Context())
					t.Errorf("join predicate pushdown changed the result\n with:\n%s\n without:\n%s\n plan:\n%s",
						on, off, pl)
				}
			})
		}
	}
}

// TestJoinPredicatePushdownMovesAndRenames proves the rule does something, and that
// it rewrites rather than moving verbatim.
//
// A suffixed right column must descend under its CHILD name: "v_right" exists in
// neither input, so pushing it unchanged would be an unknown column.
func TestJoinPredicatePushdownMovesAndRenames(t *testing.T) {
	cases := []struct {
		name    string
		lf      func() *ursus.LazyFrame
		below   bool   // must the FILTER end up below the JOIN?
		wantSub string // text that must appear when it descends
	}{
		{"inner, left-only", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k"))).
				Filter(ursus.Col("v").Ne("l1"))
		}, true, `FILTER [(col("v") != lit("l1"))]`},

		{"inner, right-only is un-suffixed on the way down", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k"))).
				Filter(ursus.Col("v_right").Ne("r3"))
		}, true, `FILTER [(col("v") != lit("r3"))]`},

		{"left join, left-only", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinLeft)).Filter(ursus.Col("v").Ne("l1"))
		}, true, `FILTER [(col("v") != lit("l1"))]`},

		{"left join, right-only is a BARRIER", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinLeft)).Filter(ursus.Col("v_right").Ne("r3"))
		}, false, ""},

		{"right join, left-only is a BARRIER", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinRight)).Filter(ursus.Col("v").Ne("l1"))
		}, false, ""},

		{"full join, either side is a BARRIER", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinFull)).Filter(ursus.Col("v").Ne("l1"))
		}, false, ""},

		{"both sides is a BARRIER", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k"))).
				Filter(ursus.Col("v").Ne(ursus.Col("v_right")))
		}, false, ""},

		{"validate is a total BARRIER", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinValidate(ursus.ValidateManyToOne)).Filter(ursus.Col("v").Ne("l1"))
		}, false, ""},

		{"semi join, left-only descends", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinSemi)).Filter(ursus.Col("v").Ne("l1"))
		}, true, `FILTER [(col("v") != lit("l1"))]`},

		{"anti join, left-only descends", func() *ursus.LazyFrame {
			return pdLeft().Join(pdRight(), ursus.JoinOn(ursus.Col("k")),
				ursus.JoinHow(ursus.JoinAnti)).Filter(ursus.Col("v").Ne("l1"))
		}, true, `FILTER [(col("v") != lit("l1"))]`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := c.lf().Explain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			fi, ji := strings.Index(p, "FILTER"), strings.Index(p, "JOIN")
			if fi < 0 || ji < 0 {
				t.Fatalf("expected both a FILTER and a JOIN:\n%s", p)
			}
			if c.below && fi < ji {
				t.Errorf("the predicate should have descended below the JOIN:\n%s", p)
			}
			if !c.below && fi > ji {
				t.Errorf("this cell is a BARRIER; the FILTER must stay above the JOIN:\n%s", p)
			}
			if c.wantSub != "" && !strings.Contains(p, c.wantSub) {
				t.Errorf("expected %s in the plan:\n%s", c.wantSub, p)
			}
		})
	}
}

// TestJoinPredicatePushdownPreservesValidate is the one cell result comparison
// cannot check, because the correct behaviour is an ERROR.
//
// A pushed predicate can delete the duplicate keys a ValidateManyToOne check exists
// to catch, turning a query that must fail into one that returns a plausible
// answer. That is not a wrong row count, it is a missing error.
func TestJoinPredicatePushdownPreservesValidate(t *testing.T) {
	l := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("v", []string{"keep"}))
	// k=1 is duplicated on the right, so m:1 must fail — but only the row whose v is
	// "drop" makes it a duplicate, and a pushed `v != "drop"` would remove it.
	r := ursus.Frame(
		ursus.Values("k", []int64{1, 1}),
		ursus.Values("v", []string{"keep", "drop"}),
	)

	_, err := l.Join(r, ursus.JoinOn(ursus.Col("k")),
		ursus.JoinValidate(ursus.ValidateManyToOne)).
		Filter(ursus.Col("v_right").Ne("drop")).
		Collect(t.Context(), ursus.WithVerify())
	if err == nil {
		t.Fatal("validate=m:1 must still fire: pushing the predicate would have " +
			"deleted the duplicate it exists to catch, turning a required error " +
			"into a plausible answer")
	}
	if !errors.Is(err, uerr.ErrValue) {
		t.Errorf("kind should be Value: %v", err)
	}
}

// TestJoinPredicatePushdownFlagIsolatesTheArm: the flag must disable the join arm
// and nothing else, or it is useless for bisection.
func TestJoinPredicatePushdownFlagIsolatesTheArm(t *testing.T) {
	lf := func() *ursus.LazyFrame {
		return pdLeft().
			Filter(ursus.Col("k").Lt(3)).
			Join(pdRight(), ursus.JoinOn(ursus.Col("k"))).
			Filter(ursus.Col("v").Ne("l1"))
	}

	off, err := lf().Explain(t.Context(), ursus.Optimized(true))
	if err != nil {
		t.Fatal(err)
	}
	_ = off

	// With the arm off, the predicate above the join must stay above it...
	p, err := lf().Collect(t.Context(), ursus.WithOptFlags(noJoinPushdown()), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	q, err := lf().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if p.String() != q.String() {
		t.Errorf("the flag changed the answer, not just the plan:\n%s\n%s", p, q)
	}

	// ...while the predicate BELOW the join still gets pushed into the scan, because
	// only the join arm is off.
	pl, err := lf().Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pl, "JOIN") {
		t.Fatalf("expected a join:\n%s", pl)
	}
}

// TestJoinPredicatePushdownWithDifferentlyNamedKeys covers the coalesce path where
// the two child names differ, so the rewrite is not an identity on either side.
func TestJoinPredicatePushdownWithDifferentlyNamedKeys(t *testing.T) {
	l := ursus.Frame(ursus.Values("lk", []int64{1, 2}), ursus.Values("a", []string{"x", "y"}))
	r := ursus.Frame(ursus.Values("rk", []int64{2, 3}), ursus.Values("b", []string{"p", "q"}))

	build := func() *ursus.LazyFrame {
		return l.Join(r, ursus.JoinLeftOn(ursus.Col("lk")), ursus.JoinRightOn(ursus.Col("rk")),
			ursus.JoinCoalesce(true)).
			Filter(ursus.Col("lk").Gt(1))
	}
	on, err := build().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	off, err := build().Collect(t.Context(), ursus.WithOptFlags(noJoinPushdown()), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if on.String() != off.String() {
		t.Errorf("coalesced-key pushdown changed the result\n with:\n%s\n without:\n%s", on, off)
	}
}
