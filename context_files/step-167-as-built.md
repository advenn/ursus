# Step 167 — as built

**Item 6 of `v0.6-scope.md`: runtime filters, one hop.** The midpoint audit's §5
design, with two corrections the measurements forced.

## 1. Evidence first

**q2's plan:**

- part, filtered to about 750 rows at SF=1, joins a probe side;
- that probe side is partsupp's 800,000 rows joined to the supplier chain first;
- the key, `ps_partkey`, comes unchanged from partsupp's scan.

So every partsupp row went through the supplier join, and was gathered, before the
part join dropped all but about 3,000.

**A join builds before it probes** (`joinBreaker.drain`), so its keys are known when
its probe side starts.

## 2. What changed

**`plan.RuntimeFilter` and the `runtime_filters` rule** (`rule_runtimefilter.go`),
after `build_side`. For an inner or semi join on one integer key that the probe
column already has, outside `NullsEqual` and `Validate`, the rule walks the probe
side down by the key's name, through:

- a `Filter`, or a `RuntimeFilter` already placed;
- a `Project` that passes or renames the column;
- a `WithColumns` that leaves it alone;
- the side of a join the column comes from, if that join keeps every row's key there
  (not a side it null-extends) and does not validate.

The column must keep the outer join's key type all the way down, so a key promoted
at a join between stops it. It never enters a `Cache`, which another consumer reads,
and every site of a `Cache` gets the first site's rewrite. A filter is placed only
once it has passed a join: just below the join it would repeat the probe's own
lookups.

The join carries the shared slot (`Join.Publish`). `Explain` shows
`publish #n` on the join and `RUNTIME FILTER [col("k")] by join #n` where it sits.
The flag is `RuntimeFilters`, on by default.

**The physical side** (`runtimefilter.go`):

- a partitioned build of integer keys publishes its `IntKeyParts` to the slot;
- the filter keeps every row until something is published, and for good when it
  never is: a streaming build, encoded keys, or a join exchanged at run time;
- after that it keeps the rows whose key the tables have, a null key never.

So a row is dropped only if it cannot join.

**Two corrections the measurements forced:**

1. **Publish before reading ahead** (`publishEarly`).
   - Step 162's runtime swap reads a marked join's probe side ahead before its
     build. That first pull starts the pipeline below, whose parallel workers fetched
     batches through the filter before any key existed, so they passed.
   - The test of dropped rows saw none.
   - Turning the swap off for joins that publish fixed that, but cost q9, q8 and q18
     their swaps.
   - Now a small build, up to 128k rows, builds its key tables and publishes before
     the read-ahead. `freeze` reuses them, and if the join is exchanged after all only
     that small build is lost.
   - A larger build reads ahead first and publishes at freeze. Building 1.5 million
     orders keys early, then exchanging the join, was 0.75 s of q9.
2. **Retire an unselective filter.**
   - A filter costs a lookup per row and a copy of every batch. In q9, filters whose
     keys covered most rows cost 1.28 s of 7.58 s and saved little.
   - After 64k rows, a filter that has kept more than three quarters keeps every row
     from then on, without looking.

## 3. Measured

The runner built at step 165 against this one, alternated, three rounds of five
iterations at SF=1. CPU time per iteration is from rusage, warm-up included. These
are the twelve queries that get a filter:

| query | filters | CPU per iteration | median wall |
| --- | --: | --- | --- |
| q21 | 3 | 5,344 → 2,355 ms (−56%) | 1,003 → 575 ms |
| q2 | 4 | 246 → 161 ms (−35%) | 56 → 48 ms |
| q11 | 2 | 200 → 139 ms (−30%) | 41 → 35 ms |
| q16 | 1 | 269 → 227 ms (−16%) | 80 → 73 ms |
| q17 | 1 | 1,447 → 1,387 ms (−4%) | 260 → 250 ms |
| q18 | 1 | 1,789 → 1,743 ms (−3%) | 368 → 326 ms |
| q5 | 1 | 1,386 → 1,394 ms | 249 → 227 ms |
| q8 | 6 | 1,449 → 1,445 ms | 273 → 274 ms |
| q9 | 3 | 1,902 → 1,915 ms | 365 → 361 ms |
| q7 | 4 | 2,328 → 2,342 ms | 446 → 453 ms |
| q10 | 1 | 1,338 → 1,336 ms | 289 → 290 ms |
| q20 | 1 | 1,325 → 1,362 ms | 253 → 288 ms |

- **Before the corrections:** q9 was +23% and then +35%, q18 +6%, q8 +5%.
- **q5's and q20's runs spread more than their medians moved.** In a first round q5
  measured −15%.
- **q7's filters do not reach its lineitem.** Its port is a union of two directions,
  each a chain of joins whose probe keys change at every hop: chained filters are
  Tier 2.

## 4. Tests

- **`TestRuntimeFiltersKeepTheAnswer`:** a fact joined to a middle frame and then to
  a small dimension, against the rule turned off, as sets.
  - The join between: inner, left or semi. The outer join: inner, semi, left or anti.
  - One thread, where every build streams and never publishes; eight, where builds
    are partitioned and publish.
  - Nullable keys, some duplicated.
- **`TestRuntimeFiltersGoOnlyWhereTheyCannotDropAJoinedRow`**, by the plan:
  - placed for inner and semi joins through a join, and for a narrower build key;
  - not for left or anti joins, nothing between, `NullsEqual`, a key from a side the
    join between null-extends, a validated join between, or a key promoted at the
    join between.
- **`TestARuntimeFilterNeverEntersACache`:** a shared subtree gets none, and the
  answer is the same.
- **`TestARuntimeFilterDropsRowsBelowAJoin`** (physical): 20,000 fact rows, 16 joined.
  At least 15,000 dropped at the fact's scan, by the counter.
- **`TestAnUnselectiveRuntimeFilterRetires`** (physical): a dimension with 4,900 of
  5,000 keys. The filter retires, by the counter, and the answer stays right.
- **The inventory** gains `runtime_filter.txt`: a filter on a's scan, below the join
  on s, published by the outer join.
- **`TestJoinOptimizerSoundness` now compares as sets.** Its "join of two joins" put
  an inner join under a semi join, so its order was free. The filter made its probe
  side the smaller, step 162 exchanged it, and the rows came in the other input's
  order. A `Head` above a join still pins order, and the planner keeps that.

## 5. Teeth

| tooth | result |
| --- | --- |
| rows dropped before the keys are published | **silent at first,** when every test ran on eight threads; bites on one, where nothing publishes |
| a left join's unmatched rows filtered | **bites:** both root tests, six cases |
| an anti join filtered | **bites:** the same |
| down a side the join between null-extends | **bites:** the placement test |
| through a validated join | **bites:** the same |
| into a `Cache` | **bites:** `TestARuntimeFilterNeverEntersACache` |
| down past a promoted key | **bites:** the placement test |
| `NullsEqual` joins filtered | **bites:** the same |
| keys published only at freeze | **bites:** `TestARuntimeFilterDropsRowsBelowAJoin` |
| a filter that never retires | **bites:** `TestAnUnselectiveRuntimeFilterRetires` |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
