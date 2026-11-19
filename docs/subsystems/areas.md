# Areas and admin keys

An **area** is a note the owner hands out (学习, 工作, 娱乐…) with the provider
accounts it owns. Its **area admin** adds, lists and removes those accounts
and reads what they spent, through the gateway's admin routes, from a
separate console ([magpie-console](https://github.com/qiqll/magpie-console/blob/main/README.md), whose
`docs/magpie-api.md` is the contract both sides keep, and whose
[`docs/operator.md`](https://github.com/qiqll/magpie-console/blob/main/docs/operator.md) sets both up on
a Linux server from zero). The **console key** reads every area and makes,
changes and deletes them, and does for any area what its admin does
([The console acting for an area](#the-console-acting-for-an-area-t-215)).

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
`accounts/keys`, `accounts/remove`, `usage`, `usage/requests`, `usage/stats`, `signin`, `signin/{id}`, `signin/{id}/callback`,
`signin/{id}/cancel`, `signin/claude-token`, `ips`, `ips/update`,
`ips/delete`, `ips/check`, `accounts/proxy`, `accounts/import`. Console: `me`, `catalog`,
`areas`, `areas/{id}`, `areas/{id}/accounts`, `areas/{id}/usage`,
`usage/requests`, `areas/{id}/usage/requests`, `usage/stats`,
`areas/{id}/usage/stats`,
`areas/{id}/update`, `areas/{id}/rotate-key`, `areas/{id}/delete`, `ips`,
`areas/{id}/ips`, and each area write route and `options` under
`areas/{id}/` ([The console acting for an area](#the-console-acting-for-an-area-t-215)). Request
and reply bodies are in magpie-console's `docs/magpie-api.md`.

- Adding a key claims it first and adds it after (a failed add releases
  it), so it is the area's from the moment it is in magpie. A key magpie
  has already is `409` and names no owner.
- **A key's fields** (T-213, [`admin_keys.go`](../../internal/gateway/admin_keys.go)):
  `protocol` is `""` (any) or an API the provider has an address for, as
  the window's protocol picker offers (`keyProtocols`); `plan` is one of
  the preset's plans when it sells them on keys of their own
  (`provider.KeyPlans`), the provider's own kept as none
  (`keyPlanOf`, shared with `SetKeyPlan`). The key is added with both
  (`provider.AddKeyOnPlan`). Any other value is `400` and adds nothing.
  `options` and `catalog` give each provider its `protocols` and, when
  there are any, `plans`. A key sent with `opened`, even `""`, is `400`:
  a key has no opening date.
- **Many keys** (`accounts/keys`, `adminAddKeys`): 1 to 100 after blank
  ones are dropped, each claimed and added on its own as `accounts/key`
  does, and none rolled back. Each gets a row in the order sent, by its
  `masked` value and never the key: `added` (with its account), `conflict`,
  `duplicate` (sent twice in the request) or `failed` with why. A `name`
  is given ` #2`, ` #3`… by row. Past the area's 500 accounts the rest fail
  with "an area has at most 500 accounts": the count is judged in the
  claim's own lock (`area.ClaimUpTo`, which signin and claude-token use
  too), so requests side by side never take an area past it. A key the
  owner added between the check and the write is a `conflict`, counted. Each conflict counts toward the
  area's conflict line; at the 20th the rest fail with "too many
  conflicts; try again later", and an area already held is `429` at the
  door (`heldAnswered`, shared with `probeHeld`).
- That `409` would let an area admin test whether some key or account is in
  magpie, so each area's `409`s on `accounts/key` and
  `signin/claude-token`, and each conflict row of `accounts/keys`, are counted (`conflictProbes`, admin_probe.go):
  after 20 in 10 minutes both routes answer that area `429
  too_many_conflicts` with a `Retry-After`, whatever it sends, until the
  oldest leaves the window. A held request is not run. The 20th is logged
  with the area's id and the count, never the key. Other areas are not
  held. The count is in memory: a restart clears it. The console acting
  for areas has a total of its own besides (T-222, below).
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
[`admin_ips_batch.go`](../../internal/gateway/admin_ips_batch.go),
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
  `ProxyChoice` reads. `areas.json` keeps `{ip, id, key, marked}` on the
  area's account, and the provider marks the value as the area's
  (`AreaProxies`, by the same key, T-224). Every `providers.json` write
  keeps a mark only while the value is still the one the area wrote
  (`areaProxiesKept` in `storeQuiet`), so an owner who changes it, in
  the editor, a command or a sign-in's pick, takes the mark out; a
  sign-in's pick of the same value does too (`keepSignInProxy`). An
  editor save that leaves the value as it is can't be told from one that
  typed the same value again, and keeps the mark. Once the mark is gone
  the owner set it, even back to the area's own value: the account says
  `overridden`, a new attach is `409`, and nothing of the area's takes
  the owner's value off (`areaHas`). A key's new value carries the mark
  (`ReplaceKey`). Copilot's account renamed carries it only with its
  value (`moveAccountSettings`); the area's record keeps the old name.
  A record from before the mark (no `marked`) is judged by its value
  alone, as then, until the area puts a proxy or direct on it again;
  nothing is migrated.
- **Direct** (`{"id","direct":true}`, `provider.AttachAreaDirect`, T-213):
  `"direct"` is written as the account's own proxy, so it goes through
  none, and `areas.json` keeps `{key, direct: true}` with no IP or proxy.
  It is checked and taken off as a proxy is (`areaHas`, `overWrites`):
  the owner's own value for the account, set before or since, wins
  (`overridden`, `409`); the subscription's and the global proxy don't.
  The account tells `"proxy": {"direct": true, "overridden": …}`.
  `SweepAreaProxies` leaves a direct alone: it goes through no IP, and it
  takes off only the record it read (`detachIf`), never one the area set
  since. A
  direct sent with an IP or proxy is `400`. Attaches, directs and
  detaches take `attachMu` first, so two on one account never cross.
- **Picked as it is added** (T-212): a sign-in, a setup-token and a key
  take `proxy` too, and the account added has it put on as above; see
  [Area sign-ins](#area-sign-ins).
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
  `Retry-After`; a held check connects nowhere. The console has 20
  across areas besides (T-222).
- **Renewal** (T-214): an IP with an opening date tells `renewal`
  `{opened, ends, due, state, daysLeft}` from `renewal.Of`, the window's
  one calculation: `state` is `""`, `soon` (within `WarnDays`) or `ended`,
  `due` stays the boolean it was (either of the two), and `daysLeft` is
  below 0 once ended, the whole days since (`State.DaysFrom`).
- **A proxy not saved** (`POST /ips/probe`, T-214) is checked as a saved
  one is, to the same page, its answer's `proxy` with the login left out.
  It spends one of `proxyChecks` before anything else, since judging a
  name public resolves it, and is `400 proxy_not_public` (connecting
  nowhere) when `CheckPublic` refuses it. Its connection is held to
  public addresses at its dial although no `ips.json` names it
  (`netproxy.WithPublicOnly`, which every area check now sends), so a
  name rebound after it was judged reaches nothing.
- **Many at once** (`POST /ips/batch`, T-214): the lines are read by
  `renewal.ReadBatch`, a Go port of the window's `ipsBatchParse` (six
  formats, at most `BatchMost` 100 lines), and the two read the same
  shared lines (`internal/renewal/testdata/batch_lines.json`, held by
  `TestAreaBatchLinesReadAsTheWindowReads` and
  `internal/gui/tests/proxy-ips-batch-lines.test.cjs`; a change to either
  reader regenerates them from `ipsbatch.js`). The text is bounded before
  any line is read (`CheckBatch`: 100 lines, 4096 bytes a line). A line whose host is an IP
  is kept under it, judged public by its address and never connected to;
  a host name is kept under its exit, read only with `check:true`, each
  such line one of `proxyChecks` (the batch holds the area's one at a
  time while it runs; past the budget the names left are `failed`
  "check limit reached"). What is left is judged and written at once,
  under `ipsMu` against `ips.json` read then (`renewal.AddAreaProxies`):
  `added`, `merged` into an IP the area made, `exists`, `conflict` for an
  IP magpie has as no IP the area made (whose not said; each counted in
  `conflictProbes`, and the one that reaches the line fails the rest),
  and `failed` for an IP the owner gave, a full IP (20 proxies), the
  area's 50 or magpie's 500. A full area is said before a conflict, for
  an IP magpie has or not, so a full area's misses tell nothing of other
  IPs. A whole-list check's error (`CheckIPs`, which can name another's
  IP) is logged, never told. A batch is 429 at the door while the area
  is held for conflicts, or, with `check` and a name in it, while its
  checks are spent. `dryRun` reads the lines and judges an IP host public
  and no more: it resolves no name, connects nowhere, spends nothing and
  judges no owner (`ok`, `invalid`, `not_public` alone), so it tells
  nothing of other IPs.
- **Exits read again** (`POST /ips/reread`, T-214, in place of "import
  from accounts", whose proxies are either the area's IPs already or the
  owner's): each proxy of an IP the area made that is on one of its
  accounts is checked, a check spent each (those past the budget
  `failed` unread; none left at the door is 429). One that leaves from
  another IP now moves there (`renewal.MoveAreaProxy`): to an IP the area
  made, or a new one of the area's (50 at most). Its accounts' records
  move with it in the same hold of `ipsMu` (`area.MoveProxy`; a failure
  there puts `ips.json` back). `SweepAreaProxies` reads the two files
  apart, so before it takes a proxy off it reads both again under
  `attachMu` and `ipsMu` (`sweepDetach`, `renewal.WithIPs`), and a move
  that came between leaves the account as moved. The address written to
  the account is the proxy's and stays. An exit
  that is magpie's as no IP the area made is `failed`, whose not said,
  and counted in `conflictProbes`. An IP the owner gave is never read.
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

An area's proxy needs only a public address. The owner decided on
2026-11-19 (T-225, reverting the exit check of 11-18) that its exit
isn't compared with the IP it's added under, and adding one under an IP
spends no `proxyChecks` (of the adds, only a batch line by name with
`check` reads an exit, to learn its IP): a proxy that doesn't leave from its IP only
fails its own requests.

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
device code or the address pasted, Sign in with ChatGPT (`chatgpt-api`)
by the address pasted, Copilot by device code, Claude by a setup-token or
`claude auth login`'s code pasted, Gemini and Antigravity by the address
pasted. A subscription a plugin serves signs in by the methods read to
be safe for areas ([Plugin sign-ins](#plugin-sign-ins-t-209)). Another
tool's files come in by [Imports](#imports-t-211-t-220).

- Copilot signs in on github.com, or on an enterprise's `<name>.ghe.com`
  when the request has `site` (`copilotSite`, admin_signin.go): lower
  case, a leading `https://` and a trailing `/` taken off, it must match
  `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.ghe\.com$`. Anything else
  (github.com, a GHE Server host, an IP, a port or path) is `400`, and so
  is a `site` sent for another subscription; no site, or `""`, is
  github.com as before. Copilot's entry in `options` and `catalog` has
  `"sites": ["github.com", "ghe.com"]`.
- A pasted address reaches only the sign-in's own callback
  (`pastedCallback`): its port, path and state. Another is `400`. One
  taken whose sign-in ends in `conflict` is `204`, as a plugin's paste
  is: the sign-in's state says how it ended (T-227; `claudePaste` too).
- **Opened** (T-213): `signin` and `signin/claude-token` take `opened`,
  `YYYY-MM-DD` in this computer's zone (`areaOpened`), checked by
  `renewal.CheckOpened` before anything starts (`400` else). It is kept
  with `provider.SetAccountOpened` once the sign-in leaves the account the
  area's (`openOnDone`, after `claimOnDone`): a `conflict` keeps none on
  an account that isn't the area's. The area's accounts tell `opened`
  for a subscription account that has one, never for a key.
- **A proxy picked** (T-212, admin_signin_proxy.go): `signin`,
  `signin/{id}/callback`, `signin/claude-token`, `accounts/key` and
  `accounts/keys` take `"proxy": {"ip","id"}` (one of the area's IPs,
  added by it or given by the owner, and a proxy of it) or
  `{"direct":true}`. `pickedProxy` judges it before anything starts or is
  claimed: another area's IP or proxy, the owner's own, or none such is
  `404` (`areaIP`, `ProxyOf`); neither or both sent is `400`. A sign-in
  goes through it (`SignInOptions.Proxy`), so its requests to the vendor
  leave from where the account's will. An area-added proxy is held to
  public addresses on every connection the sign-in makes through it
  (`SignInOptions.PublicOnly`, `netproxy.WithPublicOnly`), whatever
  ips.json says by then, and while a sign-in of the area under way (or
  starting) was given a proxy, the area's `ips/delete` of its IP and
  `ips/update` removing it are `409 in_use` (`ipInUse`, under `ipsMu`;
  a start judges its pick again once it is seen there), since Claude
  Code's own sign-in is given the proxy as ips.json says it. Once the
  sign-in is done and the account the area's, `mountOnDone` (after
  `claimOnDone`) puts on the pick of the proxy it went through
  (`SignInState.Proxy`, told to Done) with
  `AttachAreaProxy` or `AttachAreaDirect`, so the owner's own value for
  the account wins (`overridden`), and a `conflict` gets nothing. An
  area's pick is never kept as the owner's own value (`finish` keeps a
  picked proxy for the owner's sign-ins alone), so an account another has
  keeps the proxy it had. A setup-token and a key make no request out:
  the pick is only put on. One that can't be (the IP removed meanwhile)
  leaves the account added and says so (a sign-in's `message`, a key
  row's `message`); `accounts/key` and `claude-token` answer `201` with
  no `proxy` on the account.
- **A proxy picked as it is pasted:** the callback's `proxy` trades the
  address through it (`SubmitSignInCallbackThrough`, held public by
  `HoldSignInPublic`), and the account has it once done (`pickBox` maps
  each proxy given to its pick); a paste that isn't taken leaves the
  sign-in going through the one it had, whose pick is put on. A sign-in
  whose proxy was handed out as it started (a device code, `claude auth
  login`: `ProxyFixed`) is `400` "this sign-in's proxy was fixed when it
  started" for a proxy other than the one it goes through now; that one
  is taken.

- `SignInOptions.Area`: the sign-in starts only while none of its agent is
  under way (`reserveSignIn`, held as pending until listed); while it runs
  every other start to that agent, the owner's included, is
  `ErrSignInBusy` (`409`) instead of replacing it. The owner's still
  replace the owner's.
- The agent on this computer is never signed in to an area's account
  (`addLoginOf(…, magpieOnly)`), unless it was signed in to that account
  already.
- `SignInOptions.Done` claims the account as the sign-in is done, whether
  or not the console asks again. It is told the sign-in's agent and id
  (`finish`), which the claim is made by. One magpie had before (`Again`, the
  owner's or another area's) stays whose it was and reads `conflict`;
  its tokens were renewed by the sign-in, which hands no account across.
- Each sign-in is read, pasted into and cancelled by the area that started
  it alone (`areaSignIns`). One that ended, or that the owner's replaced
  (canceled), is still read for 10 minutes (`areaSignInKept`), so its
  console learns how it ended when another started as it ended.

## Plugin sign-ins (T-209)

[`admin_plugin_signin.go`](../../internal/gateway/admin_plugin_signin.go),
magpie-api.md §11. An area signs in to a subscription a plugin serves at
`signin/plugin` (and asks its questions at `signin/plugin/prompt`);
`signin/{id}`, its callback and cancel are the built-ins'.

- **What is offered.** `areaPlugins` lists, by provider id, the package
  read and each method an area may use, by its type and label as the
  plugin lists it, with its way: the rows marked yes or yes by key in
  [the table below](#plugin-sign-ins-an-area-can-use-t-221), for the 13
  subscriptions there. A method is offered at whatever index the plugin
  lists it now, while its label is the same; another package serving the
  id, or a method renamed or added, is offered nothing until the list is
  read again. A paste-key method is offered only while the key method
  its pasted key goes through is offered too. `options`, `catalog` and the window's areas list
  (`signInWays`) carry each such subscription whose plugin is on, with
  `plugin: true` and its `methods` (§11.2), so the owner and the console
  can open it to an area.
- **Judged before anything starts** (`pluginMethodOf`): a subscription
  not opened to the area, or not in `areaPlugins`, is `404`; one opened
  whose plugin is off, not installed or another package's is `409
  plugin_unavailable`; a method no longer at its index, or whose way isn't
  the one sent, is `409 conflict` "reload options"; one not offered is
  `400`. The inputs are held to §11.3's limits (`checkInputs`), `opened`
  is `400` with a key and taken by the other ways, and `proxy` is picked
  as `signin`'s is (T-212). The answers are walked as the prompt route
  takes them (`pluginAnswers`): each question the method asks, in its
  order, answered by one of its options and passing the plugin's
  `validate`, and nothing it doesn't ask; else `400` and nothing starts.
- **Plugin host calls are held** (T-231,
  [`admin_plugin_calls.go`](../../internal/gateway/admin_plugin_calls.go)):
  the prompt route asks the host up to 3 times a call, the start route up
  to 34 (its answers walked, then the start), and that host serves every
  plugin subscription's requests. Each area runs one of the two routes
  at a time and asks the host 200 times in 10 minutes, the console 400
  across the areas it acts for besides (`budgets`, `pluginCalls`, in
  memory); past either, or while the area's other call runs, the route
  is `429 too_many_plugin_calls` with `Retry-After` and asks the host
  nothing more. A sign-in of 16 questions, each answered at the prompt
  route and then started, asks 83 times. Not counted yet: a plugin
  sign-in's pasted callback (`signin/{id}/callback`, which the host
  takes) and an import's plugin keys (`accounts/import`, one host call a
  key, held by the import's own limits: one at a time, 200 rows, 20
  failures); `signin/{id}` and cancel ask the host nothing.
- **Conflicts count where they are decided.** An area held for its
  conflicts is `429` at the door. A key's `ErrKnown` (or a claim
  `ErrTaken`) counts at once; a sign-in that ends in `conflict`, as its
  pasted code, address or key finds an account magpie has, counts as it
  ends (`SignInOptions.Conflicted`, `conflictOnDone`). The busy `409` of
  another sign-in to the same agent, and a changed way's, count nothing:
  a wait on another area is no probe of what magpie has.
- **A new account only.** A key goes through `provider.PluginAPIKeyNew`
  (`plugin.APIKeyNew`) and is claimed once it is saved; when the claim
  fails for a reason but `ErrTaken` (the area deleted), or the account
  has no name, the record is signed out again. The other ways start
  `StartPluginSignInWith` with `Area`, which begins with
  `plugin.AuthorizeNew`, installs no vendor CLI here, and is claimed by
  `claimOnDone` as a built-in's (`subscriptionOf` gives a plugin's id).
  An account the plugin kept as another provider's than the one asked
  (one package serves Qoder and Qoder CN) is signed out again, and so is
  a key's that can't be claimed, by its own record (`unclaimed`). A key
  runs to its end whether or not the caller is still there. A
  key pasted on the keys page (paste-key) goes through `APIKeyNew` too. An
  account magpie has, the area's own included, is `conflict` (a key's
  `409`, a sign-in's `state`) and stays as it was: an area renews no
  account this way.
- **The way is checked as it starts** (`SignInOptions.PluginWay`,
  `provider.PluginWayOf`): a page whose code is pasted is paste-code, one
  that comes back to a port here paste-url, Command Code's is paste-key,
  any other page device. One that comes out otherwise is
  `ErrPluginWayChanged` (`409`); one the plugin opened on this computer
  (`Authorization.Opened`), or with no page, is `400`. Either way the
  host's session, and the key it reserved, are let go (`plugin.Abandon`).
- **The plugin's words** (T-229): a reason the plugin gave for failing
  (its own, or the host's) goes to magpie's log, what the sign-in was
  given left out (`areaSaid`, `keyFailure`), and the area reads only
  `ErrPluginFailed`. Magpie's own reasons (a known or unnamed account, a
  time out) are said as they are. Text a plugin writes that reaches the
  area (a prompt's `error`, a sign-in's `instructions` and `message`) is
  withheld whole when it holds the key, a hidden answer, or any answer of
  4 characters or more, as it is, escaped, quoted or cut short
  (`pluginSaid.held`, `provider.SaysBack`); a device code drawn from such
  words is left out. The prompt and start routes log the host's failure
  the same way (`pluginSaid.logged`).
- **The prompt route** answers one question at a time: `value` is checked
  against a list's options and the plugin's `validate`, and answers typed
  unseen (`Asked.Hidden`, or a `password` prompt answered) come back as
  `""`, their keys in `hidden`.

## Imports (T-211, T-220)

[`admin_import.go`](../../internal/gateway/admin_import.go),
[`import_options.go`](../../internal/provider/import_options.go),
magpie-api.md §13. `accounts/import` takes the files the window's import
takes, read by the same readers (`ImportLoginsWith`,
`ImportGoogleAccountsWith`; `ImportLogins`, `ImportCodexAccessOnly` and
`ImportGoogleAccounts` are the window's, unchanged and still `updated`
for an account it has). The window's pasted sign-in address is not
taken: an area pastes it to `signin/{id}/callback`.

- **Judged before anything is read.** An area held for its conflicts is
  `429` at the door. The body is 8 MB at most (`adminUpTo`, the import
  route alone; `413 too_large` past it), with 1 to 50 `files` and at most
  200 accounts (the readers' `maxGoogleImport`), else `400`. An `agent`
  not opened to the area is `404`, one that imports nothing (or a
  built-in a plugin now serves) too; `accessOnly` is codex's alone.
  `opened` is `400` for Factory's keys and taken by the rest, as `signin`
  takes it. `proxy` is picked as `signin`'s is (`pickedProxy`, T-212),
  and the readers' requests go through it, an area-added one held public
  on every connection (`netproxy.WithPublicOnly`); none picked goes
  through the subscription's (`provider.Via`). One import runs per area
  at a time, and 4 in all (`startImport`, taken before the body is
  read). Of those 4 the console's, across the areas it acts for, are 2
  at most (`adminConsoleImportsMost`, T-235), so its imports never hold
  every area's door. Another is `429 too_many_imports` with
  `Retry-After`. It runs to its end, at most 3 minutes, whether or not
  the caller is still there.
- **A new account only** (`ImportOptions.NewOnly`, T-220). An account
  magpie has, the owner's, another area's or this area's, by its refresh
  token (or access token, `accessOnly`) or by who it is once its vendor
  says (`sameLogin` for ChatGPT and Claude, the email for Google), is
  `conflict`: nothing of it is written or renewed, and the row names no
  one. A ChatGPT account whose refresh token magpie doesn't have is
  refreshed before it is judged, which signs the file's own sign-in out:
  the file's id_token says nothing an area can be believed on. The check and the write are one step under the logins' lock
  (`addLoginWith`, `addSideLoginNew`), which also claims the account for
  the area (`ImportOptions.Claim`, `area.ClaimUpTo` of its 500) before
  anything of it is written; another area's ref is `ErrTaken`, read as a
  conflict, and a save that fails after the claim lets it go (`Unclaim`).
  It is kept as an area's sign-in keeps one (`addLoginOf(…, magpieOnly)`):
  the agent here is not signed in to it.
- **Named by the vendor, never the file.** A ChatGPT account by the
  id_token ChatGPT's refresh gives (none given fails); one kept access
  token only (`accessOnly`, never refreshed) only once its access token's
  own claims name the id_token's email, workspace and plan and ChatGPT
  answers `/wham/usage` with it (`codexAccessChecked`); a Google one,
  Antigravity's or Gemini CLI's (T-232), by Google's userinfo (none fails), a Claude one by Anthropic's answer for
  its access token (`askClaudeProfile`, `/api/oauth/profile`, the owner's
  call of 2026-11-18: the window still asks Anthropic nothing). A Claude
  token that has run out by its own `expiresAt`, or that Anthropic
  refuses, fails, saying to run Claude Code on it once and export again.
  A tool's own name for an account (claude-swap's alias) isn't taken.
- **Factory's keys** (`keyImports`) go through the plugin's own key
  sign-in to a new account only (`areaPluginKey`, shared with
  `signin/plugin`'s api-key): the plugin off or not offering its key
  method is `409 plugin_unavailable`.
- **Rows.** One row an account, in the files' order: `added` with its account
  (and the proxy picked, and `opened`, put on as `signin` does),
  `conflict` ("this account is in magpie already"), or `failed` with why.
  A whole file of several that can't be read is a row with its `file`; a
  single file that can't be read is `400`. A reason that says back a run
  of 12 or more token characters with a digit that is in the files is
  left out (`heldFrom`), as a vendor's answer may repeat the token; the
  readers' new-only mode says a vendor's refusal in magpie's words and its
  HTTP status alone (`vendorRefused`), never the vendor's text.
- **Limits midway.** Each account is admitted in the files' order before
  it is judged or its vendor asked (`ImportOptions.Admit`): past the
  area's 500, or once the area's conflicts reach the line, the rest fail
  with "an area has at most 500 accounts" or "too many conflicts; try
  again later". Near the line admission waits for the accounts being
  read, each of which may be a conflict, so an import tells no more
  conflicts than the line lets through. Each conflict counts
  (`countConflict`).
- **Failures have a line too** (T-233, `importFailed`, admin_probe.go).
  Most accounts ask their vendor, through the owner's exit when no proxy
  is picked, and a failure counts against no conflict line, so each
  admitted account that fails counts to the area's 20 in 10 minutes, and
  the console's to its 40 across areas too (`budgets`, as T-222's
  conflicts). That is a vendor's refusal, a plugin's key refused, and
  also a failure found before any vendor is asked (a Claude token run
  out by its `expiresAt`, a save that fails): gateway can't tell them
  apart. Past the line the import's rest, and every next import's, fail
  with "too many accounts failed; try again later" before their vendor
  is asked, until the window lets go of its oldest; admission waits near
  it as it does for conflicts. The import route still answers `200` with
  those rows (no `429` at the door), and other add routes
  (`accounts/key`, `signin/claude-token`) aren't held by it. Reaching it
  is logged with the count alone.
- **Log:** `admin: area <id> (<note>) imported <agent>: added N,
  conflict N, failed N`, no name or token.

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

## The console acting for an area (T-215)

The console's key does for any area what its admin's key does, under
`/v1/magpie/admin/areas/{id}/…` (magpie-console's `docs/magpie-api.md`
§17): `accounts/key`, `accounts/keys`, `accounts/remove`,
`accounts/import`, `accounts/proxy`; `signin`, `signin/plugin`,
`signin/plugin/prompt`, `signin/claude-token`, `signin/{sid}`,
`signin/{sid}/callback`, `signin/{sid}/cancel`; `ips`, `ips/update`,
`ips/delete`, `ips/check`, `ips/probe`, `ips/batch`, `ips/reread`; and
`GET options`, the area's own. Every one is in `adminRoutes` under the
console, so `TestAdminKeyEveryRoute` covers it.

- **One handler, never a copy.** Each route is the area's own handler
  wrapped in `forArea` ([`admin.go`](../../internal/gateway/admin.go)),
  which sets the call's area to the one the path names and runs it. So
  the checks are the area's: what it was given (`providers`, `signIns`),
  500 accounts, 50 IPs, a public proxy, `conflictProbes`, `proxyChecks`
  and one import at a time, all counted under the area's id, shared with
  its own key. The console has no limit of its own to go round them.
  What isn't opened is first opened with `areas/{id}/update`.
- **Role, then id.** `Server.admin` judges the role first: an area's key
  is `403 admin_console_only` on these routes whatever the id, and the
  console's on the unprefixed area routes is `403 admin_area_only`. Then
  `forArea` reads `{id}` (`consoleArea`): none is `404`, unreadable is
  `503`. An area turned off is acted for still (the owner manages it).
- **The resources are the area's.** An account is claimed by the area,
  an IP is `addedBy:"area"` and held to public addresses. Another area's
  account, IP or proxy through `{id}` is `404`, as it is to that area's
  key.
- **A sign-in is its starter's.** `areaSignIns` records whether the
  console started it (`areaSignIn.console`), and `ownSignIn` matches the
  area and the starter: the area's key reads, pastes into and cancels
  the console's as `404`, and the console the area's. One agent's sign-in
  under way is still `409` to both. Turning an area off cancels the
  sign-ins its key started (`CancelAreaKeySignIns`, the window's too);
  the console's go on. Deleting it cancels both (`CancelAreaSignIns`).
  Rotating its key (`rotate-key` or the window's, both
  `RotateAreaKey`) cancels its key's as turning it off does (T-236): the
  old secret's sign-ins add no account. A sign-in recorded after any of
  these (`keepAreaSignIn` reads the area again once it is recorded, and
  for the key's, whether its secret is still `access.Current`) is
  cancelled there; a key's whose secret was rotated meanwhile is
  cancelled alone. One whose key was changed by a way that cancels
  nothing (`magpie gateway-key`, the window's key list) and ends after
  claims nothing: `claimDone` checks `access.Current` too and takes the
  account out again. A plugin's cancelled as a pasted code or key is
  traded gives the trade up (`trading`, T-262), so the plugin's host
  saves nothing of what it answers after; one saved before the cancel
  came, or by a page's own wait, is signed out again (`pluginDone`,
  `SignOutSaved`, as `unclaimed` does). It went to a new account only
  (`AuthorizeNew`, `APIKeyNew`), so that is never the owner's or
  another area's, which the plugin answers as known and leaves.

  An import, a batch of keys or a plugin's key sign-in already under way
  when the key changes, or the area is turned off or deleted, claims no
  account after (T-260): `stillActs` reads `access.Current` and the area
  again before each account is admitted and just before it is claimed,
  and the rest fail saying why (`401`'s words, not counted as the area's
  failures). A plugin's key saved meanwhile is signed out again by its
  own record (`unclaimed`, `SignOutSaved`). Once an import is kept out
  it stays out (`areaImport.acts`). An account claimed before the
  change stays the area's, and so may one whose change lands between
  the read and the claim, a window of one file write: `ClaimUpTo` reads
  neither the key nor the area's `off`.

  The console's own key is held the same way (T-261). Rotating, turning
  off or removing it from the window (`console-rotate`, `console-off`,
  `console-remove`) cancels every sign-in it started, for every area
  (`CancelConsoleSignIns`); the areas' own keys' go on. One its old
  secret records after is let go by `keepAreaSignIn`, one that ends
  after a change that cancels nothing (`magpie gateway-key`) claims
  nothing (`claimDone`), and its imports, batches of keys and plugin
  key sign-ins claim nothing after (`stillActs` reads `access.Current`
  for the console's key too, though not the area's `off`, which the
  console acts through). The new secret's go on.
- **Log.** Each action's line is `admin: console for area <id> (<note>)
  <action> actor=<X-Magpie-Actor or ->` (`adminCall.logf`, the same line
  an area's key writes as `admin: area <id> (<note>) <action>`). A write
  whose handler logs nothing (refused before it acts) is logged by
  `forArea` as `<route>: <status>`. The account a console sign-in adds
  is logged as the console's with its actor too (`claimDone`). `X-Magpie-Actor` is 1 to 64 of
  `[A-Za-z0-9._@-]`, sent once, or `400`; magpie neither checks nor keeps
  it, and keeps no audit file: the console's is the audit. Neither line
  holds a secret.
- **The console's own totals** (T-222, magpie-api.md §18). Acting for
  an area spends that area's counts and a total of the console's across
  every area: 40 conflicts (`consoleConflictMost`) and 20 proxy checks
  (`consoleChecksMost`) in 10 minutes, kept under `consoleBudget` in
  `conflictProbes` and `proxyChecks` (`budgets`). Past either, the
  console is `429 too_many_conflicts` / `too_many_checks` with a
  `Retry-After` for every area, one with tries left too, and the message
  names the console. The batch routes stop the rest as they do at an
  area's line. An area's own key spends its area's counts alone, so its
  20 and 10 are unchanged, and the console's one-at-a-time is each
  area's, not one across areas; of magpie's 4 imports at once the
  console's are 2 at most (T-235). The console's imports for different
  areas judge their reads together (`importReads`, one lock for every
  import's admit), so four at once still tell it no more than 40.

## Plugin sign-ins an area can use (T-221)

Read from the plugins repo at 63c2ff93f (2026-11-18), every package with
an `auth.methods`, and from the host (`internal/plugin/host.js`,
`providers.go`, `internal/provider/pluginsignin.go`).

**What the host does now.** Before T-221, every plugin sign-in was
written first and judged after. host.js `save` wrote the result at the
account's key, and `settle` then folded it into any account with the
same name (`accountId ?? metadata.email ?? email`, uid apart) or, with
no names on either side, the same secret. A plugin could also write
records itself while signing in (`client.auth.set`). The CodeArts plugin
does this: it writes its account's record by the account's id. So every
method, keys included, replaced a known account, and magpie learned whose
account it was only from `plugin.Saved`.

`plugin.AuthorizeNew` and `plugin.APIKeyNew` (`fresh` in host.js) are the
area's way in:

- They sign in to a fresh key only. `client.auth.set` and `auth.remove`
  are held while the sign-in runs.
- host.js `saveNew` judges the result before anything is written, in
  one `changeAuth`. The result is `ErrKnown` and nothing is written when
  any of these hold:
  - an account of the provider has the same name or uid (`sameAccount`,
    `settle`'s rule);
  - an account of the provider has the same secret (refresh token or
    key);
  - the plugin is `single` and has an account already.
- The result is `ErrUnnamed` and nothing is written when a page's
  sign-in (OAuth) names no account. A fresh token matches nothing, so
  magpie can't tell such an account from one it has. So is a result
  with neither a name nor a secret (an empty key).
- An API key needs no name: the same key is the same account.
- The name comes from the plugin's result alone (`ownInputs`): answers
  are kept only for the questions the method asks, and never as
  `email`, `accountId`, `uid` or `cli`. Otherwise an area could type the
  owner's email beside a junk key, and the owner's next sign-in to that
  account would be folded into the area's (`settle`).
- The new key is reserved while the sign-in runs (`signingKeys`), so an
  owner's sign-in begun beside an area's is never handed the same key
  and can't write over the area's account.
- The plugin's own writes are held only until the account is kept. A
  timer the sign-in started (a keep-alive renewing the token) writes to
  the kept account after that.
- A plugin that hands its tokens over only through `client.auth.set`,
  with an empty result, gets "gave nothing to keep". None of the
  packages read does this.

Tests: `TestSignInNew*` and `TestAPIKeyNew*` in
`internal/plugin/plugin_signin_new_test.go`.

**What the host can't do.** It can't tell two names apart when they are
the same account. A plugin that names an account by something that
changes (a display name, a fingerprint of this sign-in's token, a name
the user types) gets a second record of an account magpie has. Those
methods need a plugin change. A name that falls back to a constant
(`"Zed account"`) is safe: a second such account reads as known and
fails. The other way round the owner's later sign-in to another account
that falls back to the same constant reads as the area's account: since
T-228 it fails (`ErrAreaOwned`, [The owner's side](#the-owners-side-t-228))
rather than being folded into it by `settle`, but the owner can't sign
in to that other account either. A name the plugin takes from what the
user typed (Gumloop's email, Agnes Code's username) is unsafe the same
way. Both are marked plugin below: the plugin should fail rather than
fall back, or look the account up.

**Legend:**
- **yes**: safe through `AuthorizeNew`/`APIKeyNew` now. A name the
  plugin sometimes lacks fails as `ErrUnnamed`.
- **yes, by key**: the host keeps the key, and the same key is known.
  Another key of the same vendor account is another account, as for the
  owner.
- **plugin**: a plugin change is needed (named in the row). The method is
  not offered until then.
- **not offered**: the method needs this computer. It opens a browser or
  app here, runs the vendor's CLI here, or reads a file, keychain or app
  state here (Q1, `Authorization.Opened`). An area's user can't reach
  any of that, and it would claim the host's own account.

The way column follows §11.1:
- `paste-url`: a loopback page whose address is pasted
  (`PluginPastesCallback`).
- `device`: the plugin polls by itself.
- `paste-code`: method `"code"`.

| Plugin (provider) | # | Method | Way | Name in the result | Area |
| --- | --- | --- | --- | --- | --- |
| cursor | 0 | Cursor (browser) | device | `accountId` = email, when `whoIs` has one | yes |
| cursor | 1 | cursor-agent's sign-in | — | reads `~/.cursor`, keychain | not offered |
| cursor | 2 | Cursor API key | api-key | — | yes, by key |
| kiro | 0 | Kiro (Google … IdC) | paste-url (fixed ports) | email + login way, else `"Kiro account"`; uid = login way | plugin: fail in place of the constant |
| kiro | 1 | Kiro CLI's / IDE's sign-in | — | reads kiro-cli / IDE files | not offered |
| kiro | 2 | Kiro API key (ksk_) | api-key | — | yes, by key |
| factory | 0 | Factory (device code) | device | email or userId, always | yes |
| factory | 1 | Factory API key | api-key | — | yes, by key |
| zed | 0 | Zed (browser) | paste-url | GitHub login, username, name, id, else `"Zed account"` | plugin: fail in place of the constant |
| grok | 0 | Grok Build (device code) | — | runs `grok login` here, which opens the browser | not offered (Q1) |
| grok | 1 | Grok Build's current sign-in | — | reads the CLI's home | not offered |
| grok | 2 | Import CPA Grok JSON | — | a file path here | not offered; plugin: take the JSON pasted |
| devin | 0 | Devin (browser) | paste-url | `metadata.email` = user, else `"Devin · <hash>"` of this sign-in | plugin: no fingerprint name, fail without the user |
| devin | 1 | Devin CLI's sign-in | — | reads `credentials.toml` | not offered |
| devin | 2 | Devin API key (cog_) | api-key | — | yes, by key |
| qoder, qoder-cn | 0 | Sign in with {site} | device | email or user id, may be none | yes |
| qoder, qoder-cn | 1 | {site} PAT | api-key-like (password prompt) | email or uid, always | yes |
| qoder-cn | 2 | Enterprise (VPC) | device | email or uid (vpc) | yes |
| qoder-cn | 3 | Enterprise dedicated domain | device / Lingma | device: as #0; Lingma reads Qoder CN IDE files | plugin: offer the device branch alone |
| workbuddy, workbuddy-ai | 0 | {site} account (browser) | device | display name, else `site.name`; uid may be "" | plugin: name by uid |
| workbuddy, workbuddy-ai | 1 | {site} desktop's sign-in | — | reads CodeBuddy files | not offered |
| workbuddy, workbuddy-ai | 2 | {site} API key | api-key | — | yes, by key |
| zcode | 0, 1 | ZCode: Z.ai / BigModel | device | email, name, id, else `"ZCode"`; uid when known | plugin: fail in place of the constant |
| zcode | 2 | a team's plan | device (select `site`) | as #0, `" (team)"` | plugin: as #0 |
| zcode | 3 | ZCode app's sign-in | — | reads `~/.zcode` | not offered |
| zcode | 4 | GLM Coding Plan API key | api-key (select `site`) | — | yes, by key |
| mimo (mimo-app) | 0 | Xiaomi account | device | userId, always | yes |
| commandcode (commandcode-plan) | 0 | Command Code (browser) | paste-key (to #2) | key from #2 | yes, by key |
| commandcode | 1 | CLI's sign-in | — | reads `~/.commandcode` | not offered |
| commandcode | 2 | API key | api-key | — | yes, by key |
| copilot (github-copilot) | 0 | Login with GitHub Copilot | device | none | plugin: name by GitHub login (`/user`) |
| agentcompass | 0 | decision provider | api-key (keyless, single) | constant key | not offered: nothing an area owns |
| agnes-code | 0 | desktop sign-in | — | reads the app's DPAPI files | not offered |
| agnes-code | 1 | paste accessToken | api-key (prompts) | username as typed | plugin: name by `/user/self` |
| agnes | 0–2 | browser (plan / personal / enterprise) | — | Chrome over CDP here; constant names | not offered |
| agnes | 3 | Agnes API key | api-key | — | yes, by key |
| agy-cli | 0, 1 | Sign in with Google / another | — | runs `agy` here | not offered |
| antigravity | 0 | Google (browser) | paste-url | email from userinfo, always | yes |
| antigravity | 1 | Import refresh token / JSON | text prompt | email from the pasted JSON, unchecked | plugin: always ask userinfo |
| astudio | 0, 1 | browser / desktop sign-in | — | opens a browser here / reads app files | not offered |
| astudio | 2 | paste modelBearerToken | api-key (prompts) | accountId or uid as typed | plugin: look the account up |
| atomcode | 0 | browser | device | email, username or id, always | yes |
| atomcode | 1 | CLI's sign-in | — | a file path here | not offered |
| autoclaw | 0 | 网页登录 | paste-url (fixed ports) | display name, else `"<vendor> 账号"` | plugin: name by user_id |
| autoclaw-cn | 0 | 手机验证码登录 | paste-code (phone prompt) | display name, else phone | plugin: name by user_id |
| catpaw | 0 | 网页登录 | device | userName, else a constant | plugin: name by uid |
| catpaw | 1 | 桌面端登录态 | — | reads the app's auth.json | not offered |
| catpaw | 2 | 手动粘贴 token | text prompt | `"手动粘贴"` for every token | plugin: look the account up |
| cline | 0 | Cline (browser) | device | email or uid, may be "" | yes |
| cline | 1 | Cline API key | api-key | — | yes, by key |
| cloudflare-workers-ai | 0 | API token | api-key (prompts) | none (`metadata.accountId` is CF's) | yes, by key |
| codearts, officeace | 0 (and Intl.) | Sign in with Huawei Cloud | paste-url | `accountId` = IAM id, may be "" (`ErrUnnamed`); writes its record itself | yes: its own write is held |
| codearts | 1 | CodeArts Agent on this machine | — | reads the IDE's state | not offered |
| doubao-work | 0 | 在豆包窗口中登录 | — | starts the Doubao app here | not offered |
| gcp-ai | 0 | ADC / gcloud | — | runs gcloud and Chrome here | not offered |
| goanyapi | 0 | GoAnyAPI (browser) | paste-url | none (single) | plugin: name the account |
| goanyapi | 1 | API key | api-key (single) | — | yes, by key; one account |
| gumloop | 0 | API key | api-key (keyless, prompts) | email as typed | plugin: no typed name |
| inkstone (intern-inkstone) | 0, 1 | browser sign-in | — | opens a browser here (`opened`) | not offered |
| inkstone | 2 | API key only | api-key | `inkstone-<fp(key)>` | yes, by key |
| inkstone | 3 | API key + console token | api-key | whoami name, always | yes |
| joycode | 0 | 浏览器登录 | paste-url | userName or userId, may be none | yes |
| joycode | 1 | ptKey | api-key (tenant) | — | yes, by key |
| kimi-work | 0 | 本机 Kimi Work | — | reads the app's files | not offered |
| loomy | 0 | 短信验证码登录 | paste-code (phone) | masked phone; uid when given | plugin: name by uid (two phones mask alike) |
| loomy | 1 | 手动粘贴 session | text prompt | the session's last 6 | plugin: look the account up |
| mclaw | 0 | 浏览器登录 | — | Chrome here | not offered |
| minimax (minimax-code, -global) | 0 | {site} account | device | email, name, uid, else `site.name` | plugin: fail in place of the constant |
| nous (nous-portal) | 0 | device code | device | JWT `sub`, when present | yes |
| openai-decisions | 0 | OpenAI API key | api-key | — | yes, by key |
| opendesign | 0 | Open Design account | device | email when present; uid | yes |
| opendesign | 1 | API key | api-key | — | yes, by key |
| orcaterm | 0 | WeChat QR | — | the QR page is on this computer's port 4080 | not offered; plugin: give the QR's URL |
| orcaterm | 1 | cookie pair | api-key | — | yes, by key |
| raccoon | 0, 2 | browser / paste callback | paste-url / paste-code | display name, `Raccoon-<uid>` or a constant | plugin: name by uid |
| raccoon | 1 | 桌面端登录态 | — | reads the app's token | not offered |
| river | 0 | API key | api-key | — | yes, by key |
| sap-aicore | 0 | credential file | — | a file path here | not offered |
| search1api | 0 | Search1API (browser) | paste-url | none (single) | plugin: name the account |
| search1api | 1 | API key | api-key (single) | — | yes, by key; one account |
| sensenova (sensenova-token-plan) | 0 | browser | — | Chromium here (`opened`) | not offered |
| sensenova | 1 | account password | prompts | username, always | yes |
| sensenova | 2 | API key only | api-key (keyless) | `key:<fp>` | yes, by key |
| tabbit | 0 | Tabbit app's sign-in | — | reads cookies and keychain | not offered |
| tabbit | 1 | paste token | api-key | — | yes, by key |
| trae (trae-cn, trae-global) | 0 | {site} account | paste-url | name or uid; uid | yes |
| warp | 0 | Warp app's sign-in | — | reads Warp's keychain | not offered |
| warp | 1 | refresh token | text prompt | email or sub, else `"warp"` | plugin: fail in place of the constant |
| zen-free (opencode-zen-free) | 0 | Zen 免费访问 | api-key (keyless, single) | constant `public` | not offered: nothing an area owns |

**What this left T-209** (built as [Plugin sign-ins](#plugin-sign-ins-t-209)).

- **The 13 subscriptions an area can sign in to today:**
  - Cursor: browser, key.
  - Kiro: key only.
  - Factory: device, key.
  - Zed: nothing.
  - Devin: key only.
  - Qoder and Qoder CN: device, PAT, VPC.
  - WorkBuddy and WorkBuddy AI: key only.
  - ZCode: GLM key only.
  - MiMo: device.
  - Command Code: browser via paste-key, key.
  - Grok: nothing. Every one of its methods needs this computer.
- **What plugin releases would add:**
  - Kiro's, Zed's and ZCode's browser and device sign-ins (fail in place
    of the constant name).
  - Devin's browser (no fingerprint name).
  - WorkBuddy's browser (name by uid).
  - Qoder CN's dedicated domain (offer the device branch alone).
  - Grok's import (take pasted JSON).
  - Each such change raises its mover's `min`.
- **The method list.** T-209's `/options` and `/catalog` list only the
  rows marked yes or yes by key. That list lives in magpie
  (package, version and method), not in the plugin, so a method a new
  plugin version adds is not offered until it is read.
  - Index alone isn't enough: a version's method order can change, and
    `way` is checked as §11.3 says.
  - A plugin that sets `opened`, or that has no URL and no `"code"`
    method (it works on this computer), is refused at start even when
    listed.
- **Signing in to a known account fails.** An area's sign-in to an
  account it owns already is `ErrKnown` too: `fresh` never replaces
  anything. If areas are to renew their own accounts this way, T-209 needs
  a path that signs in to that account's key (`plugin.Authorize` with
  the account) after `ownsRef`. Otherwise the answer is `conflict`, as
  for anyone's.
- **Claiming the account.** `ErrKnown` and `ErrUnnamed` map to
  `state:"conflict"` and `failed`. `area.ClaimUpTo` comes after
  `plugin.Saved`, as `claimOnDone` does. When the claim fails for any
  reason but `ErrTaken`, the saved record is signed out again
  (`plugin.SignOut`).
  - `ErrTaken` can't follow a fresh save: an account an area owns is in
    plugin-auth.json, so it was known, and its key was reserved while it
    signed in.
- **The owner's side** is built as
  [The owner's side](#the-owners-side-t-228) (T-228).

### The owner's side (T-228)

The owner's sign-in (`plugin.Authorize`, `plugin.APIKey`, not to a new
account only) folds into any account of the same name (`settle`). Before
T-228 an area's account was included: the area's tokens were replaced by
the owner's, and the owner's sign-in was the area's admin's from then on.

- `plugin.AreaOwned` (set by `internal/provider/plugin_area_keep.go`,
  `areaPluginKeys`) is the plugin-auth.json keys of the accounts an area
  owns, found as the area's claim was made:
  `<PluginID>/<LoginID of the name pluginNames gives>`. When areas.json
  can't be read (`area.ErrUnreadable`), or an area owns accounts and no
  plugin can be read, every account is kept. A plugin with an area ref
  no account's name gives now (a name a duplicate's " (slot)" changed,
  or an account gone) has every one of its accounts kept.
- `authorize`, `apiKey` and `callback` pass it to the host as `keep`;
  the area's own sign-ins (`fresh`) ignore it, since `saveNew` never
  folds.
- host.js `settle` with `keep`: when any account the sign-in is the same
  as (`sameAccount`) is kept, what was just saved at the sign-in's key
  is deleted again in the same `changeAuth`, and the answer is
  `{ ok: false, area: true }`, `plugin.ErrAreaOwned` in Go. The area's
  record is never written.
- A `single` plugin's sign-in goes to its one account's key: when that
  key is kept, `authorize` and `apiKey` answer the same before any step
  runs, and `save` checks it again.
- The plugin's own `client.auth.set` and `auth.remove` while the owner
  signs in leave a kept key alone (the CodeArts plugin writes its
  account's record by its id). The guard (`keepGuard`) ends with the
  sign-in, so a timer it started renews its own account after.
- A sign-in that saved over a record of its own key (one to an account
  named) puts that record back when it is refused, not deletes it.
- The owner's Factory key (`factoryKeyToPlugin`) goes through
  `plugin.ImportOwner`, `import` with `keep`: an area's account of the
  same email is `ErrAreaOwned` and the key's import fails.
- `pluginDone` finishes such a sign-in as `failed` with
  `ErrAreaOwned`'s words (the owner's window shows a failure's reason;
  it has no conflict view), logged as `conflict`. `PluginAPIKey` returns
  the error, logged as `conflict`; the paste-key path ends the sign-in
  with it, as it does a known account.

Not covered:

- `keep` is read when `plugin.Finish` is called. A browser sign-in that
  finishes by itself ("auto") calls it as it begins, so an area's claim
  made while it waits is not seen. Such a claim is the area's own sign-in
  to the same vendor account in those minutes.
- A mover carrying a built-in's accounts (`plugin.Import`) still
  settles without `keep`: refusing one would fail a move that has no
  way back.
- The keep list is made from names (`pluginNames`), not marked on the
  record, so it can only be as right as those names; a mark written
  by `saveNew` and read in `settle`'s lock would not depend on them.

Tests: `TestSignInOwner*` and `TestSignInOwnerImportLeavesAnAreasAccount` in
`internal/plugin/plugin_area_owned_test.go`,
`TestSignInOwnerOfAnAreasPluginAccountFails` and
`TestSignInOwnerUnmappedAreaRefKeepsThePlugin` in
`internal/provider/plugin_area_keep_test.go`; each is red with its
check taken out (`kept` false, the hook unset, the guard never off,
`import` without keep, an unmapped ref not widened).


## Verification

`go test -race ./internal/area`; `go test ./internal/gateway -run
'TestAdmin|TestReadOnlyKeyEveryRoute|TestKeySlotEveryRoute'`; `go test
./internal/provider -run 'TestAreaSignIn|TestAreaLetsGo|TestAttachOnlyOwnIP|TestReleaseClearsAreaProxy|TestSweepAreaProxies'`;
`go test ./internal/renewal ./internal/netproxy -run 'Area|Owner|OldIPs'`;
`go test ./internal/gateway -run 'TestAdmin(IPRenewalDays|ProbeProxy|Batch|Reread|ProxyChecksHoldSpend)'`;
`go test ./internal/provider -run TestAreaSweepBesideARereadMove`;
`go test ./internal/gateway -run 'TestAdmin(SignInThroughAreaProxy|SignInCallbackPicksAreaProxy|DeviceSignInProxyFixed|KeyAndTokenTakeAreaProxy|SignInHoldsItsProxyPublic|SignInRefusedPasteKeepsItsPick)'`;
`go test ./internal/plugin -run 'TestSignInNew|TestAPIKeyNew'`;
`go test ./internal/plugin ./internal/provider -run TestSignInOwner`;
`go test ./internal/gateway -run TestAdminPlugin`;
`node --test internal/gui/tests/proxy-ips-batch-lines.test.cjs`;
`go test ./internal/gui -run TestIPAreaRoute`; `go test ./internal/gui -run
TestAreasRoutes`; `go test ./internal/gateway -run TestAdminConsole`. `TestAdminKeyEveryRoute` fails
when a route is under `/v1/magpie/admin` and in no role's list, or when an
admin key reaches any other route or an upstream.
