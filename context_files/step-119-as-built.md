# Step 119 — as built

**What the audit of 0.4's Parquet work found, fixed.** The read-only review of steps
102, 108 and 112 traced two regressions, a wrong value at the edge of Datetime(ns),
and gaps in the check that keeps arrow-go's decoders from panicking.

## 1. The defects

| | what happened | why |
| --- | --- | --- |
| a struct with a Null first field | ErrInternal on a file that read before step 108, and on one ursus writes itself. PyArrow infers Null for a struct field that only ever holds None. | Step 108 read the NULL type by skipping its rows, which says nothing about whether the struct was present, and a struct asks its first field. |
| `List(Null)` | Written since step 108, and refused when read back. PyArrow's `list<null>`, which read as List(Int32), was refused too. | The list reader had no Null element. |
| a later file failing | `ScanParquetFiles([a, b]).Filter(…).Head(5)` failed when b could not be opened at scan time, though a's rows answer it. A `CollectBatches` consumer lost a's rows. | The parallel reader opens the next file, and reads later row groups' statistics, as soon as a worker is free, and returned that failure at once. |
| INT96 on 1677-09-21 | The day's start is below MinInt64. `day*perDay` wrapped, and a value came back in 2262; the earliest instant itself was refused. | The arithmetic took the day's start first. |
| encodings | An INT32 or INT64 column under BYTE_STREAM_SPLIT was refused, though arrow-go decodes it. A FIXED_LEN_BYTE_ARRAY under DELTA_BYTE_ARRAY, which parquet-mr's v2 writer produces, reached arrow-go's panic and was reported as a corrupt file. | The check knew one pairing, and had it backwards. |
| `nullCol`'s count | A truncated chunk of a Null column read as made-up rows. | arrow-go's `Skip` reports the count it was asked for. |

## 2. The fixes

- **`levelsOf`** reads only the levels of any typed chunk reader. The Null column now
  counts its rows from its definition levels and records its struct's presence as
  every other leaf does. `nullElems` is a List(Null)'s element accumulator, a count.
- **A failure while issuing is held** until the row groups already in flight are
  delivered. A panic while issuing becomes the file's error, held the same way.
- **The in-flight queue's head** is removed only if it is still the task Next waited
  on. The reviewer showed a concurrent Next, or Close racing a blocked Next, could
  otherwise skip a row group or panic. No current caller does either; this is
  defensive and has no test.
- **INT96 before 1970** is counted down from its day's end.
- **`decodable`** is arrow-go v18.7.0's table, by physical type, read from its
  `typed_encoder.go`:

  | type | encodings |
  | --- | --- |
  | integers | PLAIN, DELTA_BINARY_PACKED, BYTE_STREAM_SPLIT |
  | floats, FIXED_LEN_BYTE_ARRAY | PLAIN, BYTE_STREAM_SPLIT |
  | BYTE_ARRAY | PLAIN and both delta encodings |
  | all but BOOLEAN | dictionaries |
  | all | RLE, because the levels use it |

## 3. Tests and teeth

**Three PyArrow fixtures,** documented in `testdata/parquet/README.md`:

- a struct with a Null first field and a `list<null>`;
- an INT32 under BYTE_STREAM_SPLIT;
- a FIXED_LEN_BYTE_ARRAY under DELTA_BYTE_ARRAY.

**`parquetaudit_test.go`:**

- `TestNullFieldsInNestedColumns`;
- `TestParquetEncodingsByPhysicalType`;
- `TestALaterFileFailingCostsNoEarlierRows`: a `ScanParquetFrom` whose second file
  opens at plan time and fails at scan time. Without a limit, because a limit reads
  serially.

**`TestInt96Nanos`** gains:

- the earliest and the latest instant Datetime(ns) holds;
- the nanosecond either side of them;
- the earliest day's midnight.

| tooth | result |
| --- | --- |
| the Null column tracks no parent | **bites** |
| `List(Null)` not readable | **bites** |
| the integers refuse BYTE_STREAM_SPLIT | **bites** |
| FIXED_LEN_BYTE_ARRAY takes the delta encodings | **bites** |
| an issuing error returned at once | **bites:** both shapes |
| INT96's `day*perDay` again | **bites** |

**Gate:** with steps 118–122; see step 122.
