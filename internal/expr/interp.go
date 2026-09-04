package expr

// Interpolation selects how Quantile resolves a rank that falls between two
// values.
//
// It lives here, at the level of the expression IR, rather than in the root
// package, because both Agg's type resolution and the kernel need it and neither
// may import the other's caller. The root package re-exports it as an alias, the
// same way it does for JoinKind.
type Interpolation uint8

const (
	// InterpLinear interpolates proportionally between the two neighbours. The
	// default, and the only option whose result need not be an input value.
	InterpLinear Interpolation = iota

	// InterpLower and InterpHigher take the neighbour below or above.
	InterpLower
	InterpHigher

	// InterpNearest takes the closer neighbour, rounding half away from the lower
	// one.
	InterpNearest

	// InterpMidpoint averages the two neighbours regardless of where the rank fell
	// between them.
	InterpMidpoint

	interpCount
)

var interpNames = [interpCount]string{
	InterpLinear: "linear", InterpLower: "lower", InterpHigher: "higher",
	InterpNearest: "nearest", InterpMidpoint: "midpoint",
}

func (i Interpolation) String() string {
	// The `!= ""` half is the load-bearing one. interpNames is sized by the enum's
	// sentinel, so `int(i) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the three maps that dedup on String(). dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(i) < len(interpNames) && interpNames[i] != "" {
		return interpNames[i]
	}
	return "?"
}
