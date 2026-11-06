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
// A pick is a stint routing chose among two or more ready accounts. It
// is tight when another ready account had roomier (30) points more left
// before its cap. A stint's end is "quota" when it moved off an account
// out of what it had (capped, spent, low, resting, refused, stranded),
// "access" when the account was no longer the user's to use (held by the
// key, out of the user's binding, gone), "routine" when it moved off one
// ready (cold, a new turn), "failed" on another failure, "end" with its
// conversation.
type UserPlacements struct {
	Users []UserUse `json:"users"`
	Tiers []UserUse `json:"tiers"` // "heavy" and "light", each summed
}

// UserUse is one user's (or a tier's) placements.
type UserUse struct {
	User     string `json:"user"`
	Tier     string `json:"tier"` // heavy: the busiest users that together asked 80% of requests
	Requests int    `json:"requests"`
	Tokens   int64  `json:"tokens"`
	Stints   int    `json:"stints"`
	// Ends counts stints by how they ended: end, quota, access, routine,
	// failed.
	Ends map[string]int `json:"ends"`
	// Picks, of them Tight, and of each how many ended on quota.
	Picks      int `json:"picks"`
	Tight      int `json:"tight"`
	TightQuota int `json:"tightQuota"`
	LooseQuota int `json:"looseQuota"`
	// PerRequest is how many points a request raised its account's fullest
	// window by, over the user's stints that saw no renewal: what turns
	// headroom into requests. The account's other users' requests count in
	// it too, so it is an upper bound.
	PerRequest float64 `json:"perRequest"`
	// Served is the median requests a tight pick that ended on quota had
	// answered before it moved; Could the median requests the roomier
	// account had room for then (its headroom over PerRequest), Had those
	// of the account picked.
	Served float64 `json:"served"`
	Had    float64 `json:"had"`
	Could  float64 `json:"could"`
}

// endOf is a stint's end as UserPlacements counts it.
func endOf(s stint) string {
	switch {
	case s.ended == "end":
		return "end"
	case s.ended == "stranded", s.ended == "refused", s.ended == "spent", s.ended == "resting":
		return "quota"
	case strings.Contains(s.left, "unbound"), strings.Contains(s.left, "held"), s.left == "absent", strings.Contains(s.left, "barred"), strings.Contains(s.left, "lapsed"):
		return "access"
	case strings.Contains(s.left, "capped"), strings.Contains(s.left, "spent"), strings.Contains(s.left, "low"), strings.Contains(s.left, "resting"):
		return "quota"
	case strings.HasPrefix(s.ended, "failed:"):
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
				label = "(not bound)"
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
		if s.headroom >= 0 && s.toKnown && !s.renewed && s.to >= s.from {
			a.rise += s.to - s.from
			a.riseReqs += s.requests
		}
	}
	for name, a := range users {
		if a.riseReqs > 0 {
			a.u.PerRequest = a.rise / float64(a.riseReqs)
		}
		for _, s := range stints {
			if s.user != name || s.choice < 2 || s.headroom < 0 {
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
			if !quota {
				continue
			}
			a.u.TightQuota++
			a.served = append(a.served, float64(s.requests))
			if a.u.PerRequest > 0 {
				a.had = append(a.had, s.headroom/a.u.PerRequest)
				a.could = append(a.could, s.best/a.u.PerRequest)
			}
		}
		a.u.Served, a.u.Had, a.u.Could = centile(a.served, 50), centile(a.had, 50), centile(a.could, 50)
	}
	names := make([]string, 0, len(users))
	total := 0
	for n, a := range users {
		names = append(names, n)
		total += a.u.Requests
	}
	sort.Slice(names, func(i, j int) bool {
		if users[names[i]].u.Requests != users[names[j]].u.Requests {
			return users[names[i]].u.Requests > users[names[j]].u.Requests
		}
		return names[i] < names[j]
	})
	out := &UserPlacements{}
	tiers := map[string]*acc{"heavy": {u: UserUse{User: "heavy", Tier: "heavy", Ends: map[string]int{}}}, "light": {u: UserUse{User: "light", Tier: "light", Ends: map[string]int{}}}}
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
		t.u.TightQuota += a.u.TightQuota
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
