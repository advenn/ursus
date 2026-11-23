package duck

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/model"
)

// open is a DuckDB for a test, or skips it in a build without the duckdb tag: the
// laptop never links DuckDB, and ci.yml's qcheck-duckdb job runs these.
func open(t *testing.T) *DB {
	t.Helper()
	if !Supported {
		t.Skip("DuckDB is not in this build: go test -tags duckdb,duckdb_arrow, which ci.yml's qcheck-duckdb job runs")
	}
	d, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// rows runs q and returns its rows, each value as Go reads it (value).
func rows(t *testing.T, d *DB, q string) ([][]any, *arrow.Schema) {
	t.Helper()
	schema, recs, err := d.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("%s\n%v", q, err)
	}
	defer func() {
		for _, r := range recs {
			r.Release()
		}
	}()
	var out [][]any
	for _, r := range recs {
		for i := range int(r.NumRows()) {
			row := make([]any, r.NumCols())
			for j := range row {
				row[j] = value(r.Column(j), i)
			}
			out = append(out, row)
		}
	}
	return out, schema
}

// one runs q, of one row, and returns it.
func one(t *testing.T, d *DB, q string) []any {
	t.Helper()
	rs, _ := rows(t, d, q)
	if len(rs) != 1 {
		t.Fatalf("%s: %d rows", q, len(rs))
	}
	return rs[0]
}

// column runs q, of one column, and returns it.
func column(t *testing.T, d *DB, q string) []any {
	t.Helper()
	rs, _ := rows(t, d, q)
	out := make([]any, len(rs))
	for i, r := range rs {
		out[i] = r[0]
	}
	return out
}

// value is an Arrow value as Go: nil, an int64 for a signed integer or a day, a
// uint64, a float64, a bool, a string, or a *big.Int for a Decimal128 of scale 0.
func value(a arrow.Array, i int) any {
	if a.IsNull(i) {
		return nil
	}
	switch v := a.(type) {
	case *array.Int8:
		return int64(v.Value(i))
	case *array.Int16:
		return int64(v.Value(i))
	case *array.Int32:
		return int64(v.Value(i))
	case *array.Int64:
		return v.Value(i)
	case *array.Uint8:
		return uint64(v.Value(i))
	case *array.Uint16:
		return uint64(v.Value(i))
	case *array.Uint32:
		return uint64(v.Value(i))
	case *array.Uint64:
		return v.Value(i)
	case *array.Float64:
		return v.Value(i)
	case *array.Boolean:
		return v.Value(i)
	// Text is cloned: arrow-go's Value aliases the record's buffer, which DuckDB
	// frees with the record.
	case *array.String:
		return strings.Clone(v.Value(i))
	case *array.LargeString:
		return strings.Clone(v.Value(i))
	case *array.StringView:
		return strings.Clone(v.Value(i))
	case *array.Date32:
		return int64(v.Value(i))
	case *array.Decimal128:
		if dt := v.DataType().(*arrow.Decimal128Type); dt.Scale == 0 {
			return v.Value(i).BigInt()
		}
	}
	return fmt.Sprintf("%s:%s", a.DataType(), a.ValueStr(i))
}

// same is whether two Go values are the same, a float by its bits, any NaN one.
func same(a, b any) bool {
	fa, oka := a.(float64)
	fb, okb := b.(float64)
	if oka && okb {
		return math.Float64bits(fa) == math.Float64bits(fb) || math.IsNaN(fa) && math.IsNaN(fb)
	}
	ba, oka := a.(*big.Int)
	bb, okb := b.(*big.Int)
	if oka && okb {
		return ba.Cmp(bb) == 0
	}
	return a == b
}

// show renders a value for a message, a float with its bits' sign.
func show(v any) string {
	switch v := v.(type) {
	case float64:
		if v == 0 && math.Signbit(v) {
			return "-0"
		}
		return fmt.Sprint(v)
	case string:
		return fmt.Sprintf("%q", v)
	}
	return fmt.Sprint(v)
}

// col is one column of a test's table: its name, type and values, nil a null.
type col struct {
	name string
	t    model.Type
	vals []any
}

// table builds a model.Table of cols: an input a test chose, whose expected answer
// still comes from ursus, never from the test.
func table(name string, cols ...col) *model.Table {
	n := 0
	if len(cols) > 0 {
		n = len(cols[0].vals)
	}
	t := &model.Table{Name: name, Rows: n}
	fields := make([]arrow.Field, len(cols))
	for i, c := range cols {
		t.Cols = append(t.Cols, model.Column{Name: c.name, Type: c.t, Profile: model.Extreme})
		fields[i] = arrow.Field{Name: c.name, Type: gen.ArrowType(c.t), Nullable: true}
	}
	b := array.NewRecordBuilder(memory.NewGoAllocator(), arrow.NewSchema(fields, nil))
	defer b.Release()
	for i, c := range cols {
		fb := b.Field(i)
		for _, v := range c.vals {
			if v == nil {
				fb.AppendNull()
				continue
			}
			switch fb := fb.(type) {
			case *array.Int64Builder:
				fb.Append(toInt(v))
			case *array.Int32Builder:
				fb.Append(int32(toInt(v)))
			case *array.Float64Builder:
				fb.Append(v.(float64))
			case *array.StringBuilder:
				fb.Append(v.(string))
			case *array.BooleanBuilder:
				fb.Append(v.(bool))
			case *array.Date32Builder:
				fb.Append(arrow.Date32(toInt(v)))
			// The wide types (step 539): an unsigned value a uint64, a Float32 a
			// float32, a Decimal its unscaled digits, a temporal value its ticks.
			case *array.Int8Builder:
				fb.Append(int8(toInt(v)))
			case *array.Int16Builder:
				fb.Append(int16(toInt(v)))
			case *array.Uint8Builder:
				fb.Append(uint8(v.(uint64)))
			case *array.Uint16Builder:
				fb.Append(uint16(v.(uint64)))
			case *array.Uint32Builder:
				fb.Append(uint32(v.(uint64)))
			case *array.Uint64Builder:
				fb.Append(v.(uint64))
			case *array.Float32Builder:
				fb.Append(v.(float32))
			case *array.Decimal128Builder:
				u, ok := new(big.Int).SetString(v.(string), 10)
				if !ok {
					panic("not a Decimal's unscaled digits: " + v.(string))
				}
				fb.Append(decimal128.FromBigInt(u))
			case *array.TimestampBuilder:
				fb.Append(arrow.Timestamp(toInt(v)))
			case *array.DurationBuilder:
				fb.Append(arrow.Duration(toInt(v)))
			case *array.Time32Builder:
				fb.Append(arrow.Time32(toInt(v)))
			case *array.Time64Builder:
				fb.Append(arrow.Time64(toInt(v)))
			default:
				panic(fmt.Sprintf("no table column of %s", c.t))
			}
		}
	}
	t.Rec = b.NewRecordBatch()
	return t
}

func toInt(v any) int64 {
	switch v := v.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	}
	panic(fmt.Sprintf("not an integer: %T", v))
}
