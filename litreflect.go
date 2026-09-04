package ursus

import (
	"reflect"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// litNodeReflect handles literals whose type is a DEFINED type over a permitted
// underlying type — `type UserID int64`, `type Region string`.
//
// The Literal constraint uses `~`, so those are accepted at the call site, but no
// `case int64:` in litNode will match them. Rather than forbid them (which would
// make the tildes a lie) or enumerate them (which is impossible), they are
// converted here via reflection.
//
// The cost is one reflect call per literal per QUERY BUILD, never per row.
func litNodeReflect(v any) expr.Node {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return &expr.Lit{Value: rv.Bool(), DT: dtype.Bool}

	case reflect.Int, reflect.Int64:
		return &expr.Lit{Value: rv.Int(), DT: dtype.Int64}
	case reflect.Int8:
		return &expr.Lit{Value: int8(rv.Int()), DT: dtype.Int8}
	case reflect.Int16:
		return &expr.Lit{Value: int16(rv.Int()), DT: dtype.Int16}
	case reflect.Int32:
		return &expr.Lit{Value: int32(rv.Int()), DT: dtype.Int32}

	case reflect.Uint, reflect.Uint64:
		return &expr.Lit{Value: rv.Uint(), DT: dtype.Uint64}
	case reflect.Uint8:
		return &expr.Lit{Value: uint8(rv.Uint()), DT: dtype.Uint8}
	case reflect.Uint16:
		return &expr.Lit{Value: uint16(rv.Uint()), DT: dtype.Uint16}
	case reflect.Uint32:
		return &expr.Lit{Value: uint32(rv.Uint()), DT: dtype.Uint32}

	case reflect.Float32:
		return &expr.Lit{Value: float32(rv.Float()), DT: dtype.Float32}
	case reflect.Float64:
		return &expr.Lit{Value: rv.Float(), DT: dtype.Float64}

	case reflect.String:
		return &expr.Lit{Value: rv.String(), DT: dtype.String}

	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return &expr.Lit{Value: rv.Bytes(), DT: dtype.Binary}
		}
	}

	return &expr.Err{E: uerr.New(uerr.KindValue, "lit",
		"cannot build a literal from %T", v)}
}
