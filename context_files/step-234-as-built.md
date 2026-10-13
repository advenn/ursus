# Step 234 — as built

**The v0.6.4 scope.** No code changed.

The maintainer chose one theme for 0.6.4: out of core, the leftovers.
[`v0.6.4-scope.md`](./v0.6.4-scope.md) was written from:

- three read-only surveys:
  - every `KindResource` refusal left at `678358c`, and the tests that assert one;
  - the window machinery: the in-memory path, the keyed spill, the one-partition
    stream and its hot-key split, the external sort, reverse, the kernels;
  - keyed `Rolling` and `GroupByDynamic`, and the holistic accumulators;
- a design pass on windows over one huge partition: one sorted pass for each
  distinct order, as the scope's section 2 sets out.

**The order of work:**

1. the windows: ordered bounded functions, reversed functions, rank, interpolation,
   memory and messages, a keyless window beside a keyed one;
2. n_unique and window aggregates;
3. keyed `Rolling` and `GroupByDynamic`;
4. Tier 2;
5. the report.

Implode over a hot key stays refused: its answer alone is as large as its input.
