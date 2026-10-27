# Step 398 — as built

**A small join's keys.** B4 of `v0.8-scope.md`.

## 1. What was wrong

A join's build keys its table by the integer, one integer key's or several packed into
words, only when it runs partitioned: deferred to its `freeze`, which `deferBuild`
allowed on more than one thread under the default budget or none (steps 151, 159,
391). On one thread, and under a limit the caller set, the streaming build encoded
every key to bytes, and the probe each of its keys with them. Step 397's build, the
join of `svcprobe lat` to a 100-row dimension: on one thread 936 µs at 10,000 rows and
6,594 at 100,000, against 656 and 2,508 on all threads; under a limit of 1 GiB on one
thread 820 and 6,501. Step 396 kept a join on its threads past a batch for it.

And the probe spent a third of its own time on a call a row: `enter` and
`appendMatches`, made for a residual, a spill to route to and `Validate`, which a plain
join has none of.

## 2. How

- **One thread defers too** (`deferBuild`): under the default budget, an engine's or
  none, a keyed join's build waits for `freeze` and runs as one partition, its keys
  integers or packed words as on several. Past half the budget it still catches up and
  streams, as it can spill.
- **The streaming build keys its one integer** (`joinBuildSink.ints`, `admitInts`):
  under a limit the caller set, or once a deferred build catches up, a key of one
  integer column whose nulls match nothing goes into an `IntKeyTable`, never encoded,
  and `freeze` hands the probe an `IntKeyParts` of it. A split routes a key by its
  encoded bytes, so `escalate`, and `Merge`, turn the table into encoded keys first,
  each id kept (`bytesKeys`): its integers made a column of the key's type again
  (`kernel.IntKeyColumn`), which encodes as the column they were read from, a tag and
  the value by its width. A spill's sub-sinks key by bytes, as before. An Enum is
  keyed by its index, which it encodes as; a Categorical is stored as its text and was
  never an integer key.
- **A plain probe** (`stepPlain`): where a row's output is its match list alone, a
  keyed join that emits pairs, nothing spilled and no `Validate`, one loop does what
  `enter` and `appendMatches` did, the same pairs in the same order, cut at the same
  batch size.
- **Left:** several integer keys packed into words stay encoded in the streaming
  build: a packer has no unpacking, which a split would need. A join of several
  integer keys under a caller's limit encodes, as before.
- **Step 396's rule for a join** needs no change: with a pipeline of at most 16,384
  rows serial, the default takes one thread's time or less up to it, the join too,
  0.97 to 1.01 at 8,000 to 16,000 rows; past it, a join's threads are the quicker, 1.12
  at 24,000 and 1.38 at 32,000.

## 3. Measured

`svcprobe lat`, step 397's library and this one under the same probe, four runs each
alternated, at a load of 2 to 5:

| rows | the join to a 100-row dimension | step 397 | here |
| --- | --- | --- | --- |
| 1,000 | all threads | 95.9 µs | 77.6 |
| | one thread | 97.5 | 76.7 |
| | one thread, a limit of 1 GiB | 96.6 | 55.3 |
| 10,000 | all threads | 656.2 | 593.8 |
| | one thread | 935.8 | 509.5 |
| | one thread, a limit of 1 GiB | 819.5 | 438.1 |
| 100,000 | all threads | 2,508.3 | 2,250.0 |
| | one thread | 6,594.0 | 3,051.9 |
| | one thread, a limit of 1 GiB | 6,500.6 | 2,812.1 |

The plain probe alone, step 398's keys with and without it: one thread 0.83 at 10,000
rows and 0.84 at 100,000; all threads 0.93 and 0.97. The other three queries, which
nothing here touches: 0.88 to 1.10 a query, the 1,000-row ones again six times
alternated 0.95 to 1.10. All threads at 10,000 rows remain 3.8 times Polars' 157 µs:
the probe's gathers, the collector's quarter, which B3 is for, and `Collect`'s copy.

PDS-H at SF=0.1 against step 397's runner, four rounds: 0.98 on four threads given,
1.00 on the default's; q18 at 1.13 on four, again six rounds 0.96.

## 4. Tests

- **`TestTheStreamingBuildKeysItsInteger`** (`physical`): an Int8, Int16, Int32, Int64,
  Uint8, Uint16, Uint32 and Uint64 key, a Date's and an Enum's, every tenth null,
  Inner, Left, Full and Semi: the streaming build under a caller's limit reads an
  `IntKeyParts` and answers the partitioned build's rows; under 16 KiB a kind that
  keeps its rows splits, its keys encoded, and answers them again.
- **`TestASemiJoinSpillsItsEncodedKeys`** (`physical`): a Semi and an Anti join of
  5,000 keys of five types under 16 KiB split and answer as in memory. A kind that
  keeps its rows builds its table again from them at the split; a Semi or Anti join
  keeps the encoded one, which is what tests the encoding.
- **`TestAPlainProbeCutsItsBatches`** (`physical`): four probe rows of 1,800 matches
  each come in batches of at most 512 rows, 7,200 of them.
- **`TestAOneThreadBuildDefers`** (`physical`): one thread defers under no budget and
  the default, and streams under a caller's limit; its build reads integers.
- **Changed:** the join tests' reference build, "one thread", deferred now, so it is
  `streamingOpts`, one thread under a limit of the caller's; a caught-up Semi join's
  keys are counted in its integer table; `TestJoinAccountsItsHashTable`'s floor on one
  thread is the integer tables', 3/4 MiB, and it runs the streaming build under a
  limit too. `TestTheDefaultBudgetSetsTheProcessMemoryLimit` sets the default engine
  aside and back: run first, as `-run` ran it, it made the engine of its lowered limit
  for every later test, which step 395 left.

**Teeth: 8 of 8 bite:** one thread not deferring; the streaming build encoding; a
split keeping the integers; keys encoded as Int64s whatever their width, which only
the Semi join's spill bites; `freeze` reading the encoded table; the plain loop
marking no match, dropping unmatched rows, and running past a batch, the last of which
only the batch test bites.

**Gate:** the light gate, 28 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1. The internal packages and the root in
full before it.
