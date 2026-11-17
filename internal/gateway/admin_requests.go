package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// The usage request rows an admin reads (docs/subsystems/areas.md, and
// magpie-console's docs/magpie-api.md §8): GET /usage/requests, an area
// key's for its own area and the console's for every area (?area=
// narrows), and the console's /areas/{id}/usage/requests. Every read
// takes its areas from adminScope alone, its rows from areaRows alone,
// and tells each row through adminRowOf alone, an allow list.

// adminScope is the areas an admin read covers, from its key alone: an
// area key's own area, never one the request names (?area= is a 400,
// its own id too: a console that sends it has a bug, which no silence
// should hide); the console's every area, or the one the path or
// ?area= names (404 when there is none). Never the owner's own
// accounts: they are in no area.
func adminScope(w http.ResponseWriter, r *http.Request, c adminCall) ([]area.Area, bool) {
	q := r.URL.Query()
	if c.who.Admin == access.AdminArea {
		if q.Has("area") {
			adminError(w, http.StatusBadRequest, adminBadRequest, "an area admin's key reads its own area alone: leave ?area= out")
			return nil, false
		}
		return []area.Area{c.area}, true
	}
	if c.who.Admin != access.AdminConsole {
		adminError(w, http.StatusUnauthorized, adminKeyRequired, "send an enabled magpie admin key as Authorization: Bearer <key>")
		return nil, false
	}
	if r.PathValue("id") != "" {
		if q.Has("area") {
			adminError(w, http.StatusBadRequest, adminBadRequest, "the path names the area already: leave ?area= out")
			return nil, false
		}
		a, ok := consoleArea(w, r)
		return []area.Area{a}, ok
	}
	if q.Has("area") {
		a, err := area.Get(q.Get("area"))
		switch {
		case errors.Is(err, area.ErrNotFound):
			adminNotFoundErr(w)
			return nil, false
		case err != nil:
			adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
			return nil, false
		}
		return []area.Area{a}, true
	}
	areas, err := area.List()
	if err != nil {
		adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
		return nil, false
	}
	return areas, true
}

// visitLedger is usage.Visit, a variable for the tests, which count
// that an area with no account reads nothing.
var visitLedger = usage.Visit

// areaRecord is whether r is a request the gateway here took on and
// logged: not one it refused before reaching any account, nor one a
// client reported (it went straight to its vendor), nor another
// computer's.
func areaRecord(r usage.Record) bool {
	return !r.IsRejected() && !r.IsReported() && r.Computer == ""
}

// areaRows calls fn with each gateway record in [since, until) (no end
// for a zero until) that an account of scope answered, scope[i] being
// area i's accounts: a key's by its fingerprint (and those it had
// before), an account's by Record.Account() under its provider, each
// from the time the area got it. A record is given once, to the first
// account that names it. An area with no account visits nothing: never
// the whole ledger.
func areaRows(scope [][]adminAccount, since, until time.Time, fn func(area int, ref string, r usage.Record)) {
	type own struct {
		area int
		areaMatch
	}
	byProvider := map[string][]own{}
	for i, accs := range scope {
		for pid, ms := range areaMatches(accs) {
			for _, m := range ms {
				byProvider[pid] = append(byProvider[pid], own{i, m})
			}
		}
	}
	if len(byProvider) == 0 {
		return
	}
	visitLedger(since, func(r usage.Record) {
		if r.Time.Before(since) || !until.IsZero() && !r.Time.Before(until) || !areaRecord(r) {
			return
		}
		for _, m := range byProvider[r.Provider] {
			if r.Time.Before(m.added) || !m.names(r) {
				continue
			}
			fn(m.area, m.ref, r)
			return
		}
	})
}

// The statuses a row is told by (rowStatus).
const (
	rowOK       = "ok"
	rowRetried  = "retried"
	rowFailed   = "failed"
	rowCanceled = "canceled"
)

// rowStatus is r's: canceled by its agent, failed (a try handed on to
// another account too), answered after a failed try, or answered.
func rowStatus(r usage.Record) string {
	switch {
	case r.Canceled():
		return rowCanceled
	case r.Failed():
		return rowFailed
	case r.RetriedRequest():
		return rowRetried
	}
	return rowOK
}

// callerKeyNone is the callerKey of a call that came with no gateway
// key (this computer's).
const callerKeyNone = "ck_none"

// areaCallerKey is how an area is told the gateway key id: opaque, the
// same within the area and different in another (salt is the area's
// own, area.Salt), and no name.
func areaCallerKey(salt []byte, id string) string {
	if id == "" {
		return callerKeyNone
	}
	m := hmac.New(sha256.New, salt)
	m.Write([]byte(id))
	return "ck_" + hex.EncodeToString(m.Sum(nil))[:8]
}

// adminRowAccount is the area's account that answered a row.
type adminRowAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// adminRow is one request as an admin reads it. Every field is copied
// from the record by name (adminRowOf), never the record with fields
// taken out: a field the record gains later reaches no admin until it is
// put here. adminRowAllowed and adminRowDenied say where each of the
// record's fields went; TestAdminRowFieldsClassified fails on one in
// neither.
type adminRow struct {
	Time          time.Time       `json:"time"`
	Area          string          `json:"area,omitempty"` // the console's alone
	Account       adminRowAccount `json:"account"`
	Provider      string          `json:"provider"`
	ProviderName  string          `json:"providerName"`
	Model         string          `json:"model"`
	Served        string          `json:"served"`
	Agent         string          `json:"agent"`
	AgentName     string          `json:"agentName"`
	CallerKey     string          `json:"callerKey"`
	CallerKeyName string          `json:"callerKeyName,omitempty"` // the console's alone
	Status        string          `json:"status"`
	HTTPStatus    int             `json:"httpStatus"`
	ErrorType     string          `json:"errorType"`
	HandedOn      bool            `json:"handedOn"`
	Tries         int             `json:"tries"`
	Input         int             `json:"inputTokens"`
	Output        int             `json:"outputTokens"`
	CacheRead     int             `json:"cacheReadTokens"`
	CacheWrite    int             `json:"cacheWriteTokens"`
	Reasoning     int             `json:"reasoningTokens"`
	Cost          float64         `json:"cost"`
	Priced        bool            `json:"priced"`
	Credits       *float64        `json:"credits"`
	Effort        string          `json:"effort"`
	TierSent      string          `json:"tierSent"`
	TierServed    string          `json:"tierServed"`
	Ms            int64           `json:"ms"`
	TTFTMs        int64           `json:"ttftMs"`
	// Stream: the reply was streamed (it has a time to first content,
	// which only a streamed one has)
	Stream          bool   `json:"stream"`
	Endpoint        string `json:"endpoint"`
	VendorRequestID string `json:"vendorRequestId"`
}

// adminRowAllowed are the record's JSON fields an admin row tells, by
// the field it tells it as; adminRowDenied the ones it never tells, and
// why. Every JSON field of usage.Record and usage.Row is in one of them.
var adminRowAllowed = map[string]string{
	"t": "time", "agent": "agent (as ReportedAs names it)", "provider": "provider",
	"providerKeyId": "account.id (claimed by it, never told as it is)", "providerKeyName": "account.name (the area's own key)",
	"providerAccount": "account.name (the area's own account)",
	"callerKeyId":     "callerKey (opaque per area)", "callerKeyName": "callerKeyName (the console's alone)",
	"model": "model", "served": "served", "in": "inputTokens", "out": "outputTokens",
	"cache_read": "cacheReadTokens", "cache_write": "cacheWriteTokens", "reasoning": "reasoningTokens",
	"effort": "effort", "tier_sent": "tierSent", "tier_served": "tierServed", "ms": "ms", "ttft_ms": "ttftMs and stream",
	"status": "httpStatus and status", "err_type": "errorType", "rid": "vendorRequestId", "ep": "endpoint",
	"tries": "tries", "handed_on": "handedOn", "credits": "credits",
	"cost": "cost", "priced": "priced",
}

var adminRowDenied = map[string]string{
	"client_ip":              "the caller's address: never",
	"host":                   "where the call went, as whom, maybe the owner's relay",
	"req":                    "the owner's alias or routing group",
	"session":                "the caller's conversation",
	"native_session":         "the caller's conversation",
	"session_provider":       "the caller's session",
	"session_account":        "the caller's session",
	"session_official_login": "the caller's session",
	"subagent":               "the caller's session",
	"parent_agent":           "the caller's session",
	"user":                   "the caller's own user",
	"computer":               "the owner's other computers",
	"via":                    "the owner's other computers",
	"archive":                "the owner's request archive",
	"err":                    "the vendor's words often echo the request: its type and status alone",
	"told":                   "what the caller was told",
	"callerIssuerId":         "the owner's gateway keys",
	"operation":              "not what the account spent",
	"route_id":               "magpie's own id",
	"upstream":               "not what the account spent",
	"cache_write_1h":         "not what the account spent",
	"cache_unknown":          "not what the account spent",
	"tier_asked":             "what the caller asked",
	"first_text_ms":          "not what the account spent",
	"compaction":             "the caller's conversation",
	"first_reasoning_ms":     "not what the account spent",
	"reasoning_hidden":       "not what the account spent",
	"sent_ms":                "magpie's own timing",
	"own_ms":                 "magpie's own timing",
	"masking_ms":             "the owner's privacy settings",
	"retried_ms":             "magpie's own timing",
	"queued_ms":              "magpie's own timing",
	"flow_ms":                "magpie's own timing",
	"response_id":            "the caller's conversation",
	"stop":                   "not what the account spent",
	"rejected":               "never listed (areaRecord)",
	"up_bytes":               "the caller's request",
	"plain_bytes":            "the caller's request",
	"down_bytes":             "the caller's reply",
	"image_files_saved":      "the caller's request",
	"image_files":            "the caller's request",
	"ws":                     "not what the account spent",
	"kind":                   "the caller's agent's purpose",
	"source":                 "never listed (areaRecord)",
	"session_role":           "the caller's session",
	"swapped":                "not what the account spent",
	"routed":                 "the owner's routing",
	"client":                 "the caller's own client name",
}

// rowFaces names providers and agents for rows, each looked up once.
type rowFaces struct {
	providers map[string]string
	agents    map[string]string
}

func newRowFaces() *rowFaces {
	f := &rowFaces{providers: map[string]string{}, agents: map[string]string{}}
	if usageapi.Clients != nil {
		for _, c := range usageapi.Clients() {
			f.agents[c.ID] = c.Name
		}
	}
	return f
}

func (f *rowFaces) provider(id string) string {
	if n, ok := f.providers[id]; ok {
		return n
	}
	n := id
	if p, err := provider.Find(id); err == nil && p.Name != "" {
		n = p.Name
	}
	f.providers[id] = n
	return n
}

func (f *rowFaces) agent(id string) string {
	if n := f.agents[id]; n != "" {
		return n
	}
	return id
}

// adminRowOf is r as an admin reads it: the area's account that
// answered it (ref, named name), and the console's fields only for
// console.
func adminRowOf(r usage.Record, areaID, ref, name string, salt []byte, console bool, cost float64, priced bool, faces *rowFaces) adminRow {
	agent := usage.ReportedAs(r.Agent)
	out := adminRow{
		Time: r.Time.UTC(), Account: adminRowAccount{ID: ref, Name: name},
		Provider: r.Provider, ProviderName: faces.provider(r.Provider), Model: r.Model, Served: r.Served,
		Agent: agent, AgentName: faces.agent(agent), CallerKey: areaCallerKey(salt, r.CallerKeyID),
		Status: rowStatus(r), HTTPStatus: r.Status, ErrorType: r.ErrType, HandedOn: r.HandedOn, Tries: max(1, r.Tries),
		Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite, Reasoning: r.Reasoning,
		Cost: cost, Priced: priced, Effort: r.Effort, TierSent: r.TierSent, TierServed: r.TierServed,
		Ms: r.Millis, TTFTMs: r.TTFT, Stream: r.TTFT > 0, Endpoint: r.Endpoint, VendorRequestID: r.RequestID,
	}
	if r.Credits != nil {
		c := *r.Credits
		out.Credits = &c
	}
	if console {
		out.Area, out.CallerKeyName = areaID, r.CallerKeyName
	}
	return out
}

// requestsSummary is what a query's rows came to, every page's: the
// usage totals (adminTotals, as /usage counts them) and the requests
// answered after a failed try, and those their agent canceled.
type requestsSummary struct {
	adminTotals
	Retried  int `json:"retried"`
	Canceled int `json:"canceled"`
}

func (s *requestsSummary) add(r usage.Record, cost float64, priced bool) {
	s.adminTotals.add(r, cost, priced)
	if r.RetriedRequest() {
		s.Retried++
	}
	if !r.HandedOn && r.Canceled() {
		s.Canceled++
	}
}

// requestsPage is one page of an admin's request rows.
type requestsPage struct {
	Period  string          `json:"period"`
	Zone    string          `json:"zone"`
	Since   time.Time       `json:"since"`
	Until   time.Time       `json:"until"`
	Summary requestsSummary `json:"summary"`
	Total   int             `json:"total"` // the rows the filters keep, a try handed on too
	Rows    []adminRow      `json:"rows"`  // the newest first
	Next    string          `json:"next,omitempty"`
}

// The bounds of a request rows query.
const (
	requestsDays      = 92
	requestsLimit     = 100
	requestsLimitMost = 500
)

// rowKey orders rows newest first: by time, then route, then the order
// the ledger has rows of one time and route in (ord). A row the ledger
// gains between two pages sorts where its key says, so a page never
// repeats or skips one it had.
type rowKey struct {
	T   int64 `json:"t"`
	R   int64 `json:"r"`
	Ord int   `json:"o"`
}

// before: k comes before o, newest first.
func (k rowKey) before(o rowKey) bool {
	if k.T != o.T {
		return k.T > o.T
	}
	if k.R != o.R {
		return k.R > o.R
	}
	return k.Ord > o.Ord
}

// rowCursor is where a page left off: the moment the first page was
// read (AsOf, which fixes the period's window and leaves out every call
// made after it), the last row given, and the query it is of.
type rowCursor struct {
	AsOf  int64  `json:"a"`
	After rowKey `json:"k"`
	Query string `json:"q"`
}

func (c rowCursor) String() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func cursorOf(s string) (rowCursor, bool) {
	var c rowCursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.AsOf == 0 || c.Query == "" {
		return rowCursor{}, false
	}
	return c, true
}

// rowQuery is a request rows query, read and checked.
type rowQuery struct {
	period       usage.Period
	loc          *time.Location
	asOf         time.Time
	since, until time.Time // until: zero for no end before asOf
	filters      map[string][]string
	limit        int
	after        *rowKey
	hash         string
}

// rowFilters are the filters a query takes, each repeatable.
var rowFilters = []string{"provider", "model", "account", "agent", "callerKey", "status"}

// readPeriodZone reads v's period (def when there is none) and tz: a
// period usage.Period.Known() takes except all, never another in place
// of one it doesn't, and an IANA zone (this computer's by default).
func readPeriodZone(w http.ResponseWriter, v url.Values, def usage.Period) (usage.Period, *time.Location, bool) {
	p, loc := def, time.Local
	if s := v.Get("period"); s != "" {
		p = usage.Period(s)
	}
	if !p.Known() || p == usage.All {
		adminError(w, http.StatusBadRequest, adminBadRequest, "period is today, 7d, 30d, last:<duration>, YYYY-MM-DD..YYYY-MM-DD or YYYY-MM-DDTHH:MM..YYYY-MM-DDTHH:MM")
		return "", nil, false
	}
	if tz := v.Get("tz"); tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			adminError(w, http.StatusBadRequest, adminBadRequest, "tz is an IANA time zone, such as Asia/Shanghai")
			return "", nil, false
		}
		loc = l
	}
	return p, loc, true
}

// readFilters reads v's filters (rowFilters, each repeatable), sorted,
// accounts being the accounts the account filter may name: another is
// 404, as one that isn't.
func readFilters(w http.ResponseWriter, v url.Values, accounts map[string]bool) (map[string][]string, bool) {
	out := map[string][]string{}
	for _, k := range rowFilters {
		for _, x := range v[k] {
			if x == "" {
				continue
			}
			switch k {
			case "status":
				if !slices.Contains([]string{rowOK, rowRetried, rowFailed, rowCanceled}, x) {
					adminError(w, http.StatusBadRequest, adminBadRequest, "status is ok, retried, failed or canceled")
					return nil, false
				}
			case "account":
				if !accounts[x] {
					adminError(w, http.StatusNotFound, adminNotFound, "no such account")
					return nil, false
				}
			}
			if !slices.Contains(out[k], x) {
				out[k] = append(out[k], x)
			}
		}
		slices.Sort(out[k])
	}
	return out, true
}

// readRowQuery reads r's period, tz, filters, limit and cursor for
// scope's ids, accounts being the accounts the account filter may name.
// now is the clock's time.
func readRowQuery(w http.ResponseWriter, r *http.Request, ids []string, accounts map[string]bool, now time.Time) (rowQuery, bool) {
	v := r.URL.Query()
	bad := func(msg string) (rowQuery, bool) {
		adminError(w, http.StatusBadRequest, adminBadRequest, msg)
		return rowQuery{}, false
	}
	q := rowQuery{limit: requestsLimit}
	var ok bool
	if q.period, q.loc, ok = readPeriodZone(w, v, usage.Today); !ok {
		return rowQuery{}, false
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > requestsLimitMost {
			return bad("limit is 1 to 500")
		}
		q.limit = n
	}
	if q.filters, ok = readFilters(w, v, accounts); !ok {
		return rowQuery{}, false
	}
	h := sha256.New()
	sorted := slices.Sorted(slices.Values(ids))
	json.NewEncoder(h).Encode([]any{sorted, string(q.period), q.loc.String(), q.filters})
	q.hash = hex.EncodeToString(h.Sum(nil))[:16]

	q.asOf = now
	if s := v.Get("cursor"); s != "" {
		c, ok := cursorOf(s)
		if !ok {
			return bad("cursor isn't one a page gave")
		}
		if c.Query != q.hash {
			return bad("cursor is for another query: send the period, tz and filters it was given with, or start again without it")
		}
		asOf := time.Unix(0, c.AsOf)
		if asOf.After(now) {
			return bad("cursor isn't one a page gave")
		}
		q.asOf, q.after = asOf, &c.After
	}
	at := q.asOf.In(q.loc)
	q.since, q.until = q.period.Since(at), q.period.Until(at)
	today := time.Date(now.In(q.loc).Year(), now.In(q.loc).Month(), now.In(q.loc).Day(), 0, 0, 0, 0, q.loc)
	if q.since.Before(today.AddDate(0, 0, -(requestsDays - 1))) {
		return bad("the period reaches back 92 days at most, today included")
	}
	return q, true
}

// scopedAreas are scope's accounts, salts and account names, read once.
type scopedAreas struct {
	areas []area.Area
	accs  [][]adminAccount
	salts [][]byte
	names map[string]string // account id: name
}

// scopeOf reads scope's accounts and salts, answering 503 when an
// area's salt can't be made.
func scopeOf(w http.ResponseWriter, scope []area.Area) (scopedAreas, bool) {
	s := scopedAreas{areas: scope, accs: make([][]adminAccount, len(scope)), salts: make([][]byte, len(scope)), names: map[string]string{}}
	for i, a := range scope {
		s.accs[i] = areaAccounts(a)
		for _, acc := range s.accs[i] {
			if !acc.Contested {
				s.names[acc.ID] = acc.Name
			}
		}
		salt := a.CallerSalt
		if salt == "" && len(s.accs[i]) > 0 {
			var err error
			if salt, err = area.Salt(a.ID); err != nil {
				adminError(w, http.StatusServiceUnavailable, adminAreasUnread, err.Error())
				return s, false
			}
		}
		b, err := hex.DecodeString(salt)
		if err != nil {
			adminError(w, http.StatusServiceUnavailable, adminAreasUnread, "an area's callerSalt in areas.json isn't hex")
			return s, false
		}
		s.salts[i] = b
	}
	return s, true
}

func (s *Server) adminUsageRequests(w http.ResponseWriter, r *http.Request, c adminCall) {
	scope, ok := adminScope(w, r, c)
	if !ok {
		return
	}
	sc, ok := scopeOf(w, scope)
	if !ok {
		return
	}
	ids := make([]string, len(scope))
	for i, a := range scope {
		ids[i] = a.ID
	}
	q, ok := readRowQuery(w, r, ids, scopedAccounts(sc), usage.Clock())
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, requestRows(sc, q, c.who.Admin == access.AdminConsole))
}

// requestRows is the page q asks of sc's areas: from the query's index
// (rowIndexes) when a page of it was read in the last minute, else from
// one read of the period, kept for the pages after it.
func requestRows(sc scopedAreas, q rowQuery, console bool) requestsPage {
	out := requestsPage{Period: string(q.period), Zone: q.loc.String(), Since: q.since, Until: q.asOf.In(q.loc), Rows: []adminRow{}}
	if !q.until.IsZero() && q.until.Before(q.asOf) {
		out.Until = q.until
	}
	key := rowIndexKey(sc, q)
	idx, fresh := rowIndexes.get(key), false
	if idx == nil {
		idx, fresh = buildRowIndex(sc, q), true
		rowIndexes.put(key, idx)
	}
	page, more := idx.pageOf(q)
	recs, ok := pageRecords(sc, q, page)
	if !ok && !fresh {
		// a row the index has is gone from the ledger (rewritten since):
		// read the period again
		rowIndexes.drop(key)
		idx = buildRowIndex(sc, q)
		rowIndexes.put(key, idx)
		page, more = idx.pageOf(q)
		recs, _ = pageRecords(sc, q, page)
	}
	out.Summary, out.Total = idx.summary, idx.total
	if more && len(page) > 0 {
		out.Next = rowCursor{AsOf: q.asOf.UnixNano(), After: page[len(page)-1].key, Query: q.hash}.String()
	}
	faces := newRowFaces()
	cost := usage.NewCoster()
	for i, row := range page {
		r := recs[i]
		if r.Time.IsZero() {
			continue // gone from the ledger as it was read
		}
		c, priced := cost(r)
		ref := idx.refs[row.ref]
		out.Rows = append(out.Rows, adminRowOf(r, sc.areas[row.area].ID, ref, sc.names[ref], sc.salts[row.area], console, c, priced, faces))
	}
	return out
}
