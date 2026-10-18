# Step 274 — as built

**B of `v0.6.6-scope.md`: the binary namespace.** These are Polars' `bin.contains`,
`starts_with`, `ends_with`, `get`, `head`, `tail`, `slice`, `size`, `encode`,
`decode` and `reinterpret`. `Str()` now refuses a Binary column, as Polars' `.str`
does, where it read one as text and could split its bytes.

## 1. What changed

**`Str()` refuses Binary, a break:**

- **The cause:** a Binary column is stored as a String's is, offsets and bytes, and the
  string family's gate asked only for that storage. So `Str()` took a Binary.
- **The harm:** the byte-safe functions answered rightly: `LenBytes`, `StartsWith`,
  `Encode`. But a text function read the bytes as runes, and `Str().Slice(1, 2)` of
  `00 ff 61 62` gave the String `"�a"`, its byte `ff` replaced.
- **The fix:** the gate, the per-row string functions' typing and `to_integer`'s ask
  for a String or an Enum, as Polars asks for a String. The refusal names `Bin()`.
- **Fallout:** no test or plan of ursus's read a Binary through `Str()`. The
  CHANGELOG lists the break.

**`Bin()`** (`expr_bin.go`), every function counting bytes. A String is refused, as
Polars refuses one; `.Cast(ursus.Binary)` is its text's bytes.

- **`Contains`, `StartsWith`, `EndsWith`:** the pattern is bytes, text or an
  expression of either, a row's own; a null pattern is null.
- **`Get(index, nullOnOOB)`:** a UInt8.
  - The index is a constant or an expression, from the end where negative.
  - An index past a value is refused, naming its row, or with `nullOnOOB` null, as
    Polars has it. Polars refuses even an empty value's `get(0)`, and so does this.
- **`Head(n)` and `Tail(n)`:** n bytes from the start or the end, or where n is
  negative all but |n| from the other end. n is a constant or an expression; a null n
  is null.
- **`Slice(offset, length...)`:** each argument an int or an Expr.
  - A negative offset counts from the end. Where it lands before the start, the
    length is cut by as much: `Slice(-6, 3)` of "hello" is "he", `Slice(-10, 3)`
    nothing.
  - No length, or a null one, is the rest. A null offset is null; a negative length
    is refused.
  - All 18 of Polars' slices, heads and tails of "hello" are tested.
- **`Size(unit)`:** bytes as a UInt32, or `SizeKB` to `SizeTB`, powers of 1024, as an
  exact Float64.
- **`Encode` and `Decode`:** step 271's kernels, which read the storage alike.
- **`Reinterpret(dt, endianness)`:**
  - each value's bytes as one value of an integer, Int128 too, a float, a Date, a
    Datetime, a Duration or a Time;
  - little- or big-endian;
  - a value of another width than dt's is null, as Polars makes it;
  - any other type is refused, as Polars refuses it.

**How they are built:**

- **One-operand functions** (`Encode`, `Decode`, `Size`, `Reinterpret`): a binary
  family of their own at 1100, with an `IsBinary` classifier and an arm in
  `ResolveCall` and in `evalCall`. `kernel.BinCall` and `binCallOut` run them.
- **Per-row functions** (`Contains`, `StartsWith`, `EndsWith`, `Get`, `Slice`): they
  join the horizontal block. One kernel takes a constant or an expression alike, a
  literal operand read at every row, so each takes either, as Polars does. `Head` and
  `Tail` are `FnBinSlice`'s modes.

**The tests' sweeps:**

- **The names tests:** the range of 1100, the binary family, sweeps to 1200, and 197
  functions.
- **The contract sweep:** runs to 1200 and drives the binary family. Its fixture's
  Binary column is now refused by every string function and answered by every binary
  one, both halves agreeing.

**Not built:** `reinterpret` to an Array waits for the Array type (`v0.6.6-scope.md`
§6.1).

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestBinMethods`:**
  - over Polars' frame:
    - contains, starts_with and ends_with, constant and a row's;
    - get, from the end and a row's;
    - head, tail and slice, each of all but and a row's;
    - size in bytes and kilobytes;
    - encode both ways;
  - the 18 slices of "hello";
  - nulls in each argument;
  - get past both ends;
  - decode of hex and base64 text;
  - six refusals, `Str()` of a Binary among them.
- **`TestBinReinterpret`:**
  - Int32, UInt32, Int16, Int64, UInt8, big-endian and Date over values of three
    widths;
  - Float64's denormal and 1.0;
  - Float32's least denormal and a NaN;
  - Int128 both ways, `2^120` big-endian, checked against Polars;
  - Boolean and String refused.

| tooth | result |
| --- | --- |
| an offset before the start keeps its length | **bites** |
| head of all but is head of n | **bites** |
| tail of all but is all | **bites** |
| a null length is none | **bites** |
| get from the end is out of bounds | **bites** |
| get past the end is null | **bites** |
| reinterpret reads a prefix | **bites** |
| big-endian is little | **bites** |
| Int128's halves kept | **bites** |
| a kilobyte is 1000 bytes | **bites** |
| starts_with is contains | **bites** |
| Str reads a Binary | **bites** |
| Bin reads a String | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
