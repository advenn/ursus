# Step 352 — as built

**A Categorical's I/O,** after step 351's type: in and out of Arrow, IPC and Parquet as
Polars writes one, and `QCut` and `Hist`'s category answering one, as Polars' do. Two
breaks.

## 1. How

**Arrow out** (`arrowout.categoricalData`): a Categorical is a dictionary array, its
codes `Uint32`, each value's the order it first appears in in the column, into a String
dictionary of the values in that order: Polars' `dictionary<values=string,
indices=uint32>`, which Polars and PyArrow read back as a Categorical. A String stays
zero-copy; a Categorical's export hashes each row, its storage being its text.

**Arrow in** (`arrowin.typeOf`): a dictionary is an encoding, decoded to its values as
before, and one of text, `utf8`, `large_utf8` or a view, is now a Categorical, as Polars
reads one: an IPC file's, a pandas categorical's through PyArrow. The byte builder types
its column as the plan says, a Binary or now a Categorical. A dictionary of numbers is
its numbers still. **The break:** such a column read as a String before.

**Parquet:** a footer's Arrow schema, which Polars and PyArrow write under
`ARROW:schema` (step 341), now says which columns are Categoricals as well as which
are Arrays (`arraysOf`, `categorical`), and a column of text it calls a dictionary is
read as one. ursus writes the key where a Categorical is, its type the dictionary
`arrowout` names, its pages a String's. The byte-array readers' fast path and the
dictionary filter take a Categorical as a String.

**`QCut`'s labels and `Hist`'s category** come from the data, so they are a
Categorical, as Polars' are; `Cut`'s are an Enum's (step 350). **The break:** both were
Strings.

| Polars 1.44.1 | ursus |
| --- | --- |
| its own Parquet and IPC files of a Categorical | read as Categoricals |
| ursus's Parquet and IPC files of one | read by Polars as Categoricals; PyArrow sees `dictionary<values=string, indices=uint32>` |
| `to_arrow()` of one | a dictionary of its values, codes in first appearance |
| an Arrow dictionary of text | a Categorical; of numbers, its numbers |
| `qcut`, `hist(include_category=True)` | a Categorical |

## 2. Tests

**`TestCategoricalIO`,** Polars 1.44.1's answers: Polars' own Parquet and IPC files;
Parquet and IPC written and read back; an Arrow export, its dictionary and indices, and
read back; a dictionary of numbers; `QCut` and `Hist`'s category. **Updated:**
`TestEveryArrowTypeIsMappedOrRefused`'s dictionary of text, a Categorical; step 350's
`QCut` text, a Categorical. **Fixtures:** `testdata/parquet/polars_categorical.parquet`
and `testdata/ipc/polars_categorical.arrow`, written by Polars 1.44.1.

**Teeth (8 of 8 bite):** exported as plain text; a dictionary of text read as a String;
the footer's Categorical not read; the footer not written for a Categorical; a value's
code not its first appearance's; `QCut` text; `Hist`'s category text; an Arrow
Categorical built a String.

## 3. Performance

A million rows of 1000 values, the median of seven: `Record()` of a Categorical 15.0 ms,
its dictionary built, where Polars' `to_arrow()` takes 10.4 and a String's export is
zero-copy.

**Gate:** test-all 140 ok, race 28 ok (the root package 767 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
