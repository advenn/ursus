package kernel

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// A float aggregate that is NaN answers one NaN (step 530).
//
// Sum, Mean, Var, Std, Skew, Kurtosis, Product, Corr, Cov, CorrSpearman and Entropy
// combine their values by arithmetic, as do the rolling, cumulative and
// exponentially weighted sums, means and moments, and the list and horizontal
// reductions that run the same accumulators. Over NaNs of several payloads such an
// answer is NaN, and x86 hands on the first NaN operand's payload, so which one came
// out followed the order of the additions: a parallel group-by folds its workers'
// partial sums in worker order, and one query answered other bits on four threads
// than on one (step 523's "listed, not fixed"). Every NaN such an answer holds now
// leaves its kernel as nan64, math.NaN()'s bits, or nan32 in a Float32. Picks — Min,
// Max, First, Last, Median, Quantile, TopK and their kin — answer one of their
// values and keep its bits, which step 523 made the same in every order.
//
// math.NaN() is the NaN ursus made wherever it made one already: a rolling window
// holding a NaN, a correlation of fewer than two pairs, "NaN" parsed from text.
// Polars has no one NaN to agree with: its sum and product hand on the first NaN
// operand's payload, as ursus's did, and the NaN it makes itself is Rust's f64::NAN,
// 0x7FF8000000000000, a payload bit from Go's.
var (
	nan64 = math.NaN()                       // 0x7FF8000000000001
	nan32 = math.Float32frombits(0x7FC00000) // float32(math.NaN()), the quiet NaN
)

// oneNaN makes each NaN of v the canonical one, in place, and returns v.
func oneNaN(v []float64) []float64 {
	for i, x := range v {
		if x != x {
			v[i] = nan64
		}
	}
	return v
}

// narrowOneNaN is v narrowed to Float32, each NaN the canonical one.
func narrowOneNaN(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		if x != x {
			out[i] = nan32
		} else {
			out[i] = float32(x)
		}
	}
	return out
}

// aggFloatsAs is floatsAs for an answer by arithmetic: each NaN the canonical one.
func aggFloatsAs(name string, out dtype.DataType, res []float64, valid bitmap.View) *data.Column {
	if out == dtype.Float32 {
		return data.NewFixedOwned(name, out, narrowOneNaN(res), valid)
	}
	return data.NewFixedOwned(name, out, oneNaN(res), valid)
}
