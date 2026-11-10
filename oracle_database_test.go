package ursus_test

// The database group (step 502, from database_test.go's bare-driver table): a column's
// type is its values', met as Polars meets them; each case's values, the param values,
// are fed through the fake database/sql driver as ReadDatabase's result, under the
// param options, as bench/scripts/oracle/database.py gave them to Polars through
// SQLite or a DB-API connection of rows.

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/advenn/ursus"
)

func init() {
	// values is the param name, a list of JSON values, as driver values: an integer,
	// past an Int64 a uint64; a float, a string, a Boolean, a null; {"b64": ...} bytes;
	// {"datetime": ...} a time.Time, in UTC where it is naive, else in a fixed zone of
	// its offset.
	values := func(in oracleInputs, name string) []driver.Value {
		list, ok := in.params[name].([]any)
		if !ok {
			in.t.Fatalf("the case has no list param %q", name)
		}
		out := make([]driver.Value, len(list))
		for i, v := range list {
			switch x := v.(type) {
			case nil:
			case string:
				out[i] = x
			case bool:
				out[i] = x
			case json.Number:
				if strings.ContainsAny(x.String(), ".eE") {
					out[i] = oracleFloat(in.t, x)
				} else if n, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
					out[i] = n
				} else {
					out[i] = oracleUint(in.t, x)
				}
			case map[string]any:
				if b, ok := x["b64"]; ok {
					out[i] = oracleBytes(in.t, b)
					continue
				}
				out[i] = oracleInstant(in, oracleString(in.t, x["datetime"]))
			default:
				in.t.Fatalf("the param %s holds %v (%T)", name, v, v)
			}
		}
		return out
	}
	// answer is ReadDatabase's frame as the registry's, or its refusal as the query's.
	answer := func(in oracleInputs, df *ursus.DataFrame, err error) *ursus.LazyFrame {
		if err != nil {
			*in.failed = err
			return nil
		}
		return df.Lazy()
	}
	var n int
	// result registers the case's values as a bare result of one column, x.
	result := func(in oracleInputs) (*sql.DB, string) {
		n++
		return fakeDB(in.t), fake(fmt.Sprintf("oracle database %d", n), column(fakeCol{}, values(in, "values")...))
	}
	// read is ReadDatabase of the result, or, under options, ReadDatabaseRows of it.
	read := func(in oracleInputs) *ursus.LazyFrame {
		db, q := result(in)
		opts := in.databaseOptions()
		if len(opts) == 0 {
			df, err := ursus.ReadDatabase(in.t.Context(), db, q)
			return answer(in, df, err)
		}
		rows, err := db.QueryContext(in.t.Context(), q)
		if err != nil {
			in.t.Fatal(err)
		}
		df, err := ursus.ReadDatabaseRows(rows, opts...)
		return answer(in, df, err)
	}
	reg := map[string]oracleQuery{
		"arguments": func(in oracleInputs) *ursus.LazyFrame {
			vals := values(in, "args")
			args := make([]any, len(vals))
			for i, v := range vals {
				args[i] = v
			}
			df, err := ursus.ReadDatabase(in.t.Context(), fakeDB(in.t), "args", args...)
			return answer(in, df, err)
		},
		"a_name_twice_refused": oracleRefusedAs(ursus.ErrSchema, func(in oracleInputs) *ursus.LazyFrame {
			q := fake("oracle database a name twice", fakeResult{cols: []fakeCol{{name: "a"}, {name: "a"}}})
			df, err := ursus.ReadDatabase(in.t.Context(), fakeDB(in.t), q)
			return answer(in, df, err)
		}),
		"bytes_and_an_integer_refused":      oracleRefusedAs(ursus.ErrType, read),
		"bytes_text_and_an_integer_refused": oracleRefusedAs(ursus.ErrType, read),
		"override_past_int32_refused":       oracleRefusedAs(ursus.ErrValue, read),
		"an_instant_and_a_boolean_refused":  oracleRefusedAs(ursus.ErrType, read),
	}
	for _, name := range []string{
		"integers", "floats", "text", "bytes", "an_integer_and_a_float", "a_large_integer_and_a_float", "an_integer_and_text",
		"a_float_and_text", "bytes_and_text", "no_value", "a_null_then_an_integer", "a_float_after_150_integers",
		"override_int32", "override_text_of_an_integer_and_text", "override_decimal_of_a_float", "integers_gen", "floats_gen",
		"text_gen", "bytes_gen", "integers_and_floats_gen", "integers_text_and_nulls_gen", "booleans", "a_boolean_and_an_integer",
		"a_boolean_and_text", "an_instant", "an_instant_with_an_offset", "an_int64_and_past_one", "booleans_gen", "instants_gen",
		"instants_with_an_offset_gen", "integers_past_int64_gen",
	} {
		reg[name] = read
	}
	oracleGroups["database"] = reg
}

// oracleInstant is an instant's ISO text as a Go driver gives it: naive, a time.Time
// in UTC; with an offset, in a fixed zone of the offset.
func oracleInstant(in oracleInputs, s string) time.Time {
	if v, err := time.Parse("2006-01-02T15:04:05.999999999", s); err == nil {
		return v
	}
	v, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", s)
	if err != nil {
		in.t.Fatalf("%q is not an instant: %v", s, err)
	}
	_, offset := v.Zone()
	return v.In(time.FixedZone("", offset))
}
