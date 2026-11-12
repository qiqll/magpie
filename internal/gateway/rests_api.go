package gateway

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// RestsPath tells what rests now in the process serving the gateway, for
// a console on this computer that doesn't serve it: rests are that
// process's alone, so the Gateway page's User bindings could tell none of
// them from another. It names accounts and keys, so, as
// /v1/magpie/concurrency, it answers this machine, another only with an
// enabled gateway key while magpie is shared, and never a held key.
// Bound by user, a request is told the rests of the accounts and keys its
// user is on alone (usersSeats; DefaultUser's for one with a gateway key
// naming none, T-163), as /v1/magpie/concurrency tells their lanes
// (T-166, ownRest): a rest's id names its account. One without a key, the Gateway
// page's OwnerRests, is told every rest.
const RestsPath = "/v1/magpie/rests"

func (s *Server) rests(w http.ResponseWriter, r *http.Request) {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, "magpie's rests are told to another machine only when magpie is shared on the local network and the request carries its API key")
		return
	}
	if refuseHeld(w, r, heldRefused) {
		return
	}
	own, ok := s.usersSeats(w, r)
	if !ok {
		return
	}
	rests := Rests()
	if own != nil {
		for id := range rests {
			if !ownRest(own.seats, id) {
				delete(rests, id)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"object": "rests", "data": rests})
}

// ownRest says the rest id (Rests) is of an account or key on seats: what
// it rests by, without the model one rests alone for, is a seat's who
// (candidate.who). A provider's one key, or its own account, rests by the
// provider's id alone (keyCandidate, accountCandidate), though its seat
// is id#key: a rest by the bare id is told to a user on a seat of that
// provider, which names no account.
func ownRest(seats []userSeat, id string) bool {
	who, _, _ := strings.Cut(id, "/")
	if strings.ContainsAny(who, "#@") {
		return seatIn(seats, who)
	}
	return slices.ContainsFunc(seats, func(st userSeat) bool { return seatOf(st, who) })
}

// Rests is what rests now in this process, by what it rests by: a
// provider's id, id#key or id@account, or one of those then /model for
// one model alone.
func Rests() map[string]Rest {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	now := time.Now()
	out := map[string]Rest{}
	for id, until := range restingUntil.m {
		if now.Before(until) {
			r := restingUntil.note[id]
			r.Until = until
			out[id] = r
		}
	}
	return out
}

// OwnerRests reads Rests of the magpie serving the gateway's address, for
// a console that doesn't serve it. ok is false when nothing answers there,
// or a magpie too old to tell them, or one that refuses: unknown, never
// nothing resting. It waits no longer than OwnerLanes does.
func OwnerRests() (map[string]Rest, bool) {
	base := ownerURL()
	if base == "" {
		return nil, false
	}
	c := &http.Client{Timeout: 700 * time.Millisecond}
	res, err := c.Get(base + RestsPath)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, false
	}
	var out struct {
		Object string          `json:"object"`
		Data   map[string]Rest `json:"data"`
	}
	if decodeJSON(b, &out) != nil || out.Object != "rests" || out.Data == nil {
		return nil, false
	}
	return out.Data, true
}
