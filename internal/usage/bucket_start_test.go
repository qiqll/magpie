package usage

import (
	"testing"
	"time"
)

// An hour bucket begins at the zone's own hour, a half-hour zone's too,
// and the hour a fall-back repeats is two buckets; a week's begins on
// the Monday's midnight.
func TestBucketStartInZone(t *testing.T) {
	kolkata, _ := time.LoadLocation("Asia/Kolkata")
	at := time.Date(2026, 11, 17, 10, 47, 3, 9, kolkata)
	if got := BucketStart("hour", at); !got.Equal(time.Date(2026, 11, 17, 10, 0, 0, 0, kolkata)) {
		t.Fatalf("Kolkata's hour: %s", got)
	}
	ny, _ := time.LoadLocation("America/New_York")
	second := time.Date(2026, 11, 1, 6, 40, 0, 0, time.UTC).In(ny) // 01:40 EST, the second 01:40
	if got := BucketStart("hour", second); !got.Equal(time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("the repeated hour: %s", got)
	}
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, ny)
	if n := len(BucketStarts("hour", day, day.AddDate(0, 0, 1))); n != 25 {
		t.Fatalf("a fall-back day's hours: %d", n)
	}
	if i := BucketIndex("hour", day, second); i != 2 {
		t.Fatalf("the second 01:40's hour: %d", i)
	}
	if got := BucketStart("week", time.Date(2026, 11, 22, 23, 0, 0, 0, ny)); !got.Equal(time.Date(2026, 11, 16, 0, 0, 0, 0, ny)) {
		t.Fatalf("a Sunday's week: %s", got)
	}
}
