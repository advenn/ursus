package ursus

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// The JSON writers (step 333): Polars' write_ndjson, write_json and sink_ndjson, a
// value a type as Polars writes it.

// WriteNDJSON runs the query and writes each row to w as a JSON object on a line of
// its own, streaming, as Polars' write_ndjson: a key a column, in the columns' order.
// It does not close w. A value is written as Polars writes one:
//
//   - a number as one, a float in Polars' text, "1.0", "1e+20"; NaN and the
//     infinities null, which JSON has no word for;
//   - text quoted and escaped, its UTF-8 kept;
//   - a Decimal quoted, "1.25"; a Date "2024-01-02", a Time "03:04:05.000006", a
//     Datetime "2024-01-02 03:04:05.123456", a zoned one "2024-01-02T03:04:05+05:00";
//     a Duration in ISO 8601's seconds, "PT90S", "P0D" for none;
//   - a List an array and a Struct an object; a null null.
//
// A Binary column is refused, as Polars cannot write one either.
func (lf *LazyFrame) WriteNDJSON(ctx context.Context, w io.Writer, opts ...CollectOption) error {
	return lf.writeJSON(ctx, "write_ndjson", w, false, opts)
}

// WriteJSON runs the query and writes it to w as one JSON array of the rows' objects,
// as Polars' write_json: "[]" for no rows. See WriteNDJSON for each value's text.
func (lf *LazyFrame) WriteJSON(ctx context.Context, w io.Writer, opts ...CollectOption) error {
	return lf.writeJSON(ctx, "write_json", w, true, opts)
}

// SinkNDJSON runs the query and writes it to path as WriteNDJSON writes, streaming,
// as Polars' sink_ndjson: to a temporary name, renamed on success, so a failed query
// leaves no file half written.
//
// With WithPartitionBy or WithMaxRowsPerFile, path is a directory, and the result a
// hive dataset below it, of files named .jsonl, as Polars names them.
func (lf *LazyFrame) SinkNDJSON(ctx context.Context, path string, opts ...NDJSONSinkOption) error {
	cfg := ndjsonSinkCfg{collect: baseCollectCfg()}
	for _, o := range opts {
		o.applyNDJSONSink(&cfg)
	}
	cfg.collect.finish()
	if cfg.part.partitioned() {
		return lf.sinkPartitioned(ctx, "sink_ndjson", path, cfg.collect, cfg.part, ".jsonl", newNDJSONFile)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_ndjson", "creating a temporary file for %s", path)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // a no-op on the success path, which ends with a rename
	}()
	if err := lf.writeJSONCfg(ctx, "write_ndjson", tmp, false, cfg.collect); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_ndjson", "closing %s", tmpName)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "sink_ndjson", "renaming to %s", path)
	}
	return nil
}

// NDJSONSinkOption is an option to SinkNDJSON: a PartitionOption or a CollectOption.
// New in 0.6.8, where SinkNDJSON took CollectOptions alone.
type NDJSONSinkOption interface{ applyNDJSONSink(*ndjsonSinkCfg) }

type ndjsonSinkCfg struct {
	collect collectCfg
	part    partitionCfg
}

func (o CollectOption) applyNDJSONSink(c *ndjsonSinkCfg) { o(&c.collect) }

func (lf *LazyFrame) writeJSON(ctx context.Context, op string, w io.Writer, array bool, opts []CollectOption) error {
	cfg := baseCollectCfg()
	for _, o := range opts {
		o(&cfg)
	}
	cfg.finish()
	return lf.writeJSONCfg(ctx, op, w, array, cfg)
}

func (lf *LazyFrame) writeJSONCfg(ctx context.Context, op string, w io.Writer, array bool, cfg collectCfg) (err error) {
	defer uerr.Catch(&err, op)
	defer cfg.done()
	root, err := lf.compile(ctx, cfg)
	if err != nil {
		return err
	}
	defer root.Close()

	schema := exec.Schema(root)
	keys := make([][]byte, schema.Len())
	for j, f := range schema.All() {
		if err := jsonWritable(f.Name, f.Type); err != nil {
			return uerr.Wrap(err, uerr.KindType, op, "column %q", f.Name)
		}
		keys[j] = append(appendJSONString(nil, f.Name), ':')
	}
	sep := byte('\n') // after each row of NDJSON; between the rows of an array
	if array {
		sep = ','
	}

	// Each batch is encoded on a goroutine of its own, a few ahead, and the texts
	// written in the batches' order, so the bytes are the same however many run.
	type text struct {
		buf []byte
		err error
	}
	pending := make(chan chan text, max(2, cfg.planned()))
	written := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if p := recover(); p != nil {
				err = uerr.FromPanic(p, op) // a panic in the caller's writer
				for range pending {
				}
			}
			written <- err
		}()
		bw := bufio.NewWriterSize(w, 1<<16)
		any := false
		if array {
			_, err = bw.Write([]byte{'['})
		}
		for ch := range pending {
			t := <-ch
			switch {
			case err != nil:
			case t.err != nil:
				err = t.err
			case len(t.buf) > 0:
				if array && any {
					err = bw.WriteByte(',')
				}
				if err == nil {
					_, err = bw.Write(t.buf)
				}
				any = true
			}
		}
		if err == nil && array {
			err = bw.WriteByte(']')
		}
		if err == nil {
			err = bw.Flush()
		}
	}()
	var runErr error
	for b, err := range exec.Batches(ctx, root) {
		if err != nil {
			runErr = err
			break
		}
		ch := make(chan text, 1)
		pending <- ch
		go func() {
			defer func() {
				if p := recover(); p != nil {
					ch <- text{err: uerr.FromPanic(p, op)} // a panic in a value's text
				}
			}()
			buf, err := encodeJSONRows(b, keys, sep, array)
			ch <- text{buf, err}
		}()
	}
	close(pending)
	if err := <-written; err != nil && runErr == nil {
		var ue *uerr.Error
		if errors.As(err, &ue) {
			return err
		}
		return uerr.Wrap(err, uerr.KindIO, op, "writing")
	}
	return runErr
}

// encodeJSONRows is a batch's rows as JSON objects, each followed by sep for NDJSON
// and joined by it for an array.
func encodeJSONRows(b *data.Batch, keys [][]byte, sep byte, array bool) ([]byte, error) {
	vals := make([]jsonAppender, b.NumCols())
	for j, c := range b.Columns() {
		var err error
		if vals[j], err = jsonValues(c); err != nil {
			return nil, uerr.Wrap(err, uerr.KindType, "write_json", "column %q", c.Name())
		}
	}
	buf := make([]byte, 0, b.Rows()*(16*len(vals)+2))
	for row := range b.Rows() {
		if array && row > 0 {
			buf = append(buf, sep)
		}
		buf = append(buf, '{')
		for j, val := range vals {
			if j > 0 {
				buf = append(buf, ',')
			}
			buf = val(append(buf, keys[j]...), row)
		}
		buf = append(buf, '}')
		if !array {
			buf = append(buf, sep)
		}
	}
	return buf, nil
}

// jsonWritable refuses a type no JSON text is written for: Binary, as Polars refuses
// it, within a List or a Struct too.
func jsonWritable(name string, dt dtype.DataType) error {
	switch dt.ID() {
	case dtype.TypeBinary:
		return uerr.New(uerr.KindUnsupported, "", "%s holds Binary, which JSON has no text for", name).
			Hint("cast it to String, or leave it out")
	case dtype.TypeList, dtype.TypeArray:
		return jsonWritable(name, dt.Inner())
	case dtype.TypeStruct:
		for _, f := range dt.Fields() {
			if err := jsonWritable(name+"."+f.Name, f.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonAppender appends row's value, as JSON.
type jsonAppender func(dst []byte, row int) []byte

// jsonValues is c's appender: null where c is, the value's text elsewhere.
func jsonValues(c *data.Column) (jsonAppender, error) {
	val, err := jsonValueText(c)
	if err != nil {
		return nil, err
	}
	if c.NullCount() == 0 {
		return val, nil
	}
	return func(dst []byte, row int) []byte {
		if !c.IsValid(row) {
			return append(dst, "null"...)
		}
		return val(dst, row)
	}, nil
}

// jsonValueText is the text of c's valid values.
func jsonValueText(c *data.Column) (jsonAppender, error) {
	dt := c.DType()
	if dt.IsNull() || c.IsPayloadFree() {
		return func(dst []byte, _ int) []byte { return append(dst, "null"...) }, nil
	}
	switch dt.ID() {
	case dtype.TypeBool:
		bits := c.Bools()
		return func(dst []byte, i int) []byte { return strconv.AppendBool(dst, bits.Get(i)) }, nil
	case dtype.TypeString, dtype.TypeCategorical:
		strs := c.Strings()
		return func(dst []byte, i int) []byte { return appendJSONString(dst, strs.Get(i)) }, nil
	case dtype.TypeEnum:
		cats := dt.Categories()
		return jsonOf(c, func(dst []byte, v uint32) []byte { return appendJSONString(dst, cats[v]) })
	case dtype.TypeFloat32:
		return jsonOf(c, func(dst []byte, v float32) []byte { return appendJSONFloat(dst, float64(v), 32) })
	case dtype.TypeFloat64:
		return jsonOf(c, func(dst []byte, v float64) []byte { return appendJSONFloat(dst, v, 64) })
	case dtype.TypeDecimal:
		return jsonOf(c, func(dst []byte, v i128.Int128) []byte {
			return appendJSONString(dst, dtype.FormatDecimal(v.String(), dt.Scale()))
		})
	case dtype.TypeInt128:
		return jsonOf(c, func(dst []byte, v i128.Int128) []byte { return append(dst, v.String()...) })
	case dtype.TypeInt8:
		return jsonOf(c, func(dst []byte, v int8) []byte { return strconv.AppendInt(dst, int64(v), 10) })
	case dtype.TypeInt16:
		return jsonOf(c, func(dst []byte, v int16) []byte { return strconv.AppendInt(dst, int64(v), 10) })
	case dtype.TypeInt32:
		return jsonOf(c, func(dst []byte, v int32) []byte { return strconv.AppendInt(dst, int64(v), 10) })
	case dtype.TypeInt64:
		return jsonOf(c, func(dst []byte, v int64) []byte { return strconv.AppendInt(dst, v, 10) })
	case dtype.TypeUint8:
		return jsonOf(c, func(dst []byte, v uint8) []byte { return strconv.AppendUint(dst, uint64(v), 10) })
	case dtype.TypeUint16:
		return jsonOf(c, func(dst []byte, v uint16) []byte { return strconv.AppendUint(dst, uint64(v), 10) })
	case dtype.TypeUint32:
		return jsonOf(c, func(dst []byte, v uint32) []byte { return strconv.AppendUint(dst, uint64(v), 10) })
	case dtype.TypeUint64:
		return jsonOf(c, func(dst []byte, v uint64) []byte { return strconv.AppendUint(dst, v, 10) })
	case dtype.TypeDate:
		return jsonOf(c, func(dst []byte, v int32) []byte {
			t, _ := dt.ToTime(int64(v))
			return append(appendChronoDate(append(dst, '"'), t), '"')
		})
	case dtype.TypeTime:
		return jsonOf(c, func(dst []byte, v int64) []byte {
			t, _ := dt.ToTime(v)
			return append(appendChronoClock(append(dst, '"'), t), '"')
		})
	case dtype.TypeDatetime:
		if tz := dt.TimeZone(); tz != "" {
			loc, err := time.LoadLocation(tz)
			if err != nil {
				return nil, uerr.New(uerr.KindValue, "", "unknown time zone %q", tz)
			}
			return jsonOf(c, func(dst []byte, v int64) []byte {
				t, _ := dt.ToTime(v)
				t = t.In(loc)
				dst = appendChronoClock(append(appendChronoDate(append(dst, '"'), t), 'T'), t)
				return append(appendOffset(dst, t), '"')
			})
		}
		return jsonOf(c, func(dst []byte, v int64) []byte {
			t, _ := dt.ToTime(v)
			return append(appendChronoClock(append(appendChronoDate(append(dst, '"'), t), ' '), t), '"')
		})
	case dtype.TypeDuration:
		perSec := map[dtype.TimeUnit]int64{dtype.Nano: 1_000_000_000, dtype.Micro: 1_000_000,
			dtype.Milli: 1_000, dtype.Second: 1}[dt.TimeUnit()]
		return jsonOf(c, func(dst []byte, v int64) []byte { return appendISODuration(dst, v, perSec) })
	case dtype.TypeList, dtype.TypeArray:
		acc := c.Lists()
		elem, err := jsonValues(acc.Child())
		if err != nil {
			return nil, err
		}
		return func(dst []byte, i int) []byte {
			start, end, _ := acc.Get(i)
			dst = append(dst, '[')
			for e := start; e < end; e++ {
				if e > start {
					dst = append(dst, ',')
				}
				dst = elem(dst, int(e))
			}
			return append(dst, ']')
		}, nil
	case dtype.TypeStruct:
		fields := c.Fields()
		keys := make([][]byte, len(fields))
		vals := make([]jsonAppender, len(fields))
		for k, f := range fields {
			keys[k] = append(appendJSONString(nil, f.Name()), ':')
			var err error
			if vals[k], err = jsonValues(f); err != nil {
				return nil, err
			}
		}
		return func(dst []byte, i int) []byte {
			dst = append(dst, '{')
			for k, val := range vals {
				if k > 0 {
					dst = append(dst, ',')
				}
				dst = val(append(dst, keys[k]...), i)
			}
			return append(dst, '}')
		}, nil
	}
	return nil, uerr.New(uerr.KindUnsupported, "", "JSON has no text for %s", dt)
}

// jsonOf is jsonValueText for a column whose values are V's.
func jsonOf[V data.Fixed](c *data.Column, text func([]byte, V) []byte) (jsonAppender, error) {
	v, err := data.Values[V](c)
	if err != nil {
		return nil, err
	}
	return func(dst []byte, i int) []byte { return text(dst, v[i]) }, nil
}

// appendJSONFloat is a float in Polars' text, NaN and the infinities null.
func appendJSONFloat(dst []byte, v float64, bits int) []byte {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return append(dst, "null"...)
	}
	return kernel.AppendFloatText(dst, v, bits)
}

// appendJSONString is s quoted, as JSON quotes it: the quote, the backslash and every
// control character escaped, a byte that is not UTF-8 as U+FFFD, and everything else
// as it is, < and > and & too, where encoding/json escapes them for HTML.
func appendJSONString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		b := s[i]
		if b >= 0x20 && b != '"' && b != '\\' && b < utf8.RuneSelf {
			i++
			continue
		}
		if b >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				dst = append(append(dst, s[start:i]...), `�`...)
				i++
				start = i
				continue
			}
			i += size
			continue
		}
		dst = append(dst, s[start:i]...)
		switch b {
		case '"', '\\':
			dst = append(dst, '\\', b)
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xf])
		}
		i++
		start = i
	}
	return append(append(dst, s[start:]...), '"')
}

// appendChronoDate is chronoDate's text, appended: a year outside 0 to 9999 signed.
func appendChronoDate(dst []byte, t time.Time) []byte {
	y, m, d := t.Date()
	if y < 0 || y > 9999 {
		if y >= 0 {
			dst = append(dst, '+')
		} else {
			dst = append(dst, '-')
			y = -y
		}
		dst = appendPadded(dst, y, 4) // chrono's %+05d: the sign counts in its width
	} else {
		dst = appendPadded(dst, y, 4)
	}
	dst = appendPadded(append(dst, '-'), int(m), 2)
	return appendPadded(append(dst, '-'), d, 2)
}

// appendChronoClock is chronoClock's text, appended: the fraction in three, six or
// nine digits, the fewest that hold it, and none when the second is whole.
func appendChronoClock(dst []byte, t time.Time) []byte {
	h, m, s := t.Clock()
	dst = appendPadded(dst, h, 2)
	dst = appendPadded(append(dst, ':'), m, 2)
	dst = appendPadded(append(dst, ':'), s, 2)
	switch ns := t.Nanosecond(); {
	case ns == 0:
		return dst
	case ns%1_000_000 == 0:
		return appendPadded(append(dst, '.'), ns/1_000_000, 3)
	case ns%1_000 == 0:
		return appendPadded(append(dst, '.'), ns/1_000, 6)
	default:
		return appendPadded(append(dst, '.'), ns, 9)
	}
}

// appendOffset is t's offset from UTC, "+05:00", "-04:00", "+00:00".
func appendOffset(dst []byte, t time.Time) []byte {
	_, off := t.Zone()
	if off < 0 {
		dst, off = append(dst, '-'), -off
	} else {
		dst = append(dst, '+')
	}
	dst = appendPadded(dst, off/3600, 2)
	return appendPadded(append(dst, ':'), off%3600/60, 2)
}

// appendISODuration is a length as Polars writes one to JSON: ISO 8601 in seconds,
// "PT90S", "-PT0.5S", the fraction's trailing zeros dropped, and "P0D" for none.
func appendISODuration(dst []byte, v, perSec int64) []byte {
	if v == 0 {
		return append(dst, `"P0D"`...)
	}
	dst = append(dst, '"')
	neg := v < 0
	if neg {
		dst = append(dst, '-')
	}
	dst = append(dst, "PT"...)
	// The magnitude as unsigned, so the least Int64 has one too.
	u := uint64(v)
	if neg {
		u = -u
	}
	dst = strconv.AppendUint(dst, u/uint64(perSec), 10)
	if frac := u % uint64(perSec); frac != 0 {
		digits := len(strconv.FormatUint(uint64(perSec), 10)) - 1
		var b [20]byte
		f := strconv.AppendUint(b[:0], frac, 10)
		dst = append(dst, '.')
		for range digits - len(f) {
			dst = append(dst, '0')
		}
		for len(f) > 0 && f[len(f)-1] == '0' {
			f = f[:len(f)-1]
		}
		dst = append(dst, f...)
	}
	return append(dst, 'S', '"')
}

// appendPadded is v, not negative, in at least width digits, zeros before: written a
// digit at a time into place, where strconv's buffer was a third of an NDJSON writer's
// time over instants.
func appendPadded(dst []byte, v, width int) []byte {
	n := 1
	for p := 10; p <= v && n < 19; p *= 10 {
		n++
	}
	n = max(n, width)
	at := len(dst)
	dst = append(dst, make([]byte, n)...)
	for i := at + n - 1; i >= at; i-- {
		dst[i] = byte('0' + v%10)
		v /= 10
	}
	return dst
}
