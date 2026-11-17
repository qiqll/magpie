package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// TestAdminUsageAtScale times the admin usage reads on a large ledger
// written straight to usage.jsonl: MAGPIE_ADMIN_SCALE=<calls a day in the
// last 92 days> (the 274 days before get a tenth of that). Not run by
// default; it writes hundreds of MB to a temp HOME. It records numbers,
// it asserts no time.
//
//	MAGPIE_ADMIN_SCALE=12000 go test ./internal/gateway -run TestAdminUsageAtScale -v -timeout 30m
func TestAdminUsageAtScale(t *testing.T) {
	perDay, _ := strconv.Atoi(os.Getenv("MAGPIE_ADMIN_SCALE"))
	if perDay <= 0 {
		t.Skip("set MAGPIE_ADMIN_SCALE to the calls a day to time")
	}
	w := newRowsWorld(t)
	keys := map[string]string{"A": w.aKey, "B": w.bKey}
	fps := []string{w.aFP, w.bFP}
	for _, note := range []string{"C", "D", "E"} {
		_, k := makeArea(t, w.do, w.console, note, "plan")
		secret := "sk-area" + note + "-000000001"
		b, _ := json.Marshal(map[string]string{"provider": "plan", "name": "key of " + note, "key": secret})
		if c, body := w.do("POST", AdminPath+"/accounts/key", lanPeer, k, string(b)); c != http.StatusCreated {
			t.Fatal(c, body)
		}
		keys[note] = k
		fps = append(fps, provider.KeyID(secret))
	}
	// every area has had its key for over a year
	raw, err := os.ReadFile(area.Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version int         `json:"version"`
		Areas   []area.Area `json:"areas"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for i := range f.Areas {
		for j := range f.Areas[i].Accounts {
			f.Areas[i].Accounts[j].Added = w.now.AddDate(-2, 0, 0)
		}
	}
	raw, _ = json.Marshal(f)
	if err := os.WriteFile(area.Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// half the calls are the owner's own key, the rest the five areas'
	owners := append([]string{w.ownerFP, w.ownerFP, w.ownerFP, w.ownerFP, w.ownerFP}, fps...)
	models := []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8"}
	agents := []string{"claude-code", "codex", "opencode", "cursor"}
	start := time.Now()
	out, err := os.Create(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriterSize(out, 1<<20)
	n, route := 0, int64(0)
	for day := 365; day >= 0; day-- {
		calls := perDay
		if day >= 92 {
			calls = perDay / 10
		}
		from := w.now.AddDate(0, 0, -day-1)
		step := 24 * time.Hour / time.Duration(calls)
		for i := range calls {
			route++
			r := usage.Record{Time: from.Add(time.Duration(i) * step), RouteID: route, Agent: agents[i%len(agents)], Provider: "plan",
				ProviderKeyID: owners[i%len(owners)], Model: models[(i/3)%len(models)], Status: 200, Input: 1200 + i%500, Output: 300 + i%90,
				CacheRead: i % 4000, CallerKeyID: "ck" + strconv.Itoa(i/10%20), CallerKeyName: "caller " + strconv.Itoa(i/10%20), RequestID: "req-" + strconv.FormatInt(route, 10)}
			if i%50 == 0 {
				r.Status = 500
			}
			b, _ := json.Marshal(r)
			bw.Write(b)
			bw.WriteByte('\n')
			n++
		}
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	out.Close()
	fi, _ := os.Stat(usage.Path())
	t.Logf("ledger: %d records, %d MB, written in %v", n, fi.Size()>>20, time.Since(start).Round(time.Millisecond))

	timed := func(label, secret, path string, want int) string {
		t.Helper()
		at := time.Now()
		c, body := w.do("GET", path, lanPeer, secret, "")
		took := time.Since(at)
		if c != want {
			t.Fatalf("%s: %d %s", label, c, body[:min(len(body), 300)])
		}
		t.Logf("%-46s %8v  (%d bytes)", label, took.Round(time.Millisecond), len(body))
		return body
	}
	day := w.now.In(time.Local)
	p92 := day.AddDate(0, 0, -91).Format("2006-01-02") + ".." + day.Format("2006-01-02")
	timed("first read (index built), console 7d", w.console, AdminPath+"/usage/requests?period=7d", 200)
	for _, who := range []string{"A", "console"} {
		secret := keys["A"]
		if who == "console" {
			secret = w.console
		}
		path := AdminPath + "/usage/requests?period=" + p92
		body := timed(who+" requests 92d page 1", secret, path, 200)
		for page := 2; page <= 10; page++ {
			var p requestsPage
			json.Unmarshal([]byte(body), &p)
			if p.Next == "" {
				t.Fatal("no next page at", page)
			}
			body = timed(fmt.Sprintf("%s requests 92d page %d", who, page), secret, path+"&cursor="+p.Next, 200)
		}
		var p requestsPage
		json.Unmarshal([]byte(body), &p)
		t.Logf("%s: %d rows in the period", who, p.Total)
		// a page at the period's far end, from the same first page
		c, _ := cursorOf(p.Next)
		c.After = rowKey{T: day.AddDate(0, 0, -90).UnixNano()}
		timed(who+" requests 92d page at day 90", secret, path+"&cursor="+c.String(), 200)
	}
	p366 := day.AddDate(0, 0, -365).Format("2006-01-02") + ".." + day.Format("2006-01-02")
	timed("A stats 92d (day)", keys["A"], AdminPath+"/usage/stats?period="+p92, 200)
	timed("A stats 366d (week)", keys["A"], AdminPath+"/usage/stats?period="+p366+"&granularity=week", 200)
	timed("console stats 366d (week)", w.console, AdminPath+"/usage/stats?period="+p366+"&granularity=week", 200)
	timed("console stats 366d week groupBy=model", w.console, AdminPath+"/usage/stats?period="+p366+"&granularity=week&groupBy=model", 200)
	timed("A stats 7d hour groupBy=callerKey,model top=100 (refused)", keys["A"], AdminPath+"/usage/stats?period=7d&granularity=hour&groupBy=callerKey,model&top=100", 400)
	timed("console stats 366d week groupBy=callerKey,model top=100 (refused)", w.console, AdminPath+"/usage/stats?period="+p366+"&granularity=week&groupBy=callerKey,model&top=100", 400)
}
