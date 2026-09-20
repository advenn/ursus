package dtype

import "time"

// TimeUnit is the resolution of a Time, Datetime or Duration.
type TimeUnit uint8

const (
	Second TimeUnit = iota
	Milli
	Micro
	Nano
)

func (u TimeUnit) String() string {
	switch u {
	case Second:
		return "s"
	case Milli:
		return "ms"
	case Micro:
		return "us"
	case Nano:
		return "ns"
	default:
		return "?"
	}
}

// Duration returns the length of one tick.
func (u TimeUnit) Duration() time.Duration {
	switch u {
	case Second:
		return time.Second
	case Milli:
		return time.Millisecond
	case Micro:
		return time.Microsecond
	default:
		return time.Nanosecond
	}
}

// Finer reports whether u has strictly higher resolution than v. Used by type
// promotion: combining two temporal columns keeps the finer unit, so no
// precision is silently discarded.
func (u TimeUnit) Finer(v TimeUnit) bool { return u > v }

// NanosPerTick returns the length of one stored tick in nanoseconds, and whether
// d is a temporal type with a defined tick length.
//
// This is the conversion factor every temporal cast, parse and format needs, in
// one place. Date is included at whole days: it stores days since the epoch, so
// its tick is 86400 seconds even though it carries no TimeUnit of its own.
//
// The widest value is Date's 8.64e13, and multiplying a tick count by it is how a
// caller loses. That product leaves int64 at 106_751 ticks — 292 years for a Date,
// and the same 292 years for every Datetime unit, because the product is a
// nanosecond count either way. The as-of join scaled its keys up like that and
// matched two dates three centuries apart against a one-hour tolerance; the fix was
// to scale the TOLERANCE down into ticks instead, where nothing can overflow. This
// comment used to claim the product was safe within ~10^5 years, which is the false
// premise that made it look fine.
func (d DataType) NanosPerTick() (int64, bool) {
	switch d.ID() {
	case TypeDate:
		return int64(24 * time.Hour), true
	case TypeTime, TypeDatetime, TypeDuration:
		return int64(d.TimeUnit().Duration()), true
	default:
		return 0, false
	}
}
