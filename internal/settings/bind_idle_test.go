package settings

import (
	"testing"
	"time"
)

// A bound user's idle time is the user's, held to 1 to 720 hours; unset,
// or 0 or less as a file edited by hand may hold, it is the 60 hours of
// before, never an hour (T-242).
func TestBindIdle(t *testing.T) {
	for _, c := range []struct {
		hours *int
		want  time.Duration
	}{
		{nil, 60 * time.Hour},
		{new(0), 60 * time.Hour},
		{new(-5), 60 * time.Hour},
		{new(1), time.Hour},
		{new(12), 12 * time.Hour},
		{new(721), 720 * time.Hour},
	} {
		if got := (Settings{BindIdleHours: c.hours}).BindIdle(); got != c.want {
			t.Errorf("%v: %v, want %v", c.hours, got, c.want)
		}
	}
}
