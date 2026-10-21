# Step 302 — as built

**C5: `InsertColumn` and `HashRows`** (`v0.6.7-scope.md` §2), as Polars'
`DataFrame.insert_column` and `hash_rows`. This finishes the scope's columns.

On the way, a bug found and fixed: a selection among a variadic function's operands
expanded the whole function once per column. `MaxHorizontal(All())` gave each column
itself. Fixing it is a break.

The first gate found the fix wrong in a list's body. Fixing that also fixed a fold in
a body, broken since step 266.

## 1. `InsertColumn`

`lf.InsertColumn(i, c)` puts a column at index i, and `df.InsertColumn(ctx, i, c)` is
the eager form. The column is an `Expr` or a `*Column` of Go values, as Polars takes
an Expr or a Series. Both are one method, generic over `Expr | *Column`.

**The index is Polars':**

- the columns from i on move one place right;
- i of the frame's width appends;
- a negative i counts from the width, so −1 is the place before the last column.

**An expression** is typed by a `Select` of it, and must be one column. It is added
by `WithColumns`, so an aggregate or a literal is broadcast, as Polars'. Then a
`Select` puts every column in its new place.

**Go values** go through `HStack`, so their count must be the frame's height, or the
query fails as `HStack`'s does.

**Refused, each as Polars refuses it:**

- an index past either end, "column index 4 is out of range: the frame has 3
  columns";
- a name the frame already has, `the frame has a column "a" already`. ursus does not
  replace the old column, since `WithColumns` does that;
- an expression of two columns, "InsertColumn inserts one column, and the expression
  gives 2";
- a nil column.

## 2. `HashRows`

- **`HashRows(seed, exprs...)`** is an expression: each row's hash over the given
  columns, or every column when none is given, as a `Uint64`. In Polars that is
  `pl.struct(...).hash()`.
- **`df.HashRows(ctx, seed)`** is Polars' `DataFrame.hash_rows`: a `*Series[uint64]`
  named "", as Polars names it.

**The hash is `Hash`'s,** over the row's group-by key:

- the `GroupKeyEncoder` encodes the row;
- FNV-1a hashes the bytes, seeded, and splitmix64's finalizer mixes them;
- `Hash` now shares that loop, `hashKeys`;
- `HashRows(s, c("a"))` equals `c("a").Hash(s)`, which the test holds.

**So rows a group-by takes as one hash alike:**

- −0 with 0, and NaN with NaN;
- a row of nulls has a hash of its own;
- the columns' order counts.

**How the operands arrive:**

- A column of the Null type is hashed as nulls of an `Int64`.
- A literal operand arrives as one row and is broadcast to the batch's height.
- The seed travels as the last operand, an `Int64` literal.

**The IR.** `FnHashRows` ends the horizontal family, so the CallFn count is 210. Its
typing refuses:

- a List or a Struct, "hash_rows reads values a group-by can key on, and "l" is
  List(String)";
- no column, which `Variadic` catches first, as "hash_rows of no expression".

**In §9:**

- a List or a Struct is refused, where Polars hashes one. §3's key row names
  `HashRows` too;
- one seed, where Polars takes four;
- the numbers are ursus's own, as `Hash`'s are (`v0.6.6-scope.md` §6.3).

## 3. Found on the way: a selection among a function's operands

**Writing `HashRows(seed)` over `All()` found it.** The call came out once per
column: each column hashed alone, a column each.

Every variadic function did the same. Expansion writes a selection out per expression:
`All().Sum()` is one sum per column. A selection among a function's operands made the
whole function one per column too. So:

- `MaxHorizontal(All())` gave each column its own row's maximum, which is itself;
- `SumHorizontal(All()).Alias("s")` was refused, as two columns named "s";
- `ConcatStr`, `Struct`, `ConcatList` and `Coalesce` of a selection answered a column
  each.

Polars writes a selection out as the function's operands and answers one column, so
`sum_horizontal(pl.all())` is each row's sum across every column.

**The fix is `expr.Variadic`** (`internal/expr/variadic.go`): an operator name, the
operand expressions, and a `Build` over them.

- **When it is built.** `Expand` builds it after the folds, innermost first, as
  `buildFolds` builds a `Fold`. `buildVariadics` writes each operand out over the
  input's schema, a selection into its columns in order, then calls `Build`.
- **No operand.** An operand list that matches no column is refused, "sum_horizontal
  of no expression", as Polars refuses it.
- **Reaching typing.** `Field` raises an internal error if a `Variadic` reaches typing
  unexpanded. `Rebuild` knows it.

**The API builds through it,** by the `variadic` helper in `horizontal.go`:

- the six horizontal reductions, `Max`, `Min`, `Sum`, `Mean`, `Any` and
  `AllHorizontal`;
- `ConcatStr`, whose `Build` puts the separator between the operands, and
  `ConcatStrIgnoreNulls`, whose separator comes last;
- `Struct`, `ConcatList`, `Coalesce`, whose `Build` makes the nested `Cond` chain,
  and `HashRows`;
- `List().Concat`, which is `ConcatList` of its receiver first. Polars' `list.concat`
  is `concat_list` too, and over `pl.all()` answers one column, probed.

**The fills.** `FillMin`, `FillMax` and `FillMean` built on `Coalesce`, and
`sugared` needs the `*expr.Cond` at hand. They now build it at once by
`coalesceNow`. Its operands are the expression and its aggregate, so a selection
expands the whole fill a column at a time: `All().FillNull(FillMin)` fills each
column with its own minimum, probed.

**Tests that flipped,** each now Polars' answer:

- `TestConcatStr`'s "over a selection" is one column, "first",
  `["AdaLovelace!", "AlanTuring!", null]`, and its "literal" one too;
- `TestCoalesceThroughMatch` is one column, a = [1 2].

The CHANGELOG lists the change under breaks. §1 has the row, fixed.

## 4. Found by the gate: a list's body

**The first gate failed two tests,** `TestRle` and `TestUniqueAndValueCounts`, with
"concat_str reached typing unexpanded". Each has a `ConcatStr` inside
`List().Eval`.

- **Why.** `List().Eval` and `CumulativeEval` lower their body when it is written
  (step 268), and the lowered body is the `ListMap`'s own, not a child. So no
  expansion reaches it, and a `Variadic` there is never built.
- **A fold failed the same way, since step 266.** `List().Eval(Fold(...))` and a
  `Fold` in `CumulativeEval`'s body failed, "fold reached typing unexpanded". No test
  had one.

**The fix is `buildAsWritten`** (`internal/expr/listeval.go`). Before lowering, it
builds every `Fold` and `Variadic` in the body over its expressions as written.

- A body reads nothing but `Element()`. So a selection among the expressions is
  refused, as any selection in a body is, "list.eval reads all()". Polars refuses
  one too: "named columns are not allowed in `eval` functions".
- A fold is built by `Fold.write`, taken out of `buildFolds`. `CumSumHorizontal`
  casts its fields to the last one's type, which a body cannot know when it is
  written, so in a body it is refused. Polars' fails there too, on fields of one
  name.
- A fold in `CumulativeEval`'s body now answers, a value a prefix. Polars refuses
  one, "not allowed with non-scalar output", its function opaque to it. In §9.

**Another design was tried and dropped.** It built a variadic at once when no
operand held a selection, as before the step, keeping `Variadic` for selections
only. It also passed every test. But each mechanism then covered the other in a
body, so neither had a tooth that bit. One path was kept: every variadic waits for
expansion, and a body builds it as written.

**Two more places saw an unbuilt `Variadic` when written:**

- **`OutputName`** names a `Variadic` after its first operand, as the function built
  is named. `Format` takes its name from its first argument when it is written, and
  `Format("<{}>", ConcatStr("-", Lit("x"), c("a")))` was named "a", by its roots,
  where Polars' is "literal".
- **`Format` is built at once,** a `concat_str` call of its pieces, not through
  `ConcatStr`. Polars' `format` formats each column of a selection, probed, where its
  `concat_str` takes them all as operands. Through `ConcatStr`, `Format("<{}>",
  All())` would have been one column of every column.

**A gap, recorded in §1:** `Format` over a selection is refused, as it was before the
step, its columns all named "literal" after the first argument. Polars names each
after its column. `Format("<{}>", SumHorizontal(All()))` is named "literal" where
Polars' is "a". A name taken at expansion would fix both.

## 5. The site

**`docs/guide/selecting.md`** has `InsertColumn` and `HashRows`, each new in 0.6.7, on
`dev`:

- `Example_selectInsertColumn`: a revenue expression at 1, and Go values at −1;
- `Example_selectHashRows`: a repeated row and its equal hashes.

**`docs/concepts/expressions.md`** says a selection inside a function across columns
is that function's operands, since 0.6.7. `Example_exprHorizontal` sums and takes the
max over `ColDType(Int64)`.

**The Polars page** maps `insert_column` and `hash_rows`.

## 6. Tests and teeth

**The tests:**

- `TestInsertColumn`: values at 1, an expression at 0, the width, −1 and −3, an
  aggregate and a literal broadcast, each Polars' answer; the six refusals; the eager
  forms;
- `TestHashRows`:
  - the properties: equal rows alike, other rows apart, the seed, the columns' order,
    0 and −0, NaN;
  - the expression equal to the frame's `Series`, and one column's equal to `Hash`;
  - a literal operand equal to a column of it;
  - the refusals;
- `TestVariadicOverASelection`: each variadic function over `All()`, one column named
  "a", Polars' answer:
  - `All().List().Concat` one column;
  - `All().Sub(SumHorizontal(All()))` one column each;
  - a selection matching nothing refused;
- `TestVariadicInAListsBody`: `ConcatStr`, `SumHorizontal`, `Coalesce`, `Struct` and
  a fold in `List().Eval`, in a group-by too, and `MaxHorizontal` and a fold in
  `CumulativeEval`, each Polars' answer but the last; a selection and
  `CumSumHorizontal` in a body refused;
- `TestFormatOfAVariadic`: a format of a variadic named as Polars names it, and of a
  selection refused, not joined.

**Teeth, all biting:**

- a horizontal function built at once;
- a coalesce built at once;
- a negative index not counted from the width;
- a name the frame has let through;
- the seed ignored;
- a literal operand not broadcast;
- a body's variadic not built;
- a body's fold not built;
- `Format` through `ConcatStr`;
- an unbuilt variadic named by its roots.

The last was silent at first: the test ran in batches of one row, where a literal's
one row is already the batch's height. The literal's case now runs at the default
batch size, and the tooth bites.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
