package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/usage"
)

func (w *rowsWorld) stats(t *testing.T, secret, path string) (adminStats, string) {
	t.Helper()
	c, body := w.do("GET", path, lanPeer, secret, "")
	if c != 200 {
		t.Fatalf("%s: %d %s", path, c, body)
	}
	var s adminStats
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err, body)
	}
	return s, body
}

func statsGroupNamed(s adminStats, dim, val string) *statsGroupOut {
	for i := range s.Groups {
		if s.Groups[i].Key[dim] == val {
			return &s.Groups[i]
		}
	}
	return nil
}

// S1: an area's key adds up its own accounts' calls alone, split by
// account too, and can't split by area; the console adds up every
// area's, never the owner's own, and ?area= or the path narrows it.
func TestAdminStatsOwnAreaOnly(t *testing.T) {
	w := newRowsWorld(t)
	for i, fp := range []string{w.aFP, w.aFP, w.bFP, w.ownerFP, w.ownerFP, w.ownerFP} {
		usage.Append(usage.Record{Time: w.now.Add(-time.Duration(i+1) * time.Minute), Provider: "plan", ProviderKeyID: fp, Model: "m1", Status: 200, Input: 10})
	}
	a, _ := w.stats(t, w.aKey, AdminPath+"/usage/stats?period=7d&groupBy=account")
	if a.Totals.Requests != 2 || a.Totals.Input != 20 || len(a.Groups) != 1 || a.Groups[0].Key["account"] != "plan/"+w.aFP || a.Groups[0].Totals.Requests != 2 {
		t.Fatalf("A's stats: %+v", a)
	}
	if c, body := w.do("GET", AdminPath+"/usage/stats?groupBy=area", lanPeer, w.aKey, ""); c != http.StatusBadRequest {
		t.Fatal("an area split by area:", c, body)
	}
	all, _ := w.stats(t, w.console, AdminPath+"/usage/stats?period=7d&groupBy=area")
	if all.Totals.Requests != 3 || len(all.Groups) != 2 || statsGroupNamed(all, "area", w.aID).Totals.Requests != 2 || statsGroupNamed(all, "area", w.bID).Totals.Requests != 1 {
		t.Fatalf("the console's every area: %+v", all)
	}
	if statsGroupNamed(all, "area", w.aID).Name["area"] != "A" {
		t.Fatalf("an area's group isn't named by its note: %+v", all.Groups)
	}
	acc, _ := w.stats(t, w.console, AdminPath+"/usage/stats?period=7d&groupBy=account")
	if statsGroupNamed(acc, "account", "plan/"+w.ownerFP) != nil || len(acc.Groups) != 2 {
		t.Fatalf("the console split by account: %+v", acc.Groups)
	}
	for _, path := range []string{"/usage/stats?period=7d&area=" + w.bID, "/areas/" + w.bID + "/usage/stats?period=7d"} {
		if b, _ := w.stats(t, w.console, AdminPath+path); b.Totals.Requests != 1 || b.Totals.Input != 10 {
			t.Fatalf("%s: %+v", path, b)
		}
	}
	if c, body := w.do("GET", AdminPath+"/usage/stats?period=7d&account=plan/"+w.bFP, lanPeer, w.aKey, ""); c != http.StatusNotFound {
		t.Fatal("A filtered by B's account:", c, body)
	}
	if c, body := w.do("GET", AdminPath+"/usage/stats?area="+w.aID, lanPeer, w.aKey, ""); c != http.StatusBadRequest {
		t.Fatal("an area's ?area=:", c, body)
	}
	if c, body := w.do("GET", AdminPath+"/areas/"+w.aID+"/usage/stats", lanPeer, w.bKey, ""); c != http.StatusForbidden {
		t.Fatal("B read the console's route of A:", c, body)
	}
}

// S2: /usage is the stats of the same period split by model and in days.
func TestAdminStatsMatchesUsage(t *testing.T) {
	w := newRowsWorld(t)
	at := w.now.Add(-30 * time.Minute)
	rec := func(model string, r usage.Record) {
		at = at.Add(time.Second)
		r.Time, r.Provider, r.ProviderKeyID, r.Model = at, "plan", w.aFP, model
		usage.Append(r)
	}
	credits := 2.5
	rec("m1", usage.Record{RouteID: 1, Status: 429, ErrType: "rate_limit_error", HandedOn: true, Input: 3})
	rec("m1", usage.Record{RouteID: 1, Status: 200, Tries: 2, Input: 10, Output: 2, TTFT: 300})
	rec("m2", usage.Record{RouteID: 2, Status: 500, Error: "boom", Input: 1})
	rec("m2", usage.Record{RouteID: 3, Status: usage.StatusCanceled, Error: usage.AgentCanceled, ErrType: usage.CanceledErrType})
	rec("m3", usage.Record{RouteID: 4, Status: 200, Input: 5, CacheRead: 7, CacheWrite: 1, Credits: &credits})
	rec("m3", usage.Record{RouteID: 5, Status: 200, Input: 99, Source: usage.SourceReported})
	usage.Append(usage.Record{Time: at, Provider: "plan", ProviderKeyID: w.bFP, Model: "m1", Status: 200, Input: 1000})

	c, body := w.do("GET", AdminPath+"/usage?period=7d", lanPeer, w.aKey, "")
	if c != 200 {
		t.Fatal(c, body)
	}
	var u AreaUsage
	json.Unmarshal([]byte(body), &u)
	same := func(what string, got statsMetrics, want adminTotals) {
		t.Helper()
		if g := (adminTotals{Requests: got.Requests - got.Failed - got.Canceled, Failed: got.Failed, Input: got.Input, Output: got.Output,
			CacheRead: got.CacheRead, CacheWrite: got.CacheWrite, Cost: got.Cost, Unpriced: got.Unpriced}); g != want {
			t.Errorf("%s: stats %+v, /usage %+v", what, g, want)
		}
	}
	byModel, _ := w.stats(t, w.aKey, AdminPath+"/usage/stats?period=7d&granularity=day&groupBy=model")
	same("totals", byModel.Totals, u.Totals)
	if len(byModel.Groups) != len(u.ByModel) || len(u.ByModel) != 3 {
		t.Fatalf("models: stats %+v, /usage %+v", byModel.Groups, u.ByModel)
	}
	// matched by model: /usage puts a tie in cost by its requests, which
	// count no failure
	for _, m := range u.ByModel {
		g := statsGroupNamed(byModel, "model", m.Model)
		if g == nil {
			t.Fatalf("/usage's %s isn't in the stats: %+v", m.Model, byModel.Groups)
		}
		same(m.Model, g.Totals, m.adminTotals)
	}
	days, _ := w.stats(t, w.aKey, AdminPath+"/usage/stats?period=7d&granularity=day")
	if len(days.Series) != len(days.Buckets) || len(days.Buckets) != 7 {
		t.Fatalf("7d in days: %d buckets, %d points", len(days.Buckets), len(days.Series))
	}
	told := 0
	for i, m := range days.Series {
		if m.Attempts == 0 {
			continue
		}
		day := days.Buckets[i].In(time.Local).Format(time.DateOnly)
		if told >= len(u.ByDay) || u.ByDay[told].Day != day {
			t.Fatalf("day %s isn't /usage's %d: %+v", day, told, u.ByDay)
		}
		same(day, m, u.ByDay[told].adminTotals)
		told++
	}
	if told != len(u.ByDay) || told == 0 {
		t.Fatalf("days: stats %d, /usage %+v", told, u.ByDay)
	}
	if days.Totals.Credits != credits || days.Totals.Timed != 1 || days.Totals.TTFTAvg != 300 || days.Totals.Reasoning != 0 {
		t.Fatalf("credits and ttft: %+v", days.Totals)
	}
}

// statsAt is q over accs (one area, "plan"'s key fp from long ago), the
// ledger being recs and now the clock's time, through the query as an
// area's request reads it.
func statsAt(t *testing.T, recs []usage.Record, now time.Time, query string) adminStats {
	t.Helper()
	was := visitLedger
	visitLedger = func(since time.Time, fn func(usage.Record) bool) {
		for _, r := range recs {
			if !r.Time.Before(since) && !fn(r) {
				return
			}
		}
	}
	t.Cleanup(func() { visitLedger = was })
	w := httptest.NewRecorder()
	q, ok := readStatsQuery(w, httptest.NewRequest("GET", AdminPath+"/usage/stats?"+query, nil), false, map[string]bool{}, now)
	if !ok {
		t.Fatalf("%s: %d %s", query, w.Code, w.Body)
	}
	acc := adminAccount{ID: "plan/fp_zone", Kind: "key", Name: "k", Added: now.AddDate(-2, 0, 0)}
	sc := scopedAreas{areas: []area.Area{{ID: "ar_zone"}}, accs: [][]adminAccount{{acc}}, salts: [][]byte{{1}}, names: map[string]string{acc.ID: acc.Name}}
	out, ok := areaStats(sc, q, false)
	if !ok {
		t.Fatal("too many points:", query)
	}
	b, _ := json.Marshal(out)
	var back adminStats
	json.Unmarshal(b, &back)
	return back
}

// S3: buckets begin at the zone's midnights and hours: a call at 16:30
// UTC is the next day in Shanghai, and a DST day in New York has 23 or 25
// hours, the hour a fall-back repeats twice over. Run with TZ=UTC and a far
// zone: the process's own zone moves none of it.
func TestAdminStatsBucketsInZone(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	call := func(at time.Time) usage.Record {
		return usage.Record{Time: at, Provider: "plan", ProviderKeyID: "fp_zone", Model: "m1", Status: 200, Input: 1}
	}
	late := call(time.Date(2026, 11, 10, 16, 30, 0, 0, time.UTC))
	sh := statsAt(t, []usage.Record{late}, now, "period=2026-11-10..2026-11-11&tz=Asia/Shanghai&granularity=day")
	if len(sh.Series) != 2 || sh.Series[0].Requests != 0 || sh.Series[1].Requests != 1 || sh.Buckets[1].Format(time.RFC3339) != "2026-11-11T00:00:00+08:00" {
		t.Fatalf("Shanghai: %+v", sh)
	}
	utc := statsAt(t, []usage.Record{late}, now, "period=2026-11-10..2026-11-11&tz=UTC&granularity=day")
	if utc.Series[0].Requests != 1 || utc.Series[1].Requests != 0 {
		t.Fatalf("UTC: %+v", utc.Series)
	}
	ny, _ := time.LoadLocation("America/New_York")
	// 2026-11-01: 01:00 EDT (05:00Z), then 01:00 EST (06:00Z) again
	back := call(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC))
	fall := statsAt(t, []usage.Record{back}, now, "period=2026-11-01..2026-11-01&tz=America/New_York&granularity=hour")
	if len(fall.Buckets) != 25 || fall.Series[2].Requests != 1 || fall.Series[1].Requests != 0 {
		t.Fatalf("fall back: %d buckets, %+v", len(fall.Buckets), fall.Series[:3])
	}
	if b1, b2 := fall.Buckets[1].Format(time.RFC3339), fall.Buckets[2].Format(time.RFC3339); b1 != "2026-11-01T01:00:00-04:00" || b2 != "2026-11-01T01:00:00-05:00" {
		t.Fatalf("the repeated hour: %s, %s", b1, b2)
	}
	spring := statsAt(t, nil, now, "period=2026-03-08..2026-03-08&tz=America/New_York&granularity=hour")
	if len(spring.Buckets) != 23 || !spring.Since.Equal(time.Date(2026, 3, 8, 0, 0, 0, 0, ny)) {
		t.Fatalf("spring forward: %d buckets from %s", len(spring.Buckets), spring.Since)
	}
	// weeks begin on a Monday, in the zone
	week := statsAt(t, []usage.Record{late}, now, "period=2026-08-01..2026-11-17&tz=Asia/Shanghai&granularity=week")
	if s := week.Since.Format(time.RFC3339); s != "2026-07-27T00:00:00+08:00" || week.FirstRecord == nil || !week.FirstRecord.Equal(late.Time) {
		t.Fatalf("weeks from %s, first %v", s, week.FirstRecord)
	}
	// no tz: this computer's
	local := statsAt(t, []usage.Record{late}, now, "period=2026-11-09..2026-11-12&granularity=day")
	want := usage.BucketIndex("day", time.Date(2026, 11, 9, 0, 0, 0, 0, time.Local), late.Time.In(time.Local))
	if local.Zone != time.Local.String() || local.Series[want].Requests != 1 {
		t.Fatalf("this computer's zone %s: day %d of %+v", time.Local, want, local.Series)
	}
}

// S4: what a query may ask for: hour buckets for 7 days, day buckets for
// 92, 366 days in all, reaching back 366, two dimensions, and 5000 points
// once top has merged the rest.
func TestAdminStatsLimits(t *testing.T) {
	w := newRowsWorld(t)
	today := w.now.UTC()
	day := func(n int) string { return today.AddDate(0, 0, -n).Format(time.DateOnly) }
	span := func(from, to int) string { return "tz=UTC&period=" + day(from) + ".." + day(to) }
	for q, want := range map[string]int{
		span(6, 0) + "&granularity=hour":     200,
		span(7, 0) + "&granularity=hour":     400,
		span(91, 0) + "&granularity=day":     200,
		span(92, 0) + "&granularity=day":     400,
		span(365, 0):                         200,
		span(366, 0):                         400,
		span(366, 365):                       400, // reaches back too far
		span(-1, -400) + "&granularity=week": 400, // a year on
		"groupBy=model,status":               200,
		"groupBy=model,status,agent":         400,
		"groupBy=model,model":                400,
		"groupBy=client":                     400,
		"groupBy=area":                       400,
		"granularity=month":                  400,
		"top=0":                              400,
		"top=101":                            400,
		"period=all":                         400,
		"period=year":                        400,
		"tz=Mars/Base":                       400,
		"status=great":                       400,
	} {
		if c, body := w.do("GET", AdminPath+"/usage/stats?"+q, lanPeer, w.aKey, ""); c != want {
			t.Errorf("%s: %d, want %d: %s", q, c, want, body)
		}
	}
	if s, _ := w.stats(t, w.aKey, AdminPath+"/usage/stats?"+span(365, 0)); s.Granularity != "week" {
		t.Fatalf("a year's default granularity: %s", s.Granularity)
	}
	if s, _ := w.stats(t, w.aKey, AdminPath+"/usage/stats?period=last:24h"); s.Granularity != "hour" {
		t.Fatalf("a day's default granularity: %s", s.Granularity)
	}
	// 60 models in 92 days: 92 × (50 + other) points pass, 92 × (60) don't
	for i := range 60 {
		usage.Append(usage.Record{Time: w.now.Add(-time.Duration(i+1) * time.Second), Provider: "plan", ProviderKeyID: w.aFP, Model: fmt.Sprintf("m%02d", i), Status: 200, Input: i + 1})
	}
	q := span(91, 0) + "&granularity=day&groupBy=model"
	s, _ := w.stats(t, w.aKey, AdminPath+"/usage/stats?"+q+"&top=50")
	if len(s.Groups) != 50 || s.Other == nil || s.Other.Groups != 10 || len(s.Other.Points) != 92 || s.Other.Totals.Requests != 10 || s.Totals.Requests != 60 {
		t.Fatalf("top 50: %d groups, other %+v", len(s.Groups), s.Other)
	}
	if c, body := w.do("GET", AdminPath+"/usage/stats?"+q+"&top=60", lanPeer, w.aKey, ""); c != http.StatusBadRequest {
		t.Fatal("5520 points:", c, body)
	}
}

// S5: split by caller key, an area is told neither the gateway key's id
// nor its name; the console is told the name.
func TestAdminStatsCallerKeyOpaque(t *testing.T) {
	w := newRowsWorld(t)
	const id, name = "gk_9f8e7d6c5b4a", "家里 Mac of Alice"
	for _, fp := range []string{w.aFP, w.aFP, w.bFP} {
		usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: fp, Model: "m1", Status: 200, CallerKeyID: id, CallerKeyName: name})
	}
	usage.Append(usage.Record{Time: w.now.Add(-2 * time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200})
	a, aBody := w.stats(t, w.aKey, AdminPath+"/usage/stats?period=7d&groupBy=callerKey,agent")
	b, bBody := w.stats(t, w.bKey, AdminPath+"/usage/stats?period=7d&groupBy=callerKey")
	for _, body := range []string{aBody, bBody} {
		for _, s := range []string{id, id[3:11], "家里", "Alice"} {
			if strings.Contains(body, s) {
				t.Errorf("an area is told %q: %s", s, body)
			}
		}
	}
	none, keyed := statsGroupNamed(a, "callerKey", callerKeyNone), (*statsGroupOut)(nil)
	for i := range a.Groups {
		if a.Groups[i].Key["callerKey"] != callerKeyNone {
			keyed = &a.Groups[i]
		}
	}
	if len(a.Groups) != 2 || none == nil || keyed == nil || keyed.Totals.Requests != 2 || len(keyed.Key["callerKey"]) != 11 || b.Groups[0].Key["callerKey"] == keyed.Key["callerKey"] {
		t.Fatalf("A's %+v, B's %+v", a.Groups, b.Groups)
	}
	if f, _ := w.stats(t, w.aKey, AdminPath+"/usage/stats?period=7d&callerKey="+keyed.Key["callerKey"]); f.Totals.Requests != 2 {
		t.Fatalf("filtered by A's caller key: %+v", f.Totals)
	}
	if c, cBody := w.stats(t, w.console, AdminPath+"/usage/stats?period=7d&groupBy=callerKey"); !strings.Contains(cBody, name) || len(c.Groups) != 3 {
		t.Fatal("the console isn't told the key's name:", cBody)
	}
}

// S6: two stats of an area's run at once; a third is 429 with a
// Retry-After and runs nothing, another area's runs, and the area has
// its slots back once its two are done.
func TestAdminStatsConcurrencyHeld(t *testing.T) {
	w := newRowsWorld(t)
	usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200})
	usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: w.bFP, Model: "m1", Status: 200})
	var mu sync.Mutex
	visits := 0
	in, release := make(chan struct{}, 8), make(chan struct{})
	was := visitLedger
	visitLedger = func(since time.Time, fn func(usage.Record) bool) {
		mu.Lock()
		visits++
		n := visits
		mu.Unlock()
		if n <= 2 { // A's first two hold until released
			in <- struct{}{}
			<-release
		}
		was(since, fn)
	}
	t.Cleanup(func() { visitLedger = was })
	path := AdminPath + "/usage/stats?period=7d"
	h := lanGuard(New().Handler()) // one gateway, its slots, for every call here
	call := func(secret string) (int, string, http.Header) {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = lanPeer
		r.Header.Set("Authorization", "Bearer "+secret)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String(), rec.Header()
	}
	done := make(chan int, 2)
	for range 2 {
		go func() { c, _, _ := call(w.aKey); done <- c }()
	}
	<-in
	<-in
	c, body, hdr := call(w.aKey)
	if secs, _ := strconv.Atoi(hdr.Get("Retry-After")); c != http.StatusTooManyRequests || secs < 1 || !strings.Contains(body, adminTooMany) {
		t.Fatalf("A's third: %d %q %s", c, hdr.Get("Retry-After"), body)
	}
	if c, body, _ := call(w.bKey); c != 200 || !strings.Contains(body, `"requests":1,`) {
		t.Fatalf("B, beside A's two: %d %s", c, body)
	}
	mu.Lock()
	if visits != 3 {
		t.Errorf("visits %d: A's third read the ledger", visits)
	}
	mu.Unlock()
	close(release)
	for range 2 {
		if c := <-done; c != 200 {
			t.Fatal("A's held stats:", c)
		}
	}
	if c, body, _ := call(w.aKey); c != 200 {
		t.Fatalf("A once its two are done: %d %s", c, body)
	}
}

// S7: split by status, the requests add up to the totals', and a try
// handed on is an attempt of failed's alone.
func TestAdminStatsStatusPartition(t *testing.T) {
	w := newRowsWorld(t)
	at := w.now.Add(-10 * time.Minute)
	rec := func(r usage.Record) {
		at = at.Add(time.Second)
		r.Time, r.Provider, r.ProviderKeyID, r.Model = at, "plan", w.aFP, "m1"
		usage.Append(r)
	}
	rec(usage.Record{RouteID: 1, Status: 429, ErrType: "rate_limit_error", HandedOn: true})
	rec(usage.Record{RouteID: 1, Status: 200, Tries: 2})
	rec(usage.Record{RouteID: 2, Status: 500, Error: "boom"})
	rec(usage.Record{RouteID: 3, Status: usage.StatusCanceled, Error: usage.AgentCanceled, ErrType: usage.CanceledErrType})
	rec(usage.Record{RouteID: 4, Status: 200})
	rec(usage.Record{RouteID: 5, Status: 200})
	s, _ := w.stats(t, w.aKey, AdminPath+"/usage/stats?period=7d&groupBy=status")
	sum := 0
	for _, g := range s.Groups {
		sum += g.Totals.Requests
	}
	if sum != s.Totals.Requests || s.Totals.Requests != 5 || s.Totals.Attempts != 6 {
		t.Fatalf("requests %d of %+v", sum, s.Totals)
	}
	for st, want := range map[string][2]int{rowOK: {2, 2}, rowRetried: {1, 1}, rowFailed: {1, 2}, rowCanceled: {1, 1}} {
		g := statsGroupNamed(s, "status", st)
		if g == nil || g.Totals.Requests != want[0] || g.Totals.Attempts != want[1] {
			t.Errorf("%s: %+v, want requests and attempts %v", st, g, want)
		}
	}
	if s.Totals.Failed != 1 || s.Totals.Retried != 1 || s.Totals.Canceled != 1 {
		t.Fatalf("totals: %+v", s.Totals)
	}
}

// T-203: a query over 5000 points stops reading once its groups make it
// so (groups are only ever added), and is the same 400; one under the
// bound reads the period to its end.
func TestAdminStatsRefusedStopsReading(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	var recs []usage.Record
	for i := range 1000 { // 1000 models over 1000 minutes, the oldest first
		recs = append(recs, usage.Record{Time: now.Add(-time.Duration(1000-i) * time.Minute), Provider: "plan", ProviderKeyID: "fp_zone", Model: fmt.Sprintf("m%04d", i), Status: 200, Input: 1})
	}
	read := 0
	was := visitLedger
	visitLedger = func(since time.Time, fn func(usage.Record) bool) {
		for _, r := range recs {
			if r.Time.Before(since) {
				continue
			}
			read++
			if !fn(r) {
				return
			}
		}
	}
	t.Cleanup(func() { visitLedger = was })
	run := func(query string) (adminStats, bool) {
		w := httptest.NewRecorder()
		q, ok := readStatsQuery(w, httptest.NewRequest("GET", AdminPath+"/usage/stats?"+query, nil), false, map[string]bool{}, now)
		if !ok {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
		acc := adminAccount{ID: "plan/fp_zone", Kind: "key", Name: "k", Added: now.AddDate(-2, 0, 0)}
		sc := scopedAreas{areas: []area.Area{{ID: "ar_zone"}}, accs: [][]adminAccount{{acc}}, salts: [][]byte{{1}}, names: map[string]string{acc.ID: acc.Name}}
		return areaStats(sc, q, false)
	}
	// 7d to 12:00 is 157 hour buckets: 32 groups are 5024 points, 31 4867
	if _, ok := run("tz=UTC&period=7d&granularity=hour&groupBy=model&top=100"); ok || read != 32 {
		t.Fatalf("refused: ok %v after %d records, want the 32nd", ok, read)
	}
	read = 0
	if s, ok := run("tz=UTC&period=7d&granularity=hour&groupBy=model&top=20"); !ok || read != 1000 || s.Other == nil || s.Other.Groups != 980 {
		t.Fatalf("top 20: ok %v after %d records", ok, read)
	}
	if statsMost(statsQuery{top: 20, buckets: make([]time.Time, 168)}) != 0 || statsMost(statsQuery{top: 100, buckets: make([]time.Time, 157)}) != 32 {
		t.Fatal("statsMost: top 20 of 168 buckets (3528 points) can't be over; top 100 of 157 is over at 32")
	}
}
