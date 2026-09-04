# Step 6 — as built

The string and temporal namespaces, and the temporal type family made real.
Authoritative where it disagrees with [`step-5-as-built.md`](./step-5-as-built.md)
and the vision docs.

**567 tests green** under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`,
with the experiment off, and under `-race`.

```go
df, err := ursus.ScanCSV("events.csv").
    Filter(ursus.Col("name").Str().Contains("error", true)).
    WithColumns(
        ursus.Col("ts").Dt().Year().Alias("year"),
        ursus.Col("ts").Dt().Truncate(time.Hour).Alias("bucket"),
    ).
    Collect(ctx)
```

Step 6 opens v0.2. `dataframe-features.md` §14 buckets the namespaces there, but
§5.7 marks `.str` **P0** and §5.8 marks `.dt` **P0/P1** — "must exist for v0.1 to be
useful" — so this is the item the project's own priority key ranked highest.

---

## 1. The temporal family was advertised and half-broken

Six defects. **No test referenced `time.Time` at all** before this step, which is
exactly how a type family stays broken while looking supported.

| # | Defect | What a user saw |
| --- | --- | --- |
| 1 | A Datetime rendered as its tick count | `2024-01-01` printed as `1704067200000000000` |
| 2 | **`CastTimeUnit` was a silent no-op** | `Datetime(ns) → Datetime(s)` relabelled without dividing, so an instant became the year **55,974,289** |
| 3 | `instant ± duration` did not reconcile units | `Datetime(s) + Duration(ns)` off by 10⁹ |
| 4 | `Duration * 2.5` failed at execution | the type checker accepted it, then the kernel asked a Float64 column for `[]int64` |
| 5 | `CanCast` promised casts the kernel refused | `Col("s").Cast(Date)` type-checked, planned, and failed at runtime |
| 6 | `[]time.Duration` lost its type | landed as a bare Int64 |

**#2 is the worst and was not in the audit.** It surfaced only when a probe printed
the result: `kernel.Cast`'s "same physical layout, different logical meaning —
free, just relabel" rule is right for `Int64 ↔ Datetime`, which is the documented
way to reach the tick count, and catastrophic for `Datetime(ns) → Datetime(s)`,
because those are also both Int64. Data corruption wearing a no-op's clothes.

The rule is now: **rescale when both sides carry a tick length; relabel only when
they agree, or when one side is a bare integer.** Widening checks for overflow and
produces NULL rather than a wrapped instant; narrowing floors toward negative
infinity, so instants either side of the epoch stay monotone rather than both
collapsing onto day 0.

### One correction to an earlier claim

I reported that `Values("ts", []time.Time{...})` silently produced an all-null
column. That was wrong — `series.go:60` has a `[]time.Time` case and it works. The
silent-null path is narrower: `convertNamedSlice`'s `default`, reached only by a
*defined type over* `time.Time`.

### #4 turned out to be a semantic question, not a binding bug

Binding the float to the Int64 kernel was the old failure. Two fixes were available
and both were worse than an error: truncating the multiplier makes `1h * 2.5`
silently `2h`, and there is no exact answer at integer resolution. So it is refused
at **plan** time — which was the actual complaint, a runtime failure for something
already type-checked — with a message naming a conversion that works:
`.Cast(Float64).Mul(2.5).Cast(Duration(...))`. That path is tested.

## 2. `expr.Call` — the one real design decision

`Contains(pattern, literal)` and `Truncate(every)` carry **arguments**, and
`Unary{Op, Child}` has nowhere to put them. Three shapes were possible: one op per
(function, argument) pair, which does not terminate; arguments smuggled into the op
enum, which is the same thing wearing a hat; or one node holding a function and its
operands.

`Call{Fn CallFn, Args []Node}` is the third, with `Args[0]` the receiver. Args are
**ordinary children**, so `RootNames`, expansion and projection pushdown see through
a Call with no special case — the property the design docs predicted for window
nodes, holding here for the same reason.

Non-receiver args must be literals. A column-valued pattern is *representable* in
the IR and rejected with a message saying so, which is the right way round for a
constraint that may lift later.

A new node type has to reach the arms that otherwise fail silently:
`substitute` (a `panic`, and without the arm expansion would drop a Call's
children) and `extractAggs`. `Eval` and `Field` got explicit arms.

This is also the shape `.list`, `.struct` and `.arr` will need, so it is paid for
once rather than three more times.

## 3. What shipped

**`.str` (23 methods)** — `Contains`, `StartsWith`, `EndsWith`, `Find`,
`CountMatches`, `Extract`, `Replace`, `ReplaceAll`, `ToLower`, `ToUpper`,
`LenBytes`, `LenChars`, `Slice`, `Head`, `Tail`, `StripChars`, `StripPrefix`,
`StripSuffix`, `Reverse`, `ToInteger`, `ToDate`, `ToDatetime`.

**`.dt` (19 methods)** — `Year`, `Month`, `Day`, `Hour`, `Minute`, `Second`,
`Millisecond`, `Microsecond`, `Nanosecond`, `Weekday`, `OrdinalDay`, `Quarter`,
`Week`, `Epoch`, `Truncate`, `TotalDays/Hours/Minutes/Seconds`, `ToString`.

Decisions inside them worth knowing:

- **`Find` returns NULL for no match, not −1.** A sentinel index compares and does
  arithmetic like a real position, so `find(x) < 5` would be true for "not found".
- **There is no `Len`.** `LenBytes` and `LenChars` differ on any non-ASCII input and
  which one is wanted is not guessable.
- **`Slice` and `Reverse` work in runes**, so they cannot split a character in half.
- **`Weekday` is ISO** — Monday 1, Sunday 7. Go's `time.Weekday` puts Sunday at 0,
  and passing that through is the classic off-by-one in date libraries.
- **`Week` is the ISO 8601 week**, which is not "day of year / 7": the first week of
  a year can begin in the previous one.
- **Components are read in the column's own timezone.** `2024-01-01T00:00:00+09:00`
  is hour 0 in Tokyo and hour 15 in UTC; reading in UTC would make `hour` wrong for
  every row of any non-UTC column, silently.
- **A Duration has no calendar components.** `.Year()` on one is refused at plan
  time — a duration is a length, not a point in time.
- **`literal` defaults to true.** Go's `regexp` is RE2: no catastrophic
  backtracking, but no JIT either, so a plain substring test through it costs far
  more than `strings.Contains`. The flag is explicit rather than inferred from
  whether the pattern *looks* like a regex.

## 4. One parse/format core, four callers

`.str.ToDatetime`, `.dt.ToString`, the CSV reader and the CSV writer all need the
same two functions. `dtype/temporal.go` holds them — `ToTime`/`FromTime`,
`FormatTemporal`/`ParseTemporal` — so a frame printed to a terminal and a frame
written to a file agree by construction.

Step 2's audit found the running-schema walk written four times with two copies
already diverged. Four copies of a date parser is the same defect with more surface.

**Temporal now round-trips through CSV**, which it did not before: the writer
refused temporal precisely because its output would not be readable by its own
reader. `TestTemporalCSVRoundTrip` is that property as a test.

The reader is forgiving where the writer is strict — a space instead of `T`, a
missing timezone — because the reader is the one handed files it did not create.

## 5. No SIMD here, and that is not an oversight

Strings are outside the `Numeric` constraint entirely, and temporal columns are
Int32/Int64, which `simd_amd64.go` records as having no portable SIMD path today —
only float64 does. So the scalar/SIMD/differential machinery does not apply: there
is no scalar twin to differ from, because there is only one implementation.

**What replaces it is a differential test against the standard library**, which is
the same argument the hand-rolled CSV scanner makes:

- Every `.dt` component is checked against `time.Time`'s own `Year()`, `ISOWeek()`
  and friends over a corpus with leap days, 1900 (not a leap year), 2000 (is one),
  three year-boundaries where the ISO week belongs to the neighbouring year, and
  instants before the epoch.
- Every `.str` function is checked against `strings` over a corpus with empty
  strings, multi-byte UTF-8, emoji and invalid UTF-8 — and the test **fails if no
  corpus entry made `LenBytes` and `LenChars` differ**, so the multi-byte cases
  cannot quietly stop proving anything.

## 6. Honest gaps

- **Anything returning a List is out**: `Split`, `SplitExact`, `SplitN`,
  `ExtractAll`, `ExtractGroups`, `FindMany`. `data.Column` has four payload slots —
  fixed, bits, offsets, chars — and none is a list. Adding one is a storage-layer
  step, and `Take`, `Concat`, `NullColumn` and `GroupKeyEncoder` would all need arms.
- **Parquet still refuses `Time`, `Datetime` and `Duration`.** `Date` round-trips,
  as it did before. This was the piece the plan named as first to drop, and it was
  dropped: CSV is where the temporal gap actually bites.
- No timezone conversion (`ConvertTimeZone`, `ReplaceTimeZone`), business days,
  Aho-Corasick `_many` variants, unicode normalization, JSON, `strftime` with a
  custom format, `PadStart`/`ZFill`, `ToTitle`.
- **CSV inference has no temporal rung.** `dtype.LooksTemporal` exists and is
  deliberately conservative — it requires a separator, so a column of `20240101`
  integers cannot become dates — but the inference lattice does not consult it yet.
  Reading dates from CSV needs an explicit schema.
- **`Duration` has no Parquet logical type**, so it stays refused there on purpose
  rather than for lack of work.
- Still open from earlier steps: window functions, streaming/spilling, external
  sort, as-of join, parallel pipeline breakers.

## 7. Verification

```bash
make test-all   # 4 SIMD widths + experiment-off
make race
make levels
```

| Test | What it would otherwise miss |
| --- | --- |
| `TestCastTimeUnitRescales` | defect #2 — the relabel that made an instant the year 55,974,289 |
| `TestInstantPlusDurationReconcilesUnits` | defect #3, at all four resolutions |
| `TestDatetimeRendersAsATimestamp` | defect #1 |
| `TestStringCastsAgreeWithCanCast` | defect #5, both directions, strict and lossy |
| `TestDurationScaledByNumber` | defect #4, and that the suggested workaround works |
| `TestDurationColumnKeepsItsType` | defect #6 |
| `TestDtComponentsMatchGoTime` | leap years, ISO week boundaries, pre-epoch instants |
| `TestStrMatchesStdlib` | byte-vs-rune confusion — and it fails if the corpus stops exercising it |
| `TestStrRegexAndLiteralDiffer` | a `literal` flag that does nothing, so `a.b` silently matches `axb` |
| `TestNamespaceNullsPropagate` | a kernel reading the payload at a null position |
| `TestNamespacesAreParallelSafe` | a shared compiled regex across N workers |
| `TestTemporalCSVRoundTrip` | a writer whose output its own reader rejects |

Teeth verified by reintroduction: restoring the relabel-without-rescale, restoring
the unreconciled `instant ± duration`, and making `LenChars` count bytes each fail
their test.

One existing test changed: `TestSinkCSVIsAtomic` used a Datetime column as its
"unsupported type", which step 6 made supported. It now uses a failing strict cast —
atomicity should be tested against a failing *query*, not against whichever types
happen to be unwritable this month.
