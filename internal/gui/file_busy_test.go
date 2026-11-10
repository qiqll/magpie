package gui

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/filelock"
)

// A change refused while another magpie held providers.json is
// answered with the words the page says in the reader's language (said,
// with the file's name), not only the Go error it was wrapped in.
func TestFailSaysFileBusy(t *testing.T) {
	rec := httptest.NewRecorder()
	fail(rec, fmt.Errorf("set the limit: %w", &filelock.BusyError{Path: "/home/u/.magpie/providers.json.lock", Pid: 42}))
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 400 || out["said"] != busyKey || out["file"] != "providers.json" || !strings.Contains(out["error"], "try again") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b, err := os.ReadFile("assets/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), fmt.Sprintf("%q:", busyKey)); n != 4 {
		t.Fatalf("i18n.js has the busy words in %d languages, want 4 (zh, zh-TW, ja, de)", n)
	}
}
