package physical

// The as-of tolerance test, against an independent oracle.
//
// withinTolerance decides whether a candidate row is close enough to match, and every
// way it can fail answers TRUE — within tolerance. It scales the DATA up to
// nanoseconds (`d*npt`, where npt is 8.64e13 for a Date key) rather than scaling the
// tolerance down, so two ordinary dates 292 years apart match a one-hour bound; the
// difference itself can wrap; -d leaves MinInt64 negative, which satisfies every
// bound; and both conversion failures in the calendar branch return true rather than
// excluding.
//
// # The oracle is tri-state, deliberately
//
// within / outside / meaningless. Collapsing the third into a bool is how this defect
// happened: "one month after a five-second span" has no answer, and answering `true`
// makes the tolerance inert instead of loud.
//
// The duration oracle computes the ORIGINAL semantics — a nanosecond distance against
// a nanosecond tolerance — in math/big, so it proves the fix's tick arithmetic rather
// than restating it. The calendar oracle shares AddTo (whose calendar rules
// dtype/interval_test.go owns) and shares none of the tick plumbing, which is the part
// that changes.
//
// It must not use time.Time's Before/After: those compare the internal ext field as a
// signed int64, which is the one place Go's time package is not modular-consistent
// across the whole int64 tick range.

import (
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
)

// asOfKeyTypes is every key type the resolver admits a tolerance for, derived through
// its own predicate rather than listed.
func asOfKeyTypes(t *testing.T) []dtype.DataType {
	t.Helper()
	all := allOperandTypes()
	var keys []dtype.DataType
	for _, d := range all {
		if d.IsTemporal() { // nodes_asof.go's own gate for accepting a tolerance
			keys = append(keys, d)
		}
	}
	// Neither empty nor everything: if the resolver's gate changes, this file fails
	// rather than quietly sweeping nothing or sweeping types it cannot build.
	if len(keys) == 0 || len(keys) == len(all) {
		t.Fatalf("%d of %d operand types are temporal; the filter has gone vacuous",
			len(keys), len(all))
	}
	return keys
}

// asOfProbes adds this defect's landmarks to the shared probe set. probes itself is
// left alone: step 61's counters are calibrated on it.
var asOfProbes = append(slices.Clone(probes),
	106_751, -106_751, 106_752, -106_752, // the Date edge: 106752 days * 8.64e13 wraps
	9_223_372_036, -9_223_372_036, // the second FromTime can still represent
	9_223_372_037, -9_223_372_037, // the first it cannot
	9_222_422_400_000_000_000, // 2262-04-01T00:00:00Z in nanosecond ticks
)

type toleranceCase struct {
	label string
	tol   dtype.Interval
	cal   bool
}

func asOfTolerances() []toleranceCase {
	return []toleranceCase{
		{"0", dtype.FromDuration(0), false},
		{"1ns", dtype.FromDuration(1), false},
		{"1h", dtype.FromDuration(time.Hour), false},
		{"24h", dtype.FromDuration(24 * time.Hour), false},
		{"maxint64ns", dtype.FromDuration(math.MaxInt64), false},
		// 1500ms against a second-resolution key is the not-a-whole-number-of-ticks
		// cell, which no fixture in the repo covers.
		{"1500ms", dtype.Every("1500ms"), false},
		{"1mo", dtype.Every("1mo"), true},
		{"1d", dtype.Every("1d"), true},
		{"1y", dtype.Every("1y"), true},
		{"1mo12h", dtype.Every("1mo12h"), true},
	}
}

type verdict int

const (
	within verdict = iota
	outside
	meaningless // the question has no answer: a calendar span from a Duration or a Time
)

func (v verdict) String() string {
	switch v {
	case within:
		return "within"
	case outside:
		return "outside"
	}
	return "meaningless"
}

// oracleTolerance decides the case independently of the implementation.
func oracleTolerance(key dtype.DataType, tc toleranceCase, left, right int64) verdict {
	if tc.tol.IsZero() {
		return within // no bound at all
	}
	npt, ok := key.NanosPerTick()
	if !ok || npt <= 0 {
		return meaningless
	}

	if !tc.cal {
		// The ORIGINAL semantics, in big.Int: |left - right| * npt <= tolerance nanos.
		d := new(big.Int).Sub(big.NewInt(left), big.NewInt(right))
		d.Abs(d)
		d.Mul(d, big.NewInt(npt))
		if d.Cmp(big.NewInt(tc.tol.Nanos())) <= 0 {
			return within
		}
		return outside
	}

	// A calendar span is measured from the left key as an instant, so a key that is
	// not an instant has no answer.
	if key.ID() == dtype.TypeDuration || key.ID() == dtype.TypeTime {
		return meaningless
	}
	lt, ok := key.ToTime(left)
	if !ok {
		return meaningless
	}
	lo := oracleTicks(key, tc.tol.Neg().AddTo(lt), false)
	hi := oracleTicks(key, tc.tol.AddTo(lt), true)
	if right >= lo && right <= hi {
		return within
	}
	return outside
}

// oracleTicks converts an instant to ticks with its own arithmetic, SATURATING at the
// type's extremes rather than reporting failure: a bound that does not fit is the
// widest bound the type can express, and every candidate is a representable tick, so
// clamping admits exactly the set an unbounded side would.
func oracleTicks(key dtype.DataType, t time.Time, up bool) int64 {
	npt, _ := key.NanosPerTick()
	ns := new(big.Int).Mul(big.NewInt(t.Unix()), big.NewInt(int64(time.Second)))
	ns.Add(ns, big.NewInt(int64(t.Nanosecond())))

	q, m := new(big.Int), new(big.Int)
	q.DivMod(ns, big.NewInt(npt), m) // Euclidean: q is the floor, m >= 0
	if q.IsInt64() {
		return q.Int64()
	}
	if up {
		return math.MaxInt64
	}
	return math.MinInt64
}

// knownToleranceFailOpen names each (key family, tolerance kind) that disagrees with
// the oracle today, measured before any repair. Emptied by the commit that fixes it;
// an unlisted disagreement fails, and a listed one that now agrees is stale.
var knownToleranceFailOpen = map[string]bool{
	// Empty since the tolerance is measured in the key's own ticks and an
	// unrepresentable bound saturates. Seven shapes were listed here — Date, Datetime
	// and Duration under a fixed tolerance, and Date, Datetime, Duration and Time
	// under a calendar one — each measured before the repair; see step-63-as-built.md.
}

func failShape(key dtype.DataType, tc toleranceCase) string {
	kind := "dur"
	if tc.cal {
		kind = "cal"
	}
	return key.ID().String() + " " + kind
}

func TestAsOfToleranceAgreesWithTheOracle(t *testing.T) {
	keys := asOfKeyTypes(t)

	var compared int
	seen := map[string]map[verdict]int{} // key type -> verdict -> count
	observed := map[string]bool{}
	var wrong []string
	subTickCell := 0

	for _, key := range keys {
		npt, _ := key.NanosPerTick()
		for _, tc := range asOfTolerances() {
			sink := &asOfBuildSink{keyType: key, tolerance: tc.tol, allowExact: true}
			sink.loc = temporalLocation(key)
			// Through prepareTolerance, not by filling tolTicks here: a sink built by
			// hand would leave it zero and the sweep would exercise a path production
			// never takes.
			if err := sink.prepareTolerance(); err != nil {
				t.Fatalf("%s tol=%s: %v", key, tc.label, err)
			}

			for _, a := range asOfProbes {
				for _, b := range asOfProbes {
					left, right := fitTick(key, a), fitTick(key, b)
					want := oracleTolerance(key, tc, left, right)
					got, err := sink.withinTolerance(left, right)
					compared++

					if seen[key.String()] == nil {
						seen[key.String()] = map[verdict]int{}
					}
					seen[key.String()][want]++
					if key.ID() == dtype.TypeDatetime && npt == int64(time.Second) &&
						tc.label == "1500ms" {
						subTickCell++
					}

					// A question with no answer must not be answered: a refusal is
					// the only agreement. Every one of these used to return true,
					// which made the tolerance silently inert.
					agrees := (want == within) == got && err == nil
					if want == meaningless {
						agrees = err != nil
					}
					if agrees {
						continue
					}
					shape := failShape(key, tc)
					observed[shape] = true
					if !knownToleranceFailOpen[shape] && len(wrong) < 12 {
						wrong = append(wrong, key.String()+" tol="+tc.label+
							" left="+strconv.FormatInt(left, 10)+" right="+strconv.FormatInt(right, 10)+
							": engine="+boolStr(got)+errStr(err)+", oracle="+want.String())
					}
				}
			}
		}
	}

	if len(wrong) > 0 {
		t.Errorf("%d unlisted tolerance disagreements:\n  %s",
			len(wrong), strings.Join(wrong, "\n  "))
	}
	for shape := range knownToleranceFailOpen {
		if !observed[shape] {
			t.Errorf("%q no longer disagrees with the oracle — delete it", shape)
		}
	}

	// Anti-vacuity. A sweep where everything matches is the bug being swept for, and
	// one where nothing matches proves nothing — so BOTH verdicts must appear for
	// every key type.
	if len(keys) < 17 {
		t.Errorf("only %d key types derived", len(keys))
	}
	if compared < 60_000 {
		t.Errorf("only %d comparisons ran", compared)
	}
	if subTickCell == 0 {
		t.Error("the 1500ms x Datetime(s) cell never ran; the floor identity is unpinned")
	}
	for name, counts := range seen {
		if counts[within] == 0 || counts[outside] == 0 {
			t.Errorf("%s: the oracle answered within %d times and outside %d — a key "+
				"type that never does both proves nothing", name, counts[within], counts[outside])
		}
	}
	t.Logf("keys=%d comparisons=%d shapes=%d", len(keys), compared, len(observed))
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return " (refused)"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
