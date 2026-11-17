package gateway

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// rowsWorld is adminWorld with two areas, A and B, each with a key of
// "plan" of its own, the owner's own key beside them, and the clock held
// an hour past now (every call logged at it is after the keys' Added).
type rowsWorld struct {
	do                   adminDo
	console              string
	aID, aKey, bID, bKey string
	aFP, bFP, ownerFP    string
	now                  time.Time
	setNow               func(time.Time)
}

func newRowsWorld(t *testing.T) *rowsWorld {
	t.Helper()
	do, console, _ := adminWorld(t)
	w := &rowsWorld{do: do, console: console}
	w.aID, w.aKey = makeArea(t, do, console, "A", "plan")
	w.bID, w.bKey = makeArea(t, do, console, "B", "plan")
	add := func(secret, key string) {
		b, _ := json.Marshal(map[string]string{"provider": "plan", "name": "key of " + key[:6], "key": key})
		if c, body := do("POST", AdminPath+"/accounts/key", lanPeer, secret, string(b)); c != http.StatusCreated {
			t.Fatal(c, body)
		}
	}
	add(w.aKey, "sk-areaA-000000001")
	add(w.bKey, "sk-areaB-000000001")
	w.aFP, w.bFP, w.ownerFP = provider.KeyID("sk-areaA-000000001"), provider.KeyID("sk-areaB-000000001"), provider.KeyID("upstream-secret-plan")
	var mu sync.Mutex
	w.now = time.Now().Add(time.Hour).Truncate(time.Second)
	at := w.now
	w.setNow = func(t time.Time) { mu.Lock(); at = t; mu.Unlock() }
	was := usage.Clock
	usage.Clock = func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	t.Cleanup(func() { usage.Clock = was })
	return w
}

func (w *rowsWorld) page(t *testing.T, secret, path string) (requestsPage, string) {
	t.Helper()
	c, body := w.do("GET", path, lanPeer, secret, "")
	if c != 200 {
		t.Fatalf("%s: %d %s", path, c, body)
	}
	var p requestsPage
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err, body)
	}
	return p, body
}

func rowRefs(p requestsPage) map[string]int {
	out := map[string]int{}
	for _, r := range p.Rows {
		out[r.Account.ID]++
	}
	return out
}

// An area's key reads the rows of its own accounts alone, and counts them
// alone; the console reads every area's, never the owner's own, and
// ?area= narrows it to one.
func TestAdminRequestsOwnAreaOnly(t *testing.T) {
	w := newRowsWorld(t)
	for i, fp := range []string{w.aFP, w.aFP, w.bFP, w.ownerFP, w.ownerFP, w.ownerFP} {
		usage.Append(usage.Record{Time: w.now.Add(-time.Duration(i+1) * time.Minute), Provider: "plan", ProviderKeyID: fp, Model: "m1", Status: 200, Input: 10})
	}
	a, _ := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d")
	if a.Total != 2 || len(a.Rows) != 2 || rowRefs(a)["plan/"+w.aFP] != 2 || a.Summary.Requests != 2 || a.Summary.Input != 20 {
		t.Fatalf("A's page: %+v", a)
	}
	for _, r := range a.Rows {
		if r.Area != "" {
			t.Fatal("an area's row names its area:", r)
		}
	}
	all, _ := w.page(t, w.console, AdminPath+"/usage/requests?period=7d")
	if all.Total != 3 || rowRefs(all)["plan/"+w.aFP] != 2 || rowRefs(all)["plan/"+w.bFP] != 1 || rowRefs(all)["plan/"+w.ownerFP] != 0 {
		t.Fatalf("the console's every area: %+v", all)
	}
	for _, r := range all.Rows {
		if r.Area == "" {
			t.Fatal("a console row names no area:", r)
		}
	}
	b, _ := w.page(t, w.console, AdminPath+"/usage/requests?period=7d&area="+w.bID)
	if b.Total != 1 || rowRefs(b)["plan/"+w.bFP] != 1 {
		t.Fatalf("the console's ?area=B: %+v", b)
	}
	b2, _ := w.page(t, w.console, AdminPath+"/areas/"+w.bID+"/usage/requests?period=7d")
	if b2.Total != 1 || rowRefs(b2)["plan/"+w.bFP] != 1 {
		t.Fatalf("the console's /areas/B: %+v", b2)
	}
	if c, body := w.do("GET", AdminPath+"/usage/requests?area=ar_nope", lanPeer, w.console, ""); c != http.StatusNotFound {
		t.Fatal("an area there is none of:", c, body)
	}
	// B's account isn't A's to filter by: not found, as one that isn't
	if c, body := w.do("GET", AdminPath+"/usage/requests?period=7d&account=plan/"+w.bFP, lanPeer, w.aKey, ""); c != http.StatusNotFound {
		t.Fatal("A filtered by B's account:", c, body)
	}
	if c, body := w.do("GET", AdminPath+"/areas/"+w.aID+"/usage/requests", lanPeer, w.bKey, ""); c != http.StatusForbidden {
		t.Fatal("B read the console's route of A:", c, body)
	}
}

// An account's calls from before its area got it were the owner's.
func TestAdminRequestsFromAdded(t *testing.T) {
	w := newRowsWorld(t)
	a, _ := area.Get(w.aID)
	added := a.Accounts[0].Added
	usage.Append(usage.Record{Time: added.Add(-time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, Input: 1000})
	usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, Input: 7})
	p, _ := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d")
	if p.Total != 1 || p.Summary.Input != 7 {
		t.Fatalf("from Added: %+v", p)
	}
}

// The summary is what /usage says the same period came to: a try handed
// on is no request and no failure of its own, a canceled one neither,
// and neither counts a call a client reported or one refused before any
// account.
func TestAdminRequestsSummaryIsUsage(t *testing.T) {
	w := newRowsWorld(t)
	at := w.now.Add(-10 * time.Minute)
	rec := func(r usage.Record) {
		at = at.Add(time.Second)
		r.Time, r.Provider, r.ProviderKeyID, r.Model = at, "plan", w.aFP, "m1"
		usage.Append(r)
	}
	rec(usage.Record{RouteID: 1, Status: 429, Error: "rate limited", ErrType: "rate_limit_error", HandedOn: true, Input: 3})
	rec(usage.Record{RouteID: 1, Status: 200, Tries: 2, Input: 10, Output: 2})
	rec(usage.Record{RouteID: 2, Status: 500, Error: "boom", Input: 1})
	rec(usage.Record{RouteID: 3, Status: usage.StatusCanceled, Error: usage.AgentCanceled, ErrType: usage.CanceledErrType})
	rec(usage.Record{RouteID: 4, Status: 200, Input: 5})
	rec(usage.Record{RouteID: 5, Status: 200, Input: 99, Source: usage.SourceReported, RequestID: "rep-1"})
	rec(usage.Record{RouteID: 6, Status: 403, Rejected: true})
	p, _ := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d")
	c, body := w.do("GET", AdminPath+"/usage?period=7d", lanPeer, w.aKey, "")
	if c != 200 {
		t.Fatal(c, body)
	}
	var u AreaUsage
	json.Unmarshal([]byte(body), &u)
	if p.Summary.adminTotals != u.Totals {
		t.Fatalf("summary %+v, /usage %+v", p.Summary.adminTotals, u.Totals)
	}
	want := adminTotals{Requests: 2, Failed: 1, Input: 19, Output: 2}
	if p.Summary.adminTotals.Requests != want.Requests || p.Summary.Failed != want.Failed || p.Summary.Input != want.Input {
		t.Fatalf("summary %+v, want %+v", p.Summary, want)
	}
	if p.Summary.Retried != 1 || p.Summary.Canceled != 1 || p.Total != 5 {
		t.Fatalf("retried, canceled, total: %+v", p)
	}
	statuses := map[string]int{}
	for _, r := range p.Rows {
		statuses[r.Status]++
	}
	if statuses[rowFailed] != 2 || statuses[rowRetried] != 1 || statuses[rowCanceled] != 1 || statuses[rowOK] != 1 {
		t.Fatalf("statuses: %v", statuses)
	}
	f, _ := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d&status=failed")
	if f.Total != 2 || f.Summary.Failed != 1 {
		t.Fatalf("status=failed: %+v", f)
	}
}

// Neither an area nor the console is told the caller's address, the
// host it went to, the alias asked for, the session, the caller's user
// or the vendor's error text.
func TestAdminRequestsNoClientIP(t *testing.T) {
	w := newRowsWorld(t)
	hidden := map[string]string{
		"client ip": "203.0.113.77", "host": "relay.example.net as bob@corp.example", "requested": "group/owners-secret-alias",
		"session": "sess-owner-7f3a", "native session": "native-sess-9c1d", "user": "alice-the-caller",
		"error": "DB_PASSWORD=hunter2 in your prompt", "subagent": "subagent-x91", "computer": "",
	}
	for _, id := range []string{w.aFP, w.bFP} {
		usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: id, Model: "m1", Status: 400, ErrType: "invalid_request_error",
			ClientIP: hidden["client ip"], Host: hidden["host"], Requested: hidden["requested"], Session: hidden["session"], NativeSession: hidden["native session"],
			User: hidden["user"], Error: hidden["error"], Subagent: hidden["subagent"]})
	}
	_, areaBody := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d")
	_, consoleBody := w.page(t, w.console, AdminPath+"/usage/requests?period=7d")
	for name, v := range hidden {
		if v == "" {
			continue
		}
		for who, body := range map[string]string{"area": areaBody, "console": consoleBody} {
			if strings.Contains(body, v) {
				t.Errorf("the %s is told the %s: %s", who, name, body)
			}
		}
	}
	for _, s := range []string{"DB_PASSWORD", "bob@corp", "203.0.113"} {
		if strings.Contains(areaBody, s) || strings.Contains(consoleBody, s) {
			t.Errorf("%q told", s)
		}
	}
	if !strings.Contains(areaBody, `"errorType":"invalid_request_error"`) || !strings.Contains(areaBody, `"httpStatus":400`) {
		t.Fatal("the error's type and status:", areaBody)
	}
}

// A gateway key reaches an area opaque: never its id or name, the same
// within the area and another value in another; the console reads its name.
func TestAdminRequestsCallerKeyOpaque(t *testing.T) {
	w := newRowsWorld(t)
	const id, name = "gk_9f8e7d6c5b4a", "家里 Mac of Alice"
	for _, fp := range []string{w.aFP, w.aFP, w.bFP} {
		usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: fp, Model: "m1", Status: 200, CallerKeyID: id, CallerKeyName: name})
	}
	usage.Append(usage.Record{Time: w.now.Add(-2 * time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200})
	a, aBody := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d")
	b, bBody := w.page(t, w.bKey, AdminPath+"/usage/requests?period=7d")
	for _, body := range []string{aBody, bBody} {
		for _, s := range []string{id, id[3:11], "家里", "Alice", "callerKeyName"} {
			if strings.Contains(body, s) {
				t.Errorf("an area is told %q: %s", s, body)
			}
		}
	}
	keys := map[string]bool{}
	for _, r := range a.Rows {
		keys[r.CallerKey] = true
	}
	if len(keys) != 2 || !keys[callerKeyNone] {
		t.Fatalf("A's caller keys: %v", keys)
	}
	var ak string
	for k := range keys {
		if k != callerKeyNone {
			ak = k
		}
	}
	if len(ak) != 11 || !strings.HasPrefix(ak, "ck_") || b.Rows[0].CallerKey == ak {
		t.Fatalf("A's %q, B's %q", ak, b.Rows[0].CallerKey)
	}
	if f, _ := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d&callerKey="+ak); f.Total != 2 {
		t.Fatalf("filtered by A's caller key: %+v", f)
	}
	if again, _ := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d"); again.Rows[0].CallerKey != a.Rows[0].CallerKey {
		t.Fatal("a caller key not stable within its area")
	}
	if _, cBody := w.page(t, w.console, AdminPath+"/usage/requests?period=7d"); !strings.Contains(cBody, name) {
		t.Fatal("the console isn't told the key's name:", cBody)
	}
}

// Every JSON field of the record (and the ledger's row) is told an admin
// as adminRow has it, or never: a field added to the record and put in
// neither list fails here.
func TestAdminRowFieldsClassified(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[usage.Record](), reflect.TypeFor[usage.Row]()} {
		for _, name := range jsonFields(typ) {
			_, ok := adminRowAllowed[name]
			_, no := adminRowDenied[name]
			if ok == no {
				t.Errorf("%s's field %q is in neither adminRowAllowed nor adminRowDenied, or in both: say whether an admin reads it", typ, name)
			}
		}
	}
	// and every field the row tells is one an allowed field gives
	told := map[string]bool{"area": true, "account": true, "providerName": true, "agentName": true, "stream": true}
	for _, v := range adminRowAllowed {
		for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == '(' || r == ')' || r == ',' }) {
			told[strings.Split(f, ".")[0]] = true
		}
	}
	told["status"] = true
	for _, name := range jsonFields(reflect.TypeFor[adminRow]()) {
		if !told[name] {
			t.Errorf("adminRow's %q comes from no allowed field", name)
		}
	}
}

func jsonFields(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		tag := f.Tag.Get("json")
		if !f.IsExported() || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// An area's key never names its area, its own id included.
func TestAdminRequestsAreaParamRefused(t *testing.T) {
	w := newRowsWorld(t)
	for _, q := range []string{"?area=" + w.aID, "?area=" + w.bID, "?area="} {
		if c, body := w.do("GET", AdminPath+"/usage/requests"+q, lanPeer, w.aKey, ""); c != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, c, body)
		}
	}
	for _, q := range []string{"?period=year", "?period=all", "?tz=Mars/Base", "?limit=0", "?limit=501", "?status=great", "?cursor=nope", "?period=2026-01-01..2026-11-17"} {
		if c, body := w.do("GET", AdminPath+"/usage/requests"+q, lanPeer, w.aKey, ""); c != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, c, body)
		}
	}
}

// An area with no account reads nothing of the ledger.
func TestAdminRequestsEmptyAreaReadsNothing(t *testing.T) {
	w := newRowsWorld(t)
	_, empty := makeArea(t, w.do, w.console, "空", "plan")
	usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: w.ownerFP, Model: "m1", Status: 200, Input: 5})
	var visits int
	was := visitLedger
	visitLedger = func(since time.Time, fn func(usage.Record)) { visits++; was(since, fn) }
	t.Cleanup(func() { visitLedger = was })
	p, _ := w.page(t, empty, AdminPath+"/usage/requests?period=7d")
	if visits != 0 || p.Total != 0 || len(p.Rows) != 0 {
		t.Fatalf("visits %d, page %+v", visits, p)
	}
	w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d")
	if visits != 1 {
		t.Fatal("an area with an account visited", visits)
	}
}

// Pages read on while the ledger grows give each row once: a call made
// after the first page is on none of them, and the first page's total
// holds.
func TestAdminRequestsCursorStable(t *testing.T) {
	w := newRowsWorld(t)
	for i := range 7 {
		// two of them at one time and route: told apart by their order
		at := w.now.Add(-time.Duration(10-i/2) * time.Minute)
		usage.Append(usage.Record{Time: at, RouteID: int64(i / 2), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, Input: i + 1, RequestID: "req-" + string(rune('a'+i))})
	}
	seen := map[string]int{}
	path := AdminPath + "/usage/requests?period=7d&limit=2"
	p, _ := w.page(t, w.aKey, path)
	total, pages := p.Total, 1
	for {
		for _, r := range p.Rows {
			seen[r.VendorRequestID]++
		}
		if p.Total != total {
			t.Fatalf("page %d's total %d, the first's %d", pages, p.Total, total)
		}
		if p.Next == "" {
			break
		}
		// a call made between two pages, as the ledger grows
		w.setNow(w.now.Add(time.Duration(pages) * time.Minute))
		usage.Append(usage.Record{Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, RequestID: "req-late"})
		p, _ = w.page(t, w.aKey, path+"&cursor="+p.Next)
		if pages++; pages > 10 {
			t.Fatalf("still paging after %d pages: a cursor that doesn't move on", pages)
		}
	}
	if len(seen) != 7 || seen["req-late"] != 0 || pages != 4 {
		t.Fatalf("pages %d, seen %v", pages, seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s on %d pages", id, n)
		}
	}
	first, _ := w.page(t, w.aKey, path)
	if c, body := w.do("GET", path+"&model=m2&cursor="+first.Next, lanPeer, w.aKey, ""); c != http.StatusBadRequest {
		t.Fatal("a cursor of another query:", c, body)
	}
	if c, body := w.do("GET", AdminPath+"/usage/requests?period=7d&limit=2&cursor="+first.Next, lanPeer, w.bKey, ""); c != http.StatusBadRequest {
		t.Fatal("A's cursor taken by B:", c, body)
	}
}

// Only the gateway's own records: an agent's session file and a call a
// client reported are on no page. (Another computer's calls are never in
// usage.jsonl, which is all usage.Visit reads.)
func TestAdminRequestsNoSessionRows(t *testing.T) {
	w := newRowsWorld(t)
	was := usage.LogCalls
	usage.LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{{Time: w.now.Add(-time.Minute), Agent: "codex", Session: "s1", Model: "m1", Upstream: "plan", RequestID: "req-session"}}
	}
	t.Cleanup(func() { usage.LogCalls = was })
	usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, Source: usage.SourceReported, RequestID: "req-reported"})
	usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, RequestID: "req-own"})
	p, body := w.page(t, w.aKey, AdminPath+"/usage/requests?period=7d")
	if p.Total != 1 || p.Rows[0].VendorRequestID != "req-own" {
		t.Fatalf("rows: %s", body)
	}
}
