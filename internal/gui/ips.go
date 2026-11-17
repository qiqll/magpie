package gui

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	areas "github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/provider"
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
	// the areas an IP can be given to, by id and note (T-198); none
	// listed when areas.json can't be read, so nothing is offered
	given := []map[string]string{}
	if all, err := areas.List(); err == nil {
		for _, a := range all {
			given = append(given, map[string]string{"id": a.ID, "note": a.Note})
		}
	}
	return map[string]any{"ips": out, "rev": rev, "days": renewal.Days, "warnDays": renewal.WarnDays, "areas": given}, nil
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
		// an area's IP or proxy the owner removed comes off the area's
		// accounts it was on (T-198)
		if err := provider.SweepAreaProxies(); err != nil {
			log.Printf("ips: the areas' proxies not swept: %v", err)
		}
	}
	out, err := ipsState(time.Now())
	if err != nil {
		fail(rw, err)
		return
	}
	writeJSON(rw, out)
}

// handleIPArea gives the owner's IP to an area, or takes it back
// ({"ip", "area"}, "" for back; T-198, docs/subsystems/areas.md "Proxy
// IPs"): the area's admin then sees it and may put its proxies on the
// area's accounts, never check or change them. Taken back, it comes off
// the area's accounts it was on. Answers what GET /api/ips would.
func handleIPArea(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		IP   string `json:"ip"`
		Area string `json:"area"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		fail(rw, err)
		return
	}
	var err error
	if in.Area == "" {
		err = renewal.TakeBackIP(in.IP)
	} else if _, err = areas.Get(in.Area); err == nil {
		err = renewal.GiveIP(in.IP, in.Area)
	}
	if err != nil {
		fail(rw, err)
		return
	}
	if err := provider.SweepAreaProxies(); err != nil {
		log.Printf("ips: the areas' proxies not swept: %v", err)
	}
	out, err := ipsState(time.Now())
	if err != nil {
		fail(rw, err)
		return
	}
	writeJSON(rw, out)
}
