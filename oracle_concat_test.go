package ursus_test

// The concat group (step 501, from concat_test.go's hand answers): Concat's order, its
// diagonal and Polars' relaxed, horizontal and align modes, each spelled as
// bench/scripts/oracle/concat.py asks Polars.

import (
	"slices"

	"github.com/advenn/ursus"
)

func init() {
	// cat concatenates the case's inputs in the order of their names (a, b or f0,
	// f1, f2), as the Python side does.
	cat := func(mode ursus.ConcatMode) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			names := make([]string, 0, len(in.frames))
			for name := range in.frames {
				names = append(names, name)
			}
			slices.Sort(names)
			frames := make([]*ursus.LazyFrame, len(names))
			for i, name := range names {
				frames[i] = in.frame(name)
			}
			return ursus.Concat(frames, ursus.WithConcatMode(mode))
		}
	}
	oracleGroups["concat"] = map[string]oracleQuery{
		"vertical_twice": func(in oracleInputs) *ursus.LazyFrame {
			a, b := in.frame("a"), in.frame("b")
			return ursus.Concat([]*ursus.LazyFrame{a, b, a})
		},
		"vertical_int32_int64":      cat(ursus.ConcatStrict),
		"diagonal":                  cat(ursus.ConcatDiagonal),
		"relaxed_int_float":         cat(ursus.ConcatRelaxed),
		"relaxed_number_text":       cat(ursus.ConcatRelaxed),
		"relaxed_bool_number":       cat(ursus.ConcatRelaxed),
		"relaxed_date_datetime":     cat(ursus.ConcatRelaxed),
		"relaxed_int32_int64":       cat(ursus.ConcatRelaxed),
		"diagonal_relaxed":          cat(ursus.ConcatDiagonalRelaxed),
		"horizontal_extend":         cat(ursus.ConcatHorizontalExtend),
		"horizontal_extend_no_rows": cat(ursus.ConcatHorizontalExtend),
		"horizontal":                cat(ursus.ConcatHorizontal),
		"align":                     cat(ursus.ConcatAlign),
		"align_inner":               cat(ursus.ConcatAlignInner),
		"align_left":                cat(ursus.ConcatAlignLeft),
		"align_right":               cat(ursus.ConcatAlignRight),
		"align_two_columns":         cat(ursus.ConcatAlign),
		"align_null_keys":           cat(ursus.ConcatAlign),
		"align_key_twice":           cat(ursus.ConcatAlign),
		"align_widths":              cat(ursus.ConcatAlign),
		"align_one_frame":           cat(ursus.ConcatAlign),
		"vertical_gen":              cat(ursus.ConcatStrict),
		"diagonal_gen":              cat(ursus.ConcatDiagonal),
		"align_gen":                 cat(ursus.ConcatAlign),
		"align_left_gen":            cat(ursus.ConcatAlignLeft),
	}
}
