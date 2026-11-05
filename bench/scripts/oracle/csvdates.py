"""The csvdates group: read_csv(try_parse_dates=True), and a Date, a Datetime or a Time
column read under a given schema, by Polars' patterns (csvdates_test.go, step 355).

A case's input is not a frame but a CSV's text, recorded as the param `csv`; the Go
side scans it. Not here, with the reason: two values of two patterns in one column
("2020-01-02" beside "02/01/2020", "10:30" beside "10:30:00.123"), which Polars
refuses, reading a column by its first value's pattern, and ursus reads each by its
own (feature-gaps.md §9): Go's time package is the oracle there; the refusal of a late
value that does not parse; and a Date's and a Datetime's value as a time.Time, a Go
API, held to Go's time package.
"""

from __future__ import annotations

import io
from datetime import datetime, time

import polars as pl

from . import Case
from .gen import Gen


def csv(name: str, text: str, note: str = "", **opts) -> Case:
    text = "x\n" + text
    return Case(name, {}, lambda: pl.read_csv(io.StringIO(text), try_parse_dates=True, **opts), params={"csv": text}, note=note)


def given(name: str, text: str, dtype: pl.DataType) -> Case:
    text = "x\n" + text
    return Case(name, {}, lambda: pl.read_csv(io.StringIO(text), schema_overrides={"x": dtype}), params={"csv": text})


def lines(vals: list[str | None]) -> str:
    return "".join(("" if v is None else v) + "\n" for v in vals)


def cases() -> list[Case]:
    g = Gen(4985)
    dates = g.dates(120, edges=False).to_list()
    dates[-1] = dates[-1] or dates[0]
    dts = g.datetimes(120, "us", lo=1960, hi=2040, edges=False).to_list()
    dts[-1] = dts[-1] or dts[0]
    tods = [None if g.rng.random() < 0.1 else int(v) for v in g.rng.integers(0, 86_400_000_000, 120)]
    tods[-1] = tods[-1] or 0
    gen_dates = lines([None if v is None else v.isoformat() for v in dates])
    gen_slash = lines([None if v is None else v.strftime("%d/%m/%Y") for v in dates])
    gen_dts = lines([None if v is None else v.strftime("%Y-%m-%d %H:%M:%S.%f") for v in dts])
    gen_zoned = lines([None if v is None else v.strftime("%Y-%m-%dT%H:%M:%S.%f") + "+05:30" for v in dts])
    gen_times = lines([None if v is None else time(v // 3_600_000_000, v // 60_000_000 % 60, v // 1_000_000 % 60,
                                                   v % 1_000_000).isoformat() for v in tods])
    late = "2020-01-03\n" * 150 + "abc\n"
    dt_us, utc = pl.Datetime("us"), pl.Datetime("us", "UTC")
    return [
        csv("iso_dates", "2020-01-02\n2021-12-31\n"),
        csv("slash_dates", "2020/01/02\n2021/12/31\n"),
        csv("dot_date", "2020.01.02\n"),
        csv("day_first_slash", "02/01/2020\n31/12/2021\n"),
        csv("day_first_dash", "02-01-2020\n"),
        csv("day_first_dot", "02.01.2020\n"),
        csv("unpadded_iso", "2020-1-2\n"),
        csv("one_digit_day_first", "1/2/2020\n", note="the 1st of February: a day comes first"),
        csv("year_0999", "0999-01-02\n"),
        csv("blank_line_is_null", "2020-01-02\n\n2021-12-31\n"),
        csv("month_first_is_text", "01/02/2020\n12/31/2021\n", note="a month first is not a pattern: 12/31 is no day of a month 31"),
        csv("not_a_date_is_text", "2020-01-02\nnot a date\n"),
        csv("february_30_is_text", "2020-02-30\n"),
        csv("month_13_is_text", "2020-13-01\n"),
        csv("trailing_space_is_text", "2020-01-02 \n"),
        csv("month_name_is_text", "Jan 2 2020\n"),
        csv("digits_are_an_integer", "20200102\n"),
        csv("datetimes_with_a_space", "2020-01-02 03:04:05\n2021-12-31 23:59:59\n"),
        csv("datetimes_with_a_t_and_a_fraction", "2020-01-02T03:04:05\n2021-12-31T23:59:59.123\n"),
        csv("a_space_beside_a_t", "2020-01-02 03:04:05\n2020-01-02T03:04:05\n"),
        csv("nanoseconds_cut", "2020-01-02T03:04:05.123456789\n", note="a fraction past microseconds is cut"),
        csv("half_a_second", "2020-01-02 03:04:05.5\n"),
        csv("no_seconds", "2020-01-02 03:04\n"),
        csv("one_digit_hour", "2020-01-02 3:04:05\n"),
        csv("day_first_datetime", "02/01/2020 03:04:05\n"),
        csv("slash_datetime", "2020/01/02 03:04:05\n"),
        csv("pm_is_text", "2020-01-02 03:04:05 PM\n"),
        csv("z_offset", "2020-01-02T03:04:05Z\n", note="an offset is the instant, in UTC"),
        csv("z_offset_fraction", "2020-01-02T03:04:05.123Z\n"),
        csv("offset_0900", "2020-01-02T03:04:05+0900\n"),
        csv("offset_after_a_space", "2020-01-02T03:04:05 +0900\n"),
        csv("offsets_with_a_colon", "2020-01-02T03:04:05+09:00\n2020-01-02T03:04:05+01:00\n"),
        csv("offset_0530_fraction", "2020-01-02T03:04:05.123+05:30\n"),
        csv("offset_zero", "2020-01-02T03:04:05.000000+0000\n"),
        csv("zoned_beside_naive_is_text", "2020-01-02T03:04:05\n2020-01-02T03:04:05+0900\n"),
        csv("datetime_beside_date_is_text", "2020-01-02 03:04:05.123\n2020-01-02\n"),
        csv("date_beside_datetime_is_text", "2020-01-02\n2020-01-02 03:04:05\n"),
        csv("times", "10:30:00\n11:00:00\n"),
        csv("time_fraction", "10:30:00.5\n"),
        csv("time_no_seconds", "10:30\n"),
        csv("hour_25_is_text", "25:00:00\n"),
        csv("time_pm_is_text", "10:30 PM\n"),
        csv("time_trailing_x_is_text", "10:30:00x\n"),
        csv("offset_trailing_x_is_text", "2020-01-02T03:04:05+0900x\n"),
        csv("time_beside_date_is_text", "10:30:00\n2020-01-02\n"),
        Case("no_try_parse", {}, lambda: pl.read_csv(io.StringIO("x\n2020-01-02\n")), params={"csv": "x\n2020-01-02\n"},
             note="Polars' default: no date inferred"),
        csv("null_value_na", "NA\n2020-01-02\n", null_values=["NA"]),
        Case("override_string_beside_a_datetime", {},
             lambda: pl.read_csv(io.StringIO("x,y\n2020-01-02,2020-01-02 03:04:05\n"), try_parse_dates=True,
                                 schema_overrides={"x": pl.String}),
             params={"csv": "x,y\n2020-01-02,2020-01-02 03:04:05\n"}),
        given("given_date_day_first", "02/01/2020\n", pl.Date),
        given("given_date_dots", "2020.01.02\n", pl.Date),
        given("given_date_unpadded", "2020-1-2\n", pl.Date),
        given("given_datetime_day_first", "02/01/2020 03:04:05\n", dt_us),
        given("given_datetime_utc_offset", "2020-01-02T03:04:05+0900\n", utc),
        given("given_datetime_ms_of_a_date", "2020-01-02\n", pl.Datetime("ms")),
        given("given_time_no_seconds", "10:30\n", pl.Time),
        given("given_datetime_ns_utc", "2020-01-02T03:04:05.123456789Z\n", pl.Datetime("ns", "UTC"),),
        Case("late_value_every_row_inferred", {}, lambda: pl.read_csv(io.StringIO("x\n" + late), try_parse_dates=True,
                                                                      infer_schema_length=None).tail(1),
             params={"csv": "x\n" + late}, note="with every row inferred, a value that does not parse makes the column text"),
        # --- generated ---------------------------------------------------------------------
        csv("dates_gen", gen_dates),
        csv("day_first_gen", gen_slash),
        csv("datetimes_gen", gen_dts),
        csv("zoned_gen", gen_zoned),
        csv("times_gen", gen_times),
    ]
