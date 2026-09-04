package engine

import (
	"context"
	"fmt"
	"math"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// SimpleAnswer adapts an answer held as plain Go slices to the harness.
//
// The Arrow-native engines write their results directly; the ones that hand
// back ordinary slices — gota, qframe — use this instead of each growing its own
// Parquet writer and its own idea of what summing a column means.
//
// Columns must be []string, []int64 or []float64, and every column must have
// the same length.
type SimpleAnswer struct {
	Names   []string
	Columns map[string]any
}

func (s *SimpleAnswer) Rows() int {
	for _, name := range s.Names {
		switch values := s.Columns[name].(type) {
		case []string:
			return len(values)
		case []int64:
			return len(values)
		case []float64:
			return len(values)
		}
	}
	return 0
}

func (s *SimpleAnswer) WriteFrame(ctx context.Context, path string) error {
	fields := make([]arrow.Field, 0, len(s.Names))
	for _, name := range s.Names {
		var dt arrow.DataType
		switch s.Columns[name].(type) {
		case []string:
			dt = arrow.BinaryTypes.String
		case []int64:
			dt = arrow.PrimitiveTypes.Int64
		case []float64:
			dt = arrow.PrimitiveTypes.Float64
		default:
			return fmt.Errorf("column %q has unsupported type %T", name, s.Columns[name])
		}
		fields = append(fields, arrow.Field{Name: name, Type: dt, Nullable: true})
	}

	pool := memory.NewGoAllocator()
	builder := array.NewRecordBuilder(pool, arrow.NewSchema(fields, nil))
	defer builder.Release()

	for i, name := range s.Names {
		switch values := s.Columns[name].(type) {
		case []string:
			builder.Field(i).(*array.StringBuilder).AppendValues(values, nil)
		case []int64:
			builder.Field(i).(*array.Int64Builder).AppendValues(values, nil)
		case []float64:
			// NaN means "no value" for these engines, which is what a sum over
			// an empty group produces. Written as null so it means the same
			// thing to the validator as every other engine's null.
			valid := make([]bool, len(values))
			for j, v := range values {
				valid[j] = !math.IsNaN(v)
			}
			builder.Field(i).(*array.Float64Builder).AppendValues(values, valid)
		}
	}

	record := builder.NewRecord()
	defer record.Release()
	return WriteRecord(path, record)
}

func (s *SimpleAnswer) Checksum(ctx context.Context, cols []string) (map[string]float64, error) {
	sums := make(map[string]float64, len(cols))
	for _, name := range cols {
		column, ok := s.Columns[name]
		if !ok {
			return nil, fmt.Errorf("answer has no column %q", name)
		}
		total := 0.0
		switch values := column.(type) {
		case []int64:
			for _, v := range values {
				total += float64(v)
			}
		case []float64:
			for _, v := range values {
				if !math.IsNaN(v) { // nulls are skipped, as SQL sum() does
					total += v
				}
			}
		default:
			return nil, fmt.Errorf("column %q is %T, which cannot be summed", name, column)
		}
		sums[name] = total
	}
	return sums, nil
}
