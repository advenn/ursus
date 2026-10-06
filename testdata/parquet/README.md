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
