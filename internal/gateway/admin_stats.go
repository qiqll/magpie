package gateway

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/usage"
)

// The usage stats an admin reads (docs/subsystems/areas.md, and
// magpie-console's docs/magpie-api.md §8): GET /usage/stats, an area
// key's for its own area and the console's for every area (?area=
// narrows, groupBy=area splits), and the console's
// /areas/{id}/usage/stats. They take their areas from adminScope and
// their records from areaRows, as the request rows do, and add them up
// in tallyStats, which /usage (areaUsage) adds up in too: the two can't
// tell one period two ways.

// The bounds of a stats query.
const (
	statsDays      = 366 // the period reaches back this many days, today included, and spans no more
	statsHourDays  = 7   // hour buckets span this many days at most
	statsDayDays   = 92  // day buckets this many
	statsPoints    = 5000
	statsTop       = 20
	statsTopMost   = 100
	statsGroupMost = 2
	statsAtOnce    = 2 // stats an area (or the console) has running at once
	statsNone      = "none"
	statsOther     = "other"
	adminTooMany   = "too_many_requests"
)

// statsDims are what stats split by (groupBy), area the console's alone.
var statsDims = []string{"provider", "model", "account", "agent", "callerKey", "status", "area"}

// statsMetrics are what some records came to. requests are the requests
// (a try handed on to another account is none), attempts every record;
// failed, retried and canceled split requests with ok (rowStatus), so a
// status split adds up to requests. answered is /usage's requests
// (adminTotals.add), which counts neither a failure nor a cancel.
type statsMetrics struct {
	Requests   int     `json:"requests"`
	Attempts   int     `json:"attempts"`
	Failed     int     `json:"failed"`
	Retried    int     `json:"retried"`
	Canceled   int     `json:"canceled"`
	Input      int     `json:"inputTokens"`
	Output     int     `json:"outputTokens"`
	CacheRead  int     `json:"cacheReadTokens"`
	CacheWrite int     `json:"cacheWriteTokens"`
	Reasoning  int     `json:"reasoningTokens"`
	Cost       float64 `json:"cost"`
	Unpriced   int     `json:"unpriced"`
	Credits    float64 `json:"credits"`
	TTFTAvg    int64   `json:"ttftMsAvg"` // over the answered calls that streamed
	Timed      int     `json:"timed"`     // how many those were

	ttft     int64
	answered int
}

func (m *statsMetrics) add(r usage.Record, cost float64, priced bool) {
	m.Attempts++
	if !r.HandedOn {
		m.Requests++
		if r.Canceled() {
			m.Canceled++
		}
	}
	if r.LostRequest() {
		m.Failed++
	}
	if r.RetriedRequest() {
		m.Retried++
	}
	if r.Answered() {
		m.answered++
		if r.TTFT > 0 {
			m.Timed++
			m.ttft += r.TTFT
		}
	}
	m.Input += r.Input
	m.Output += r.Output
	m.CacheRead += r.CacheRead
	m.CacheWrite += r.CacheWrite
	m.Reasoning += r.Reasoning
	if priced {
		m.Cost += cost
	} else if r.Input+r.Output+r.CacheRead+r.CacheWrite > 0 {
		m.Unpriced++
	}
	if r.Credits != nil {
		m.Credits += *r.Credits
	}
}

func (m *statsMetrics) plus(o *statsMetrics) {
	m.Requests += o.Requests
	m.Attempts += o.Attempts
	m.Failed += o.Failed
	m.Retried += o.Retried
	m.Canceled += o.Canceled
	m.Input += o.Input
	m.Output += o.Output
	m.CacheRead += o.CacheRead
	m.CacheWrite += o.CacheWrite
	m.Reasoning += o.Reasoning
	m.Cost += o.Cost
	m.Unpriced += o.Unpriced
	m.Credits += o.Credits
	m.Timed += o.Timed
	m.ttft += o.ttft
	m.answered += o.answered
}

// done works out the average once the adding is over.
func (m *statsMetrics) done() {
	m.TTFTAvg = 0
	if m.Timed > 0 {
		m.TTFTAvg = (m.ttft + int64(m.Timed)/2) / int64(m.Timed)
	}
}

// totals are m as /usage tells them.
func (m statsMetrics) totals() adminTotals {
	return adminTotals{Requests: m.answered, Failed: m.Failed, Input: m.Input, Output: m.Output,
		CacheRead: m.CacheRead, CacheWrite: m.CacheWrite, Cost: m.Cost, Unpriced: m.Unpriced}
}

// statsQuery is a stats query, read and checked.
type statsQuery struct {
	period       usage.Period
	loc          *time.Location
	since, until time.Time // the records counted; until zero for no end
	shownUntil   time.Time // the period's end, or now
	gran         string    // hour, day, week or none
	start        time.Time // the first bucket's
	buckets      []time.Time
	groupBy      []string
	filters      map[string][]string
	top          int
}

// spanDays is how many days since to end spans, a DST change's hour not
// making one more.
func spanDays(since, end time.Time) int {
	d := end.Sub(since) - time.Hour
	if d <= 0 {
		return 1
	}
	return int((d + 24*time.Hour - 1) / (24 * time.Hour))
}

// readStatsQuery reads r's period, tz, granularity, groupBy, top and
// filters, accounts being the accounts the account filter may name, now
// the clock's time. Only the console may split by area.
func readStatsQuery(w http.ResponseWriter, r *http.Request, console bool, accounts map[string]bool, now time.Time) (statsQuery, bool) {
	v := r.URL.Query()
	bad := func(msg string) (statsQuery, bool) {
		adminError(w, http.StatusBadRequest, adminBadRequest, msg)
		return statsQuery{}, false
	}
	q := statsQuery{top: statsTop}
	var ok bool
	if q.period, q.loc, ok = readPeriodZone(w, v, usage.Week); !ok {
		return statsQuery{}, false
	}
	at := now.In(q.loc)
	q.since, q.until = q.period.Since(at), q.period.Until(at)
	end := q.until
	if end.IsZero() {
		end = at
	}
	q.shownUntil = end
	if q.since.Before(usage.BucketStart("day", at).AddDate(0, 0, -(statsDays - 1))) {
		return bad("the period reaches back 366 days at most, today included")
	}
	span := spanDays(q.since, end)
	if span > statsDays {
		return bad("the period is 366 days at most")
	}
	switch g := v.Get("granularity"); g {
	case "":
		switch {
		case end.Sub(q.since) <= 48*time.Hour:
			q.gran = "hour"
		case span <= statsDayDays:
			q.gran = "day"
		default:
			q.gran = "week"
		}
	case "hour", "day", "week", statsNone:
		q.gran = g
	default:
		return bad("granularity is hour, day, week or none")
	}
	if q.gran == "hour" && span > statsHourDays {
		return bad("hour granularity is for 7 days at most")
	}
	if q.gran == "day" && span > statsDayDays {
		return bad("day granularity is for 92 days at most")
	}
	q.groupBy = []string{}
	if s := v.Get("groupBy"); s != "" {
		for _, d := range strings.Split(s, ",") {
			d = strings.TrimSpace(d)
			if !slices.Contains(statsDims, d) || d == "area" && !console {
				if console {
					return bad("groupBy is up to two of provider, model, account, agent, callerKey, status and area")
				}
				return bad("groupBy is up to two of provider, model, account, agent, callerKey and status")
			}
			if slices.Contains(q.groupBy, d) {
				return bad("groupBy names " + d + " twice")
			}
			q.groupBy = append(q.groupBy, d)
		}
		if len(q.groupBy) > statsGroupMost {
			return bad("groupBy is two dimensions at most")
		}
	}
	if s := v.Get("top"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > statsTopMost {
			return bad("top is 1 to 100")
		}
		q.top = n
	}
	if q.filters, ok = readFilters(w, v, accounts); !ok {
		return statsQuery{}, false
	}
	q.buckets = []time.Time{}
	if q.gran != statsNone {
		q.start = usage.BucketStart(q.gran, q.since)
		last := q.until
		if last.IsZero() {
			last = at.Add(time.Nanosecond) // up to the bucket now is in
		}
		q.buckets = usage.BucketStarts(q.gran, q.start, last)
	}
	return q, true
}

// statsGroup is one group's tally: its values and names by dimension,
// and what its records came to, over all and in each bucket.
type statsGroup struct {
	key, name map[string]string
	totals    statsMetrics
	points    map[int]*statsMetrics
}

// statsTally is what the records a query keeps came to: over all, in
// each bucket (series), and in each group of each grouping asked for.
type statsTally struct {
	totals statsMetrics
	series []statsMetrics
	groups []map[string]*statsGroup
	first  time.Time
}

// tallyStats adds up the records of sc's areas that q keeps, split by
// each of groupings (each up to two dimensions), from one visit of the
// ledger (areaRows: none for areas with no account). console names a
// caller key and an area; an area is told neither.
func tallyStats(sc scopedAreas, q statsQuery, console bool, groupings [][]string) statsTally {
	t := statsTally{series: make([]statsMetrics, len(q.buckets)), groups: make([]map[string]*statsGroup, len(groupings))}
	for i := range t.groups {
		t.groups[i] = map[string]*statsGroup{}
	}
	faces := newRowFaces()
	cost := usage.NewCoster()
	var sb strings.Builder
	areaRows(sc.accs, q.since, q.until, func(i int, ref string, r usage.Record) {
		val := func(d string) string {
			switch d {
			case "provider":
				return r.Provider
			case "model":
				return r.Model
			case "account":
				return ref
			case "agent":
				return usage.ReportedAs(r.Agent)
			case "callerKey":
				return areaCallerKey(sc.salts[i], r.CallerKeyID)
			case "status":
				return rowStatus(r)
			case "area":
				return sc.areas[i].ID
			}
			return ""
		}
		for k, want := range q.filters {
			if len(want) > 0 && !slices.Contains(want, val(k)) {
				return
			}
		}
		c, priced := cost(r)
		t.totals.add(r, c, priced)
		if t.first.IsZero() || r.Time.Before(t.first) {
			t.first = r.Time
		}
		b := -1
		if len(q.buckets) > 0 {
			if b = usage.BucketIndex(q.gran, q.start, r.Time.In(q.loc)); b >= len(q.buckets) {
				b = -1 // logged past now: a clock set back
			}
		}
		if b >= 0 {
			t.series[b].add(r, c, priced)
		}
		for gi, dims := range groupings {
			sb.Reset()
			vals := make([]string, len(dims))
			for di, d := range dims {
				vals[di] = val(d)
				sb.WriteString(vals[di])
				sb.WriteByte(0)
			}
			g := t.groups[gi][sb.String()]
			if g == nil {
				g = &statsGroup{key: map[string]string{}, name: map[string]string{}, points: map[int]*statsMetrics{}}
				for di, d := range dims {
					g.key[d] = vals[di]
					switch d {
					case "provider":
						g.name[d] = faces.provider(vals[di])
					case "model":
						g.name[d] = vals[di]
					case "account":
						g.name[d] = sc.names[ref]
					case "agent":
						g.name[d] = faces.agent(vals[di])
					case "area":
						g.name[d] = sc.areas[i].Note
					}
				}
				t.groups[gi][sb.String()] = g
			}
			if console && slices.Contains(dims, "callerKey") && r.CallerKeyName != "" {
				g.name["callerKey"] = r.CallerKeyName // the latest the key was called
			}
			g.totals.add(r, c, priced)
			if b >= 0 {
				if g.points[b] == nil {
					g.points[b] = &statsMetrics{}
				}
				g.points[b].add(r, c, priced)
			}
		}
	})
	return t
}

// statsGroupOut and statsOtherOut are a group, and the groups past top, as
// a reply tells them.
type statsGroupOut struct {
	Key    map[string]string `json:"key"`
	Name   map[string]string `json:"name,omitempty"`
	Totals statsMetrics      `json:"totals"`
	Points []statsMetrics    `json:"points,omitempty"`
}

type statsOtherOut struct {
	Groups int            `json:"groups"`
	Totals statsMetrics   `json:"totals"`
	Points []statsMetrics `json:"points,omitempty"`
}

// adminStats is a stats reply.
type adminStats struct {
	Period      string          `json:"period"`
	Zone        string          `json:"zone"`
	Since       time.Time       `json:"since"`
	Until       time.Time       `json:"until"`
	Granularity string          `json:"granularity"`
	GroupBy     []string        `json:"groupBy"`
	FirstRecord *time.Time      `json:"firstRecord,omitempty"`
	Buckets     []time.Time     `json:"buckets"`
	Totals      statsMetrics    `json:"totals"`
	Groups      []statsGroupOut `json:"groups"`
	Other       *statsOtherOut  `json:"other,omitempty"`
	Series      []statsMetrics  `json:"series,omitempty"`
}

// sortedGroups are g's groups, the most spent first: by cost, then
// requests, then key.
func sortedGroups(g map[string]*statsGroup) []*statsGroup {
	out := make([]*statsGroup, 0, len(g))
	keys := map[*statsGroup]string{}
	for k, x := range g {
		out = append(out, x)
		keys[x] = k
	}
	slices.SortFunc(out, func(x, y *statsGroup) int {
		if c := cmp.Compare(y.totals.Cost, x.totals.Cost); c != 0 {
			return c
		}
		if c := cmp.Compare(y.totals.Requests, x.totals.Requests); c != 0 {
			return c
		}
		return strings.Compare(keys[x], keys[y])
	})
	return out
}

// dense is a group's points, one for each of n buckets.
func dense(points map[int]*statsMetrics, n int) []statsMetrics {
	out := make([]statsMetrics, n)
	for b, m := range points {
		out[b] = *m
		out[b].done()
	}
	return out
}

// areaStats is q over sc's areas, false when its points, top applied,
// are more than statsPoints.
func areaStats(sc scopedAreas, q statsQuery, console bool) (adminStats, bool) {
	var groupings [][]string
	if len(q.groupBy) > 0 {
		groupings = [][]string{q.groupBy}
	}
	t := tallyStats(sc, q, console, groupings)
	out := adminStats{Period: string(q.period), Zone: q.loc.String(), Since: q.since, Until: q.shownUntil, Granularity: q.gran,
		GroupBy: q.groupBy, Buckets: q.buckets, Totals: t.totals, Groups: []statsGroupOut{}}
	if q.gran != statsNone {
		out.Since = q.start
	}
	out.Totals.done()
	if !t.first.IsZero() {
		f := t.first.UTC()
		out.FirstRecord = &f
	}
	n := len(q.buckets)
	if len(groupings) == 0 {
		if q.gran != statsNone {
			out.Series = t.series
			for i := range out.Series {
				out.Series[i].done()
			}
		}
		return out, true
	}
	all := sortedGroups(t.groups[0])
	kept := all[:min(len(all), q.top)]
	if rest := all[len(kept):]; len(rest) > 0 {
		o := &statsOtherOut{Groups: len(rest)}
		points := map[int]*statsMetrics{}
		for _, g := range rest {
			o.Totals.plus(&g.totals)
			for b, m := range g.points {
				if points[b] == nil {
					points[b] = &statsMetrics{}
				}
				points[b].plus(m)
			}
		}
		o.Totals.done()
		if q.gran != statsNone {
			o.Points = dense(points, n)
		}
		out.Other = o
	}
	shown := len(kept)
	if out.Other != nil {
		shown++
	}
	if max(n, 1)*shown > statsPoints {
		return out, false
	}
	for _, g := range kept {
		x := statsGroupOut{Key: g.key, Name: g.name, Totals: g.totals}
		x.Totals.done()
		if q.gran != statsNone {
			x.Points = dense(g.points, n)
		}
		out.Groups = append(out.Groups, x)
	}
	return out, true
}

// statsSlots are the stats each area (or the console) has running, held
// to statsAtOnce: a year of weeks reads a year of the log. Nothing that
// waits runs under mu.
type statsSlots struct {
	mu      sync.Mutex
	running map[string]int
}

// take is a slot of id's, false when it has every one taken.
func (s *statsSlots) take(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] >= statsAtOnce {
		return false
	}
	if s.running == nil {
		s.running = map[string]int{}
	}
	s.running[id]++
	return true
}

func (s *statsSlots) give(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id]--; s.running[id] <= 0 {
		delete(s.running, id)
	}
}

func (s *Server) adminUsageStats(w http.ResponseWriter, r *http.Request, c adminCall) {
	scope, ok := adminScope(w, r, c)
	if !ok {
		return
	}
	sc, ok := scopeOf(w, scope)
	if !ok {
		return
	}
	console := c.who.Admin == access.AdminConsole
	q, ok := readStatsQuery(w, r, console, scopedAccounts(sc), usage.Clock())
	if !ok {
		return
	}
	// the console's stats are held apart from every area's, so neither
	// holds the other
	slot := "area " + c.area.ID
	if console {
		slot = "console"
	}
	if !s.stats.take(slot) {
		w.Header().Set("Retry-After", "1")
		adminError(w, http.StatusTooManyRequests, adminTooMany, "two stats of this key's are running: try again when one is done")
		return
	}
	defer s.stats.give(slot)
	out, ok := areaStats(sc, q, console)
	if !ok {
		adminError(w, http.StatusBadRequest, adminBadRequest, "more than 5000 points: a shorter period, a coarser granularity or a smaller top")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// scopedAccounts are the ids of sc's accounts, which an account filter
// may name.
func scopedAccounts(sc scopedAreas) map[string]bool {
	out := make(map[string]bool, len(sc.names))
	for id := range sc.names {
		out[id] = true
	}
	return out
}

// usageStats is /usage over a's accounts: the records p keeps from
// since, in day buckets, split by model and by account, from one visit.
func usageStats(a area.Area, p usage.Period, loc *time.Location, now time.Time) (statsQuery, statsTally, map[string]string) {
	accs := areaAccounts(a)
	names := map[string]string{}
	for _, acc := range accs {
		names[acc.ID] = acc.Name
	}
	now = now.In(loc)
	q := statsQuery{period: p, loc: loc, since: p.Since(now), gran: "day"}
	q.start = usage.BucketStart("day", q.since)
	q.buckets = usage.BucketStarts("day", q.start, now.Add(time.Nanosecond))
	sc := scopedAreas{areas: []area.Area{a}, accs: [][]adminAccount{accs}, salts: [][]byte{nil}, names: names}
	return q, tallyStats(sc, q, false, [][]string{{"model"}, {"account"}}), names
}
