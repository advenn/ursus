# Step 94 — as built

**A compiled call lives as long as its expression, not as long as the process.**
This is `v0.4-scope.md` item 4, the first of the service-safety items.

## 1. What was wrong

`callCache` in `internal/physical/eval.go` memoises each `Call` node's compiled
form: a regex, an `is_in` probe set, or a `list.contains` needle. Without it, each of
these would be rebuilt for every batch of 8192 rows.

- The key was the node's pointer, so an entry kept its node alive.
- Nothing ever removed an entry.
- In a service that builds a query per request, with a pattern or an `is_in` list
  taken from the request, the map grew by every call of every query, with its
  regexes and probe sets, for the life of the process.

**Measured** (`TestCompiledCallsDoNotOutliveTheirQuery`): 200 finished queries, each
with one regex and one `is_in`, left **400 entries** behind after garbage
collection.

One query collected 200 times left none. A LazyFrame's plan keeps its nodes across
`Collect`s, so a repeated query was never the leak. A new expression was.

## 2. The fix

- **The key holds the node weakly** (`weak.Pointer[expr.Call]`). Weak pointers made
  from one pointer compare equal, before and after the node is collected, so the
  key still identifies the node.
- **A cleanup on the node** (`runtime.AddCleanup`) deletes the entry once the node
  is collected. It is registered once per entry, through `LoadOrStore`.
- **Within a query, and across `Collect`s of a held LazyFrame, the cache still
  hits.** The plan keeps its nodes alive.
- **A failed compilation is cached as before.** Its error does not pin the node:
  `uerr.Error.Node` is the node's rendering, a string. A draft of this step stopped
  caching failures for that reason. A tooth showed the guard changed nothing, the
  error was printed to check, and the guard came out.

## 3. Tests and teeth

**`TestCompiledCallsDoNotOutliveTheirQuery`** has three cases:

- **A new query each time:** no entries left behind (400 before the fix).
- **One query collected again and again:** none either.
- **A held query keeps its compiled calls:** both entries are present while the
  LazyFrame is alive. So a fix that simply stopped caching would fail.

**`TestAFailedCallLeavesNoEntry`:** 100 queries with an unbalanced pattern leave
nothing behind. RE2 rejects that pattern in `compileCall`, which is the only place
it is checked.

`internal/physical/export_test.go` adds `CallCacheLen`. The test waits for cleanups
by collecting garbage until the count settles.

The tests ran five times each under `-race` and with `GODEBUG=simd=0`, and passed
every time.

| tooth | result |
| --- | --- |
| no cleanup registered | **bites:** the new-query and the failed-call cases |
| nothing stored | **bites:** the held-query case |
| the cleanup closure captures the node, which keeps it alive | **bites:** all four cases |
| failures not cached (a draft's guard, removed) | silent: the guard changed nothing |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB. Run once over steps 94 and 95 together.
