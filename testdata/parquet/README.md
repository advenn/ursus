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

`pyarrow_null_nested.parquet` — pyarrow 25, `pq.write_table(t, path, compression='none')`,
three rows: `s`, a struct of `a`, PyArrow's Null type, and `b` Int64, holding {a: None,
b: 1}, a null struct, and {a: None, b: 3}; and `l`, a `list<null>`, holding [None, None],
[] and a null list. PyArrow infers Null for a field or element that only ever holds
None. `parquetaudit_test.go` reads it (step 119).

`pyarrow_bss_int.parquet` — pyarrow 25, `compression='none', use_dictionary=False,
column_encoding={"i": "BYTE_STREAM_SPLIT"}`: `i` INT32 1, -2, 3, 400000, under an
encoding arrow-go decodes for integers; and `f`, FIXED_LEN_BYTE_ARRAY(4), plain.

`pyarrow_flba_delta.parquet` — pyarrow 25, `compression='none', use_dictionary=False,
column_encoding={"f": "DELTA_BYTE_ARRAY"}`: `f`, FIXED_LEN_BYTE_ARRAY(4), "abcd",
"efgh", "ijkl", "mnop", under an encoding arrow-go does not decode for that type and
panics on. `parquetaudit_test.go` reads both (step 119).

`pyarrow_small_pages.parquet` — pyarrow 25, `compression='none', data_page_size=256,
use_dictionary=['dict'], write_batch_size=50`: 3,000 rows of two String columns, `dict`
(RLE_DICTIONARY) and `plain`, each holding "v%04d" of i*7 mod 1000, and null where i is
a multiple of 13. Its 256-byte pages put dozens in every batch the reader reads.
`readerroundtrip_test.go` reads it (step 124).
