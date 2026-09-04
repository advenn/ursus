# Step 2 — as built

The analytics core: `WithColumns`, `Sort`, `GroupBy().Agg()`, `Unique`, and
predicate pushdown. Authoritative where it disagrees with
[`ursus-api.md`](./ursus-api.md).

**192 tests green** under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`,
with the experiment off, and under `-race`.

```go
df, err := lf.
    GroupBy(ursus.Col("region")).
    Agg(
        ursus.Col("amount").Sum().Alias("total"),
        ursus.Col("amount").Mean().Alias("avg"),
        ursus.Col("rep").NUnique().Alias("reps"),
        ursus.Len().Alias("orders"),
    ).
    Sort(ursus.Desc(ursus.Col("total")).NullsLast()).
    Collect(ctx)
```

---

## 1. Semantic decisions — where ursus deliberately diverges from Polars

ursus committed to being both "Polars-class" and "SQL-compatible on nulls". Those
conflict in four places. Each resolution is invisible in normal data, so each is
pinned by a test named after it.

| # | Question | ursus | Polars | Why |
| --- | --- | --- | --- | --- |
| S1 | `sum` of an all-null or empty group | **NULL** | `0` | `0` is indistinguishable afterwards from a genuine zero; NULL converts to 0 with FillNull. Postgres, DuckDB, Spark and PyArrow all say NULL — Polars is the outlier and closed the alignment issue as "not planned". |
| S2 | `NUnique` and nulls | **skips nulls** (`0` for all-null) | counts null as distinct (`1`) | Every other aggregate in ursus skips nulls. An n_unique that did otherwise would be a permanent trap. Preserves `NUnique(c) ≤ Count(c)`. |
| S3 | Sort null placement | **nulls first**, per-key `.NullsLast()`, independent of direction | nulls first | Matches Polars. Tying placement to direction, as SQL does, means `.Desc()` silently relocates the nulls. |
| S4 | Integer `sum` overflow | **Int128 accumulator AND output** | wraps silently | DuckDB's answer. Overflow then needs 2^63 max-magnitude rows, i.e. it is unreachable. |

Settled by research, not in dispute:

- `Count` counts non-null; `Len` counts rows. **Never aliased** — Polars had to fix
  a conflation here.
- `Mean` = `sum(non-null) / count(non-null)`, **never `/ len`**. Mean of an all-null
  group is NULL, **not NaN**: nothing was computed, as opposed to a computation
  that came out undefined.
- Float sums accumulate in **Float64** even when the output is Float32. Polars has
  a live bug from float32 accumulation past 2²⁸ elements.
- `First`/`Last` are **positional** and may return null. They are a selection, not
  a reduction — skipping nulls would break `First(a), First(b)` coming from one row.
- Group keys: null forms its own group; NaN groups with NaN; `-0.0` groups with
  `+0.0`. `NUnique` uses the same equality, so it agrees with `GroupBy`.

## 2. The Int128 decision, and what it cost

Choosing DuckDB-style 128-bit accumulation over "widen to 64 and error" was the
largest single cost in the step: a new `i128` package plus changes across ~20 files,
because `data.Fixed`, the kernel dispatch switches, the group-key encoder, the
comparators, casting and rendering all enumerate physical types.

**The structural consequence.** Int128 is a struct, so `a < b` and `a + b` do not
compile for it. That forced splitting the constraint:

- `data.Primitive` — types supporting Go's operators. Generic kernels use this.
- `data.Fixed` — `Primitive | i128.Int128`. Storage, `Values`, `Take`, `Concat`.

The compiler then found every operator use on the widened set — which is the type
system doing the audit rather than a reviewer.

**Two payoffs beyond overflow safety.** `Promote(Uint64, Int64)` now returns
Int128 instead of being rejected, closing a hole that previously forced users to
cast a value the engine itself had chosen. And `arrow.GetData` had to be replaced
with our own `unsafeData`, since arrow's constraint enumerates arrow's own types —
which removed arrow-go from the value-access hot path entirely.

## 3. Architecture

| Shape | Used by | Why |
| --- | --- | --- |
| `Sink` (Consume/Merge/Finish) + `breaker` | Sort, Aggregate | Pipeline breakers. Written as monolithic `Operator`s they would work today and be rewritten the day v0.2's morsel scheduler lands. `Merge` is also the spilling seam. `stage`, `scanOp`, `limitOp`, `filterOp`, `projectOp` and all three exec drivers are untouched. |
| `BatchOp` | WithColumns | Stateless and 1:1, so the standing rule applies: everything that CAN be a BatchOp MUST be. |
| stateful `Operator` | Distinct, Limit | Distinct decides row-by-row, so `Unique().Head(10)` stops after ten distinct rows instead of scanning everything. |

`Merge` is **implemented and tested now** despite nothing calling it concurrently.
A Merge written later, against forgotten invariants, is a Merge that is wrong.

**Two-phase aggregate lowering.** `sum(a)/count(a)` is a Binary over two Aggs. The
sink computes each distinct inner Agg into a temporary (`__agg0`, …); if any
expression is more than a bare Agg, a `Project` on top evaluates the surrounding
arithmetic. Teaching the sink to evaluate expressions would have duplicated the
evaluator; reusing Project means compound aggregates get the expression system free.

## 4. Predicate pushdown — legality is the specification

This rule returns different **rows** when wrong, with no error, and usually only on
data larger than a fixture.

| Node | Verdict |
| --- | --- |
| Scan | push into the source if it declares `Caps().Predicate` |
| Filter | merge (conjoin) |
| Project / WithColumns | push **with substitution** of output name → defining expression |
| Sort | push freely |
| Distinct (whole row) | push freely |
| Distinct (subset) | only if the predicate reads nothing outside the subset |
| **Limit / Head** | **BARRIER** |
| **Aggregate** | **BARRIER** (key-pushdown deferred) |
| anything unknown | **BARRIER — fail closed** |

**Why Limit is a hard barrier.** `Filter(Limit(2,[1..5]), x>3)` is `[]`;
`Limit(2, Filter(...))` is `[4,5]` — different cardinality *and* different rows.
Limit is not row-local: removing earlier rows promotes later ones into the window.

**Why substitution is mandatory.** `WithColumns(Col("x").Mul(2).Alias("x"))` then
`Filter(Col("x").Gt(10))` pushed verbatim filters the **un-doubled** column: the
same name means two different things above and below the node.

**How it is checked.** The optimizer's `Verify` mode compares schemas, and a
wrongly-moved filter does not change the schema. So soundness is tested the only
way it can be: every query runs with the optimizer on and off and must return
identical results. Verified to have teeth — making Limit pushable makes
`TestPushdownSoundness` fail with 3 rows against 2.

## 5. Defects fixed from the premature start

Implementation began before planning did; ~960 lines were written first. An audit
found nine defects. Three would have shipped:

| # | Defect | Impact |
| --- | --- | --- |
| P1 | `rejectAggregate` documented as guarding `Select`, **never called from it** | `Select(Col("x").Sum())` reached the evaluator and died as "a bug in ursus" |
| P2 | `WithColumns.Schema()` and `resolveWithColumns()` implemented **different semantics** | A self-referencing WithColumns resolved, then failed its own `Schema()`, surfacing as "the optimizer produced a plan whose schema does not resolve" |
| P4 | **`ArgTopK ≠ ArgSort[:k]`** — two independent stability bugs | Broke the optimizer's own soundness rule. `Verify` cannot catch it: it checks schemas, never rows. Counterexample `[5,5,1,5]` k=2 → `{2,1}` vs `{2,0}` |

Six more: `IsAggregation` accepted nested aggregates (`Sum().Mean()`); the group-key
encoder flipped the sign bit for **unsigned** ints, breaking its documented
order-preservation; a latent Enum panic in both the comparator and the key encoder;
`SortKey`'s doc contradicted its own zero value and misstated Polars; a zero-key
`Sort` could survive expansion; `Aggregate.Label()` printed only a count, making
golden plans blind to the aggregate list.

**The running-schema walk had been written FOUR times** (Schema, Resolve, physical
planning, predicate pushdown) and two copies had already diverged — that was P2.
It is now one function, `plan.WalkWithColumns`.

## 6. Honest gaps

- **Predicate pushdown into scans does nothing yet.** No source declares
  `Caps().Predicate`, so predicates stop above the scan. The reordering below
  Sort/Project is real; statistics-based skipping arrives with Parquet.
- **Aggregate key-pushdown is not implemented.** A predicate on a group key could
  legally descend; telling it apart from HAVING is real work and getting it wrong
  computes the aggregate over a filtered subset.
- **`extremumAcc` keeps a single-row Column per group**, which is one allocation
  per group per batch. Correct and type-generic, but the obvious thing to optimize.
- **`hashAggSink.Merge` does not remap group ids.** The signature carries what v0.3
  needs; today the only caller is the equivalence test, which shares a numbering.
- **No SIMD accumulators.** Scalar first, differential test second — the
  established order. `AddBatch` already receives a whole column.
- **Int128 has no Mul, division or modulo.** `sum(x) % 7` is not a real query; cast
  to Int64 or Float64 first.
- **Limit pushdown into `Sort.Limit` is not implemented.** `Sort.Limit` is read and
  rendered and `ArgTopK` is correct; the rule is ~20 lines.

## 7. Not built, with the seam

Joins; window functions (`.Over()`); `GroupByDynamic`/`Rolling`; `Std`, `Var`,
`Median`, `Quantile`, `Product`, `ArgMin`/`ArgMax`; spilling and external merge
sort; parallel aggregation; Parquet/CSV IO; SQL; `Uint128`.

The load-bearing seams: `Sink.Merge` is parallelism and spilling, tested now;
`Sort.Limit` is limit pushdown; projection pushdown's conservative default keeps
every unbuilt node correct-but-unoptimized rather than silently wrong.

## 8. Verification

```bash
make test-all   # 4 SIMD widths + experiment-off
make race
make levels
go test ./... -update   # golden plans
```

The tests worth knowing about, because each pins a decision that is otherwise
invisible: `TestSumOfAllNullIsNull`, `TestMeanDividesByCount`,
`TestNUniqueSkipsNulls`, `TestNUniqueUsesGroupingEquality`,
`TestFirstLastArePositional`, `TestMergeEquivalence`, `TestArgTopKMatchesArgSort`,
`TestGroupKeyBiconditional`, `TestPushdownSoundness`, `TestLimitIsAPushdownBarrier`,
`TestAggregateBatchSizeInvariance`, `TestMinMaxAgreeWithSort`,
`TestIntegerSumWidensToInt128`.
