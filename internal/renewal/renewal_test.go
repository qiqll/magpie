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
		s := of(opened, now, time.UTC)
		if s == nil || s.DaysLeft != c.days || s.Due != c.due || !s.Ends.Equal(now.Add(c.left)) || !s.Opened.Equal(opened) {
			t.Errorf("%v left: %+v, want %d days, due %q", c.left, s, c.days, c.due)
		}
	}
	if s := of(time.Time{}, now, time.UTC); s != nil {
		t.Errorf("no time: %+v", s)
	}
	// a day's start in the reader's zone ends on the same date's start,
	// a day the clocks change on included: Berlin goes back an hour on
	// 10-25, so 30 days from 10-10 00:00 are 721 hours, not 720
	sh := time.FixedZone("UTC+8", 8*3600)
	if s := of(time.Date(2026, 11, 1, 0, 0, 0, 0, sh), now, sh); !s.Ends.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, sh)) || s.Ends.Location() != time.UTC {
		t.Errorf("in UTC+8: %+v", s)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no zone database:", err)
	}
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, berlin)
	s := of(start, time.Date(2026, 11, 4, 0, 0, 0, 0, berlin), berlin)
	if !s.Ends.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, berlin)) || s.Ends.Sub(start) != 721*time.Hour {
		t.Errorf("over the clocks' change in Berlin: ends %v", s.Ends.In(berlin))
	}
	if s.DaysLeft != 5 || s.Due != DueSoon {
		t.Errorf("5 days before, in Berlin: %+v", s)
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
