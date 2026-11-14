"""The enum_byhand group: an Enum built and used (enum_byhand_test.go, v0.3-scope.md
§2.3): a string with a fixed, ordered vocabulary, ordered as its categories are
declared, met at the String for equality. What Polars 2 refuses and ursus answers
(cum_max, a cast to Int64, .str) stays in the Go file as a differential.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
ET = pl.Enum(["lo", "mid", "hi"])
OTHER = pl.Enum(["hi", "lo", "mid"])

EF = pl.DataFrame({"e": pl.Series("e", ["hi", "lo", "mid", None], dtype=ET), "n": pl.Series("n", [1, 2, 3, 4], dtype=pl.Int64)})
EFS = EF.with_columns(pl.Series("s", ["hi", "x", "mid", "lo"]))
TEXT = pl.DataFrame({"s": ["lo", "hi"]})
UNKNOWN = pl.DataFrame({"s": ["lo", "zzz"]})
DUP = pl.DataFrame({"e": pl.Series("e", ["lo", "lo", "hi"], dtype=ET)})
X = pl.DataFrame({"e": ["x"]})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    g = Gen(4999)
    n = 150
    words = g.strings(n, alphabet="ab", maxlen=2, edges=False, name="e")
    cats = pl.Enum(["b", "a", "bb", "ab", "ba", "aa"])
    gen = g.frame(words.cast(cats), g.ints(n, pl.Int64, lo=0, hi=3, null_rate=0, edges=False, name="k"),
                  g.strings(n, alphabet="ab", maxlen=2, edges=False, name="s"))
    return [
        case("sort", EF, lambda df: df.filter(c("e").is_not_null()).sort("e"), note="category order, not text order"),
        case("min_max", EF, sel(c("e").min().alias("mn"), c("e").max().alias("mx"))),
        case("group_by", EF, lambda df: df.filter(c("e").is_not_null()).group_by("e").agg(c("n").sum().cast(pl.Int64)).sort("e"),
             note="the sum cast to the Int64 both agree on: an integer Sum is an Int128 here"),
        case("unique", DUP, lambda df: df.unique(), order="set"),
        case("concat", {"top": EF, "bottom": EF}, lambda top, bottom: pl.concat([top, bottom])),
        case("shift", EF, sel(c("e").shift(1))),
        case("string_to_enum", TEXT, sel(c("s").cast(ET))),
        case("string_to_enum_lossy", UNKNOWN, sel(c("s").cast(ET, strict=False)), note="CastLossy nulls an unknown value"),
        case("enum_to_string", EF, sel(c("e").cast(pl.String))),
        case("enum_to_enum", EF, sel(c("e").cast(OTHER))),
        case("eq_mid", EF, sel(c("e") == "mid")),
        case("eq_zzz", EF, sel(c("e") == "zzz"), note="a value outside the categories equals nothing"),
        case("lt_mid", EF, sel(c("e") < "mid"), note="category order"),
        case("eq_string_column", EFS, sel(c("e") == c("s")), note="met at the String, by value"),
        case("is_in", EF, sel(c("e").is_in(["lo", "zzz"]))),
        case("fill_null_lo", EF, sel(c("e").fill_null("lo")), note="a category fills and the Enum stays"),
        case("fill_null_zzz", EF, sel(c("e").cast(pl.String).fill_null("zzz")),
             note="spelled to ursus's documented rule (step 507): a fill outside the Enum's categories meets the Enum at the "
                  "String, a String column holding zzz (FillNullWith's doc); Polars 2.1 keeps the null and the Enum, so it is "
                  "asked with the Enum cast to String first"),
        case("concat_with_string", {"top": EF.select("e"), "bottom": X}, lambda top, bottom: pl.concat([top, bottom], how="vertical_relaxed"),
             note="Polars' vertical refuses an Enum beside a String; its relaxed mode meets them at String, as Concat does here"),
        # --- generated -----------------------------------------------------------------
        case("sort_gen", gen, lambda df: df.sort("e", "k", nulls_last=True)),
        case("compare_gen", gen, sel((c("e") == "ab").alias("eq"), (c("e") < "ab").alias("lt"), (c("e") >= "ba").alias("ge"),
                                     (c("e") == c("s")).alias("eqs"), c("e").is_in(["a", "bb"]).alias("isin"))),
        case("group_gen", gen, lambda df: df.group_by("k").agg(c("e").min().alias("mn"), c("e").max().alias("mx"),
                                                               c("e").first().alias("first"), c("e").last().alias("last")).sort("k")),
        case("casts_gen", gen, sel(c("e").cast(pl.String).alias("txt"), c("e").fill_null("a").alias("filled"), c("e").shift(-1).alias("sh"),
                                   c("s").cast(cats, strict=False).alias("back"))),
    ]
