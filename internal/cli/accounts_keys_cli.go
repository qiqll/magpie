package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/yetone/magpie/internal/gui"
)

// accountsKeysRead is the gateway keys with the accounts each may be
// served by, as Models › Accounts' key filter reads them
// (GET /api/accounts/keys); tests replace it.
var accountsKeysRead = gui.AccountsKeys

// accountsKeysTo is `magpie accounts keys [--json]`: each gateway key that
// works now and the subscription accounts a request sending it may be
// served by, "all" for a key held to none. Any other word is refused.
func accountsKeysTo(out io.Writer, args []string) error {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			return fmt.Errorf("%q: magpie accounts keys takes only --json\nusage: magpie accounts keys [--json]", a)
		}
		asJSON = true
	}
	keys, err := accountsKeysRead()
	if err != nil {
		return err
	}
	if asJSON {
		b, err := json.MarshalIndent(map[string]any{"keys": keys}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(b))
		return nil
	}
	if len(keys) == 0 {
		// none, or each one off or expired: either is mended from the list
		fmt.Fprintln(out, muted.Render("no gateway key works now ·"), "see them: magpie gateway-key list · add one: magpie gateway-key add <name>")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tACCOUNTS")
	for _, k := range keys {
		accounts := "all"
		switch {
		case !k.All && len(k.Accounts) == 0:
			accounts = "none"
		case !k.All:
			accounts = strings.Join(k.Accounts, ", ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", k.ID, k.Name, accounts)
	}
	return w.Flush()
}
