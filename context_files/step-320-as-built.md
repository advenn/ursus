# Step 320 — as built

**Tier 2, its first half:** Polars' `to_dummies`, `unstack`, and `transpose` named
from a column (`v0.6.7-scope.md` §6).

## 1. The API

```go
func (df *DataFrame) ToDummies(ctx context.Context, o DummiesOptions) (*DataFrame, error)
func (s *Series[T]) ToDummies(ctx context.Context, o DummiesOptions) (*DataFrame, error)
func (df *DataFrame) Unstack(ctx context.Context, o UnstackOptions) (*DataFrame, error)
// TransposeOptions.ColumnNamesFrom
```

**`ToDummies`,** as Polars 1.44.1's `to_dummies`, probed for every rule below:

- a `Uint8` column for each distinct value, 1 where a row holds it;
- a null is a value, every NaN one, and -0.0 is 0.0, named as it first appears;
- the dummies stand in their column's place, **ordered by name**, byte by byte, as
  Polars sorts them: `a_1, a_10, a_2, a_null`, and `c_Banana` before `c_apple`;
- `DropFirst` drops **the first value to appear that is not null**: of
  `[null, 5, 3]` it drops 5; a column of nulls keeps its null;
- `DropNulls` drops the null; `Separator`, `_` when empty, as `PivotOptions`', so
  Polars' `separator=""` cannot be asked for;
- a name twice, `c_a` beside a column `c_a`, or the text `"null"` beside a null, is
  refused, as Polars' DuplicateError;
- a List or a Struct column is refused, as a group key of one is.

**The names are Polars' display of each value,** AnyValue's `Display`, not its cast
to text (`polarsText`):

| Type | Polars displays | e.g. |
| --- | --- | --- |
| float | `fmt_float`: a whole number below 999999 with `.0`; past nine characters, scientific with four decimals outside 1e-6 to 999999, else six decimals with trailing zeros dropped; a whole one otherwise scientific | `1.0`, `1.0000e20`, `0.333333`, `1.234567e6`, `NaN`, `-inf`; a Float32 widened first |
| Date, Datetime, Time | chrono's: the fraction in 3, 6 or 9 digits, the fewest that hold it; a year outside 0–9999 signed; a zone's wall clock and abbreviation | `2024-01-02 03:04:05.120`, `+10000-01-01`, `03:04:05 CET`, `+05` |
| Duration | `fmt_duration_*`: whole days, hours, minutes, seconds, then the rest in the unit's largest exact one | `1d 1h 7µs`, `-1m -30s`, `0ns`; the micro sign U+00B5; a duration of seconds, which Polars lacks, as milliseconds |
| Binary | `b"…"`, every byte outside `!`–`~` as `\xhh`, a space too | `b"\x20~"` |
| Decimal, integers, Boolean, text, Enum | as written | `-0.05`, `18446744073709551615` |

ursus's own cast to text differs for floats, `1` for `1.0`, which `feature-gaps.md`
row 64 records; the names do not go through it.

**`Unstack`,** as Polars' `unstack`, its Python source read for the shape:

- vertically, `Step` rows and ⌈n / Step⌉ columns, column j the values from j·Step;
- horizontally, **`Step` columns** and ⌈n / Step⌉ rows, row i the values from
  i·Step;
- the slots after the last value null, or `Fill`: one value for all, or one a column,
  cast as `ExtendConstant` casts, so 2.5 is 2 in an Int64 and `"z"` null; a null
  already there stays;
- each suffix zero-padded to the digits of the count of new columns, as Python's
  `zfill`: `x_00`…`x_12` for 13, `x_00`…`x_09` for 10;
- an empty frame unstacks to no columns, or `Step` empty ones horizontally, where
  Polars refuses it by a logarithm of zero.

**`Transpose`:**

- `ColumnNamesFrom` names the columns by a text column, which is left out; a null
  refused, a number refused, a name twice refused, as Polars;
- every column meets at Polars' supertype, `dtype.Supertype`, so text beside a number
  or a Boolean is text, and a Boolean beside a number the number. It refused text
  beside a number until now.

## 2. How, and how fast

**`kernel.FirstSeenIDs`** numbers a column's values in the order each first appears,
a chunk of 64 keys to a group-by's hash table at a time:

- an integer, temporal or Enum column's values as integer keys;
- a float's bits as integer keys too, every NaN made one and -0.0 +0.0's;
- a String or Binary column's own bytes, raw;
- anything else encoded by the group key encoder.

**Past 2 × 65,536 rows it runs in parts,** one a core:

1. each part numbers its rows in a table of its own, in parallel;
2. the parts' values are numbered again in one table, part by part in order, each by
   the row it first appears at in its part, so a value's number is where it first
   appears in the whole column;
3. each part's ids are renumbered, in parallel.

**`dummies`** buckets the rows by value, a counting sort, so that each dummy writes
its own rows in order. The dummies are then made in parallel, each zeroed and
written by one goroutine. Only the distinct values are formatted.

**`Unstack`** slices a vertical column, sharing its values, and gathers a horizontal
one, O(n) in all. Polars extends, sorts by `index % columns` and slices.

**Speed.** One million rows, medians of seven, two rounds interleaved on a quiet
machine:

| | ursus | Polars 1.44.1 |
| --- | --- | --- |
| `to_dummies`, 10 strings and 100 integers | 20.5, 25.4 ms | 23.7, 26.7 ms |
| `to_dummies`, 10 strings | 8.6, 9.5 ms | 14.1, 14.4 ms |
| `to_dummies`, 1,000 floats: a gigabyte of dummies | 70.2, 71.8 ms | 92.1, 93.3 ms |
| `unstack`, vertical, a step of 1,000 | 0.4 ms | 1.6 ms |
| `unstack`, horizontal | 9.4, 8.8 ms | 29.1, 29.4 ms |
| `unstack`, vertical, filled | 0.4, 0.3 ms | 2.0, 1.9 ms |

**The first version was 1.5 to 1.8 times Polars',** one thread throughout. The
profile found two things:

- **A column without nulls read every row's bit.** `ursus.Values` builds a bitmap with
  every bit set that does not say so, so `IsAllSet` was false and every key went
  through the nullable path, a closure and a bit a row: 30% of the strings' time. The
  kernel counts the nulls now. The strings went from 34 to 24 ms.
- **The gigabyte of 1,000 floats' dummies was zeroed and written on one thread,**
  their writes scattered over a thousand columns: 55 ms zeroing and 50 ms writing.
  The counting sort and the parallel dummies made it 70 ms.

Polars runs the columns of a frame in parallel, ursus each column's work, which
serves one column as well as many.

**A memory guard.** A dummy is a byte a row, so a million values over a million rows
would be a terabyte. Past the default memory budget, half the machine's, `ToDummies`
refuses with `ErrResource` before making any, and `Unstack` refuses a padding past
it, a step of 2^31-1 on three rows. Polars would try, and the process be killed.

## 3. Found on the way

**The scratchpad draft was wrong in five places,** each caught by probing Polars
before it reached the repository:

- it ordered the dummies by value, a null last; Polars orders them by name;
- its `DropFirst` dropped the least value, where Polars drops the first non-null one
  to appear;
- its names were the cast to text, so `1.0` would have been `1`;
- it compared each row's text, so `0.0` was not `-0.0`'s;
- its horizontal `Unstack` made ⌈n / Step⌉ columns, right only when that equals
  `Step`, and it padded no suffix.

Its test would have panicked besides: it handed `int64` values to a helper asserting
`int`.

**The parallel kernel's test caught its own reference,** not the kernel: the map it
was checked against named 12.0 by `string(rune(48))`, `"0"`, which was 0.0's name.

## 4. Tests

- **`TestToDummies`:** the four answers of a frame, by name with `10` before `2`, the
  first dropped after a null, only nulls, text by its bytes, floats with -0.0 and
  NaN, a `Series`, nothing left, and the refusals;
- **`TestDummyNames`:** every type's names, 22 lists, each Polars' own;
- **`TestUnstack`:** vertical, horizontal, a column, two across ten rows, a step past
  the height, fills for all and each, a null kept, horizontal filled, the padded
  names, no rows, the refusals;
- **`TestTransposeAnswersAsPolars`:** text beside numbers, a Boolean beside a number,
  a List refused, names from a column and five refusals;
- **`kernel.TestFirstSeenIDs`:** each path, the empty string apart from the null, a
  NaN of either sign, all null, no rows, past a chunk; and past five parts on four
  paths, nulls among them, against a map's numbering;
- **the memory guards:** a million values over a million rows, and a step of 2^31-1,
  each refused with `ErrResource`; skipped where the default budget is past them.

## 5. Docs

- **Reshaping:** "Dummies" and "Unstack", with examples, and `Transpose`'s paragraph
  and example with `ColumnNamesFrom`.
- **Go values:** a `Series`' `ToDummies`.
- **Coming from Polars:** two rows.
- **The CHANGELOG, `feature-gaps.md`, `polars-parity.md`:** the features.

## 6. Teeth, all 15 biting

- the dummies in first-seen order, not by name;
- `DropFirst` dropping a null;
- a float's six decimals keeping their zeros;
- the micro sign as `us`;
- a fraction of milliseconds in six digits;
- a space unescaped;
- a year past 9999 unsigned;
- no zone abbreviation;
- -0.0 apart from 0.0;
- a null string keyed by its slot's bytes, the empty string's;
- a horizontal grid ⌈n / Step⌉ columns wide;
- suffixes unpadded;
- the first fill for every column;
- `Transpose` meeting at `Promote`;
- the names' column transposed too.

Three were rewritten:

- **two matched no source:** the micro sign's, whose `\u00b5` escape the file then
  held as the character, written back as the escape; and the null string's, written
  against the serial kernel before the parallel one replaced it;
- **one failed the build:** "suffixes unpadded" left `strconv` unused.

**Gate:** test-all 125 ok, race 25 ok (the root package 673 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
