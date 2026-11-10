package ursus_test

// The joinorder group (step 501, from joinorder_test.go's hand answers): Polars'
// maintain_order for every kind of join, the left and right value columns alone, each
// spelled as bench/scripts/oracle/joinorder.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	on := ursus.JoinOn(c("k"))
	kinds := map[string]ursus.JoinKind{"inner": ursus.JoinInner, "left": ursus.JoinLeft, "right": ursus.JoinRight, "full": ursus.JoinFull}
	orders := map[string]ursus.JoinOrder{"left": ursus.JoinOrderLeft, "right": ursus.JoinOrderRight,
		"left_right": ursus.JoinOrderLeftRight, "right_left": ursus.JoinOrderRightLeft}
	join := func(kind ursus.JoinKind, order ursus.JoinOrder, opts []ursus.JoinOption, sel ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			all := append([]ursus.JoinOption{on, ursus.JoinHow(kind), ursus.JoinMaintainOrder(order)}, opts...)
			return in.frame("left").Join(in.frame("right"), all...).Select(sel...)
		}
	}
	reg := map[string]oracleQuery{}
	for kn, kind := range kinds {
		for name, order := range orders {
			reg[kn+"_"+name] = join(kind, order, nil, c("l"), c("r"))
		}
		for name, order := range map[string]ursus.JoinOrder{"left": ursus.JoinOrderLeft, "right": ursus.JoinOrderRight} {
			reg["gen_"+kn+"_"+name] = join(kind, order, nil, c("l"), c("r"))
		}
	}
	for kn, kind := range map[string]ursus.JoinKind{"semi": ursus.JoinSemi, "anti": ursus.JoinAnti} {
		for name, order := range orders {
			reg[kn+"_"+name] = join(kind, order, nil, c("l"))
		}
		reg["gen_"+kn] = join(kind, ursus.JoinOrderLeft, nil, c("l"))
	}
	for name, order := range orders {
		reg["right_key_"+name] = join(ursus.JoinRight, order, nil, c("k"), c("l"), c("r"))
		reg["full_coalesced_key_"+name] = join(ursus.JoinFull, order, []ursus.JoinOption{ursus.JoinCoalesce(true)}, c("k"), c("l"), c("r"))
	}
	for name, order := range map[string]ursus.JoinOrder{"left": ursus.JoinOrderLeft, "right": ursus.JoinOrderRight} {
		order := order
		reg["cross_"+name] = func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("left").Join(in.frame("right"), ursus.JoinHow(ursus.JoinCross), ursus.JoinMaintainOrder(order)).Select(c("l"), c("r"))
		}
	}
	for kn, kind := range map[string]ursus.JoinKind{"inner": ursus.JoinInner, "left": ursus.JoinLeft, "full": ursus.JoinFull} {
		reg["negative_zero_key_"+kn] = join(kind, ursus.JoinOrderRight, []ursus.JoinOption{ursus.JoinCoalesce(true)}, c("k"), c("l"), c("r"))
	}
	oracleGroups["joinorder"] = reg
}
