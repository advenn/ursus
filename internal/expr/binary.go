package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// The binary namespace (step 274, B of v0.6.6-scope.md): Polars' .bin.

// binReceiver refuses a receiver that is not Binary, as Polars refuses one: a
// String's bytes are its text's, which Str() reads.
func binReceiver(fn CallFn, in dtype.DataType) error {
	if in.ID() == dtype.TypeBinary || in.IsNull() {
		return nil
	}
	e := uerr.New(uerr.KindType, "bin", "%s requires a Binary operand, got %s", fn, in)
	if in.ID() == dtype.TypeString {
		e.Hint("cast the text to its bytes first, .Cast(ursus.Binary)")
	}
	return e
}

func binCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	if err := binReceiver(fn, in); err != nil {
		return dtype.Null, err
	}
	switch fn {
	case FnBinEncode:
		return dtype.String, nil
	case FnBinDecode:
		return dtype.Binary, nil
	case FnBinSize:
		args, err := CallArgs(c)
		if err != nil {
			return dtype.Null, err
		}
		switch per := callLitInt(args, 0); {
		case len(args) != 1 || per < 1:
			return dtype.Null, uerr.Internalf("expr: %s in %d bytes", fn, per)
		case per == 1:
			return dtype.Uint32, nil
		}
		return dtype.Float64, nil
	case FnBinReinterpret:
		l, ok := c.Args[len(c.Args)-1].(*Lit)
		if len(c.Args) != 3 || !ok {
			return dtype.Null, uerr.Internalf("expr: %s without its type", fn)
		}
		if w := ReinterpretWidth(l.DT); w == 0 {
			return dtype.Null, uerr.New(uerr.KindType, "bin.reinterpret",
				"reinterpret reads bytes as a number or a temporal value, not %s", l.DT)
		}
		return l.DT, nil
	}
	return dtype.Null, uerr.Internalf("expr: unknown binary call %d", fn)
}

// ReinterpretWidth is the bytes of dt reinterpret reads, 0 for a type it does not:
// an integer, a float, a Date, a Datetime, a Duration or a Time, as Polars reads them.
func ReinterpretWidth(dt dtype.DataType) int {
	switch {
	case dt.IsInteger(), dt.IsFloat(), dt.ID() == dtype.TypeDate, dt.ID() == dtype.TypeDatetime,
		dt.ID() == dtype.TypeDuration, dt.ID() == dtype.TypeTime:
		return dt.BitWidth() / 8
	}
	return 0
}

// binHorizontalOut types the binary functions of a row's operands.
func binHorizontalOut(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	want := map[CallFn]int{FnBinContains: 2, FnBinStartsWith: 2, FnBinEndsWith: 2, FnBinGet: 3, FnBinSlice: 4}[fn]
	if len(ops) != want {
		return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
	}
	if err := binReceiver(fn, ops[0].Type); err != nil {
		return dtype.Null, err
	}
	switch fn {
	case FnBinContains, FnBinStartsWith, FnBinEndsWith:
		// Polars takes the pattern as bytes or as text.
		if p := ops[1].Type; p.ID() != dtype.TypeBinary && p.ID() != dtype.TypeString && !p.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "bin", "%s takes its pattern as bytes, got %s", fn, p)
		}
		return dtype.Bool, nil
	case FnBinGet:
		if i := ops[1].Type; !i.IsInteger() && !i.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "bin.get", "bin.get takes an integer index, got %s", i)
		}
		if ops[2].Type != dtype.Bool {
			return dtype.Null, uerr.Internalf("expr: %s with null_on_oob a %s", fn, ops[2].Type)
		}
		return dtype.Uint8, nil
	}
	for _, f := range ops[1:3] {
		if !f.Type.IsInteger() && !f.Type.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "bin.slice", "bin.slice takes integers, got %s", f.Type)
		}
	}
	if ops[3].Type.ID() != dtype.TypeString {
		return dtype.Null, uerr.Internalf("expr: %s without its mode", fn)
	}
	return dtype.Binary, nil
}
