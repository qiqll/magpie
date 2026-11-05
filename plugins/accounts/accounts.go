// Package accounts is Models › Accounts: every subscription account
// magpie keeps, of every provider, on one page — each one's state, its
// allowances' windows, what it served lately and its proxy — with the
// common things done to an account there: use it or not, pause it, sign
// it in again, read its usage again (需求方 2026-11-04, after CLIProxyAPI's
// credentials page). Adding and removing one stay in the provider's
// editor. Off, the page leaves the window; the accounts work as before.
package accounts

import "github.com/yetone/magpie/internal/pluginrt"

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "accounts", API: "^1", Requires: []string{"window", "providers"}, Essential: true,
		Name: "Accounts", Sub: "Every subscription account on one page: its state, allowances and recent requests, used, paused or signed in again there",
		Parts:  []pluginrt.Part{{Slot: "nav.page", Key: "accounts"}},
		Client: []string{"i18n.js", "accounts.css", "client.js", "page.js"},
	})
}
