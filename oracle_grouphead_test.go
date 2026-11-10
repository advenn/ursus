package ursus_test

// The grouphead group (step 501, from grouphead_test.go's hand answers): a group's
// head and tail, each spelled as bench/scripts/oracle/grouphead.py asks Polars, with
// maintain_order.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	q := func(f func(lf *ursus.LazyFrame) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return f(in.df()) }
	}
	oracleGroups["grouphead"] = map[string]oracleQuery{
		"head_2":                  q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GroupBy(c("k")).Head(2) }),
		"tail_2":                  q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GroupBy(c("k")).Tail(2) }),
		"head_past_a_groups_rows": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GroupBy(c("k")).Head(10) }),
		"two_keys": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(c("v").Mod(2).Alias("j")).GroupBy(c("k"), c("j")).Head(1)
		}),
		"a_computed_key":         q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GroupBy(c("v").Mod(3)).Head(1) }),
		"a_computed_key_aliased": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GroupBy(c("v").Mod(3).Alias("m")).Tail(1) }),
		"no_rows":                q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GroupBy(c("k")).Head(2) }),
		"head_gen":               q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GroupBy(c("k")).Head(3) }),
		"tail_gen":               q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GroupBy(c("k")).Tail(2) }),
	}
}
