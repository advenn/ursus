# Step 353 — as built

**Databases,** Polars' `read_database`, from `polars-parity.md` §5: `ReadDatabase`
runs a query through Go's `database/sql`, on any driver, and reads its rows as a frame.

## 1. How

**The API** (`database.go`): `ReadDatabase(ctx, db, query, args...)` runs a query on a
`Queryer`, a `*sql.DB`, a `*sql.Tx` or a `*sql.Conn`, its arguments Polars'
`execute_options` parameters; `ReadDatabaseRows(rows, opts...)` reads a result already
run, a prepared statement's, and closes it, and takes `WithDatabaseSchemaOverrides`,
Polars' `schema_overrides`. The frame is eager and read whole, as Polars' is.

**Reading** (`internal/source/sqlrows`): each column is a `sql.Scanner`, so a driver's
value reaches it as the driver gives it, uncopied and unboxed by `database/sql`, and is
kept by its kind: a number's, a Boolean's or an instant's wall clock in a word, text
and bytes in one buffer, an instant's offset from UTC beside it. A buffer is made at
the first row that needs it, so a column of one kind keeps no other's; no `any` is held
a cell.

**Its type is its values'**, every row's, where Polars reads 100 and truncates a later
float to an integer or refuses a later kind: integers an Int64, an unsigned one past
them a Uint64, floats a Float64, text a String (or bytes where it is not UTF-8), bytes
a Binary, and instants a Datetime in microseconds, of no zone where each is in UTC, as a
driver gives a timestamp without one, and in UTC where one is in a zone, as Polars
reads an aware datetime. A driver's own type is read by its Go kind, a `driver.Valuer`
by its value, anything else as the text fmt writes. Two kinds meet at Polars'
supertype, `dtype.Supertype`, and text beside bytes at bytes, as Polars'; each kind's
values are cast to it and each row taken from its kind's column. A number does not
meet bytes, as Polars' does not, and neither does an instant a Boolean.

**Then the type its driver names,** where every value fits it, a lossy cast that nulls
none: by the type's name a DECIMAL of its precision, or of 38 digits and its text's
scale where it states none, as Polars reads a Python Decimal; a DATE, from each
instant's own wall clock and only where each is a midnight, as Oracle's DATE holds a
time; a TIME; a TIMESTAMP of no zone, or in one; text; or bytes. Else by the Go type the
driver scans to: an integer of its width, a Float32, a Boolean, text. Bytes are text
where the type has a name and is not a binary one, as a text protocol gives a
`NUMERIC`, a `UUID`, an `INTERVAL`. A float is not a type for integers: SQLite's
NUMERIC is one to its drivers and holds integers as such, which Polars reads as Int64.
A column of no value is of its driver's type, or a Null, and `NOT NULL` makes a field
non-nullable where the values agree.

| Polars 1.44.1, through sqlite3 | ursus |
| --- | --- |
| integers, floats, text, bytes; with nulls | as Polars |
| an integer and a float; and text; bytes and text | Float64; String; Binary, as Polars |
| a null and an integer; no value | Int64; Null, as Polars |
| 150 integers and a float | Int64, the float read as 1; Float64 here, as `infer_schema_length=None` |
| bytes and an integer | refused, as Polars |
| a name twice | refused, as Polars |
| a naive, an aware datetime | Datetime(us); Datetime(us, UTC), as Polars |
| a datetime and text | refused, "could not append value"; text here, by Polars' supertype |
| `schema_overrides` of a column the result lacks | ignored; refused here, as NDJSON's |
| `schema_overrides` of text to a Date, a Decimal | refused, null; parsed here |
| `execute_options={"parameters": …}` | `args...` |

## 2. Tests

**`TestReadDatabase`,** a fake driver's results, bare, naming no column types, every
answer Polars 1.44.1's but where noted. **`TestReadDatabaseDriverTypes`:** a typed
driver's INT4, FLOAT4, BOOLEAN, NUMERIC sized and not and its NaN, DATE from instants,
from text and with a time of day, TIME, TIMESTAMPTZ, DATETIME from text, VARCHAR,
UUID, BLOB, INTERVAL, SQLite's NUMERIC, no value of a type; no row; NOT NULL.
**`TestReadDatabaseOverrides`** and **`TestReadDatabaseRefusals`:** overrides, and
refused ones; mixed kinds, a name twice, a broken result, a failed query, an instant
past a Datetime, a cancelled context; a transaction, a connection and a statement's
rows. **`TestReadDatabaseManyRows`:** 10,000 rows whose kind changes at 5,000.

**A real driver:** one SQLite file read by ursus through modernc's pure-Go driver and
by Polars through Python's sqlite3, seven queries: every type and value agrees but
three columns' types, which the Go driver gives typed: a TIMESTAMP's instants a
Datetime(us), a DATE's a Date, a BOOLEAN's a Bool, where sqlite3 gives Polars text and
integers (feature-gaps §9).

**Teeth (14 of 14 bite):** bytes beside text not bytes; a narrower integer not taken;
a DATE the instant's date in UTC; a DATE's time of day lost; an instant in a zone
naive; text not UTF-8 text; NOT NULL ignored; a named type's bytes not text; no check
that each kind casts to the meet; a NUMERIC of no size of scale 0; an override of no
column taken; a late kind's buffer misaligned; a late text's offsets misaligned; a
value not fitting the driver's type taken.

## 3. Performance

A million rows of an integer, a float and text from one SQLite file through modernc's
driver, the median of seven: a hand-written `Scan` loop into typed values appended to
slices 734 ms, `ReadDatabase` 799, 9% over it, the driver's reading most of both;
Polars' `read_database` through Python's sqlite3, a C SQLite, 1049 ms.

**Gate:** test-all 140 ok, race 28 ok (the root package 762 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
