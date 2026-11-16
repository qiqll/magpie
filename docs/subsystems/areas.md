# Areas and admin keys

An **area** is a note the owner hands out (学习, 工作, 娱乐…) with the provider
accounts it owns. Its **area admin** adds, lists and removes those accounts
and reads what they spent, through the gateway's admin routes, from a
separate console ([magpie-console](../../../magpie-console/README.md), whose
`docs/magpie-api.md` is the contract both sides keep). The **console key**
reads every area and makes, changes and deletes them.

An area decides who may manage an account and see what it spent, **never
where a request goes**: routing reads no area, and an area's accounts serve
the owner's traffic as any other account does. Traffic stays the owner's
to route.

**What that means for trust.** An account an area adds serves the owner's
requests and every gateway key's, so whoever holds that vendor account sees
them where its vendor shows them: OpenAI's request logs (Responses store
them by default), a relay's own logs. An area's admin can also remove its
account, or add one that fails, and the requests it served go elsewhere.
Give an area to someone you would trust with the traffic its accounts can
take, or keep sensitive work off providers an area can add to.

## Source of truth

| What | Where |
| --- | --- |
| Areas, their accounts, what each may add | `areas.json` beside `settings.json` ([`internal/area`](../../internal/area/area.go)) |
| Admin keys | `caller-keys.json`, `Key.Admin` and `Key.Area` ([`internal/access/admin.go`](../../internal/access/admin.go)) |
| Admin routes | [`internal/gateway/admin.go`](../../internal/gateway/admin.go), [`admin_signin.go`](../../internal/gateway/admin_signin.go) |
| Area sign-ins | [`internal/provider/signin_area.go`](../../internal/provider/signin_area.go) |
| Admin allow list | `Settings.AdminAllowIPs`, or `MAGPIE_ADMIN_ALLOW_IPS` (`gateway.AdminAllowIPs`) |

An account is named as a gateway key's account list names it:
`<provider>/<login id>` for a signed-in account, `<provider>/<key
fingerprint>` for a key. A built-in subscription's provider id is its
agent's (`codex`, `claude`, `copilot`, `gemini`, `antigravity`).

## areas.json

- Changed under a file lock (`areas.json.lock`), read afresh inside it,
  written whole through a temp file renamed over it (`area.change`).
- An account belongs to one area at most: `area.Claim` checks inside the
  lock. A ref two areas hold (written by hand) is neither's (`Owner`'s
  `contested`): no admin manages it until the owner settles it.
- A file that can't be read or parsed keeps the last good read; with none
  since magpie started it is `ErrUnreadable` (the admin routes answer 503),
  never "no areas", and it is never written over.
- An area that owns accounts can't be deleted (`ErrNotEmpty`): its admin
  removes them, or the owner takes them back.
- Accounts already in magpie belong to no area. What an area's account
  spent counts from `Account.Added`, when the area got it.

## Admin keys

- `Admin` is `area` (with `Area`) or `console`. The console has one key at
  most. Both are made by magpie (`sk-magpie-key-…`); the area's is made with
  the area, by the console's `POST /areas` or the owner's window, and told
  only then and on a rotation.
- On disk an admin key is also `ReadOnly`, held to the account `-/-` and to
  no MCP server (`adminHeld`). A magpie from before admin keys reads it as
  any other key: the stats routes alone, of which a key held to accounts it
  has none of reads nothing but its own name and limits (`/v1/magpie/limit`,
  `/v1/magpie/me`) and the gateway's `/` (its routes and how many models it
  has); quotas, usage, route events, statusline, every model route and MCP
  are refused (tried on 373eaeeb8, from this computer and the LAN, T-182).
- Such a magpie writing `caller-keys.json` (any key added, renamed or
  rotated) drops `Admin` and `Area`: each admin key is then an ordinary
  read-only key held to `-/-`, here too. Its console is refused every admin
  route (`403 gateway_key_read_only`) and reads nothing else; the area has
  no admin key until the owner rotates it (Gateway → Areas, which makes a
  new one), and the console's key is made again. The old ones stay in
  Gateway keys, where the owner removes them.
- `access.Update` does only `rename`, `rotate` (with no value of the user's
  own), `on`/`off`, `remove`, `move` and `copy` to an admin key.
- `identityOf` gives an admin key's caller `Admin`, `Area`, `ReadOnly`, the
  held account and its models (`-/*`), and nothing else.

## The guard

1. `readOnlyGuard` refuses an admin key every route but the admin ones
   (`adminRoutes`, by the pattern the request is routed to), before
   anything is read or sent: `403 gateway_key_admin_only`. It is judged
   before `ReadOnly`, which an admin key is on disk too.
2. Each admin handler goes through `Server.admin`, which judges again and
   never by where the request came from (`lanGuard` lets this computer's
   requests through with no key, or a wrong one, as the owner's):
   - the connection's address (never a forwarding header) is this
     computer's, or on the admin allow list: else `403
     admin_ip_not_allowed`. An empty list is this computer alone. A request
     that reaches loopback through a proxy or tunnel here (a forwarding
     header) is refused, or judged by the address the proxy names when
     `MAGPIE_TRUST_PROXY=1`. One through magpie web's page port is refused
     either way: a raw forward to it brings whatever header its client
     wrote. A raw TCP forward to the gateway's own port adds no header and
     still reads as this computer; the admin key is then all that guards.
   - an enabled admin key (`access.Current`): else `401
     admin_key_required`, from loopback too;
   - the route's role: else `403 admin_console_only` / `admin_area_only`;
   - an area key's area is there and on: else `401` (deleted or off), or
     `503 areas_unreadable`.
3. An area's request never names its area: it comes from the key. What
   isn't the area's is `404 not_found`, whether or not it exists.

## Routes

`/v1/magpie/admin/*`. Area: `me`, `options`, `accounts`, `accounts/key`,
`accounts/remove`, `usage`, `signin`, `signin/{id}`, `signin/{id}/callback`,
`signin/{id}/cancel`, `signin/claude-token`. Console: `me`, `catalog`,
`areas`, `areas/{id}`, `areas/{id}/accounts`, `areas/{id}/usage`,
`areas/{id}/update`, `areas/{id}/rotate-key`, `areas/{id}/delete`. Request
and reply bodies are in magpie-console's `docs/magpie-api.md`.

- Adding a key claims it first and adds it after (a failed add releases
  it), so it is the area's from the moment it is in magpie. A key magpie
  has already is `409` and names no owner.
- Removing goes through `gateway.Reseat` (`agent.Reseat` sets it), as the
  window's removal does, then releases the account. An account the owner
  already removed is only released.
- Usage visits the gateway's records since the period's start
  (`usage.Visit`) and keeps those an area account answered from its
  `Added` on: a key's by `ProviderKeyID`, an account's by `Record.Account()`
  under its provider. An area with no account spent nothing; it is never
  given the whole ledger.

## Area sign-ins

An area admin signs in from a browser that isn't on this computer, so each
way finishes with nothing coming back to this computer's ports: Codex by
device code or the address pasted, Copilot by device code, Claude by a
setup-token or `claude auth login`'s code pasted, Gemini and Antigravity
by the address pasted. A subscription a plugin serves is offered to no
area yet, nor are Sign in with ChatGPT and file imports.

- `SignInOptions.Area`: the sign-in starts only while none of its agent is
  under way (`reserveSignIn`, held as pending until listed); while it runs
  every other start to that agent, the owner's included, is
  `ErrSignInBusy` (`409`) instead of replacing it. The owner's still
  replace the owner's.
- The agent on this computer is never signed in to an area's account
  (`addLoginOf(…, magpieOnly)`), unless it was signed in to that account
  already.
- `SignInOptions.Done` claims the account as the sign-in is done, whether
  or not the console asks again. One magpie had before (`Again`, the
  owner's or another area's) stays whose it was and reads `conflict`;
  its tokens were renewed by the sign-in, which hands no account across.
- Each sign-in is read, pasted into and cancelled by the area that started
  it alone (`areaSignIns`). One that ended, or that the owner's replaced
  (canceled), is still read for 10 minutes (`areaSignInKept`), so its
  console learns how it ended when another started as it ended.

## The owner's window

Gateway → Areas ([`plugins/gateway/areas.js`](../../internal/gui/assets/plugins/gateway/areas.js),
[`internal/gui/areas.go`](../../internal/gui/areas.go), `/api/areas`): make,
rename, turn off and delete an area, pick the providers and subscriptions its
admin may add, give it an account no area owns or take one back (the account
stays in magpie), copy or rotate its admin key, make, copy, rotate or remove
the console's key, and set the admin allow list. A new key's value is copied
as it is made. The window's Gateway keys list leaves admin keys out
(`gatewayOnly`); they are kept last in the store, so `move-key`'s index is
the same in both.

## Accounts removed elsewhere

An area's account removed by anyone (the window, `magpie accounts`, its
admin) is released at the removal's choke point once it went through:
`provider.RemoveKey`, `RemoveKeys`, `ForgetLogin` (`releaseArea`). A key
replaced in place (`ReplaceKey`) moves the area's ownership to its new
fingerprint. One the area couldn't be told of lists as `gone`.

## Verification

`go test -race ./internal/area`; `go test ./internal/gateway -run
'TestAdmin|TestReadOnlyKeyEveryRoute|TestKeySlotEveryRoute'`; `go test
./internal/provider -run 'TestAreaSignIn|TestAreaLetsGo'`; `go test ./internal/gui -run
TestAreasRoutes`. `TestAdminKeyEveryRoute` fails
when a route is under `/v1/magpie/admin` and in no role's list, or when an
admin key reaches any other route or an upstream.
