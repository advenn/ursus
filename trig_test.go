package ursus_test

// The trigonometric and hyperbolic functions (step 114), `v0.4-scope.md` item 21.

import (
	"math"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

func TestTrigonometry(t *testing.T) {
	c := ursus.Col
	in := []float64{-2, -1, -0.5, 0, 0.25, 0.5, 1, 2, math.Pi / 3}
	valid := []bool{true, true, true, true, true, true, true, true, true}
	frame := ursus.Frame(ursus.ValuesNullable("x", append(in, 0), append(valid, false)))

	fns := []struct {
		name string
		expr func(ursus.Expr) ursus.Expr
		want func(float64) float64
	}{
		{"sin", ursus.Expr.Sin, math.Sin}, {"cos", ursus.Expr.Cos, math.Cos}, {"tan", ursus.Expr.Tan, math.Tan},
		{"arcsin", ursus.Expr.ArcSin, math.Asin}, {"arccos", ursus.Expr.ArcCos, math.Acos},
		{"arctan", ursus.Expr.ArcTan, math.Atan},
		{"sinh", ursus.Expr.Sinh, math.Sinh}, {"cosh", ursus.Expr.Cosh, math.Cosh}, {"tanh", ursus.Expr.Tanh, math.Tanh},
		{"arcsinh", ursus.Expr.ArcSinh, math.Asinh}, {"arccosh", ursus.Expr.ArcCosh, math.Acosh},
		{"arctanh", ursus.Expr.ArcTanh, math.Atanh},
		{"degrees", ursus.Expr.Degrees, func(r float64) float64 { return r * 180 / math.Pi }},
		{"radians", ursus.Expr.Radians, func(d float64) float64 { return d * math.Pi / 180 }},
	}
	for _, fn := range fns {
		t.Run(fn.name, func(t *testing.T) {
			df, err := frame.Select(fn.expr(c("x")).Alias("y")).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			col, err := df.Column[float64]("y")
			if err != nil {
				t.Fatal(err)
			}
			for i, x := range in {
				got, ok := col.Get(i)
				want := fn.want(x)
				if !ok {
					t.Errorf("%s(%v) is null", fn.name, x)
					continue
				}
				// Out of the domain is NaN, as math says: arcsin(2), arccosh(0).
				if math.IsNaN(want) != math.IsNaN(got) || !math.IsNaN(want) && math.Abs(got-want) > 1e-12*math.Max(1, math.Abs(want)) {
					t.Errorf("%s(%v) = %v, want %v", fn.name, x, got, want)
				}
			}
			if _, ok := col.Get(len(in)); ok {
				t.Errorf("%s of a null is not null", fn.name)
			}
		})
	}

	t.Run("an integer widens to Float64, a Float32 stays", func(t *testing.T) {
		df, err := ursus.Frame(ursus.Values("i", []int64{0, 1}), ursus.Values("f", []float32{0, 1})).
			Select(c("i").Sin().Alias("i"), c("f").Cos().Alias("f")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := df.Schema().Field(0).Type; got != dtype.Float64 {
			t.Errorf("sin(Int64) is %s, want Float64", got)
		}
		if got := df.Schema().Field(1).Type; got != dtype.Float32 {
			t.Errorf("cos(Float32) is %s, want Float32", got)
		}
	})
	t.Run("degrees and radians are inverses", func(t *testing.T) {
		df, err := frame.GroupBy().Agg(c("x").Degrees().Radians().Sub(c("x")).Abs().Max().Alias("err")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		v, ok, err := df.At[float64](0, "err")
		if err != nil || !ok || v > 1e-15 {
			t.Errorf("the round trip is off by %v (%v, %v)", v, ok, err)
		}
	})
}
