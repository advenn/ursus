package uerr_test

// internal/uerr had no test file: 355 lines, imported by 129 files, the
// most-depended-on package in the repository and the only one with none.
//
// That matters more here than almost anywhere, because this package decides what
// OTHER tests can see. Its Is/Unwrap interaction is what let four checked-in tests
// pass against a broken error message for eighteen steps, until step 48 worked out
// why: errors.Is walks the cause chain, so a KindInternal wrapper around a
// KindSchema cause matches BOTH. Nothing pinned that; these do.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
)

// TestIsMatchesTheKindAnywhereInTheChain is the step-48 mechanism, written down.
//
// This is not a bug — it is what errors.Is means — but it is the reason
// `errors.Is(err, uerr.ErrSchema)` does NOT assert "this is a schema error", and
// four tests were written as though it did. The assertion that distinguishes the
// two is the TOP-LEVEL kind, which is what assertUserError checks.
func TestIsMatchesTheKindAnywhereInTheChain(t *testing.T) {
	inner := uerr.New(uerr.KindSchema, "select", "unknown column %q", "nope")
	wrapped := uerr.Wrap(inner, uerr.KindInternal, "optimize", `rule %q failed`, "pushdown")

	if !errors.Is(wrapped, uerr.ErrInternal) {
		t.Error("the wrapper's own kind must match")
	}
	if !errors.Is(wrapped, uerr.ErrSchema) {
		t.Error("errors.Is walks Unwrap, so the CAUSE's kind matches too — this is " +
			"the property that made four tests pass against a broken message")
	}
	// And the distinguishing assertion, which is the one to write in a test:
	var e *uerr.Error
	if !errors.As(wrapped, &e) || e.Kind != uerr.KindInternal {
		t.Error("errors.As finds the OUTERMOST *Error, which is how to ask what " +
			"this error actually is")
	}
}

// TestIsDoesNotMatchAnUnrelatedKind guards the other direction, so the test above
// cannot be satisfied by an Is that returns true for everything.
func TestIsDoesNotMatchAnUnrelatedKind(t *testing.T) {
	e := uerr.New(uerr.KindType, "cast", "nope")
	for _, k := range []error{uerr.ErrSchema, uerr.ErrValue, uerr.ErrIO, uerr.ErrResource} {
		if errors.Is(e, k) {
			t.Errorf("a KindType error matched %v", k)
		}
	}
	if !errors.Is(e, uerr.ErrType) {
		t.Error("it must match its own kind")
	}
}

// TestKindStringNamesEveryKind. The fallback used to be `default: "internal"`,
// which is the hazard ConcatMode.String documents for eight enums — and worse here,
// because KindInternal is the zero value, so the default arm was legitimately
// reached and the omission read as deliberate.
//
// An unnamed kind rendering as "internal" tells a user their resource limit is a bug
// in ursus.
func TestKindStringNamesEveryKind(t *testing.T) {
	for _, c := range []struct {
		k    uerr.Kind
		want string
	}{
		{uerr.KindInternal, "internal"},
		{uerr.KindSchema, "schema"},
		{uerr.KindType, "type"},
		{uerr.KindValue, "value"},
		{uerr.KindUnsupported, "unsupported"},
		{uerr.KindIO, "io"},
		{uerr.KindResource, "resource"},
	} {
		if got := c.k.String(); got != c.want {
			t.Errorf("Kind(%d) = %q, want %q", c.k, got, c.want)
		}
	}

	// An undeclared kind must NOT render as a plausible label.
	if got := uerr.Kind(99).String(); got == "internal" {
		t.Error("an undeclared kind renders as \"internal\", which claims it is a " +
			"bug in ursus")
	} else if !strings.Contains(got, "99") {
		t.Errorf("an undeclared kind should report its number, got %q", got)
	}
}

// TestWithOpAndWithNodeAreInnermostWins. Both set only when empty, which is what
// lets an error accumulate attribution as it propagates outward without the
// outermost frame overwriting the most specific one.
func TestWithOpAndWithNodeAreInnermostWins(t *testing.T) {
	e := uerr.New(uerr.KindValue, "round", "bad")
	e.WithOp("select").WithNode("Project")
	if e.Op != "round" {
		t.Errorf("Op = %q, want the innermost %q", e.Op, "round")
	}
	if e.Node != "Project" {
		t.Errorf("Node = %q, want it set when empty", e.Node)
	}
	e.WithNode("Filter")
	if e.Node != "Project" {
		t.Errorf("Node = %q; a later frame must not overwrite it", e.Node)
	}
}

// TestAnnotateMutatesItsArgument pins a property that is surprising and load-bearing.
//
// Annotate does errors.As then WithOp/WithNode, so it WRITES THROUGH THE POINTER and
// returns the original. Any caller holding that error sees the change.
//
// It is safe today only because lazy.go's nodes() checks Expr.Err() and returns
// before a parked *expr.Err can reach plan.Resolve — a guard nobody had written
// down. This test exists so that the day Annotate stops mutating, or a caller starts
// sharing an error across queries, the change is visible here rather than as a
// mysterious node attribution in someone's error message.
func TestAnnotateMutatesItsArgument(t *testing.T) {
	e := uerr.New(uerr.KindValue, "", "bad")
	got := uerr.Annotate(e, "select", "Project")

	if got != error(e) {
		t.Error("Annotate returns the original error, not a copy")
	}
	if e.Op != "select" || e.Node != "Project" {
		t.Errorf("Annotate did not write through the pointer: op=%q node=%q",
			e.Op, e.Node)
	}
}

// TestAnnotateFindsTheOutermostError. errors.As stops at the first match, so
// annotating a wrapper leaves the cause alone. Worth pinning because the rendering
// prints both.
func TestAnnotateFindsTheOutermostError(t *testing.T) {
	inner := uerr.New(uerr.KindSchema, "scan", "unknown column")
	outer := uerr.Wrap(inner, uerr.KindInternal, "", "optimize failed")

	uerr.Annotate(outer, "optimize", "Project")
	if outer.Op != "optimize" {
		t.Errorf("the outer error should be annotated, got op=%q", outer.Op)
	}
	if inner.Op != "scan" {
		t.Errorf("the cause must keep its own attribution, got op=%q", inner.Op)
	}
}

// TestAnnotateIgnoresAForeignError: a non-*Error passes through untouched.
func TestAnnotateIgnoresAForeignError(t *testing.T) {
	plain := errors.New("not ours")
	if got := uerr.Annotate(plain, "select", "Project"); got != plain {
		t.Error("a foreign error must pass through unchanged")
	}
}

// TestErrorRendersEveryPart, in the order the whole suite's substring assertions
// depend on: message, then hints, then the node, then the cause.
func TestErrorRendersEveryPart(t *testing.T) {
	inner := uerr.New(uerr.KindSchema, "scan", "unknown column %q", "nope")
	e := uerr.Wrap(inner, uerr.KindType, "select", "cannot resolve")
	e.Hint("first hint").Hint("second hint")
	e.WithNode("Project")

	got := e.Error()
	for _, want := range []string{
		"ursus: ", "select", "cannot resolve",
		"first hint", "second hint", "at Project", "caused by:", "nope",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendering omits %q:\n%s", want, got)
		}
	}
	// Order, because hints append and the node line trails them.
	if i, j := strings.Index(got, "first hint"), strings.Index(got, "second hint"); i > j {
		t.Error("hints must render in the order they were added")
	}
	if i, j := strings.Index(got, "second hint"), strings.Index(got, "at Project"); i > j {
		t.Error("the node line trails the hints")
	}
	if i, j := strings.Index(got, "at Project"), strings.Index(got, "caused by:"); i > j {
		t.Error("the cause renders last")
	}
}

// TestHintAppends, because Error() renders them in order and repeated annotation
// elsewhere in the engine relies on them not being replaced.
func TestHintAppends(t *testing.T) {
	e := uerr.New(uerr.KindValue, "op", "msg")
	e.Hint("a").Hint("b")
	if n := strings.Count(e.Error(), "\n  "); n < 2 {
		t.Errorf("both hints should render, got:\n%s", e.Error())
	}
}

// TestInternalfIsAlwaysKindInternal, since the whole suite asserts
// !errors.Is(err, uerr.ErrInternal) to mean "a user mistake, not an ursus bug".
func TestInternalfIsAlwaysKindInternal(t *testing.T) {
	e := uerr.Internalf("physical: no evaluator for %T", 1)
	if !errors.Is(e, uerr.ErrInternal) {
		t.Error("Internalf must produce KindInternal")
	}
	if !strings.Contains(e.Error(), "bug") {
		t.Errorf("an internal error should tell the user it is a bug:\n%v", e)
	}
}

// TestZeroValueErrorClaimsToBeAnUrsusBug is a hazard worth knowing about rather
// than a defect: KindInternal is the zero value, so a struct literal that forgets
// Kind silently claims "this is a bug in ursus".
func TestZeroValueErrorClaimsToBeAnUrsusBug(t *testing.T) {
	var e uerr.Error
	if !errors.Is(&e, uerr.ErrInternal) {
		t.Skip("KindInternal is no longer the zero value; this note is stale")
	}
	if got := e.Kind.String(); got != "internal" {
		t.Errorf("the zero Kind renders as %q", got)
	}
}

// TestUnknownColumnSuggests is the single most common error in a dataframe
// library, and the reason NearMatches exists.
func TestUnknownColumnSuggests(t *testing.T) {
	e := uerr.UnknownColumn("select", "pirce", []string{"price", "qty", "region"})
	got := e.Error()
	if !strings.Contains(got, "did you mean") || !strings.Contains(got, "price") {
		t.Errorf("a one-transposition typo should be suggested:\n%s", got)
	}
	if !strings.Contains(got, "available") {
		t.Errorf("the full schema should be listed:\n%s", got)
	}
	if !errors.Is(e, uerr.ErrSchema) {
		t.Error("an unknown column is a schema error")
	}
}

// TestUnknownColumnWithNoNearMatch omits the suggestion line rather than offering
// something unhelpful.
func TestUnknownColumnWithNoNearMatch(t *testing.T) {
	e := uerr.UnknownColumn("select", "zzzzzzzz", []string{"a", "b"})
	if strings.Contains(e.Error(), "did you mean") {
		t.Errorf("nothing is close; there should be no suggestion:\n%v", e)
	}
}

// TestWrapPreservesTheCauseForErrorsIs, which the whole engine relies on when it
// wraps an io error or a spill failure.
func TestWrapPreservesTheCauseForErrorsIs(t *testing.T) {
	sentinel := errors.New("disk on fire")
	e := uerr.Wrap(sentinel, uerr.KindIO, "scan_csv", "reading %s", "f.csv")
	if !errors.Is(e, sentinel) {
		t.Error("the cause must remain reachable through errors.Is")
	}
	if !errors.Is(e, uerr.ErrIO) {
		t.Error("and the wrapper's kind must match")
	}
	if !strings.Contains(fmt.Sprint(e), "f.csv") {
		t.Error("the formatted message should carry its arguments")
	}
}
