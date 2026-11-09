package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

const usersUsage = "usage: magpie users held [--json] | magpie users release <user|key|session|kind:who> | magpie users hours [<0-168>|default]"

// usersCmd: `magpie users held [--json]`, `magpie users release <who>`: the
// callers held after Anthropic answered one of their requests
// authentication_failed (gateway/auth_hold.go), and letting one go.
func usersCmd(args []string) error { return usersTo(os.Stdout, args[1:]) }

// usersHeldRead and usersRelease are the gateway's; tests replace them.
var (
	usersHeldRead     = gateway.AuthHolds
	usersRelease      = gateway.ReleaseAuthHold
	usersSaveSettings = settings.Save
)

func usersTo(out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usersUsage)
	}
	switch args[0] {
	case "held":
		asJSON := false
		for _, a := range args[1:] {
			if a != "--json" {
				return fmt.Errorf("%q: magpie users held takes only --json\n%s", a, usersUsage)
			}
			asJSON = true
		}
		holds, err := usersHeldRead()
		if err != nil {
			return err
		}
		if asJSON {
			b, err := json.MarshalIndent(map[string]any{"holds": holds}, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(out, string(b))
			return nil
		}
		if len(holds) == 0 {
			fmt.Fprintln(out, muted.Render("nobody is held ·"), "a caller is held", settings.Shared().AuthHold(), "after Anthropic answers one of their requests authentication_failed")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "HELD\tUNTIL\tAFTER")
		for _, h := range holds {
			who := h.Kind + ":" + h.Who
			if h.Name != "" {
				who += " (" + h.Name + ")"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", who, h.Until.Local().Format("2006-01-02 15:04"), h.Provider+"@"+h.Account+" at "+h.At.Local().Format(time.Kitchen))
		}
		return w.Flush()
	case "release":
		if len(args) != 2 || args[1] == "" || args[1][0] == '-' {
			return fmt.Errorf("%s", usersUsage)
		}
		if err := usersRelease(args[1]); err != nil {
			return err
		}
		fmt.Fprintln(out, green.Render("✓"), args[1], "is no longer held")
		return nil
	case "hours":
		// how long a caller is held after authentication_failed: alone,
		// what it is; a number of hours, 0 for nobody; default for 12
		if len(args) == 1 {
			fmt.Fprintln(out, "a caller is held", settings.Shared().AuthHold(), "after Anthropic answers one of their requests authentication_failed")
			return nil
		}
		if len(args) != 2 {
			return fmt.Errorf("%s", usersUsage)
		}
		var hours *int
		if args[1] != "default" {
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 0 || n > settings.AuthHoldMost {
				return fmt.Errorf("%q: a number of hours from 0 to %d, or default\n%s", args[1], settings.AuthHoldMost, usersUsage)
			}
			hours = &n
		}
		s, err := settings.LoadChecked()
		if err != nil {
			return err
		}
		s.AuthHoldHours = hours
		if err := usersSaveSettings(s); err != nil {
			return err
		}
		fmt.Fprintln(out, green.Render("✓"), "a caller is now held", s.AuthHold(), "after authentication_failed")
		return nil
	}
	return fmt.Errorf("%q: magpie users takes held, release or hours\n%s", args[0], usersUsage)
}
