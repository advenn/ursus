# Parquet fixtures

Files written by another engine, so ursus's reader is tested against a writer that is
not its own. Each is small enough to check in.

`pyarrow_lists.parquet` — pyarrow 25, `pq.write_table(t, path, compression='none')`,
one list column per element type audit.md's I17 named: Bool, Uint8, Uint16, Uint32,
Uint64, Decimal(10, 2) and Time(ms). Each column holds the same four rows: a list of
two (with a null element where the type allows), an empty list, a null list, and a
list of one. `parquet_nested_byhand_test.go` states the values.

`pyarrow_null_column.parquet` — pyarrow 25, `pq.write_table(t, path, compression='none')`,
three rows: `id` Int64 1, 2, 3; `nothing`, PyArrow's Null type, which it stores as
`optional int32 nothing (Null)`; and `name` String "a", null, "c". `nullcolumn_test.go`
reads it (audit.md I18).

`pyarrow_int96_flba.parquet` — pyarrow 25, `pq.write_table(t, path, compression='none',
use_deprecated_int96_timestamps=True)`, four rows: `ts`, INT96, which Spark writes,
holding 2024-01-02 03:04:05.123456, null, 1969-12-31 23:59:59.999999 and 1900-01-01;
and `fixed`, an unannotated FIXED_LEN_BYTE_ARRAY(4), holding "abcd", 00 01 02 03, null
and "zzzz". `int96flba_test.go` reads it (audit.md I16, `v0.4-scope.md` item 22).
