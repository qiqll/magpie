// Package renewal is when something the user pays for by the month runs
// out, from the time they say it was opened: a subscription account's
// (provider.AccountOpened, T-154) and a proxy exit IP's (ips.json, T-155).
// Both pages read the one calculation here (Of), so they never say
// different things of the same dates.
package renewal

import (
	"errors"
	"time"
)

// Days is the length of one subscription period: fixed (需求方
// 2026-11-11), not set per account or per IP.
const Days = 30

// WarnDays is how many days before the end the page asks for a renewal.
const WarnDays = 5

// Due says what a page asks of the user: nothing yet, renew soon, or
// renew now, the period over.
const (
	DueSoon  = "soon"
	DueEnded = "ended"
)

// State is a period worked out at a time: when it was opened, when it
// ends (Days later), the whole days left, rounded up so that 5 days and an
// hour is 6, 0 once it has ended, and Due.
type State struct {
	Opened   time.Time `json:"opened"`
	Ends     time.Time `json:"ends"`
	DaysLeft int       `json:"daysLeft"`
	Due      string    `json:"due,omitempty"`
}

// Of is the period opened at opened as it stands at now, nil for no time.
// It ends Days×24h after opened; from WarnDays days before then it is due
// soon, and at its end it is ended. One period only: a renewal is the
// user setting the new time.
func Of(opened, now time.Time) *State {
	if opened.IsZero() {
		return nil
	}
	s := &State{Opened: opened.UTC(), Ends: opened.Add(Days * 24 * time.Hour).UTC()}
	left := s.Ends.Sub(now)
	if left <= 0 {
		s.Due = DueEnded
		return s
	}
	s.DaysLeft = int((left + 24*time.Hour - 1) / (24 * time.Hour))
	if s.DaysLeft <= WarnDays {
		s.Due = DueSoon
	}
	return s
}

// CheckOpened says whether t can be kept as an opening time: from 2000 on
// and no more than a period ahead of now, so a mistyped year is refused
// rather than kept as never due.
func CheckOpened(t, now time.Time) error {
	if t.Year() < 2000 || t.After(now.Add(Days*24*time.Hour)) {
		return errOpened
	}
	return nil
}

var errOpened = errors.New("the opening date is from 2000 on and at most 30 days ahead")
