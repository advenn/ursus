# Step 178 — as built

**R2 of `v0.6-scope.md`, its first three rows:** S26, S21 and I26 of `audit.md`.
Fixing I26 also found that struct tags had gone unread since the module's rename.

## 1. S26: `MinInt // −1` wraps. Closed, by decision

**Evidence:** asked of Polars 1.44.1 and of ursus, over Int64 and Int32:

| expression | Polars | ursus, before |
| --- | --- | --- |
| `MaxInt64 + 1` | MinInt64 | MinInt64 |
| `MinInt64 − 1` | MaxInt64 | MaxInt64 |
| `MinInt64 * −1` | MinInt64 | MinInt64 |
| `MinInt64 // −1` | MinInt64 | MinInt64 |
| `MinInt64 % −1` | 0 | 0 |
| Int32's `MaxInt32 + 1`, `MinInt32 // −1` | wrap | wrap |

The scope proposed refusing `//`, as Int128 arithmetic refuses. But ursus's
`+`, `−` and `*` on an integer wrap as Polars' do, and only an instant's or a
Duration's arithmetic is checked. Refusing `//` alone would make it the one integer
operator that refuses.

**The decision:** integer arithmetic wraps, two's complement, as Polars' and Go's do,
and `MinInt // −1` with it. It is now written in the arithmetic operators' doc
(`expr.go`), and `TestIntegerArithmeticWrapsAsPolars` pins every row of the table.
Checking every integer operation would be a different, wider change, and Polars
does not make it.

## 2. S21: `ToUpper("ß")` is `"ß"`. Fixed

`strings.ToUpper` maps a rune to a rune. Polars applies full Unicode case mapping, as
Rust's and Python's do:

- **upper case:** 102 characters become several. ß is SS, ﬁ is FI, ŉ is ʼN, and Greek
  with a subscript iota gains an Ι.
- **lower case:** İ is an i and a combining dot, and Σ is ς where it ends a word.

**The change** (`kernel/casemap.go`, `specialcase_gen.go`):

- **`upperExpansions`** is generated from the bench environment's Python 3.14.4,
  Unicode 16.0.0: every code point whose `str.upper` is more than one character. It
  is SpecialCasing.txt's unconditional mappings, as a Go table. `golang.org/x/text`
  has them too, but the README promises one dependency, and this costs 102 lines.
- **`fullUpper` and `fullLower`** fall back to `strings.ToUpper` and `ToLower` unless
  a string holds one of these characters. Bytes that are not UTF-8 are kept as they
  are.
- **The final sigma** is Unicode's rule: a cased letter before Σ and none after, each
  side looking past case-ignorable characters, which are marks, format characters,
  modifiers and the apostrophes, periods and colons a word holds.

**Checked against Polars:** 27 strings, the generated table's own cases among them,
and every final-sigma context: word ends, digits, punctuation, a mark or soft hyphen
on either side, and ΣΣ.

## 3. I26: `Rows` decodes no List. Fixed

`makeSetter` gains a slice case, `sliceSetter`:

- **A List decodes into a slice,** each element through its type's own setter. So a
  `[]*int64` sees a null element as nil, and a `[][]int64` decodes a list of lists.
- **A null list is a nil slice,** which a `*[]T` field tells apart from an empty one.
- **A Binary or String column decodes into a `[]byte`,** a copy.

## 4. Found: struct tags unread since the module's rename

`columnName` looked up the tag key `"github.com/advenn/ursus"`. The rename commit,
`fd89262`, was anchored on the literal `"ursus"` and rewrote this key with the import
paths. From then on, no `ursus:"name"` tag was read: a field matched a column only by
its own name, case-insensitively.

`ursus_test.go`'s tagged fields are named as their columns are, so they never showed
it. The key is `"ursus"` again. `TestRowsReadsTheUrsusTag` reads a column through a
tag that its field's name does not match, with an option after a comma, beside `"-"`.
The rename's other non-import rewrites are correct: the levels checker's module path,
and Parquet's stack-frame prefix.

## 5. Tests and teeth

- `TestIntegerArithmeticWrapsAsPolars`;
- `TestCaseMappingIsFullAsPolars`, 27 strings;
- `TestRowsDecodesAList`: a List, a pointer to each element, a list of lists, Binary,
  and a null list;
- `TestRowsReadsTheUrsusTag`.

| tooth | result |
| --- | --- |
| upper case rune by rune again | **bites:** the case test |
| a sigma final whatever follows it | **bites:** the case test |
| nothing case-ignorable | **bites:** the case test, once a mark between was added; it was silent before |
| İ lowered rune by rune | **bites:** the case test |
| the tag key the module path again | **bites:** both `Rows` tests |
| a null list decoded as an empty one | **bites:** `TestRowsDecodesAList` |
| `MinInt // −1` made null | **bites:** the wrap test |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
