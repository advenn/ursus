package compare

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// ursusEngine is the laptop's stand-in for DuckDB, which only CI links: it answers
// a query by running its tree, not its SQL, in ursus at the reference
// configuration, and hands the answer over through Arrow in the types DuckDB
// exports (toArrow), so the runner, FromArrow and the comparator run as they do
// against DuckDB.
type ursusEngine struct{ tables []*model.Table }

func (e *ursusEngine) Load(tables []*model.Table) error { e.tables = tables; return nil }

func (e *ursusEngine) tableMap() map[string]*model.Table {
	m := map[string]*model.Table{}
	for _, t := range e.tables {
		m[t.Name] = t
	}
	return m
}

func (e *ursusEngine) Answer(ctx context.Context, q *ast.Query, s *sqlgen.SQL) (*arrow.Schema, []arrow.RecordBatch, error) {
	lf := build.Frame(q.Root, build.RecordsOf(e.tables, 1))
	opts := build.Options(model.Reference())
	if q.Count {
		n, err := lf.Count(ctx, opts...)
		if err != nil {
			return nil, nil, err
		}
		return toArrow(&Answer{Names: []string{"n"}, Types: []model.Type{model.Int64}, Rows: 1,
			Cells: [][]string{{intCell(n)}}})
	}
	df, err := lf.Collect(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	info, err := ast.Schema(q.Root, e.tableMap())
	if err != nil {
		return nil, nil, err
	}
	a, err := FromUrsus(df, info.Cols)
	if err != nil {
		return nil, nil, err
	}
	return toArrow(a)
}

func (e *ursusEngine) Counts(ctx context.Context, q *ast.Query, s *sqlgen.SQL) ([]int64, error) {
	out := make([]int64, len(s.Nodes))
	for i, f := range s.Nodes {
		n, err := build.Frame(f, build.RecordsOf(e.tables, 1)).Count(ctx, build.Options(model.Reference())...)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

// duckType is the Arrow type DuckDB exports a model type as: a HUGEINT as a
// Decimal128(38, 0), a Date, which the SQL holds as its days, as an Int32, and a
// Datetime, a Duration and a Time, which it holds as their ticks (T-TICKS), as an
// Int64; a DECIMAL as a Decimal128 of its precision and scale.
func duckType(t model.Type) arrow.DataType {
	switch t.Kind() {
	case model.Uint8:
		return arrow.PrimitiveTypes.Uint8
	case model.Uint16:
		return arrow.PrimitiveTypes.Uint16
	case model.Uint32:
		return arrow.PrimitiveTypes.Uint32
	case model.Float32:
		return arrow.PrimitiveTypes.Float32
	case model.DatetimeKind, model.DurationKind, model.TimeKind:
		return arrow.PrimitiveTypes.Int64
	case model.DecimalKind:
		return &arrow.Decimal128Type{Precision: int32(t.Prec()), Scale: int32(t.Scale())}
	case model.Bool:
		return arrow.FixedWidthTypes.Boolean
	case model.Int8:
		return arrow.PrimitiveTypes.Int8
	case model.Int16:
		return arrow.PrimitiveTypes.Int16
	case model.Int32, model.Date:
		return arrow.PrimitiveTypes.Int32
	case model.Int64:
		return arrow.PrimitiveTypes.Int64
	case model.Int128:
		return &arrow.Decimal128Type{Precision: 38, Scale: 0}
	case model.Uint64:
		return arrow.PrimitiveTypes.Uint64
	case model.Float64:
		return arrow.PrimitiveTypes.Float64
	case model.String:
		return arrow.BinaryTypes.String
	}
	panic("no Arrow type for " + t.String())
}

// toArrow is a's cells as one record of DuckDB's types.
func toArrow(a *Answer) (*arrow.Schema, []arrow.RecordBatch, error) {
	fields := make([]arrow.Field, len(a.Names))
	for j := range a.Names {
		fields[j] = arrow.Field{Name: a.Names[j], Type: duckType(a.Types[j]), Nullable: true}
	}
	schema := arrow.NewSchema(fields, nil)
	b := array.NewRecordBuilder(memory.NewGoAllocator(), schema)
	defer b.Release()
	for j, t := range a.Types {
		fb := b.Field(j)
		for _, c := range a.Cells[j] {
			if c == "" {
				fb.AppendNull()
				continue
			}
			v := c[1:]
			var err error
			switch fb := fb.(type) {
			case *array.BooleanBuilder:
				fb.Append(v == "1")
			case *array.Int8Builder:
				var x int64
				x, err = strconv.ParseInt(v, 10, 8)
				fb.Append(int8(x))
			case *array.Int16Builder:
				var x int64
				x, err = strconv.ParseInt(v, 10, 16)
				fb.Append(int16(x))
			case *array.Int32Builder:
				var x int64
				x, err = strconv.ParseInt(v, 10, 32)
				fb.Append(int32(x))
			case *array.Int64Builder:
				var x int64
				x, err = strconv.ParseInt(v, 10, 64)
				fb.Append(x)
			case *array.Uint8Builder:
				var x uint64
				x, err = strconv.ParseUint(v, 10, 8)
				fb.Append(uint8(x))
			case *array.Uint16Builder:
				var x uint64
				x, err = strconv.ParseUint(v, 10, 16)
				fb.Append(uint16(x))
			case *array.Uint32Builder:
				var x uint64
				x, err = strconv.ParseUint(v, 10, 32)
				fb.Append(uint32(x))
			case *array.Uint64Builder:
				var x uint64
				x, err = strconv.ParseUint(v, 10, 64)
				fb.Append(x)
			case *array.Decimal128Builder:
				// An Int128's cell is its integer, a Decimal's its text at its scale
				// (decimalCell): the unscaled value is the digits without the point.
				x, ok := new(big.Int).SetString(strings.Replace(v, ".", "", 1), 10)
				if !ok {
					err = fmt.Errorf("a %s %q", t, v)
				}
				fb.Append(decimal128.FromBigInt(x))
			case *array.Float32Builder:
				fb.Append(Float32(c))
			case *array.Float64Builder:
				fb.Append(Float(c))
			case *array.StringBuilder:
				fb.Append(v)
			default:
				err = fmt.Errorf("no builder for %s", t)
			}
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return schema, []arrow.RecordBatch{b.NewRecordBatch()}, nil
}
