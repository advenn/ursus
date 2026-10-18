package kernel

import (
	"slices"

	"github.com/advenn/ursus/internal/data"
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
