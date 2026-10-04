package ursus_test

// FloorDiv and Mod over every integer type, against exact integers.
//
// FloorDiv floors, and Mod is its remainder: a == b*(a//b) + a%b, and a%b takes the
// divisor's sign. The pairs are each type's edges and the small values either side
// of zero, every one against every other. The oracle is big.Int's truncated
// quotient, corrected to a floor — it shares no code with the kernels.
//
// Excluded: a zero divisor, which is null and has its own case, and MinInt // -1,
// whose quotient is MaxInt+1 and wraps, as Go's and Polars' do.

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/advenn/ursus"
)

// knownFloorDivDefects counts the wrong pairs per type.
var knownFloorDivDefects = map[string]int{}

type integer interface {
	int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64
}

// edges is min, min+1, max-1, max and the small values either side of zero that T
// holds.
func edges[T integer](lo, hi T) []T {
	out := []T{lo, lo + 1, hi - 1, hi}
	for _, v := range []int64{-7, -2, -1, 0, 1, 2, 7} {
		if x := T(v); int64(x) == v && (x < 0) == (v < 0) {
			out = append(out, x)
		}
	}
	return out
}

func toBig[T integer](v T) *big.Int {
	if v < 0 {
		return big.NewInt(int64(v))
	}
	return new(big.Int).SetUint64(uint64(v))
}

// floorDivWrong runs every pair of vals through FloorDiv and Mod and returns the
// pairs whose answer is not the floored quotient and its remainder.
func floorDivWrong[T integer](t *testing.T, lo, hi T) (wrong []string, pairs int) {
	t.Helper()
	vals := edges(lo, hi)
	var as, bs []T
	for _, a := range vals {
		for _, b := range vals {
			if b == 0 || (lo < 0 && a == lo && b == T(0)-1) {
				continue
			}
			as, bs = append(as, a), append(bs, b)
		}
	}
	c := ursus.Col
	df, err := ursus.Frame(ursus.Values("a", as), ursus.Values("b", bs)).
		Select(c("a").FloorDiv(c("b")).Cast(ursus.String).Alias("q"),
			c("a").Mod(c("b")).Cast(ursus.String).Alias("m")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	qs, err := df.Column[string]("q")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := df.Column[string]("m")
	if err != nil {
		t.Fatal(err)
	}
	for i := range as {
		a, b := toBig(as[i]), toBig(bs[i])
		q, m := new(big.Int).QuoRem(a, b, new(big.Int))
		if m.Sign() != 0 && (m.Sign() < 0) != (b.Sign() < 0) {
			q.Sub(q, big.NewInt(1))
			m.Add(m, b)
		}
		gq, _ := qs.Get(i)
		gm, _ := ms.Get(i)
		if gq != q.String() || gm != m.String() {
			wrong = append(wrong, fmt.Sprintf("%v // %v = %s, %% = %s; want %s, %s", as[i], bs[i], gq, gm, q, m))
		}
	}
	return wrong, len(as)
}

func TestFloorDivAndModAgainstExactIntegers(t *testing.T) {
	var total int
	for _, tc := range []struct {
		name string
		run  func() ([]string, int)
	}{
		{"Int8", func() ([]string, int) { return floorDivWrong[int8](t, -1<<7, 1<<7-1) }},
		{"Int16", func() ([]string, int) { return floorDivWrong[int16](t, -1<<15, 1<<15-1) }},
		{"Int32", func() ([]string, int) { return floorDivWrong[int32](t, -1<<31, 1<<31-1) }},
		{"Int64", func() ([]string, int) { return floorDivWrong[int64](t, -1<<63, 1<<63-1) }},
		{"Uint8", func() ([]string, int) { return floorDivWrong[uint8](t, 0, 1<<8-1) }},
		{"Uint16", func() ([]string, int) { return floorDivWrong[uint16](t, 0, 1<<16-1) }},
		{"Uint32", func() ([]string, int) { return floorDivWrong[uint32](t, 0, 1<<32-1) }},
		{"Uint64", func() ([]string, int) { return floorDivWrong[uint64](t, 0, 1<<64-1) }},
	} {
		wrong, pairs := tc.run()
		total += pairs
		n, known := knownFloorDivDefects[tc.name]
		switch {
		case known && len(wrong) == 0:
			t.Errorf("%s answers correctly now; delete it from knownFloorDivDefects", tc.name)
		case known && len(wrong) != n:
			t.Errorf("%s: %d wrong, the ratchet says %d: %v", tc.name, len(wrong), n, wrong)
		case !known && len(wrong) > 0:
			t.Errorf("%s: %d wrong: %v", tc.name, len(wrong), wrong)
		}
	}
	if total < 600 {
		t.Fatalf("only %d pairs ran — the sweep has gone vacuous", total)
	}
}
