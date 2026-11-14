# Step 518 — as built

**A Struct with no fields keeps its rows.** CI's fuzzing of step 511's `FuzzScanJSON`
(run 43641515921) found that a JSON or NDJSON object field holding `{}` failed the
whole read with ursus's internal error. A struct column carries its own row count now,
through every path that builds, reads, writes or rebuilds one, and a Struct of no
fields, Polars' `Struct({})`, reads, keeps its rows, is written to IPC and JSON, and is
a key. Parquet and CSV refuse it cleanly, as Polars does. The bug is on the
release-0.7 line too; §7 says what a backport needs.

## 1. The bug

```
json   [{"a":{}}]                        -> ursus: data: batch declares 1 rows but its columns hold 0 (this is a bug in ursus)
json   [{"a":{},"b":1}]                  -> ursus: data: batch column "b" has 1 rows, but column "a" has 0
ndjson {"a":{},"b":1}\n{"a":{},"b":2}\n  -> ursus: data: batch column "b" has 2 rows, but column "a" has 0
```

`internal/data`'s `NewStruct` took the struct's length from `fields[0].Len()`, so a
struct of no fields was zero rows long whatever its frame's height. `"metadata": {}` is
a common shape in real JSON. Polars 2.1.0 reads each as a `Struct({})` column of the
right height, `{}` or null a row; `{"a":{"x":1}}` beside `{"a":{}}` reads as
`Struct({'x': Int64})`, x null in the second row, which ursus already did.

Four places knew the rule and refused around it: Arrow import refused a struct of no
fields (step 60), `kernel.NullColumn` refused a null column of one, the internal
`struct.select_fields` refused no name (step 299), and `dtype.IsHashable` kept a
struct of no fields from being a key (step 344). Each reason is gone, and each refusal
is lifted (§2).

## 2. Every path audited, and what each needed

The constructor first. `data.NewStructLen(name, n, fields, valid)` is `NewStruct` told
its length; `NewStruct` keeps its signature and takes the length from its first field,
or, with none, from its validity's length. `NewStructLen` keeps the fields a non-nil
slice even when there are none: a nil one is `data.NewNull`'s payload-free column,
which a dozen kernels read as all null (`IsPayloadFree`; the JSON writer wrote such a
column as `null` a row). Every one of the 22 production callers of `NewStruct` calls
`NewStructLen` now with the length it knows; the 24 test callers keep `NewStruct`,
each with a field.

| path | where | what it needed |
| --- | --- | --- |
| NDJSON and JSON reads | `source/ndjson/build.go`, `structBuilder.finish` | the length from the builder's row count, `b.valid.n` |
| NDJSON inference | `source/ndjson/infer.go` | nothing: `{}` infers `Struct()`, and beside an object of fields that struct, as Polars 2.1 |
| `str.json_decode` | `kernel/strjson.go` | the batch's length; the oracle's loader builds its `Struct()` columns through it |
| Arrow import | `arrowin/types.go`, `structOf`; `arrowin/builder.go` | the refusal of a struct of no fields lifted; `finish` from the builder's row count. `TestTypeRefusals` loses that case; `TestAStructOfNoFieldsKeepsItsRows` holds a generated arrow-go array's rows and nulls, whole and windowed, and its export |
| Arrow export, IPC | `arrowout.columnData` | nothing: an Arrow struct is told its length, `c.Len()` |
| Parquet writer | `source/parquet/types.go`, `nestedNode` | a Struct of no fields, at any depth and as a list's element (`holdsEmptyStruct`), is refused first, by name: it would be a group of no columns, which Parquet has not and Polars refuses |
| Parquet reader | `source/parquet/nested.go`, `structCol.finish` | the length from the rows read, `c.rows`. A file's group of no children is declined at schema time by `structFieldLeaves`, unchanged |
| the spill format | `spill/spill.go`, `getColumn` | the reader's struct from the batch's `n`; the writer writes a struct's validity and no field, unchanged |
| take (filter, sort, join, gather, shift) | `kernel/take.go`, `takeStruct` | the selection's length |
| concat | `kernel/take.go`, `concatStruct` | the parts' total |
| a null column (a join's missing side, a diagonal concat, `When`) | `kernel/take.go`, `NullColumn` | its refusal of a struct of no fields lifted: n null rows of none |
| slice | `data.Column.Slice` | nothing: it sets the length, and keeps the fields a non-nil slice |
| `Struct(...)`, `with_fields` | `kernel/horizontal.go` | the batch's length |
| `rename_fields`, `select_fields`, `drop` | `kernel/structfn.go` | the struct's own length |
| `select_fields` typing | `expr/callnested.go` | its refusal of no name lifted, so `MatchToSchema` meets a schema's `Struct()` (`match_schema_to_none`); the evaluator contract gains `,none` |
| `Name().PrefixFields` and kin | `physical/eval.go`, the `Rename` of fields | the struct's own length |
| `field` | `kernel/structfn.go` | nothing: a name the struct has not is refused at typing, `no field "x" in Struct()` |
| unnest | `physical/unnest.go`; `Expr.Unnest` | nothing: the operator builds its batch with the input's rows, so a struct of no fields unnests to no column and keeps the frame's height |
| `list.to_struct` | `kernel/listfn.go` | the list column's length |
| `value_counts`, `rle`, `hist` | `kernel/listmode.go`, `listhist.go` | their element counts (two or more fields each; the length is stated, not read) |
| `split_exact`, `extract_groups`, `splitn` | `kernel/strmany.go` | the batch's length |
| casts | `kernel.Cast`, `dtype.CanCast` | nothing: `Struct()` to itself is the identity, a null to it is `NullColumn`'s; any cast between struct types of other fields is refused, as before |
| keys | `dtype.IsHashable`; `kernel/groupkey.go`, `structKey` | a struct of no fields is hashable: its key is its own validity's tag, the fields writing nothing, so `{}` is one group and null another, as Polars keys them. Group-by, `Unique`, `Join`, `Over`, `NUnique`, `IsDuplicated`, `Mode`, `Rle` and `Hash` take one |
| a frame's display | `frame.go`, `renderCell` | nothing: `{}`, and `null` |
| a row as Go values | `rowsout.go`, `cellReader`; `rows.go` | nothing: an empty map, nil for a null; `CollectInto` fills a `*struct{}` and a `struct{}` |
| the JSON writer | `jsonwrite.go` | nothing, given the non-nil fields: `{}` or `null` |
| `struct.json_encode` | `kernel/jsonenc.go` | nothing: `"{}"`, and a null for a null struct, ursus's documented answer where Polars writes `"null"` |
| the CSV writer | `csv.go` | nothing: it refuses a `Struct()` column as any nested one |
| `ursustest.AssertFrameEqual` | `ursustest/support.go`, `renderStruct` | nothing: `{}` against `null`; `TestAStructOfNoFieldsIsCompared` holds a generated validity equal to itself and apart from itself with a row flipped and a row longer |
| `FromRows` | `fromrows.go` | a `struct{}` field (or `*struct{}`, nil a null) was zero rows; the rows' count now |
| `DataFrame.ToStruct` | `frameconv.go` | a frame of no columns gave zero rows; the frame's height now, and the doc says so |

The public constructors that build a Struct from fields, checked with none:

- `Struct(exprs...)` refuses none, as Polars' `pl.struct()` refuses it; its doc now
  says so, and names `Str().JsonDecode(StructOf())` and JSON's `{}` as where a struct of
  no fields comes from. The signature is not reshaped.
- `DataFrame.ToStruct` of a frame of no columns, and `FromRows` of a `struct{}` field,
  give a struct of no fields of the frame's rows (above).
- `StructExpr.Drop` of every field stays refused, as its doc says. The reason its doc
  gives, "a struct of no fields has no length of its own", no longer holds; lifting the
  refusal would be an addition, the maintainer's call.
- `Str().ExtractGroups` of a pattern of no group and `SplitN` of none stay refused
  (`TestManyPatterns`), where Polars 2.1 answers a Struct of no fields; the same.

The guide's structs section (`docs/guide/lists-and-structs.md`) gains a paragraph.

## 3. The recorded group: `emptystruct`

`bench/scripts/oracle/emptystruct.py`, registered in `oracle_emptystruct_test.go`,
recorded by `make oracle GROUPS=emptystruct` under Polars 2.1.0: 66 cases, 6 of them
refusals, 179 KB.

- **19 reads**, as `ndjsonread` and `jsonread` read: `{}` alone and beside columns, in
  an array and as one object, under the empty key (CI's input), beside an object of
  fields either way round, inside a struct beside a field and alone, inside a list
  (an empty list and a null one beside), null and a missing key against `{}`, a schema
  given, and 700 generated lines of `{}`, null and a missing key.
- **44 questions of frames** holding a `Struct()` column, a generated frame of 40 rows
  (`Gen(5180)`) and two of a nested `Struct(e: Struct(), n: Int64)` and a
  `List(Struct())`: filter, filter on it, head, tail, slice, the struct alone, sort
  either way, reverse, concat and concat of it alone, an inner and a left join
  carrying it, `unnest` and `struct.unnest()`, a cast to itself, `is_null`, the counts,
  first and last, shift, gather, implode, `drop_nulls`, group-by with it a value, a
  list and a key, unique, `with_fields`, `rename_fields`, `match_to_schema` either way,
  explode, a nested field and sort, and IPC, IPC stream, nested and listed IPC read
  back.
- **3 written texts**: `write_ndjson` and `write_json`, and a nested struct's NDJSON.
- **6 refusals**, each held to ursus's own refusal: `struct.field("x")` (`"x"`), a
  cast to String (`cannot cast Struct() to String`), CSV (`of type Struct() to CSV`),
  and Parquet of the struct, of a struct holding it and of a list of it (`no fields`;
  Polars: "Unable to write struct type with no child field to Parquet").

The loader learnt the type: `oracleDType` reads `Struct()` as a struct of no fields, and
`oracleEmptyStructFrame` builds one by `JsonDecode` of `{}` texts, since `Struct()`
needs an expression. Not recorded, with the reason: a cast to a struct of other fields,
which Polars answers by filling or dropping fields and ursus refuses as it refuses
every cast between struct types (`dtype.CanCast`); `struct.drop` of every field,
refused by its doc; `struct.json_encode` of a null struct, a documented difference.

Asked before the fix, the cases failed as §1 says: the reads with the bug's error, the
frames' loader with `produced 0 rows for a 40-row batch`, the joins with `cannot build
a null column of Struct()`, the key with `cannot group by col("a"), which is a
Struct()`. After it, all 66 pass.

## 4. The differential

`TestEmptyStructDifferential` (`emptystruct_test.go`) draws ten frames from a seed, 20
to 319 rows: an id `i`, `b` and `k` with nulls, and a witness `w`; beside them `a`, a
Struct of no fields, `{}` where `w` and null where not, built five ways in turn: by
`JsonDecode`, read from NDJSON and from JSON (a missing key half the nulls), by
`FromRows` of a `*struct{}` field, and through an IPC file. Each frame, with `a` and
without it, goes through 33 operations at the default batch size and at 7: filter,
filter on `w`, head, tail, slice, sort either way, reverse, concat, `WithColumns`,
inner, left and full joins, a join with it on the build side, a join on it (nulls
equal) against one on `w`, unnest and `struct.unnest()`, a cast, `is_null` against
`!w`, shift, `drop_nulls` against a filter on `w`, the struct alone, a group-by with it
a value (first, last, count against `w`'s sum), as lists exploded back, as a key and
as one of two keys against `w`, unique, `Over` it against `Over` `w`, implode and
explode, and the IPC, IPC stream, NDJSON and JSON writers read back. Each answer has
the height and the other columns of the same question's without `a`, and `a` is null
exactly where `w` is not true (`esSame`). Parquet and CSV refuse the frame, never with
`ErrInternal`, and write it without `a`. The rows `ItemAt` reads, the display's line
count, `ToStruct` unnested back and `Gather` are held to the frame without it. A frame
of 6,000 rows is sorted (both ways), grouped, and joined with it on either side under
64 KiB in batches of 64; the sorts and the build-side join must spill, and do. Each
trial is drawn inside its own subtest, so one that cannot build its frame fails alone.
It runs in 2.8 s.

## 5. Teeth

`internal/data/column.go` was saved, and the old rule put back where the length is
set: `n = 0`, then `fields[0].Len()` where there is a field, at the top of
`NewStructLen`, and `NewStruct`'s `n := 0`. Under it:

| test | result |
| --- | --- |
| the seed `FuzzScanJSON/empty-object-field` | fails: `batch declares 1 rows but its columns hold 0` |
| the seed `FuzzScanNDJSON/empty-object-beside-repeated-keys` | fails: `batch column "1" has 0 rows, but column "0" has 1` |
| `TestOracle/emptystruct` | 58 of 66 fail; the 8 that pass build no struct of no fields: the three reads of `{}` beside an object of fields, whose struct has a field, and five refusals typed before any column is built (a field, a cast, Parquet three times) |
| `TestEmptyStructDifferential` | all 11 subtests fail, each source's frame refused as it is built (`produced 0 rows for a 80-row batch`, `batch column "a" has 0 rows, but column "i" has 235`) |
| `TestAStructOfNoFieldsKeepsItsRows` | fails: `a Struct() of 0 rows` |
| `TestAStructOfNoFieldsIsCompared` | fails: `a frame of a Struct of no fields, 30 rows, has 0` |

The file was restored from the saved copy, never by git, and its SHA-256 checked
equal to the copy's.

## 6. Fuzz

Two seeds from CI's fuzzing are kept: `testdata/fuzz/FuzzScanJSON/empty-object-field`,
`[{"":{}}]` (run 43641515921), and `testdata/fuzz/FuzzScanNDJSON/empty-object-beside-repeated-keys`,
`{"0":0,"0":0.0,"0":true,"0":"0","0":null,"0":[0,0],"1":{}}` (PR #69's run, the same
bug through NDJSON). Both pass. Each target was then fuzzed 20 s, one at a time,
under the shared lock, `-fuzzminimizetime=3s`, with no failure:

| target | executions | new interesting | corpus |
| --- | --- | --- | --- |
| `FuzzScanJSON` | 26,331 | 14 | 820 |
| `FuzzScanNDJSON` | 46,525 | 18 | 739 |

The fuzzers' own cache keeps what they found; nothing new is checked in.

## 7. What a backport to 0.7 would need

release-0.7 has the same 22 callers of `NewStruct` (its Parquet struct in
`source/parquet/column.go`, its `select_fields` typing in `expr/call.go`, before steps
493 and 505 split them) and the same four refusals. It has neither the fuzz target
nor the recorded oracle (steps 496–511 are master's).

- **The fix:** `NewStructLen` in `data/column.go`, the 22 callers, `NullColumn`'s and
  Arrow import's refusals lifted, and the Parquet writer's refusal by name. That alone
  makes `{"a":{}}` read and keep its rows.
- **The additions,** which a patch release may leave on master: a struct of no fields
  as a key (`IsHashable`), `select_fields` of no name (`MatchToSchema` to `Struct()`),
  and the docs' paragraphs.
- **The tests:** `TestEmptyStructDifferential`, `TestAStructOfNoFieldsKeepsItsRows` and
  `TestAStructOfNoFieldsIsCompared` port as they stand if 0.7 has each API they use
  (`ScanJSONBytes`, `FromRows` tags, `WriteIPCStream`, `JoinNullsEqual`,
  `FillNullWith`); the recorded group needs the oracle harness, which 0.7 lacks, and the
  seed needs readfuzz.
- A v0.7.3 CHANGELOG line, and the forward-port note on master's.
