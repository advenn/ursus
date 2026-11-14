// Package invariants turns on internal/data's boundary checks in the test binary that
// imports it, for a fuzz target that builds batches (step 508):
//
//	import _ "github.com/advenn/ursus/internal/fuzzcheck/invariants"
//
// data.CheckNonNullable, on by default since step 98, says a column declared
// non-nullable holds no nulls; data.CheckTimeRange, off outside tests for its O(rows)
// cost, says a Time's ticks lie in a day. Each violation is ErrInternal at the batch
// that holds it, which fuzzcheck.Run fails, where it would otherwise surface as a
// wrong answer far downstream or not at all.
//
// It is a package of its own, at level 22 above internal/data, so that
// internal/fuzzcheck imports internal/uerr alone and stays usable from the internal
// tests of the packages internal/data imports: dtype, i128, bitmap. Test files alone
// import it; a program that did would pay CheckTimeRange's cost on every batch.
package invariants

import "github.com/advenn/ursus/internal/data"

func init() {
	data.CheckNonNullable = true
	data.CheckTimeRange = true
}

// On reports whether both checks are on, for a target to assert.
func On() bool { return data.CheckNonNullable && data.CheckTimeRange }
