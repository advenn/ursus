package kernel_test

// dt.truncate now has TWO rules, and that is the hazard this file exists for.
//
// Step 56 moved truncate's refusals to plan time, because the interval is a constant
// of the expression and a query that Explain renders cleanly must not fail at
// Collect. But the kernel's checks are still there — kernel.DtCall is exported and
// reachable without going through ResolveCall — so the same question is now answered
// in two places.
//
// "Two copies would drift, which is the Binding lesson" is ResolveCall's own comment
// about why it is the single authority for output types. A second copy of a REFUSAL
// has exactly the same problem, and it is worse here because the kernel's copy is now
// unreachable from the expression path: it could drift for a whole release and no
// existing test would notice, since nothing reaches it.
//
// So this pins them together over the cross product rather than trusting them.

import (
	"errors"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

func TestTruncateRefusalsAgree(t *testing.T) {
	const n = 3

	receivers := []struct {
		label string
		dt    dtype.DataType
		col   func(dtype.DataType) *data.Column
	}{
		{"Date", dtype.Date, func(d dtype.DataType) *data.Column {
			return data.NewFixed("c", d, []int32{1, 2, 3}, bitmap.AllSet(n))
		}},
		{"Datetime(us,UTC)", dtype.Datetime(dtype.Micro, "UTC"), tickCol},
		{"Datetime(us,naive)", dtype.Datetime(dtype.Micro, ""), tickCol},
		{"Time(ns)", dtype.Time(dtype.Nano), tickCol},
	}

	intervals := []struct {
		label string
		args  []any
	}{
		{"1h", []any{int64(0), int64(0), int64(time.Hour)}},
		{"1ns", []any{int64(0), int64(0), int64(1)}},
		{"1mo", []any{int64(1), int64(0), int64(0)}},
		{"1d", []any{int64(0), int64(1), int64(0)}},
		{"1mo1d", []any{int64(1), int64(1), int64(0)}},
		{"zero", []any{int64(0), int64(0), int64(0)}},
		{"-1h", []any{int64(0), int64(0), int64(-time.Hour)}},
		{"missing", nil},
	}

	var refusals, accepts int
	for _, r := range receivers {
		for _, iv := range intervals {
			label := "truncate(" + r.label + ", " + iv.label + ")"

			args := []expr.Node{&expr.Col{Name: "c"}}
			for _, a := range iv.args {
				args = append(args, &expr.Lit{Value: a, DT: dtype.Int64})
			}
			out, rerr := expr.ResolveCall(&expr.Call{Fn: expr.FnDtTruncate, Args: args}, r.dt)

			// The kernel is handed the type the OLD resolver would have promised, so
			// a refusal that moved to plan time is still visible here rather than
			// disappearing behind a resolve error.
			_, kerr := kernel.DtCall(expr.FnDtTruncate, "c", r.dt, r.col(r.dt), iv.args)

			switch {
			case rerr != nil && kerr != nil:
				refusals++
				// Same kind, not merely both non-nil. errors.Is walks the cause
				// chain and would call a KindValue and a KindType the same thing.
				var re, ke *uerr.Error
				if errors.As(rerr, &re) && errors.As(kerr, &ke) && re.Kind != ke.Kind {
					t.Errorf("%s: plan time says %v, the kernel says %v — the same "+
						"refusal reported as two different kinds", label, re.Kind, ke.Kind)
				}
			case rerr == nil && kerr == nil:
				accepts++
				if out != r.dt {
					t.Errorf("%s: resolved to %s, want the receiver's own type %s",
						label, out, r.dt)
				}
			case rerr != nil:
				t.Errorf("%s: refused at plan time (%v) but the kernel runs it — the "+
					"planner rejects a query the engine can answer", label, rerr)
			default:
				t.Errorf("%s: resolved to %s and the kernel refused: %v — Explain "+
					"would print a plan that cannot run", label, out, kerr)
			}
		}
	}

	// Anti-vacuity on BOTH counters. All-refuse and all-accept each satisfy a test
	// that only asserts agreement, and each would be a different disaster.
	if refusals < 10 {
		t.Errorf("only %d refusals across %d combinations; the pair is agreeing by "+
			"accepting everything", refusals, len(receivers)*len(intervals))
	}
	if accepts < 10 {
		t.Errorf("only %d accepts; the pair is agreeing by refusing everything", accepts)
	}
}

func tickCol(d dtype.DataType) *data.Column {
	return data.NewFixed("c", d, []int64{1, 2, 3}, bitmap.AllSet(3))
}
