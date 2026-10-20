# Step 296 — as built

**The user guide's next four chapters** (`docs-site-scope.md` §2):

- reshaping;
- strings;
- lists and structs;
- dates and times.

Writing the strings chapter found a bug in how a frame prints, and the step fixed it.

## 1. The chapters

**`guide/reshaping.md`:**

- `Pivot`, and why it is a `DataFrame`'s;
- `Unpivot`, and its order;
- `Explode` and `Unnest`;
- `Concat`, strict and diagonal;
- `HStack` and `Transpose`.

**`guide/strings.md`:**

- cleaning and lengths, characters against bytes;
- matching with RE2 patterns or literals;
- `Extract` and `ExtractAll`;
- `Replace` with groups;
- `Split` and `SplitExact` unnested;
- padding and slicing by characters;
- parsing, and when it is strict;
- `ConcatStr`, `Format`, and `Str().Join` in an aggregation.

**`guide/lists-and-structs.md`:**

- building lists;
- the questions per list;
- `Sort`, `Join` and `Eval` over `Element()`, and `Filter` and a rank in `Eval`;
- the set operations;
- `Explode`;
- `Struct`, `Field`, `WithFields` and `JsonEncode`;
- what is not there yet.

**`guide/dates-and-times.md`:**

- the four types;
- parsing;
- a date's parts in ISO's terms;
- calendar arithmetic, business days with a roll;
- `Truncate` and `Strftime`;
- zones across New York's change to daylight time;
- `DateRange`;
- dynamic groups, daily with the weekend's empty windows and weekly by key;
- a rolling group.

**The examples:** 33 more, 105 in all. Each chapter's data is shown as the helper that
builds it: `people`, `baskets` and `days`.

## 2. Held to Polars

**Run in Polars 1.44.1 on the same inputs, and equal:**

- every string method shown;
- every list method and the set operations, in their order;
- `json_encode`;
- `pivot`;
- the dynamic weekly groups by key;
- the rolling group;
- `add_business_days`, `offset_by`, `month_end`, `truncate`, `strftime`;
- ISO week, weekday and ordinal day.

**What differs, now in `feature-gaps.md` §9:**

- **`Unpivot`'s row order,** row by row where Polars goes column by column. Its doc
  chose that, since it streams by batch; §9 had not listed it.
- **`GroupByDynamic`'s empty windows,** a row of `Len` 0 that Polars leaves out. Also
  its doc's choice, also unlisted.
- **`ToInteger`, `ToDate` and `ToDatetime` are lossy** where Polars' are strict by
  default.
- **A pivot's empty cell under `Sum` is null,** where Polars' is 0: the empty-sum rule
  §9 has.

The Polars page lists the most surprising ones: `Remove`'s nulls, the empty windows,
the lossy parsers.

## 3. Found and fixed: the printed frame's alignment

**A frame printed its cells padded by bytes,** `len(s)`. A row with "Zoë" came out a
column short, and so did every row holding a letter outside ASCII.

**Now it pads by the columns a terminal draws,** through `golang.org/x/text/width`, a
module ursus already requires:

- a wide East Asian character counts two;
- a combining mark or a format character counts none.

That is how Polars' table measures a cell. `TestFrameTableAlignsByColumnsNotBytes`
holds a table of "Zoë", "数据", an e with a combining acute, and a header "名前".

## 4. Found, not fixed: a naive datetime is written with a `Z`

`Datetime(s)`, with no zone, prints `2026-03-08T06:30:00Z`, as though it were UTC.
The CSV writer shares `dtype.FormatTemporal`, so it writes it that way too, and
another reader takes it as UTC; Polars writes no offset.

Changing it changes the CSV format, so it waits for 0.6.8's I/O. It is in
`feature-gaps.md` §1. Meanwhile the chapter shows zoned instants only.

## 5. The site

**Output that is prose,** an error or a schema, no table in it, wraps when it is wider
than the page. The generator marks it `.wrap`, and a rule in `extra.css` wraps it.
Before, the memory refusal's 320 columns scrolled.

**`TestTheExamplesFitThePage` holds every fragment** to the page's 84 columns, its
code and its printed frames, helpers included. Step 295's check was by hand, and
missed the helpers: `people`, `baskets` and `days` were wider, and are broken across
lines now.

## 6. Tests and teeth

**The tests:**

- `TestFrameTableAlignsByColumnsNotBytes`;
- `TestFragmentWrapsProseNotTables`;
- `TestTheExamplesFitThePage`.

**Teeth, all biting:**

- the table padded by bytes again: the new test bites;
- prose never wrapped: two tests;
- a line wider than the page.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
