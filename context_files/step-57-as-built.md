# Step 57 — as built

**A Time is a time of day.** `dtype` says `TypeTime` is *"time of day since midnight,
int64 in the type's unit"*. That sentence was the whole of the enforcement — nothing
asserted `0 <= ticks < 24h` at construction, in the resolver, in the kernel, or in any
cast, writer or spill path.

A survey found **thirteen** consequences and **not one of them was an error or a
panic.** Every single one was a plausible-looking wrong answer, and the headline is
that the engine contradicted itself.

Three commits.

Authoritative where it disagrees with [`step-56-as-built.md`](./step-56-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (85 package-ok lines = 17 packages × 5 SIMD configurations),
`make race` exit 0 (17), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. Measured before anything was changed

| expression | printed | sorted | compared |
| --- | --- | --- | --- |
| `23:00 + 2h` | `01:00:00` | **after** `14:00:00` | **unequal** to a real `01:00:00` |
| `01:00 - 2h` | `23:00:00` | — | **unequal** to `23:00:00` |
| `+ 106751 days` | **`00:15:26.290448384`** | | — |
| `Cast(Datetime → Time)` then sort | — | `22:00:00` **before** `06:00:00` | — |

A rolled-over value and a genuine one **render identically and compare unequal**, so a
`group_by` or a join produces two groups with the same label, and sorting yields
output whose printed order looks scrambled.

Each consumer is individually plausible: `FormatTemporal` rolls over through
`time.Unix` and prints the right clock; comparison and sort are a raw integer compare
on ticks and are internally consistent. Only the **disagreement between them** is
visible, which is why every assertion in this step is about agreement rather than
about any one consumer.

---

## 2. The decision: wrap at the point of production

A time of day that is 25:00 is not a time of day, so the invariant is real and the
only question was where to restore it. Restoring it **where the value is made** fixes
all six consumers at once:

```
FormatTemporal   compare/sort/group   Cast(Time -> String)
CSV writer       Parquet writer       spill
```

Two producers, and both now normalise: the arithmetic kernel, and `Cast`.

---

## 3. `Cast(Datetime → Time)` was a fourth silent wrong answer

`Cast` relabels whenever both sides carry the same tick length, so `Datetime(s) →
Time(s)` reinterpreted **seconds since the epoch** as a time of day.

It **printed correctly, by accident** — `ToTime` rebuilds the real instant and the
`"15:04:05"` layout throws the date away — while the stored tick stayed a full epoch
offset. So sorting such a column ordered by **date**, and equality across two days
never matched.

Normalising does not merely restore an invariant here; it makes the cast mean what
the user asked for. It is done once after every arm rather than in each of the four
that can produce a Time.

---

## 4. The invariant fired on checked-in code immediately

`data.CheckTimeRange` follows `CheckNonNullable`'s flag-and-init pattern verbatim, and
it earned its keep the moment it was added, failing **two** things already in the
repository:

- **`dttruncate_test.go`, written one step ago.** It builds a Time column by casting a
  Datetime, for a test about *truncation*. That is how the cast defect went from
  predicted to confirmed — by a test written for an unrelated purpose.
- **A fixture the suite had been carrying.** `TestTemporalParquetRoundTrip` used ONE
  tick list for all seven temporal types, and `86_400_000` is exactly
  midnight-tomorrow in milliseconds while `-1` is a negative time of day. **The test
  was asserting that a value a Time cannot hold survives a round trip.** It does, and
  that is the bug. An instant has no such range, so the original list stays for
  Datetime and Date.

---

## 5. The wrap has to reduce before it adds, and that is the whole subtlety

A pass over `arithNum`'s output cannot work. `addScalar` is `dst[i] = a[i] + b[i]` on
int64 with no check, so a large Duration wraps int64 **before any modulo could see
it**, and the modulo then returns a wrong answer that is comfortably **in range**.

Measured with the arm present and only the reduction removed:

```
23:50 + 106751 whole days  ->  00:15:26.290448384      (want 23:50:00)
```

`CheckTimeRange` cannot catch that — nothing about it is out of range — and the other
eleven tests in the file pass. One tooth distinguishes the two implementations, which
is exactly what the argument-set fork was for.

Go's `%` is truncated rather than floored, so a negative intermediate needs the
modulus added back. That is what makes `01:00 - 2h` come out as `23:00` rather than as
a negative tick that only *prints* as `23:00` by rolling backwards through the epoch.

`Time - Time` is untouched: it produces a **Duration**, signed and unbounded by
design. Wrapping it too turns `01:00 - 23:00` from −22h into +2h — the same mistake
one type over, and a test asserts it.

---

## 6. Two more of the family, neither previously recorded

**`ToDuration` overflowed on the DISPLAY path**, five lines below `ToTime`, which
documents and implements exactly that defence. A `time.Duration` is itself int64
nanoseconds, so it spans ~292 years, and a `Duration(Second)` column reaches that at
9.2e9 ticks — which `Datetime - Datetime` produces from two instants three centuries
apart.

```
three centuries of seconds  ->  -2346317h47m53.709551616s
the negative of it          ->  +2346317h47m53.709551616s
```

`FormatTemporal` discarded the second return value, which is why it printed as an
ordinary span with the wrong sign. The fallback is now the raw tick count with its
unit: a number a reader can see is unusual beats a duration that looks ordinary and
is wrong.

**Parquet's `TIME(MILLIS)` narrowing was a bare `int32(v)`** — the construct step 49
removed from `rescaleTemporal`, where it was *"a silently wrapped date"*. Here it also
emitted an out-of-spec `TIME(MILLIS)`, whose Parquet range is `0..86_399_999`.

---

## 7. Teeth

| tooth | result |
| --- | --- |
| remove the kernel's Time arm | **bites** — eleven failures across print, sort, compare and every unit |
| **remove only the reduce-before-add** | **bites, alone** — `00:15:26.290448384`, in range and wrong; the other eleven tests pass |
| remove the `Cast` normalisation | **bites** — on `dttruncate_test.go`, checked in one step earlier |
| turn `CheckTimeRange` off as well | **bites differently** — see below |
| restore `ToDuration`'s bare multiply | **bites** — a positive span renders negative and a negative one positive |
| wrap `Time - Time` too | **bites** — `01:00 - 23:00` becomes +2h |
| disable the Parquet guard | **did not bite**, then re-aimed — §8 |

### The fourth tooth is the interesting one

With the cast fix **and** the flag both off, `dttruncate_test.go` goes green again and
only the ordering test fails. So the two instruments catch different things:

- the **ordering test** knows about the cast specifically;
- the **flag** fires on code written for an unrelated purpose, which is the case no
  test author would have thought to write.

Neither is redundant, and saying "the flag is the only witness" would have been wrong.

---

## 8. A tooth that did not bite, and what was done about it

Disabling the Parquet guard changed nothing: after §3 and §5 the value always fits, so
the guard is a **proof rather than a repair** — which is the right order, and which
also made it unreachable from any ordinary query. Unreachable code with no test is how
a guard rots.

So rather than record a tooth that does not bite, it was **aimed**: a test stands
`CheckTimeRange` down for as long as it takes to build a column no producer in the
engine can make any more — exactly the shape a future source could produce — and
asserts both boundaries, so the guard cannot be satisfied by refusing everything. The
re-aimed tooth bites.

---

## 9. An instrument caught my own mistake, for the fourth step running

The first `ToDuration` fixture claimed `MaxInt64/1e6` milliseconds overflows. It does
not — that is the largest span that fits, and the boundary is one tick further on. The
engine was right and the fixture was wrong, which is the same shape as step 52's Date
epochs and step 53's `"cd"`.

---

## 10. What is still open

- **`Datetime ± Duration` wraps int64 silently**, and a Go `time.Duration` literal
  lifts straight to `Duration(Nano)`, so no cast intervenes to catch it. Making
  temporal add and sub partial — a null on overflow, per `rescaleTemporal`'s stated
  principle — touches `MayProduceNull`, `arithNum` and the nullability declarations.
  Its own step. `Date ± Duration` is guarded in the *cast* and not in the addition,
  and the guard is unreachable for Date anyway: int32 days × 86400 cannot overflow.
- **List and Struct in the shared contract fixture**, surveyed and sized rather than
  attempted: two columns add ~1568 labels, 19 runs and a predicted **32** Field/Eval
  disagreements, all one shape. `dtype.Promote` succeeds for two identical Lists via
  `a == b` — nested types intern their payload — while `resolveComparison` consults
  `isOrdered` only for the *ordering* ops, so `==`, `!=`, `<=>` and `<!>` reach
  `dispatchCompare`, which has no arm. Two findings from that survey deserve their own
  lines: **`listCallOut` returns `Float64` for `list.mean` without inspecting the
  element type** while `listReduce` refuses a non-numeric one — the same shape as step
  56's `dtCallOut`, and **which element type the fixture picks decides whether it is
  visible** — and **`kernel.NullColumn` has a List arm and no Struct arm**.
  `kernel.Select` and `GroupKeyEncoder` have neither, correctly mirrored by
  `genCallOut`'s `IsHashable` gate for `is_in` but by nothing for a conditional.
- **The byte-flip spill sweep**, deferred a fifth time. Zero bounds checks in the
  reader; every length varint reaches `make` before the read that would bound it; a
  zero-length validity buffer silently becomes *all-set*; a corrupt `TimeUnit` byte
  has no unknown-value arm. It keeps losing to defects reachable from ordinary user
  code, which has been the right call each time.
- `Optimizer.Verify` is off in the golden inventory (`inventory_test.go`, a bare
  `plan.NewOptimizer()` — one line) and in `Explain` (`lazy.go`'s `optimizer()`, which
  never sets it; only `compile` does). **UDF name uniqueness** is never enforced, and
  the obvious implementation is a trap: `kernel.ColumnUDF` is a named **func** type,
  so comparing two `UDFImpl` values with `==` panics rather than answering — identity
  has to go through `reflect.ValueOf(impl).Pointer()`. Nothing keys a map on one
  today, so it is latent.
- The standing list: ~37 weak `errors.Is` accept-assertions against two `ErrInternal`
  rejections and one `errors.As` in the whole corpus, `Writer.Write`'s unwrapped error
  and `Open`'s discarded read error, `callCache`'s missing eviction, `List().Join()`
  and `Str().Join()`, the UDF serial opt-out, `.list` set operations, `Expr`-level
  selection, `Pivot`, and a benchmark suite thirty-three commits stale on a machine
  that cannot run it.
