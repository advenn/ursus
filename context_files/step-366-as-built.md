# Step 366 — as built

**A database written:** Polars' `write_database(table_name, connection,
if_table_exists)`, L1 of `v0.6.8-scope.md`.

## 1. How

**At the root** (`databasewrite.go`):

- `LazyFrame.WriteDatabase(ctx, db, table, opts...) (int64, error)` runs the query and
  inserts its rows, and answers how many it wrote; `DataFrame.WriteDatabase` is the
  same on a frame.
- `db` is a `DatabaseWriter`: `ExecContext`, `QueryContext` and `PrepareContext`, so a
  `*sql.DB`, a `*sql.Conn` or a `*sql.Tx`.
- **Options** (`DatabaseWriteOption`): `WithIfTableExists(IfExistsFail |
  IfExistsReplace | IfExistsAppend)`, `WithDialect(DialectSQLite | DialectPostgres |
  DialectMySQL | DialectSQLServer)`, and any `CollectOption`, which the query runs
  with.

**The modes** are pandas' `to_sql`, which Polars' default engine calls, probed at step
365:

- fail refuses a table already there, writing nothing, and creates one that is not;
- replace drops the table if it is there and creates it;
- append inserts, and creates a table that is not there.

**The dialect** (`internal/source/sqlrows/dialect.go`). `database/sql` has no name for
it, so it is named by `WithDialect`, or by the driver's package: a `*sql.DB`'s
`Driver()`, a `*sql.Conn`'s driver connection through `Raw`. modernc's and mattn's
SQLite, pgx and lib/pq, the MySQL driver, Microsoft's and denisenkom's SQL Server. A
`*sql.Tx` shows neither, and an unknown driver is refused, naming `WithDialect`. The
dialect gives:

- the quoting: `"a"`, `` `a` `` or `[a]`, the closing quote doubled within; a dotted
  name is a schema's table, each part quoted;
- the placeholders: `?`, `$n` or `@pn`;
- the columns' types, those `ReadDatabase` reads back as theirs: an Int64 a `BIGINT`
  (SQLite's `INTEGER`), a UInt64 a `NUMERIC(20, 0)`, a Float64 a `DOUBLE PRECISION`,
  text a `TEXT`, `LONGTEXT` or `NVARCHAR(MAX)`, an instant a `TIMESTAMP` or, with a
  zone, a `TIMESTAMPTZ` or `DATETIMEOFFSET(7)`, a Decimal a `DECIMAL(p, s)`. A List, an
  Array, a Struct and a Duration have none, and are refused;
- the question whether the table is there: SQLite's catalogue, PostgreSQL's
  `to_regclass`, MySQL's `information_schema`, SQL Server's `OBJECT_ID`;
- how many rows an INSERT takes: as many as its parameters allow, at most a thousand.
  SQLite's are 999, the least any build allows; SQL Server's 2,099.

**The write:**

- The query is compiled first, so a query that cannot run says so whatever the
  database: `TestEveryEntryPointRecovers`, which sweeps every entry point with a plan
  that panics, found the dialect refused first.
- Every column's type is checked, by building the `CREATE TABLE`, before any statement
  runs, whether or not the table is created.
- A `*sql.DB` or a `*sql.Conn` writes in a transaction of its own, committed at the end
  and rolled back on any error. A `*sql.Tx` is written in, and left to its caller.
- The rows go as the query yields its batches, into one prepared INSERT of the full
  size, and one of the rest at the end.
- **A value** is what `ToMaps` gives, but:
  - a time of day is its text, `03:04:05.123456`;
  - an Int128 is its text, and so is an unsigned integer past 2^63−1, which
    `database/sql`'s conversion refuses; SQLite, which holds no such integer, refuses
    it by name;
  - a date or an instant for SQLite is SQLite's text of one, which its date functions
    read: `2024-01-02`, `2024-01-02 03:04:05.123456`, with `+09:00` where it has a
    zone. modernc wrote a `time.Time` as Go's `String()`, and its reader parses a
    column declared `TIMESTAMP` alone, not `TIMESTAMPTZ`, so a zoned instant is a
    `TIMESTAMP` there;
  - an instant with a zone for MySQL is its UTC wall clock, which a `DATETIME` holds.

## 2. Tests

A recording driver (`databasewrite_test.go`) keeps each table's statement and rows,
logs every statement, answers the dialects' existence queries from its tables, and
undoes a rolled-back transaction.

- **`TestWriteDatabaseWritesEveryType`:** 150 rows of a Boolean, an Int8, an Int64, a
  UInt64, two floats, text with nulls, Binary, a Date, a Time, an instant with no zone
  and one in Tokyo, a Decimal and a Categorical, to SQLite and to PostgreSQL. The
  statement it created, the statements in order, 71 rows to an INSERT of 994
  parameters for SQLite, and every value as `ToMaps` gives it.
- **`TestWriteDatabaseModes`:** fail, replace and append, as pandas answers them.
- **`TestWriteDatabaseDialects`:** each dialect's quoting, placeholders, types and
  existence query, for a schema's table, and a null.
- **`TestWriteDatabaseRefuses`:** a Duration, a List and no columns refused before any
  statement; an unknown driver; 2^64−1 refused by SQLite and text for PostgreSQL.
- **`TestWriteDatabaseRollsBack`:** a failing INSERT undoes the write and the table it
  created; a caller's transaction is written in and not committed.
- **`TestWriteDatabaseQuotesNames`:** `a"b` and `main.t`, the rows of a filter, on two
  threads.

**A real SQLite, outside the repository** (the scratchpad's `sqlitert`, modernc
v1.61.0, so `go.mod` gains no driver):

- the dialect named by the driver;
- fail, then append, then fail refused;
- `ReadDatabase` reads back every value as it was written: Booleans, integers, floats,
  text with a null, Binary, a Date, both instants, as Datetimes in microseconds, the
  UTC one with no zone, by `ReadDatabase`'s rule for instants all in UTC;
- the Decimal comes back a Float64, SQLite's affinity keeping it as a REAL, which
  `feature-gaps.md` §9 records.

**Teeth: 16 of 16 bite:** a dialect's placeholders, quoting, parameters or zoned type
wrong; a table there not refused; replace without its drop; append never creating;
never committed; never rolled back; PostgreSQL's true not read; an unknown driver
taken for SQLite; the last rows not inserted; a time of day sent as a duration;
SQLite's dates sent as instants; 2^64−1 sent to SQLite; the types checked after the
transaction begins.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok, the root's rerun after `TestEveryEntryPointRecovers` was given a
`DatabaseWriter`; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
