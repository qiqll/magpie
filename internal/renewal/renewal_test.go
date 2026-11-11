package renewal

import (
	"testing"
	"time"
)

// The period's end and its reminder, on a clock held still: 6 days left
// (and 5 days and an hour, rounded up to 6) asks nothing, 5 days asks for
// a renewal, as does its last hour, and at its end and after it has ended.
func TestOf(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, c := range []struct {
		left time.Duration
		days int
		due  string
	}{
		{30 * day, 30, ""},
		{6 * day, 6, ""},
		{5*day + time.Hour, 6, ""},
		{5 * day, 5, DueSoon},
		{5*day - time.Second, 5, DueSoon},
		{day, 1, DueSoon},
		{time.Hour, 1, DueSoon},
		{0, 0, DueEnded},
		{-day, 0, DueEnded},
		{-40 * day, 0, DueEnded},
	} {
		opened := now.Add(c.left - Days*day)
		s := Of(opened, now)
		if s == nil || s.DaysLeft != c.days || s.Due != c.due || !s.Ends.Equal(now.Add(c.left)) || !s.Opened.Equal(opened) {
			t.Errorf("%v left: %+v, want %d days, due %q", c.left, s, c.days, c.due)
		}
	}
	if s := Of(time.Time{}, now); s != nil {
		t.Errorf("no time: %+v", s)
	}
	// a time in another zone ends as the same instant
	sh := time.FixedZone("UTC+8", 8*3600)
	if s := Of(time.Date(2026, 11, 1, 0, 0, 0, 0, sh), now); !s.Ends.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, sh)) || s.Ends.Location() != time.UTC {
		t.Errorf("in UTC+8: %+v", s)
	}
}

func TestCheckOpened(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	for at, ok := range map[time.Time]bool{
		now:                            true,
		now.AddDate(-3, 0, 0):          true,
		now.Add(Days * 24 * time.Hour): true,
		time.Date(1999, 12, 31, 23, 0, 0, 0, time.UTC): false,
		now.Add(Days*24*time.Hour + time.Second):       false,
	} {
		if err := CheckOpened(at, now); (err == nil) != ok {
			t.Errorf("%v: %v", at, err)
		}
	}
}
