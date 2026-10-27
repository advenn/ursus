package dtype

import (
	"testing"
	"time"
)

// TestAZoneIsLoadedOnce (step 399): a zone that loads is kept, the same Location
// each time, its answers time.LoadLocation's; one that does not is refused each
// time and not kept, so names that are no zones cannot grow the cache.
func TestAZoneIsLoadedOnce(t *testing.T) {
	a, err := Location("Europe/Amsterdam")
	if err != nil {
		t.Skipf("no zone database: %v", err)
	}
	b, _ := Location("Europe/Amsterdam")
	if a != b {
		t.Error("Europe/Amsterdam loaded twice")
	}
	want, _ := time.LoadLocation("Europe/Amsterdam")
	at := time.Date(2024, 7, 1, 12, 0, 0, 0, time.UTC)
	if got, w := at.In(a).Format(time.RFC3339), at.In(want).Format(time.RFC3339); got != w {
		t.Errorf("the kept zone gives %s, LoadLocation's %s", got, w)
	}
	for range 2 {
		if _, err := Location("Nowhere/At_All"); err == nil {
			t.Fatal("a zone that is none loaded")
		}
	}
	if _, kept := zones.Load("Nowhere/At_All"); kept {
		t.Error("a zone that does not load was kept")
	}
}

// TestAZonedInstantFormatsInItsZone: FormatTemporal of an instant in a zone, which
// reads the kept zone, gives its wall clock and offset there.
func TestAZonedInstantFormatsInItsZone(t *testing.T) {
	if _, err := Location("Asia/Tokyo"); err != nil {
		t.Skipf("no zone database: %v", err)
	}
	dt := Datetime(Second, "Asia/Tokyo")
	for range 2 {
		if got := FormatTemporal(dt, 0); got != "1970-01-01T09:00:00+09:00" {
			t.Errorf("the epoch in Tokyo is %s", got)
		}
	}
}
