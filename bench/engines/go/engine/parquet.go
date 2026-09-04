package engine

import (
	"fmt"
	"math"
	"os"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
)

// WriteChecksum writes the one-row answer summary the h2o suite is validated on:
// a row count plus the sum of each requested column, in the order requested.
//
// The schema must match what engines/py/_common.py:checksum produces, because
// validate.py compares the two by name: `rows` as int64 and `sum_<col>` as
// float64, with a NaN sum written as null so that "no rows to sum" means the
// same thing in every engine.
func WriteChecksum(path string, rows int, cols []string, sums map[string]float64) error {
	fields := make([]arrow.Field, 0, len(cols)+1)
	fields = append(fields, arrow.Field{Name: "rows", Type: arrow.PrimitiveTypes.Int64})
	for _, name := range cols {
		fields = append(fields, arrow.Field{
			Name:     "sum_" + name,
			Type:     arrow.PrimitiveTypes.Float64,
			Nullable: true,
		})
	}
	schema := arrow.NewSchema(fields, nil)

	pool := memory.NewGoAllocator()
	builder := array.NewRecordBuilder(pool, schema)
	defer builder.Release()

	builder.Field(0).(*array.Int64Builder).Append(int64(rows))
	for i, name := range cols {
		field := builder.Field(i + 1).(*array.Float64Builder)
		value, ok := sums[name]
		if !ok || math.IsNaN(value) {
			field.AppendNull()
			continue
		}
		field.Append(value)
	}

	record := builder.NewRecord()
	defer record.Release()

	return WriteRecord(path, record)
}

// WriteRecord persists an Arrow record as a Parquet file. Used by engines whose
// natural answer representation is Arrow.
func WriteRecord(path string, record arrow.Record) error {
	return WriteRecords(path, []arrow.Record{record})
}

// WriteRecords persists a sequence of records sharing one schema.
func WriteRecords(path string, records []arrow.Record) error {
	if len(records) == 0 {
		return fmt.Errorf("no records to write to %s", path)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}

	props := parquet.NewWriterProperties(parquet.WithCompression(compress.Codecs.Zstd))
	writer, err := pqarrow.NewFileWriter(
		records[0].Schema(), file, props, pqarrow.DefaultWriterProps(),
	)
	if err != nil {
		file.Close()
		return err
	}
	for _, record := range records {
		if err := writer.Write(record); err != nil {
			writer.Close()
			return err
		}
	}
	// Close writes the footer and closes the underlying writer — which is the
	// file. Closing or syncing it again here fails with "file already closed".
	return writer.Close()
}

// SumColumn totals an Arrow column as float64, skipping nulls the way SQL sum()
// does. Used by engines that hand their answer back as Arrow and need the
// checksum the h2o suite is validated on.
func SumColumn(column arrow.Array) (float64, error) {
	total := 0.0
	switch values := column.(type) {
	case *array.Int8:
		for i := 0; i < values.Len(); i++ {
			if values.IsValid(i) {
				total += float64(values.Value(i))
			}
		}
	case *array.Int16:
		for i := 0; i < values.Len(); i++ {
			if values.IsValid(i) {
				total += float64(values.Value(i))
			}
		}
	case *array.Int32:
		for i := 0; i < values.Len(); i++ {
			if values.IsValid(i) {
				total += float64(values.Value(i))
			}
		}
	case *array.Int64:
		for i := 0; i < values.Len(); i++ {
			if values.IsValid(i) {
				total += float64(values.Value(i))
			}
		}
	case *array.Uint32:
		for i := 0; i < values.Len(); i++ {
			if values.IsValid(i) {
				total += float64(values.Value(i))
			}
		}
	case *array.Uint64:
		for i := 0; i < values.Len(); i++ {
			if values.IsValid(i) {
				total += float64(values.Value(i))
			}
		}
	case *array.Float32:
		for i := 0; i < values.Len(); i++ {
			if values.IsValid(i) {
				total += float64(values.Value(i))
			}
		}
	case *array.Float64:
		for i := 0; i < values.Len(); i++ {
			if values.IsValid(i) {
				total += values.Value(i)
			}
		}
	default:
		return 0, fmt.Errorf("cannot sum a %s column", column.DataType())
	}
	return total, nil
}
