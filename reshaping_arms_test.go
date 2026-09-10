package ursus_test

// The two arms Explode, Unnest, RowIndex, HStack and Unpivot were missing: a
// resolve arm, so a typo is a typo rather than an optimizer failure, and a
// projection-pushdown arm, so they stop reading every column of their input.

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// assertUserError is the assertion shape the suite was missing.
//
// Four checked-in tests already asserted `errors.Is(err, uerr.ErrSchema)` and a
// substring, and all four passed against the BROKEN message — because
// Optimizer.Run wraps a rule failure as KindInternal, `(*uerr.Error).Is` compares
// only its own kind, and `errors.Is` then walks the cause chain and finds the good
// kind underneath. The substring survived for the same reason: it was on the
// `caused by:` line.
//
// So the only thing that can tell the two apart is the TOP-LEVEL kind, plus the
// fact that a refusal reaching resolve makes Explain fail too — the discipline
// Expr.DropNulls states: "A refusal that only Collect notices ... lets Explain
// print a plan that cannot run."
func assertUserError(t *testing.T, lf *ursus.LazyFrame, want string) {
	t.Helper()

	if _, err := lf.Explain(t.Context()); err == nil {
		t.Error("Explain printed a plan that cannot run")
	}

	_, err := lf.Collect(t.Context())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the error should mention %q: %v", want, err)
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Errorf("a user mistake reported as an ursus bug: %v", err)
	}
	if strings.Contains(err.Error(), "rule \"") {
		t.Errorf("a user mistake reported as an optimizer-rule failure: %v", err)
	}
}

// TestReshapingRefusalsAreUserErrors covers every node whose validation lives in
// Schema() and which had no resolve arm before step 48.
//
// Explode and Unnest were named in step 47; RowIndex and HStack were found by
// reading every armless node's Schema() for one that can refuse.
func TestReshapingRefusalsAreUserErrors(t *testing.T) {
	base := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("id", []int64{1, 2}),
			ursus.Values("v", []string{"a,b", "c"}),
		)
	}

	t.Run("explode an unknown column", func(t *testing.T) {
		assertUserError(t, base().Explode("nope"), "nope")
	})
	t.Run("explode a column that is not a List", func(t *testing.T) {
		assertUserError(t, base().Explode("id"), "not a List")
	})
	t.Run("unnest an unknown column", func(t *testing.T) {
		assertUserError(t, base().Unnest("nope"), "nope")
	})
	t.Run("unnest a column that is not a Struct", func(t *testing.T) {
		assertUserError(t, base().Unnest("id"), "not a Struct")
	})
	t.Run("row index onto an existing name", func(t *testing.T) {
		assertUserError(t, base().WithRowIndex("id", 0), "already has a column")
	})
	t.Run("hstack with a colliding name", func(t *testing.T) {
		assertUserError(t, base().HStack(base()), "appears in both")
	})
	t.Run("unpivot an unknown column", func(t *testing.T) {
		assertUserError(t,
			base().Unpivot(ursus.UnpivotOptions{On: []string{"nope"}}), "nope")
	})

	// The defect was INTERMITTENT, which is why it survived: every arm-bearing node
	// returns its input's Schema() error unwrapped, so anything with an arm above
	// the broken node masked it. `.Select(...)` was always clean; a bare node, or
	// one under a Limit, was not.
	t.Run("masked by an arm-bearing ancestor", func(t *testing.T) {
		assertUserError(t, base().Explode("nope").Select(ursus.Col("id")), "nope")
	})
	t.Run("not masked under a Limit", func(t *testing.T) {
		assertUserError(t, base().Explode("nope").Head(1), "nope")
	})
}

// noProjectionPushdown is DefaultFlags with the one rule off, so every pruning test
// below is a differential: the answer must not depend on it.
func noProjectionPushdown() plan.Flags {
	f := plan.DefaultFlags()
	f.ProjectionPushdown = false
	return f
}

// scanReads reports the columns the plan's scan actually decodes.
func scanReads(t *testing.T, lf *ursus.LazyFrame) string {
	t.Helper()
	s, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(s, "\n") {
		if strings.Contains(line, "projection:") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no projection line in:\n%s", s)
	return ""
}

// TestExplodePrunesItsInput. Before step 48 an Explode fell to the rule's fail-safe
// default — "an unrecognised node type requires ALL of its input's columns" — so a
// Select above it was annihilated, which is the NORMAL shape for explode: one list
// column of a wide frame, then a few columns out.
func TestExplodePrunesItsInput(t *testing.T) {
	lf := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("id", []int64{1, 2}),
			ursus.Values("unused", []string{"x", "y"}),
			ursus.Values("tags", []string{"a,b", "c"}),
		).
			WithColumns(ursus.Col("tags").Str().Split(",").Alias("tag")).
			Explode("tag").
			Select(ursus.Col("id"), ursus.Col("tag"))
	}

	if got := scanReads(t, lf()); strings.Contains(got, "unused") {
		t.Errorf("nothing reads `unused`: %s", got)
	}

	// The case that matters: the exploded column is NOT selected, and it comes
	// straight from the scan. It still drives the row count, so a `need` set that
	// omits it prunes it at the source.
	//
	// Both halves are needed. Without "not selected", `required` already contains
	// it. Without "straight from the scan", a WithColumns in between recomputes it
	// anyway — that arm prunes pass-through columns but never its own expressions —
	// so the column survives however wrong the Explode arm is.
	path := writeListFile(t,
		[]int64{1, 2}, [][]int64{{10, 11}, {12}}, []bool{true, true})
	unnamed := func() *ursus.LazyFrame {
		return ursus.ScanParquet(path).Explode("tags").Select(ursus.Col("id"))
	}
	heights, err := unnamed().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if heights.Height() != 3 {
		t.Errorf("got %d rows, want 3 — the exploded column sets the height even "+
			"when nothing selects it\n%s", heights.Height(), heights)
	}

	// The answer must not depend on the rule, which is the failure mode that
	// matters: a pushdown that errors is loud, one that changes rows is not.
	on, err := lf().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	off, err := lf().Collect(t.Context(), ursus.WithOptFlags(noProjectionPushdown()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, on, off, ursustest.CheckNullability())
}

// TestUnnestPrunesItsInput is the interesting one: the parent asks for FIELD names,
// which exist in no input schema, so the required set has to be filtered against the
// input and the struct added back. Passing the field names straight down would ask
// the scan for columns it never had.
func TestUnnestPrunesItsInput(t *testing.T) {
	path := writeStructFile(t, []person{{age(30), city("eu"), true}, {age(40), city("us"), true}}, []int64{1, 2})

	lf := func() *ursus.LazyFrame {
		return ursus.ScanParquet(path).Unnest("person").Select(ursus.Col("age"))
	}
	if got := scanReads(t, lf()); strings.Contains(got, "id") {
		t.Errorf("only `person` is read, and only its age field is selected: %s", got)
	}

	on, err := lf().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	off, err := lf().Collect(t.Context(), ursus.WithOptFlags(noProjectionPushdown()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, on, off, ursustest.CheckNullability())
}

// TestUnpivotExplicitIndexSurvivesPruning is where the join's own arm shape would be
// WRONG.
//
// pushdownJoin iterates the layout and keeps what the parent asked for. Doing that
// here would prune an index column nobody selected — and IndexColumns returns the
// named list verbatim, so Schema() would then fail with `unknown index column`.
// Resolution succeeds and then the schema does not, which is defect P2's signature.
// An explicit Index is required in full: the node names those columns, so it reads
// them.
func TestUnpivotExplicitIndexSurvivesPruning(t *testing.T) {
	lf := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("id", []int64{1, 2}),
			ursus.Values("unused", []string{"x", "y"}),
			ursus.Values("jan", []int64{10, 30}),
			ursus.Values("feb", []int64{20, 40}),
		).
			Unpivot(ursus.UnpivotOptions{
				On:    []string{"jan", "feb"},
				Index: []string{"id"},
			}).
			// Nothing selects `id`, and it must survive anyway.
			Select(ursus.Col("variable"), ursus.Col("value"))
	}

	df, err := lf().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("the named index column must stay alive: %v", err)
	}
	if df.Height() != 4 {
		t.Fatalf("got %d rows, want 4\n%s", df.Height(), df)
	}
	got := scanReads(t, lf())
	if strings.Contains(got, "unused") {
		t.Errorf("nothing reads `unused`: %s", got)
	}
	if !strings.Contains(got, "id") {
		t.Errorf("`id` is a named index column and must be read: %s", got)
	}
}

// TestUnpivotImplicitIndexAtTheRoot is the case the monotonicity argument rests on.
//
// With no Index the index columns are computed from the input schema, so narrowing
// the input narrows the output. That is safe only because Apply seeds the root's
// required set with "everything it currently produces" — so when the Unpivot IS the
// root, nothing is dropped.
func TestUnpivotImplicitIndexAtTheRoot(t *testing.T) {
	lf := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("id", []int64{1, 2}),
			ursus.Values("region", []string{"eu", "us"}),
			ursus.Values("jan", []int64{10, 30}),
		).Unpivot(ursus.UnpivotOptions{On: []string{"jan"}})
	}

	on, err := lf().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(on.Schema().Names(), ","); got != "id,region,variable,value" {
		t.Errorf("as the root, every index column survives: got %s", got)
	}
	off, err := lf().Collect(t.Context(), ursus.WithOptFlags(noProjectionPushdown()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, on, off, ursustest.CheckNullability())

	// And with a Select above, the unreferenced index columns SHOULD go — that is
	// the optimisation, and it is invisible because nobody named them.
	narrowed := lf().Select(ursus.Col("value"))
	if got := scanReads(t, narrowed); strings.Contains(got, "region") {
		t.Errorf("nothing reads `region`: %s", got)
	}
}
