# Parquet fixtures

Files written by another engine, so ursus's reader is tested against a writer that is
not its own. Each is small enough to check in.

`pyarrow_lists.parquet` — pyarrow 25, `pq.write_table(t, path, compression='none')`,
one list column per element type audit.md's I17 named: Bool, Uint8, Uint16, Uint32,
Uint64, Decimal(10, 2) and Time(ms). Each column holds the same four rows: a list of
two (with a null element where the type allows), an empty list, a null list, and a
list of one. `parquet_nested_byhand_test.go` states the values.
