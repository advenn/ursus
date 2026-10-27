# Step 399 — as built

**A zone loaded once, a format compiled once.** B5 of `v0.8-scope.md`.

## 1. What was wrong

`time.LoadLocation` reads and parses a zone's file every time it is called, and ursus
called it for every instant `dtype.FormatTemporal` formatted, which a cast to text, a
frame's printing, a literal and an error message use, and for every batch a kernel
read in a zone: `locationOf` and `newZoneClock`, a rolling window by time, a temporal
group, the CSV and JSON writers' columns. `strftime.Compile` parsed a format for every
batch `dt.strftime` and `str.to_datetime` ran on.

`svcprobe zone` on step 398's build, a million instants in Europe/Amsterdam, the
median of five:

| | all threads | one thread |
| --- | --- | --- |
| strftime | 120.9 ms | 282.7 |
| cast to text | 3,168.6 | 7,742.9 |
| truncate to the hour | 33.1 | 97.5 |
| the hour | 12.9 | 31.4 |
| a frame of them printed | 0.10 | |

The cast to text was 7.7 µs an instant.

## 2. How

- **`dtype.Location`** (`dtype/zone.go`) is `time.LoadLocation`, each zone kept once it
  loads, in a `sync.Map`. Only a zone that loads is kept, so a service passing the
  names its requests carry cannot grow it past the zones there are; one that does not
  is looked for again, and refused again. Every `time.LoadLocation` in the module goes
  through it: `FormatTemporal`, the kernels, the writers, `Rows`' instants,
  `ToDummies`, Arrow's zones, and the planner's checks of a zone's name.
- **`strftime.Shared`** (`internal/strftime/shared.go`) is `Compile`, each format kept
  and its `Format` shared, which nothing writes once compiled, at most 256 of them:
  past that a format is compiled each time, as before, so a service passing its users'
  formats does not grow it without end. A format that does not compile is not kept.
  The kernels compile through it; the planner and the CSV writer compile once a
  query, as they did.

## 3. Measured

`svcprobe zone`, this build, at a load of 6 to 7:

| | all threads | one thread |
| --- | --- | --- |
| strftime | 120.5 ms | 285.5 |
| cast to text | 156.0 | 453.0 |
| truncate to the hour | 39.4 | 105.6 |
| the hour | 13.4 | 40.2 |
| a frame of them printed | 0.05 | |

The cast to text 20 times quicker on all threads and 17 on one, 453 ns an instant. A
strftime, a truncation and an hour were not held up by a load or a compile a batch,
a few hundred of a million values' work: they are as they were, within the load.

PDS-H at SF=0.1 against step 398's runner, four rounds on four threads: 1.02. It reads
no zone.

## 4. Tests

- **`TestAZoneIsLoadedOnce`** (`dtype`): Europe/Amsterdam is the same `Location` twice,
  and gives `time.LoadLocation`'s wall clock; a name that is no zone is refused twice
  and not kept.
- **`TestAZonedInstantFormatsInItsZone`** (`dtype`): the epoch in Tokyo is
  1970-01-01T09:00:00+09:00, twice.
- **`TestAFormatIsCompiledOnce`** (`strftime`): a format is one `Format` twice; one of no
  directive is refused twice and not kept; 512 formats keep no more than 256.

**Teeth: 4 of 6 bite:** a zone not kept; a zone that does not load kept; a format not
kept; no bound on the formats. Silent: `FormatTemporal` loading its zone each time and
a kernel compiling its format each batch, whose cost is time, measured above, and no
answer.

**Gate:** the light gate, 28 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1. The internal packages, `dtype` and the
root in full before it.
