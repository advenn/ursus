# Step 499 — as built

**Wave 499 of [`oracle-v2-scope.md`](./oracle-v2-scope.md) §3: strings, lists,
structs and enums converted to recorded oracles.** Fifteen files of the root
package: every hand-typed data value gone, each file's questions asked of Polars
2.1.0 by a group of `bench/scripts/oracle/` and recorded in `testdata/oracle/`, each
spelled in its `oracle_<group>_test.go`. 321 cases in fifteen groups, 58 of them
generated; 1,611 lines of the fifteen files gone, 762 of registry added. Three kernel
fixes along the way, one skipped finding, and what Polars 2 has no answer for held
to Go's own oracles or to a differential.

## 1. The files

| file | lines before | after | hand-typed value lines before | after | cases | generated |
| --- | --- | --- | --- | --- | --- | --- |
| `listns_test.go` | 739 | 281 | about 83 | 0 | 21 | 10 |
| `arrns_test.go` | 129 | 58 | 48 | 0 | 45 | 6 |
| `listeval_test.go` | 129 | 53 | 31 | 0 | 28 | 4 |
| `listmore_test.go` | 154 | 103 | 29 | 0 | 21 | 3 |
| `listset_test.go` | 203 | 134 | 34 | 0 | 7 | 3 |
| `liststruct_test.go` | 85 | 75 | 15 | 0 | 6 | 3 |
| `structfields_test.go` | 84 | 49 | 25 | 0 | 20 | 3 |
| `withfields_test.go` | 102 | 43 | 16 | 0 | 3 | 1 |
| `enum_byhand_test.go` | 361 | 165 | 41 | 0 | 22 | 4 |
| `expr_bin_test.go` | 190 | 60 | 57 | 0 | 35 | 4 |
| `expr_cat_test.go` | 149 | 126 | 25 | 0 | 13 | 1 |
| `strmany_test.go` | 134 | 44 | 44 | 0 | 32 | 4 |
| `strcodec_test.go` | 262 | 129 | 63 | 0 | 38 | 5 |
| `strsplit_test.go` | 395 | 194 | 40 | 0 | 12 | 5 |
| `strptime_test.go` | 181 | 172 | 45 | 0 | 18 | 2 |

The hand-typed counts are the removed lines that held a literal answer (a table row,
a want string or slice, a rendered-cell string), counted by pattern over the diff;
the inventory's classifier (step 504) will count them again. What the fifteen files
keep asserts a refusal, a type, a count, a rendering or a plan, reads the same query
another way (a batch-size sweep, a String cast beside an Enum), or holds a value
Polars 2.1 has no answer for to Go: the fixtures' inputs stay, as inputs.

## 2. What stays, and why

- **`listns_test.go`** keeps its two Parquet fixtures and `TestListNamespaceBatchSizes`,
  which reads the whole namespace at batch sizes 1 to 4 and 8192 and holds every
  answer to the one-batch answer with `CheckNullability` — the sweep the recordings
  cannot run, since the loader builds frames in memory. `TestListMeanKeepsTheElementType`
  holds a `List(Duration)` mean past int64 to `math/big` over a hundred generated
  lists of three, each summing to a multiple of three; the ordinary Duration means are
  the oracle's `mean_duration_gen`. `listCell` stays for `implode_test.go`.
- **`listeval_test.go`** keeps `evalFixture` and `listText` for `ranknull_test.go`.
- **`listmore_test.go`**'s `TestListSample` holds a seeded draw's properties with no
  value: no drawn value outside its list (a filter of none), the same seed drawing
  alike (two draws equal), a whole list unshuffled equal to the list, a whole list
  shuffled summing to the list's sum; the lengths a draw has are the oracle's.
- **`enum_byhand_test.go`** holds what Polars 2 refuses and ursus answers to a
  differential through a String cast: `cum_max` of an Enum against `cum_max` of its
  indices, `Enum("1", "2")` cast to Int64, `.str` of an Enum, the CSV writer and the
  CSV reader under an Enum schema. Its `knownEnumDefects` machinery went: it was
  empty.
- **`expr_cat_test.go`**: Polars 2 removed `cat.get_categories` ("use its
  `dtype.categories`"), so `TestGetCategories` holds every answer to the type's own
  `Categories()`: the list, its count, the list a group, beside a frame of its
  length, of an aliased receiver, and a Filter by the count. The physical index's
  width at 255, 256, 65,535 and 65,536 categories stays as a type check, the last
  index the count less one; 255 and 256 are recorded too.
- **`strcodec_test.go`** holds a regex replaced more than once, which Polars 2.1
  refuses ("regex replacement with 'n > 1' not yet supported"), to Go's `regexp`
  replacing its first n matches through `$1` templates, over 300 generated strings.
- **`strptime_test.go`**: Polars has no second unit, so an offset read into
  `Datetime(s, UTC)` and into a naive `Datetime(s)` is held to `time.Parse` over 300
  generated instants and offsets; Polars 2.1 refuses a Date formatted with a clock
  ("cannot format Date with format '%F %T'"), so its midnight is held to `time.Format`
  over 300 generated dates. A Time in the recordings is Polars' nanoseconds.
- **`strsplit_test.go`** keeps its batch-size and stdlib differentials, the null
  propagation and the full join's count.

Dropped as covered by a recording's dtypes and names: `decode is Binary`,
`ToIntegerWith`'s dtype, `cat`'s Uint32 lengths and String slices, `eval`'s name and
`List(Int128)` type, `WithFields`'s field order, `TestListNamespaceWithoutExploding`
(the oracle's `filter_len_gt_1`), `TestListMeanOfFloat32StaysFloat32` (`mean_f32`).

## 3. Differences named

Every one cites feature-gaps.md §9:

- **An empty list's or an all-null array's sum is null, 0 in Polars; an integer Sum is
  an Int128, Polars' an Int64:** `listns/semantics`, `listeval/sum`, `filtered_sum`,
  `agg_aggregates`, `arrns/sum`, `sum_floats_all_null`, `dot`, `dot_null_row`,
  `structfields/aggregate_each`; `strcodec/fold_of_aggregates`, whose Int64 literal
  meets an Int128 sum. The generated sum cases cast to Int64 over lists with no empty
  one and no null element, and say so.
- **A running sum has Sum's width** (`window.go`'s CumSum): `listeval/cum_sum`,
  `eval_gen`'s `cum`, `List(Int128)`.
- **`Max` propagates NaN:** `listns/max_nan`, a cell; the generated float lists hold
  no NaN for `max`.
- **`n_unique` passes a null over:** `arrns/n_unique`, a cell; and a count or a
  position is a Uint64 where Polars' is a UInt32: `arrns/n_unique`, `arg_min`,
  `arg_max`, `aggs_gen`, `sum_n_unique_gen`, `strsplit/group_by_a_list`. The
  position's width is read under §9's count bullet and "Arr()'s functions answer as
  List()'s"; the maintainer may want the word *position* in §9.
- **A literal has its Go value's type:** `listeval/constant` (`List(Int64)` for
  Polars' `List(Int32)`), `structfields/fold_fields`.
- **An offset read into a naive Datetime is its UTC wall clock, naive;** Polars types
  it UTC: `strptime/offset_read_into_naive`.
- Inside `list.eval`, a count is a Uint32 here as in Polars: no difference.

## 4. What the oracle found

- **`to_titlecase` of a digraph.** "ǆemal" was "Ǆemal" (U+01C4, the upper case);
  Polars 2.1 and Unicode's title mapping give "ǅemal" (U+01C5). `unicode.ToUpper` is
  `unicode.ToTitle` now in `internal/kernel/strmany.go`. Fixed.
- **`extract_groups` of a null string** was a Struct of null fields; Polars 2.1 gives
  a null Struct (and a Struct of nulls under `split_exact` and `splitn`, which ursus
  gave and gives). The kernel's comment said "as Polars makes it"; the struct's
  validity is the input's under `extract_groups` now. Fixed.
- **An inclusive split's trailing part.** `"a,b,".split_inclusive(",")` gave
  `["a,", "b,", ""]`; Polars, as Rust's `split_inclusive`, gives `["a,", "b,"]`, a part
  after the last separator only where there is something, and `split_exact(",", 1,
  inclusive=True)` of "a," `{"a,", null}` not `{"a,", ""}`. The empty string, which
  was special-cased to no part, follows the same rule. Fixed in `strfn.go` and
  `strmany.go`.
- **`FillNullWith("zzz")` of an Enum**, a value outside the categories: a String
  column holding "zzz" here, where Polars 2.1 keeps the null and the Enum. Not in
  §9: the case `enum_byhand/fill_null_zzz` is recorded and skipped with the finding;
  the file keeps the type assertion. For the maintainer: Polars' answer loses the
  value silently, ursus's changes the type.
- **Polars 2.1's `set_difference` orders a list's elements by its hash table**, where
  `set_union`, `set_intersection` and `set_symmetric_difference` keep the first list's
  order then the second's, as ursus does for all four. The literal cases agree; the
  generated ones sort the difference on both sides and say so.
- **A null list's or array's `to_struct` is a null Struct in Polars 2.1**, as here;
  1.44.1 gave a Struct of nulls, which §9's Arr bullet still says. Stale, not
  changed here.
- **`cum_max` of an Enum, a cast of an Enum to Int64 and `.str` of an Enum** are
  refused by Polars 2.1 and answered here (§9 covers the `.str`); each is a
  differential now, §2.
- **`Strftime` has no `%U`, `%u`, `%w`, `%C`, `%G`, `%V` or `%s`** (week numbers, ISO
  years, the epoch's seconds), which Polars' chrono formats; the format case uses
  `%j %a %h %y %D %R` instead. A gap, not a bug.
- Every other recorded value, Polars 1.44.1's pasted or the author's own, is Polars
  2.1.0's answer too: the list namespace's null-against-empty table whole, the eight
  reshapes, the set operations' first-list order (NaN equal to NaN, −0 kept), the
  binary slices of "hello", `json_path_match`'s number reprinting, the twelve-hour
  clock, the normal forms' byte lengths.

## 5. Checks

`make oracle` twice, every group byte-identical (`md5sum -c`, 20 of 20);
`go test -run TestOracle .` 321 of 321 (one skipped); the root package in full,
`./internal/...` and `./docs/...`, `go vet ./...`, `go run ./internal/gen/api -check`
(0 gone, nothing exported) and `gofmt -l` empty, all under `GOEXPERIMENT=simd
GOAMD64=v3`.
