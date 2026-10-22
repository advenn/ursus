package kernel_test

import (
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// TestFirstSeenIDs: FirstSeenIDs (step 320) numbers a column's values in the order
// each first appears, a null one value, on each of its paths: an integer's own
// values, a float's bits, a string's bytes and encoded keys. Every NaN is one value,
// -0.0 is +0.0's, and the empty string is not the null.
func TestFirstSeenIDs(t *testing.T) {
	valid := func(ok ...bool) bitmap.View {
		b := bitmap.NewBuilder(len(ok))
		for _, v := range ok {
			b.Append(v)
		}
		return b.Finish()
	}
	nan, negZero := math.NaN(), math.Copysign(0, -1)
	dec := func(vs ...int64) []i128.Int128 {
		out := make([]i128.Int128, len(vs))
		for i, v := range vs {
			out[i] = i128.FromInt64(v)
		}
		return out
	}
	for _, tc := range []struct {
		name        string
		col         *data.Column
		ids, firsts []int32
	}{
		{"Int64, a null", data.NewFixed("x", dtype.Int64, []int64{5, 7, 0, 5, 7}, valid(true, true, false, true, true)),
			[]int32{0, 1, 2, 0, 1}, []int32{0, 1, 2}},
		{"Int8", data.NewFixed("x", dtype.Int8, []int8{-1, 3, -1}, bitmap.AllSet(3)), []int32{0, 1, 0}, []int32{0, 1}},
		{"Float64", data.NewFixed("x", dtype.Float64, []float64{nan, negZero, 0, -nan, 1}, bitmap.AllSet(5)),
			[]int32{0, 1, 1, 0, 2}, []int32{0, 1, 4}},
		{"Float32, a null", data.NewFixed("x", dtype.Float32, []float32{1, 9, 1}, valid(true, false, true)),
			[]int32{0, 1, 0}, []int32{0, 1}},
		{"String, empty and null", data.NewString("x", []string{"b", "", "a", "b", ""}, valid(true, true, true, true, false)),
			[]int32{0, 1, 2, 0, 3}, []int32{0, 1, 2, 4}},
		{"Boolean, encoded", data.NewBool("x", valid(true, false, false, true), valid(true, true, false, true)),
			[]int32{0, 1, 2, 0}, []int32{0, 1, 2}},
		{"Decimal, encoded", data.NewFixed("x", dtype.Decimal(10, 2), dec(150, -3, 150, 0), valid(true, true, true, false)),
			[]int32{0, 1, 0, 2}, []int32{0, 1, 3}},
		{"all null", data.NewNull("x", dtype.Int64, 3), []int32{0, 0, 0}, []int32{0}},
		{"no rows", data.NewFixed("x", dtype.Int64, []int64{}, bitmap.AllSet(0)), []int32{}, nil},
	} {
		ids, firsts, err := kernel.FirstSeenIDs("test", tc.col)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !slices.Equal(ids, tc.ids) || !slices.Equal(firsts, tc.firsts) {
			t.Errorf("%s: ids %v firsts %v, want %v and %v", tc.name, ids, firsts, tc.ids, tc.firsts)
		}
	}

	// Past five parts, numbered in parallel and renumbered in order, the ids are one
	// pass's: a map's, on each path, nulls among them.
	big := 5*(1<<16) + 123
	ok := make([]bool, big)
	ints, floats, texts := make([]int64, big), make([]float64, big), make([]string, big)
	for i := range big {
		ok[i] = i%37 != 5
		ints[i] = int64(i * 7919 % 5003)
		floats[i] = float64(i%3001) / 4
		if i%1000 == 7 {
			floats[i] = negZero
		}
		texts[i] = "k" + string(rune('a'+i%26)) + string(rune('a'+i*31%26))
	}
	text := func(f float64) string {
		if f == 0 {
			return "0" // -0.0 too
		}
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	for name, tc := range map[string]struct {
		col *data.Column
		key func(i int) string
	}{
		"Int64":   {data.NewFixed("x", dtype.Int64, ints, valid(ok...)), func(i int) string { return string(rune(ints[i])) }},
		"Float64": {data.NewFixed("x", dtype.Float64, floats, valid(ok...)), func(i int) string { return text(floats[i]) }},
		"String":  {data.NewString("x", texts, valid(ok...)), func(i int) string { return texts[i] }},
		"Decimal": {data.NewFixed("x", dtype.Decimal(10, 0), dec(ints...), valid(ok...)), func(i int) string { return string(rune(ints[i])) }},
	} {
		number := map[string]int32{}
		var wantFirsts []int32
		wantIDs := make([]int32, big)
		for i := range big {
			key := "null"
			if ok[i] {
				key = "v" + tc.key(i)
			}
			id, seen := number[key]
			if !seen {
				id = int32(len(number))
				number[key] = id
				wantFirsts = append(wantFirsts, int32(i))
			}
			wantIDs[i] = id
		}
		ids, firsts, err := kernel.FirstSeenIDs("test", tc.col)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(ids, wantIDs) || !slices.Equal(firsts, wantFirsts) {
			t.Errorf("%s of %d rows: %d values, want %d", name, big, len(firsts), len(wantFirsts))
		}
	}

	// Past a chunk of keys, the numbering carries on.
	n := 3*kernel.ManyChunk + 5
	vals := make([]int64, n)
	for i := range vals {
		vals[i] = int64(i % 100)
	}
	ids, firsts, err := kernel.FirstSeenIDs("test", data.NewFixed("x", dtype.Int64, vals, bitmap.AllSet(n)))
	if err != nil {
		t.Fatal(err)
	}
	if len(firsts) != 100 || firsts[99] != 99 || ids[n-1] != int32((n-1)%100) {
		t.Errorf("%d rows of 100 values: %d firsts, the last row's id %d", n, len(firsts), ids[n-1])
	}
}
