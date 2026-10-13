package kernel

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
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
// ordinal, dense and min. Peers are winRank's: two nulls tie with each other and
// with nothing else, and two values tie when their order does. A group's last
// position, which max and average need, is not known until the group ends; the
// caller has it from min over the order reversed.
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
	peers := func(a, b int) bool {
		av, bv := valid.Get(a), valid.Get(b)
		if !av || !bv {
			return av == bv
		}
		return cmp(a, b) == 0
	}
	out := make([]uint32, n)
	for i := range n {
		r := lead + i
		if r == 0 || !peers(r-1, r) {
			st.dense++
			st.first = st.pos + 1
		}
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
	if n > 0 {
		st.prev = col.Slice(n-1, 1)
	}
	return data.NewFixed(name, dtype.Uint32, out, bitmap.AllSet(n)), nil
}
