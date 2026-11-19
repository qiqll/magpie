package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/yetone/magpie/internal/access"
	areas "github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// The Gateway page's Areas (plugins/gateway/areas.js,
// docs/subsystems/areas.md): the owner makes areas, gives each the
// providers and subscriptions its admin may add and the accounts it owns,
// hands out its admin key, and makes the console's key. What an area's
// admin does is the gateway's admin routes'.

type areasState struct {
	Areas      []any       `json:"areas"`
	ConsoleKey *access.Key `json:"consoleKey"`
	Unowned    []any       `json:"unowned"`
	Catalog    any         `json:"catalog"`
	AllowIPs   []string    `json:"adminAllowIPs"`
	AllowEnv   string      `json:"adminAllowIPsEnv,omitempty"`
}

func readAreas() (areasState, error) {
	all, err := areas.List()
	if err != nil {
		return areasState{}, err
	}
	st := areasState{Areas: gateway.AreaViews(all), Unowned: gateway.UnownedAccounts(), Catalog: gateway.AreaCatalog()}
	keys, err := access.List()
	if err != nil {
		return areasState{}, err
	}
	if i := slices.IndexFunc(keys, func(k access.Key) bool { return k.Admin == access.AdminConsole }); i >= 0 {
		st.ConsoleKey = &keys[i]
	}
	var env bool
	st.AllowIPs, env = gateway.AdminAllowIPs()
	if env {
		st.AllowEnv = gateway.AdminAllowIPsEnv
	}
	if st.AllowIPs == nil {
		st.AllowIPs = []string{}
	}
	return st, nil
}

func areaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/areas", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		st, err := readAreas()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("POST /api/areas/{action}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID, Note, Ref      string
			Off                *bool
			Providers, SignIns *[]string
			IPs                []string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		secret, err := areaAction(r, r.PathValue("action"), in.ID, in.Note, in.Ref, in.Off, in.Providers, in.SignIns, in.IPs)
		if err != nil {
			fail(w, err)
			return
		}
		st, err := readAreas()
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"state": st, "secret": secret})
	})
}

// cancelConsoleSignIns cancels the console's sign-ins once its key is
// rotated, turned off or removed (T-261); a test counts the calls.
var cancelConsoleSignIns = gateway.CancelConsoleSignIns

func areaAction(r *http.Request, action, id, note, ref string, off *bool, providers, signIns *[]string, ips []string) (string, error) {
	areaKey := func() (access.Key, error) {
		k, ok, err := access.AreaKey(id)
		if err == nil && !ok {
			err = errors.New("this area has no admin key: rotate makes one")
		}
		return k, err
	}
	consoleKey := func() (access.Key, error) {
		keys, err := access.List()
		if err != nil {
			return access.Key{}, err
		}
		if i := slices.IndexFunc(keys, func(k access.Key) bool { return k.Admin == access.AdminConsole }); i >= 0 {
			return keys[i], nil
		}
		return access.Key{}, errors.New("there is no console key")
	}
	switch action {
	case "create":
		var ps, ss []string
		if providers != nil {
			ps = *providers
		}
		if signIns != nil {
			ss = *signIns
		}
		a, err := areas.Create(note, ps, ss)
		if err != nil {
			return "", err
		}
		_, secret, err := access.AddAdmin(access.AdminArea, a.ID, a.Note+" admin")
		if err != nil {
			areas.Delete(a.ID)
			return "", err
		}
		return secret, nil
	case "update":
		s := areas.Spec{Off: off, Providers: providers, SignIns: signIns}
		if note != "" {
			s.Note = &note
		}
		a, err := areas.Update(id, s)
		if err == nil && a.Off {
			gateway.CancelAreaKeySignIns(id)
		}
		return "", err
	case "delete":
		if err := areas.Delete(id); err != nil {
			return "", err
		}
		gateway.CancelAreaSignIns(id)
		return "", access.RemoveArea(id)
	case "rotate":
		a, err := areas.Get(id)
		if err != nil {
			return "", err
		}
		return gateway.RotateAreaKey(a)
	case "copy":
		k, err := areaKey()
		if err != nil {
			return "", err
		}
		return access.Update("copy-key", access.Change{Key: k.ID})
	case "claim":
		return "", areas.Claim(id, ref)
	case "release":
		return "", areas.Release(ref)
	case "console-add":
		_, secret, err := access.AddAdmin(access.AdminConsole, "", "Console")
		return secret, err
	case "console-rotate", "console-copy", "console-remove", "console-on", "console-off":
		k, err := consoleKey()
		if err != nil {
			return "", err
		}
		a := map[string]string{"console-rotate": "rotate-key", "console-copy": "copy-key", "console-remove": "remove-key", "console-on": "on-key", "console-off": "off-key"}[action]
		secret, err := access.Update(a, access.Change{Key: k.ID})
		if err == nil && (action == "console-rotate" || action == "console-remove" || action == "console-off") {
			// the old secret's sign-ins add no account (T-261)
			cancelConsoleSignIns()
		}
		return secret, err
	case "allow-ips":
		if _, env := gateway.AdminAllowIPs(); env {
			return "", fmt.Errorf("%s sets the admin allow list; change it there", gateway.AdminAllowIPsEnv)
		}
		list, err := settings.CleanAllowIPs(ips)
		if err != nil {
			return "", err
		}
		s, err := settings.LoadChecked()
		if err != nil {
			return "", err
		}
		s.AdminAllowIPs = list
		return "", settings.Save(s)
	}
	return "", fmt.Errorf("unknown area action %q", action)
}
