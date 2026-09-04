// Package arrowengine runs the benchmark against arrow-go's compute kernels
// directly, with no query engine on top.
//
// This is not a competitor. arrow-go's compute package registers scalar,
// comparison and selection kernels but no aggregates, no hash aggregation and no
// join, so it cannot express 21 of the 22 PDS-H queries or any of the h2o ones.
// What it can express is PDS-H q6 — scan, conjunctive filter, one arithmetic
// expression, one total — and that is exactly the point.
//
// ursus is built on arrow-go. Running q6 through both puts a floor under the
// comparison: the gap between this row and the ursus row is what ursus's lazy
// plan, expression evaluator and batch pipeline cost over the Arrow layer they
// sit on, with the Parquet reader and the memory format held constant. Against
// duckdb or polars that difference is entangled with a different file reader, a
// different allocator and a different threading model; here it is not.
//
// The final sum is a plain Go loop over the values buffer, because arrow-go has
// no aggregate kernel to call. That is a fact about the library, and it is part
// of what the floor measures.
//
// Everything else reports `unsupported`, with the reason.
package arrowengine

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/compute"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/arrow/scalar"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"

	"ursusbench/engine"
)

func init() { engine.Register("arrowgo", Build) }

// q6Columns is everything the query touches, in the order the batch code below
// indexes them. Projection happens in the Parquet reader, as every other
// engine's optimiser would arrange.
var q6Columns = []string{"l_shipdate", "l_discount", "l_quantity", "l_extendedprice"}

// Build resolves the query. Only pdsh/q6 exists here; see the package comment.
func Build(a engine.Args) (engine.Once, error) {
	if a.Suite != "pdsh" || a.Query != "q6" {
		return nil, engine.Unsupported(
			"arrow-go compute has no aggregate, hash-aggregate or join kernels; " +
				"only pdsh/q6 (scan + filter + total) is expressible")
	}
	if a.IO != "parquet" {
		return nil, engine.Unsupported("arrow-go: this runner reads Parquet only")
	}

	path := a.TablePath("lineitem")
	return func(ctx context.Context) (engine.Answer, error) {
		revenue, err := q6(ctx, path, a.Threads)
		if err != nil {
			return nil, err
		}
		return &answer{revenue: revenue}, nil
	}, nil
}

func q6(ctx context.Context, path string, threads int) (float64, error) {
	pool := memory.NewGoAllocator()
	ctx = compute.WithAllocator(ctx, pool)

	reader, err := file.OpenParquetFile(path, true)
	if err != nil {
		return 0, err
	}
	defer reader.Close()

	arrowReader, err := pqarrow.NewFileReader(reader, pqarrow.ArrowReadProperties{
		BatchSize: 8192,
		Parallel:  threads > 1,
	}, pool)
	if err != nil {
		return 0, err
	}

	indices, err := columnIndices(arrowReader, q6Columns)
	if err != nil {
		return 0, err
	}

	records, err := arrowReader.GetRecordReader(ctx, indices, nil)
	if err != nil {
		return 0, err
	}
	defer records.Release()

	lo := scalar.NewDate32Scalar(dateOf(1994, time.January, 1))
	hi := scalar.NewDate32Scalar(dateOf(1995, time.January, 1))

	total := 0.0
	for {
		record, err := records.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		batch, err := q6Batch(ctx, record, lo, hi)
		record.Release()
		if err != nil {
			return 0, err
		}
		total += batch
	}
	return total, nil
}

func q6Batch(ctx context.Context, record arrow.Record, lo, hi scalar.Scalar) (float64, error) {
	shipdate, discount := record.Column(0), record.Column(1)
	quantity, price := record.Column(2), record.Column(3)

	keep, err := conjunction(ctx,
		comparison(ctx, "greater_equal", shipdate, lo),
		comparison(ctx, "less", shipdate, hi),
		comparison(ctx, "greater_equal", discount, scalar.NewFloat64Scalar(0.05)),
		comparison(ctx, "less_equal", discount, scalar.NewFloat64Scalar(0.07)),
		comparison(ctx, "less", quantity, scalar.NewFloat64Scalar(24)),
	)
	if err != nil {
		return 0, err
	}
	defer keep.Release()

	// Zero value is DropNulls, which is SQL's WHERE rule and what every other
	// engine here applies. The constants live in an internal package, so the
	// zero value is also the only one nameable from outside arrow-go.
	options := compute.FilterOptions{}
	selectedPrice, err := compute.FilterArray(ctx, price, keep, options)
	if err != nil {
		return 0, err
	}
	defer selectedPrice.Release()

	selectedDiscount, err := compute.FilterArray(ctx, discount, keep, options)
	if err != nil {
		return 0, err
	}
	defer selectedDiscount.Release()

	product, err := compute.Multiply(ctx, compute.ArithmeticOptions{NoCheckOverflow: true},
		compute.NewDatumWithoutOwning(selectedPrice),
		compute.NewDatumWithoutOwning(selectedDiscount))
	if err != nil {
		return 0, err
	}
	defer product.Release()

	values, ok := product.(*compute.ArrayDatum)
	if !ok {
		return 0, fmt.Errorf("multiply returned %T, want an array", product)
	}
	column := values.MakeArray()
	defer column.Release()

	floats, ok := column.(*array.Float64)
	if !ok {
		return 0, fmt.Errorf("multiply produced %s, want float64", column.DataType())
	}
	// No aggregate kernel exists, so this is the sum: a bare loop over the
	// values buffer. It is the fastest thing arrow-go can do and the floor the
	// rest of the report is measured against.
	sum := 0.0
	for _, v := range floats.Float64Values() {
		sum += v
	}
	return sum, nil
}

type pending struct {
	datum compute.Datum
	err   error
}

func comparison(ctx context.Context, fn string, values arrow.Array, bound scalar.Scalar) pending {
	out, err := compute.CallFunction(ctx, fn, nil,
		compute.NewDatumWithoutOwning(values), compute.NewDatum(bound))
	return pending{datum: out, err: err}
}

// conjunction ANDs the predicates together and hands back the mask.
func conjunction(ctx context.Context, parts ...pending) (arrow.Array, error) {
	var combined compute.Datum
	for _, part := range parts {
		if part.err != nil {
			if combined != nil {
				combined.Release()
			}
			return nil, part.err
		}
		if combined == nil {
			combined = part.datum
			continue
		}
		next, err := compute.CallFunction(ctx, "and_kleene", nil, combined, part.datum)
		combined.Release()
		part.datum.Release()
		if err != nil {
			return nil, err
		}
		combined = next
	}
	defer combined.Release()

	values, ok := combined.(*compute.ArrayDatum)
	if !ok {
		return nil, fmt.Errorf("filter mask is %T, want an array", combined)
	}
	return values.MakeArray(), nil
}

func columnIndices(reader *pqarrow.FileReader, want []string) ([]int, error) {
	schema, err := reader.Schema()
	if err != nil {
		return nil, err
	}
	indices := make([]int, 0, len(want))
	for _, name := range want {
		found := schema.FieldIndices(name)
		if len(found) == 0 {
			return nil, fmt.Errorf("column %q is not in the file", name)
		}
		indices = append(indices, found[0])
	}
	return indices, nil
}

func dateOf(year int, month time.Month, day int) arrow.Date32 {
	return arrow.Date32FromTime(time.Date(year, month, day, 0, 0, 0, 0, time.UTC))
}

// answer holds a single number: q6 returns one row with one column.
type answer struct{ revenue float64 }

func (a *answer) Rows() int { return 1 }

func (a *answer) WriteFrame(ctx context.Context, path string) error {
	pool := memory.NewGoAllocator()
	schema := arrow.NewSchema(
		[]arrow.Field{{Name: "revenue", Type: arrow.PrimitiveTypes.Float64}}, nil)
	builder := array.NewRecordBuilder(pool, schema)
	defer builder.Release()

	builder.Field(0).(*array.Float64Builder).Append(a.revenue)
	record := builder.NewRecord()
	defer record.Release()

	return engine.WriteRecord(path, record)
}

func (a *answer) Checksum(ctx context.Context, cols []string) (map[string]float64, error) {
	return nil, engine.Unsupported("arrow-go: no checksum-mode query is implemented")
}
