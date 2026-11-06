package main

import (
	"sort"
	"time"
)

// Placements is where routing put each conversation, read from routing/:
// a stint is the turns a conversation had on one account, from the turn
// routing put it there (its first, or a move) to the turn it left or its
// last. Each says what the account had left when the stint began — the
// share its fullest window had left, 100 less the Weighed's Used, which
// is what the stint could spend before a window refused it — and what the
// best other account ready then had, and how the stint ended. It tells
// whether conversations put on an account with little left were moved
// for it soon after, while another had room.
type Placements struct {
	Convs  int `json:"convs"`
	Stints int `json:"stints"`
	// Started counts stints by how they began: "first" (routing's pick
	// for a new conversation), "moved" (the conversation left another
	// account) or "seen" (its first route was cut from the history)
	Started map[string]int `json:"started"`
	// Buckets are the stints by what their account had left as they
	// began, the least first; Unknown those whose account's use wasn't
	// told.
	Buckets []Bucket `json:"buckets"`
	Unknown Bucket   `json:"unknown"`
	// Rise is how far the account's fullest window rose over a stint that
	// ended with its conversation and wasn't cut by a renewal, at the
	// 50th, 75th and 90th centile: what a conversation, and whatever else
	// the account answered meanwhile, cost it.
	Rise [3]float64 `json:"rise"`
	// RiseStints is how many stints Rise is of.
	RiseStints int `json:"riseStints"`
	// Ready is how many accounts routing could have put a new
	// conversation on (told, not resting, held, barred, capped, a reserve
	// or a fallback), at the 50th and 90th centile, over first stints:
	// the pool a user's binding leaves routing.
	Ready [2]float64 `json:"ready"`
	// Users is how many users were bound to accounts, and Bound how many
	// accounts each was bound to at the 50th and 90th centile.
	Users int        `json:"users"`
	Bound [2]float64 `json:"bound"`
}

// Bucket is the stints that began with their account's headroom in
// [From, To).
type Bucket struct {
	From, To float64
	Stints   int `json:"stints"`
	First    int `json:"first"` // of them, a new conversation's
	// Forced: it ended as its account couldn't take the next turn:
	// "spent" (all but used up), "resting" or "refused" (a try on it was
	// turned away for its quota or rate). Ended counts every way a stint
	// ended, those too: "end" (the conversation's last turn), "cold",
	// "gone" and others as Affinity.Why says.
	Forced int            `json:"forced"`
	Ended  map[string]int `json:"ended"`
	// Turns is the stints' median length; FiveHours how many began with
	// the five hours, not the week, the fullest window.
	Turns     float64 `json:"turns"`
	FiveHours int     `json:"fiveHours"`
	// Roomier: another ready account had at least roomier (30) points
	// more left when it began.
	Roomier int `json:"roomier"`
}

const roomier = 30

var bucketEdges = []float64{0, 10, 25, 50, 75, 101}

func forced(why string) bool {
	switch why {
	case "spent", "resting", "refused":
		return true
	}
	return false
}

// ofConversation: a route of a conversation's own turn, not a side
// request (a title) nor a subagent's.
func (l routeLine) ofConversation() bool {
	return l.Session != "" && l.Kind == "" && l.Sub == "" && l.SubagentID == ""
}

// slim is the route as place reads it: nothing of a route that isn't a
// conversation's own.
func (l routeLine) slim() routeLine {
	if !l.ofConversation() {
		return routeLine{User: l.User}
	}
	return l
}

func (l routeLine) user() string {
	if l.User != nil {
		return l.User.User
	}
	return ""
}

// answered is the ID of the try that answered, "" when none did.
func (l routeLine) answered() string {
	for i := len(l.Tries) - 1; i >= 0; i-- {
		if t := l.Tries[i]; t.Status >= 200 && t.Status < 300 {
			return t.ID
		}
	}
	return ""
}

func (l routeLine) weighed(id string) (weighedLine, bool) {
	for _, w := range l.Order {
		if w.ID == id {
			return w, true
		}
	}
	return weighedLine{}, false
}

// refusedOn: a try on id was turned away for its quota, credit or rate.
func (l routeLine) refusedOn(id string) bool {
	for _, t := range l.Tries {
		if t.ID == id && (t.Fail == "quota" || t.Fail == "credit" || t.Fail == "rate") {
			return true
		}
	}
	return false
}

// ready: routing could put a new conversation on it.
func (w weighedLine) ready() bool {
	return w.Known && w.Rest == nil && !w.Barred && !w.Held && w.Capped == 0 && w.Reserve == 0 && w.Lapsed == "" && !w.Fallback
}

type stint struct {
	id       string
	headroom float64 // -1: not told
	best     float64 // the best other ready account's headroom, -1: none
	fiveHour bool
	started  string
	ended    string
	turns    int
	from, to float64 // its account's Used at the stint's first and last turn
	toKnown  bool
	renewed  bool
	refused  bool
}

func place(routes []routeLine) *Placements {
	p := &Placements{Started: map[string]int{}}
	bound := map[string]int{}
	byConv := map[string][]routeLine{}
	for _, l := range routes {
		if l.User != nil && l.User.User != "" {
			bound[l.User.User] = max(bound[l.User.User], 1+len(l.User.Spares))
		}
		if !l.ofConversation() {
			continue
		}
		k := l.Provider + "\x00" + l.user() + "\x00" + l.Session + "\x00" + l.Model
		byConv[k] = append(byConv[k], l)
	}
	var stints []stint
	var ready []float64
	for _, rs := range byConv {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Time.Before(rs[j].Time) })
		var cur *stint
		counted := false
		for _, l := range rs {
			id := l.answered()
			if id == "" {
				if cur != nil && l.refusedOn(cur.id) {
					cur.refused = true
				}
				continue
			}
			if cur == nil || id != cur.id {
				started := "seen"
				if l.Affinity != nil && l.Affinity.Why == "first" {
					started = "first"
				} else if cur != nil {
					started = "moved"
				}
				if cur != nil {
					cur.ended = "moved"
					switch {
					case cur.refused || l.refusedOn(cur.id):
						cur.ended = "refused"
					case l.Affinity != nil && l.Affinity.Why != "":
						cur.ended = l.Affinity.Why
					}
					stints = append(stints, *cur)
				}
				cur = &stint{id: id, headroom: -1, best: -1, started: started}
				if w, ok := l.weighed(id); ok && w.Known {
					cur.headroom, cur.from = 100-w.Used, w.Used
					cur.fiveHour = w.UsedRenews != nil && w.UsedRenews.Sub(l.Time) <= 5*time.Hour+time.Minute
				}
				n := 0
				for _, w := range l.Order {
					if !w.ready() {
						continue
					}
					n++
					if w.ID != id && 100-w.Used > cur.best {
						cur.best = 100 - w.Used
					}
				}
				if started == "first" {
					ready = append(ready, float64(n))
				}
				counted = true
			}
			cur.turns++
			if w, ok := l.weighed(cur.id); ok && w.Known {
				if cur.toKnown && w.Used < cur.to || !cur.toKnown && w.Used < cur.from {
					cur.renewed = true // a window renewed in the stint
				}
				cur.to, cur.toKnown = w.Used, true
			}
		}
		if cur != nil {
			cur.ended = "end"
			stints = append(stints, *cur)
		}
		if counted {
			p.Convs++
		}
	}
	for i := 0; i+1 < len(bucketEdges); i++ {
		p.Buckets = append(p.Buckets, Bucket{From: bucketEdges[i], To: bucketEdges[i+1], Ended: map[string]int{}})
	}
	p.Unknown = Bucket{From: -1, To: -1, Ended: map[string]int{}}
	turns := make([][]float64, len(p.Buckets)+1)
	var rises []float64
	for _, s := range stints {
		p.Stints++
		p.Started[s.started]++
		at := len(p.Buckets)
		b := &p.Unknown
		if s.headroom >= 0 {
			for k := range p.Buckets {
				if s.headroom >= p.Buckets[k].From && s.headroom < p.Buckets[k].To {
					b, at = &p.Buckets[k], k
					break
				}
			}
		}
		b.Stints++
		if s.started == "first" {
			b.First++
		}
		b.Ended[s.ended]++
		if forced(s.ended) {
			b.Forced++
		}
		if s.fiveHour {
			b.FiveHours++
		}
		if s.headroom >= 0 && s.best >= s.headroom+roomier {
			b.Roomier++
		}
		turns[at] = append(turns[at], float64(s.turns))
		if s.ended == "end" && s.headroom >= 0 && s.toKnown && !s.renewed {
			rises = append(rises, s.to-s.from)
		}
	}
	for k := range p.Buckets {
		p.Buckets[k].Turns = centile(turns[k], 50)
	}
	p.Unknown.Turns = centile(turns[len(p.Buckets)], 50)
	p.RiseStints = len(rises)
	p.Rise = [3]float64{centile(rises, 50), centile(rises, 75), centile(rises, 90)}
	p.Ready = [2]float64{centile(ready, 50), centile(ready, 90)}
	var bs []float64
	for _, n := range bound {
		bs = append(bs, float64(n))
	}
	p.Users = len(bound)
	p.Bound = [2]float64{centile(bs, 50), centile(bs, 90)}
	return p
}

// centile is the q-th centile of v, nearest rank; 0 when v is empty.
func centile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(q/100*float64(len(s))+0.5) - 1
	return s[min(max(i, 0), len(s)-1)]
}
