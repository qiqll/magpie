package main

import (
	"sort"
	"strings"
)

// UserPlacements is placements by user: a user's conversations under
// every gateway key are the user's (the user's name alone), and users are
// told apart by how much they ask, since a heavy user's conversation put
// on an account with little left is moved off it where a light user's
// ends first.
//
// A pick is a stint routing chose: its own account was ready, among two or
// more. It is tight when another ready account had roomier (30) points
// more left before its cap. Smart puts first the account whose fullest
// window renews soonest, on purpose, as what it has left is lost then; so
// a tight pick is "sooner" when the account picked renewed before the
// roomier one did (Smart's choice), else "later" (a pick with nothing for
// it). A stint's end is:
//   - "quota": it moved off an account out of what it had: past its cap,
//     spent, low, resting or refused for its quota or credit, gone onto
//     its credits, or stranded there;
//   - "rate": rate limited, or resting for it, with allowance left;
//   - "access": the account was no longer the user's to use (held by the
//     key, out of the user's binding with nothing else said, barred,
//     lapsed, gone);
//   - "failed": another failure, or resting for one;
//   - "routine": it moved off an account still ready (cold, a new turn);
//   - "unknown": the moving route weighed nobody;
//   - "end": with its conversation.
//
// Quota is judged before access: the binding moves a user off an account
// at its cap, so "unbound, left:capped" is the cap's doing.
type UserPlacements struct {
	Users []UserUse `json:"users"`
	Tiers []UserUse `json:"tiers"` // "heavy" and "light", each summed
	// Unbound is the requests of no user, every key's together: kept out
	// of the ranking, as they are not one user's.
	Unbound *UserUse `json:"unbound,omitempty"`
}

// UserUse is one user's (or a tier's) placements.
type UserUse struct {
	User     string `json:"user"`
	Tier     string `json:"tier"` // heavy: the busiest users that together asked 80% of requests
	Requests int    `json:"requests"`
	Tokens   int64  `json:"tokens"`
	Stints   int    `json:"stints"`
	// Ends counts stints by how they ended (endOf).
	Ends map[string]int `json:"ends"`
	// Picks; of them Tight, split into Sooner and Later; and of each how
	// many ended on quota.
	Picks       int `json:"picks"`
	Tight       int `json:"tight"`
	Sooner      int `json:"sooner"`
	Later       int `json:"later"`
	SoonerQuota int `json:"soonerQuota"`
	LaterQuota  int `json:"laterQuota"`
	LooseQuota  int `json:"looseQuota"`
	// PerRequest is how many points a request raised its account's fullest
	// window by, over the user's stints of two or more requests that saw
	// no renewal: the rise between a stint's first and last request over
	// the requests between them. The account's other users count in it.
	PerRequest float64 `json:"perRequest"`
	// For tight picks that ended on quota, by PerRequest: Served the median
	// requests answered before it moved, Had the requests the account
	// picked had room for, Could those the roomier one had.
	Served float64 `json:"served"`
	Had    float64 `json:"had"`
	Could  float64 `json:"could"`
}

// endOf is a stint's end as UserPlacements counts it.
func endOf(s stint) string {
	left := strings.TrimPrefix(s.left, "unbound, ")
	switch {
	case s.ended == "end":
		return "end"
	case s.ended == "stranded", s.ended == "refused", s.ended == "spent", s.ended == "credits":
		return "quota"
	case s.ended == "rate":
		return "rate"
	case strings.Contains(left, "capped"), strings.Contains(left, "spent"), strings.Contains(left, "low"),
		strings.Contains(left, "resting:quota"), strings.Contains(left, "resting:credit"):
		return "quota"
	case strings.Contains(left, "resting:rate"):
		return "rate"
	case strings.Contains(left, "resting"):
		return "failed"
	case left == "untold":
		return "unknown"
	case strings.HasPrefix(s.left, "unbound"), strings.Contains(left, "held"), left == "absent", strings.Contains(left, "barred"), strings.Contains(left, "lapsed"):
		return "access"
	case strings.HasPrefix(s.ended, "failed:"), s.ended == "resting":
		return "failed"
	}
	return "routine"
}

func byUser(stints []stint, m masker) *UserPlacements {
	type acc struct {
		u                  UserUse
		rise               float64
		riseReqs           int
		served, had, could []float64
	}
	users := map[string]*acc{}
	of := func(name string) *acc {
		a := users[name]
		if a == nil {
			label := m.mask(name)
			if name == "" {
				label = "(no user)"
			}
			a = &acc{u: UserUse{User: label, Ends: map[string]int{}}}
			users[name] = a
		}
		return a
	}
	for _, s := range stints {
		a := of(s.user)
		a.u.Requests += s.requests
		a.u.Tokens += int64(s.tokens)
		a.u.Stints++
		a.u.Ends[endOf(s)]++
		if s.headroom >= 0 && s.toKnown && !s.renewed && s.requests >= 2 && s.to >= s.from {
			a.rise += s.to - s.from
			a.riseReqs += s.requests - 1
		}
	}
	for _, a := range users {
		if a.riseReqs > 0 {
			a.u.PerRequest = a.rise / float64(a.riseReqs)
		}
	}
	for _, s := range stints {
		a := users[s.user]
		if s.choice < 2 || !s.ready || s.headroom < 0 {
			continue
		}
		a.u.Picks++
		quota := endOf(s) == "quota"
		if s.best < s.headroom+roomier {
			if quota {
				a.u.LooseQuota++
			}
			continue
		}
		a.u.Tight++
		sooner := s.renews != nil && s.bestRenews != nil && s.renews.Before(*s.bestRenews)
		if sooner {
			a.u.Sooner++
		} else {
			a.u.Later++
		}
		if !quota {
			continue
		}
		if sooner {
			a.u.SoonerQuota++
		} else {
			a.u.LaterQuota++
		}
		if a.u.PerRequest > 0 {
			a.served = append(a.served, float64(s.requests))
			a.had = append(a.had, s.headroom/a.u.PerRequest)
			a.could = append(a.could, s.best/a.u.PerRequest)
		}
	}
	for _, a := range users {
		a.u.Served, a.u.Had, a.u.Could = centile(a.served, 50), centile(a.had, 50), centile(a.could, 50)
	}
	out := &UserPlacements{}
	if a := users[""]; a != nil {
		a.u.Tier = "-"
		out.Unbound = &a.u
	}
	names := make([]string, 0, len(users))
	total := 0
	for n, a := range users {
		if n == "" {
			continue
		}
		names = append(names, n)
		total += a.u.Requests
	}
	sort.Slice(names, func(i, j int) bool {
		if users[names[i]].u.Requests != users[names[j]].u.Requests {
			return users[names[i]].u.Requests > users[names[j]].u.Requests
		}
		return names[i] < names[j]
	})
	tiers := map[string]*acc{}
	for _, k := range []string{"heavy", "light"} {
		tiers[k] = &acc{u: UserUse{User: "(" + k + ")", Tier: k, Ends: map[string]int{}}}
	}
	sum := 0
	for _, n := range names {
		a := users[n]
		a.u.Tier = "light"
		if sum*5 < total*4 { // the busiest until 80% of requests are counted
			a.u.Tier = "heavy"
		}
		sum += a.u.Requests
		out.Users = append(out.Users, a.u)
		t := tiers[a.u.Tier]
		t.u.Requests += a.u.Requests
		t.u.Tokens += a.u.Tokens
		t.u.Stints += a.u.Stints
		for k, v := range a.u.Ends {
			t.u.Ends[k] += v
		}
		t.u.Picks += a.u.Picks
		t.u.Tight += a.u.Tight
		t.u.Sooner += a.u.Sooner
		t.u.Later += a.u.Later
		t.u.SoonerQuota += a.u.SoonerQuota
		t.u.LaterQuota += a.u.LaterQuota
		t.u.LooseQuota += a.u.LooseQuota
		t.rise += a.rise
		t.riseReqs += a.riseReqs
		t.served = append(t.served, a.served...)
		t.had = append(t.had, a.had...)
		t.could = append(t.could, a.could...)
	}
	for _, k := range []string{"heavy", "light"} {
		t := tiers[k]
		if t.riseReqs > 0 {
			t.u.PerRequest = t.rise / float64(t.riseReqs)
		}
		t.u.Served, t.u.Had, t.u.Could = centile(t.served, 50), centile(t.had, 50), centile(t.could, 50)
		out.Tiers = append(out.Tiers, t.u)
	}
	return out
}
