package kernel

import (
	"math"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// listMode is each list's most frequent elements, each once, null a value, as
// Polars' mode counts it (step 277): in the order they first appear with maintain,
// else ascending, nulls first. Polars' order without maintain_order is its hash
// table's; sorted is one order it can give.
func listMode(name string, c *data.Column, maintain bool) (*data.Column, error) {
	child := c.Lists().Child()
	enc, err := NewGroupKeyEncoder("mode", []*data.Column{child})
	if err != nil {
		return nil, err
	}
	var cmp Comparator
	// A List's or a Struct's elements have no order: they come as they first appear
	// (step 344).
	maintain = maintain || !child.DType().IsOrdered()
	if !maintain {
		if cmp, err = NewComparator([]*data.Column{child}, []SortSpec{{}}); err != nil {
			return nil, err
		}
	}
	counts := make(map[string]int)
	firsts := make(map[string]int32)
	return listRebuild(name, c, func(start, end int32, out []int32) []int32 {
		clear(counts)
		clear(firsts)
		var order []string
		best := 0
		for e := start; e < end; e++ {
			k := string(enc.Encode(int(e)))
			if _, seen := firsts[k]; !seen {
				firsts[k] = e
				order = append(order, k)
			}
			counts[k]++
			best = max(best, counts[k])
		}
		mark := len(out)
		for _, k := range order {
			if counts[k] == best {
				out = append(out, firsts[k])
			}
		}
		if !maintain {
			slices.SortStableFunc(out[mark:], func(a, b int32) int { return cmp(int(a), int(b)) })
		}
		return out
	})
}

// counted is each list's distinct elements, by the group-by's keys, null a value, in
// the order they first appear, or by count with sorted: their offsets, the elements
// themselves, gathered from their first places, and their counts, in step.
func counted(c *data.Column, sorted bool) (offs []int32, values *data.Column, counts []int32, err error) {
	child := c.Lists().Child()
	enc, err := NewGroupKeyEncoder("value_counts", []*data.Column{child})
	if err != nil {
		return nil, nil, nil, err
	}
	at := make(map[string]int)
	offs, values, err = listGather(c, func(start, end int32, out []int32) []int32 {
		clear(at)
		mark := len(out)
		for e := start; e < end; e++ {
			k := string(enc.Encode(int(e)))
			if j, seen := at[k]; seen {
				counts[mark+j]++
				continue
			}
			at[k] = len(out) - mark
			out = append(out, e)
			counts = append(counts, 1)
		}
		if sorted {
			// By count, most first; ties as they first appeared, as Polars' sort is.
			idx := make([]int, len(out)-mark)
			for i := range idx {
				idx[i] = i
			}
			seg, cs := slices.Clone(out[mark:]), slices.Clone(counts[mark:])
			slices.SortStableFunc(idx, func(a, b int) int { return int(cs[b]) - int(cs[a]) })
			for i, j := range idx {
				out[mark+i], counts[mark+i] = seg[j], cs[j]
			}
		}
		return out
	})
	return offs, values, counts, err
}

// listUniqueCounts is each list's distinct elements' counts, in the order they first
// appear, a List(UInt32): Polars' unique_counts in an aggregation (step 280).
func listUniqueCounts(name string, c *data.Column) (*data.Column, error) {
	offs, _, counts, err := counted(c, false)
	if err != nil {
		return nil, err
	}
	u := make([]uint32, len(counts))
	for i, k := range counts {
		u[i] = uint32(k)
	}
	return data.NewList(name, offs, data.NewFixed(name, dtype.Uint32, u, bitmap.AllSet(len(u))), c.Validity()), nil
}

// listValueCounts is each list's distinct elements beside their counts, a Struct
// each, a List of them: Polars' value_counts in an aggregation (step 280). args are
// sort, normalize, the count's name and the value's; normalized, a count is over the
// list's length, nulls counted.
func listValueCounts(name string, out dtype.DataType, c *data.Column, args []any) (*data.Column, error) {
	sorted, _ := argBool(args, 0)
	normalize, _ := argBool(args, 1)
	offs, values, counts, err := counted(c, sorted)
	if err != nil {
		return nil, err
	}
	fields := out.Inner().Fields()
	var count *data.Column
	if normalize {
		shares := make([]float64, len(counts))
		acc := c.Lists()
		for i := range c.Len() {
			start, end, ok := acc.Get(i)
			if !ok {
				continue
			}
			for j := offs[i]; j < offs[i+1]; j++ {
				shares[j] = float64(counts[j]) / float64(end-start)
			}
		}
		count = data.NewFixed(fields[1].Name, dtype.Float64, shares, bitmap.AllSet(len(shares)))
	} else {
		u := make([]uint32, len(counts))
		for i, k := range counts {
			u[i] = uint32(k)
		}
		count = data.NewFixed(fields[1].Name, dtype.Uint32, u, bitmap.AllSet(len(u)))
	}
	pairs := data.NewStruct(name, []*data.Column{values.Rename(fields[0].Name), count}, bitmap.AllSet(len(counts)))
	return data.NewList(name, offs, pairs, c.Validity()), nil
}

// listRle is each list's runs of equal elements, by the group-by's keys, so NaN runs
// with NaN, -0 with 0 and a null with a null: a Struct of each run's length and its
// first value, a List of them, Polars' rle in an aggregation (step 281).
func listRle(name string, out dtype.DataType, c *data.Column) (*data.Column, error) {
	child := c.Lists().Child()
	enc, err := NewGroupKeyEncoder("rle", []*data.Column{child})
	if err != nil {
		return nil, err
	}
	var lens []uint32
	offs, values, err := listGather(c, func(start, end int32, out []int32) []int32 {
		var prev string
		for e := start; e < end; e++ {
			k := string(enc.Encode(int(e)))
			if e > start && k == prev {
				lens[len(lens)-1]++
				continue
			}
			prev = k
			out = append(out, e)
			lens = append(lens, 1)
		}
		return out
	})
	if err != nil {
		return nil, err
	}
	fields := out.Inner().Fields()
	runs := data.NewStruct(name, []*data.Column{
		data.NewFixed(fields[0].Name, dtype.Uint32, lens, bitmap.AllSet(len(lens))),
		values.Rename(fields[1].Name),
	}, bitmap.AllSet(len(lens)))
	return data.NewList(name, offs, runs, c.Validity()), nil
}

// listAppend is each row's first list then its second, a List of out's elements:
// Append in an aggregation (step 282). ops are the two lists and the mode; the second
// is cast to out's element, a float truncated toward zero where the mode is
// AppendCast and the element an integer, as Polars' extend_constant casts its value.
func listAppend(name string, out dtype.DataType, ops []*data.Column, n int) (*data.Column, error) {
	cast := data.MustValues[int64](ops[2])[0] == expr.AppendCast
	lists := make([]*data.Column, 2)
	for k, c := range ops[:2] {
		if c.DType() == out {
			lists[k] = c
			continue
		}
		var err error
		if k == 1 && cast {
			c, err = truncatedFor(c, out.Inner())
			if err != nil {
				return nil, err
			}
		}
		if lists[k], err = Cast(c.Name(), out, !cast, c); err != nil {
			return nil, err
		}
	}
	return listsOf(name, out, lists, n)
}

// truncatedFor is a List of floats with each element truncated toward zero, where to
// is an integer type; c as it is otherwise.
func truncatedFor(c *data.Column, to dtype.DataType) (*data.Column, error) {
	if c.DType().ID() != dtype.TypeList || !c.DType().Inner().IsFloat() || !to.IsInteger() {
		return c, nil
	}
	// The list's own elements, zero-based, so a sliced list keeps only its window.
	offs, elems, err := listGather(c, func(start, end int32, out []int32) []int32 {
		for e := start; e < end; e++ {
			out = append(out, e)
		}
		return out
	})
	if err != nil {
		return nil, err
	}
	if elems, err = Cast(elems.Name(), dtype.Float64, true, elems); err != nil {
		return nil, err
	}
	if elems, err = readable(elems); err != nil {
		return nil, err
	}
	v := data.MustValues[float64](elems)
	t := make([]float64, len(v))
	for i, x := range v {
		t[i] = math.Trunc(x)
	}
	return data.NewList(c.Name(), offs, data.NewFixed(elems.Name(), dtype.Float64, t, elems.Validity()), c.Validity()), nil
}
