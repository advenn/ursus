package arrowin

// The copy, one builder per ursus type.
//
// # Rules every builder follows
//
// No Arrow object is created or released here, with one exception. A range of an
// array is addressed as (arr, lo, hi) — positions within arr — so there is no
// array.NewSlice, whose Release is easy to get wrong and whose construction panics on
// bad bounds. The accessors that hand back a child (ListValues, Struct.Field,
// Storage) return arrays their parent owns and must not be released. The exception
// is a dictionary's values, built from the Data with array.MakeFromData and released
// before returning, because Dictionary.Dictionary() builds its array lazily and
// WRITES it into the shared parent — two scans reading one record would race.
// NullN() writes a lazily computed count the same way, so validity is read from the
// bitmap directly.
//
// Nulls are appended in ONE canonical form, through appendNulls, whatever the source
// held in the slot: a zero, an empty string, an empty list, a struct whose every
// field is null beneath it. That single path is the normalisation the package doc
// describes, and it is why no builder copies a null slot's bytes.
//
// Foreign offsets, view headers and dictionary indices are checked before they are
// used. array.Validate checks buffer sizes and the first and last offset of each
// array; everything between is checked here, and a malformed value is an error, not
// a panic.

import (
	"math"
	"slices"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/float16"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/arrowx"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

type builder interface {
	// appendRange copies rows [lo, hi) of arr, which holds no dictionary or
	// extension encoding at its top level — appendArrow removes those.
	appendRange(arr arrow.Array, lo, hi int) error
	// appendNulls appends n null rows in the canonical form.
	appendNulls(n int)
	// reserve makes room for n more rows, so a caller that knows the count
	// allocates once.
	reserve(n int)
	finish(name string) (*data.Column, error)
}

func newBuilder(dt dtype.DataType) (builder, error) {
	switch dt.ID() {
	case dtype.TypeNull:
		return &nullBuilder{}, nil
	case dtype.TypeBool:
		return &boolBuilder{}, nil

	case dtype.TypeInt8:
		return newFixed(dt, same[int8, int8]), nil
	case dtype.TypeInt16:
		return newFixed(dt, same[int16, int16]), nil
	case dtype.TypeInt32:
		return newFixed(dt, same[int32, int32]), nil
	case dtype.TypeInt64:
		return newFixed(dt, same[int64, int64]), nil
	case dtype.TypeUint8:
		return newFixed(dt, same[uint8, uint8]), nil
	case dtype.TypeUint16:
		return newFixed(dt, same[uint16, uint16]), nil
	case dtype.TypeUint32:
		return newFixed(dt, same[uint32, uint32]), nil
	case dtype.TypeUint64:
		return newFixed(dt, same[uint64, uint64]), nil
	case dtype.TypeFloat32:
		return newFixed(dt, float32s), nil
	case dtype.TypeFloat64:
		return newFixed(dt, same[float64, float64]), nil

	case dtype.TypeDate:
		return newFixed(dt, dates), nil
	case dtype.TypeTime:
		return newFixed(dt, times), nil
	case dtype.TypeDatetime:
		return newFixed(dt, timestamps), nil
	case dtype.TypeDuration:
		return newFixed(dt, durations), nil
	case dtype.TypeDecimal, dtype.TypeInt128:
		return newFixed(dt, decimals), nil

	case dtype.TypeString, dtype.TypeBinary:
		return &bytesBuilder{dt: dt, offs: []int32{0}}, nil

	case dtype.TypeList:
		child, err := newBuilder(dt.Inner())
		if err != nil {
			return nil, err
		}
		return &listBuilder{child: child, offs: []int32{0}}, nil

	case dtype.TypeStruct:
		fs := dt.Fields()
		kids := make([]builder, len(fs))
		for i, f := range fs {
			k, err := newBuilder(f.Type)
			if err != nil {
				return nil, err
			}
			kids[i] = k
		}
		return &structBuilder{dt: dt, fields: kids}, nil
	}
	return nil, uerr.Internalf("arrow: no import builder for %s", dt)
}

// appendArrow removes a dictionary or extension encoding and hands the rest to b.
func appendArrow(b builder, arr arrow.Array, lo, hi int) error {
	if arr == nil {
		return uerr.New(uerr.KindValue, op, "an Arrow array is nil")
	}
	if lo < 0 || hi < lo || hi > arr.Len() {
		return uerr.New(uerr.KindValue, op,
			"rows [%d, %d) are outside an Arrow array of %d rows", lo, hi, arr.Len())
	}
	if lo == hi {
		return nil
	}
	switch a := arr.(type) {
	case *array.Dictionary:
		return appendDictionary(b, a, lo, hi)
	case array.ExtensionArray:
		return appendArrow(b, a.Storage(), lo, hi)
	}
	return b.appendRange(arr, lo, hi)
}

// appendDictionary decodes each valid row's index into the values it names.
//
// A null row's index is never read. Arrow leaves it unspecified, and a producer may
// leave anything there; reading it first would refuse, or worse decode, a value that
// does not exist.
func appendDictionary(b builder, d *array.Dictionary, lo, hi int) error {
	dd, ok := d.Data().Dictionary().(*array.Data)
	if !ok || dd == nil {
		return uerr.New(uerr.KindValue, op, "a dictionary array has no dictionary")
	}
	values := array.MakeFromData(dd)
	defer values.Release()

	index, err := indexReader(d.Indices())
	if err != nil {
		return err
	}
	size := int64(values.Len())

	return eachRun(d, lo, hi, func(valid bool, a, z int) error {
		if !valid {
			b.appendNulls(z - a)
			return nil
		}
		// Runs of consecutive indices become one appendRange, so a dictionary whose
		// indices happen to count up costs what a plain array does.
		start, end := int64(-1), int64(-1)
		flush := func() error {
			if start < end {
				return appendArrow(b, values, int(start), int(end))
			}
			return nil
		}
		for i := a; i < z; i++ {
			k, ok := index(i)
			if !ok || k < 0 || k >= size {
				return uerr.New(uerr.KindValue, op,
					"dictionary index at row %d is outside a dictionary of %d values", i, size)
			}
			if k != end {
				if err := flush(); err != nil {
					return err
				}
				start = k
			}
			end = k + 1
		}
		return flush()
	})
}

func indexReader(idx arrow.Array) (func(i int) (int64, bool), error) {
	signed := func(get func(int) int64) func(int) (int64, bool) {
		return func(i int) (int64, bool) { return get(i), true }
	}
	switch a := idx.(type) {
	case *array.Int8:
		return signed(func(i int) int64 { return int64(a.Value(i)) }), nil
	case *array.Int16:
		return signed(func(i int) int64 { return int64(a.Value(i)) }), nil
	case *array.Int32:
		return signed(func(i int) int64 { return int64(a.Value(i)) }), nil
	case *array.Int64:
		return signed(a.Value), nil
	case *array.Uint8:
		return signed(func(i int) int64 { return int64(a.Value(i)) }), nil
	case *array.Uint16:
		return signed(func(i int) int64 { return int64(a.Value(i)) }), nil
	case *array.Uint32:
		return signed(func(i int) int64 { return int64(a.Value(i)) }), nil
	case *array.Uint64:
		return func(i int) (int64, bool) {
			v := a.Value(i)
			return int64(v), v <= math.MaxInt64
		}, nil
	}
	return nil, uerr.New(uerr.KindValue, op, "dictionary indices are %s, not an integer type",
		idx.DataType())
}

// eachRun calls fn for each maximal run of rows in [lo, hi) of arr that share a
// validity, in order.
//
// The bitmap is read at arr's own data offset, which is where a sliced array's bit
// 0 lives. A Null-typed array has no bitmap and every row is null; any other array
// with no bitmap has no nulls.
func eachRun(arr arrow.Array, lo, hi int, fn func(valid bool, a, z int) error) error {
	if lo == hi {
		return nil
	}
	if arr.DataType().ID() == arrow.NULL {
		return fn(false, lo, hi)
	}
	bits := arr.NullBitmapBytes()
	if len(bits) == 0 {
		return fn(true, lo, hi)
	}
	off := arr.Data().Offset()
	if need := (off + hi + 7) / 8; need > len(bits) {
		return uerr.New(uerr.KindValue, op,
			"a validity bitmap of %d bytes is too short for %d rows at offset %d",
			len(bits), hi, off)
	}

	v := bitmap.NewView(bits, off+lo, hi-lo)
	runValid := v.Get(0)
	runStart, pos := lo, lo
	for w, n := range v.Words {
		all := ^uint64(0) >> (64 - n)
		if (runValid && w == all) || (!runValid && w == 0) {
			pos += n
			continue
		}
		for k := range n {
			if bit := w>>k&1 == 1; bit != runValid {
				if err := fn(runValid, runStart, pos); err != nil {
					return err
				}
				runStart, runValid = pos, bit
			}
			pos++
		}
	}
	return fn(runValid, runStart, hi)
}

// validity accumulates a validity bitmap, and returns the no-storage all-set form
// when nothing was null.
type validity struct {
	bits  *bitmap.Builder
	nulls int
}

func (v *validity) append(valid bool, n int) {
	if v.bits == nil {
		v.bits = bitmap.NewBuilder(n)
	}
	v.bits.AppendMany(valid, n)
	if !valid {
		v.nulls += n
	}
}

func (v *validity) finish(n int) bitmap.View {
	if v.nulls == 0 || v.bits == nil {
		return bitmap.AllSet(n)
	}
	return v.bits.Finish()
}

func mismatch(arr arrow.Array, dt dtype.DataType) error {
	return uerr.New(uerr.KindValue, op, "an Arrow %s array cannot be read as %s",
		arr.DataType(), dt)
}

// --- null --------------------------------------------------------------------------

type nullBuilder struct{ n int }

func (b *nullBuilder) appendRange(arr arrow.Array, lo, hi int) error {
	if arr.DataType().ID() != arrow.NULL {
		return mismatch(arr, dtype.Null)
	}
	b.n += hi - lo
	return nil
}
func (b *nullBuilder) appendNulls(n int) { b.n += n }
func (b *nullBuilder) reserve(int)       {}
func (b *nullBuilder) finish(name string) (*data.Column, error) {
	return data.NewNull(name, dtype.Null, b.n), nil
}

// --- bool --------------------------------------------------------------------------

type boolBuilder struct {
	bits  *bitmap.Builder
	valid validity
	n     int
}

func (b *boolBuilder) init(n int) {
	if b.bits == nil {
		b.bits = bitmap.NewBuilder(n)
	}
}

func (b *boolBuilder) appendRange(arr arrow.Array, lo, hi int) error {
	a, ok := arr.(*array.Boolean)
	if !ok {
		return mismatch(arr, dtype.Bool)
	}
	b.init(hi - lo)
	bufs := a.Data().Buffers()
	off := a.Data().Offset()
	var vals []byte
	if len(bufs) > 1 && bufs[1] != nil {
		vals = bufs[1].Bytes()
	}
	if need := (off + hi + 7) / 8; need > len(vals) {
		return uerr.New(uerr.KindValue, op,
			"a boolean values buffer of %d bytes is too short for %d rows at offset %d",
			len(vals), hi, off)
	}
	return eachRun(a, lo, hi, func(valid bool, x, z int) error {
		if !valid {
			b.appendNulls(z - x)
			return nil
		}
		for w, n := range bitmap.NewView(vals, off+x, z-x).Words {
			b.bits.AppendBits(w, n)
		}
		b.valid.append(true, z-x)
		b.n += z - x
		return nil
	})
}

func (b *boolBuilder) appendNulls(n int) {
	b.init(n)
	b.bits.AppendMany(false, n)
	b.valid.append(false, n)
	b.n += n
}

func (b *boolBuilder) reserve(n int) { b.init(n) }

func (b *boolBuilder) finish(name string) (*data.Column, error) {
	b.init(0)
	return data.NewBool(name, b.bits.Finish(), b.valid.finish(b.n)), nil
}

// --- fixed width -------------------------------------------------------------------

// reader returns a function copying rows [lo, hi) of arr into dst, or an error if
// arr cannot be read as dt. It is resolved once per appendRange, not per row.
type reader[T data.Fixed] func(arr arrow.Array, dt dtype.DataType) (func(dst []T, lo, hi int) error, error)

// fixedBuilder writes straight into an ursus buffer. The row count is known at every
// call site that matters — a window, a list's element span, a struct's rows — so
// reserve sizes the buffer exactly and finish wraps it with no second copy.
type fixedBuilder[T data.Fixed] struct {
	dt    dtype.DataType
	read  reader[T]
	buf   *memory.Buffer
	vals  []T // len is the rows appended; cap is the buffer's capacity
	valid validity
}

func newFixed[T data.Fixed](dt dtype.DataType, read reader[T]) *fixedBuilder[T] {
	return &fixedBuilder[T]{dt: dt, read: read}
}

func (b *fixedBuilder[T]) realloc(capRows int) {
	var zero T
	nb := arrowx.NewBuffer(capRows * int(unsafe.Sizeof(zero)))
	nv := data.Reinterpret[T](nb.Bytes())[:len(b.vals):capRows]
	copy(nv, b.vals)
	b.buf, b.vals = nb, nv
}

func (b *fixedBuilder[T]) reserve(n int) {
	if len(b.vals)+n > cap(b.vals) {
		b.realloc(len(b.vals) + n)
	}
}

// extend grows by n rows and returns them. Growth past a reservation doubles; that
// happens only when rows arrive one dictionary index at a time.
func (b *fixedBuilder[T]) extend(n int) []T {
	if len(b.vals)+n > cap(b.vals) {
		b.realloc(max(len(b.vals)+n, 2*cap(b.vals)))
	}
	old := len(b.vals)
	b.vals = b.vals[:old+n]
	return b.vals[old:]
}

func (b *fixedBuilder[T]) appendRange(arr arrow.Array, lo, hi int) error {
	read, err := b.read(arr, b.dt)
	if err != nil {
		return err
	}
	return eachRun(arr, lo, hi, func(valid bool, a, z int) error {
		if !valid {
			b.appendNulls(z - a)
			return nil
		}
		b.valid.append(true, z-a)
		return read(b.extend(z-a), a, z)
	})
}

func (b *fixedBuilder[T]) appendNulls(n int) {
	clear(b.extend(n))
	b.valid.append(false, n)
}

func (b *fixedBuilder[T]) finish(name string) (*data.Column, error) {
	n := len(b.vals)
	if b.buf == nil || cap(b.vals) != n {
		b.realloc(n)
	}
	return data.NewFixedBuffer(name, b.dt, b.buf, n, b.valid.finish(n)), nil
}

// valuesOf is every arrow-go array whose values are one fixed-width slice.
type valuesOf[S any] interface {
	arrow.Array
	Values() []S
}

// same reads an array whose values have ursus's physical representation already.
// S and T differ only for named types such as arrow.Date32, which is an int32.
func same[T data.Fixed, S any](arr arrow.Array, dt dtype.DataType) (func([]T, int, int) error, error) {
	a, ok := arr.(valuesOf[S])
	if !ok {
		return nil, mismatch(arr, dt)
	}
	v := reinterpret[S, T](a.Values())
	return func(dst []T, lo, hi int) error {
		copy(dst, v[lo:hi])
		return nil
	}, nil
}

// reinterpret views a slice of one fixed-width type as another of the same size.
func reinterpret[S, T any](s []S) []T {
	var zs S
	var zt T
	if unsafe.Sizeof(zs) != unsafe.Sizeof(zt) {
		panic("arrowin: reinterpret between types of different sizes")
	}
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice((*T)(unsafe.Pointer(unsafe.SliceData(s))), len(s))
}

func float32s(arr arrow.Array, dt dtype.DataType) (func([]float32, int, int) error, error) {
	switch a := arr.(type) {
	case *array.Float32:
		return same[float32, float32](a, dt)
	case *array.Float16:
		v := reinterpret[float16.Num, uint16](a.Values())
		return func(dst []float32, lo, hi int) error {
			for i := lo; i < hi; i++ {
				dst[i-lo] = halfToFloat32(v[i])
			}
			return nil
		}, nil
	}
	return nil, mismatch(arr, dt)
}

// halfToFloat32 decodes an IEEE 754 half, exactly, including subnormals and NaN
// payloads. float16.Num.Float32 does not handle subnormals, which is why this exists.
func halfToFloat32(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch exp {
	case 0:
		// Zero or subnormal: frac × 2^-24, exact in a float32.
		v := float32(math.Ldexp(float64(frac), -24))
		if sign != 0 {
			v = -v
		}
		if frac == 0 {
			return math.Float32frombits(sign)
		}
		return v
	case 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | frac<<13)
}

func dates(arr arrow.Array, dt dtype.DataType) (func([]int32, int, int) error, error) {
	switch a := arr.(type) {
	case *array.Date32:
		return same[int32, arrow.Date32](a, dt)
	case *array.Date64:
		const msPerDay = 86_400_000
		v := a.Values()
		return func(dst []int32, lo, hi int) error {
			for i := lo; i < hi; i++ {
				ms := int64(v[i])
				// The spec says a whole number of days, and nothing enforces it.
				// Truncating would silently move an instant to midnight.
				if ms%msPerDay != 0 {
					return uerr.New(uerr.KindValue, op,
						"date64 value %d at row %d is not a whole number of days", ms, i).
						Hint("a date64 that carries a time of day is a timestamp; " +
							"cast it to timestamp[ms] before handing it to ursus")
				}
				days := ms / msPerDay
				if days < math.MinInt32 || days > math.MaxInt32 {
					return uerr.New(uerr.KindValue, op,
						"date64 value %d at row %d is %d days from the epoch, "+
							"outside the range of an ursus Date", ms, i, days)
				}
				dst[i-lo] = int32(days)
			}
			return nil
		}, nil
	}
	return nil, mismatch(arr, dt)
}

// unitIs reports whether an Arrow time unit is dt's.
func unitIs(u arrow.TimeUnit, dt dtype.DataType) bool {
	got, err := unit(u, nil)
	return err == nil && got == dt.TimeUnit()
}

func times(arr arrow.Array, dt dtype.DataType) (func([]int64, int, int) error, error) {
	perDay, ok := dtype.TicksPerDay(dt)
	if !ok {
		return nil, uerr.Internalf("arrow: %s has no ticks per day", dt)
	}
	// Wrapped into [0, 24h), as Cast does: nothing in arrow-go range-checks a time,
	// and every ursus kernel assumes the range.
	wrap := func(v int64) int64 { return (v%perDay + perDay) % perDay }
	switch a := arr.(type) {
	case *array.Time32:
		if t, ok := a.DataType().(*arrow.Time32Type); !ok || !unitIs(t.Unit, dt) {
			return nil, mismatch(arr, dt)
		}
		v := a.Values()
		return func(dst []int64, lo, hi int) error {
			for i := lo; i < hi; i++ {
				dst[i-lo] = wrap(int64(v[i]))
			}
			return nil
		}, nil
	case *array.Time64:
		if t, ok := a.DataType().(*arrow.Time64Type); !ok || !unitIs(t.Unit, dt) {
			return nil, mismatch(arr, dt)
		}
		v := a.Values()
		return func(dst []int64, lo, hi int) error {
			for i := lo; i < hi; i++ {
				dst[i-lo] = wrap(int64(v[i]))
			}
			return nil
		}, nil
	}
	return nil, mismatch(arr, dt)
}

func timestamps(arr arrow.Array, dt dtype.DataType) (func([]int64, int, int) error, error) {
	a, ok := arr.(*array.Timestamp)
	if !ok {
		return nil, mismatch(arr, dt)
	}
	if t, ok := a.DataType().(*arrow.TimestampType); !ok || !unitIs(t.Unit, dt) {
		return nil, mismatch(arr, dt)
	}
	return same[int64, arrow.Timestamp](a, dt)
}

func durations(arr arrow.Array, dt dtype.DataType) (func([]int64, int, int) error, error) {
	a, ok := arr.(*array.Duration)
	if !ok {
		return nil, mismatch(arr, dt)
	}
	if t, ok := a.DataType().(*arrow.DurationType); !ok || !unitIs(t.Unit, dt) {
		return nil, mismatch(arr, dt)
	}
	return same[int64, arrow.Duration](a, dt)
}

// decimals reads Decimal32, Decimal64 and Decimal128 into ursus's 128-bit storage.
//
// The words are reordered: arrow's decimal128.Num is {lo, hi} and i128.Int128 is
// {Hi, Lo}, so a reinterpretation would be valid and wrong by about 2^64 — the seam
// arrowout names for the other direction.
func decimals(arr arrow.Array, dt dtype.DataType) (func([]i128.Int128, int, int) error, error) {
	p, s := int32(dt.Precision()), int32(dt.Scale())
	if dt.ID() == dtype.TypeInt128 {
		p, s = 38, 0
	}
	if t, ok := arr.DataType().(arrow.DecimalType); !ok || t.GetPrecision() != p || t.GetScale() != s {
		return nil, mismatch(arr, dt)
	}
	switch a := arr.(type) {
	case *array.Decimal32:
		v := a.Values()
		return func(dst []i128.Int128, lo, hi int) error {
			for i := lo; i < hi; i++ {
				dst[i-lo] = widen(int64(v[i]))
			}
			return nil
		}, nil
	case *array.Decimal64:
		v := a.Values()
		return func(dst []i128.Int128, lo, hi int) error {
			for i := lo; i < hi; i++ {
				dst[i-lo] = widen(int64(v[i]))
			}
			return nil
		}, nil
	case *array.Decimal128:
		v := a.Values()
		return func(dst []i128.Int128, lo, hi int) error {
			for i := lo; i < hi; i++ {
				dst[i-lo] = i128.Int128{Hi: v[i].HighBits(), Lo: v[i].LowBits()}
			}
			return nil
		}, nil
	}
	return nil, mismatch(arr, dt)
}

// widen sign-extends into 128 bits.
func widen(x int64) i128.Int128 { return i128.Int128{Hi: x >> 63, Lo: uint64(x)} }

// --- strings and binary ------------------------------------------------------------

// bytesBuilder counts in int64 and refuses before appending anything that would
// carry an offset past int32 — ursus's offsets are 32 bits, and a wrapped offset
// is a column whose rows read each other's bytes.
type bytesBuilder struct {
	dt    dtype.DataType
	offs  []int32
	chars []byte
	valid validity
}

func (b *bytesBuilder) reserve(n int) { b.offs = slices.Grow(b.offs, n) }

func (b *bytesBuilder) appendNulls(n int) {
	end := int32(len(b.chars))
	for range n {
		b.offs = append(b.offs, end)
	}
	b.valid.append(false, n)
}

func (b *bytesBuilder) appendRange(arr arrow.Array, lo, hi int) error {
	isString := b.dt.ID() == dtype.TypeString
	switch a := arr.(type) {
	case *array.String:
		if isString {
			return appendOffsets(b, a, a.ValueOffsets(), buffer(a, 2), lo, hi)
		}
	case *array.LargeString:
		if isString {
			return appendOffsets(b, a, a.ValueOffsets(), buffer(a, 2), lo, hi)
		}
	case *array.StringView:
		if isString {
			return appendViews(b, a, a.ValueHeader, lo, hi)
		}
	case *array.Binary:
		if !isString {
			return appendOffsets(b, a, a.ValueOffsets(), buffer(a, 2), lo, hi)
		}
	case *array.LargeBinary:
		if !isString {
			return appendOffsets(b, a, a.ValueOffsets(), buffer(a, 2), lo, hi)
		}
	case *array.BinaryView:
		if !isString {
			return appendViews(b, a, a.ValueHeader, lo, hi)
		}
	case *array.FixedSizeBinary:
		if !isString {
			return appendFixedBinary(b, a, lo, hi)
		}
	}
	return mismatch(arr, b.dt)
}

// buffer returns buffer k of arr's data, or nil.
func buffer(arr arrow.Array, k int) []byte {
	bufs := arr.Data().Buffers()
	if k >= len(bufs) || bufs[k] == nil {
		return nil
	}
	return bufs[k].Bytes()
}

// room refuses n more bytes when they would carry an offset past int32.
func (b *bytesBuilder) room(n int64) error {
	if int64(len(b.chars))+n > math.MaxInt32 {
		return uerr.New(uerr.KindValue, op,
			"a column's %s data would exceed 2 GiB in one batch", b.dt).
			Hint("ursus offsets are 32-bit; read the data in smaller batches")
	}
	return nil
}

// appendOffsets copies from an offsets-and-bytes layout.
//
// ValueOffsets is WINDOWED to the array's rows but its values are ABSOLUTE positions
// in the whole byte buffer — while arrow-go's ValueBytes is windowed too, starting at
// the first row's offset. Indexing ValueBytes with ValueOffsets reads the wrong bytes
// for any sliced array, so the whole buffer is read here instead.
func appendOffsets[O int32 | int64](b *bytesBuilder, arr arrow.Array, offs []O, chars []byte, lo, hi int) error {
	if len(offs) < hi+1 {
		return uerr.New(uerr.KindValue, op,
			"an offsets buffer holds %d entries, too few for %d rows", len(offs), hi)
	}
	return eachRun(arr, lo, hi, func(valid bool, a, z int) error {
		if !valid {
			b.appendNulls(z - a)
			return nil
		}
		first := int64(offs[a])
		prev := first
		for i := a + 1; i <= z; i++ {
			cur := int64(offs[i])
			if cur < prev {
				return uerr.New(uerr.KindValue, op,
					"offsets decrease at row %d (%d after %d)", i-1, cur, prev)
			}
			prev = cur
		}
		last := prev
		if first < 0 || last > int64(len(chars)) {
			return uerr.New(uerr.KindValue, op,
				"offsets [%d, %d] fall outside a data buffer of %d bytes",
				first, last, len(chars))
		}
		if err := b.room(last - first); err != nil {
			return err
		}
		base := int64(len(b.chars)) - first
		for i := a + 1; i <= z; i++ {
			b.offs = append(b.offs, int32(base+int64(offs[i])))
		}
		b.chars = append(b.chars, chars[first:last]...)
		b.valid.append(true, z-a)
		return nil
	})
}

func appendViews(b *bytesBuilder, arr arrow.Array, header func(int) *arrow.ViewHeader, lo, hi int) error {
	bufs := arr.Data().Buffers()
	return eachRun(arr, lo, hi, func(valid bool, a, z int) error {
		if !valid {
			b.appendNulls(z - a)
			return nil
		}
		for i := a; i < z; i++ {
			h := header(i)
			n := h.Len()
			if n < 0 {
				return uerr.New(uerr.KindValue, op, "a view at row %d has length %d", i, n)
			}
			var v []byte
			if h.IsInline() {
				v = h.InlineBytes()
			} else {
				k, at := int(h.BufferIndex()), int64(h.BufferOffset())
				if k < 0 || 2+k >= len(bufs) || bufs[2+k] == nil || at < 0 ||
					at+int64(n) > int64(bufs[2+k].Len()) {
					return uerr.New(uerr.KindValue, op,
						"a view at row %d points outside its data buffers", i)
				}
				v = bufs[2+k].Bytes()[at : at+int64(n)]
			}
			if err := b.room(int64(n)); err != nil {
				return err
			}
			b.chars = append(b.chars, v...)
			b.offs = append(b.offs, int32(len(b.chars)))
		}
		b.valid.append(true, z-a)
		return nil
	})
}

func appendFixedBinary(b *bytesBuilder, a *array.FixedSizeBinary, lo, hi int) error {
	t, ok := a.DataType().(*arrow.FixedSizeBinaryType)
	if !ok || t.ByteWidth < 0 {
		return mismatch(a, b.dt)
	}
	w := int64(t.ByteWidth)
	vals := buffer(a, 1)
	off := int64(a.Data().Offset())
	if (off+int64(hi))*w > int64(len(vals)) {
		return uerr.New(uerr.KindValue, op,
			"a fixed-size binary buffer of %d bytes is too short for %d rows of %d bytes",
			len(vals), off+int64(hi), w)
	}
	return eachRun(a, lo, hi, func(valid bool, x, z int) error {
		if !valid {
			b.appendNulls(z - x)
			return nil
		}
		if err := b.room(int64(z-x) * w); err != nil {
			return err
		}
		for range z - x {
			b.offs = append(b.offs, int32(int64(b.offs[len(b.offs)-1])+w))
		}
		b.chars = append(b.chars, vals[(off+int64(x))*w:(off+int64(z))*w]...)
		b.valid.append(true, z-x)
		return nil
	})
}

func (b *bytesBuilder) finish(name string) (*data.Column, error) {
	n := len(b.offs) - 1
	c := data.NewStringParts(name, b.offs, b.chars, b.valid.finish(n))
	if b.dt.ID() == dtype.TypeBinary {
		c = c.WithDType(dtype.Binary)
	}
	return c, nil
}

// --- list --------------------------------------------------------------------------

type listBuilder struct {
	offs  []int32
	child builder
	valid validity
}

func (b *listBuilder) reserve(n int) { b.offs = slices.Grow(b.offs, n) }

func (b *listBuilder) appendNulls(n int) {
	end := b.offs[len(b.offs)-1]
	for range n {
		b.offs = append(b.offs, end)
	}
	b.valid.append(false, n)
}

// appendRange reads List, LargeList, FixedSizeList, ListView, LargeListView and Map
// through the one interface they share, whose ValueOffsets already applies the
// array's own offset. The child is the WHOLE values array, addressed absolutely.
//
// Two passes: the first checks every valid row's range and sums the elements in
// int64, so a malformed or oversized range is refused before anything is allocated —
// a LargeList<Null> can declare three billion elements in a few bytes. A null row's
// range is never read, whatever it covers: Arrow lets a null list own elements, and
// ursus's list kernels assume it owns none.
func (b *listBuilder) appendRange(arr arrow.Array, lo, hi int) error {
	ll, ok := arr.(array.ListLike)
	if !ok {
		return uerr.New(uerr.KindValue, op, "an Arrow %s array cannot be read as a list",
			arr.DataType())
	}
	values := ll.ListValues()
	if values == nil {
		return uerr.New(uerr.KindValue, op, "a list array has no values")
	}
	size := int64(values.Len())

	var total int64
	err := eachRun(arr, lo, hi, func(valid bool, a, z int) error {
		if !valid {
			return nil
		}
		for i := a; i < z; i++ {
			s, e := ll.ValueOffsets(i)
			if s < 0 || e < s || e > size {
				return uerr.New(uerr.KindValue, op,
					"list row %d covers elements [%d, %d) of a child of %d", i, s, e, size)
			}
			total += e - s
		}
		return nil
	})
	if err != nil {
		return err
	}
	if int64(b.offs[len(b.offs)-1])+total > math.MaxInt32 {
		return uerr.New(uerr.KindValue, op,
			"a list column would hold more than 2^31 elements in one batch").
			Hint("ursus list offsets are 32-bit; read the data in smaller batches")
	}
	b.child.reserve(int(total))

	return eachRun(arr, lo, hi, func(valid bool, a, z int) error {
		if !valid {
			b.appendNulls(z - a)
			return nil
		}
		// Rows whose ranges abut become one child append: every row of a List, and a
		// ListView's rows wherever they happen to be laid out in order.
		start, end := int64(-1), int64(-1)
		flush := func() error {
			if start < end {
				return appendArrow(b.child, values, int(start), int(end))
			}
			return nil
		}
		cur := b.offs[len(b.offs)-1]
		for i := a; i < z; i++ {
			s, e := ll.ValueOffsets(i)
			if s != end {
				if err := flush(); err != nil {
					return err
				}
				start = s
			}
			end = e
			cur += int32(e - s)
			b.offs = append(b.offs, cur)
		}
		if err := flush(); err != nil {
			return err
		}
		b.valid.append(true, z-a)
		return nil
	})
}

func (b *listBuilder) finish(name string) (*data.Column, error) {
	child, err := b.child.finish("item")
	if err != nil {
		return nil, err
	}
	n := len(b.offs) - 1
	return data.NewList(name, b.offs, child, b.valid.finish(n)), nil
}

// --- struct ------------------------------------------------------------------------

type structBuilder struct {
	dt     dtype.DataType
	fields []builder
	valid  validity
	n      int
}

func (b *structBuilder) reserve(n int) {
	for _, f := range b.fields {
		f.reserve(n)
	}
}

// appendNulls makes every field null beneath a null struct row.
func (b *structBuilder) appendNulls(n int) {
	for _, f := range b.fields {
		f.appendNulls(n)
	}
	b.valid.append(false, n)
	b.n += n
}

// appendRange copies a field's values only for rows where the STRUCT is valid.
//
// Arrow does not require a null struct's fields to be null — arrow-go's
// StructBuilder.AppendValues leaves them valid, and so does pyarrow's
// StructArray.from_arrays with a mask — while ursus's struct.field hands back the
// field's own validity. Copying the fields as they stand would give a null struct a
// real age.
func (b *structBuilder) appendRange(arr arrow.Array, lo, hi int) error {
	st, ok := arr.(*array.Struct)
	if !ok || st.NumField() != len(b.fields) {
		return mismatch(arr, b.dt)
	}
	return eachRun(st, lo, hi, func(valid bool, a, z int) error {
		if !valid {
			b.appendNulls(z - a)
			return nil
		}
		// Field(k) is already sliced to the struct's window, so row i of the struct
		// is row i of every field.
		for k, f := range b.fields {
			if err := appendArrow(f, st.Field(k), a, z); err != nil {
				return within(err, "field %q", b.dt.Fields()[k].Name)
			}
		}
		b.valid.append(true, z-a)
		b.n += z - a
		return nil
	})
}

func (b *structBuilder) finish(name string) (*data.Column, error) {
	fs := b.dt.Fields()
	cols := make([]*data.Column, len(b.fields))
	for k, f := range b.fields {
		c, err := f.finish(fs[k].Name)
		if err != nil {
			return nil, err
		}
		cols[k] = c
	}
	return data.NewStruct(name, cols, b.valid.finish(b.n)), nil
}
