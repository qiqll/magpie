package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// countVisits counts the ledger reads from since on, and those of a
// whole period (from a since before every record the test logs).
func countVisits(t *testing.T, periodSince func(time.Time) bool) (whole func() int, all func() int) {
	t.Helper()
	var mu sync.Mutex
	w, a := 0, 0
	was := visitLedger
	visitLedger = func(since time.Time, fn func(usage.Record) bool) {
		mu.Lock()
		a++
		if periodSince(since) {
			w++
		}
		mu.Unlock()
		was(since, fn)
	}
	t.Cleanup(func() { visitLedger = was })
	return func() int { mu.Lock(); defer mu.Unlock(); return w }, func() int { mu.Lock(); defer mu.Unlock(); return a }
}

// freshRowCache is an empty index cache for the test, its clock held.
func freshRowCache(t *testing.T) func(time.Duration) {
	t.Helper()
	was, wasNow := rowIndexes, rowCacheNow
	rowIndexes = &rowCache{}
	var mu sync.Mutex
	at := time.Now()
	rowCacheNow = func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	t.Cleanup(func() { rowIndexes, rowCacheNow = was, wasNow })
	return func(d time.Duration) { mu.Lock(); at = at.Add(d); mu.Unlock() }
}

// The pages after the first read their rows from the first's index:
// the ledger is read for the period once, and a later page reads it
// only from its own oldest row on. A minute later the index is gone.
func TestAdminRequestsPagesFromIndex(t *testing.T) {
	w := newRowsWorld(t)
	pass := freshRowCache(t)
	for i := range 6 {
		usage.Append(usage.Record{Time: w.now.Add(-time.Duration(10+i) * time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, Input: i + 1, RequestID: "req-" + string(rune('a'+i))})
	}
	oldest := w.now.Add(-15 * time.Minute)
	whole, _ := countVisits(t, func(since time.Time) bool { return !since.After(oldest) })
	path := AdminPath + "/usage/requests?period=7d&limit=2"
	p, _ := w.page(t, w.aKey, path)
	if whole() != 1 || p.Total != 6 || len(p.Rows) != 2 {
		t.Fatalf("first page: %d whole reads, %+v", whole(), p)
	}
	seen := map[string]bool{}
	for pages := 1; ; pages++ {
		for _, r := range p.Rows {
			if seen[r.VendorRequestID] {
				t.Fatal("a row twice:", r.VendorRequestID)
			}
			seen[r.VendorRequestID] = true
		}
		if p.Next == "" {
			break
		}
		next, _ := w.page(t, w.aKey, path+"&cursor="+p.Next)
		if next.Total != 6 || next.Summary.Input != 21 {
			t.Fatalf("page %d: %+v", pages+1, next)
		}
		p = next
	}
	// the last page's oldest row is the period's oldest: read from it
	if len(seen) != 6 || whole() != 2 {
		t.Fatalf("seen %v, %d whole reads (the first page's and the last's from its oldest row)", seen, whole())
	}
	w.setNow(w.now.Add(time.Second)) // a first page of its own moment
	first, _ := w.page(t, w.aKey, path)
	second, _ := w.page(t, w.aKey, path+"&cursor="+first.Next)
	if whole() != 3 || len(second.Rows) != 2 || second.Rows[0].VendorRequestID != "req-c" {
		t.Fatalf("a second page read the period again: %d whole reads, %+v", whole(), second)
	}
	pass(rowCacheTTL)
	w.page(t, w.aKey, path+"&cursor="+first.Next)
	if whole() != 4 {
		t.Fatal("an index a minute old was read:", whole())
	}
}

// rowsOf is an area's query read at now through requestRows, as the
// route reads it, after the row after names (nil: the first page).
func rowsOf(t *testing.T, id, query string, now time.Time, after *rowKey) requestsPage {
	t.Helper()
	a, err := area.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	sc, ok := scopeOf(rec, []area.Area{a})
	if !ok {
		t.Fatal(rec.Body)
	}
	q, ok := readRowQuery(rec, httptest.NewRequest("GET", "/?"+query, nil), []string{id}, scopedAccounts(sc), now)
	if !ok {
		t.Fatal(rec.Body)
	}
	q.after = after
	return requestRows(sc, q, false)
}

// The same query of B's at the same moment as A's never reads A's
// index: not its rows, nor its total and summary on a page with no row
// of A's to look up.
func TestAdminRequestsIndexScoped(t *testing.T) {
	w := newRowsWorld(t)
	freshRowCache(t)
	usage.Append(usage.Record{Time: w.now.Add(-time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, Input: 100, RequestID: "req-a1"})
	usage.Append(usage.Record{Time: w.now.Add(-3 * time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, Input: 100, RequestID: "req-a2"})
	usage.Append(usage.Record{Time: w.now.Add(-2 * time.Minute), Provider: "plan", ProviderKeyID: w.bFP, Model: "m1", Status: 200, Input: 7, RequestID: "req-b"})
	a := rowsOf(t, w.aID, "period=7d", w.now, nil)
	if a.Total != 2 || len(a.Rows) != 2 || a.Rows[0].VendorRequestID != "req-a1" {
		t.Fatalf("A: %+v", a)
	}
	b := rowsOf(t, w.bID, "period=7d", w.now, nil)
	if b.Total != 1 || len(b.Rows) != 1 || b.Rows[0].VendorRequestID != "req-b" || b.Rows[0].Account.ID != "plan/"+w.bFP {
		t.Fatalf("B was given A's index: %+v", b)
	}
	// past A's last row: no row to look up in either
	last := &rowKey{T: w.now.Add(-3 * time.Minute).UnixNano()}
	rowsOf(t, w.aID, "period=7d", w.now.Add(time.Second), last)
	b = rowsOf(t, w.bID, "period=7d", w.now.Add(time.Second), last)
	if b.Total != 1 || b.Summary.Input != 7 || len(b.Rows) != 0 {
		t.Fatalf("B told A's total: %+v", b)
	}
}

// An account claimed or let go of since the first page is in the next
// page's rows, or out of them: the old index isn't read.
func TestAdminRequestsIndexFollowsAccounts(t *testing.T) {
	w := newRowsWorld(t)
	freshRowCache(t)
	secret := "sk-areaA-000000002"
	fp := provider.KeyID(secret)
	for i := range 3 {
		usage.Append(usage.Record{Time: w.now.Add(-time.Duration(i+1) * time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200})
	}
	usage.Append(usage.Record{Time: w.now.Add(-30 * time.Second), Provider: "plan", ProviderKeyID: fp, Model: "m1", Status: 200})
	path := AdminPath + "/usage/requests?period=7d&limit=1"
	p, _ := w.page(t, w.aKey, path)
	if p.Total != 3 {
		t.Fatalf("before the claim: %+v", p)
	}
	b, _ := json.Marshal(map[string]string{"provider": "plan", "name": "second", "key": secret})
	if c, body := w.do("POST", AdminPath+"/accounts/key", lanPeer, w.aKey, string(b)); c != http.StatusCreated {
		t.Fatal(c, body)
	}
	p, _ = w.page(t, w.aKey, path+"&cursor="+p.Next)
	if p.Total != 4 {
		t.Fatalf("claimed since the first page: %+v", p)
	}
	if err := area.Release("plan/" + w.aFP); err != nil {
		t.Fatal(err)
	}
	p, _ = w.page(t, w.aKey, path+"&cursor="+p.Next)
	if p.Total != 1 || p.Summary.Requests != 1 || len(p.Rows) != 0 {
		t.Fatalf("let go of since the first page: %+v", p)
	}
}

// The cache keeps 8 entries and no more rows than its bound, the least
// recently used going first.
// A kept index holds no more than its rows: the bound counts rows, and an
// appended slice's array can be twice as long.
func TestRowCacheKeepsAnIndexAtItsLength(t *testing.T) {
	c := &rowCache{}
	rows := make([]indexedRow, 3, 64)
	c.put("k", &rowIndex{rows: rows})
	if got := cap(c.entries[0].idx.rows); got != 3 {
		t.Fatal("kept with room for", got, "rows")
	}
}

func TestRowCacheBounds(t *testing.T) {
	freshRowCache(t)
	c := &rowCache{}
	of := func(n int) *rowIndex { return &rowIndex{rows: make([]indexedRow, n)} }
	for i := range rowCacheMost {
		c.put(string(rune('a'+i)), of(1))
	}
	if c.get("a") == nil {
		t.Fatal("a")
	}
	c.put("z", of(1))
	if c.get("b") != nil || c.get("a") == nil || len(c.entries) != rowCacheMost {
		t.Fatalf("the least recently used stays: %d entries", len(c.entries))
	}
	c.put("big", of(rowCacheRowsMost-1))
	if c.get("big") == nil || len(c.entries) != 2 {
		t.Fatalf("over the rows bound: %d entries", len(c.entries))
	}
	c.put("huge", of(rowCacheRowsMost+1))
	if c.get("huge") != nil {
		t.Fatal("an index over the whole bound kept")
	}
}

// A later page stops reading the ledger once it has its rows: a page
// at the period's far end doesn't read the rest of the period.
func TestAdminRequestsPageStopsAtItsRows(t *testing.T) {
	w := newRowsWorld(t)
	freshRowCache(t)
	for i := range 20 { // the oldest first, as the ledger grows
		usage.Append(usage.Record{Time: w.now.Add(-time.Duration(40-i) * time.Minute), Provider: "plan", ProviderKeyID: w.aFP, Model: "m1", Status: 200, RequestID: "req-" + string(rune('a'+i))})
	}
	var mu sync.Mutex
	read := 0
	was := visitLedger
	visitLedger = func(since time.Time, fn func(usage.Record) bool) {
		was(since, func(r usage.Record) bool { mu.Lock(); read++; mu.Unlock(); return fn(r) })
	}
	t.Cleanup(func() { visitLedger = was })
	path := AdminPath + "/usage/requests?period=7d&limit=2"
	p, _ := w.page(t, w.aKey, path)
	c, _ := cursorOf(p.Next)
	c.After = rowKey{T: w.now.Add(-37*time.Minute - time.Second).UnixNano()} // the far end: req-c and req-b after it
	mu.Lock()
	read = 0
	mu.Unlock()
	far, _ := w.page(t, w.aKey, path+"&cursor="+c.String())
	if len(far.Rows) != 2 || far.Rows[0].VendorRequestID != "req-c" || far.Rows[1].VendorRequestID != "req-b" || read != 2 {
		t.Fatalf("%d records read for %+v", read, far.Rows)
	}
}
