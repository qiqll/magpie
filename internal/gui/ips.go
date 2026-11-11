package gui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/renewal"
)

// Models › Proxy IPs (T-155): the proxy exit IPs the user pays for by the
// month, each with when its period was opened and, worked out now by the
// same renewal.Of as Models › Accounts, when it ends and whether it is
// due. GET reads the list and its revision; POST keeps the whole list
// sent, made from that revision (renewal.SetIPs refuses one made from an
// older read), and answers what GET would.

// ipJSON is an IP as the page is told it: the kept record and its period
// now, none when no opening time is kept.
type ipJSON struct {
	renewal.IP
	Renewal *renewal.State `json:"renewal,omitempty"`
}

func ipsState(now time.Time) (map[string]any, error) {
	ips, rev, err := renewal.IPs()
	if err != nil {
		return nil, err
	}
	out := make([]ipJSON, 0, len(ips))
	for _, x := range ips {
		out = append(out, ipJSON{IP: x, Renewal: renewal.Of(x.Opened, now)})
	}
	return map[string]any{"ips": out, "rev": rev, "days": renewal.Days, "warnDays": renewal.WarnDays}, nil
}

func handleIPs(rw http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		// both asked: a body without the list is no list to keep
		// (T-155 review L1), and one without the revision was made from
		// no read
		var in struct {
			IPs *[]renewal.IP `json:"ips"`
			Rev *string       `json:"rev"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.IPs == nil || in.Rev == nil {
			fail(rw, errors.New("the IPs and the revision they were read at are both needed"))
			return
		}
		if err := renewal.SetIPs(*in.IPs, *in.Rev, time.Now()); err != nil {
			fail(rw, err)
			return
		}
	}
	out, err := ipsState(time.Now())
	if err != nil {
		fail(rw, err)
		return
	}
	writeJSON(rw, out)
}
