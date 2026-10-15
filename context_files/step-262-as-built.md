# Step 262 — as built

**The 0.6.6 scope.** No code changed.

[`v0.6.6-scope.md`](./v0.6.6-scope.md) takes `feature-gaps.md` §4 and §5,
namespaces, generators and statistics, as 0.6.5's §5 promised. Every name and
default in it is Polars 1.44.1's, read from its signatures in the bench's
environment. Before the items were listed, each was looked for in the public API, so
the scope lists only what is missing.

**What it holds:**

- **generators:** integer, linear and temporal ranges, `Repeat`, `Ones` and `Zeros`,
  and values from their components. A generator of scalar arguments is its per-row
  form's list exploded over a frame of one row, so 0.6.5's E4 gives it a place;
- **folds and constructors:** `ConcatList`, `Fold`, `Reduce` and the cumulative
  forms, built from a Go function over Exprs when the query is planned, no UDF;
  `Format`; `Nth` and the column selectors; `ArgWhere` and the arg sorts;
- **`List().Eval` and `Element()`,** the body lowered by what it does to a group, as
  0.6.5's E3 lowered an aggregation's reshapes;
- **the missing methods** of `.list`, `.str`, `.dt`, `.struct`, and the `.cat` and
  `.bin` namespaces;
- **statistics and expression methods** (M1–M5), with rolling and EWM additions as a
  second tier.

**Decisions it makes:**

- **`.arr` waits for an Array type** (§6.1). 0.6.5's scope put it here, but it needs
  a type ursus does not store. It goes with Categorical and large strings in a
  release of types after I/O, a release the order of 2026-10-14 did not name.
- **`SplitN` returns a Struct,** as Polars' does (§6.2): a break.
- **Some answers cannot be Polars' to the bit** (§6.3): a hash, a seeded sample, an
  approximate count. Their tests hold properties instead.
- **The list differences stay** (§6.4).
- **`Date`, `Datetime` and `Duration` already name the types,** so Polars'
  `pl.date` and its kin are `MakeDate`, `MakeDatetime`, `MakeTime` and
  `MakeDuration`. `Corr` keeps its signature, and Spearman's is `CorrSpearman`.

**`feature-gaps.md`** marks what 0.6.5 built in its namespace lists: `dt.date` and
`dt.time`, the list median, std, var and gather_every, and Explode and Unnest as
expressions.

**Gate:** no code changed since step 260's gate.
