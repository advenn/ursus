# Step 255 — as built

**E5 of `v0.6.5-scope.md`, second part: string patterns as expressions.**
`Contains`, `StartsWith`, `EndsWith`, `StripPrefix`, `StripSuffix` and `Split` take
a pattern a row; `Replace` and `ReplaceAll` take a value a row.

## 1. What changed

**Eight call functions in the horizontal block,** `str.contains_expr` and the rest:

- each takes its constant form's arguments after the receiver, with the pattern or
  value a column; the flags stay literals;
- `strExprOut` types them: the receiver, pattern and value must be Strings, and the
  answer is the constant form's type, read from a table of the constant forms;
- `replace`'s pattern stays a constant, as Polars' does: Polars 1.44 refuses a
  per-row pattern there ("dynamic pattern length").

**The kernel (`internal/kernel/strexpr.go`), `strEach`:**

- a null value, pattern or replacement gives null;
- a regex pattern is compiled once for each distinct pattern in a batch, with the
  cache starting over past 256. An invalid one is an error naming the pattern and
  its row in the batch;
- each row otherwise answers as the constant form, with the same `strings` calls;
- a per-row value is inserted as it is written, `$0` and all, as Polars inserts it,
  where a constant replacement expands `$1` under a regex.

**One difference from Polars:** a null replacement value gives null, as every
string function here does for a null operand. Polars keeps the string as it was.
`feature-gaps.md` §9 records it.

**Public:** the eight methods are generic over `string | Expr` in the pattern, or in
the value for the replaces. A string compiles to the constant call as before.

## 2. Tests and teeth

**`TestStringPatternsAreExpressions`.** Eight rows with a null in each column, a
regex that is not its own literal (`a+`, `.`), and an empty pattern, at batch sizes
1, 3 and 8192:

- contains as a regex and as a literal;
- starts_with and ends_with;
- strip_prefix and strip_suffix;
- split, joined and counted;
- replace and replace_all of a value a row, literal and regex;
- a `$0` in a value a row, inserted as written.

Every answer is Polars 1.44.1's, except the null value.

**`TestStringPatternsRefuse`:**

- an invalid regex in a row, named with its row;
- an integer pattern, value and receiver;
- an invalid constant pattern beside a value a row;
- the same invalid pattern taken as literal text, which is no error.

| tooth | result |
| --- | --- |
| contains is always literal | **bites** |
| starts_with is ends_with | **bites** |
| a null value replaces with nothing | **bites** |
| a value a row expands $0 | **bites** |
| split takes the first row's pattern | **bites** |
| the cache answers every pattern with the first | **bites** |
| a bad pattern is not named | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
