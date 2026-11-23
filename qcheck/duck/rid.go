package duck

import (
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/qcheck/sqlgen"
)

// WithRID is rec with __rid, 0 to n-1, before its columns; each Date32 column an
// Int32 of the same days (T-DATE-DAYS), and each Timestamp, Duration and Time
// column an Int64 of its ticks in its own unit, its zone dropped (T-TICKS, step
// 539): sqlgen computes on the ticks as ursus does, where DuckDB's TIMESTAMPTZ
// would hold microseconds and its INTERVAL months, days and nanoseconds.
func WithRID(rec arrow.RecordBatch) arrow.RecordBatch {
	n := rec.NumRows()
	b := array.NewInt64Builder(memory.NewGoAllocator())
	defer b.Release()
	for i := range n {
		b.Append(i)
	}
	rid := b.NewArray()
	defer rid.Release()
	fields := []arrow.Field{{Name: sqlgen.RID, Type: arrow.PrimitiveTypes.Int64}}
	cols := []arrow.Array{rid}
	for j, f := range rec.Schema().Fields() {
		col := rec.Column(j)
		switch f.Type.ID() {
		case arrow.DATE32:
			col = retype(col, arrow.PrimitiveTypes.Int32)
			defer col.Release()
			f.Type = arrow.PrimitiveTypes.Int32
		case arrow.TIMESTAMP, arrow.DURATION, arrow.TIME64:
			col = retype(col, arrow.PrimitiveTypes.Int64)
			defer col.Release()
			f.Type = arrow.PrimitiveTypes.Int64
		case arrow.TIME32:
			col = widen(col.(*array.Time32))
			defer col.Release()
			f.Type = arrow.PrimitiveTypes.Int64
		}
		fields = append(fields, f)
		cols = append(cols, col)
	}
	return array.NewRecordBatch(arrow.NewSchema(fields, nil), cols, n)
}

// retype is col's buffers read as t, a type of the same width.
func retype(col arrow.Array, t arrow.DataType) arrow.Array {
	data := array.NewData(t, col.Len(), col.Data().Buffers(), nil, col.NullN(), col.Data().Offset())
	defer data.Release()
	return array.MakeFromData(data)
}

// widen is a Time32's ticks as an Int64.
func widen(col *array.Time32) arrow.Array {
	b := array.NewInt64Builder(memory.NewGoAllocator())
	defer b.Release()
	for i := range col.Len() {
		if col.IsNull(i) {
			b.AppendNull()
			continue
		}
		b.Append(int64(col.Value(i)))
	}
	return b.NewArray()
}
