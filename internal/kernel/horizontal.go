package kernel

import (
	"math"
	"slices"
	"sort"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// HorizontalCall applies a horizontal function (step 146) to its operands, each a
// column of the batch's height or a literal of length 1, typed out by
// expr.ResolveHorizontal.
func HorizontalCall(fn expr.CallFn, name string, out dtype.DataType,
	ops []*data.Column) (*data.Column, error) {

	if len(ops) == 0 {
		return nil, uerr.Internalf("kernel: %s has no operands", fn)
	}
	// The height is the one operand longer than a literal's, or 1 if every operand
	// is a literal; an empty batch's operands are 0 long.
	n := 1
	for _, c := range ops {
		if c.Len() == 1 {
			continue
		}
		if n != 1 && c.Len() != n {
			return nil, uerr.Internalf("kernel: %s over operands of %d and %d rows", fn, n, c.Len())
		}
		n = c.Len()
	}
	switch fn {
	case expr.FnConcatStr:
		return concatStr(name, ops, n)
	case expr.FnStructOf:
		return structOf(name, out, ops, n)
	case expr.FnCut, expr.FnCutLeftClosed:
		return cut(name, fn == expr.FnCutLeftClosed, ops, n)
	}
	return nil, uerr.Internalf("kernel: no horizontal kernel for %s", fn)
}

// concatStr joins each row's operands, formatted as text, and is null where any
// operand is: Polars' concat_str with its default, ignore_nulls=False. A first pass
// measures the rows, so the characters are allocated once and exactly.
func concatStr(name string, ops []*data.Column, n int) (*data.Column, error) {
	strs := make([]data.StringAccessor, len(ops))
	valids := make([]bitmap.View, len(ops))
	for i, c := range ops {
		if c.DType().ID() != dtype.TypeString {
			var err error
			if c, err = Cast(c.Name(), dtype.String, true, c); err != nil {
				return nil, err
			}
		}
		strs[i], valids[i] = c.Strings(), c.Validity()
	}
	valid := bitmap.NewBuilder(n)
	var size int64
	for row := range n {
		ok := true
		for i, c := range ops {
			if !valids[i].Get(broadcastIdx(c, row)) {
				ok = false
				break
			}
		}
		valid.Append(ok)
		if !ok {
			continue
		}
		for i, c := range ops {
			size += int64(len(strs[i].Get(broadcastIdx(c, row))))
		}
		if size > data.MaxStringBytes {
			return nil, stringTooLong("concat_str", name)
		}
	}
	vv := valid.Finish()
	offs := make([]int32, 1, n+1)
	chars := make([]byte, 0, size)
	for row := range n {
		if vv.Get(row) {
			for i, c := range ops {
				chars = append(chars, strs[i].Get(broadcastIdx(c, row))...)
			}
		}
		offs = append(offs, int32(len(chars)))
	}
	return data.NewStringParts(name, offs, chars, vv), nil
}

// structOf builds a Struct column whose fields are the operands, a literal repeated
// to the batch's height. The struct itself is never null, as Polars' is not: a row
// whose operands are all null is a struct of nulls.
func structOf(name string, out dtype.DataType, ops []*data.Column, n int) (*data.Column, error) {
	fields := out.Fields()
	if len(fields) != len(ops) {
		return nil, uerr.Internalf("kernel: struct of %d operands typed with %d fields", len(ops), len(fields))
	}
	children := make([]*data.Column, len(ops))
	for i, c := range ops {
		if c.Len() == 1 && n != 1 {
			var err error
			if c, err = readable(c); err != nil {
				return nil, err
			}
			if c, err = Take(c, make([]int32, n)); err != nil {
				return nil, err
			}
		}
		children[i] = c.Rename(fields[i].Name)
	}
	return data.NewStruct(name, children, bitmap.AllSet(n)), nil
}

// stringTooLong refuses a String column longer than its 32-bit offsets reach.
func stringTooLong(fn, name string) error {
	return uerr.New(uerr.KindValue, fn,
		"%s of %q needs more than the %d bytes a String column holds", fn, name, data.MaxStringBytes).
		Hint("a String column's offsets are 32-bit; work on fewer rows at once")
}

// cut puts each value in the bin its breaks give it (step 147), and answers the
// bin's label: the label operand of that bin, or else its interval, "(1, 2.5]" or,
// closed on the left, "[1, 2.5)", from -inf to inf.
//
// A null or NaN value, or one whose breaks are not all there — QCut's quantiles of
// a column of nulls — has no bin and is null. The breaks are read at each row, since
// QCut's are columns, and are non-decreasing: a break repeated, which QCut gives a
// column of few values, leaves an empty bin, never a misplaced value. An interval
// label is built again only when the breaks change, which for a literal or a
// quantile is once.
func cut(name string, leftClosed bool, ops []*data.Column, n int) (*data.Column, error) {
	fields := make([]dtype.Field, len(ops))
	for i, c := range ops {
		fields[i] = dtype.Field{Type: c.DType()}
	}
	k := expr.CutBreaks(fields)
	nums := make([][]float64, 1+k)
	valids := make([]bitmap.View, 1+k)
	for i, c := range ops[:1+k] {
		// A payload-free column of nulls, such as an aggregate's over nothing, is
		// given one to read, as every operand is (take.go's NullColumn).
		c, err := readable(c)
		if err != nil {
			return nil, err
		}
		if c.DType() != dtype.Float64 {
			if c, err = Cast(c.Name(), dtype.Float64, true, c); err != nil {
				return nil, err
			}
		}
		v, err := data.Values[float64](c)
		if err != nil {
			return nil, err
		}
		nums[i], valids[i] = v, c.Validity()
	}
	labels := ops[1+k:]

	out := make([]string, n)
	valid := bitmap.NewBuilder(n)
	at := make([]float64, k)
	var intervals []string // the interval labels of the breaks in seen
	seen := make([]float64, k)
	for row := range n {
		ok := true
		for i := range nums {
			j := broadcastIdx(ops[i], row)
			if !valids[i].Get(j) || math.IsNaN(nums[i][j]) {
				ok = false
				break
			}
			if i > 0 {
				at[i-1] = nums[i][j]
			}
		}
		valid.Append(ok)
		if !ok {
			continue
		}
		v := nums[0][broadcastIdx(ops[0], row)]
		// The bin is the number of breaks below v, or at or below it on the left.
		bin := sort.Search(k, func(j int) bool {
			if leftClosed {
				return at[j] > v
			}
			return at[j] >= v
		})
		if len(labels) > 0 {
			l := labels[bin]
			out[row] = l.Strings().Get(broadcastIdx(l, row))
			continue
		}
		if intervals == nil || !slices.Equal(at, seen) {
			intervals = cutIntervals(at, leftClosed)
			copy(seen, at)
		}
		out[row] = intervals[bin]
	}
	return data.NewString(name, out, valid.Finish()), nil
}

// cutIntervals names the k+1 bins of k breaks by their intervals, each break
// formatted as a cast to String formats a Float64.
func cutIntervals(breaks []float64, leftClosed bool) []string {
	open, close := "(", "]"
	if leftClosed {
		open, close = "[", ")"
	}
	bound := func(j int) string {
		switch {
		case j < 0:
			return "-inf"
		case j == len(breaks):
			return "inf"
		}
		return strconv.FormatFloat(breaks[j], 'g', -1, 64)
	}
	out := make([]string, len(breaks)+1)
	for j := range out {
		out[j] = open + bound(j-1) + ", " + bound(j) + close
	}
	return out
}
