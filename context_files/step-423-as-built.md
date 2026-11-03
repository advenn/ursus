# Step 423 — as built

**A Parquet footer is measured before anything recurses on its schema.** H1 of
`v0.7.2-scope.md`, the plan's 421 and 482, which the critic moved into M2 on this
evidence.

## 1. What was wrong

A Parquet schema is a flat list of elements in the footer, each group saying how many
children follow. Two readers built its tree by recursing once a level:

- **ursus's own naming of a column's type,** `nodeType` and `structType`: a crafted
  footer of 1.6 MB, 200,000 optional groups each holding one, overflowed a 64 MB
  stack, `fatal error: stack overflow`, which no recover catches. Run at step 423;
  at Go's default 1 GB a footer about sixteen times deeper.
- **arrow-go's `schema.FromParquet`,** inside `file.NewParquetReader`, before ursus
  sees a schema: two million levels overflowed it, so a check in ursus's naming alone
  only raised the bar.

A service reading a caller's Parquet file ended by a file of some megabytes.

## 2. The change

- **`measureFooter`** (`internal/source/parquet/footer.go`), in `openFile` before
  arrow-go's reader: it reads the file's last 8 bytes and its footer, as arrow-go
  does, walks the footer's Thrift compact encoding with a reader of its own, takes
  each schema element's `num_children`, and computes the schema's depth with a stack
  of its own, so a schema of any depth is measured without recursing. A column nested
  past 1,000 levels, its leaf counted, the JSON readers' limit (step 412), is an
  `ErrUnsupported`; a footer the walk cannot read, or one nesting structs or lists past
  64 levels in any field, is an `ErrIO`, corrupt.
- **No read more:** arrow-go is handed a reader whose footer and last 8 bytes come
  from what the walk read, so a file in an object store costs the same two ranged
  reads as before, which `TestScanParquetFrom`'s count of bytes holds.
- **Left as they were:** a file too short, or not ending in a plain footer, an
  encrypted one's `PARE`, is left to arrow-go, which says what it is. A check inside
  `fileSchema` was written first, and taken out once the walk made it unreachable.
- **Not changed:** Arrow IPC, whose loader in arrow-go refuses past 64 levels; a
  caller's own `RecordReader`, whose types are the caller's.

## 3. Tests

- **`TestAParquetSchemaNestedPastTheLimitIsRefused`** (root), over footers written by
  hand in the test: two million groups refused as nested past the limit, where it
  ended the binary in ursus's naming and then in arrow-go's; 1,000 groups around a
  leaf refused, 999 read; a footer holding a million structs deep in an unknown field
  refused as corrupt.

**Teeth: 4 of 4 bite:** the footer not measured, and its depth not counted, end the
binary by the overflow; the limit one level wide reads 1,000 groups; `skip`
unbounded overflows on the million structs. A second bound, in `fields`, was silent,
`skip` bounding every path, and was taken out.

**Gate:** the Parquet package's tests; the root package's Parquet, hive, dataset and
source-seam tests, the count of bytes a caller's reader is asked for among them; the
22 PDS-H answers at SF=0.1 and SF=1 match DuckDB's, DuckDB's own footers walked; vet
and levels. CI's every width, the scalar build and the race detector on the pull
request.
