package kernel

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// list.hist (step 286): each list's values counted in bins, Polars' hist
// (crates/polars-ops/src/chunked_array/hist.rs at py-1.44.1).
//
//   - Edges given: they must increase, and fewer than two make no bin. A value is
//     in the bin whose right edge is the first not below it; the first bin holds its
//     left edge as well. A value outside the edges, or NaN, is in none.
//   - A bin count, 10 by default: equal widths over the values' least to greatest,
//     NaN aside, the last edge the greatest itself. One value, or values all alike,
//     are the bins over the value ± 0.5, and no value the bins over [0, 1].
//
// Each bin answers its count, a UInt32, or a Struct of its right edge, its interval
// as Polars writes it, "[1.0, 3.333333]" then "(3.333333, 5.666667]", and its count.
func listHist(name string, c *data.Column, args []any) (*data.Column, error) {
	if len(args) < 3 {
		return nil, uerr.Internalf("kernel: list.hist without its arguments")
	}
	binCount, _ := argInt(args, 0)
	category, _ := argBool(args, 1)
	breakpoint, _ := argBool(args, 2)
	var edges []float64
	for _, a := range args[3:] {
		e, ok := a.(float64)
		if !ok {
			return nil, uerr.Internalf("kernel: list.hist with an edge %v", a)
		}
		edges = append(edges, e)
	}
	for i := 1; i < len(edges); i++ {
		if !(edges[i]-edges[i-1] > 0) {
			return nil, uerr.New(uerr.KindValue, "hist", "the bins' edges must increase, and %v follows %v", edges[i], edges[i-1])
		}
	}
	offs, elems, err := listGather(c, func(start, end int32, out []int32) []int32 {
		for e := start; e < end; e++ {
			out = append(out, e)
		}
		return out
	})
	if err != nil {
		return nil, err
	}
	if elems, err = readable(elems); err != nil {
		return nil, err
	}
	vals, err := toFloat64(elems)
	if err != nil {
		return nil, err
	}
	valid := elems.Validity()
	listValid := c.Validity()
	var bps []float64
	var cats []string
	var counts []uint32
	outOffs := make([]int32, 1, c.Len()+1)
	var xs []float64
	for i := range c.Len() {
		if listValid.Get(i) {
			xs = xs[:0]
			for e := offs[i]; e < offs[i+1]; e++ {
				if valid.Get(int(e)) {
					xs = append(xs, vals[e])
				}
			}
			breaks := edges
			uniform := binCount >= 0
			if uniform {
				breaks = uniformBreaks(xs, int(binCount))
			}
			bins := max(len(breaks), 1) - 1
			n := make([]uint32, bins)
			switch {
			case bins == 0 || len(xs) == 0:
			case uniform:
				uniformCount(breaks, xs, n)
			default:
				edgeCount(breaks, xs, n)
			}
			counts = append(counts, n...)
			if bins > 0 {
				bps = append(bps, breaks[1:]...)
				open := "["
				for b := 1; b < len(breaks); b++ {
					cats = append(cats, open+polarsFloat(breaks[b-1])+", "+polarsFloat(breaks[b])+"]")
					open = "("
				}
			}
		}
		outOffs = append(outOffs, int32(len(counts)))
	}
	all := bitmap.AllSet(len(counts))
	child := data.NewFixed("count", dtype.Uint32, counts, all)
	if category || breakpoint {
		var fields []*data.Column
		if breakpoint {
			fields = append(fields, data.NewFixed("breakpoint", dtype.Float64, bps, all))
		}
		if category {
			fields = append(fields, data.NewString("category", cats, all).WithDType(dtype.Categorical))
		}
		child = data.NewStruct(name, append(fields, child), all)
	}
	return data.NewList(name, outOffs, child, listValid), nil
}

// uniformBreaks is Polars' get_breaks for a bin count: k+1 edges, x·width + offset,
// the last the upper limit itself.
func uniformBreaks(xs []float64, k int) []float64 {
	offset, width, upper := 0.0, 1/float64(k), 1.0
	switch len(xs) {
	case 0:
	case 1:
		offset, upper = xs[0]-0.5, xs[0]+0.5
	default:
		lo, hi := math.NaN(), math.NaN()
		for _, x := range xs {
			if !math.IsNaN(x) && !(x >= lo) {
				lo = x
			}
			if !math.IsNaN(x) && !(x <= hi) {
				hi = x
			}
		}
		if lo == hi {
			offset, upper = lo-0.5, hi+0.5
		} else {
			offset, width, upper = lo, (hi-lo)/float64(k), hi
		}
	}
	b := make([]float64, 0, k+1)
	for x := range k {
		b = append(b, float64(float64(x)*width)+offset)
	}
	return append(b, upper)
}

// uniformCount is Polars' uniform_hist_count: a value's bin by its scaled offset,
// corrected by one either way against the edges.
func uniformCount(breaks, xs []float64, n []uint32) {
	k := len(breaks) - 1
	lo, hi := breaks[0], breaks[k]
	scale := float64(k) / (hi - lo)
	for _, x := range xs {
		switch {
		case x > lo && x <= hi:
			i := k - 1
			if f := scale * (x - lo); f < float64(k-1) {
				i = int(f)
			}
			if x <= breaks[i] {
				i--
			} else if x > breaks[i+1] {
				i++
			}
			n[i]++
		case x == lo:
			n[0]++
		}
	}
}

// edgeCount is Polars' hist_count: the values sorted and walked against the edges.
func edgeCount(breaks, xs []float64, n []uint32) {
	s := slices.Clone(xs)
	slices.Sort(s)
	lo, hi := breaks[0], breaks[len(breaks)-1]
	bin, upper := 0, breaks[1]
	for _, x := range s {
		if math.IsNaN(x) || x < lo {
			continue
		}
		for x > upper {
			if x > hi {
				return
			}
			bin++
			upper = breaks[bin+1]
		}
		n[bin]++
	}
}

// polarsFloat writes v as Polars displays a float (polars-core's fmt_float, in its
// default settings): a whole number below 999999 with one decimal, 1.0; one whose
// shortest form is longer than nine characters with six decimals, its trailing
// zeros cut, or in scientific notation with four outside [1e-6, 999999]; otherwise
// its shortest form, in scientific notation if whole.
func polarsFloat(v float64) string {
	whole := v == math.Trunc(v) && !math.IsInf(v, 0)
	if whole && math.Abs(v) < 999999 {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	plain := rustDisplay(v)
	switch {
	case len(plain) > 9:
		if a := math.Abs(v); !(a >= 0.000001 && a <= 999999) {
			return rustExp(strconv.FormatFloat(v, 'e', 4, 64))
		}
		s := strings.TrimRight(strconv.FormatFloat(v, 'f', 6, 64), "0")
		if strings.HasSuffix(s, ".") {
			s += "0"
		}
		return s
	case whole:
		return rustExp(strconv.FormatFloat(v, 'e', -1, 64))
	}
	return plain
}

// rustDisplay is Rust's {} of a float: its shortest form, never in scientific
// notation, NaN and inf as Rust writes them.
func rustDisplay(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// rustExp is Go's scientific notation as Rust writes it: 1.5e6 for 1.5e+06.
func rustExp(s string) string {
	m, e, ok := strings.Cut(s, "e")
	if !ok {
		return s
	}
	neg := strings.HasPrefix(e, "-")
	e = strings.TrimLeft(strings.TrimLeft(e, "+-"), "0")
	if e == "" {
		e = "0"
	}
	if neg {
		e = "-" + e
	}
	return m + "e" + e
}
