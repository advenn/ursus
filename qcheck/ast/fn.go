package ast

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/advenn/ursus/qcheck/model"
)

// FnOp is a function of the string or the temporal namespace (step 539).
type FnOp uint8

// The functions. Each reads X, its receiver, and constant parameters: S its text
// (a pattern, a value, a separator, an interval, a zone), I its numbers (a slice's
// offset and length, a period's nanoseconds) and B a pattern's literal flag.
const (
	StrContains         FnOp = iota // S[0] the pattern, B literal
	StrStartsWith                   // S[0]
	StrEndsWith                     // S[0]
	StrReplace                      // S[0] the pattern, S[1] the value, B literal
	StrReplaceAll                   // as StrReplace
	StrLenBytes                     //
	StrLenChars                     //
	StrSlice                        // I[0] the offset, I[1] the length
	StrToUpper                      //
	StrToLower                      //
	StrStripPrefix                  // S[0]
	StrStripSuffix                  // S[0]
	StrSplitLen                     // S[0] the separator: Split(by).List().Len()
	StrToInteger                    //
	DtYear                          //
	DtMonth                         //
	DtDay                           //
	DtHour                          //
	DtMinute                        //
	DtSecond                        //
	DtMillisecond                   //
	DtMicrosecond                   //
	DtNanosecond                    //
	DtWeekday                       //
	DtOrdinalDay                    //
	DtTruncate                      // I[0] the period in nanoseconds, a time.Duration
	DtRound                         // as DtTruncate
	DtOffsetBy                      // S[0] the interval, as Every reads it
	DtDate                          //
	DtTime                          //
	DtConvertTimeZone               // S[0] the zone
	DtTotalDays                     //
	DtTotalHours                    //
	DtTotalMinutes                  //
	DtTotalSeconds                  //
	DtTotalMilliseconds             //
	DtTotalMicroseconds             //
	DtTotalNanoseconds              //
	fnCount
)

var fnNames = [...]string{"StrContains", "StrStartsWith", "StrEndsWith", "StrReplace", "StrReplaceAll",
	"StrLenBytes", "StrLenChars", "StrSlice", "StrToUpper", "StrToLower", "StrStripPrefix", "StrStripSuffix",
	"StrSplitLen", "StrToInteger", "DtYear", "DtMonth", "DtDay", "DtHour", "DtMinute", "DtSecond",
	"DtMillisecond", "DtMicrosecond", "DtNanosecond", "DtWeekday", "DtOrdinalDay", "DtTruncate", "DtRound",
	"DtOffsetBy", "DtDate", "DtTime", "DtConvertTimeZone", "DtTotalDays", "DtTotalHours", "DtTotalMinutes",
	"DtTotalSeconds", "DtTotalMilliseconds", "DtTotalMicroseconds", "DtTotalNanoseconds"}

func (o FnOp) String() string {
	if int(o) < len(fnNames) {
		return fnNames[o]
	}
	return fmt.Sprintf("FnOp(%d)", o)
}

// FnOps are every function, for a case file's names.
func FnOps() []FnOp {
	out := make([]FnOp, fnCount)
	for i := range out {
		out[i] = FnOp(i)
	}
	return out
}

// IsStr is a function of the string namespace.
func (o FnOp) IsStr() bool { return o <= StrToInteger }

// IsPart is a component of an instant or a Time: Year to OrdinalDay.
func (o FnOp) IsPart() bool { return o >= DtYear && o <= DtOrdinalDay }

// IsTotal is a Duration's Total family.
func (o FnOp) IsTotal() bool { return o >= DtTotalDays && o <= DtTotalNanoseconds }

// TotalUnitNanos is a Total function's unit in nanoseconds.
func (o FnOp) TotalUnitNanos() int64 {
	return [...]int64{86_400e9, 3_600e9, 60e9, 1e9, 1e6, 1e3, 1}[o-DtTotalDays]
}

// Fn is a function of the string or temporal namespace, X.Str().Op(...) or
// X.Dt().Op(...).
type Fn struct {
	Op FnOp
	X  Expr
	S  []string
	I  []int64
	B  bool
	T  model.Type
}

func (e *Fn) Type() model.Type { return e.T }
func (e *Fn) Kids() []Expr     { return []Expr{e.X} }

// Interval is an offset as Every reads the ones qcheck writes: months, days and
// nanoseconds, from an integer and one unit, "1mo", "-2d", "90m", "1ns".
type Interval struct {
	Months, Days, Nanos int64
}

var intervalUnits = []struct {
	suffix              string
	months, days, nanos int64
}{
	{"mo", 1, 0, 0}, {"ns", 0, 0, 1}, {"us", 0, 0, 1e3}, {"ms", 0, 0, 1e6},
	{"y", 12, 0, 0}, {"w", 0, 7, 0}, {"d", 0, 1, 0}, {"h", 0, 0, 3_600e9}, {"m", 0, 0, 60e9}, {"s", 0, 0, 1e9},
}

// ParseInterval reads an interval of one unit.
func ParseInterval(s string) (Interval, error) {
	for _, u := range intervalUnits {
		num, ok := strings.CutSuffix(s, u.suffix)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			return Interval{}, fmt.Errorf("an interval %q", s)
		}
		return Interval{Months: n * u.months, Days: n * u.days, Nanos: n * u.nanos}, nil
	}
	return Interval{}, fmt.Errorf("an interval %q", s)
}

// NewFn is the typed function node: its receiver's type, its parameters' count,
// and what the type can hold, checked as ursus checks them while a query is planned
// (expr_str.go, expr_dt.go), so a well-typed tree is never refused there.
func NewFn(op FnOp, x Expr, s []string, i []int64, b bool) (Expr, error) {
	xt := x.Type()
	bad := func() (Expr, error) {
		return nil, fmt.Errorf("%s of %s (%q, %v)", op, xt, s, i)
	}
	ns, ni := 0, 0
	var t model.Type
	switch {
	case op.IsStr():
		if xt != model.String {
			return bad()
		}
		switch op {
		case StrContains, StrStartsWith, StrEndsWith:
			ns, t = 1, model.Bool
		case StrReplace, StrReplaceAll:
			ns, t = 2, model.String
		case StrLenBytes, StrLenChars:
			t = model.Uint32
		case StrSlice:
			ni, t = 2, model.String
		case StrToUpper, StrToLower:
			t = model.String
		case StrStripPrefix, StrStripSuffix:
			ns, t = 1, model.String
		case StrSplitLen:
			ns, t = 1, model.Uint32
		case StrToInteger:
			t = model.Int64
		}
	case op.IsPart():
		t = model.Int32
		date := op == DtYear || op == DtMonth || op == DtDay || op == DtWeekday || op == DtOrdinalDay
		switch {
		case xt.IsDatetime():
		case xt == model.Date && date, xt.IsTime() && !date:
		default:
			return bad()
		}
	case op == DtTruncate || op == DtRound:
		ni, t = 1, xt
		if len(i) != 1 || i[0] <= 0 || !xt.IsInstant() || i[0]%tickNanos(xt) != 0 {
			return bad()
		}
	case op == DtOffsetBy:
		ns, t = 1, xt
		if len(s) != 1 || !xt.IsInstant() {
			return bad()
		}
		iv, err := ParseInterval(s[0])
		if err != nil || iv.Nanos%tickNanos(xt) != 0 {
			return bad()
		}
	case op == DtDate:
		if !xt.IsDatetime() {
			return bad()
		}
		t = model.Date
	case op == DtTime:
		if !xt.IsDatetime() {
			return bad()
		}
		t = model.Time(xt.Unit())
	case op == DtConvertTimeZone:
		ns = 1
		if len(s) != 1 || !xt.IsDatetime() || xt.Zone() == "" {
			return bad()
		}
		z := zoneIndex(s[0])
		if z <= 0 {
			return bad()
		}
		t = model.Datetime(xt.Unit(), z)
	case op.IsTotal():
		if !xt.IsDuration() {
			return bad()
		}
		t = model.Int64
	default:
		return bad()
	}
	if len(s) != ns || len(i) != ni {
		return bad()
	}
	return &Fn{Op: op, X: x, S: s, I: i, B: b, T: t}, nil
}

// tickNanos is an instant's tick in nanoseconds: a Date's a day.
func tickNanos(t model.Type) int64 {
	if t == model.Date {
		return 86_400e9
	}
	return t.Unit().Nanos()
}

func zoneIndex(z string) int {
	for i, name := range model.Zones {
		if name == z {
			return i
		}
	}
	return -1
}

func (e *Fn) Go() string {
	x := e.X.Go()
	q := func(i int) string { return strconv.Quote(e.S[i]) }
	switch e.Op {
	case StrContains:
		return fmt.Sprintf("%s.Str().Contains(%s, %v)", x, q(0), e.B)
	case StrStartsWith, StrEndsWith, StrStripPrefix, StrStripSuffix:
		return fmt.Sprintf("%s.Str().%s(%s)", x, strings.TrimPrefix(e.Op.String(), "Str"), q(0))
	case StrReplace, StrReplaceAll:
		return fmt.Sprintf("%s.Str().%s(%s, %s, %v)", x, strings.TrimPrefix(e.Op.String(), "Str"), q(0), q(1), e.B)
	case StrSlice:
		return fmt.Sprintf("%s.Str().Slice(%d, %d)", x, e.I[0], e.I[1])
	case StrSplitLen:
		return fmt.Sprintf("%s.Str().Split(%s).List().Len()", x, q(0))
	case DtTruncate, DtRound:
		return fmt.Sprintf("%s.Dt().%s(time.Duration(%d))", x, strings.TrimPrefix(e.Op.String(), "Dt"), e.I[0])
	case DtOffsetBy:
		return fmt.Sprintf("%s.Dt().OffsetBy(ursus.Every(%s))", x, q(0))
	case DtConvertTimeZone:
		return fmt.Sprintf("%s.Dt().ConvertTimeZone(%s)", x, q(0))
	}
	if e.Op.IsStr() {
		return x + ".Str()." + strings.TrimPrefix(e.Op.String(), "Str") + "()"
	}
	return x + ".Dt()." + strings.TrimPrefix(e.Op.String(), "Dt") + "()"
}
