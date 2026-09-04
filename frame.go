package ursus

import (
	"strconv"
	"strings"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/data"
	"ursus/internal/uerr"
)

// DataFrame is a materialised result: named, equal-length, typed columns.
//
// There is no row index. Row order is a property of the data, not an addressable
// label space — the single most consequential thing Polars got right and pandas
// did not.
type DataFrame struct {
	batch *data.Batch
}

// Schema returns the column names and types.
func (df *DataFrame) Schema() *Schema { return df.batch.Schema() }

// Height returns the number of rows; Width the number of columns.
func (df *DataFrame) Height() int { return df.batch.Rows() }
func (df *DataFrame) Width() int  { return df.batch.NumCols() }

// Shape returns (rows, columns).
func (df *DataFrame) Shape() (int, int) { return df.batch.Rows(), df.batch.NumCols() }

// Columns returns the column names in order.
func (df *DataFrame) Columns() []string { return df.batch.Schema().Names() }

// Batch exposes the underlying batch for internal use and tests.
func (df *DataFrame) Batch() *data.Batch { return df.batch }

// Column returns a typed view of a column.
//
// A generic METHOD — the thing Go 1.27 unlocked. Previously this had to be
// `ursus.Column[int64](df, "x")`, with the receiver demoted to an argument.
func (df *DataFrame) Column[T any](name string) (*data.Series[T], error) {
	c, ok := df.batch.ByName(name)
	if !ok {
		return nil, uerr.UnknownColumn("column", name, df.Columns())
	}
	return data.TypedColumn[T](c)
}

// At returns one value and whether it is non-null.
func (df *DataFrame) At[T any](row int, name string) (T, bool, error) {
	var zero T
	s, err := df.Column[T](name)
	if err != nil {
		return zero, false, err
	}
	if row < 0 || row >= s.Len() {
		return zero, false, uerr.New(uerr.KindValue, "at",
			"row %d is out of range (%d rows)", row, s.Len())
	}
	v, ok := s.Get(row)
	return v, ok, nil
}

// Rows decodes every row into a T.
func (df *DataFrame) Rows[T any]() ([]T, error) { return decodeRows[T](df.batch) }

// String renders the frame as an aligned table.
//
// Display quality is not cosmetic: this is what people look at all day, and a
// column of unaligned numbers with no visible types is the difference between a
// library that feels finished and one that does not.
func (df *DataFrame) String() string {
	const maxRows = 10

	schema := df.batch.Schema()
	nCols := schema.Len()
	if nCols == 0 {
		return "shape: (" + strconv.Itoa(df.Height()) + ", 0)\n"
	}

	shown := min(df.Height(), maxRows)

	// Header is two lines per column: name, then dtype. Seeing the dtype without
	// asking prevents a whole category of confusion about why a comparison failed.
	header := make([]string, nCols)
	types := make([]string, nCols)
	for i, f := range schema.All() {
		header[i] = f.Name
		types[i] = f.Type.String()
	}

	cells := make([][]string, shown)
	for r := range shown {
		row := make([]string, nCols)
		for c := range nCols {
			row[c] = renderCell(df.batch.Column(c), r)
		}
		cells[r] = row
	}

	widths := make([]int, nCols)
	for c := range nCols {
		widths[c] = max(len(header[c]), len(types[c]))
		for r := range shown {
			widths[c] = max(widths[c], len(cells[r][c]))
		}
	}

	var b strings.Builder
	b.WriteString("shape: (" + strconv.Itoa(df.Height()) + ", " + strconv.Itoa(nCols) + ")\n")

	writeRow := func(vals []string) {
		b.WriteString("│")
		for c, v := range vals {
			b.WriteString(" " + pad(v, widths[c]) + " │")
		}
		b.WriteString("\n")
	}
	writeRule := func(l, m, r string) {
		b.WriteString(l)
		for c := range nCols {
			b.WriteString(strings.Repeat("─", widths[c]+2))
			if c < nCols-1 {
				b.WriteString(m)
			}
		}
		b.WriteString(r + "\n")
	}

	writeRule("┌", "┬", "┐")
	writeRow(header)
	writeRow(types)
	writeRule("├", "┼", "┤")
	for r := range shown {
		writeRow(cells[r])
	}
	writeRule("└", "┴", "┘")

	if df.Height() > shown {
		b.WriteString("… " + strconv.Itoa(df.Height()-shown) + " more rows\n")
	}
	return b.String()
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// renderCell formats one value. Null renders as "null" rather than as an empty
// cell or a zero, so a missing value is never mistaken for a present one.
func renderCell(c *data.Column, row int) string {
	if !c.IsValid(row) {
		return "null"
	}
	if s, err := data.TypedColumn[string](c); err == nil {
		v, _ := s.Get(row)
		return strconv.Quote(v)
	}
	if s, err := data.TypedColumn[bool](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatBool(v)
	}
	if s, err := data.TypedColumn[float64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	if s, err := data.TypedColumn[float32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	}
	// Temporal types are physically Int32/Int64, so without this they fall through
	// to the integer branch and a Datetime prints as its raw tick count —
	// 2024-01-01 as 1704067200000000000. Decimal has the same shape of guard
	// immediately below, for the same reason.
	if dt := c.DType(); dt.IsTemporal() {
		if ticks, ok := temporalTicks(c, row); ok {
			return dtype.FormatTemporal(dt, ticks)
		}
	}
	if s, err := data.TypedColumn[i128.Int128](c); err == nil {
		v, _ := s.Get(row)
		// A Decimal is physically an Int128 of unscaled units, so it reaches this
		// branch and would otherwise print 12.34 as "1234" — wrong by a factor of
		// 10^scale, and plausible enough that nobody would question it.
		if dt := c.DType(); dt.ID() == dtype.TypeDecimal {
			return dtype.FormatDecimal(v.String(), dt.Scale())
		}
		return v.String()
	}
	if s, err := data.TypedColumn[int64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(v, 10)
	}
	if s, err := data.TypedColumn[int32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10)
	}
	if s, err := data.TypedColumn[uint64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(v, 10)
	}
	if s, err := data.TypedColumn[uint32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10)
	}
	// The narrow widths. data.Values checks the EXACT physical type, not the bit
	// width, so an Int8 column cannot be read through the int32 accessor above — it
	// fell through to "?" and printed nothing readable. That is the same failure the
	// aggregate tests already record: a renderer that answers "?" makes a comparison
	// of two rendered frames pass while proving nothing.
	if s, err := data.TypedColumn[int16](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10)
	}
	if s, err := data.TypedColumn[int8](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10)
	}
	if s, err := data.TypedColumn[uint16](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10)
	}
	if s, err := data.TypedColumn[uint8](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10)
	}
	// Not "?": a type this cannot render is a gap worth naming, and an unnamed
	// placeholder is what lets two different values compare equal.
	return "<unrenderable " + c.DType().String() + ">"
}

// temporalTicks reads one row of a temporal column as an int64 tick count,
// whatever its storage width. Date is int32 days; the rest are int64.
func temporalTicks(c *data.Column, row int) (int64, bool) {
	if s, err := data.TypedColumn[int32](c); err == nil {
		v, ok := s.Get(row)
		return int64(v), ok
	}
	if s, err := data.TypedColumn[int64](c); err == nil {
		v, ok := s.Get(row)
		return v, ok
	}
	return 0, false
}
