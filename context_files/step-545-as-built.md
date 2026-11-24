# Step 545 — a far Datetime's text names its own instant

**The text side of steps 543 and 544, toward v0.8.2.** #96's DuckDB run (step 539)
found it: `Cast(String)` of Datetime(s) -9223372036854775807 did not start with "-",
where its true year is -292,277,022,657.

## The fix

`dtype.FormatTemporal` writes a Datetime through Go's `time`, whose calendar ends
about the year -292,277,022,399. Past 10^12 days from the epoch, the bound the kernel
uses since step 543, which only a Datetime(s) reaches, `farDatetimeText` writes the
text from dtype's own `civil` (Hinnant's civil_from_days, as the ISO reader uses). It
keeps Go's layout:
- a year of four digits or more after its sign;
- the unit's fraction;
- a zoned instant in UTC with a "Z", as an offset with seconds already is.

Within the bound nothing changes. The display, Cast(String), and the CSV and JSON
writers share `FormatTemporal`, so all of them are fixed.

## Tests

`dtype/fartext_test.go`. The oracle is Hinnant over math/big with fmt's
formatting, which share no code with the text's. It covers:
- int64's ends;
- the last instants Go's path writes (±10^12 days) and the first past them;
- naive and in UTC.

## Checks

On CI only: the maintainer is away, and the laptop is kept quiet.
