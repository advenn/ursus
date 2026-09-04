// Package kernel is ursus's compute layer.
//
// # The null contract, enforced by types
//
// Getting null propagation right is the hardest correctness problem in a
// dataframe engine, because getting it wrong is silent: the query returns a
// number, the number is just subtly incorrect. The design this package replaces
// had a single signature
//
//	func(dst, a, b []T, validity *bitmap.Bitmap)
//
// which takes ONE validity bitmap and no input validities — making `null + 1 ==
// null` literally inexpressible.
//
// The fix here is structural rather than a matter of discipline. Kernels are
// split into classes by how they relate to nulls, and a kernel is only handed the
// validity bitmaps it is entitled to see:
//
//	Total    never sees validity, never produces nulls.
//	         The DISPATCHER computes `valid_out = valid_a AND valid_b`.
//	Partial  may itself produce nulls (integer division by zero, a narrowing
//	         cast, sqrt of a negative). Receives the combined input validity so it
//	         can skip lanes it must not evaluate, and writes its own `ok` mask,
//	         which the dispatcher ANDs with the input validity.
//	Missing  nulls are DATA, not absence. null == null is true and the output is
//	         never null.
//	Kleene   three-valued boolean, where output validity is NOT `va AND vb`.
//
// A Total kernel author cannot get propagation wrong because they are not given
// the bitmaps. That is the whole point.
package kernel

import (
	"github.com/advenn/ursus/internal/bitmap"
)

// Numeric is the set of physical storage types the kernels operate on.
//
// Logical types reach these through their physical representation: a Datetime
// column is processed as int64, a Date column as int32. One integer kernel
// therefore serves every type that stores integers.
type Numeric interface {
	~int8 | ~int16 | ~int32 | ~int64 |
		~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// Integer is the subset of Numeric on which `%` is defined and on which division
// by zero PANICS rather than producing an infinity. Kernels that divide must be
// constrained to it, not to Numeric — otherwise instantiating them for float32
// fails to compile, which is Go usefully refusing to let the two very different
// division semantics share one implementation.
type Integer interface {
	~int8 | ~int16 | ~int32 | ~int64 |
		~uint8 | ~uint16 | ~uint32 | ~uint64
}

// --- Class A: TOTAL ----------------------------------------------------------
//
// Defined for every lane, including lanes whose inputs are null. A Total kernel
// computes something for a null lane — it cannot know which lanes those are — and
// that value is unspecified but deterministic. Nothing reads it, because the
// dispatcher marks the lane invalid.

// TotalBinary computes dst = a OP b elementwise.
type TotalBinary[T Numeric] func(dst, a, b []T)

// TotalBinaryScalar computes dst = a OP s elementwise.
type TotalBinaryScalar[T Numeric] func(dst, a []T, s T)

// TotalUnary computes dst = OP a elementwise.
type TotalUnary[T Numeric] func(dst, a []T)

// TotalCompare writes the boolean PAYLOAD bits of a OP b.
//
// It writes one bitmap, not two. The VALIDITY of the result is the dispatcher's
// job, because `null > 5` must be null rather than false — an Arrow Boolean
// column has two bitmaps and conflating them was defect D13(3).
type TotalCompare[T Numeric] func(out *bitmap.Builder, a, b []T)

// TotalCompareScalar writes the boolean payload bits of a OP s.
type TotalCompareScalar[T Numeric] func(out *bitmap.Builder, a []T, s T)

// --- Class B: PARTIAL --------------------------------------------------------

// PartialBinary computes dst = a OP b, writing its own validity into ok.
//
// `in` is the combined input validity, supplied so the kernel can SKIP lanes it
// must not evaluate. That is not an optimization: integer division panics in Go,
// so a kernel that blindly computed a[i]/b[i] over a null lane containing a zero
// would crash the process.
type PartialBinary[T Numeric] func(dst []T, ok *bitmap.Builder, a, b []T, in bitmap.View)

// PartialBinaryScalar is PartialBinary with a scalar right operand.
type PartialBinaryScalar[T Numeric] func(dst []T, ok *bitmap.Builder, a []T, s T, in bitmap.View)

// --- Class C: MISSING COMPARISON ---------------------------------------------

// MissingCompare implements eq_missing / ne_missing, where nulls compare equal to
// each other. It needs the input validities because nulls are the data, and its
// output is never null.
type MissingCompare[T Numeric] func(out *bitmap.Builder, a, b []T, va, vb bitmap.View)

// --- Class D: KLEENE ---------------------------------------------------------

// KleeneBinary implements three-valued AND/OR/XOR.
//
// Output validity is NOT `va AND vb`:
//
//	false AND null == false   (valid!)
//	true  OR  null == true    (valid!)
//
// so the kernel must compute both bitmaps itself. Using `va AND vb` here is a
// silent filter bug: rows that should survive get dropped.
type KleeneBinary func(outVals, outValid *bitmap.Builder, a, va, b, vb bitmap.View)
