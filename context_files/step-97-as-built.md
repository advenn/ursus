# Step 97 — as built

**`SetProcessMemoryLimit`: an opt-in Go soft memory limit near the container's
ceiling.** This is `v0.4-scope.md` item 7, whose decision was "an opt-in function or
a documented recipe". It is both: the function, and a pointer to it from the
README and from `WithMemoryLimit`.

## 1. The gap

Step 90 budgets every query at half the process's ceiling. Half, because ursus's
buffers live on the Go heap, and with `GOGC=100` the heap grows to about twice what
is live before it collects. The budget bounds what is live. Nothing bounded the
slack, and the v0.3.1 report found gb10 over CSV at 7.58 GB under an 8 GB cgroup.

**Measured** (a scratch program): a 300 MB live set, with 256 KB batches built and
dropped around it, as a query does.

| | peak heap in use | peak runtime memory |
| --- | --- | --- |
| defaults | 613–634 MB | 645–653 MB |
| `debug.SetMemoryLimit(450 MB)` | 416–423 MB | 437–449 MB |

The soft limit is what keeps the slack under a ceiling. Only the program can choose
it.

## 2. The decision

**Opt-in, never automatic.** The limit is the whole process's: setting it changes
how the program around ursus collects garbage too, and a library must not decide
that on import.

`ursus.SetProcessMemoryLimit()`:

- **Sets the runtime's soft limit to nine tenths of the ceiling.** The ceiling is
  the smaller of the cgroup's limit and RAM, as the default budget reads them.
  `execopt.Ceiling` is new; `DefaultLimit` is now `Ceiling()/2`.
- **Keeps a limit already chosen,** and returns it: `GOMEMLIMIT` in the
  environment, `off` included, or an earlier `debug.SetMemoryLimit`.
- **Sets nothing where no ceiling can be read,** which is anywhere but Linux.
- **Returns the limit in effect,** with `math.MaxInt64` meaning none.

The default budget stays at half. With the soft limit set, the slack above the
budget is the collector's to manage. Raising the budget fraction would be a separate
decision with its own measurement.

## 3. Tests and teeth

**`TestSetProcessMemoryLimit`** has four cases, with the ceiling injected through
`processCeiling`:

- nothing chosen: nine tenths;
- an existing limit kept;
- `GOMEMLIMIT=off` kept;
- no ceiling: nothing set.

Each case checks both the return value and the runtime's actual limit, and the
runtime limit is restored afterwards.

| tooth | result |
| --- | --- |
| the environment ignored | **bites:** the `GOMEMLIMIT=off` case |
| an existing limit overridden | **bites:** the existing-limit case. A first mutation removed `math`'s only use and failed to build, which does not count; the second compiled. |
| the whole ceiling, not nine tenths | **bites** |
| a limit set with no ceiling | **bites** |
| the default budget no longer halved, after the refactor | **bites:** all six fixture cases of `TestDefaultLimitIsHalfTheSmallerCeiling` |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
