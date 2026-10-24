package sqlrows

import (
	"database/sql"
	"reflect"
	"strings"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// driverTypes is the types column ct's driver names it, to try in turn on col, its
// values read as base. By its type's name: a DECIMAL or a NUMERIC of its precision and
// scale, or where it gives none and the values are text, of 38 digits and their
// scale, as Polars reads a Python Decimal; a DATE; a TIME; a TIMESTAMP of no zone or
// in one, the second in UTC; text; or bytes. Else by the Go type the driver scans it
// to: an integer of its width, a float of its, a Boolean, text, bytes or an instant.
// Bytes are text where the column's type is named and is not a binary one, as a
// database's text protocol gives every value, and text a type is parsed from stays
// text where a value does not parse.
func driverTypes(ct *sql.ColumnType, base dtype.DataType, col *data.Column) []dtype.DataType {
	name := strings.ToUpper(strings.TrimSpace(ct.DatabaseTypeName()))
	var ts []dtype.DataType
	switch {
	case decimalName(name):
		if p, s, ok := ct.DecimalSize(); ok && p >= 1 && p <= dtype.MaxDecimalPrecision && s >= 0 && s <= p {
			ts = []dtype.DataType{dtype.Decimal(uint8(p), uint8(s))}
		} else if (base == dtype.String || base == dtype.Binary) && col != nil {
			if s, ok := textScale(col); ok {
				ts = []dtype.DataType{dtype.Decimal(dtype.MaxDecimalPrecision, uint8(s))}
			}
		} else {
			ts = scanTypes(ct.ScanType())
		}
	case name == "DATE":
		ts = []dtype.DataType{dtype.Date}
	case name == "TIME" || strings.HasPrefix(name, "TIME(") || name == "TIME WITHOUT TIME ZONE":
		ts = []dtype.DataType{dtype.Time(dtype.Nano)}
	case zonedName(name):
		ts = []dtype.DataType{dtype.Datetime(dtype.Micro, "UTC")}
	case naiveName(name):
		ts = []dtype.DataType{dtype.Datetime(dtype.Micro, "")}
	case textName(name):
		ts = []dtype.DataType{dtype.String}
	case binaryName(name):
		ts = []dtype.DataType{dtype.Binary}
	default:
		ts = scanTypes(ct.ScanType())
	}
	if base == dtype.Binary && name != "" && !binaryName(name) {
		ts = append(ts, dtype.String)
	}
	return ts
}

func decimalName(name string) bool {
	return strings.Contains(name, "DECIMAL") || strings.Contains(name, "NUMERIC") ||
		name == "NUMBER" || strings.HasPrefix(name, "NUMBER(") || strings.HasSuffix(name, "MONEY")
}

// zonedName is an instant's type in a zone: Postgres' TIMESTAMPTZ, the standard's
// WITH TIME ZONE, SQL Server's DATETIMEOFFSET.
func zonedName(name string) bool {
	return strings.Contains(name, "TIMESTAMPTZ") || strings.Contains(name, "DATETIMEOFFSET") ||
		(strings.Contains(name, "WITH TIME ZONE") || strings.Contains(name, "WITH LOCAL TIME ZONE")) &&
			!strings.Contains(name, "WITHOUT")
}

func naiveName(name string) bool {
	return strings.HasPrefix(name, "TIMESTAMP") || strings.HasPrefix(name, "DATETIME") ||
		name == "SMALLDATETIME"
}

func textName(name string) bool {
	for _, w := range []string{"CHAR", "TEXT", "CLOB", "STRING", "JSON", "XML"} {
		if strings.Contains(name, w) {
			return true
		}
	}
	switch name {
	case "UUID", "ENUM", "SET", "NAME", "INET", "CIDR", "MACADDR":
		return true
	}
	return false
}

// binaryName is a type of bytes that are not text: a BLOB, Postgres' BYTEA, a
// BINARY, SQL Server's IMAGE and UNIQUEIDENTIFIER, a BIT string, a GEOMETRY.
func binaryName(name string) bool {
	for _, w := range []string{"BLOB", "BYTEA", "BINARY", "IMAGE", "GEOMETRY"} {
		if strings.Contains(name, w) {
			return true
		}
	}
	switch name {
	case "RAW", "UNIQUEIDENTIFIER", "BIT", "VARBIT", "BIT VARYING":
		return true
	}
	return false
}

// scanTypes is the type the Go type a driver scans a column to names.
func scanTypes(st reflect.Type) []dtype.DataType {
	if st == nil {
		return nil
	}
	var t dtype.DataType
	switch st {
	case reflect.TypeFor[sql.NullInt64]():
		t = dtype.Int64
	case reflect.TypeFor[sql.NullInt32]():
		t = dtype.Int32
	case reflect.TypeFor[sql.NullInt16]():
		t = dtype.Int16
	case reflect.TypeFor[sql.NullByte]():
		t = dtype.Uint8
	case reflect.TypeFor[sql.NullFloat64]():
		t = dtype.Float64
	case reflect.TypeFor[sql.NullBool]():
		t = dtype.Bool
	case reflect.TypeFor[sql.NullString]():
		t = dtype.String
	case reflect.TypeFor[sql.NullTime](), reflect.TypeFor[time.Time]():
		t = dtype.Datetime(dtype.Micro, "")
	case reflect.TypeFor[sql.RawBytes](), reflect.TypeFor[[]byte]():
		t = dtype.Binary
	default:
		switch st.Kind() {
		case reflect.Int64, reflect.Int:
			t = dtype.Int64
		case reflect.Int32:
			t = dtype.Int32
		case reflect.Int16:
			t = dtype.Int16
		case reflect.Int8:
			t = dtype.Int8
		case reflect.Uint64, reflect.Uint:
			t = dtype.Uint64
		case reflect.Uint32:
			t = dtype.Uint32
		case reflect.Uint16:
			t = dtype.Uint16
		case reflect.Uint8:
			t = dtype.Uint8
		case reflect.Float64:
			t = dtype.Float64
		case reflect.Float32:
			t = dtype.Float32
		case reflect.Bool:
			t = dtype.Bool
		case reflect.String:
			t = dtype.String
		default:
			return nil
		}
	}
	return []dtype.DataType{t}
}

// textScale is the most digits after the point among col's values, each a decimal of
// 38 digits at most, or ok false where one is not.
func textScale(col *data.Column) (int, bool) {
	sa, valid := col.Strings(), col.Validity()
	scale, whole := 0, 0
	for r := range col.Len() {
		if !valid.Get(r) {
			continue
		}
		s := sa.Get(r)
		if s != "" && (s[0] == '-' || s[0] == '+') {
			s = s[1:]
		}
		intPart, frac, _ := strings.Cut(s, ".")
		if (intPart == "" && frac == "") || !digits(intPart) || !digits(frac) {
			return 0, false
		}
		whole = max(whole, len(strings.TrimLeft(intPart, "0")))
		scale = max(scale, len(frac))
	}
	if whole+scale > dtype.MaxDecimalPrecision {
		return 0, false
	}
	return scale, true
}

func digits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
