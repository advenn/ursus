# Step 81 — as built

**Nested data round-trips through Parquet.** This is `v0.3-scope.md` §2.2 and §3
item 3: ursus read List and Struct columns from Parquet and could write neither back.
It also fixes `audit.md` I17, the reader's own refusal of lists of several element
types, because a writer whose output its own reader rejects is worse than one that
refuses. It was the eighth step of the road to 0.3.

Six commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference. Flat writing did
  not move.

No benchmarks.

**The suite is 2835 passing tests and subtests** (2814 at step 80).

---

## 1. What was wrong

- **`SinkParquet` refused every nested column**, with `cannot write column "l" of type
  List(Int64) to Parquet`.
- **The reader** reads a List of primitives in the standard three-level encoding, and
  a Struct of primitive fields. It refused a list of Bool, Uint8/16/32/64, Decimal,
  Int128 or Time(ms), under a hint claiming the unsigned and Time types were read
  (I17).

## 2. Evidence first

**E1 is `parquet_nested_byhand_test.go`**: 19 cases.
- **Ten write a frame and read it back:** lists of Int64, String, Float64 with NaN,
  Bool, Uint8, Uint64, Decimal and Time(ms); a struct with a null struct and null
  fields; and lists split across row groups of two.
- **Seven read `testdata/parquet/pyarrow_lists.parquet`**, a 2.6 KB file pyarrow 25
  wrote with one list column per I17 element type, so the reader is tested against a
  writer that is not ursus.
- **Two are controls:** List(List) and List(Struct) are refused by name.
- **17 were wrong.**

**E2 is `TestParquetNestedRoundTrips`**, generated.
- **Lists:** 23 element types — the integers, Int128, the floats, Bool, String,
  Binary, Date, Time and Datetime at three units, and two Decimals — each as a list
  holding a null list, an empty list, one value, one null, and several with a null
  among them.
- **Row groups:** sizes 1, 2, 3 and 1000, so a list is cut wherever a boundary can
  fall.
- **Structs:** a struct with a field of every type, at three row-group sizes.
- **Total:** 95 routes, all refused at write.

## 3. The fixes

1. **I17: `newListElems` gains arms.**
   - Uint8/16/32 and TIME(MILLIS) from INT32 storage, and Uint64 from INT64, as a flat
     column's reader converts them.
   - Decimal from INT32, INT64 or FIXED_LEN_BYTE_ARRAY, and Int128 written as
     DECIMAL(38, 0).
   - **Bool through a new `boolElems` accumulator**, which builds a bitmap where the
     others build a buffer.
2. **The writer writes the shapes the reader reads:**
   - **A List** of any primitive the flat writer writes, as
     `optional group (LIST) { repeated group list { optional element } }`. That is the
     standard encoding PyArrow and Polars write.
   - **A Struct** of primitive fields, as an optional group.
   - **Deeper nesting is refused by name**, as the reader refuses it.
3. **`shred` splits each column into its Parquet leaves**, with Dremel's levels for
   that schema:

   | | definition level | repetition level |
   | --- | --- | --- |
   | struct field | 2 present, 1 a null field, 0 a null struct | — |
   | list element | 3 present, 2 a null element, 1 an empty list, 0 a null list | 0 opens each row, 1 after |

   - The leaf writers take a leaf's column, the indices of its present values, and its
     levels.
   - A row group's columns count **leaves**, not top-level columns.

**Checked once against other engines:** pyarrow 25 and Polars 1.44 read a nested file
ursus wrote — lists of Int64, String, Bool, Uint8 and Float64 with Inf, and a struct,
across two row groups — value for value. That covered null lists, empty lists, null
elements, a null struct and a null field.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**10 of 11 bite.**

| reintroduce | fails |
| --- | --- |
| rep 1 for a row's first element / an empty list as null / a null element as present | the seven by-hand list round trips, each |
| a struct's null not carried to its fields | the struct cases |
| the leaf counter not advanced | the struct cases and five flat Parquet tests |
| deeper nesting not refused | both controls (re-aimed after a first build failure) |
| the unsigned / Time(ms) / Bool / FLBA list arm dropped | its by-hand case, its pyarrow case and the sweep |

**Silent, and why:** the Bool case I added to the reader's schema gate,
`readableElem`. A Bool's bit width is 1, so `IsFixedWidth` already admitted it. The
case was dead code, and a commit removed it.

## 5. Behaviour changes

- **`SinkParquet` and `WriteParquet` write a List of a primitive and a Struct of
  primitive fields.**
- **Lists of Bool, the unsigned types, Decimal, Int128 and Time(ms) are read.**
- An Int128 element, like a flat Int128, reads back as Decimal(38, 0). That is the
  documented asymmetry.

## 6. Still open

- **Deeper nesting**, both ways.
- **CSV** has no representation for nested columns, and refuses them.
- The rest of the road to 0.3: the spill claim next.
