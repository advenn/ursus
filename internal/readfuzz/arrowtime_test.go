package readfuzz_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/bitutil"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
)

// TestArrowTimesOutsideTheDay: an Arrow Time32 or Time64 is a time of day, and every
// way into ursus reads one exactly as arrow-go holds it and refuses one outside
// [0, 24h), negative or from midnight on, as corrupt, naming the column, the row of its
// record and the value (step 525). Import wrapped them into the day, so 26:00 read as
// 02:00, which renders as a time of day, sorts after 23:00 and equals no 02:00, as
// Parquet's checkTimes says (step 510); CI's fuzzing of FuzzScanIPCFile found it, the
// seed time64-past-its-day.
//
// A generated differential against arrow-go's arrays, the oracle: records of a
// Time32(s), Time32(ms), Time64(us) or Time64(ns) column at the top level, in a List,
// in a Struct of two and in a List of such Structs; each record a slice of a larger
// one; nulls whose slots, list ranges and struct fields hold times outside the day,
// which are no value and refused by no reader, as are the rows past a slice. Each is
// read by ScanArrowRecords, ScanArrow, ScanIPCBytes and ScanIPC of an IPC file, and
// ScanIPCStreamBytes and ScanIPCStream of an IPC stream: a frame of arrow-go's cells
// where every value its records hold lies in the day, and otherwise an ErrValue naming
// the first, in the order of the records, their rows, a row's elements and a struct's
// fields.
func TestArrowTimesOutsideTheDay(t *testing.T) {
	units := []arrow.DataType{
		arrow.FixedWidthTypes.Time32s, arrow.FixedWidthTypes.Time32ms,
		arrow.FixedWidthTypes.Time64us, arrow.FixedWidthTypes.Time64ns,
	}
	pair := func(tm arrow.DataType) arrow.DataType {
		return arrow.StructOf(arrow.Field{Name: "a", Type: tm, Nullable: true},
			arrow.Field{Name: "b", Type: tm, Nullable: true})
	}
	shapes := []struct {
		name string
		of   func(arrow.DataType) arrow.DataType
	}{
		{"top level", func(tm arrow.DataType) arrow.DataType { return tm }},
		{"in a List", func(tm arrow.DataType) arrow.DataType { return arrow.ListOf(tm) }},
		{"in a Struct", pair},
		{"in a List of Structs", func(tm arrow.DataType) arrow.DataType { return arrow.ListOf(pair(tm)) }},
	}
	const cases = 24
	dir := t.TempDir()
	for ui, tm := range units {
		perDay := ticksPerDay(tm)
		for si, shape := range shapes {
			dt := shape.of(tm)
			label := fmt.Sprintf("%s %s", tm, shape.name)
			var read, refused, readPastJunk int
			for k := range cases {
				g := &timeGen{
					rng:    rand.New(rand.NewPCG(525, uint64(ui*1000+si*100+k))),
					perDay: perDay,
					wide:   tm.ID() == arrow.TIME64,
				}
				switch k % 3 {
				case 0: // in the day, junk where no value is
					g.junk = true
				case 1: // in the day everywhere
				case 2: // some values outside the day
					g.junk, g.badRate = true, 0.15
				}
				schema := arrow.NewSchema([]arrow.Field{{Name: "t", Type: dt, Nullable: true}}, nil)
				recs := make([]arrow.RecordBatch, 1+g.rng.IntN(3))
				for r := range recs {
					recs[r] = g.record(t, schema, 1+g.rng.IntN(12))
				}
				want := firstOutside(recs, perDay)

				name := fmt.Sprintf("%s, case %d", label, k)
				file, stream := writeRecords(t, true, recs), writeRecords(t, false, recs)
				filePath := filepath.Join(dir, fmt.Sprintf("t%d-%d-%d.arrow", ui, si, k))
				streamPath := filepath.Join(dir, fmt.Sprintf("t%d-%d-%d.arrows", ui, si, k))
				if err := os.WriteFile(filePath, file, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(streamPath, stream, 0o600); err != nil {
					t.Fatal(err)
				}
				for _, entry := range []struct {
					how      string
					lf       *ursus.LazyFrame
					numbered bool // the error names the record by its place
				}{
					{"ScanArrowRecords", ursus.ScanArrowRecords(recs...), true},
					{"ScanArrow", ursus.ScanArrow(func() (array.RecordReader, error) {
						return array.NewRecordReader(schema, recs)
					}), false},
					{"ScanIPCBytes", ursus.ScanIPCBytes(file, "t.arrow"), false},
					{"ScanIPC", ursus.ScanIPC(filePath), false},
					{"ScanIPCStreamBytes", ursus.ScanIPCStreamBytes(stream, "t.arrows"), false},
					{"ScanIPCStream", ursus.ScanIPCStream(streamPath), false},
				} {
					df, err := entry.lf.Collect(t.Context())
					if want.found {
						if err == nil {
							t.Fatalf("%s, %s: record %d's row %d holds %d, outside [0, %d), and was read",
								name, entry.how, want.record, want.row, want.tick, perDay)
						}
						says := []string{`column "t"`, fmt.Sprintf("%d in row %d of its record", want.tick, want.row)}
						if entry.numbered {
							says = append(says, fmt.Sprintf("record %d:", want.record))
						}
						for _, s := range says {
							if !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), s) {
								t.Fatalf("%s, %s: want an ErrValue saying %q, got %v", name, entry.how, s, err)
							}
						}
						continue
					}
					if err != nil {
						t.Fatalf("%s, %s: every time lies in the day, and the read failed: %v", name, entry.how, err)
					}
					if why := sameCells(df, recs); why != "" {
						t.Fatalf("%s, %s: %s", name, entry.how, why)
					}
				}
				switch {
				case want.found:
					refused++
				case g.junkN > 0:
					readPastJunk++
				default:
					read++
				}
			}
			if read == 0 || refused == 0 || readPastJunk == 0 {
				t.Errorf("%s: %d cases read, %d refused and %d read past a time outside the day where "+
					"no value is: a kind of case never ran", label, read, refused, readPastJunk)
			}
			t.Logf("%s: %d cases read, %d read past a time outside the day where no value is, %d refused",
				label, read, readPastJunk, refused)
		}
	}
}

// ticksPerDay is a day in tm's unit, by package time.
func ticksPerDay(tm arrow.DataType) int64 {
	var u arrow.TimeUnit
	switch tt := tm.(type) {
	case *arrow.Time32Type:
		u = tt.Unit
	case *arrow.Time64Type:
		u = tt.Unit
	}
	return int64(24 * time.Hour / u.Multiplier())
}

// timeGen draws the arrays of a case.
type timeGen struct {
	rng     *rand.Rand
	perDay  int64
	wide    bool    // a Time64, whose values are int64s, not int32s
	badRate float64 // the chance a value lies outside the day
	junk    bool    // whether a slot holding no value holds one outside the day, half of them
	junkN   int     // the slots holding no value given one outside the day
}

// tick is a time for a slot holding a value, or holding none (a null's slot, a null
// list's range, a null struct's field, a row past a record).
func (g *timeGen) tick(value bool) int64 {
	if value && g.rng.Float64() < g.badRate {
		return g.outside()
	}
	if !value && g.junk && g.rng.IntN(2) == 0 {
		g.junkN++
		return g.outside()
	}
	switch g.rng.IntN(4) {
	case 0:
		return 0
	case 1:
		return g.perDay - 1
	}
	return g.rng.Int64N(g.perDay)
}

// outside is a time outside the day that the column's integers hold: its ends, the
// day's, and anywhere between.
func (g *timeGen) outside() int64 {
	lo, hi := int64(math.MinInt32), int64(math.MaxInt32)
	if g.wide {
		lo, hi = math.MinInt64, math.MaxInt64
	}
	switch g.rng.IntN(6) {
	case 0:
		return -1
	case 1:
		return g.perDay
	case 2:
		return lo
	case 3:
		return hi
	case 4:
		return g.perDay + g.rng.Int64N(hi-g.perDay)
	}
	return -1 - g.rng.Int64N(-(lo + 1))
}

// record is n rows of schema's one column, a slice of a record with up to three rows
// before them and after, which hold no value of the slice's.
func (g *timeGen) record(t *testing.T, schema *arrow.Schema, n int) arrow.RecordBatch {
	t.Helper()
	pre, post := g.rng.IntN(4), g.rng.IntN(4)
	none := make([]bool, pre+n+post)
	for i := range none {
		none[i] = i < pre || i >= pre+n
	}
	col := g.array(t, schema.Field(0).Type, none)
	if err := array.ValidateFull(col); err != nil {
		t.Fatalf("the fixture is not legal Arrow, so it proves nothing: %v", err)
	}
	full := array.NewRecordBatch(schema, []arrow.Array{col}, int64(len(none)))
	return full.NewSlice(int64(pre), int64(pre+n))
}

// array is an array of dt, a row for each of none, true where the row holds no value
// whatever its own validity says: a null parent's, or past the record.
func (g *timeGen) array(t *testing.T, dt arrow.DataType, none []bool) arrow.Array {
	t.Helper()
	n := len(none)
	valid := make([]bool, n)
	for i := range valid {
		valid[i] = g.rng.Float64() >= 0.2
	}
	var children []arrow.ArrayData
	var values *memory.Buffer
	switch tt := dt.(type) {
	case *arrow.Time32Type:
		v := make([]int32, n)
		for i := range v {
			v[i] = int32(g.tick(valid[i] && !none[i]))
		}
		values = memory.NewBufferBytes(arrow.Int32Traits.CastToBytes(v))
	case *arrow.Time64Type:
		v := make([]int64, n)
		for i := range v {
			v[i] = g.tick(valid[i] && !none[i])
		}
		values = memory.NewBufferBytes(arrow.Int64Traits.CastToBytes(v))
	case *arrow.ListType:
		// A null row owns elements too, as Arrow lets it.
		offs := make([]int32, n+1)
		var elems []bool
		for i := range n {
			k := g.rng.IntN(4)
			for range k {
				elems = append(elems, none[i] || !valid[i])
			}
			offs[i+1] = offs[i] + int32(k)
		}
		values = memory.NewBufferBytes(arrow.Int32Traits.CastToBytes(offs))
		children = []arrow.ArrayData{g.array(t, tt.Elem(), elems).Data()}
	case *arrow.StructType:
		// A null row's fields are valid as often as any, as Arrow lets them be.
		under := make([]bool, n)
		for i := range under {
			under[i] = none[i] || !valid[i]
		}
		for _, f := range tt.Fields() {
			children = append(children, g.array(t, f.Type, under).Data())
		}
	default:
		t.Fatalf("no generator of %s", dt)
	}
	bits := make([]byte, bitutil.BytesForBits(int64(n)))
	nulls := 0
	for i, ok := range valid {
		if ok {
			bitutil.SetBit(bits, i)
		} else {
			nulls++
		}
	}
	bufs := []*memory.Buffer{memory.NewBufferBytes(bits)}
	if values != nil {
		bufs = append(bufs, values)
	}
	d := array.NewData(dt, n, bufs, children, nulls, 0)
	defer d.Release()
	return array.MakeFromData(d)
}

// outsideAt is the first value outside the day that row i of arr holds, in the order
// of a list's elements and a struct's fields, arrow-go's reading; a null holds none.
func outsideAt(arr arrow.Array, i int, perDay int64) (int64, bool) {
	if arr.IsNull(i) {
		return 0, false
	}
	switch a := arr.(type) {
	case *array.Time32:
		v := int64(a.Value(i))
		return v, v < 0 || v >= perDay
	case *array.Time64:
		v := int64(a.Value(i))
		return v, v < 0 || v >= perDay
	case *array.Struct:
		for k := range a.NumField() {
			if v, ok := outsideAt(a.Field(k), i, perDay); ok {
				return v, true
			}
		}
	case array.ListLike:
		lo, hi := a.ValueOffsets(i)
		for e := lo; e < hi; e++ {
			if v, ok := outsideAt(a.ListValues(), int(e), perDay); ok {
				return v, true
			}
		}
	}
	return 0, false
}

// outsideTime is where recs first hold a value outside the day: record, row and tick.
type outsideTime struct {
	found       bool
	record, row int
	tick        int64
}

func firstOutside(recs []arrow.RecordBatch, perDay int64) outsideTime {
	for r, rec := range recs {
		for i := range int(rec.NumRows()) {
			if v, ok := outsideAt(rec.Column(0), i, perDay); ok {
				return outsideTime{true, r, i, v}
			}
		}
	}
	return outsideTime{}
}

// sameCells says how df differs from recs read one after another, cell by cell as
// arrow-go's arrays hold them (cellOf): "" for not at all.
func sameCells(df *ursus.DataFrame, recs []arrow.RecordBatch) string {
	row := 0
	for r, rec := range recs {
		for i := range int(rec.NumRows()) {
			if row >= df.Height() {
				return fmt.Sprintf("%d rows, fewer than the records hold", df.Height())
			}
			got, err := df.Row(row)
			if err != nil {
				return err.Error()
			}
			if want := cellOf(rec.Column(0), i); !sameValue(got[0], want) {
				return fmt.Sprintf("row %d (record %d's row %d): ursus reads %#v, and arrow-go holds %#v",
					row, r, i, got[0], want)
			}
			row++
		}
	}
	if row != df.Height() {
		return fmt.Sprintf("%d rows, and the records hold %d", df.Height(), row)
	}
	return ""
}

// writeRecords is recs written as an IPC file or stream, a batch each.
func writeRecords(t *testing.T, file bool, recs []arrow.RecordBatch) []byte {
	t.Helper()
	var buf bytes.Buffer
	opt := ipc.WithSchema(recs[0].Schema())
	var w interface {
		Write(arrow.RecordBatch) error
		Close() error
	}
	if file {
		fw, err := ipc.NewFileWriter(&buf, opt)
		if err != nil {
			t.Fatal(err)
		}
		w = fw
	} else {
		w = ipc.NewWriter(&buf, opt)
	}
	for _, rec := range recs {
		if err := w.Write(rec); err != nil {
			t.Fatalf("writing %s: %v", rec.Schema(), err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
