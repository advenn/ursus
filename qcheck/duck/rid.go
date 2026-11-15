package duck

import (
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/qcheck/sqlgen"
)

// WithRID is rec with __rid, 0 to n-1, before its columns, and each Date32 column
// an Int32 of the same days.
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
		if f.Type.ID() == arrow.DATE32 {
			data := array.NewData(arrow.PrimitiveTypes.Int32, col.Len(), col.Data().Buffers(), nil,
				col.NullN(), col.Data().Offset())
			col = array.MakeFromData(data)
			data.Release()
			defer col.Release()
			f.Type = arrow.PrimitiveTypes.Int32
		}
		fields = append(fields, f)
		cols = append(cols, col)
	}
	return array.NewRecordBatch(arrow.NewSchema(fields, nil), cols, n)
}
