package kernel

import (
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// dtOffsetEach is dt.offset_by with an offset a row, each a duration string as Every
// reads it (step 256, E5 of v0.6.5-scope.md): Polars' offset_by(expr). It is null
// where the instant or the offset is. An offset that does not parse, or that is
// finer than the column holds, is an error naming its row, where the constant form
// is refused while the query is planned; each moves an instant as the constant
// form's does.
func dtOffsetEach(name string, out dtype.DataType, c, by *data.Column, n int) (*data.Column, error) {
	dt := c.DType()
	if !isDateLike(dt) {
		return nil, uerr.Internalf("kernel: offset_by on %s", dt)
	}
	if by.DType().IsNull() {
		return NullColumn(name, out, n)
	}
	ticks, err := readTicks(c)
	if err != nil {
		return nil, err
	}
	loc := locationOf(dt)
	npt, _ := dt.NanosPerTick()
	strs := by.Strings()
	seen := map[string]dtype.Interval{}
	res := make([]int64, n)
	valid := bitmap.NewBuilder(n)
	for i := range n {
		r, b := rowOf(c, i), rowOf(by, i)
		if !c.IsValid(r) || !by.IsValid(b) {
			valid.Append(false)
			continue
		}
		s := strs.Get(b)
		iv, ok := seen[s]
		if !ok {
			iv = dtype.Every(s)
			if err := expr.OffsetRefusal(iv, dt); err != nil {
				return nil, uerr.Wrap(err, uerr.KindValue, "dt", "offset_by %q in row %d", s, i)
			}
			// A column of distinct offsets would hold every one: past a few hundred
			// the cache starts over.
			if len(seen) >= 256 {
				clear(seen)
			}
			seen[strings.Clone(s)] = iv
		}
		t := ticks[r]
		var v int64
		if iv.IsCalendar() {
			tm, ok := dt.ToTime(t)
			if !ok {
				return nil, uerr.Internalf("kernel: offset_by cannot read %s as an instant", dt)
			}
			if loc != nil {
				tm = tm.In(loc)
			}
			v, ok = fromTime(dt, iv.AddTo(tm))
			if !ok {
				return nil, offsetRange(dt, t, iv)
			}
		} else {
			step := iv.Nanos() / npt
			if overflowsI64(expr.OpAdd, t, step) || !fitsTicks(dt, t+step) {
				return nil, offsetRange(dt, t, iv)
			}
			v = t + step
		}
		res[i] = v
		valid.Append(true)
	}
	return packTicks(name, out, res, valid.Finish()), nil
}

// offsetRange refuses an instant t moved by iv past what dt holds.
func offsetRange(dt dtype.DataType, t int64, iv dtype.Interval) error {
	return uerr.New(uerr.KindValue, "dt",
		"offset_by of %s by %s is outside the range of %s", dtype.FormatTemporal(dt, t), iv, dt).
		Hint(widerUnitHint)
}
