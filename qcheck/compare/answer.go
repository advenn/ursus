package compare

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/model"
)

// Answer is one engine's answer to a query, read into canonical cells by the
// query's model types, whatever Arrow type the engine gave a column: an integer,
// whatever its width or an Int128's decimal, and a Date as its decimal text; a
// float as its bits, any NaN as one NaN, so -0 is not +0; text as it is; a
// Boolean as 0 or 1. A null is "", any value "v" and its text.
type Answer struct {
	Err     error
	IsCount bool
	Count   int64
	Names   []string
	Types   []model.Type
	Rows    int
	Cells   [][]string
}

const nanCell = "vNaN"

func intCell(v int64) string   { return "v" + strconv.FormatInt(v, 10) }
func uintCell(v uint64) string { return "v" + strconv.FormatUint(v, 10) }
func floatCell(f float64) string {
	if math.IsNaN(f) {
		return nanCell
	}
	return "v" + strconv.FormatUint(math.Float64bits(f), 16)
}

// Float is a float cell's value.
func Float(cell string) float64 {
	if cell == nanCell {
		return math.NaN()
	}
	u, err := strconv.ParseUint(cell[1:], 16, 64)
	if err != nil {
		panic("compare: a float cell " + cell)
	}
	return math.Float64frombits(u)
}

// FloatCell is f's cell.
func FloatCell(f float64) string { return floatCell(f) }

// FromUrsus reads ursus's frame as the answer of a query whose columns are cols.
func FromUrsus(df *ursus.DataFrame, cols []ast.Column) (*Answer, error) {
	a := &Answer{Names: df.Columns(), Rows: df.Height()}
	if len(a.Names) != len(cols) {
		return nil, fmt.Errorf("ursus answered columns %v, the query's are %d", a.Names, len(cols))
	}
	for j, col := range cols {
		c := df.ColumnAt(j)
		if a.Names[j] != col.Name || !c.DType().Equal(build.DType(col.Type)) {
			return nil, fmt.Errorf("ursus answered column %q of %s, the query's %q is %s", a.Names[j], c.DType(), col.Name, col.Type)
		}
		a.Types = append(a.Types, col.Type)
		cells := make([]string, c.Len())
		switch col.Type {
		case model.Bool:
			bits := c.Bools()
			for i := range cells {
				if c.IsValid(i) {
					cells[i] = "v0"
					if bits.Get(i) {
						cells[i] = "v1"
					}
				}
			}
		case model.String:
			s := c.Strings()
			for i := range cells {
				if c.IsValid(i) {
					cells[i] = "v" + s.Get(i)
				}
			}
		default:
			raw := c.RawFixed()
			w := c.DType().Physical().BitWidth() / 8
			for i := range cells {
				if !c.IsValid(i) {
					continue
				}
				cells[i] = fixedCell(col.Type, raw[i*w:(i+1)*w])
			}
		}
		a.Cells = append(a.Cells, cells)
	}
	return a, nil
}

// fixedCell is a fixed-width value's cell, from its little-endian bytes.
func fixedCell(t model.Type, b []byte) string {
	switch t {
	case model.Int8:
		return intCell(int64(int8(b[0])))
	case model.Int16:
		return intCell(int64(int16(binary.LittleEndian.Uint16(b))))
	case model.Int32, model.Date:
		return intCell(int64(int32(binary.LittleEndian.Uint32(b))))
	case model.Int64:
		return intCell(int64(binary.LittleEndian.Uint64(b)))
	case model.Uint64:
		return uintCell(binary.LittleEndian.Uint64(b))
	case model.Int128:
		// i128.Int128 is struct{ Hi int64; Lo uint64 }, Hi first in memory.
		hi, lo := int64(binary.LittleEndian.Uint64(b)), binary.LittleEndian.Uint64(b[8:])
		v := new(big.Int).Lsh(big.NewInt(hi), 64)
		return "v" + v.Add(v, new(big.Int).SetUint64(lo)).String()
	case model.Float64:
		return floatCell(math.Float64frombits(binary.LittleEndian.Uint64(b)))
	}
	panic("compare: no fixed cell of " + t.String())
}

// FromArrow reads an engine's Arrow answer as the answer of a query whose columns
// are cols. A column is read by its model type from any Arrow type that holds it
// exactly: an integer from any integer or a Decimal128 of scale 0, a Date from an
// Int32 or a Date32, text from a String, a LargeString or a StringView.
func FromArrow(schema *arrow.Schema, recs []arrow.RecordBatch, cols []ast.Column) (*Answer, error) {
	if schema.NumFields() != len(cols) {
		return nil, fmt.Errorf("the engine answered %d columns, the query's are %d", schema.NumFields(), len(cols))
	}
	a := &Answer{}
	for _, r := range recs {
		a.Rows += int(r.NumRows())
	}
	for j, col := range cols {
		f := schema.Field(j)
		if f.Name != col.Name {
			return nil, fmt.Errorf("the engine answered column %q, the query's is %q", f.Name, col.Name)
		}
		a.Names = append(a.Names, col.Name)
		a.Types = append(a.Types, col.Type)
		cells := make([]string, 0, a.Rows)
		for _, r := range recs {
			arr := r.Column(j)
			for i := range arr.Len() {
				if arr.IsNull(i) {
					cells = append(cells, "")
					continue
				}
				c, err := arrowCell(col.Type, arr, i)
				if err != nil {
					return nil, fmt.Errorf("column %q: %w", col.Name, err)
				}
				cells = append(cells, c)
			}
		}
		a.Cells = append(a.Cells, cells)
	}
	return a, nil
}

func arrowCell(t model.Type, arr arrow.Array, i int) (string, error) {
	switch t {
	case model.Bool:
		if b, ok := arr.(*array.Boolean); ok {
			if b.Value(i) {
				return "v1", nil
			}
			return "v0", nil
		}
	case model.Float64:
		if f, ok := arr.(*array.Float64); ok {
			return floatCell(f.Value(i)), nil
		}
	case model.String:
		switch s := arr.(type) {
		case *array.String:
			return "v" + s.Value(i), nil
		case *array.LargeString:
			return "v" + s.Value(i), nil
		case *array.StringView:
			return "v" + s.Value(i), nil
		}
	default:
		switch v := arr.(type) {
		case *array.Int8:
			return intCell(int64(v.Value(i))), nil
		case *array.Int16:
			return intCell(int64(v.Value(i))), nil
		case *array.Int32:
			return intCell(int64(v.Value(i))), nil
		case *array.Int64:
			return intCell(v.Value(i)), nil
		case *array.Uint8:
			return uintCell(uint64(v.Value(i))), nil
		case *array.Uint16:
			return uintCell(uint64(v.Value(i))), nil
		case *array.Uint32:
			return uintCell(uint64(v.Value(i))), nil
		case *array.Uint64:
			return uintCell(v.Value(i)), nil
		case *array.Date32:
			return intCell(int64(v.Value(i))), nil
		case *array.Decimal128:
			if dt := v.DataType().(*arrow.Decimal128Type); dt.Scale == 0 {
				return "v" + v.Value(i).BigInt().String(), nil
			}
		}
	}
	return "", fmt.Errorf("a %s column read from Arrow %s", t, arr.DataType())
}

// show renders a cell for a report.
func show(cell string, t model.Type) string {
	switch {
	case cell == "":
		return "null"
	case cell == nanCell:
		return "NaN"
	case t == model.Float64:
		return ast.GoFloat(Float(cell))
	case t == model.String:
		s := cell[1:]
		if len(s) > 40 {
			s = s[:40] + fmt.Sprintf("…(%d bytes)", len(s))
		}
		return strconv.Quote(s)
	case t == model.Bool:
		return map[string]string{"v0": "false", "v1": "true"}[cell]
	}
	return cell[1:]
}

// Row renders row r.
func (a *Answer) Row(r int) string {
	parts := make([]string, len(a.Names))
	for k := range a.Names {
		parts[k] = a.Names[k] + "=" + show(a.Cells[k][r], a.Types[k])
	}
	return strings.Join(parts, " ")
}

// String renders the answer: its count, its refusal, or its first rows in order.
func (a *Answer) String() string {
	switch {
	case a.Err != nil:
		return "refused: " + firstLine(a.Err)
	case a.IsCount:
		return fmt.Sprintf("Count %d", a.Count)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d rows of %v", a.Rows, a.Names)
	for r := range min(a.Rows, 12) {
		b.WriteString("\n  " + a.Row(r))
	}
	if a.Rows > 12 {
		fmt.Fprintf(&b, "\n  … %d more", a.Rows-12)
	}
	return b.String()
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}
