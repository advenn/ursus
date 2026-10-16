# Step 270 — as built

**S of `v0.6.6-scope.md`, first half: the string namespace's matching, splitting and
forms.** These are Polars' `contains_any`, `replace_many`, `extract_many`,
`find_many`, `extract_groups`, `split_exact`, `splitn` as a Struct,
`split(inclusive=True)`, `to_titlecase` and `normalize`. The second half, the
encodings, `to_integer(base)`, `replace`'s count, `concat_str`'s `ignore_nulls` and
JSON, is step 271.

## 1. What changed

**Many patterns at once** (`internal/kernel/strmany.go`):

- **`manyMatcher`** finds literal patterns by Polars' three semantics:
  - **standard, the default:** the match that ends first, and of two ending together
    the one that starts first;
  - **leftmost:** the match that starts first, and of two starting together the
    pattern listed first;
  - **overlapping:** every match, by its end and then its start.

  Each was read off Polars' answers to ties. It compares each pattern at each place,
  quick for the few patterns these take. An empty pattern matches between
  characters. `ascii_case_insensitive` folds ASCII letters only, as Polars' does:
  `École` does not match `éCOLE`;
- **the patterns** are a List literal argument, step 257's;
- **contains_any, replace_many, extract_many and find_many** run on it. replace_many
  takes one replacement for all or one each, and find_many gives byte offsets, a
  List(UInt32), as Polars' do. Overlapping with leftmost is refused, as Polars
  matches overlapping only the standard way.

**Struct answers** (`strStruct`):

- **`extract_groups`:** each capture group of the first match, a String field named
  after its group or numbered from 1, all null where nothing matches. `GroupNames`
  (in `expr`) names them when the plan is typed, from the pattern;
- **`split_exact(by, n, inclusive)`:** fields `field_0` to `field_n`, what follows
  them dropped;
- **`splitn(by, n)`:** fields `field_0` to `field_{n-1}`, the last the rest;
- a field is null where there is no part, and a null string's fields are null, the
  Struct not, as Polars makes it.

**`SplitN` is a Struct now, a break:** it was a List, with `n <= 0` unlimited. `Split`
is every part. The CHANGELOG lists it, and two tests that read it as a list now read
its fields.

**`split_inclusive`:** each part keeps the separator after it, and an empty string is
no part, as Polars' `split(inclusive=True)` gives `[]`.

**`to_titlecase`:** a letter after a character that is not a letter is upper case and
any other lower, as Polars makes it: `3rd` is `3Rd`, `hello_world` `Hello_World`.

**`normalize`:** NFC, NFD, NFKC or NFKD by `golang.org/x/text/unicode/norm`.
`go.mod` now requires `golang.org/x/text v0.37.0` directly. It was already in the
module graph through arrow-go, and the committed `go.sum` held both its hashes, so
`go.sum` is unchanged. `bench/micro`'s `go.mod`, which lists ursus's dependencies,
gains it as indirect: the gate's vet of that module failed without it, and its
`go.sum` held the hashes too.

**A block of its own at 800.** The nine functions first went in the string family's
block. That pushed the list family's end onto the struct family's base, 500, the
collision step 253 met, and the build caught the duplicate map keys. `IsString`
covers both blocks. The names test's ranges now take the blocks at 600, 700 and 800,
which it had not checked.

**Public:** `ContainsAny`, `ReplaceMany`, `ExtractMany`, `FindMany`, `ExtractGroups`,
`SplitExact`, `SplitInclusive`, `ToTitlecase`, and `Normalize` with the
`UnicodeForm` constants `NFC`, `NFD`, `NFKC` and `NFKD`. The patterns are constants;
Polars takes an expression too.

## 2. Tests and teeth

**`TestManyPatterns`:** every answer is Polars 1.44.1's:

- contains_any, case-blind, of an empty pattern, and ASCII-only case-blindness;
- replace_many, with one replacement and case-blind;
- the ties: ending together, ending first, leftmost listed first, an empty pattern
  between characters;
- extract_many, overlapping and not, ordered by their ends, leftmost;
- find_many, in bytes;
- and two refusals.

**`TestStringsAsStructs`:**

- extract_groups named and numbered;
- split_exact, and inclusive;
- splitn, of one and past the parts;
- split inclusive;
- a pattern of no group and splitn of none refused.

**`TestTitlecaseAndNormalForms`:** Polars' title case of five strings, and four
normal forms' byte lengths of a ligature, a composed é and a decomposed one.

| tooth | result |
| --- | --- |
| of two ending together, the later start | **bites** |
| leftmost takes the last listed | **bites** |
| case is always folded | **bites** |
| overlapping matches do not overlap | **bites** |
| split_exact keeps the rest | **bites** |
| a title's digits are letters | **bites** |
| groups are numbered from zero | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet of the root and bench/engines/go
and the bench engine tests clean; the 22 PDS-H answers match DuckDB. The vet of
bench/micro failed on its `go.mod`, fixed as above and run again clean.
