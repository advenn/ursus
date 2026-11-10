"""The rollingoffset group: a rolling group's Offset, where each row's window starts,
(t + offset, t + offset + period] (rollingoffset_test.go, step 313). Each window is
its rows' values as a list.

Polars 2.1 ends a window at its start plus the period, so an offset of -28d or -4w
before a month's period runs past the row (Feb 28 - 28d + 1mo is Feb 29), where Polars
1.44.1 ended it at the row, and ursus does: those cases are findings, skipped.
"""

from __future__ import annotations

from datetime import date

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col

DAYS = pl.DataFrame({"t": [date(2024, 1, 31), date(2024, 2, 28), date(2024, 2, 29), date(2024, 3, 1), date(2024, 3, 29), date(2024, 3, 30), date(2024, 3, 31)],
                     "v": pl.Series([1, 2, 3, 4, 5, 6, 7], dtype=pl.Int64)})
INTS = pl.DataFrame({"t": pl.Series([1, 3, 5, 6], dtype=pl.Int64), "v": pl.Series([1, 2, 3, 4], dtype=pl.Int64)})


def roll(period: str, offset: str | None = None, closed: str = "right"):
    return lambda df: df.rolling("t", period=period, offset=offset, closed=closed).agg(c("v"))


def cases() -> list[Case]:
    gen = Gen(5014)
    n = 120
    ts = [int(k) for k in gen.rng.integers(1, 4, n).cumsum()]
    gen_ints = pl.DataFrame({"t": pl.Series(ts, dtype=pl.Int64), "v": gen.ints(n, pl.Int64, lo=-50, hi=50, name="v")})
    return [
        case("1mo", DAYS, roll("1mo")),
        case("1mo_offset_m1mo", DAYS, roll("1mo", "-1mo"), note="minus the period, given: the default"),
        case("1mo_no_offset", DAYS, roll("1mo", "0d")),
        case("1mo_offset_m2d", DAYS, roll("1mo", "-2d")),
        case("1mo_offset_m28d", DAYS, roll("1mo", "-28d"), note="Polars 2.1 ends Feb 28's window at Jan 31 + 1mo, Feb 29; at the row here: a finding"),
        case("1mo_offset_m4w", DAYS, roll("1mo", "-4w"), note="as -28d: a finding"),
        case("1mo_offset_m40d", DAYS, roll("1mo", "-40d"), note="wholly behind"),
        case("3d_offset_m1d", DAYS, roll("3d", "-1d")),
        case("3d_offset_1d_closed_left", DAYS, roll("3d", "1d", "left")),
        case("1mo_closed_both", DAYS, roll("1mo", None, "both")),
        case("2i", INTS, roll("2i")),
        case("2i_no_offset", INTS, roll("2i", "0i")),
        case("2i_offset_m3i", INTS, roll("2i", "-3i")),
        case("2i_offset_1i", INTS, roll("2i", "1i")),
        case("2i_offset_m2i", INTS, roll("2i", "-2i")),
        case("2i_offset_m1i", INTS, roll("2i", "-1i")),
        # --- generated -----------------------------------------------------------------------------
        case("5i_gen", gen_ints, roll("5i")),
        case("5i_no_offset_gen", gen_ints, roll("5i", "0i")),
        case("5i_offset_m8i_gen", gen_ints, roll("5i", "-8i")),
        case("5i_offset_2i_closed_left_gen", gen_ints, roll("5i", "2i", "left")),
        case("4i_closed_both_gen", gen_ints, roll("4i", None, "both")),
        case("4i_closed_none_gen", gen_ints, roll("4i", None, "none")),
    ]
