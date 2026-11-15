package self

import (
	"math/bits"
	"slices"

	"github.com/advenn/ursus/qcheck/model"
)

// subsets are the subsets of the factors in diff, the fewest factors first, in the
// order Factors lists them within a size: the order bisection tries them.
func subsets(diff model.Factor) []model.Factor {
	var out []model.Factor
	for s := model.Factor(1); s <= diff; s++ {
		if s&^diff == 0 {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b model.Factor) int {
		return bits.OnesCount8(uint8(a)) - bits.OnesCount8(uint8(b))
	})
	return out
}

// Bisect is the self check's bisection: the fewest factors in which v differs from
// ref that, changed alone in ref, still fail, each single factor first, then
// pairs, and so on; fails says whether a configuration does. It returns the factors
// and their configuration, and found false, with v's own factors, when no subset
// failed, v's own among them.
//
// Step 514's shrinker takes a case's fewest factors through it, as the self check
// names a mismatch's.
func Bisect(ref, v model.Config, fails func(cfg model.Config) bool) (f model.Factor, cfg model.Config, found bool) {
	diff := v.Diff(ref)
	for _, s := range subsets(diff) {
		if cfg := ref.With(v, s); fails(cfg) {
			return s, cfg, true
		}
	}
	return diff, v, false
}
