package ursus

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// Dummy columns (step 320, tier 2 of v0.6.7-scope.md): Polars' to_dummies.

// DummiesOptions configures ToDummies.
type DummiesOptions struct {
	// Columns are the columns to encode, every column when empty. A Series encodes
	// itself and does not read them.
	Columns []string

	// Separator joins a column's name to a value's, "_" when empty.
	Separator string

	// DropFirst leaves out the first value to appear that is not null, which the
	// others imply, and DropNulls the null.
	DropFirst, DropNulls bool
}

// ToDummies replaces each of Columns with a Uint8 column for each of its distinct
// values, 1 where a row holds the value and 0 where it does not: Polars'
// to_dummies. A null is a value, NaN is one, and -0.0 is 0.0.
//
// A dummy is named the column, the separator and the value as Polars displays it:
// text as itself, 1.0 as "1.0", a date "2024-01-02", an instant "2024-01-02
// 03:04:05.120" and a zoned one with its zone's abbreviation after, a duration
// "1m 30s", a null "null". A column's dummies stand in its place, ordered by name,
// as Polars orders them, so 10 comes before 2. A column of lists or structs is
// refused, as a group key of one is.
//
//	df.ToDummies(ctx, ursus.DummiesOptions{Columns: []string{"color"}}) // color_blue, color_red
func (df *DataFrame) ToDummies(ctx context.Context, o DummiesOptions) (_ *DataFrame, err error) {
	defer uerr.Catch(&err, "to_dummies")
	encode := make(map[string]bool, len(o.Columns))
	for _, name := range o.Columns {
		if !df.batch.Schema().Has(name) {
			return nil, uerr.UnknownColumn("to_dummies", name, df.Columns())
		}
		encode[name] = true
	}
	var out []*data.Column
	for _, c := range df.batch.Columns() {
		if len(encode) > 0 && !encode[c.Name()] {
			out = append(out, c)
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cols, err := dummies(c, o)
		if err != nil {
			return nil, err
		}
		out = append(out, cols...)
	}
	return framed(out, df.Height())
}

// ToDummies is the series' dummy columns, as DataFrame.ToDummies makes a column's:
// Polars' Series.to_dummies.
func (s *Series[T]) ToDummies(ctx context.Context, o DummiesOptions) (_ *DataFrame, err error) {
	defer uerr.Catch(&err, "to_dummies")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cols, err := dummies(s.Column(), o)
	if err != nil {
		return nil, err
	}
	return framed(cols, s.Len())
}

// framed is a frame of these columns, of no rows when there are none, as Polars'
// frame of no columns is.
func framed(cols []*data.Column, rows int) (*DataFrame, error) {
	if len(cols) == 0 {
		rows = 0
	}
	return frameOfColumns(cols, rows)
}

// dummies is a column's dummy columns, ordered by name. Its values are numbered as a
// group-by numbers its groups, and each row's 1 is written to its value's column, so
// the cost is a pass over the rows and the zeroed columns, whatever the number of
// values.
func dummies(c *data.Column, o DummiesOptions) ([]*data.Column, error) {
	sep := cmp.Or(o.Separator, "_")
	ids, firsts, err := kernel.FirstSeenIDs("to_dummies", c)
	if err != nil {
		return nil, err
	}
	text, err := polarsText(c)
	if err != nil {
		return nil, err
	}
	type dummy struct {
		id   int32
		name string
	}
	valid := c.Validity()
	kept := make([]dummy, 0, len(firsts))
	first := o.DropFirst
	for id, row := range firsts {
		null := c.DType().IsNull() || !valid.Get(int(row))
		switch {
		case null && o.DropNulls:
			continue
		case !null && first:
			first = false
			continue
		}
		name := "null"
		if !null {
			name = text(int(row))
		}
		kept = append(kept, dummy{int32(id), c.Name() + sep + name})
	}
	slices.SortFunc(kept, func(a, b dummy) int { return strings.Compare(a.name, b.name) })

	// A dummy is a byte a row. Past the default memory budget the dummies cannot
	// fit, and an error is kinder than the process killed.
	n := c.Len()
	if need, lim := int64(n)*int64(len(kept)), execopt.DefaultLimit(); lim > 0 && need > lim {
		return nil, uerr.New(uerr.KindResource, "to_dummies",
			"%q has %d values to encode, whose dummies of %d rows would take %d bytes, past the memory limit of %d",
			c.Name(), len(kept), n, need, lim).Hint("encode fewer values: cut or group the column first")
	}
	// The rows by value, a counting sort, so that each dummy writes its own rows in
	// order; then the dummies are made in parallel, each zeroed and written by one
	// goroutine.
	start := make([]int32, len(firsts)+1)
	for _, id := range ids {
		start[id+1]++
	}
	for i := range firsts {
		start[i+1] += start[i]
	}
	rows, next := make([]int32, n), slices.Clone(start[:len(firsts)])
	for i, id := range ids {
		rows[next[id]] = int32(i)
		next[id]++
	}
	cols := make([]*data.Column, len(kept))
	var (
		wg    sync.WaitGroup
		relay uerr.Relay
		took  atomic.Int64
	)
	for range min(runtime.GOMAXPROCS(0), len(kept)) {
		relay.Go(&wg, func() {
			for k := int(took.Add(1)) - 1; k < len(kept); k = int(took.Add(1)) - 1 {
				d := kept[k]
				vals := make([]uint8, n)
				for _, r := range rows[start[d.id]:start[d.id+1]] {
					vals[r] = 1
				}
				cols[k] = data.NewFixedOwned(d.name, dtype.Uint8, vals, bitmap.AllSet(n))
			}
		})
	}
	wg.Wait()
	relay.Raise()
	return cols, nil
}

// polarsText gives the text Polars displays a value of c as, its AnyValue's
// Display, which names a dummy: text itself, a float by polarsFloat, an instant as
// chrono writes one, a duration by polarsDuration. It reads valid rows only.
func polarsText(c *data.Column) (func(row int) string, error) {
	dt := c.DType()
	if dt.IsNull() || c.IsPayloadFree() {
		return func(int) string { return "null" }, nil
	}
	switch dt.ID() {
	case dtype.TypeBool:
		bits := c.Bools()
		return func(i int) string { return strconv.FormatBool(bits.Get(i)) }, nil
	case dtype.TypeString, dtype.TypeCategorical:
		return c.Strings().Get, nil
	case dtype.TypeBinary:
		strs := c.Strings()
		return func(i int) string { return binaryText(strs.Bytes(i)) }, nil
	case dtype.TypeEnum:
		cats := dt.Categories()
		return textOf(c, func(v uint32) string { return cats[v] })
	case dtype.TypeFloat32:
		return textOf(c, func(v float32) string { return polarsFloat(float64(v)) })
	case dtype.TypeFloat64:
		return textOf(c, polarsFloat)
	case dtype.TypeDecimal:
		return textOf(c, func(v i128.Int128) string { return dtype.FormatDecimal(v.String(), dt.Scale()) })
	case dtype.TypeInt128:
		return textOf(c, func(v i128.Int128) string { return v.String() })
	case dtype.TypeInt8:
		return textOf(c, func(v int8) string { return strconv.Itoa(int(v)) })
	case dtype.TypeInt16:
		return textOf(c, func(v int16) string { return strconv.Itoa(int(v)) })
	case dtype.TypeInt32:
		return textOf(c, func(v int32) string { return strconv.Itoa(int(v)) })
	case dtype.TypeInt64:
		return textOf(c, func(v int64) string { return strconv.FormatInt(v, 10) })
	case dtype.TypeUint8:
		return textOf(c, func(v uint8) string { return strconv.Itoa(int(v)) })
	case dtype.TypeUint16:
		return textOf(c, func(v uint16) string { return strconv.Itoa(int(v)) })
	case dtype.TypeUint32:
		return textOf(c, func(v uint32) string { return strconv.FormatUint(uint64(v), 10) })
	case dtype.TypeUint64:
		return textOf(c, func(v uint64) string { return strconv.FormatUint(v, 10) })
	case dtype.TypeDate:
		return textOf(c, func(v int32) string { return instantText(dt, int64(v), nil) })
	case dtype.TypeTime:
		return textOf(c, func(v int64) string { return instantText(dt, v, nil) })
	case dtype.TypeDatetime:
		var loc *time.Location
		if tz := dt.TimeZone(); tz != "" {
			var err error
			if loc, err = time.LoadLocation(tz); err != nil {
				return nil, uerr.New(uerr.KindValue, "to_dummies", "unknown time zone %q", tz)
			}
		}
		return textOf(c, func(v int64) string { return instantText(dt, v, loc) })
	case dtype.TypeDuration:
		return textOf(c, func(v int64) string { return polarsDuration(v, dt.TimeUnit()) })
	}
	return nil, uerr.New(uerr.KindUnsupported, "to_dummies", "cannot name a dummy of a %s value", dt)
}

// textOf is polarsText for a column whose values are V's.
func textOf[V data.Fixed](c *data.Column, text func(V) string) (func(int) string, error) {
	v, err := data.Values[V](c)
	if err != nil {
		return nil, err
	}
	return func(i int) string { return text(v[i]) }, nil
}

// polarsFloat is a float as Polars displays one (polars-core's fmt_float): a whole
// number below 999999 with ".0", one of more than nine characters in scientific
// notation of four decimals when outside 0.000001 to 999999 and else to six
// decimals, trailing zeros dropped, and a short one as it is, a whole one in
// scientific notation.
func polarsFloat(v float64) string {
	fract := v - math.Trunc(v) // Rust's fract: NaN for an infinity
	if fract == 0 && math.Abs(v) < 999999 {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	if len(rustFloat(v)) > 9 {
		if a := math.Abs(v); a < 0.000001 || a > 999999 {
			return rustExp(v, 4)
		}
		s := strconv.FormatFloat(v, 'f', 6, 64)
		if !strings.HasSuffix(s, "0") {
			return s
		}
		s = strings.TrimRight(s, "0")
		if strings.HasSuffix(s, ".") {
			s += "0"
		}
		return s
	}
	if fract == 0 {
		return rustExp(v, -1)
	}
	return rustFloat(v)
}

// rustFloat is Rust's Display of a float: the shortest text that reads back as it,
// never in scientific notation, "inf" for an infinity.
func rustFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// rustExp is Rust's LowerExp of a float, prec decimals or the shortest when -1:
// "1.5000e-7", where Go writes "1.5000e-07".
func rustExp(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'e', prec, 64)
	m, e, _ := strings.Cut(s, "e")
	x, err := strconv.Atoi(e)
	if err != nil {
		return s
	}
	return m + "e" + strconv.Itoa(x)
}

// instantText is a Date, a Time or a Datetime as Polars displays one, chrono's
// Display: "2024-01-02", "03:04:05.120", "2024-01-02 03:04:05", and in a zone the
// wall clock and the zone's abbreviation, "2024-01-02 03:04:05 CET".
func instantText(dt dtype.DataType, ticks int64, loc *time.Location) string {
	t, ok := dt.ToTime(ticks)
	if !ok {
		return strconv.FormatInt(ticks, 10)
	}
	switch dt.ID() {
	case dtype.TypeDate:
		return chronoDate(t)
	case dtype.TypeTime:
		return chronoClock(t)
	}
	if loc == nil {
		return chronoDate(t) + " " + chronoClock(t)
	}
	t = t.In(loc)
	return chronoDate(t) + " " + chronoClock(t) + " " + t.Format("MST")
}

// chronoDate is a date as chrono writes one: a year outside 0 to 9999 signed and of
// at least four digits, "+10000-01-01".
func chronoDate(t time.Time) string {
	y, m, d := t.Date()
	format := "%04d-%02d-%02d"
	if y < 0 || y > 9999 {
		format = "%+05d-%02d-%02d"
	}
	return fmt.Sprintf(format, y, int(m), d)
}

// chronoClock is a time of day as chrono writes one: its fraction of a second in
// three, six or nine digits, the fewest that hold it, and none when it is whole.
func chronoClock(t time.Time) string {
	h, m, s := t.Clock()
	clock := fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	switch ns := t.Nanosecond(); {
	case ns == 0:
		return clock
	case ns%1_000_000 == 0:
		return fmt.Sprintf("%s.%03d", clock, ns/1_000_000)
	case ns%1_000 == 0:
		return fmt.Sprintf("%s.%06d", clock, ns/1_000)
	default:
		return fmt.Sprintf("%s.%09d", clock, ns)
	}
}

// polarsDuration is a duration as Polars displays one (fmt_duration_ns, _us and
// _ms): its whole days, hours, minutes and seconds, then the rest in the unit's
// largest that holds it exactly, "1d 1h 7µs", or "0" and the unit. A duration of
// seconds, which Polars has not, is written as one of milliseconds.
func polarsDuration(v int64, unit dtype.TimeUnit) string {
	const micro = "\u00b5s" // Polars' µ is U+00B5, the micro sign
	sec := int64(1_000)
	switch unit {
	case dtype.Nano:
		sec = 1_000_000_000
	case dtype.Micro:
		sec = 1_000_000
	case dtype.Second:
		if v > math.MaxInt64/1_000 || v < math.MinInt64/1_000 {
			return strconv.FormatInt(v, 10) + "s"
		}
		v *= 1_000
	}
	if v == 0 {
		switch sec {
		case 1_000_000_000:
			return "0ns"
		case 1_000_000:
			return "0" + micro
		}
		return "0ms"
	}
	sizes := [4]int64{86_400 * sec, 3_600 * sec, 60 * sec, sec}
	names := [4]string{"d", "h", "m", "s"}
	var b []byte
	for i, size := range sizes {
		whole := v / size
		if i > 0 {
			whole = v % sizes[i-1] / size
		}
		if whole != 0 {
			b = append(strconv.AppendInt(b, whole, 10), names[i]...)
			if v%size != 0 {
				b = append(b, ' ')
			}
		}
	}
	switch sec {
	case 1_000_000_000:
		switch {
		case v%1_000 != 0:
			b = append(strconv.AppendInt(b, v%sec, 10), "ns"...)
		case v%1_000_000 != 0:
			b = append(strconv.AppendInt(b, v%sec/1_000, 10), micro...)
		case v%sec != 0:
			b = append(strconv.AppendInt(b, v%sec/1_000_000, 10), "ms"...)
		}
	case 1_000_000:
		switch {
		case v%1_000 != 0:
			b = append(strconv.AppendInt(b, v%sec, 10), micro...)
		case v%sec != 0:
			b = append(strconv.AppendInt(b, v%sec/1_000, 10), "ms"...)
		}
	default:
		if v%1_000 != 0 {
			b = append(strconv.AppendInt(b, v%1_000, 10), "ms"...)
		}
	}
	return string(b)
}

// binaryText is bytes as Polars displays them, b"…" with each byte outside '!' to
// '~' escaped as \xhh, a space too.
func binaryText(v []byte) string {
	b := []byte(`b"`)
	for _, c := range v {
		if c > ' ' && c < 0x7f {
			b = append(b, c)
		} else {
			b = fmt.Appendf(b, `\x%02x`, c)
		}
	}
	return string(append(b, '"'))
}
