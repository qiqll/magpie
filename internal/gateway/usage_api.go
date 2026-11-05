package gateway

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// The usage log, as the Usage page reads it (HoGee_xxl on X: an app of
// their own reading magpie's usage over its gateway, as they can't over
// the GUI's /api/usage): GET /v1/magpie/usage is the page's summary of a
// ?period=, the same JSON as /api/usage, and GET /v1/magpie/usage/requests
// its requests a page at a time, with /api/usage/requests' filters. Both
// are read-only, and what was said in a call is not told here.
//
// They name accounts, keys and sessions, so, as /v1/magpie/quotas, they
// answer this machine, and another only with an enabled gateway key while
// magpie is shared. A key held to a budget, to some models or to some
// accounts is someone else's to use: it is told its own calls alone, from
// /v1/magpie/usage/requests, and not the summary of everyone's.

const usageRefused = "magpie's usage is told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries its API key (Authorization: Bearer <key> or x-api-key: <key>)"

// heldKey: the request's gateway key is held to a budget, models or
// accounts, and so sees only its own usage.
func heldKey(who access.Identity) bool {
	return who.KeyID != "" && (who.Limited() || len(who.Models) > 0 || len(who.Accounts) > 0)
}

// heldRefused is what a held key is told where this magpie's own accounts
// would be: the quotas, their cards and history, a card's refresh and the
// accounts' lanes (321sssrt-bit, #1981: a friend given a key with a
// budget saw every account's card, email and balance on their Remote
// magpie's Usage page). The key's own limit stays at /v1/magpie/limit.
const heldRefused = "this gateway key is held to a budget, models or accounts, so it is told nothing of this magpie's accounts: GET /v1/magpie/limit tells it its own limit"

// heldQuotasOff is what a held key whose owner hasn't turned its quotas on
// is told by the quotas and the status line (scopeQuotas): it names the
// switch that tells it those of what it may use (#2216), as steven_4422
// read the bare refusal as "a held key gets no status line".
const heldQuotasOff = "this gateway key is held to a budget, models or accounts, so it is told nothing of this magpie's accounts until its owner turns on Quotas for it (Gateway → Gateway keys, or magpie gateway-key quotas <id> on); it is then told the accounts it may use of each provider whose every model it may use. GET /v1/magpie/limit tells it its own limit and what holds it"

// refuseHeld answers a held key 403 with msg, marked with
// provider.KeyHeldCode so another magpie shows the key's own card alone
// rather than "isn't shared", and says whether it did.
func refuseHeld(w http.ResponseWriter, r *http.Request, msg string) bool {
	if !heldKey(access.Caller(r.Context())) {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"message": msg, "type": "permission_error", "code": provider.KeyHeldCode}})
	return true
}

// userCalls is, bound by user, which of the usage log's calls a request
// naming its user (UserHeader) is told of in the summary (T-093): its own
// (Record.User, under a key of its group) that an account or key it is on
// now answered (usersSeats): usersOwn.filter, which its requests are
// narrowed to as well (T-098). The summary of everyone's names every
// account, key, user and session; the user is told its own calls on its
// own accounts alone, as it is told their quotas alone (T-090). keep is
// nil, and own too, as usersSeats says: everything, as before. own also
// keys the summary's cache (usage.SummarizeWhere).
func (s *Server) userCalls(w http.ResponseWriter, r *http.Request) (keep func(usage.Record) bool, own *usersOwn, ok bool) {
	own, ok = s.usersSeats(w, r)
	if own == nil || !ok {
		return nil, nil, ok
	}
	o := own.filter()
	return func(rec usage.Record) bool { return o.Keeps(&rec) }, own, true
}

// filter is own as the usage log narrows to it (usage.Own): its user, the
// keys of its group (groupOf: every key that shares its users and the
// calls with none, or its own key alone) and its seats.
func (o *usersOwn) filter() usage.Own {
	keys := []string{}
	if o.group == sharedUsers {
		keys = append(keys, "") // groupOf: a call with no key is the shared group's
		for k, on := range o.sharers {
			if on && k != "" {
				keys = append(keys, k)
			}
		}
	} else {
		keys = append(keys, o.group)
	}
	slices.Sort(keys)
	seats := make([]string, 0, len(o.seats))
	for _, st := range o.seats {
		seats = append(seats, strings.ToLower(st.Who))
	}
	slices.Sort(seats)
	return usage.Own{User: o.user, Keys: strings.Join(keys, "\n"), Seats: strings.Join(seats, "\n")}
}

// ownKey is what own's summary is kept by: who is told it, of which
// seats, and under which keys its name is one user.
func (o *usersOwn) ownKey() string {
	whos := make([]string, 0, len(o.seats))
	for _, st := range o.seats {
		whos = append(whos, strings.ToLower(st.Who))
	}
	slices.Sort(whos)
	var sharers []string
	for k, on := range o.sharers {
		if on {
			sharers = append(sharers, k)
		}
	}
	slices.Sort(sharers)
	return strings.Join([]string{o.group, o.user, strings.Join(whos, ","), strings.Join(sharers, ",")}, "\x00")
}

func (s *Server) usageSummary(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, usageRefused)
		return
	}
	if refuseHeld(w, r, "this gateway key is held to a budget, models or accounts, so it is told its own calls alone: GET /v1/magpie/usage/requests") {
		return
	}
	keep, own, ok := s.userCalls(w, r)
	if !ok {
		return
	}
	var out usageapi.Usage
	if own != nil {
		out = usageapi.StateWhere(usageapi.PeriodOf(r.URL.Query().Get("period")), own.ownKey(), keep)
	} else {
		out = usageapi.State(usageapi.PeriodOf(r.URL.Query().Get("period")))
	}
	if !local(r) {
		out.Path = "" // where the log is on this machine
	}
	writeJSON(w, 200, out)
}

func (s *Server) usageRequests(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, usageRefused)
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	f := usageapi.FilterOf(q)
	// bound by user, a request naming its user is told its own calls on
	// its own seats alone, as its summary (userCalls; T-098), a held key's
	// within the key's own
	own, ok := s.usersSeats(w, r)
	if !ok {
		return
	}
	if own != nil {
		f.Own = own.filter()
	}
	who := access.Caller(r.Context())
	if !heldKey(who) {
		writeJSON(w, 200, withoutOthers(usageapi.LedgerPage(usageapi.PeriodOf(q.Get("period")), f, offset, limit), own))
		return
	}
	f.CallerKey = who.KeyID
	writeJSON(w, 200, withoutAccounts(withoutOthers(usageapi.OwnPage(usageapi.LedgerPage(usageapi.PeriodOf(q.Get("period")), f, offset, limit), who.KeyID), own)))
}

// withoutOthers is a user's own page (usage.Filter.Own) without the other
// computers it made no call on: they are listed whether or not a call
// was, and named by the operator. Unchanged for own nil.
func withoutOthers(l usageapi.Ledger, own *usersOwn) usageapi.Ledger {
	if own == nil {
		return l
	}
	l.Computers = slices.DeleteFunc(l.Computers, func(s usageapi.Share) bool { return s.Calls == 0 && s.ID != usage.ThisComputer })
	return l
}

// withoutAccounts is a held key's own calls with nothing of the accounts
// and keys that answered them: they are the operator's (accounts_hidden.go).
func withoutAccounts(l usageapi.Ledger) usageapi.Ledger {
	l.Accounts = nil
	for i := range l.Rows {
		r := &l.Rows[i]
		r.ProviderAccount, r.ProviderKeyName, r.ProviderKeyID, r.SessionAccount = "", "", "", ""
		if i := strings.LastIndex(r.Host, " as "); i >= 0 {
			r.Host = r.Host[:i] // where it went, not as whom
		}
		r.Error = hideAccounts(r.Error)
	}
	return l
}
