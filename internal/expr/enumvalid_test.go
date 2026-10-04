package expr_test

// RankMethod, Interpolation and Closed are public integer types, so RankMethod(99),
// Interpolation(99) and Closed(99) compile. Resolution refuses them, on every route a plan can
// take — including a plan built by hand and handed to FromPlan, which no public
// constructor checks.

import (
	"errors"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

func TestEveryDeclaredMethodIsValid(t *testing.T) {
	for m := expr.RankOrdinal; m.String() != "?"; m++ {
		if !m.Valid() {
			t.Errorf("RankMethod %s is declared and not Valid", m)
		}
	}
	for i := expr.InterpLinear; i.String() != "?"; i++ {
		if !i.Valid() {
			t.Errorf("Interpolation %s is declared and not Valid", i)
		}
	}
	for c := expr.ClosedDefault; c.String() != "?"; c++ {
		if !c.Valid() {
			t.Errorf("Closed %s is declared and not Valid", c)
		}
	}
	if expr.RankMethod(99).Valid() || expr.Interpolation(99).Valid() || expr.Closed(99).Valid() {
		t.Error("99 is Valid")
	}
}

func TestAnUndeclaredMethodIsRefused(t *testing.T) {
	in := dtype.MustSchema(dtype.Of("v", dtype.Int64), dtype.Of("g", dtype.Int64))
	v, g := &expr.Col{Name: "v"}, &expr.Col{Name: "g"}
	quantile := &expr.Agg{Op: expr.AggQuantile, Child: v,
		Params: expr.AggParams{Q: 0.5, Interp: expr.Interpolation(99)}}
	rank := &expr.WinFn{Fn: expr.WinRank, Child: v, Params: expr.WinParams{Method: expr.RankMethod(99)}}

	for _, tc := range []struct {
		name string
		n    expr.Node
	}{
		{"quantile", quantile},
		{"quantile over a window", &expr.Window{Child: quantile, PartitionBy: []expr.Node{g}}},
		{"rank", rank},
		{"rank over a window", &expr.Window{Child: rank, PartitionBy: []expr.Node{g}}},
	} {
		if _, err := tc.n.Field(in); !errors.Is(err, uerr.ErrValue) {
			t.Errorf("%s: want a value error, got %v", tc.name, err)
		}
	}
}
