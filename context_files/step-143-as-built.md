# Step 143 — as built

**Item 1 of `v0.5-scope.md`: a subtree a query uses at more than one place runs
once.** It is aimed at PDS-H q15 and q2, the two widest gaps, and it also catches q11.

## 1. Evidence first

**q15 computed its per-supplier revenue twice.** The port uses `bySupplier` for the
join and again under the maximum (`pdsh.go` `q15`). It is one `*plan.Aggregate` with
two parents, but `plan.Resolve` is a `TransformUp`, which copies a shared subtree
into each place it is reached. Step 141 found the reading, filtering and grouping
under it to be about 85% of q15's CPU.

**Explaining all 22 PDS-H queries** found three with a shared subtree that holds a
join, an aggregate or a sort:

- q15: the per-supplier revenue;
- q2: a five-way join, read by a group-by and by the final join;
- q11: a three-way join, read for the total and for the grouping.

## 2. What changed

**`plan.Cache`** (`internal/plan/nodes_cache.go`):

- one child and an ID;
- its schema is its child's;
- Explain labels it `CACHE #n`.

**`plan.MarkShared`** runs before `Resolve` (`lazy.go` `compile`, and Explain):

- It counts each node's **parents**, by pointer, not the paths that reach it. The
  nodes below a shared subtree are reached twice through it, and are not themselves
  shared.
- It wraps in a `Cache` each node with more than one parent whose subtree holds a
  join, an as-of join, an aggregate, a temporal group-by, a sort, a distinct or a
  window.
- A shared scan, or a filter over one, is cheaper read twice than held whole.

**`Flags.ShareSubplans`** switches it, default on. `plan.NoFlags` turns it off, so
the optimizer differential tests, which compare all flags off against all on, and
their bisection cover it.

**The copies stay one.** `Resolve` copies the subtree into each site, under a `Cache`
of the same ID, and the physical planner runs one of them. So every copy has to come
out of the optimizer the same:

- **Predicate pushdown** treats a `Cache` as a barrier, by its default for an
  unknown node: it optimizes below as a fresh root. A filter above one site is not
  pushed into the shared subtree.
- **Projection pushdown** gives every site the **union** of what every site of the
  ID requires (`rule_projection.go`).
  - The union is known only once every site has been reached, so `Apply` sweeps
    again while any union grew, and the last sweep is the answer.
  - The recursion's functions became methods on a `pushState` that carries the
    unions.
- **The build-side rule** keeps a cached subtree's row order if any site needs it
  (`cacheOrdered`), so a swap below one copy and not another cannot happen. It
  passes estimates and order through a `Cache`.

**The physical arm** (`internal/physical/cache.go`):

- `planCache` plans an ID's subtree the first time it is met, through a registry
  `PlanRoot` puts in `Options`, and returns a replay at every site. Options built by
  hand have no registry, and plan each site on its own.
- The subtree runs at the first `Next` of any site, to the end. What it produces is
  held on a `cache` account. If the query goes over its limit, it is written to a
  spill file, which each site reads back with a reader of its own.
- The subtree is closed as soon as it has run, so its own state, a join's table or
  a group-by's answer, does not live as long as the replays. The last site to close
  releases the rest and removes the spill directory.

## 3. What the union fixed, measured before it was built

The first version cached every column of the shared subtree. Alternated against the
previous commit, three rounds of five iterations at SF=1:

| query | before | cached, every column | change | peak RSS |
| --- | --: | --: | --: | --- |
| q15 | 239 ms | 122 ms | **−49%** | unchanged |
| q2 | 122 ms | 206 ms | **+70%** | 120 → 450 MB |
| q11 | 107 ms | 190 ms | **+77%** | 135 → 470 MB |

q2 and q11's sites read different columns of their joins. Held whole, the joins read
and kept every column of five and three tables. **With the union:**

| query | before | shared | change |
| --- | --: | --: | --: |
| q15 | 236 to 256 ms | 125 to 169 ms | −48% |
| q2 | 120 to 183 ms | 72 to 75 ms | −39% |
| q11 | 103 to 107 ms | 59 to 63 ms | −42% |

Peak RSS is unchanged, about 120 to 140 MB for all three.

**At step 127's Polars timings,** q15 goes from 10.5× to about 5×, q2 from 8.4× to
about 5×, and q11 from 4.8× to about 2.7×. That is an estimate on two different
days; the full report at the end of 0.5 will measure it.

## 4. Tests

- **`TestASharedSubtreeRunsOnce`:** q15's shape over a source that counts what it
  reads. Shared, it reads its 16 batches once; with `ShareSubplans` off, twice. The
  answers match.
- **`TestASharedSubtreeGivesEverySiteItsColumns`:** a shared join, one site reading
  `a` and the other `c`. Shared and unshared both answer right. Explain shows both
  sites reading the one cached join with `a` and `c`.
- **`TestASharedResultSpillsPastTheBudget`:** a shared sort of 40,000 rows under a
  64 KiB limit, read at both sites, in order.
  - It peaks at 73 KB, where holding the shared rows would be 320 KB.
  - Its spill directory is empty afterwards.
- **`TestEveryPlanNodeIsCovered`** gains a `cache` entry and a golden plan, and marks
  plans as `compile` does.

**Finding the right fixture for the spill test took three tries:**

1. **One frame of 320 KB:** the ledger counts an allocation whole, so the sort's first
   view of it counted all 320 KB at once.
2. **Forty batches, at the default batch size:** an external sort's merge holds a
   batch per run, five runs of 64 KB.
3. **Forty batches of 512 rows:** the peak shows the cache.

The merge's batch per run is the design, not a leak, and is recorded here only
because it hid the cache.

## 5. Teeth

| tooth | result |
| --- | --- |
| nothing is marked shared | **bites:** the run-once test and the coverage test |
| every site plans its own subtree | **bites:** the run-once and spill tests |
| a shared result never spills | **bites:** the spill test's peak |
| the spill directory is left behind | **bites:** the spill test |
| each site's own columns pushed, not the union | **bites:** the columns test |
| one sweep, before every site is reached | **bites:** the columns test |

The two last were silent until the columns test existed. In the first tests every
site needed the same columns, because a group-by always produces its keys.

**Defensive, not tested:** `cacheOrdered` in the build-side rule. A swap needs a
join whose left input is not sorted; a site that needs order is an as-of join or a
`MergeSorted`, which would refuse such an input either way. It keeps the copies
identical in a case no test reaches.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's, q2, q11 and q15 shared
among them.
