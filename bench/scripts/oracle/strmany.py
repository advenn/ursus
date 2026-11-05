"""The strmany group: contains_any, replace_many, extract_many and find_many, their
three ways of matching and ASCII case-blind (strmany_test.go, step 270); extract_groups,
split_exact, splitn and split_inclusive as Structs and Lists; to_titlecase and the
normal forms; then over generated text.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
S = pl.col("s").str

SM = pl.DataFrame({"s": ["the Cat sat", "dog and cat", "", None, "catcat dogs"]})
KV = pl.DataFrame({"s": ["a=1", "b=22", None, "x"]})
PARTS = pl.DataFrame({"s": ["a,b,c", "x", None, "a,,b", ""]})
TITLE = pl.DataFrame({"s": ["hello wORLD", "o'neil-smith x", "ÉCOLE du monde", "3rd place  two", "hello_world foo.bar ABC-def ǆemal  x1y"]})
FORMS = pl.DataFrame({"s": ["ﬁ", "é", "é"]})


def one(v: str) -> pl.DataFrame:
    return pl.DataFrame({"s": [v]})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def norm(f: str) -> pl.Expr:
    return S.normalize(f).str.len_bytes().alias(f)


def cases() -> list[Case]:
    g = Gen(49912)
    n = 150
    text = g.frame(g.strings(n, alphabet="abcAB ", maxlen=9, name="s"))
    csv = g.frame(g.strings(n, alphabet="ab,", maxlen=7, name="s"))
    kv = g.frame(g.strings(n, alphabet="ab=19", maxlen=6, name="s"))
    words = g.frame(g.strings(n, alphabet="aB cé-'ǆ3", maxlen=12, name="s"))
    pats = ["ab", "bc", "a", "AB"]
    return [
        case("contains_any", SM, sel(S.contains_any(["cat", "dog"]))),
        case("contains_any_ascii_ci", SM, sel(S.contains_any(["cat", "DOG"], ascii_case_insensitive=True))),
        case("empty_pattern", SM, sel(S.contains_any([""]))),
        case("only_ascii_is_ci", one("École"), sel(S.contains_any(["éCOLE"], ascii_case_insensitive=True))),
        case("replace_many", SM, sel(S.replace_many(["cat", "dog"], ["C", "D"]))),
        case("one_replacement", SM, sel(S.replace_many(["cat", "dog"], ["X"]))),
        case("replace_many_ascii_ci", SM, sel(S.replace_many(["CAT"], ["C"], ascii_case_insensitive=True))),
        case("ends_together", one("abcd"), sel(S.replace_many(["bc", "abc"], ["1", "2"])),
             note="the match ending first; of two ending together the one starting first"),
        case("ends_first", one("abcd"), sel(S.replace_many(["ab", "a"], ["1", "2"]))),
        case("leftmost_listed_first", one("abcd"), sel(S.replace_many(["ab", "a"], ["1", "2"], leftmost=True))),
        case("empty_pattern_between", one("ab"), sel(S.replace_many(["", "a"], ["-", "A"]))),
        case("extract_many", SM, sel(S.extract_many(["cat", "dog"]))),
        case("overlapping", one("aaaa"), sel(S.extract_many(["aa"], overlapping=True))),
        case("not_overlapping", one("aaaa"), sel(S.extract_many(["aa"]))),
        case("overlapping_by_their_ends", one("abcd"), sel(S.extract_many(["bc", "abc", "b"], overlapping=True))),
        case("leftmost", one("abcd"), sel(S.extract_many(["ab", "bc", "abc"], leftmost=True))),
        case("find_many", SM, sel(S.find_many(["cat", "dog"]))),
        case("find_many_in_bytes", one("éa"), sel(S.find_many(["a"]))),
        # --- as structs ---------------------------------------------------------------
        case("extract_groups", KV, sel(S.extract_groups(r"(?<k>\w)=(?<v>\d+)"))),
        case("numbered_groups", KV, sel(S.extract_groups(r"(\w)=(\d+)"))),
        case("split_exact", PARTS, sel(S.split_exact(",", 1))),
        case("split_exact_inclusive", PARTS, sel(S.split_exact(",", 1, inclusive=True))),
        case("splitn", PARTS, sel(S.splitn(",", 2))),
        case("splitn_of_one", one("a,b"), sel(S.splitn(",", 1))),
        case("splitn_past_the_parts", one("a,b"), sel(S.splitn(",", 3))),
        case("split_inclusive", PARTS, sel(S.split(",", inclusive=True))),
        # --- titlecase and normal forms -----------------------------------------------
        case("titlecase", TITLE, sel(S.to_titlecase()), note="a digraph's titlecase is its own letter, ǅ"),
        case("normal_forms", FORMS, sel(norm("NFKC"), norm("NFD"), norm("NFC"), norm("NFKD"))),
        # --- generated -----------------------------------------------------------------
        case("many_gen", text, sel(S.contains_any(pats).alias("any"), S.contains_any(pats, ascii_case_insensitive=True).alias("any_ci"),
                                   S.replace_many(pats, ["1", "2", "3", "4"]).alias("rep"),
                                   S.replace_many(pats, ["1", "2", "3", "4"], leftmost=True).alias("rep_left"),
                                   S.replace_many(pats, ["-"], ascii_case_insensitive=True).alias("rep_ci"),
                                   S.extract_many(pats).alias("ext"), S.extract_many(pats, overlapping=True).alias("ext_over"),
                                   S.extract_many(pats, leftmost=True).alias("ext_left"), S.find_many(pats).alias("find"),
                                   S.find_many(pats, overlapping=True, ascii_case_insensitive=True).alias("find_over_ci"))),
        case("structs_gen", csv, sel(S.split_exact(",", 2).alias("exact"), S.split_exact(",", 1, inclusive=True).alias("incl"),
                                     S.splitn(",", 2).alias("n2"), S.split(",", inclusive=True).alias("parts"))),
        case("groups_gen", kv, sel(S.extract_groups(r"(?<k>[ab]+)=(?<v>\d+)").alias("named"), S.extract_groups(r"([ab])(\d)").alias("num"))),
        case("titlecase_gen", words, sel(S.to_titlecase().alias("t"), norm("NFC"), norm("NFD"), norm("NFKC"), norm("NFKD"))),
    ]
