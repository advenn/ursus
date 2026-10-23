# Step 349 — as built

**`**` in globs, and CSV and NDJSON directories,** from `polars-parity.md` §5's hive
row's remainder: Polars' globs, whose `**` matches any number of directories, and its
`scan_csv` and `scan_ndjson` of a directory.

## 1. How

**`globFiles`** is every glob's now, `ScanCSVGlob`'s, `ScanNDJSONGlob`'s and
`ScanParquetGlob`'s. A pattern without `**` is `filepath.Glob`'s, as before. One with
it is walked from the elements before its first wildcard, and each file's path below
them matched element by element (`matchElems`): `**` any run of elements, none among
them, every other element `filepath.Match`'s; the files are sorted. A bad element is
refused before the walk, a root that is not there matches nothing.

**`datasetFiles`**, step 347's listing of a directory, moves beside it, its errors named
by the scan, and refuses files of two extensions, as Polars refuses them ("directory
contained paths with different file extensions"), with a glob as the hint: Parquet's
directories as well as the new ones. **`ScanCSV(dir)` and `ScanNDJSON(dir)`** read
every file below a directory as `ScanCSVFiles` and `ScanNDJSONFiles` do, as Polars
reads one: no partition keys, which Polars' CSV and NDJSON scans do not read either.

| Polars 1.44.1 | ursus |
| --- | --- |
| `scan_csv("d/**/*.csv")` | every CSV below `d`, `d/top.csv` among them |
| `"d/x/**/*.csv"`, `"d/x/**/b.csv"`, `"d/x*/**/*.csv"` | as Polars |
| `scan_csv(dir)`, `scan_ndjson(dir)` | every file below it |
| a directory of `.csv` and `.txt` files | refused |
| `"d/**"` | no file in Polars; every file below `d` here (feature-gaps §9) |

## 2. Tests

**`TestGlobsAndDirectories`,** Polars 1.44.1's answers: `**` at the start, in the
middle, before a file's name, after a wildcard, and a pattern without it; no match and
a bad pattern refused; a CSV directory, and one of two extensions refused; an NDJSON
directory and glob; a Parquet glob, and a Parquet directory of two extensions refused.

**Teeth (7 of 7 bite):** `**` not matching no directory; `**` read as
`filepath.Glob`'s `*`; an element's pattern not matched; files of two extensions read;
a CSV directory read as a file; an NDJSON directory read as a file; the walk from the
working directory.

**Gate:** test-all 140 ok, race 28 ok (the root package 761 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
