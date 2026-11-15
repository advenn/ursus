package data

import (
	"sort"

	"github.com/advenn/ursus/dtype"
)

// OutsideDay is a Time tick a column holds outside its day, and where.
//
// # One rule for every reader
//
// A Time is a time of day, [0, 24h) in its unit (dtype), and every kernel assumes it
// (CheckTimeRange). A reader takes the ticks its input holds as they lie, and nothing
// below it range-checks one: not a Parquet file's TIME, not arrow-go's Time32 or
// Time64. A tick past midnight read in is a Time that renders as a time of day, 26:00
// as 02:00:00, sorts after 23:00:00 and equals no 02:00:00, so each reader refuses
// one as the corrupt value it is, by FindOutsideDay: the Parquet reader since step
// 510, Arrow import, IPC files and streams among it, since step 525, which wrapped
// it into the day; on the 0.7 line both since step 532, which backported them.
type OutsideDay struct {
	At     *Column // the Time column holding it: the one looked in, or an element's or a field's
	Row    int     // the row of the column looked in that holds it
	Tick   int64
	PerDay int64 // a day's ticks in At's unit
}

// FindOutsideDay finds a valid tick outside its day that c holds, c a Time column or a
// List, Array or Struct holding one at any depth: the one of c's least row holding
// any; of a row's, its first element's; and of an element's or a row's, its first
// field's. A null holds no tick. A null list owns no element and a null struct's
// fields are null, the form every reader builds, so neither is looked past.
func FindOutsideDay(c *Column) (OutsideDay, bool) {
	if c == nil || !holdsTime(c.DType()) {
		return OutsideDay{}, false
	}
	switch c.DType().ID() {
	case dtype.TypeTime:
		perDay, ok := dtype.TicksPerDay(c.DType())
		if !ok {
			return OutsideDay{}, false
		}
		v, err := Values[int64](c)
		if err != nil {
			return OutsideDay{}, false // a column of nulls holds no ticks
		}
		for i, t := range v {
			if (t < 0 || t >= perDay) && c.IsValid(i) {
				return OutsideDay{At: c, Row: i, Tick: t, PerDay: perDay}, true
			}
		}
	case dtype.TypeList, dtype.TypeArray:
		ch := c.Child()
		if ch == nil || c.RawOffsets() == nil {
			return OutsideDay{}, false
		}
		// The elements of c's rows only: a sliced List keeps its whole child (Window).
		lists := c.Lists()
		lo, hi := lists.Window()
		if lo != 0 || int(hi) != ch.Len() {
			ch = ch.Slice(int(lo), int(hi-lo))
		}
		o, ok := FindOutsideDay(ch)
		if !ok {
			return OutsideDay{}, false
		}
		e := lo + int32(o.Row)
		o.Row = sort.Search(c.Len(), func(r int) bool {
			_, end, _ := lists.Get(r)
			return end > e
		})
		return o, true
	case dtype.TypeStruct:
		var first OutsideDay
		found := false
		for _, f := range c.Fields() {
			if o, ok := FindOutsideDay(f); ok && (!found || o.Row < first.Row) {
				first, found = o, true
			}
		}
		return first, found
	}
	return OutsideDay{}, false
}

// holdsTime is whether dt is a Time or holds one at any depth.
func holdsTime(dt dtype.DataType) bool {
	switch dt.ID() {
	case dtype.TypeTime:
		return true
	case dtype.TypeList, dtype.TypeArray:
		return holdsTime(dt.Inner())
	case dtype.TypeStruct:
		for _, f := range dt.Fields() {
			if holdsTime(f.Type) {
				return true
			}
		}
	}
	return false
}
