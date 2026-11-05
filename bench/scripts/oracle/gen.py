"""Seeded generators of input columns, a type each, with nulls and the type's edges.

A generated case's inputs are recorded with its answer, so these need only be
deterministic for one seed under the pinned numpy: numpy's default_rng(seed) is PCG64,
whose stream is stable. Each generator takes a null rate and, with edges=True, mixes
in what a kernel gets wrong first: NaN, the infinities, -0, ties at a half, 2^53, the
type's ends, the epoch and its eve, DST instants of a named zone, an empty string.
"""

from __future__ import annotations

import datetime as dt
import decimal
import math
from typing import Any
from zoneinfo import ZoneInfo

import numpy as np
import polars as pl

INT_BOUNDS = {
    pl.Int8: (-(1 << 7), (1 << 7) - 1),
    pl.Int16: (-(1 << 15), (1 << 15) - 1),
    pl.Int32: (-(1 << 31), (1 << 31) - 1),
    pl.Int64: (-(1 << 63), (1 << 63) - 1),
    pl.UInt8: (0, (1 << 8) - 1),
    pl.UInt16: (0, (1 << 16) - 1),
    pl.UInt32: (0, (1 << 32) - 1),
    pl.UInt64: (0, (1 << 64) - 1),
    pl.Int128: (-(1 << 127), (1 << 127) - 1),
}

FLOAT_EDGES = [
    float("nan"), float("inf"), float("-inf"), -0.0, 0.0, 0.5, -0.5, 1.5, 2.5, 2.675, 0.1, 0.29,
    1.0000000000000002, 2.0**53, 2.0**53 + 2, 1e300, 1e-300, 5e-324, 1.5e-310, 1.7976931348623157e308, 123.456,
]

# The epoch's eve and day, the leap days of 2000 and 2024, and the ends of what a
# Date's Int32 reaches within Python's calendar.
DATE_EDGES = [dt.date(1970, 1, 1), dt.date(1969, 12, 31), dt.date(2000, 2, 29), dt.date(2024, 2, 29),
              dt.date(1, 1, 1), dt.date(9999, 12, 31), dt.date(1900, 3, 1), dt.date(2024, 12, 30)]


def days_from_civil(y: int, m: int, d: int) -> int:
    """Days since 1970-01-01 of a proleptic Gregorian date, year 0 and before
    included, where Python's date stops at year 1 (Howard Hinnant's algorithm)."""
    y -= m <= 2
    era = (y if y >= 0 else y - 399) // 400
    yoe = y - era * 400
    doy = (153 * (m + (-3 if m > 2 else 9)) + 2) // 5 + d - 1
    doe = yoe * 365 + yoe // 4 - yoe // 100 + doy
    return era * 146097 + doe - 719468


def dst_instants(zone: str, year: int) -> list[dt.datetime]:
    """The UTC instants, to the hour, at which the zone's offset changes in the year,
    each with the hour before it: the fold's and the gap's edges."""
    z = ZoneInfo(zone)
    out: list[dt.datetime] = []
    t = dt.datetime(year, 1, 1, tzinfo=dt.timezone.utc)
    end = dt.datetime(year + 1, 1, 1, tzinfo=dt.timezone.utc)
    prev = t.astimezone(z).utcoffset()
    while t < end:
        t += dt.timedelta(hours=1)
        off = t.astimezone(z).utcoffset()
        if off != prev:
            out += [t - dt.timedelta(hours=1), t]
            prev = off
    return out


class Gen:
    """A seeded source of columns. Every column has n rows."""

    def __init__(self, seed: int):
        self.rng = np.random.default_rng(seed)

    def _place(self, vals: list[Any], null_rate: float, edges: list[Any]) -> list[Any]:
        """edges each at a random row, then nulls at the rate, over a copy."""
        vals = list(vals)
        n = len(vals)
        if n == 0:
            return vals
        for e in edges:
            vals[int(self.rng.integers(n))] = e
        if null_rate > 0:
            for i in np.flatnonzero(self.rng.random(n) < null_rate):
                vals[i] = None
        return vals

    def floats(self, n: int, lo: float = -1000.0, hi: float = 1000.0, null_rate: float = 0.1,
               edges: bool = True, ties: bool = True, dtype: pl.DataType = pl.Float64, name: str = "x") -> pl.Series:
        vals = list(self.rng.uniform(lo, hi, n))
        if ties:  # whole numbers and halves, where a rounding's tie-break shows
            for i in self.rng.choice(n, size=max(1, n // 8), replace=False):
                vals[i] = float(round(vals[i] * 2) / 2)
        extra = FLOAT_EDGES if edges else []
        if dtype == pl.Float32:
            extra = [e for e in extra if abs(e) < 3.4e38 or math.isinf(e) or math.isnan(e)]
        return pl.Series(name, self._place(vals, null_rate, extra), dtype=dtype)

    def ints(self, n: int, dtype: pl.DataType = pl.Int64, lo: int | None = None, hi: int | None = None,
             null_rate: float = 0.1, edges: bool = True, name: str = "x") -> pl.Series:
        tlo, thi = INT_BOUNDS[dtype]
        lo = tlo if lo is None else lo
        hi = thi if hi is None else hi
        span = hi - lo
        if span < (1 << 64):
            vals = [lo + int(v) for v in self.rng.integers(0, span, n, dtype=np.uint64, endpoint=True)]
        else:  # an Int128's range: two draws of 64 bits
            highs = self.rng.integers(0, span >> 64, n, dtype=np.uint64, endpoint=True)
            lows = self.rng.integers(0, (1 << 64) - 1, n, dtype=np.uint64, endpoint=True)
            vals = [min(hi, lo + (int(a) << 64) + int(b)) for a, b in zip(highs, lows)]
        extra = [e for e in (0, 1, -1, tlo, thi, 1 << 53, (1 << 53) + 1, -(1 << 53)) if lo <= e <= hi] if edges else []
        return pl.Series(name, self._place(vals, null_rate, extra), dtype=dtype)

    def bools(self, n: int, null_rate: float = 0.1, name: str = "b") -> pl.Series:
        vals = [bool(v) for v in self.rng.random(n) < 0.5]
        return pl.Series(name, self._place(vals, null_rate, []), dtype=pl.Boolean)

    def strings(self, n: int, null_rate: float = 0.1, edges: bool = True, alphabet: str = "abcxyz", maxlen: int = 6,
                name: str = "s") -> pl.Series:
        lens = self.rng.integers(1, maxlen + 1, n)
        picks = self.rng.integers(0, len(alphabet), int(lens.sum()))
        vals, at = [], 0
        for k in lens:
            vals.append("".join(alphabet[i] for i in picks[at:at + k]))
            at += k
        extra = ["", " ", "é", "𝔘rsus", "a,b", "\"q\""] if edges else []
        return pl.Series(name, self._place(vals, null_rate, extra), dtype=pl.String)

    def dates(self, n: int, lo: dt.date = dt.date(1950, 1, 1), hi: dt.date = dt.date(2050, 12, 31),
              null_rate: float = 0.1, edges: bool = True, name: str = "d") -> pl.Series:
        span = (hi - lo).days
        vals = [lo + dt.timedelta(days=int(k)) for k in self.rng.integers(0, span + 1, n)]
        return pl.Series(name, self._place(vals, null_rate, DATE_EDGES if edges else []), dtype=pl.Date)

    def datetimes(self, n: int, unit: str = "us", zone: str | None = None, lo: int = 1960, hi: int = 2040,
                  null_rate: float = 0.1, edges: bool = True, name: str = "t") -> pl.Series:
        """Instants with a time of day to the unit; a zoned column holds UTC instants
        and, with edges, the hour before and the hour of each of the zone's 2024
        offset changes."""
        per_day = {"ms": 86_400_000, "us": 86_400_000_000, "ns": 86_400_000_000_000}[unit]
        d0 = days_from_civil(lo, 1, 1)
        d1 = days_from_civil(hi, 12, 31)
        days = self.rng.integers(d0, d1 + 1, n, dtype=np.int64)
        tod = self.rng.integers(0, per_day, n, dtype=np.int64)
        ticks = [int(a) * per_day + int(b) for a, b in zip(days, tod)]
        extra: list[int] = []
        if edges:
            extra += [0, -1, per_day - 1, -per_day]
            if zone:
                tick = per_day // 86_400
                for t in dst_instants(zone, 2024):
                    extra.append(int((t - dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc)).total_seconds()) * tick)
        s = pl.Series(name, self._place(ticks, null_rate, extra), dtype=pl.Int64)
        return s.cast(pl.Datetime(unit)).dt.replace_time_zone("UTC").dt.convert_time_zone(zone) if zone \
            else s.cast(pl.Datetime(unit))

    def durations(self, n: int, unit: str = "us", lo_days: float = -2, hi_days: float = 2, null_rate: float = 0.1,
                  edges: bool = True, name: str = "du") -> pl.Series:
        per_day = {"ms": 86_400_000, "us": 86_400_000_000, "ns": 86_400_000_000_000}[unit]
        vals = [int(v) for v in self.rng.integers(int(lo_days * per_day), int(hi_days * per_day) + 1, n, dtype=np.int64)]
        extra = [0, 1, -1, per_day, -per_day, per_day - 1, 1 - per_day] if edges else []
        return pl.Series(name, self._place(vals, null_rate, extra), dtype=pl.Int64).cast(pl.Duration(unit))

    def times(self, n: int, null_rate: float = 0.1, edges: bool = True, name: str = "tm") -> pl.Series:
        per_day = 86_400_000_000_000
        vals = [int(v) for v in self.rng.integers(0, per_day, n, dtype=np.int64)]
        extra = [0, per_day - 1, 3_723_250_000_000] if edges else []
        return pl.Series(name, self._place(vals, null_rate, extra), dtype=pl.Int64).cast(pl.Time)

    def decimals(self, n: int, precision: int = 12, scale: int = 4, null_rate: float = 0.1, edges: bool = True,
                 name: str = "dec") -> pl.Series:
        top = 10 ** precision - 1
        lim = min(top, (1 << 62) - 1)
        vals = [int(v) for v in self.rng.integers(-lim, lim + 1, n, dtype=np.int64)]
        extra = [0, 1, -1, top, -top] if edges else []
        unscaled = self._place(vals, null_rate, extra)
        return pl.Series(name, [None if v is None else decimal.Decimal(v).scaleb(-scale) for v in unscaled],
                         dtype=pl.Decimal(precision, scale))

    def binary(self, n: int, null_rate: float = 0.1, edges: bool = True, name: str = "bin") -> pl.Series:
        lens = self.rng.integers(0, 5, n)
        vals = [bytes(int(b) for b in self.rng.integers(0, 256, int(k))) for k in lens]
        extra = [b"", b"\x00\xff", b"hello"] if edges else []
        return pl.Series(name, self._place(vals, null_rate, extra), dtype=pl.Binary)

    def lists(self, n: int, elements: pl.Series, maxlen: int = 4, null_rate: float = 0.1, empty_rate: float = 0.1,
              name: str = "l") -> pl.Series:
        """Lists over the elements given (its nulls become null elements), of up to
        maxlen each, some empty, some null."""
        lens = self.rng.integers(1, maxlen + 1, n)
        picks = self.rng.integers(0, len(elements), int(lens.sum()))
        elems = elements.to_list()
        vals: list[Any] = []
        at = 0
        for k in lens:
            vals.append([elems[i] for i in picks[at:at + k]])
            at += k
        for i in np.flatnonzero(self.rng.random(n) < empty_rate):
            vals[i] = []
        return pl.Series(name, self._place(vals, null_rate, []), dtype=pl.List(elements.dtype))

    def frame(self, *cols: pl.Series) -> pl.DataFrame:
        return pl.DataFrame(list(cols))
