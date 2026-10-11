# Step 196 — as built

**O1 of `v0.6.1-scope.md`: `Rolling` and `GroupByDynamic` stream.**

## 1. Why

`temporalSink` buffered its whole input and cut the windows at the end. Its comment
said why: the grid's extent comes from the index's observed minimum and maximum,
known only at the last row. Over a series larger than the budget, both operators
refused.

A sorted index needs no extent, though. A bucket's grid starts at its first row, and
each row can make the windows that start at or before it, empty ones included. That
is the set the buffered grid made: every window from the first row's to the last's.

## 2. What changed

**`tempstream.go` replaces `Consume` and `Finish`.** Each batch is processed and,
for a dynamic group, let go.

- **Buckets.** `tgBucket` is one categorical key's state: its first row's key values
  (one row), its grid position, its open windows, a rolling group's rows in reach,
  and its windows in order.
- **Dynamic** (`gridRow`):
  - the bucket's grid starts at its first row (`firstGrid`: `gridStart`, stepped
    back past a positive offset, and once more for an open lower end);
  - a row makes every grid window starting at or before it;
  - a window is made by the first of its bucket's rows that reaches its start, so no
    earlier row is in it;
  - the row is given to every open window that holds it (`place`). Open windows'
    ends rise, so passed ones are at the front, and none after one the row is below
    can hold it.
- **Rolling** (`rollRow`):
  - a row's window ends at its own instant, plus offset, and reaches back a period;
  - each bucket keeps the rows a window yet to come may reach: those not below the
    current window's lower end;
  - the new window takes them, then its own row, and stays open for later rows it
    reaches: ties, and a forward offset.
- **Membership** (`holds`, `below`, `passed`) is the one place the four closed
  conventions are read, as `rangeOf` was.
- **Pairs.** A row's (row, window) pairs are queued and gathered once a chunk, then
  fed to the ordinary accumulators (`feed`): each window's rows in index order, as
  before. What the aggregates keep is charged as they are fed, once per
  `max(chunk, windows)` pairs, as the buffered sink charged it.
- **Held batches.** A rolling group's kept rows point into their batches, which are
  gathered into one when eight are held (`letGo`), as the as-of join does.
- **The output.** Windows are numbered as made, so across buckets they interleave.
  `Finish` gives each bucket's windows in order, buckets in order of appearance,
  with one `Take` per aggregate. That is the buffered sink's order.
- **Per-window state:** a label and a flag, plus the bounds only while a window is
  open (`tgWin`), and its reported bounds only for `IncludeBoundaries`. The first
  version kept five `int64`s a window, 40 bytes a row for a rolling group.
- **Removed:** the buffered `bucket`, `windows`, `gridWindows`, `rollingWindows`,
  `rangeOf` and `accumulate`.

**Found on the way: state charged too late.** Charged once a batch, a median over a
seven-day rolling window grew the heap by 82 to 108 MB before a 16 MB limit refused;
`TestRollingStateIsCharged` failed. It is charged as it is fed now.

## 3. What it holds

Over 300,000 rows a minute apart, 6.4 MB of input, one sum:

| query | buffered peak | streaming peak |
| --- | --: | --: |
| dynamic, every 1h | 7.5 MB | 0.39 MB |
| dynamic, every 1h, by key | 9.3 MB | 0.89 MB |
| rolling, 30m | 17.3 MB | 10.0 MB |
| rolling, 30m, by key | 18.6 MB | 10.2 MB |

**A dynamic group** holds its windows' state, which is its answer, and a batch.

**A rolling group's** answer has a row for every input row, and its windows' state is
that size. So it holds its output rather than its input, peaking at about twice the
answer while `Finish` builds the answer from the state. With three aggregates that is
22 MB, against the buffered sink's 29 to 30 MB.

Emitting closed windows as they close, rather than at the end, is what would let it
stream its output too. Its output order is per key, so that needs a spill and a
sort; it is left for later.

## 4. Tests and teeth

- **`TestTemporalGroupsAnswerAsTheBufferedSinkDid`:** 120 configurations drawn with a
  fixed seed. `testdata/temporal/golden.json` holds a hash of each one's full answer,
  recorded on the buffered sink before the rewrite.
  - **dynamic:** every, period and offset in minutes and hours, the four closed
    conventions, three labels, two starts, boundaries, and a key or none;
  - **rolling:** periods, offsets back and forward, closed conventions, and a key
    or none;
  - **the data:** 600 rows with ties and nulls;
  - **the aggregates:** `First`, `Last`, `Implode`, `Sum`, `Len`, `Min`, `Median`.

  All 120 answered identically on the streaming sink's first run.
- **`TestTemporalGroupsStreamTheirInput`:** over the 300,000 rows, frame-equal to the
  unlimited answer:
  - dynamic groups under a fifth of their input, peaking at 0.57 and 1.12 MB;
  - rolling groups under 24 MB, peaking at 21.9 MB.

  A tenth of the input refuses the keyed three-hour dynamic case. Its 10,000 windows
  of three aggregates are already that much.
- **Every existing temporal test passes:** Polars' answers for the dynamic options,
  the grid's refusals, and `TestRollingStateIsCharged`.

| tooth | result |
| --- | --- |
| a rolling window not given its earlier rows | **bites:** rolling tests, the golden cases |
| a rolling window closed at its own row | **bites:** the golden cases |
| rows dropped from reach one window early | **bites:** the golden cases |
| the grid not stepped back for an open lower end | **bites:** the golden cases |
| windows made only up to before the row | **bites:** Polars' dynamic cases, the edge of time |
| buckets in the order of their windows | **bites:** the golden cases |
| a datapoint label from the last row | **bites:** the golden cases |
| the input kept, as the buffered sink kept it | **bites:** the out-of-core test |
| the state charged once a batch | **bites:** `TestRollingStateIsCharged` |
| open windows searched past the first a row is below | silent: correct either way, since `holds` refuses those windows; the `break` saves the search |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
