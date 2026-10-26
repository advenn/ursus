package data

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/uerr"
)

// raisedBy runs build and returns the error it raised, recovered as every operator
// boundary recovers one.
func raisedBy(build func()) (err error) {
	defer uerr.Catch(&err, "test")
	build()
	return nil
}

// TestStringColumnsRefusePastTheirOffsets: a String column built from a reader's
// 32-bit offsets, whose characters would pass what they hold, is refused with a
// resource error, where it wrapped (audit.md S24); NewString, which counts first,
// builds 64-bit offsets instead (step 368). The limit is lowered to 1 KiB so the
// test can reach it.
func TestStringColumnsRefusePastTheirOffsets(t *testing.T) {
	defer func(old int64) { MaxStringBytes = old }(MaxStringBytes)
	MaxStringBytes = 1 << 10

	at := []string{strings.Repeat("x", 1000), strings.Repeat("y", 24)}
	past := append(at, "z")
	cases := []struct {
		name  string
		build func()
		fits  bool
	}{
		{"NewString at the limit builds", func() { NewString("s", at, bitmap.View{}) }, true},
		{"NewString past it builds", func() { NewString("s", past, bitmap.View{}) }, true},
		{"NewStringParts at the limit builds", func() {
			NewStringParts("s", []int32{0, 1024}, make([]byte, 1024), bitmap.View{})
		}, true},
		{"NewStringParts past it is refused", func() {
			NewStringParts("s", []int32{0, 1025}, make([]byte, 1025), bitmap.View{})
		}, false},
		{"NewStringOwned at the limit builds", func() {
			NewStringOwned("s", []int32{0, 1024}, make([]byte, 1024), bitmap.View{})
		}, true},
		{"NewStringOwned past it is refused", func() {
			NewStringOwned("s", []int32{0, 1025}, make([]byte, 1025), bitmap.View{})
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := raisedBy(tc.build)
			switch {
			case tc.fits && err != nil:
				t.Fatalf("refused a column that fits: %v", err)
			case !tc.fits && err == nil:
				t.Fatal("built a column past its offsets")
			case !tc.fits && !errors.Is(err, uerr.ErrResource):
				t.Fatalf("want a resource error, got %v", err)
			case !tc.fits && !strings.Contains(err.Error(), "smaller batches"):
				t.Fatalf("the refusal does not say what to do: %v", err)
			}
		})
	}
}

// wideRows are n rows of text, of 0 to 12 bytes, every seventh null, row i's text
// beginning with its number.
func wideRows(n int) ([]string, []bool) {
	vals, valid := make([]string, n), make([]bool, n)
	for i := range n {
		vals[i] = fmt.Sprintf("%d:%s", i, strings.Repeat("abc", i%4))
		valid[i] = i%7 != 3
		if !valid[i] {
			vals[i] = ""
		}
	}
	return vals, valid
}

func viewOf(valid []bool) bitmap.View {
	b := bitmap.NewBuilder(len(valid))
	for _, v := range valid {
		b.Append(v)
	}
	return b.Finish()
}

// wantRows checks c is rows vals, valid where valid says, holding 64-bit offsets
// where wide.
func wantRows(t *testing.T, what string, c *Column, vals []string, valid []bool, wide bool) {
	t.Helper()
	if got := c.offs64 != nil; got != wide {
		t.Errorf("%s: 64-bit offsets %v, want %v", what, got, wide)
	}
	if c.offs != nil && c.offs64 != nil {
		t.Errorf("%s: both offsets", what)
	}
	if (c.RawOffsets64() != nil) != wide || (c.RawOffsets() != nil) == wide {
		t.Errorf("%s: raw offsets of the wrong width", what)
	}
	if c.Len() != len(vals) {
		t.Fatalf("%s: %d rows, want %d", what, c.Len(), len(vals))
	}
	a := c.Strings()
	if a.Len() != len(vals) {
		t.Fatalf("%s: the accessor has %d rows, want %d", what, a.Len(), len(vals))
	}
	for i, v := range vals {
		if c.IsValid(i) != valid[i] {
			t.Fatalf("%s: row %d valid %v, want %v", what, i, c.IsValid(i), valid[i])
		}
		if valid[i] && (a.Get(i) != v || string(a.Bytes(i)) != v) {
			t.Fatalf("%s: row %d is %q, %q, want %q", what, i, a.Get(i), a.Bytes(i), v)
		}
	}
}

// TestStringColumnsOfSixtyFourBitOffsets: past MaxStringBytes, lowered to 64, every
// combiner builds 64-bit offsets, whatever its inputs', and 32-bit ones up to it, and
// each reads, slices, and counts its buffers alike (step 368).
func TestStringColumnsOfSixtyFourBitOffsets(t *testing.T) {
	defer func(old int64) { MaxStringBytes = old }(MaxStringBytes)
	MaxStringBytes = 64

	vals, valid := wideRows(40) // 349 bytes
	narrow := func(lo, hi int) *Column {
		return NewString("s", vals[lo:hi], viewOf(valid[lo:hi]))
	}
	wide := NewString("s", vals, viewOf(valid))

	t.Run("NewString", func(t *testing.T) {
		wantRows(t, "past the limit", wide, vals, valid, true)
		wantRows(t, "at most it", narrow(0, 10), vals[:10], valid[:10], false)
		if wide.IsPayloadFree() {
			t.Error("a 64-bit column is payload-free")
		}
		wantRows(t, "renamed", wide.Rename("t"), vals, valid, true)
		b := wide.WithDType(dtype.Binary)
		wantRows(t, "as Binary", b, vals, valid, true)
	})

	t.Run("its buffers", func(t *testing.T) {
		var sizes []int64
		for _, size := range wide.Buffers() {
			sizes = append(sizes, size)
		}
		if !slices.Contains(sizes, int64(41*8)) {
			t.Errorf("buffers %v: no 64-bit offsets of 41 entries", sizes)
		}
		if got, chars := wide.NBytes(), int64(len(wide.RawChars())); got < 41*8+chars {
			t.Errorf("%d bytes, want at least the offsets' and the characters' %d", got, 41*8+chars)
		}
		var set BufferSet
		set.AddColumn(wide)
		set.AddColumn(wide.Rename("t"))
		if set.Total() != wide.NBytes() {
			t.Errorf("a rename's buffers counted again: %d, want %d", set.Total(), wide.NBytes())
		}
	})

	t.Run("Slice", func(t *testing.T) {
		s := wide.Slice(5, 30)
		wantRows(t, "a slice", s, vals[5:35], valid[5:35], true)
		wantRows(t, "a slice's slice", s.Slice(3, 4), vals[8:12], valid[8:12], true)
		wantRows(t, "an empty slice", wide.Slice(40, 0), nil, nil, true)
	})

	t.Run("ConcatStrings", func(t *testing.T) {
		all := viewOf(valid)
		// 32-bit parts, one a slice with offsets not from zero, past the limit.
		parts := []*Column{narrow(0, 10), narrow(10, 25).Slice(0, 15), narrow(25, 40)}
		wantRows(t, "32-bit parts", ConcatStrings("s", dtype.String, parts, all), vals, valid, true)
		// A 64-bit part and a 32-bit one.
		mixed := []*Column{wide.Slice(0, 20), narrow(20, 40)}
		wantRows(t, "mixed parts", ConcatStrings("s", dtype.String, mixed, all), vals, valid, true)
		// 64-bit parts whose rows fit.
		few := []*Column{wide.Slice(0, 3), wide.Slice(3, 3)}
		wantRows(t, "64-bit parts that fit", ConcatStrings("s", dtype.String, few, viewOf(valid[:6])),
			vals[:6], valid[:6], false)
		// A part of no payload, every row null, and an empty one.
		nulls := NewNull("s", dtype.String, 2)
		got := ConcatStrings("s", dtype.String, []*Column{wide.Slice(0, 0), nulls, wide},
			viewOf(append([]bool{false, false}, valid...)))
		wantRows(t, "a null part", got, append([]string{"", ""}, vals...),
			append([]bool{false, false}, valid...), true)
	})

	sel := []int32{39, -1, 0, 5, 5, 38, 17}
	selVals, selValid := make([]string, len(sel)), make([]bool, len(sel))
	for j, i := range sel {
		if i >= 0 {
			selVals[j], selValid[j] = vals[i], valid[i]
		}
	}
	// Past the limit: every row of vals, four times over, in a shuffled order.
	var many []int32
	for k := range 4 {
		for i := range 40 {
			many = append(many, int32((i*13+k)%40))
		}
	}
	manyVals, manyValid := make([]string, len(many)), make([]bool, len(many))
	for j, i := range many {
		manyVals[j], manyValid[j] = vals[i], valid[i]
	}
	serial := func(n int, fn func(k int) error) error {
		for k := range n {
			if err := fn(k); err != nil {
				return err
			}
		}
		return nil
	}

	t.Run("TakeStrings", func(t *testing.T) {
		wantRows(t, "from 64-bit, fitting", TakeStrings(wide, sel, viewOf(selValid)), selVals, selValid, false)
		wantRows(t, "from 64-bit, past", TakeStrings(wide, many, viewOf(manyValid)), manyVals, manyValid, true)
		src := ConcatStrings("s", dtype.String, []*Column{narrow(0, 8)}, viewOf(valid[:8]))
		if src.offs64 != nil {
			t.Fatal("eight rows past the limit")
		}
		var rep []int32
		for range 6 {
			for i := range 8 {
				rep = append(rep, int32(i))
			}
		}
		repVals, repValid := make([]string, len(rep)), make([]bool, len(rep))
		for j, i := range rep {
			repVals[j], repValid[j] = vals[i], valid[i]
		}
		wantRows(t, "from 32-bit, past", TakeStrings(src, rep, viewOf(repValid)), repVals, repValid, true)
	})

	t.Run("TakeStringsChunked", func(t *testing.T) {
		for _, chunk := range []int{1, 3, 64, 1000} {
			got, err := TakeStringsChunked(wide, many, viewOf(manyValid), chunk, serial)
			if err != nil {
				t.Fatal(err)
			}
			wantRows(t, fmt.Sprintf("past, in chunks of %d", chunk), got, manyVals, manyValid, true)
			got, err = TakeStringsChunked(wide, sel, viewOf(selValid), chunk, serial)
			if err != nil {
				t.Fatal(err)
			}
			wantRows(t, fmt.Sprintf("fitting, in chunks of %d", chunk), got, selVals, selValid, false)
		}
	})
}
