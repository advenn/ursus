package parquet

import (
	"bytes"
	"cmp"
	"math"

	"github.com/apache/arrow-go/v18/parquet/metadata"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/internal/expr"
)

// bound is one endpoint of a column's statistics, or a literal converted to the
// same representation so the two can be compared.
//
// A tagged struct rather than an `any`: comparing two `any` values means a type
// assertion per comparison and an easy way to compare an int64 with a float64 by
// accident, which in a pruner means skipping a row group that matches.
type bound struct {
	kind boundKind
	i    int64
	u    uint64
	f    float64
	b    []byte
}

type boundKind uint8

const (
	boundInt boundKind = iota
	boundUint
	boundFloat
	boundBytes
)

func (a bound) cmp(b bound) int {
	if a.kind != b.kind {
		// Never reached: litBound is asked for a specific kind. Returning "equal"
		// would make everything look like a match, which is the safe direction.
		return 0
	}
	switch a.kind {
	case boundInt:
		return cmp.Compare(a.i, b.i)
	case boundUint:
		return cmp.Compare(a.u, b.u)
	case boundFloat:
		return cmp.Compare(a.f, b.f)
	default:
		return bytes.Compare(a.b, b.b)
	}
}

// statBounds extracts (min, max) from a statistics block.
//
// # Unsigned columns are refused, not reinterpreted
//
// Parquet stores a UINT_64 in an INT64, and arrow-go hands back Min and Max as
// int64. Reinterpreting them as uint64 is only correct if the writer used unsigned
// sort order, which older writers did not — and a wrong guess makes a range like
// [2^63, 2^63+10] look like [-2^63, -2^63+10], which prunes groups that match.
// Returning "no bounds" costs a read; guessing costs data.
func statBounds(st metadata.TypedStatistics) (lo, hi bound, ok bool) {
	if isUnsigned(st.Descr()) {
		return bound{}, bound{}, false
	}

	switch s := st.(type) {
	case *metadata.Int32Statistics:
		return bound{kind: boundInt, i: int64(s.Min())}, bound{kind: boundInt, i: int64(s.Max())}, true
	case *metadata.Int64Statistics:
		return bound{kind: boundInt, i: s.Min()}, bound{kind: boundInt, i: s.Max()}, true
	case *metadata.Float32Statistics:
		lo, hi := float64(s.Min()), float64(s.Max())
		if math.IsNaN(lo) || math.IsNaN(hi) {
			return bound{}, bound{}, false // NaN bounds are not an ordering
		}
		return bound{kind: boundFloat, f: lo}, bound{kind: boundFloat, f: hi}, true
	case *metadata.Float64Statistics:
		if math.IsNaN(s.Min()) || math.IsNaN(s.Max()) {
			return bound{}, bound{}, false
		}
		return bound{kind: boundFloat, f: s.Min()}, bound{kind: boundFloat, f: s.Max()}, true
	case *metadata.ByteArrayStatistics:
		return bound{kind: boundBytes, b: s.Min()}, bound{kind: boundBytes, b: s.Max()}, true
	default:
		// Boolean, Int96, FixedLenByteArray (decimals), Float16. Comparing a decimal
		// through its raw big-endian bytes would be wrong for negatives, and the
		// others have no useful literal to compare against yet.
		return bound{}, bound{}, false
	}
}

func isUnsigned(c *schema.Column) bool {
	if c == nil {
		return false
	}
	lt, ok := c.LogicalType().(schema.IntLogicalType)
	return ok && !lt.IsSigned()
}

// litBound converts a literal to the given representation, reporting false if it
// cannot be compared to bounds of that kind.
//
// Cross-kind comparisons are refused rather than converted. `float_col > 5` puts an
// int literal against float bounds, and converting 5 to 5.0 is exact — but the
// reverse, an int column against a float literal, is not always, and one rule that
// refuses both is easier to be sure of than two rules where one is subtly wrong.
// The cost is a read of a row group the engine's own Filter then handles correctly.
func litBound(l *expr.Lit, kind boundKind) (bound, bool) {
	if l.Value == nil {
		return bound{}, false // a null literal never matches a comparison anyway
	}
	switch v := l.Value.(type) {
	case int64:
		return intLit(v, kind)
	case int32:
		return intLit(int64(v), kind)
	case int16:
		return intLit(int64(v), kind)
	case int8:
		return intLit(int64(v), kind)
	case int:
		return intLit(int64(v), kind)
	case uint64:
		if kind == boundUint {
			return bound{kind: boundUint, u: v}, true
		}
		if kind == boundInt && v <= math.MaxInt64 {
			return bound{kind: boundInt, i: int64(v)}, true
		}
		return bound{}, false
	case float64:
		if kind == boundFloat {
			return bound{kind: boundFloat, f: v}, true
		}
		return bound{}, false
	case float32:
		if kind == boundFloat {
			return bound{kind: boundFloat, f: float64(v)}, true
		}
		return bound{}, false
	case string:
		if kind == boundBytes {
			return bound{kind: boundBytes, b: []byte(v)}, true
		}
		return bound{}, false
	case []byte:
		if kind == boundBytes {
			return bound{kind: boundBytes, b: v}, true
		}
		return bound{}, false
	default:
		return bound{}, false
	}
}

func intLit(v int64, kind boundKind) (bound, bool) {
	switch kind {
	case boundInt:
		return bound{kind: boundInt, i: v}, true
	case boundUint:
		if v < 0 {
			return bound{}, false
		}
		return bound{kind: boundUint, u: uint64(v)}, true
	case boundFloat:
		// Exact for every int64 a float64 can represent; refuse the rest rather
		// than compare a rounded value against a real bound.
		if v > 1<<53 || v < -(1<<53) {
			return bound{}, false
		}
		return bound{kind: boundFloat, f: float64(v)}, true
	default:
		return bound{}, false
	}
}
