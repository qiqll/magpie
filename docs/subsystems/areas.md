# Areas and admin keys

An **area** is a note the owner hands out (学习, 工作, 娱乐…) with the provider
accounts it owns. Its **area admin** adds, lists and removes those accounts
and reads what they spent, through the gateway's admin routes, from a
separate console ([magpie-console](../../../magpie-console/README.md), whose
`docs/magpie-api.md` is the contract both sides keep, and whose
[`docs/operator.md`](../../../magpie-console/docs/operator.md) sets both up on
a Linux server from zero). The **console key** reads every area and makes,
changes and deletes them.

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
`accounts/remove`, `usage`, `usage/requests`, `usage/stats`, `signin`, `signin/{id}`, `signin/{id}/callback`,
`signin/{id}/cancel`, `signin/claude-token`, `ips`, `ips/update`,
`ips/delete`, `ips/check`, `accounts/proxy`. Console: `me`, `catalog`,
`areas`, `areas/{id}`, `areas/{id}/accounts`, `areas/{id}/usage`,
`usage/requests`, `areas/{id}/usage/requests`, `usage/stats`,
`areas/{id}/usage/stats`,
`areas/{id}/update`, `areas/{id}/rotate-key`, `areas/{id}/delete`, `ips`,
`areas/{id}/ips`. Request
and reply bodies are in magpie-console's `docs/magpie-api.md`.

- Adding a key claims it first and adds it after (a failed add releases
  it), so it is the area's from the moment it is in magpie. A key magpie
  has already is `409` and names no owner.
- That `409` would let an area admin test whether some key or account is in
  magpie, so each area's `409`s on `accounts/key` and
  `signin/claude-token` are counted (`conflictProbes`, admin_probe.go):
  after 20 in 10 minutes both routes answer that area `429
  too_many_conflicts` with a `Retry-After`, whatever it sends, until the
  oldest leaves the window. A held request is not run. The 20th is logged
  with the area's id and the count, never the key. Other areas are not
  held. The count is in memory: a restart clears it.
- Removing goes through `gateway.Reseat` (`agent.Reseat` sets it), as the
  window's removal does, then releases the account. An account the owner
  already removed is only released.
- Usage visits the gateway's records since the period's start
  (`usage.Visit`) and keeps those an area account answered from its
  `Added` on: a key's by `ProviderKeyID`, an account's by `Record.Account()`
  under its provider. An area with no account spent nothing; it is never
  given the whole ledger. The areas list (the console's `areas`, the
  window's `/api/areas`) gives every area its `today` from one visit
  (`areasToday`), by the same rules; `TestAdminAreasTodayIsEachAreasUsage`
  holds it to each area's own usage. Usage, the areas list and the
  request rows count the gateway's own records alone (`areaRecord`): not
  one refused before any account was asked (`IsRejected`), nor one a
  client reported (`IsReported`), which went straight to its vendor.
  `failed` counts a request the agent got an error for (`LostRequest`): a
  try that failed and handed the request on to another account is
  neither a request nor a failure.

## Request rows

`GET /usage/requests` (area key: its own area; console key: every area,
`?area=` narrows) and the console's `GET /areas/{id}/usage/requests`
([`admin_requests.go`](../../internal/gateway/admin_requests.go)).

- **Scope from the key alone** (`adminScope`): an area key is its own
  area, and `?area=`, its own id too, is `400`. The console's "every
  area" is every area in `areas.json`, never the owner's own accounts,
  which are in none. Every admin read of usage takes its areas from
  `adminScope`, so the stats routes (T-197) do too.
- **Rows from the area's accounts alone** (`areaRows`): the same
  matching as usage, from each account's `Added`, over `usage.VisitWhile`
  (never `usageapi.LedgerPage`, which brings in agents' session files,
  other computers and whole-ledger lists). An area with no account
  visits nothing. An `account=` filter naming an account outside the
  scope is `404`.
- **An allow-list of fields** (`adminRow`, filled by `adminRowOf` field
  by field). Never told, to an area or the console: the client's
  address, `Host`, the alias asked for (`req`), session ids and
  subagents, `X-Magpie-User`, other computers, the archive, and the
  vendor's error text (`err`, which often echoes the request): a failure
  is its `errorType` and `httpStatus`. Every JSON field of `usage.Record`
  and `usage.Row` is in `adminRowAllowed` or `adminRowDenied`;
  `TestAdminRowFieldsClassified` fails on a field in neither.
- **Caller keys** reach an area as `callerKey`, `ck_` and the first 8 hex
  of HMAC-SHA256 over the gateway key id, keyed by the area's own
  `callerSalt` (32 random bytes in `areas.json`, made with the area, or
  on first read for an area made before; never told). It is stable
  within the area and differs across areas. A call with no key is
  `ck_none`. The console also gets `callerKeyName`.
- **Paging by cursor**: the first page fixes `asOf`, and calls made after
  it are on no page. Rows go newest first by time, route id and their
  order in the log; the cursor (base64url JSON, unsigned) holds `asOf`,
  the last row and a hash of the scope, period, zone and filters. A
  cursor for another query is `400`. It only narrows: the scope is read
  from the key again on every page.
- **Index cache** ([`admin_requests_cache.go`](../../internal/gateway/admin_requests_cache.go),
  T-201): a query's first page reads the period once and keeps the rows
  it matched, by key alone (~32 bytes a row), with `summary` and
  `total`, for 60 s, 8 queries and 3M rows at most (least recently used
  out first). A later page takes its rows there and reads the log from
  its own oldest row on, for those rows' records: page 2 to 10 of a
  92-day query cost ms, a page at the period's far end about a full
  read. The key hashes every input of the match: the areas' ids and
  salts, each account's ref, kind, name, `Added`, previous fingerprints
  and contested flag, the period, zone, filters and `asOf`. So another
  area never reads the entry, and a claim, release or rename makes
  another key (the old entry ages out). A row gone from the log (the log
  rewritten) reads the period again.
- `period` is one `usage.Period.Known()` accepts except `all`, reaching
  back 92 days at most, today included; anything else is `400` (never
  30 days in its place). `limit` is 1 to 500 (default 100). `summary` and
  `total` cover every row the filters keep, not just the page.

## Usage stats

`GET /usage/stats` (area key: its own area; console key: every area,
`?area=` narrows, and only it may `groupBy=area`) and the console's
`GET /areas/{id}/usage/stats`
([`admin_stats.go`](../../internal/gateway/admin_stats.go)).

- Same scope, rows and filters as the request rows: `adminScope`,
  `areaRows`, `readPeriodZone` and `readFilters`. A group's name comes
  from the scope's own accounts, the provider and agent tables, an
  area's note (console) and a caller key's name (console); an area is
  told a caller key as its `ck_` value alone.
- **One tally** (`tallyStats`) adds every record up once, into the
  totals, each bucket and each grouping asked for. `/usage` and
  `/areas/{id}/usage` are that tally split by model and by account in
  day buckets (`usageStats`), so the two can't drift; their JSON is as
  before. `TestAdminStatsMatchesUsage` holds them together. In the
  stats, `requests` is every request (a try handed on is none; it is an
  `attempts` and a `failed` row's), split by status into ok, retried,
  failed and canceled; `/usage`'s `requests` stays the answered ones.
- Buckets are `usage.BucketStart`/`BucketStarts`/`BucketIndex`, which
  the Usage page's timeline uses too: an hour by the zone's own clock
  (a DST day is 23 or 25 of them), a midnight, a Monday's midnight.
- Bounds: the period reaches back 366 days and spans no more; hour
  buckets for 7 days at most, day buckets for 92; up to two `groupBy`
  dimensions; `top` 1 to 100 (default 20), the rest merged into
  `other`; at most 5000 points after `top`, else `400`. The points
  (`statsShown`: buckets × (min(groups, top) + 1 for `other`)) only grow
  as groups are found, so the read stops at the group that puts them
  over (`statsMost`, `usage.VisitWhile`): a refused query reads the log
  up to that record, not the period (T-203). When `top` + `other` fit,
  no count of groups is over and nothing stops. Each area runs two
  stats at once (`statsSlots`, in memory); a third is `429
  too_many_requests` with `Retry-After: 1` and reads nothing. The
  console has two of its own. Another area is never held.
- Cost: one `usage.VisitWhile` from the period's start, whatever the
  groupings; a year of weeks reads a year of the log (1.43M records, 1.1M
  of them in 92 days: an area's 92 days ~0.42 s, its 366 days of weeks
  ~0.54 s, the console's ~1.05 s, on an M-series Mac).

## Proxy IPs

An area's admin keeps the proxy exit IPs its area owns and puts their
proxies on its own accounts
([`admin_ips.go`](../../internal/gateway/admin_ips.go),
[`internal/renewal/area_ips.go`](../../internal/renewal/area_ips.go),
[`internal/provider/area_proxy.go`](../../internal/provider/area_proxy.go)).

- **Whose an IP is.** `renewal.IP.Area` ("" the owner's) and `AddedBy`
  (`area` when the area's admin made it). An `ips.json` from before has
  neither: every IP is the owner's, and no area sees it. The owner's whole
  list (`SetIPs`, Models › Proxy IPs) takes both from the file by address,
  never from the list sent. Only `GiveIP` / `TakeBackIP` (the window's
  `POST /api/ips/area`) and an area's own writes change them. A magpie from
  before T-198 writing `ips.json` drops both: every IP is the owner's
  again, which is fail-closed. The proxies already on accounts stay in
  `providers.json`, but are no longer held to public addresses.
- **Two checks.** A handler finds the IP among the area's (`areaIP`, else
  `404`). The area's writes (`AddAreaIP`, `ChangeAreaIP`, `RemoveAreaIP`)
  read the file afresh under `ipsMu` and refuse an IP not the area's again
  (`ErrIPNotFound`). Writes go through a temp file renamed over the old one.
  An `ips.json` that can't be read is `503 ips_unreadable` and is never
  written over. At most 50 IPs an area (`MaxAreaIPs`). An address magpie has
  already is `409 conflict` with no owner named, counted with the add
  routes' conflicts (`probeHeld`).
- **Proxies are named** `px_` + 10 hex of SHA-256 (`renewal.ProxyID`), and
  told with their login left out (`netproxy.Redact`).
- **On an account** (`POST /accounts/proxy`, `provider.AttachAreaProxy`):
  the IP and proxy are the area's, and the account is the area's alone
  (`ownsRef`, and again `area.SetProxy` under the areas lock). The proxy's
  address is written to the provider's `AccountProxies` (a key by its
  fingerprint, an account by its login in lower case), which
  `ProxyChoice` reads. `areas.json` keeps `{ip, id, key}` on the area's
  account. Once the value there is no longer the area's proxy, the owner
  set it: the account says `overridden`, a new attach is `409`, and
  nothing of the area's takes the owner's value off.
- **No race with a removal.** An attach checks the IP and proxy, records
  them and writes the address while holding `ipsMu`
  (`renewal.WithAreaProxy`). The area's removal of an IP or proxy asks
  again, under `ipsMu`, whether an account goes through it
  (`provider.AreaIPInUse`, `409 in_use`). Lock order: `ipsMu`, then
  `areas.json`'s lock, then `providers.json`'s. Nothing that holds either
  of the latter takes `ipsMu`, and no network work runs under any of
  them. The account's own proxy is compared and set in `providers.json`'s
  lock (`setOwnProxy`'s `allow`). It is written only while it is empty
  with no record of the area's, or still the area's own value, so an
  owner's value set at the same moment is kept (`409`).
- **Taken off** when the area lets the account go (`area.Release`,
  `ReleaseProvider`, so `releaseArea`'s removals and the owner's take-back
  too, via `area.Released`), when the area detaches it, and by
  `SweepAreaProxies` after the owner removes, takes back or changes the
  IP, or the area removes a proxy. Each only while the value is still the
  area's. `ReplaceKey` moves it to the new fingerprint (`area.Rename`).
  An IP or proxy still on an area account can't be deleted by the area
  (`409 in_use`, naming the accounts).
- **Given by the owner** (`givenByOwner`): the area lists it and puts its
  proxies on its accounts. It never checks, changes the proxies of, or
  deletes it (`400`). Its proxy may be on the owner's network, which is
  the owner's decision.
- **Checks** (`POST /ips/check`): the area's own proxies alone, by id, to a
  fixed trace page (`areaTraceURL`) through `netproxy.Dispatch` (so
  `guardDial` holds). The answer is `ok`, the exit `ip`, `loc`, `ms`,
  `matches` and an `error` category (`refused`, `timeout`, `tls`,
  `proxy_auth`, `http_status`, `not_public`, `dns`), never the error's
  text nor magpie's own exit. Each area gets 10 in 10 minutes, one at a
  time (`proxyChecks`, in memory), else `429 too_many_checks` with
  `Retry-After`; a held check connects nowhere.
- **The owner's window** (Models › Proxy IPs,
  [`plugins/accounts/ips.js`](../../internal/gui/assets/plugins/accounts/ips.js)
  `ipsAreaButton`): each IP's "Give to area…" opens the app's menu of
  areas (`/api/ips` lists them as `areas`), and one an area has reads
  "Take back from <note>". `POST /api/ips/area` gives or takes back, then
  sweeps the areas' proxies.
- The console reads `GET /ips` (every area's, `?area=` narrows, each IP
  with its `area`; never the owner's) and `GET /areas/{id}/ips`, and
  changes nothing.

**What that means for trust** (beside the section above): the admin of an
area's proxy sees the upstream connections of the area's accounts through
it (SNI, timing, sizes; an `http://` base URL in full). That is the same
level as the vendor of an area's account, and is limited to that area's
accounts, which is why a released account loses it.

**Public-only proxies** ([`internal/netproxy/publiconly.go`](../../internal/netproxy/publiconly.go)).
A proxy an area added is connected to at a public address alone
(`netguard.Public`), when it is saved (`CheckPublic`) and on every
connection magpie makes to it, so a name pointed home after it was saved
(DNS rebinding) reaches nothing:

- `netproxy.PublicOnly` names such proxies (set by internal/provider): one
  an area's IP lists (`renewal.AreaAddedProxy`, by its text or as
  `netproxy.Parse` writes it), or one `areas.json` records on an area's
  account (`areaAttachedProxy`, by its id), so an attachment whose IP is
  gone, or an `ips.json` that can't be read, stays held. A read of
  `ips.json` that fails keeps the last good one; with none since magpie
  started only the recorded attachments are held, never the owner's own
  proxies. A failed read is tried again after 5 seconds or once the file
  changes, not on every dial. The cache is keyed on the file's size, its
  time and this magpie's own writes; every transport `Dispatch`
  makes, and the gateway's image fetch through a provider's proxy, dial
  through `guardDial`, which, for a request whose proxy (`With`) is one,
  resolves the proxy's name, refuses it unless every answer is public
  (`ErrNotPublic`, `ErrUnresolved`) and connects to the answers checked,
  never to the name again. A loopback host the request goes to direct is
  let through; any other address on such a request is judged as the
  proxy's, whatever its spelling. `DialSOCKS` (the bridges) does the same.
- A child process can't be guarded at its dial: Claude Code
  (`EnvForBunWith`), any CLI given `EnvWith`, and the plugin host
  (`PinChoice`) are given the address checked in the name's place (an
  https:// proxy keeps its name for its certificate, and is checked each
  time a child starts). One that isn't public fails `EnvForBunWith` at
  once, and elsewhere is `http://proxy-not-public.invalid:9`, which
  reaches nothing, never "" (the global proxy).
- A name is refused before it is looked up when it is `localhost`, under
  `.localhost`, a single label, or an IPv4 address in a C library's
  spelling (`2130706433`, `0x7f.1`, `0177.0.0.1`, `127.1`); a trailing dot
  is the same name. IPv4-mapped, NAT64 and 6to4 forms are judged as the
  IPv4 address inside. A fake-IP resolver's answer (198.18.0.0/15) is not
  public, so behind Clash's fake-ip TUN such a proxy is written as its
  address.

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
fingerprint, and `area.Rename` keeps the old one in `Account.Previous`:
usage and the areas list count the key's calls under each earlier
fingerprint too (from `Added`, as one `byAccount` row under its ref now).
An earlier fingerprint magpie has as a key again (the old value added back,
by the owner or another area) is left out: its calls can't be told apart.
An areas.json from before `previous` reads as it did. One the area couldn't
be told of lists as `gone`.

## Agreed, not built yet (T-209 to T-215)

Everything above is what magpie does today. On 2026-11-18 the owner settled
the next contract (magpie-console's `docs/magpie-api.md` §10–19, T-208):
plugin and missing built-in sign-ins, credential imports, a proxy picked
for a sign-in, import or key, more key fields, more proxy-IP routes, and
the console acting for an area. None of it is in the code yet; each task
moves its part into the sections above as it lands. What the magpie side
must hold, beside the contract's bodies and codes:

- **One reader, never a copy.** Sign-ins go through `StartSignInWith` and
  the plugin host's sign-in (`PluginAPIKey` and the paste paths in
  `pluginsignin_paste.go`); imports through `ImportLogins`,
  `ImportCodexAccessOnly`, `ImportGoogleAccounts` and `ImportFactoryKeys`;
  proxies through `settings.CheckProxy` and `netproxy.CheckPublic`;
  renewal through `internal/renewal` (`WarnDays`). The one new parser is
  the batch-proxy one (Go, `internal/renewal`), which shares its test
  lines with the window's `ipsbatch.js`.
- **Never written over.** An account magpie has (the owner's or another
  area's) is `conflict` on every add path, and its tokens, proxy and name
  stay as they were. The import readers today renew such an account
  (`updated`) and name it (`exists`); an area's import needs a mode that
  does neither (T-220). A plugin's sign-in, by any method, replaces an
  account it has, and magpie learns whose it is only after the plugin
  saved it (`pluginDone` after `plugin.Saved`). A plugin method is
  offered to areas only when the plugin tells the account first and saves
  it after magpie claimed it, so a known account fails with its tokens
  untouched (T-221, every method, not just API keys).
- **Claude imports are checked with Anthropic** (`ClaudeTokenUser`), and
  named by its answer, never by the file's email.
- **Not offered:** a plugin method whose browser opens on this computer
  (`Authorization.Opened`).
- **Secrets stay in.** A plugin's password and hidden inputs are not sent
  back by the prompt route; no key, token, code, pasted address or proxy
  login reaches a reply or the log. Text a plugin writes (a prompt's
  `error`, `instructions`, a sign-in's `message`) is withheld whole when
  it holds the key, a hidden input or any input of 4 characters or more:
  `keyFailure` cleans the log alone today.
- **Opened dates are a subscription's.** A request that makes a key
  (`accounts/key(s)`, a plugin's `api-key`, a Factory import) with
  `opened` is `400`.
- **Many keys keep what was added**: `accounts/keys` claims and writes one
  at a time and rolls none back.
- **Direct is an area attachment** (`{"direct": true}` in `areas.json`),
  told apart from no attachment. The owner's own value for the account
  wins over it; a subscription's or the global proxy doesn't.
- **Limits are the area's, from both keys.** Every add, sign-in, import
  and proxy route the console reaches as `/areas/{id}/…` runs the area's
  own handler and checks: what the area may add, 500 accounts, 50 IPs,
  `conflictProbes`, `proxyChecks` (probe, a batch's host-name lines and
  reread share it) and one import at a time. The console also has totals
  of its own across areas (40 conflicts, 20 checks in 10 minutes), so
  more areas don't buy more probes. The role is judged before `{id}` is
  looked up. Another area's account, IP or proxy is `404`; a sign-in not
  started by the caller is `404`, and only its starter (area or console)
  reads, pastes into or cancels it. A batch's `dryRun` resolves no name
  and judges no owner, so it tells nothing of other IPs.
- **New codes:** `409 plugin_unavailable`, `413 too_large` (the import
  route's 8 MB body alone), `429 too_many_imports`.
- **Log:** a console action writes the area, its note, the action and the
  optional `X-Magpie-Actor`, marked `console`; magpie keeps no audit file.
- Every new route is in `adminRoutes` under its role, so
  `TestAdminKeyEveryRoute` covers it.

## Verification

`go test -race ./internal/area`; `go test ./internal/gateway -run
'TestAdmin|TestReadOnlyKeyEveryRoute|TestKeySlotEveryRoute'`; `go test
./internal/provider -run 'TestAreaSignIn|TestAreaLetsGo|TestAttachOnlyOwnIP|TestReleaseClearsAreaProxy|TestSweepAreaProxies'`;
`go test ./internal/renewal ./internal/netproxy -run 'Area|Owner|OldIPs'`;
`go test ./internal/gui -run TestIPAreaRoute`; `go test ./internal/gui -run
TestAreasRoutes`. `TestAdminKeyEveryRoute` fails
when a route is under `/v1/magpie/admin` and in no role's list, or when an
admin key reaches any other route or an upstream.
