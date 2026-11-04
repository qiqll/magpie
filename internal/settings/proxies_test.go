package settings

import (
	"strings"
	"testing"
)

// T-110: the proxies the user named are kept to pick from: each with a
// name of its own and an address a proxy setting takes, never "" or
// "direct"; a login in one is never said back in an error.
func TestSavedProxiesChecked(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ok := []SavedProxy{{Name: " Home ", URL: " http://me:s3cret@127.0.0.1:7890 "}, {Name: "Office", URL: "socks5://10.0.0.2:1080"}}
	if err := SetProxies(ok); err != nil {
		t.Fatal(err)
	}
	got := Load().Proxies
	if len(got) != 2 || got[0] != (SavedProxy{Name: "Home", URL: "http://me:s3cret@127.0.0.1:7890"}) || got[1].Name != "Office" {
		t.Fatalf("kept %+v", got)
	}
	if ok[0].Name != " Home " {
		t.Fatal("SetProxies trimmed the caller's list in place")
	}
	for _, bad := range [][]SavedProxy{
		{{Name: "", URL: "http://127.0.0.1:1"}},
		{{Name: "a", URL: ""}},
		{{Name: "a", URL: "direct"}},
		{{Name: "a", URL: "ftp://me:s3cret@127.0.0.1:1"}},
		{{Name: "a", URL: "http://127.0.0.1:1"}, {Name: "A", URL: "http://127.0.0.1:2"}},
		{{Name: strings.Repeat("n", 81), URL: "http://127.0.0.1:1"}},
	} {
		err := SetProxies(bad)
		if err == nil {
			t.Fatalf("%+v was kept", bad)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("the error says the login: %v", err)
		}
	}
	if got := Load().Proxies; len(got) != 2 {
		t.Fatalf("a refused list changed what was kept: %+v", got)
	}
	// a whole save keeps them, and an empty list takes them all out
	s := Load()
	s.Theme = "dark"
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxies; len(got) != 2 {
		t.Fatalf("a save dropped them: %+v", got)
	}
	if err := SetProxies(nil); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxies; got != nil {
		t.Fatalf("emptied, kept %+v", got)
	}
}

// Saved proxies are this computer's own, as its proxy is: a sync or a
// restored backup never brings another computer's (or its logins).
func TestSavedProxiesOwn(t *testing.T) {
	from := Settings{Theme: "dark", Proxies: []SavedProxy{{Name: "there", URL: "http://a:b@there:1"}}}
	from.KeepOwn(Settings{Proxies: []SavedProxy{{Name: "here", URL: "http://here:1"}}})
	if len(from.Proxies) != 1 || from.Proxies[0].Name != "here" || from.Theme != "dark" {
		t.Fatalf("KeepOwn: %+v", from)
	}
}
