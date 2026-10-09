package ursus

import (
	"math"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// CutOption configures Cut and QCut.
type CutOption func(*cutOptions)

type cutOptions struct {
	labels     []string
	leftClosed bool
}

// CutLabels names the bins, lowest first: one label more than there are breaks.
// Without it, a bin is named by its interval, "(-inf, 1]", "(1, 2.5]", "(2.5, inf]".
func CutLabels(labels ...string) CutOption {
	return func(o *cutOptions) { o.labels = labels }
}

// CutLeftClosed closes each bin on the left, [1, 2.5), where the default, Polars'
// too, closes it on the right, (1, 2.5].
func CutLeftClosed() CutOption {
	return func(o *cutOptions) { o.leftClosed = true }
}

// Cut puts each value in a bin between breaks, and answers the bin's label, a
// String: Polars' cut.
//
//	Col("age").Cut([]float64{18, 65})                                 // "(-inf, 18]", "(18, 65]", "(65, inf]"
//	Col("age").Cut([]float64{18, 65}, CutLabels("minor", "adult", "senior"))
//
// The breaks must be finite and increase. A value is compared as a Float64; a null
// or a NaN has no bin and is null. The answer is named after the value. Polars
// answers a Categorical; ursus a String, since an Enum column cannot yet be built
// from the public API.
func (e Expr) Cut(breaks []float64, opts ...CutOption) Expr {
	for i, b := range breaks {
		if math.IsNaN(b) || math.IsInf(b, 0) {
			return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "cut",
				"break %d is %v; breaks must be finite", i, b)})
		}
		if i > 0 && !(b > breaks[i-1]) {
			return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "cut",
				"breaks must increase, and %v follows %v", b, breaks[i-1])})
		}
	}
	args := make([]expr.Node, 0, 2+2*len(breaks))
	args = append(args, e.node())
	for _, b := range breaks {
		args = append(args, litNode(b))
	}
	return cutOf(args, len(breaks), opts)
}

// QCut puts each value in a bin between quantiles of its column, and answers the
// bin's label: Polars' qcut.
//
//	Col("income").QCut([]float64{0.25, 0.5, 0.75})   // quartiles
//
// Each break is the column's Quantile at q, interpolated linearly, over the whole
// frame (Over()), so QCut is a window: it belongs in Select or WithColumns. Labels,
// the closed side, and a null or NaN value are as Cut's. A column of few distinct
// values can give two equal quantiles, and the bin between them is then empty, where
// Polars refuses it unless allow_duplicates is set.
func (e Expr) QCut(quantiles []float64, opts ...CutOption) Expr {
	for i, q := range quantiles {
		if !(q >= 0 && q <= 1) {
			return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "qcut",
				"quantile %d is %v; each must be between 0 and 1", i, q)})
		}
		if i > 0 && !(q > quantiles[i-1]) {
			return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "qcut",
				"quantiles must increase, and %v follows %v", q, quantiles[i-1])})
		}
	}
	args := make([]expr.Node, 0, 2+2*len(quantiles))
	args = append(args, e.node())
	for _, q := range quantiles {
		args = append(args, e.Quantile(q, InterpLinear).Over().node())
	}
	return cutOf(args, len(quantiles), opts)
}

// QCutN is QCut into n bins of equal count: QCutN(4) is QCut at 0.25, 0.5 and 0.75.
func (e Expr) QCutN(n int, opts ...CutOption) Expr {
	if n < 1 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "qcut",
			"QCutN needs at least one bin, got %d", n)})
	}
	qs := make([]float64, n-1)
	for i := range qs {
		qs[i] = float64(i+1) / float64(n)
	}
	return e.QCut(qs, opts...)
}

// cutOf finishes a cut over args, the value and its k breaks.
func cutOf(args []expr.Node, k int, opts []CutOption) Expr {
	var o cutOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.labels != nil && len(o.labels) != k+1 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "cut",
			"%d breaks make %d bins, and %d labels were given", k, k+1, len(o.labels))})
	}
	for _, l := range o.labels {
		args = append(args, litNode(l))
	}
	fn := expr.FnCut
	if o.leftClosed {
		fn = expr.FnCutLeftClosed
	}
	return wrap(&expr.Call{Fn: fn, Args: args})
}
