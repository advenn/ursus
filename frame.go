package ursus

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/width"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
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

// ColumnAt returns the i-th column, in order, untyped: what DataFrame.Batch gave
// before step 186, a column at a time, through the public Column rather than the
// internal batch.
func (df *DataFrame) ColumnAt(i int) *Column { return df.batch.Column(i) }

// Column returns a typed view of a column.
//
// A generic METHOD — the thing Go 1.27 unlocked. Previously this had to be
// `ursus.Column[int64](df, "x")`, with the receiver demoted to an argument.
func (df *DataFrame) Column[T any](name string) (*Series[T], error) {
	c, ok := df.batch.ByName(name)
	if !ok {
		return nil, uerr.UnknownColumn("column", name, df.Columns())
	}
	return seriesOf[T](c)
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
func (df *DataFrame) Rows[T any]() (rows []T, err error) {
	defer uerr.Catch(&err, "rows")
	return decodeRows[T](df.batch)
}

// String renders the frame as an aligned table.
//
// Display quality is not cosmetic: this is what people look at all day, and a
// column of unaligned numbers with no visible types is the difference between a
// library that feels finished and one that does not.
func (df *DataFrame) String() string { return df.table(min(df.Height(), 10), df.Height()) }

// table draws the frame's first rows rows under the shape of a frame of height rows,
// and says how many more there are: String's ten of every row, Show's all of its own.
func (df *DataFrame) table(rows, height int) string {
	schema := df.batch.Schema()
	nCols := schema.Len()
	if nCols == 0 {
		return "shape: (" + strconv.Itoa(height) + ", 0)\n"
	}

	shown := rows

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
		widths[c] = max(displayWidth(header[c]), displayWidth(types[c]))
		for r := range shown {
			widths[c] = max(widths[c], displayWidth(cells[r][c]))
		}
	}

	var b strings.Builder
	b.WriteString("shape: (" + strconv.Itoa(height) + ", " + strconv.Itoa(nCols) + ")\n")

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

	if height > shown {
		b.WriteString("… " + strconv.Itoa(height-shown) + " more rows\n")
	}
	return b.String()
}

func pad(s string, w int) string {
	n := displayWidth(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// displayWidth is how many columns of a terminal s takes, which the table pads to:
// one a rune, two for an East Asian wide or full-width one, none for a combining
// mark or a zero-width format character, as Polars' table measures a cell. It was
// the string's length in bytes until step 296, which shifted every row holding a
// letter outside ASCII: "Zoë" is three columns and four bytes.
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		case width.LookupRune(r).Kind() == width.EastAsianWide,
			width.LookupRune(r).Kind() == width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}

// renderCell formats one value. Null renders as "null" rather than as an empty
// cell or a zero, so a missing value is never mistaken for a present one.
func renderCell(c *data.Column, row int) string {
	if !c.IsValid(row) {
		return "null"
	}
	// A List has to be answered before the typed probes below: its child may be
	// Int64, and TypedColumn reads the COLUMN's payload, so a list of integers
	// would otherwise fall through and print nothing sensible.
	//
	// An empty list renders "[]" and a null list "null", which is the distinction
	// the whole type has to keep — and the cheapest place to see it broken.
	if c.DType().IsListLike() {
		acc := c.Lists()
		start, end, _ := acc.Get(row)
		var b strings.Builder
		b.WriteByte('[')
		for e := start; e < end; e++ {
			if e > start {
				b.WriteString(", ")
			}
			b.WriteString(renderCell(acc.Child(), int(e)))
		}
		b.WriteByte(']')
		return b.String()
	}
	// A Struct, like a List, has to be answered before the typed probes: it has no
	// payload buffer of its own at all, so every probe below would miss and the cell
	// would render empty.
	//
	// A NULL struct printed `null` above; a struct whose fields are all null prints
	// `{age: null, city: null}`. Those two rows are the distinction the type exists
	// to keep, and this is the cheapest place to see it broken.
	if c.DType().ID() == dtype.TypeStruct {
		var b strings.Builder
		b.WriteByte('{')
		for i, f := range c.Fields() {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(f.Name())
			b.WriteString(": ")
			b.WriteString(renderCell(f, row))
		}
		b.WriteByte('}')
		return b.String()
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
