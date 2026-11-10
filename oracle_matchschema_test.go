package ursus_test

// The matchschema group (step 501, from matchschema_test.go's hand answers): Polars'
// match_to_schema, each spelled as bench/scripts/oracle/matchschema.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c, of := ursus.Col, ursus.Of
	mts := func(o ursus.MatchOptions, fields ...ursus.Field) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().MatchToSchema(ursus.MustSchema(fields...), o) }
	}
	abc := []ursus.Field{of("a", ursus.String), of("b", ursus.Int64), of("c", ursus.Float64)}
	upcast := ursus.MatchOptions{IntegerCast: ursus.MatchUpcast, FloatCast: ursus.MatchUpcast}
	f := func(n string, d ursus.DataType) ursus.Field { return ursus.Field{Name: n, Type: d, Nullable: true} }
	st := func(fs ...ursus.Field) ursus.DataType { return ursus.StructOf(fs...) }
	oracleGroups["matchschema"] = map[string]oracleQuery{
		"insert_ignore_upcast": mts(ursus.MatchOptions{MissingColumns: ursus.MatchInsert, ExtraColumns: ursus.MatchIgnore,
			IntegerCast: ursus.MatchUpcast}, abc...),
		"a_fill": mts(ursus.MatchOptions{ExtraColumns: ursus.MatchIgnore, IntegerCast: ursus.MatchUpcast,
			Columns: map[string]ursus.ColumnMatch{"c": {Fill: c("e").Cast(ursus.Float64).Mul(2.0)}}}, abc...),
		"reordered":      mts(ursus.MatchOptions{}, of("a", ursus.String), of("e", ursus.Float32), of("b", ursus.Int32)),
		"a_float_upcast": mts(ursus.MatchOptions{FloatCast: ursus.MatchUpcast}, of("b", ursus.Int32), of("a", ursus.String), of("e", ursus.Float64)),
		"an_upcast_for_one_column": mts(ursus.MatchOptions{Columns: map[string]ursus.ColumnMatch{"b": {IntegerCast: ursus.MatchUpcast}}},
			of("b", ursus.Int64), of("a", ursus.String), of("e", ursus.Float32)),
		"insert_for_one_column": mts(ursus.MatchOptions{ExtraColumns: ursus.MatchIgnore, IntegerCast: ursus.MatchUpcast,
			Columns: map[string]ursus.ColumnMatch{"c": {Missing: ursus.MatchInsert}}}, abc...),
		"uint8_as_int16":   mts(upcast, of("u", ursus.Int16)),
		"uint32_as_int64":  mts(upcast, of("u", ursus.Int64)),
		"int64_as_int128":  mts(upcast, of("u", ursus.Int128)),
		"a_null_column":    mts(ursus.MatchOptions{}, of("n", ursus.Int64)),
		"a_lists_element":  mts(ursus.MatchOptions{IntegerCast: ursus.MatchUpcast}, of("l", ursus.List(ursus.Int64))),
		"struct_same":      mts(ursus.MatchOptions{}, of("s", st(f("x", ursus.Int32), f("y", ursus.String)))),
		"struct_reordered": mts(ursus.MatchOptions{}, of("s", st(f("y", ursus.String), f("x", ursus.Int32)))),
		"struct_field_inserted": mts(ursus.MatchOptions{MissingStructFields: ursus.MatchInsert},
			of("s", st(f("x", ursus.Int32), f("y", ursus.String), f("z", ursus.Float64)))),
		"struct_field_dropped": mts(ursus.MatchOptions{ExtraStructFields: ursus.MatchIgnore}, of("s", st(f("x", ursus.Int32)))),
		"struct_field_widened": mts(ursus.MatchOptions{IntegerCast: ursus.MatchUpcast}, of("s", st(f("x", ursus.Int64), f("y", ursus.String)))),
		"match_gen": mts(ursus.MatchOptions{MissingColumns: ursus.MatchInsert, IntegerCast: ursus.MatchUpcast, FloatCast: ursus.MatchUpcast},
			of("a", ursus.String), of("b", ursus.Int64), of("c", ursus.Float64), of("e", ursus.Float64)),
	}
}
