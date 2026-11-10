package ursus_test

// The framerows group (step 501, from framerows_test.go's hand answers): a frame's
// shift, gather_every, clear, first and last, and equals as a frame of one Boolean,
// each spelled as bench/scripts/oracle/framerows.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	q := func(f func(lf *ursus.LazyFrame) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return f(in.df()) }
	}
	// equals compares the input a, through both where given, with the input b or with
	// b(a), and answers the Boolean as a frame.
	equals := func(nullEqual bool, b func(a *ursus.LazyFrame) *ursus.LazyFrame, both func(a *ursus.LazyFrame) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			a := in.frame("a")
			var other *ursus.LazyFrame
			if b != nil {
				other = b(a)
			} else {
				other = in.frame("b")
			}
			if both != nil {
				a = both(a)
			}
			got, err := collect(in.t, a).Equals(in.t.Context(), collect(in.t, other), nullEqual)
			if err != nil {
				in.t.Fatal(err)
			}
			return ursus.Frame(ursus.Values("equals", []bool{got}))
		}
	}
	self := func(a *ursus.LazyFrame) *ursus.LazyFrame { return a }
	nested := func(a *ursus.LazyFrame) *ursus.LazyFrame {
		return a.WithColumns(ursus.ConcatList(c("x"), c("x")).Alias("l"), ursus.Struct(c("x")).Alias("st"))
	}
	oracleGroups["framerows"] = map[string]oracleQuery{
		"shift_2":          q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Shift(2) }),
		"shift_minus_1":    q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Shift(-1) }),
		"shift_0":          q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Shift(0) }),
		"shift_9":          q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Shift(9) }),
		"shift_2_fill_0":   q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.ShiftFill(2, 0) }),
		"shift_1_fill_1_5": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.ShiftFill(1, 1.5) }),
		"every_2":          q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GatherEvery(2, 0) }),
		"every_2_from_1":   q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GatherEvery(2, 1) }),
		"every_3_from_9":   q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GatherEvery(3, 9) }),
		"clear_0":          q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Clear(0) }),
		"clear_2":          q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Clear(2) }),
		"first":            q((*ursus.LazyFrame).First),
		"last":             q((*ursus.LazyFrame).Last),
		"first_of_none":    q((*ursus.LazyFrame).First),

		"equals_nan_and_zeros":                   equals(true, nil, nil),
		"equals_itself":                          equals(true, self, nil),
		"equals_nulls_unequal":                   equals(false, nil, nil),
		"equals_itself_nulls_unequal":            equals(false, self, nil),
		"equals_another_name":                    equals(true, func(a *ursus.LazyFrame) *ursus.LazyFrame { return a.Rename(map[string]string{"a": "b"}) }, nil),
		"equals_int32_and_int64":                 equals(true, func(a *ursus.LazyFrame) *ursus.LazyFrame { return a.WithColumns(c("a").Cast(ursus.Int32)) }, nil),
		"equals_int64_and_float64":               equals(true, func(a *ursus.LazyFrame) *ursus.LazyFrame { return a.WithColumns(c("a").Cast(ursus.Float64)) }, nil),
		"equals_text_and_a_number":               equals(true, func(a *ursus.LazyFrame) *ursus.LazyFrame { return a.WithColumns(c("a").Cast(ursus.String)) }, nil),
		"equals_another_order":                   equals(true, func(a *ursus.LazyFrame) *ursus.LazyFrame { return a.Select(c("s"), c("a")) }, nil),
		"equals_fewer_rows":                      equals(true, func(a *ursus.LazyFrame) *ursus.LazyFrame { return a.Head(1) }, nil),
		"equals_another_value":                   equals(true, func(a *ursus.LazyFrame) *ursus.LazyFrame { return a.WithColumns(c("a").Add(int64(1))) }, nil),
		"equals_lists_and_structs":               equals(true, nested, nested),
		"equals_lists_and_structs_nulls_unequal": equals(false, nested, nested),
		"equals_none_of_none":                    equals(true, self, nil),

		"shift_gen":      q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Shift(3) }),
		"shift_gen_fill": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.ShiftFill(-2, 0.5) }),
		"every_gen":      q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.GatherEvery(3, 1) }),
	}
}
