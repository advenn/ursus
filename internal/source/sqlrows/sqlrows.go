// Package sqlrows reads a database/sql result into a batch: Polars' read_database
// (step 353). A column's type is its values', their kinds met as Polars meets the
// values of a column it builds from rows, and then the type its driver names it, where
// every value fits that type.
package sqlrows

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

const op = "read_database"

// Options are what a read is told.
type Options struct {
	// Overrides types columns by name, as Polars' schema_overrides does: each value
	// is cast to its column's type, and one that does not fit it is an error.
	Overrides map[string]dtype.DataType
}

// The kinds of value a driver gives: database/sql's driver.Value types, and an
// unsigned integer past them. A value of a type of the driver's own is read by its
// Go kind, or as the text fmt writes it.
const (
	kNull uint8 = iota
	kInt
	kUint
	kFloat
	kBool
	kText
	kBytes
	kTime
	nKinds
)

// Read reads every row of rows and closes it.
func Read(rows *sql.Rows, o Options) (*data.Batch, error) {
	defer rows.Close()
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, op, "reading the result's columns")
	}
	names := make(map[string]bool, len(cts))
	for _, ct := range cts {
		if names[ct.Name()] {
			return nil, uerr.New(uerr.KindSchema, op,
				"column %q appears more than once in the result", ct.Name()).
				Hint("name each with AS in the query")
		}
		names[ct.Name()] = true
	}
	for _, name := range slices.Sorted(maps.Keys(o.Overrides)) {
		if !names[name] {
			all := make([]string, len(cts))
			for i, ct := range cts {
				all[i] = ct.Name()
			}
			return nil, uerr.UnknownColumn(op, name, all).
				Hint("a schema override names a column of the result")
		}
	}

	cols := make([]column, len(cts))
	dest := make([]any, len(cts))
	for i := range cols {
		dest[i] = &cols[i]
	}
	n := 0
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			for i := range cols {
				if cols[i].err != nil {
					return nil, uerr.Annotate(cols[i].err, op,
						fmt.Sprintf("row %d of column %q", n+1, cts[i].Name()))
				}
			}
			return nil, uerr.Wrap(err, uerr.KindIO, op, "reading row %d", n+1)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, op, "reading the result")
	}

	fields := make([]dtype.Field, len(cts))
	out := make([]*data.Column, len(cts))
	for i, ct := range cts {
		c, err := cols[i].finish(ct, n, o.Overrides)
		if err != nil {
			return nil, uerr.Annotate(err, op, fmt.Sprintf("column %q", ct.Name()))
		}
		// The driver's NOT NULL, where it says so and the values agree.
		nullable := true
		if may, ok := ct.Nullable(); ok && !may && c.NullCount() == 0 && !c.DType().IsNull() {
			nullable = false
		}
		fields[i] = dtype.Field{Name: ct.Name(), Type: c.DType(), Nullable: nullable}
		out[i] = c
	}
	sch, err := dtype.NewSchema(fields...)
	if err != nil {
		return nil, err
	}
	return data.NewBatchRows(sch, out, n), nil
}

// column is one result column's values as they are read. Each row has a kind, and its
// value is kept as its kind is: a number's, a Boolean's and an instant's wall clock in
// a word, text and bytes in one buffer, and an instant's offset from UTC. A buffer is
// made at the first row that needs it, so a column of one kind keeps no other's.
type column struct {
	kinds []uint8
	seen  [nKinds]bool
	words []uint64 // from the first row of a word's kind, a row's word
	offs  []int32  // from the first row of text or bytes, its start and every row's end
	chars []byte
	zones []int32 // from the first instant off UTC, a row's offset in seconds
	zoned bool    // an instant whose zone is not UTC
	err   error
}

// Scan is database/sql's Scanner: it reads a row's value, src, as its driver gives it.
// Bytes are the driver's only until the next row, so they are copied.
func (c *column) Scan(src any) error {
	k, w, off, err := c.value(src)
	if err != nil {
		c.err = err
		return err
	}
	r := len(c.kinds)
	c.kinds = append(c.kinds, k)
	c.seen[k] = true
	switch k {
	case kInt, kUint, kFloat, kBool, kTime:
		if c.words == nil {
			c.words = make([]uint64, r, max(2*r, 64))
		}
	case kText, kBytes:
		if c.offs == nil {
			c.offs = make([]int32, r+1, max(2*r, 64))
		}
		if int64(len(c.chars)) > data.MaxStringBytes {
			c.err = uerr.New(uerr.KindUnsupported, op,
				"the column's text passes %d bytes, the most one column holds", data.MaxStringBytes)
			return c.err
		}
	}
	if c.words != nil {
		c.words = append(c.words, w)
	}
	if c.offs != nil {
		c.offs = append(c.offs, int32(len(c.chars)))
	}
	if off != 0 && c.zones == nil {
		c.zones = make([]int32, r, max(2*r, 64))
	}
	if c.zones != nil {
		c.zones = append(c.zones, off)
	}
	return nil
}

// value is src's kind and its word, text and bytes appended to chars, and an
// instant's offset from UTC.
func (c *column) value(src any) (k uint8, w uint64, off int32, err error) {
	switch x := src.(type) {
	case nil:
		return kNull, 0, 0, nil
	case int64:
		return kInt, uint64(x), 0, nil
	case float64:
		return kFloat, math.Float64bits(x), 0, nil
	case bool:
		if x {
			return kBool, 1, 0, nil
		}
		return kBool, 0, 0, nil
	case string:
		return c.text(x), 0, 0, nil
	case []byte:
		c.chars = append(c.chars, x...)
		return kBytes, 0, 0, nil
	case time.Time:
		return c.instant(x)
	case driver.Valuer:
		v, err := x.Value()
		if err != nil {
			return 0, 0, 0, uerr.Wrap(err, uerr.KindIO, op, "a %T's value", src)
		}
		if _, again := v.(driver.Valuer); again {
			return c.text(fmt.Sprint(v)), 0, 0, nil
		}
		return c.value(v)
	}
	v := reflect.ValueOf(src)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return kInt, uint64(v.Int()), 0, nil
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return kInt, v.Uint(), 0, nil
	case reflect.Uint, reflect.Uint64, reflect.Uintptr:
		return kUint, v.Uint(), 0, nil
	case reflect.Float32, reflect.Float64:
		return kFloat, math.Float64bits(v.Float()), 0, nil
	case reflect.Bool:
		if v.Bool() {
			return kBool, 1, 0, nil
		}
		return kBool, 0, 0, nil
	case reflect.String:
		return c.text(v.String()), 0, 0, nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			c.chars = append(c.chars, v.Bytes()...)
			return kBytes, 0, 0, nil
		}
	}
	return c.text(fmt.Sprint(src)), 0, 0, nil
}

// text appends s and is its kind: text, or bytes where it is not UTF-8, which a String
// holds only.
func (c *column) text(s string) uint8 {
	c.chars = append(c.chars, s...)
	if !utf8.ValidString(s) {
		return kBytes
	}
	return kText
}

// The seconds an instant's microseconds hold, either side of 1970.
const maxSeconds = math.MaxInt64/1_000_000 - 1

// instant is t's wall clock in microseconds, and its offset from UTC. An instant in UTC
// is a wall clock, as a driver gives a timestamp of no zone; any other is in a zone.
func (c *column) instant(t time.Time) (uint8, uint64, int32, error) {
	if name, off := t.Zone(); off != 0 || (name != "UTC" && name != "") {
		c.zoned = true
	}
	_, off := t.Zone()
	sec := t.Unix() + int64(off)
	if sec < -maxSeconds || sec > maxSeconds || t.Unix() < -maxSeconds || t.Unix() > maxSeconds {
		return 0, 0, 0, uerr.New(uerr.KindValue, op,
			"the instant %s is past the years a Datetime in microseconds holds", t)
	}
	wall := sec*1_000_000 + int64(t.Nanosecond()/1000)
	return kTime, uint64(wall), int32(off), nil
}

// finish is the column's values as a column n rows long: of the type given for it,
// or else of its values' kinds met, then of the type its driver names, where every
// value fits it.
func (c *column) finish(ct *sql.ColumnType, n int, over map[string]dtype.DataType) (*data.Column, error) {
	name := ct.Name()
	if t, ok := over[name]; ok {
		return c.build(name, n, t)
	}
	var base dtype.DataType
	first := true
	for k := kInt; k < nKinds; k++ {
		if !c.seen[k] {
			continue
		}
		t := c.kindType(k)
		if first {
			base, first = t, false
			continue
		}
		m, ok := meet(base, t)
		if !ok {
			return nil, mixed(base, t)
		}
		base = m
	}
	// Text meets a number and bytes meet text, but a number does not meet bytes.
	for k := kInt; k < nKinds; k++ {
		if t := c.kindType(k); c.seen[k] && t != base && !dtype.CanCast(t, base) {
			return nil, mixed(t, base)
		}
	}
	if first {
		// No value: the driver's type, or a Null.
		t := dtype.Null
		if named := driverTypes(ct, dtype.Null, nil); len(named) > 0 {
			t = named[0]
		}
		return kernel.NullColumn(name, t, n)
	}
	col, err := c.build(name, n, base)
	if err != nil {
		return nil, err
	}
	for _, t := range driverTypes(ct, base, col) {
		if r, ok := c.refine(col, base, t); ok {
			return r, nil
		}
	}
	return col, nil
}

func mixed(a, b dtype.DataType) error {
	return uerr.New(uerr.KindType, op, "its values are %s and %s, which no one type holds", a, b).
		Hint("give the column a type with WithDatabaseSchemaOverrides, or cast it in the query")
}

// kindType is the type a kind of value is read as: an integer an Int64, an unsigned
// one past it a Uint64, a float a Float64, text a String, bytes a Binary, and an
// instant a Datetime in microseconds, in UTC where one is in a zone.
func (c *column) kindType(k uint8) dtype.DataType {
	switch k {
	case kInt:
		return dtype.Int64
	case kUint:
		return dtype.Uint64
	case kFloat:
		return dtype.Float64
	case kBool:
		return dtype.Bool
	case kText:
		return dtype.String
	case kBytes:
		return dtype.Binary
	case kTime:
		if c.zoned {
			return dtype.Datetime(dtype.Micro, "UTC")
		}
		return dtype.Datetime(dtype.Micro, "")
	}
	return dtype.Null
}

// meet is the type values of types a and b are read as together: Polars' supertype,
// and text beside bytes bytes, as Polars' is.
func meet(a, b dtype.DataType) (dtype.DataType, bool) {
	if (a == dtype.Binary && b == dtype.String) || (a == dtype.String && b == dtype.Binary) {
		return dtype.Binary, true
	}
	return dtype.Supertype(a, b)
}

// build is the column of type t: each kind's values cast to it, strictly, and each
// row its kind's.
func (c *column) build(name string, n int, t dtype.DataType) (*data.Column, error) {
	var kinds []uint8
	for k := kInt; k < nKinds; k++ {
		if c.seen[k] {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		return kernel.NullColumn(name, t, n)
	}
	var out *data.Column
	for _, k := range kinds {
		kc, err := c.kindAs(name, n, k, t, len(kinds) == 1)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = kc
			continue
		}
		mask := bitmap.NewBuilder(n)
		for _, kk := range c.kinds {
			mask.Append(kk == k)
		}
		if out, err = kernel.Select(name, data.NewBool(name, mask.Finish(), bitmap.AllSet(n)), kc, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// kindAs is the column of the rows of kind k, of type t, every other row null; alone,
// k is every row's that holds a value. An instant becomes a Date or a time of day by
// its own wall clock, and bytes become anything but text through text.
func (c *column) kindAs(name string, n int, k uint8, t dtype.DataType, alone bool) (*data.Column, error) {
	vb := bitmap.NewBuilder(n)
	for _, kk := range c.kinds {
		vb.Append(kk == k)
	}
	valid := vb.Finish()
	if k == kTime && (t.ID() == dtype.TypeDate || t.ID() == dtype.TypeTime) {
		return c.wallsAs(name, n, valid, t)
	}
	var col *data.Column
	switch k {
	case kInt:
		col = data.NewFixedOwned(name, dtype.Int64, c.ints(n), valid)
	case kUint:
		col = data.NewFixedOwned(name, dtype.Uint64, c.uints(n), valid)
	case kFloat:
		col = data.NewFixedOwned(name, dtype.Float64, c.floats(n), valid)
	case kBool:
		bits := bitmap.NewBuilder(n)
		for _, w := range c.words {
			bits.Append(w != 0)
		}
		col = data.NewBool(name, bits.Finish(), valid)
	case kText, kBytes:
		col = c.strings(name, n, k, alone, valid)
	case kTime:
		col = data.NewFixedOwned(name, c.kindType(kTime), c.instants(n), valid)
	}
	if col.DType() == t {
		return col, nil
	}
	if k == kBytes {
		// A String's cast from bytes is a relabel, so they are checked here.
		if r := notUTF8(col); r >= 0 {
			return nil, uerr.New(uerr.KindValue, op,
				"row %d holds bytes that are not UTF-8, which a %s is read from only as text", r+1, t)
		}
		if col = col.WithDType(dtype.String); t == dtype.String {
			return col, nil
		}
	}
	return kernel.Cast(name, t, true, col)
}

// notUTF8 is the first row of col, of bytes, whose value is not UTF-8, or -1.
func notUTF8(col *data.Column) int {
	sa, valid := col.Strings(), col.Validity()
	for r := range col.Len() {
		if valid.Get(r) && !utf8.ValidString(sa.Get(r)) {
			return r
		}
	}
	return -1
}

func (c *column) ints(n int) []int64 {
	out := make([]int64, n)
	for r, w := range c.words {
		if c.kinds[r] == kInt {
			out[r] = int64(w)
		}
	}
	return out
}

func (c *column) uints(n int) []uint64 {
	out := make([]uint64, n)
	for r, w := range c.words {
		if c.kinds[r] == kUint {
			out[r] = w
		}
	}
	return out
}

func (c *column) floats(n int) []float64 {
	out := make([]float64, n)
	for r, w := range c.words {
		if c.kinds[r] == kFloat {
			out[r] = math.Float64frombits(w)
		}
	}
	return out
}

// instants is each instant's microseconds since 1970 in UTC, its wall clock less its
// offset.
func (c *column) instants(n int) []int64 {
	out := make([]int64, n)
	for r, w := range c.words {
		if c.kinds[r] == kTime {
			out[r] = int64(w)
			if c.zones != nil {
				out[r] -= int64(c.zones[r]) * 1_000_000
			}
		}
	}
	return out
}

// strings is the String or Binary column of the rows of kind k. Where it is the only
// kind kept in chars, the buffers are the column's; else its rows are copied, every
// other row empty.
func (c *column) strings(name string, n int, k uint8, alone bool, valid bitmap.View) *data.Column {
	t := dtype.String
	if k == kBytes {
		t = dtype.Binary
	}
	if alone || !(c.seen[kText] && c.seen[kBytes]) {
		return data.NewStringOwned(name, c.offs, c.chars, valid).WithDType(t)
	}
	offs := make([]int32, 1, n+1)
	var chars []byte
	for r, kk := range c.kinds {
		if kk == k {
			chars = append(chars, c.chars[c.offs[r]:c.offs[r+1]]...)
		}
		offs = append(offs, int32(len(chars)))
	}
	return data.NewStringOwned(name, offs, chars, valid).WithDType(t)
}

const microsPerDay = 86_400_000_000

// wallsAs is the instants of valid's rows as a Date or a Time: the date or the time of
// day of each one's wall clock, in its own zone.
func (c *column) wallsAs(name string, n int, valid bitmap.View, t dtype.DataType) (*data.Column, error) {
	if t.ID() == dtype.TypeDate {
		days := make([]int32, n)
		for r, w := range c.words {
			if valid.Get(r) {
				days[r] = int32(floorDiv(int64(w), microsPerDay))
			}
		}
		return data.NewFixedOwned(name, dtype.Date, days, valid), nil
	}
	ticks := make([]int64, n)
	for r, w := range c.words {
		if valid.Get(r) {
			ticks[r] = (int64(w) - floorDiv(int64(w), microsPerDay)*microsPerDay) * 1000
		}
	}
	return kernel.Cast(name, t, true, data.NewFixedOwned(name, dtype.Time(dtype.Nano), ticks, valid))
}

// midnights reports whether every instant's wall clock is a midnight.
func (c *column) midnights() bool {
	for r, w := range c.words {
		if c.kinds[r] == kTime && int64(w)%microsPerDay != 0 {
			return false
		}
	}
	return true
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// refine is col, of type base, as t, the type its driver names, if every value fits t:
// a lossy cast that nulls none.
func (c *column) refine(col *data.Column, base, t dtype.DataType) (*data.Column, bool) {
	if !refines(base, t) {
		return nil, false
	}
	if base.ID() == dtype.TypeDatetime {
		switch {
		case t.ID() == dtype.TypeDate && !c.midnights():
			// Oracle's DATE holds a time of day too.
			return nil, false
		case t.ID() == dtype.TypeDate || t.ID() == dtype.TypeTime:
			out, err := c.build(col.Name(), col.Len(), t)
			return out, err == nil
		case base.TimeZone() == "":
			// A wall clock in UTC is the instant it names.
			return col.WithDType(t), true
		}
		return nil, false
	}
	from := col
	if base == dtype.Binary {
		if notUTF8(col) >= 0 {
			return nil, false
		}
		if from = col.WithDType(dtype.String); t == dtype.String {
			return from, true
		}
	}
	out, err := kernel.Cast(col.Name(), t, false, from)
	if err != nil || out.NullCount() != col.NullCount() {
		return nil, false
	}
	return out, true
}

// refines reports whether the type a driver names, t, may stand for values read as
// base: a narrower or unsigned integer, a Boolean or a Decimal for integers; a Float32
// or a Decimal for floats; a type parsed from text for text and bytes, and text for
// bytes; and a Date, a time of day or an instant in UTC for instants. A float is not
// one for integers: SQLite's NUMERIC is a float to its drivers, and holds integers as
// such, which Polars reads as Int64.
func refines(base, t dtype.DataType) bool {
	if base == t {
		return false
	}
	parsed := t.ID() == dtype.TypeDecimal || t.ID() == dtype.TypeDate || t.ID() == dtype.TypeTime ||
		t.ID() == dtype.TypeDatetime
	switch {
	case base == dtype.Int64:
		return t.IsInteger() || t == dtype.Bool || t.ID() == dtype.TypeDecimal
	case base == dtype.Uint64:
		return t.IsUnsignedInteger() || t.ID() == dtype.TypeDecimal
	case base == dtype.Float64:
		return t == dtype.Float32 || t.ID() == dtype.TypeDecimal
	case base == dtype.String:
		return parsed
	case base == dtype.Binary:
		return parsed || t == dtype.String
	case base.ID() == dtype.TypeDatetime:
		return t.ID() == dtype.TypeDate || t.ID() == dtype.TypeTime ||
			(t.ID() == dtype.TypeDatetime && t.TimeZone() == "UTC" && t.TimeUnit() == dtype.Micro)
	}
	return false
}
