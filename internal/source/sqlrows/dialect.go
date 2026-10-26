package sqlrows

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Dialect is how a SQL database spells what a write says to it (step 366): its
// placeholders, its quoting, its columns' types and its limits. database/sql cannot
// name it, so a caller names it, or the driver's package does.
type Dialect int

const (
	DialectUnknown Dialect = iota
	SQLite
	Postgres
	MySQL
	SQLServer
)

func (d Dialect) String() string {
	switch d {
	case SQLite:
		return "SQLite"
	case Postgres:
		return "PostgreSQL"
	case MySQL:
		return "MySQL"
	case SQLServer:
		return "SQL Server"
	}
	return "an unknown dialect"
}

// DialectOf names the dialect of a database/sql driver, or of a driver connection, by
// its package: modernc's and mattn's SQLite, pgx's and lib/pq's PostgreSQL, the MySQL
// driver, and Microsoft's and denisenkom's SQL Server.
func DialectOf(driver any) Dialect {
	t := reflect.TypeOf(driver)
	if t == nil {
		return DialectUnknown
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	pkg := strings.ToLower(t.PkgPath())
	switch {
	case strings.Contains(pkg, "sqlite"):
		return SQLite
	case strings.Contains(pkg, "pgx"), strings.Contains(pkg, "lib/pq"), strings.Contains(pkg, "postgres"):
		return Postgres
	case strings.Contains(pkg, "mysql"):
		return MySQL
	case strings.Contains(pkg, "mssql"), strings.Contains(pkg, "sqlserver"):
		return SQLServer
	}
	return DialectUnknown
}

// Quote is name as one identifier: in double quotes, as the standard and SQLite and
// PostgreSQL quote it, in backquotes for MySQL, in brackets for SQL Server, each
// doubling its closing quote within.
func (d Dialect) Quote(name string) string {
	switch d {
	case MySQL:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case SQLServer:
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Table is a table's name, its parts split at each dot, as Polars splits
// "schema.table", each part quoted.
func (d Dialect) Table(name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = d.Quote(p)
	}
	return strings.Join(parts, ".")
}

// Placeholder is the nth parameter's marker, from 1.
func (d Dialect) Placeholder(n int) string {
	switch d {
	case Postgres:
		return "$" + strconv.Itoa(n)
	case SQLServer:
		return "@p" + strconv.Itoa(n)
	}
	return "?"
}

// RowsPerInsert is how many rows of width columns one INSERT takes: as many as its
// parameters allow, at most a thousand, the most SQL Server takes in one VALUES.
// SQLite's are 999, the least any of its builds allows; SQL Server's 2,100 a statement.
func (d Dialect) RowsPerInsert(width int) int {
	params := 65535 // PostgreSQL's and MySQL's
	switch d {
	case SQLite:
		params = 999
	case SQLServer:
		params = 2099
	}
	return max(1, min(1000, params/max(width, 1)))
}

// ColumnType is the type a column of t is created as, the one ReadDatabase reads back
// as t where the database keeps it. A List, an Array, a Struct and a Duration have
// none, and are refused by name.
func (d Dialect) ColumnType(t dtype.DataType) (string, error) {
	pick := func(sqlite, postgres, mysql, sqlserver string) string {
		switch d {
		case Postgres:
			return postgres
		case MySQL:
			return mysql
		case SQLServer:
			return sqlserver
		}
		return sqlite
	}
	switch t.ID() {
	case dtype.TypeBool:
		return pick("BOOLEAN", "BOOLEAN", "BOOLEAN", "BIT"), nil
	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeUint8:
		return pick("INTEGER", "SMALLINT", "SMALLINT", "SMALLINT"), nil
	case dtype.TypeInt32, dtype.TypeUint16:
		return pick("INTEGER", "INTEGER", "INT", "INT"), nil
	case dtype.TypeInt64, dtype.TypeUint32:
		return pick("INTEGER", "BIGINT", "BIGINT", "BIGINT"), nil
	case dtype.TypeUint64:
		return pick("INTEGER", "NUMERIC(20, 0)", "BIGINT UNSIGNED", "DECIMAL(20, 0)"), nil
	case dtype.TypeInt128:
		return pick("NUMERIC", "NUMERIC(39, 0)", "DECIMAL(39, 0)", "DECIMAL(38, 0)"), nil
	case dtype.TypeFloat32:
		return pick("REAL", "REAL", "FLOAT", "REAL"), nil
	case dtype.TypeFloat64:
		return pick("REAL", "DOUBLE PRECISION", "DOUBLE", "FLOAT"), nil
	case dtype.TypeDecimal:
		return fmt.Sprintf("DECIMAL(%d, %d)", t.Precision(), t.Scale()), nil
	case dtype.TypeString, dtype.TypeEnum, dtype.TypeCategorical, dtype.TypeNull:
		return pick("TEXT", "TEXT", "LONGTEXT", "NVARCHAR(MAX)"), nil
	case dtype.TypeBinary:
		return pick("BLOB", "BYTEA", "LONGBLOB", "VARBINARY(MAX)"), nil
	case dtype.TypeDate:
		return "DATE", nil
	case dtype.TypeTime:
		return pick("TIME", "TIME", "TIME(6)", "TIME(7)"), nil
	case dtype.TypeDatetime:
		if t.TimeZone() != "" {
			// SQLite has no type of its own for an instant with a zone, and its Go
			// drivers read back a TIMESTAMP alone, its text with its offset. MySQL has
			// none either: its DATETIME holds the instant's UTC wall clock, which reads
			// back as a Datetime of no zone.
			return pick("TIMESTAMP", "TIMESTAMPTZ", "DATETIME(6)", "DATETIMEOFFSET(7)"), nil
		}
		return pick("TIMESTAMP", "TIMESTAMP", "DATETIME(6)", "DATETIME2(7)"), nil
	}
	hint := "cast it to a type a database holds"
	if t.ID() == dtype.TypeDuration {
		hint = "cast it to an integer, as Duration.TotalMicroseconds does"
	}
	return "", uerr.New(uerr.KindType, "write_database", "%s has no column type in %s", t, d).Hint("%s", hint)
}

// CreateTable is the statement that creates table with the columns of s.
func (d Dialect) CreateTable(table string, s *dtype.Schema) (string, error) {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(d.Table(table))
	b.WriteString(" (")
	for i := range s.Len() {
		f := s.Field(i)
		typ, err := d.ColumnType(f.Type)
		if err != nil {
			return "", uerr.Wrap(err, uerr.KindType, "write_database", "column %q", f.Name)
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.Quote(f.Name))
		b.WriteByte(' ')
		b.WriteString(typ)
	}
	b.WriteString(")")
	return b.String(), nil
}

// DropTable drops table, if it is there.
func (d Dialect) DropTable(table string) string {
	return "DROP TABLE IF EXISTS " + d.Table(table)
}

// Insert is an INSERT of rows rows into table's columns cols.
func (d Dialect) Insert(table string, cols []string, rows int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(d.Table(table))
	b.WriteString(" (")
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.Quote(c))
	}
	b.WriteString(") VALUES ")
	n := 0
	for r := range rows {
		if r > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for c := range cols {
			if c > 0 {
				b.WriteString(", ")
			}
			n++
			b.WriteString(d.Placeholder(n))
		}
		b.WriteByte(')')
	}
	return b.String()
}

// Exists is a query of one row and one value, nonzero or true when table exists, and
// its arguments: SQLite's catalogue, PostgreSQL's to_regclass, MySQL's
// information_schema, SQL Server's OBJECT_ID. Each finds the table as a statement
// naming it would, in the schema the name gives or the connection's own.
func (d Dialect) Exists(table string) (string, []any) {
	parts := strings.Split(table, ".")
	name := parts[len(parts)-1]
	switch d {
	case Postgres:
		return "SELECT to_regclass($1) IS NOT NULL", []any{d.Table(table)}
	case SQLServer:
		return "SELECT CASE WHEN OBJECT_ID(@p1, N'U') IS NULL THEN 0 ELSE 1 END", []any{d.Table(table)}
	case MySQL:
		if len(parts) > 1 {
			return "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ?",
				[]any{parts[len(parts)-2], name}
		}
		return "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
			[]any{name}
	}
	catalogue := "sqlite_master"
	if len(parts) > 1 {
		catalogue = d.Quote(parts[len(parts)-2]) + ".sqlite_master"
	}
	return "SELECT COUNT(*) FROM " + catalogue + " WHERE type IN ('table', 'view') AND name = ?", []any{name}
}
