package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// RankState is what rank keeps between the rows of a partition walked in its order,
// so a window streaming one huge partition carries it between batches (step 237).
// Its zero value is a partition's start.
type RankState struct {
	pos   uint32       // rows ranked so far
	dense uint32       // peer groups begun so far
	first uint32       // the current peer group's first position, 1-based
	prev  *data.Column // the last row ranked, one row, to tell whether the next ties
}

// RankNext ranks col, the next rows of a partition in rank's order, continuing st:
// by position, by peer group, or by the group's first position, as winRank's
// ordinal, dense and min. Two values tie when their order does; a null has no rank
// and takes no position, as winRank's. A group's last position, which max and
// average need, is not known until the group ends; the caller has it from min over
// the order reversed.
func RankNext(method expr.RankMethod, name string, col *data.Column, st *RankState) (*data.Column, error) {
	switch method {
	case expr.RankOrdinal, expr.RankDense, expr.RankMin:
	default:
		return nil, uerr.Internalf("kernel: rank method %d has no streamed form", uint8(method))
	}
	n := col.Len()
	all, lead := col, 0
	if st.prev != nil {
		var err error
		if all, err = ConcatColumns(col.Name(), []*data.Column{st.prev, col}); err != nil {
			return nil, err
		}
		lead = 1
	}
	cmp, err := valueComparator(all)
	if err != nil {
		return nil, err
	}
	valid := all.Validity()
	out := make([]uint32, n)
	last := lead - 1 // the last value ranked, st.prev's row when there is one
	for i := range n {
		r := lead + i
		if !valid.Get(r) {
			continue
		}
		if last < 0 || cmp(last, r) != 0 {
			st.dense++
			st.first = st.pos + 1
		}
		last = r
		st.pos++
		switch method {
		case expr.RankOrdinal:
			out[i] = st.pos
		case expr.RankDense:
			out[i] = st.dense
		default:
			out[i] = st.first
		}
	}
	if last >= lead {
		st.prev = col.Slice(last-lead, 1)
	}
	return data.NewFixed(name, dtype.Uint32, out, col.Validity()), nil
}
