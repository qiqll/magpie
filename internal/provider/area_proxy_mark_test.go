package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/area"
	"github.com/yetone/magpie/internal/renewal"
)

// setOwnProxy is the owner's own write of the account key's proxy in
// provider pid, as writeOwnProxy writes it: an area's mark on it goes.
func setOwnProxy(pid, key, url string, allow func(cur string, marked bool) error) error {
	return writeOwnProxy(pid, key, url, false, allow)
}

// ownerSets is the owner's Providers editor saving provider pid with the
// account key's own proxy set to v.
func ownerSets(t *testing.T, pid, key, v string) {
	t.Helper()
	p, err := Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	own := map[string]string{}
	for k, x := range p.AccountProxies {
		own[k] = x
	}
	own[key] = v
	p.AccountProxies = own
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
}

// T-224: the owner changes an account's proxy away from the area's and
// back to the same value. It is the owner's value now: the area is told
// it is overridden, can't put its own over it, and neither its detach
// nor its letting go of the account takes it off.
func TestOwnerBackToTheAreasValueStaysTheOwners(t *testing.T) {
	a, _, keys, login := areaProxyHome(t)
	id := renewal.ProxyID(areaProxyA)
	fp := strings.TrimPrefix(keys[0], "relay/")
	for _, ref := range []string{keys[0], keys[1], login} {
		if err := AttachAreaProxy(a.ID, ref, "203.0.113.9", id); err != nil {
			t.Fatal(ref, err)
		}
	}
	// a save of the editor as it was, or of another key's proxy, leaves
	// the area's value the area's
	ownerSets(t, "relay", KeyID("sk-owner-00000001"), ownerProxy)
	if rec := recordOf(a.ID, keys[0]); rec == nil || AreaProxyOverridden("relay", *rec) {
		t.Fatalf("another key's proxy set, the area's is overridden: %+v", rec)
	}
	// the owner: away, then back to the area's own value
	ownerSets(t, "relay", fp, ownerProxy)
	ownerSets(t, "relay", fp, areaProxyA)
	// the account's (Codex isn't listed in a test, so not by its editor)
	for _, v := range []string{ownerProxy, areaProxyA} {
		if err := setOwnProxy("codex", "area@example.com", v, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range []string{keys[0], login} {
		rec := recordOf(a.ID, ref)
		if rec == nil || !AreaProxyOverridden(strings.SplitN(ref, "/", 2)[0], *rec) {
			t.Fatalf("%s: the owner's value back to the area's is not overridden: %+v", ref, rec)
		}
		if err := AttachAreaProxy(a.ID, ref, "203.0.113.9", id); !errors.Is(err, ErrOverridden) {
			t.Fatalf("%s: the area put its proxy over the owner's: %v", ref, err)
		}
	}
	if err := DetachAreaProxy(a.ID, keys[0]); err != nil || keyProxy(t, keys[0]) != areaProxyA {
		t.Fatalf("the area's detach took the owner's value off: %q %v", keyProxy(t, keys[0]), err)
	}
	if err := area.Release(login); err != nil {
		t.Fatal(err)
	}
	if got := ProxyOfLogin("codex", "Area@Example.com"); got != areaProxyA {
		t.Fatalf("let go of, the account's owner value went: %q", got)
	}
	// the other key, left alone by the owner, is still the area's
	if rec := recordOf(a.ID, keys[1]); rec == nil || AreaProxyOverridden("relay", *rec) {
		t.Fatalf("the key the owner left: %+v", rec)
	}
	if err := DetachAreaProxy(a.ID, keys[1]); err != nil || keyProxy(t, keys[1]) != "" {
		t.Fatalf("the area's own value not taken off: %q %v", keyProxy(t, keys[1]), err)
	}
}

// A direct likewise: the owner's "direct" set after another value is
// the owner's, though it is what the area wrote.
func TestOwnerBackToTheAreasDirectStaysTheOwners(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	fp := strings.TrimPrefix(keys[0], "relay/")
	if err := AttachAreaDirect(a.ID, keys[0]); err != nil {
		t.Fatal(err)
	}
	ownerSets(t, "relay", fp, ownerProxy)
	ownerSets(t, "relay", fp, "direct")
	if rec := recordOf(a.ID, keys[0]); rec == nil || !AreaProxyOverridden("relay", *rec) {
		t.Fatalf("the owner's direct is not overridden: %+v", rec)
	}
	if err := area.Release(keys[0]); err != nil {
		t.Fatal(err)
	}
	if got := keyProxy(t, keys[0]); got != "direct" {
		t.Fatalf("let go of, the owner's direct went: %q", got)
	}
}

// The owner picking the area's own proxy for the account at a sign-in
// makes it the owner's, though the value is the same.
func TestOwnerSignInPickOfTheAreasValueIsTheOwners(t *testing.T) {
	a, _, _, login := areaProxyHome(t)
	if err := AttachAreaProxy(a.ID, login, "203.0.113.9", renewal.ProxyID(areaProxyA)); err != nil {
		t.Fatal(err)
	}
	// a second account, so the pick stays the account's own
	if _, _, err := addLoginOf(codexTestLogin(t, "Owner@Example.com"), true); err != nil {
		t.Fatal(err)
	}
	if err := keepSignInProxy("codex", "Area@Example.com", areaProxyA); err != nil {
		t.Fatal(err)
	}
	rec := recordOf(a.ID, login)
	if rec == nil || !AreaProxyOverridden("codex", *rec) {
		t.Fatalf("the owner's pick is not overridden: %+v", rec)
	}
	if err := area.Release(login); err != nil {
		t.Fatal(err)
	}
	if got := ProxyOfLogin("codex", "Area@Example.com"); got != areaProxyA {
		t.Fatalf("let go of, the owner's pick went: %q", got)
	}
}

// A record made before the mark (T-198, T-213) is judged by its value,
// as it was: an area's account attached then is still taken off when the
// area lets it go, and a new attach marks it.
func TestAreaRecordBeforeTheMark(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	fp := strings.TrimPrefix(keys[0], "relay/")
	if err := setOwnProxy("relay", fp, areaProxyA, nil); err != nil {
		t.Fatal(err)
	}
	old := area.AccountProxy{IP: "203.0.113.9", ID: renewal.ProxyID(areaProxyA), Key: fp}
	if err := area.SetProxy(a.ID, keys[0], &old); err != nil {
		t.Fatal(err)
	}
	if AreaProxyOverridden("relay", old) {
		t.Fatal("a record of before the mark is overridden")
	}
	if err := AttachAreaProxy(a.ID, keys[0], "203.0.113.9", old.ID); err != nil {
		t.Fatal(err)
	}
	if rec := recordOf(a.ID, keys[0]); rec == nil || !rec.Marked {
		t.Fatalf("attached again, not marked: %+v", rec)
	}
	if err := area.SetProxy(a.ID, keys[1], &area.AccountProxy{IP: old.IP, ID: old.ID, Key: strings.TrimPrefix(keys[1], "relay/")}); err != nil {
		t.Fatal(err)
	}
	if err := setOwnProxy("relay", strings.TrimPrefix(keys[1], "relay/"), areaProxyA, nil); err != nil {
		t.Fatal(err)
	}
	if err := area.Release(keys[1]); err != nil {
		t.Fatal(err)
	}
	if got := keyProxy(t, keys[1]); got != "" {
		t.Fatalf("a record of before the mark, let go of, still goes through %q", got)
	}
}

// The window's editor sends a provider with no AreaProxies (it is
// magpie's, not the form's): a save of it, another key's proxy changed,
// leaves the area's value the area's, its mark kept from the record.
func TestEditorSaveKeepsTheAreasMark(t *testing.T) {
	a, _, keys, _ := areaProxyHome(t)
	if err := AttachAreaProxy(a.ID, keys[0], "203.0.113.9", renewal.ProxyID(areaProxyA)); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	in := p.clone()
	in.AreaProxies = nil // as the GUI's save decodes it
	in.AccountProxies[KeyID("sk-owner-00000001")] = ownerProxy
	if err := Save(in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Edit("relay", func(q *Provider) error { q.AreaProxies = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	rec := recordOf(a.ID, keys[0])
	if rec == nil || AreaProxyOverridden("relay", *rec) {
		t.Fatalf("an editor save made the area's value the owner's: %+v", rec)
	}
	if err := DetachAreaProxy(a.ID, keys[0]); err != nil || keyProxy(t, keys[0]) != "" {
		t.Fatalf("the area's own value not taken off: %q %v", keyProxy(t, keys[0]), err)
	}
}

// Copilot's account renamed onto a name whose own proxy the owner set
// (moveAccountSettings): the area's mark stays where its value stays,
// never on the owner's value, though it is the same.
func TestCopilotRenameMovesTheMarkWithItsValueAlone(t *testing.T) {
	areaProxyHome(t)
	if err := setAreaProxy("copilot", "old", areaProxyA, nil); err != nil {
		t.Fatal(err)
	}
	if err := setOwnProxy("copilot", "new", areaProxyA, nil); err != nil {
		t.Fatal(err)
	}
	if err := moveAccountSettings("copilot", map[string]string{"old": "new"}); err != nil {
		t.Fatal(err)
	}
	p, _ := storedPicks("copilot")
	if _, marked := accountOwnProxy(p, "new"); marked {
		t.Fatalf("the mark moved onto the owner's value: %v", p.AreaProxies)
	}
	if _, marked := accountOwnProxy(p, "old"); !marked {
		t.Fatalf("the area's value lost its mark: %v", p.AreaProxies)
	}
}
