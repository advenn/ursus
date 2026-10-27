package dtype

import (
	"sync"
	"time"
)

// Location is time.LoadLocation, each zone kept once it loads: LoadLocation reads
// and parses the zone's file every time it is called, and ursus called it for every
// instant it formatted and every batch a kernel read in a zone (step 399, B5 of
// v0.8-scope.md). A million instants in Europe/Amsterdam cast to text took 7.7 s on
// one thread, 7.7 µs each.
//
// Only a zone that loads is kept, so a service passing whatever names its requests
// carry cannot grow the cache past the zones there are; one that does not load is
// looked for again, and refused again.
func Location(tz string) (*time.Location, error) {
	if loc, ok := zones.Load(tz); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, err
	}
	zones.Store(tz, loc)
	return loc, nil
}

var zones sync.Map // zone name -> *time.Location
