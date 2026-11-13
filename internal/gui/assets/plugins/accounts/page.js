// Models › Accounts' code (client.js has its markup and hooks): run after
// app.js, whose providers, allowances (quotas) and account helpers it
// reads. An account's Use, Pause and Stop follow its row's tick in the
// provider's editor (renderAccounts), so the two never say different
// things; Sign in again opens that editor with the sign-in under way.

// the filters and the view (rows or cards), kept per machine as the
// reader left them: a word, the provider, the state, the gateway key, when
// it was added, whether its 5 hours and its week have room, and which the
// user marked show ("" neither banned nor disabled, "banned", "disabled",
// "any"), and the order (ACX_SORTS', "" the providers' own)
let acxQuery = "", acxProv = "", acxState = "all", acxKey = "", acxAdded = "", acx5h = "", acx7d = "", acxBanned = "", acxView = "list", acxSort = "";
try { ({ q: acxQuery = "", p: acxProv = "", s: acxState = "all", k: acxKey = "", a: acxAdded = "", h5: acx5h = "", h7: acx7d = "", b: acxBanned = "", v: acxView = "list", o: acxSort = "" } = JSON.parse(localStorage.getItem("magpie.acxFilter") || "{}")); } catch {}
const acxKeep = () => { try { localStorage.setItem("magpie.acxFilter", JSON.stringify({ q: acxQuery, p: acxProv, s: acxState, k: acxKey, a: acxAdded, h5: acx5h, h7: acx7d, b: acxBanned, v: acxView, o: acxSort })); } catch {} };
// the gateway keys that work, each with the accounts it may use, as the
// gateway holds it (/api/accounts/keys), and when read
let acxKeys = null, acxKeysAt = 0;
// the last read of them failed: a key picked stays in sight to be cleared
let acxKeysFailed = false;
// the callers held after authentication_failed and the hold's hours
// (/api/auth-holds), and when read
let acxHolds = null, acxHoldHours = 12, acxHoldsAt = 0;
// the last 5 hours' requests by account (usage's accounts), and when read
let acxUsage = null, acxUsageAt = 0;
// today's, the last 7 and 30 days' by account, by period, and when read:
// read for the cards and the order by cost alone (acxSpansWanted)
let acxSpans = {}, acxSpansAt = 0;
// Add account (需求方 2026-11-13): "" closed, "type" its type picked,
// else the agent signing in here; and the account it added
let acxAdding = "", acxAdded1 = "";
// what the list was drawn from, so a change elsewhere draws it again
let acxSeen = null, acxWatch = 0;
// the account whose start date is being picked (renewEditor, T-154), by
// its provider and its name in lower case
let acxRenewing = "";

function acxMount() {
  const q = $("#acxQ");
  q.value = acxQuery;
  q.setAttribute("aria-label", t("Search accounts"));
  q.oninput = () => { acxQuery = q.value; acxKeep(); acxDraw(); };
  q.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape" && q.value) { q.value = ""; q.oninput(); } };
  $("#acxAdd").onclick = () => { if (acxAdding) acxAddClose(); else { acxAdding = "type"; acxAdded1 = ""; acxAddDraw(); } };
  // every account's allowance read again, as the Usage page's Refresh all
  const all = $("#acxReadAll");
  all.title = t("Read every account's allowance again");
  all.onclick = () => {
    if (all.classList.contains("busy")) return;
    all.classList.add("busy");
    loadQuotas("all").finally(() => { all.classList.remove("busy"); if (view === "accounts") acxDraw(); });
  };
  const re = $("#acxReload");
  re.title = t("Read the accounts, their requests and the gateway keys again");
  re.onclick = () => {
    if (re.classList.contains("busy")) return;
    re.classList.add("busy");
    acxLoad(true).finally(() => re.classList.remove("busy"));
  };
}

// acxSpansWanted: the cards show today's, 7 and 30 days' requests and
// cost, and the order by cost is by 30 days'
const acxSpansWanted = () => acxView === "cards" || acxSort === "cost30";
const ACX_SPANS = [["today", "Today"], ["7d", "7 days"], ["30d", "30 days"]];
// acxSpansLoad reads them when wanted and a minute old, or asked again
function acxSpansLoad(again) {
  if (!acxSpansWanted() || (!again && Date.now() - acxSpansAt < 60e3)) return Promise.resolve();
  acxSpansAt = Date.now();
  const zone = typeof usageZoneQ === "function" ? usageZoneQ() : "";
  // a failed read is asked again on the next draw's load (review L4)
  return Promise.all(ACX_SPANS.map(([k]) => api("usage?period=" + k + zone).then((u) => { acxSpans[k] = u?.accounts || []; }, () => { acxSpansAt = 0; })));
}

// acxLoad: the providers (the shell's, loads: "providers"), the allowances
// as the Usage page reads them, and the last 5 hours' requests; drawn once
// each comes in, and kept drawn while the page is open (acxFollow)
async function acxLoad(again) {
  // the proxies Proxy IPs keeps, offered when an account is added
  window.ipProxiesRead?.();
  acxFollow();
  if (providers) acxDraw();
  else acxSkeleton();
  const reads = [
    loadProviders().catch(statusErr),
    (!quotas || again || Date.now() - quotasAt > 60e3 ? loadQuotas() : Promise.resolve()).catch(() => {}),
    (!acxUsage || again || Date.now() - acxUsageAt > 30e3 ? api("usage?period=" + encodeURIComponent("last:5h") + (typeof usageZoneQ === "function" ? usageZoneQ() : ""))
      .then((u) => { acxUsage = u; acxUsageAt = Date.now(); }, () => {}) : Promise.resolve()),
    (!acxKeys || again || Date.now() - acxKeysAt > 30e3 ? api("accounts/keys")
      .then((k) => { acxKeys = k?.keys || []; acxKeysAt = Date.now(); acxKeysFailed = false; }, () => { acxKeysFailed = true; }) : Promise.resolve()),
    (!acxHolds || again || Date.now() - acxHoldsAt > 30e3 ? api("auth-holds").then(acxHoldsRead, () => {}) : Promise.resolve()),
    // the growth policies an account can pick (plugins/growth/pick.js, T-133)
    growthListLoad(again),
    acxSpansLoad(again),
  ];
  for (const r of reads) r.then(() => { if (view === "accounts") acxDraw(); });
  await Promise.all(reads);
}

// acxFollow draws the list again when what it shows changed: an account
// used or paused here or in the editor (accountAction sets providers), an
// allowance read again (quotasAt). Ends when the page is left.
function acxFollow() {
  if (acxWatch) return;
  acxWatch = setInterval(() => {
    if (view !== "accounts" || document.hidden) {
      if (view !== "accounts") {
        clearInterval(acxWatch); acxWatch = 0;
        // Add account closes with the page, its sign-in canceled, as an
        // editor left on Providers takes its sign-in with it (review M1)
        if (acxAdding) acxAddClose();
      }
      return;
    }
    // not under a start date being typed: its box would lose a half-typed
    // date and the focus (T-154 review L4)
    if (document.activeElement?.closest?.("#acxList .renew-edit")) return;
    if (acxSeen && (acxSeen.providers !== providers || acxSeen.quotas !== quotasAt)) acxDraw();
  }, 700);
}

function acxSkeleton() {
  const list = $("#acxList");
  list.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const row = el("div", "row acx acx-sk");
    row.append(el("span", "skeleton acx-sk-ic"), el("span", "skeleton acx-sk-name"), el("span", "grow"), el("span", "skeleton acx-sk-bar"));
    list.append(row);
  }
}

const acxLow = (s) => String(s || "").trim().toLowerCase();

// acxRows: every account of every provider signed in to, in the providers'
// order and each one's own (loginsInOrder), with what it is and what can
// be done to it, worked out as its editor row works it out
function acxRows() {
  const out = [];
  for (const p of providers?.providers || []) {
    const a = p.account;
    if (!a?.logins?.length) continue;
    const sub = subOf(a.agent);
    const ls = loginsInOrder(a, p);
    // a Claude sign-in refused, or one the user marked banned or disabled
    const unusable = (l) => a.agent === "claude" && !!l.lapsed || l.banned || l.disabled;
    const isOn = (l) => !unusable(l) && !l.paused && (l.active || l.on);
    const several = ls.filter((l) => !unusable(l) && ((l.active && !l.paused) || l.on)).length > 1;
    const pausable = (a.agent === "claude" || a.agent === "codex") && ls.some((l) => !unusable(l) && !l.active && l.on);
    for (const l of ls) {
      // marked banned or disabled by the user: no sign-in to make again,
      // nothing to use
      const banned = !!l.banned, disabled = !!l.disabled, off = banned || disabled;
      // resting after authentication_failed: it may be banned, shown with
      // the others (the user is to act on it), and used by nothing till then
      const authFailed = !off && !!l.authFailed;
      const again = !off && !authFailed && (unusable(l) || signInLapsed(a.agent, l));
      const state = banned ? "banned" : disabled ? "disabled" : authFailed ? "authfail" : again ? "again" : l.paused ? "paused" : isOn(l) ? "on" : "off";
      // the tick's own action, or none (the agent's own account, fixed)
      let act = null;
      if (off || authFailed || unusable(l)) act = null;
      else if (l.active && (pausable || l.paused)) act = l.paused ? ["on", "Resume"] : ["off", "Pause"];
      else if (l.active && sub?.own && several) act = ["off", "Take out of rotation"];
      else if (!l.active) act = isOn(l) ? ["off", "Take out of rotation"] : ["on", "Add to rotation"];
      const q = (quotas || []).find((x) => x.provider === p.id && acxLow(x.user) === acxLow(l.user));
      const used = (acxUsage?.accounts || []).filter((g) => g.provider === p.id && acxLow(g.account) === acxLow(l.user));
      // tokens are in and out, as tokensOf counts them: an account's
      // usage has no tokens of its own
      const sum = (k) => used.reduce((n, g) => n + (g[k] || 0), 0);
      const added = p.accountAdded?.[acxLow(l.user)] || "", firstUsed = p.accountFirstUsed?.[acxLow(l.user)] || "";
      // how the growth policy holds it now, as the gateway does (provider.AccountGrowthOf)
      const growth = p.accountGrowth?.[acxLow(l.user)] || null;
      // the growth policy it picked itself, "" when it follows its subscription's (T-133)
      const pick = p.accountGrowthPolicy?.[acxLow(l.user)] || "";
      // its ramp-up found done as its policy began counting the days used (T-150)
      const graduated = p.accountGraduated?.[acxLow(l.user)] || "";
      // its ramp-up skipped, till resumed (provider.AccountGrowthSkippedOf, T-158)
      const skipped = p.accountGrowthSkipped?.[acxLow(l.user)] || "";
      // its paid period from the start date the user told, as the server
      // worked it out (provider.AccountOpened, renewal.Of, T-154)
      const renew = p.accountRenewal?.[acxLow(l.user)] || null;
      // today's, 7 and 30 days' the same way, once read
      const spans = {};
      for (const [k] of ACX_SPANS) {
        if (!acxSpans[k]) continue;
        const gs = acxSpans[k].filter((g) => g.provider === p.id && acxLow(g.account) === acxLow(l.user));
        const n = (f) => gs.reduce((m, g) => m + (g[f] || 0), 0);
        spans[k] = { calls: n("calls"), tokens: n("input") + n("output"), cost: n("cost"), unpriced: n("unpriced") };
      }
      out.push({ p, a, l, sub, state, banned, disabled, off, authFailed, again, act, q, added, firstUsed, growth, pick, graduated, skipped, renew, spans, calls: sum("calls"), errors: sum("errors"), tokens: sum("input") + sum("output"), cost: sum("cost"), unpriced: sum("unpriced") });
    }
  }
  return out;
}

const ACX_STATES = [["all", "All"], ["on", "In rotation"], ["again", "Sign in again"], ["off", "Not in rotation"]];
const ACX_SAID = { on: "In rotation", paused: "Paused", again: "Sign in again", off: "Not in rotation", banned: "Banned", disabled: "Disabled", authfail: "Sign-in refused · may be banned" };
const ACX_VIEWS = [["list", "List view"], ["cards", "Card view"]];
const ACX_ADDED = [["7", "Added in the last 7 days"], ["30", "Added in the last 30 days"], ["old", "Added over 30 days ago"], ["unknown", "Added before magpie kept it"]];
// a window's room: "room" left in it, "out" used up
// the accounts the user marked: left out (""), those banned, those
// disabled (one with both is in each), or every one
const ACX_BANNED = [["banned", "Banned"], ["disabled", "Disabled"], ["any", "All"]];
const ACX_ROOM = { "5h": [["room", "5 hours: room left"], ["out", "5 hours: used up"]], "7d": [["room", "7 days: room left"], ["out", "7 days: used up"]] };
// the orders offered beside the providers' own ("")
const ACX_SORTS = [["added", "Newest first"], ["5h", "5 hours used, most first"], ["7d", "7 days used, most first"], ["cost30", "30 days' cost, most first"], ["name", "Name"]];
// acxUsedOf: the most used of an account's 5 hours or weeks, -1 unread
const acxUsedOf = (q, kind) => Math.max(-1, ...acxWindowsOf(q, kind).map((w) => (w.unlimited ? 0 : w.used || 0)));
// acxSorted: rows in the order picked, the providers' own among equals
function acxSorted(rows) {
  const by = {
    added: (r) => -(new Date(r.added || 0).getTime() || 0),
    "5h": (r) => -acxUsedOf(r.q, "5h"),
    "7d": (r) => -acxUsedOf(r.q, "7d"),
    cost30: (r) => -(r.spans["30d"]?.cost || 0),
  }[acxSort];
  if (acxSort === "name") return rows.map((r, i) => [acxLow(accountNameOf(r.p, r.l.user) || r.l.user), i, r]).sort((x, y) => x[0].localeCompare(y[0]) || x[1] - y[1]).map((x) => x[2]);
  if (!by) return rows;
  return rows.map((r, i) => [by(r), i, r]).sort((x, y) => x[0] - y[0] || x[1] - y[1]).map((x) => x[2]);
}

// acxWindows: an account's windows as bars, a failed reading's none
const acxWindows = (q) => (q && !q.error ? q.windows || [] : []).filter((w) => !w.text);
// acxWindowsOf: an account's 5 hours ("5h") or its weeks ("7d"), as the
// menu bar tells them (trayKind: "7d", or "7d:<name>" where an account
// has two of one span, Antigravity's pools), else by name; the windows
// the menu bar shows only when picked (trayExtra: one model's) only when
// there is none of the whole
function acxWindowsOf(q, kind) {
  const named = kind === "5h" ? /^5 ?h(ours?)?$/i : /^(7 days|weekly|week)$/i;
  const ws = acxWindows(q).filter((w) => w.trayKind && !w.trayKind.startsWith("name:")
    ? w.trayKind === kind || w.trayKind.startsWith(kind + ":")
    : named.test(String(w.name || "").trim()));
  const whole = ws.filter((w) => !w.trayExtra);
  return whole.length ? whole : ws;
}
// acxRoom: whether the account has room in one of those windows ("room"),
// has each used up ("out"), or has none of them read ("")
function acxRoom(q, kind) {
  const ws = acxWindowsOf(q, kind);
  if (!ws.length) return "";
  return ws.some((w) => w.unlimited || (w.used || 0) < 100) ? "room" : "out";
}
// acxAddedMatches: an account added at added is in ACX_ADDED's v: the
// last 7 or 30 days (the 30 holding the 7), over 30 days ago, or not kept
function acxAddedMatches(added, v) {
  const at = added ? new Date(added).getTime() : NaN;
  if (!(at > 0)) return v === "unknown";
  const days = (Date.now() - at) / 864e5;
  return v === "7" ? days <= 7 : v === "30" ? days <= 30 : v === "old" ? days > 30 : false;
}
// acxKeyAllows: the gateway key k may use the account (all, for a key held
// to none): accountsKeyRoutes' list, by provider and name in lower case
const acxKeyAllows = (k, r) => !k || k.all || (k.accounts || []).includes(acxLow(r.p.id + "/" + r.l.user));
// acxPickOpts: a fixed menu's options, translated
const acxPickOpts = (xs) => xs.map(([v, n]) => ({ v, name: t(n), note: "" }));

// acxHoldsRead keeps /api/auth-holds' answer
function acxHoldsRead(h) {
  acxHolds = h?.holds || [];
  acxHoldHours = h?.hours ?? 12;
  acxHoldsAt = Date.now();
}

// the hold's lengths offered, in hours: 0 holds nobody
const ACX_HOLD_HOURS = [0, 1, 6, 12, 24, 72, 168];

// acxHeldDraw: who is held after authentication_failed, each with Release,
// and how long a caller is held
function acxHeldDraw() {
  const box = $("#acxHeld");
  if (!box) return;
  box.replaceChildren();
  const holds = acxHolds || [];
  box.hidden = !acxHolds;
  const head = el("div", "acx-held-head");
  head.append(el("span", "", t(holds.length === 1 ? "1 caller held after authentication_failed" : "{n} callers held after authentication_failed", { n: holds.length })));
  const pick = el("button", "sess-pick acx-hold-hours");
  pick.type = "button";
  const hours = (n) => (n === 0 ? t("Hold nobody") : t(n === 1 ? "Held 1 hour" : "Held {n} hours", { n }));
  sessPick(pick, hours(12), String(acxHoldHours), ACX_HOLD_HOURS.map((n) => ({ v: String(n), name: hours(n), note: "" })), "After authentication_failed", (v) => {
    api("auth-holds/hours", { hours: Number(v) }).then((h) => { acxHoldsRead(h); acxHeldDraw(); }, statusErr);
  });
  pick.title = t("How long a caller is told their usage limit is reached after Anthropic answers one of their requests authentication_failed. It may be a ban, which may follow the caller to their next account");
  head.append(pick);
  box.append(head);
  for (const h of holds) {
    const row = el("div", "acx-held-row");
    row.dataset.holder = h.holder;
    const who = h.kind === "key" ? t("Gateway key {name}", { name: h.name || h.who }) : h.kind === "session" ? t("Session {id}", { id: h.who }) : h.who;
    row.append(el("span", "acx-held-who", who), el("span", "acx-held-until", t("held until {time}", { time: new Date(h.until).toLocaleString(intlLang()) })));
    row.lastChild.title = t("After {account} answered authentication_failed", { account: h.provider + " · " + h.account });
    row.append(el("span", "grow"));
    const rel = el("button", "text acx-release", t("Release hold"));
    rel.type = "button";
    rel.onclick = () => { rel.classList.add("busy"); api("auth-holds/release", { holder: h.holder }).then((x) => { acxHoldsRead(x); acxHeldDraw(); }, statusErr); };
    row.append(rel);
    box.append(row);
  }
}

function acxDraw() {
  const page = $("#view-accounts");
  if (!page || !providers) return;
  acxSeen = { providers, quotas: quotasAt };
  const all = acxRows();
  acxHeldDraw();
  // the provider filter offers those with accounts; one gone is dropped
  const provs = [...new Map(all.map((r) => [r.p.id, r.p])).values()];
  if (acxProv && !provs.some((p) => p.id === acxProv)) acxProv = "";
  sessPick($("#acxProvider"), "All providers", acxProv,
    provs.map((p) => ({ v: p.id, name: p.name, note: t(all.filter((r) => r.p.id === p.id).length === 1 ? "1 account" : "{n} accounts", { n: all.filter((r) => r.p.id === p.id).length }) })),
    "Provider", (v) => { acxProv = v; acxKeep(); acxDraw(); });
  // shown even with one provider, so the filter's place doesn't jump
  $("#acxProvider").hidden = !provs.length;
  // a gateway key gone is dropped; the filter shows once there is a key
  const keys = acxKeys || [];
  if (acxKey && acxKeys && !keys.some((k) => k.id === acxKey)) acxKey = "";
  const key = keys.find((k) => k.id === acxKey);
  sessPick($("#acxKey"), "Any gateway key", acxKey,
    keys.map((k) => ({ v: k.id, name: k.name, note: k.all ? t("Not held to accounts") : t(k.accounts.length === 1 ? "1 account" : "{n} accounts", { n: k.accounts.length }) })),
    "Gateway key", (v) => { acxKey = v; acxKeep(); acxDraw(); });
  // a key picked whose list didn't read stays in sight, to be cleared;
  // its filter waits for the list
  $("#acxKey").hidden = !keys.length && !acxKey;
  if (acxKey && !key && acxKeysFailed) $("#acxKey").title = t("Couldn't read the gateway keys");
  const fixed = (id, all, xs, value, head, set) => {
    const b = $(id);
    sessPick(b, all, value, acxPickOpts(xs), head, (v) => { set(v); acxKeep(); acxDraw(); });
    b.hidden = !all.length || !xs.length;
    b.title = "";
  };
  if (!ACX_ADDED.some(([v]) => v === acxAdded)) acxAdded = "";
  if (!ACX_ROOM["5h"].some(([v]) => v === acx5h)) acx5h = "";
  if (!ACX_ROOM["7d"].some(([v]) => v === acx7d)) acx7d = "";
  if (!ACX_BANNED.some(([v]) => v === acxBanned)) acxBanned = "";
  fixed("#acxAdded", "Added any time", ACX_ADDED, acxAdded, "Added", (v) => { acxAdded = v; });
  fixed("#acx5h", "5 hours: any", ACX_ROOM["5h"], acx5h, "5 hours", (v) => { acx5h = v; });
  fixed("#acx7d", "7 days: any", ACX_ROOM["7d"], acx7d, "7 days", (v) => { acx7d = v; });
  fixed("#acxBanned", "Normal", ACX_BANNED, acxBanned, "Status", (v) => { acxBanned = v; });
  if (!ACX_SORTS.some(([v]) => v === acxSort)) acxSort = "";
  fixed("#acxSort", "Providers' order", ACX_SORTS, acxSort, "Sort accounts", (v) => { acxSort = v; acxSpansLoad().then(() => { if (view === "accounts") acxDraw(); }); });
  if (!ACX_VIEWS.some(([v]) => v === acxView)) acxView = "list";
  const vw = $("#acxView");
  vw.replaceChildren(segs(ACX_VIEWS.map(([k, n]) => [k, t(n)]), acxView, (k) => { acxView = k; acxKeep(); acxDraw(); acxSpansLoad().then(() => { if (view === "accounts") acxDraw(); }); }));
  vw.hidden = !all.length;
  const st = $("#acxState");
  st.replaceChildren(segs(ACX_STATES.map(([k, n]) => [k, t(n)]), acxState, (k) => { acxState = k; acxKeep(); acxDraw(); }));
  st.hidden = !all.length;

  const words = acxLow(acxQuery);
  const rows = acxSorted(all.filter((r) => (!acxProv || r.p.id === acxProv)
    && (acxBanned === "any" || (acxBanned ? r[acxBanned] : !r.off))
    && (acxState === "all" || r.state === acxState)
    && acxKeyAllows(key, r)
    && (!acxAdded || acxAddedMatches(r.added, acxAdded))
    && (!acx5h || acxRoom(r.q, "5h") === acx5h)
    && (!acx7d || acxRoom(r.q, "7d") === acx7d)
    && (!words || [r.l.user, accountNameOf(r.p, r.l.user), r.p.name, r.l.plan].some((s) => acxLow(s).includes(words)))));

  const n = (k) => all.filter((r) => r.state === k).length;
  const sum = $("#acxSum");
  sum.replaceChildren();
  if (all.length) {
    const parts = [t(all.length === 1 ? "1 account" : "{n} accounts", { n: all.length }), t("{n} in rotation", { n: n("on") })];
    if (n("paused")) parts.push(t("{n} paused", { n: n("paused") }));
    const nb = all.filter((r) => r.banned).length, nd = all.filter((r) => r.disabled).length;
    if (nb) parts.push(t("{n} banned", { n: nb }));
    if (nd) parts.push(t("{n} disabled", { n: nd }));
    // the line to act on is the warning's, said last
    if (n("again")) parts.push(t("{n} to sign in again", { n: n("again") }));
    // and the paid periods due, soon or ended (T-154)
    const due = all.filter((r) => r.renew?.due).length;
    if (due) parts.push(t(due === 1 ? "1 to renew" : "{n} to renew", { n: due }));
    sum.append(...parts.flatMap((s, i) => (i ? [" · ", el("span", "", s)] : [el("span", "", s)])));
    if (n("again") || due) sum.lastChild.classList.add("acx-warn");
    if (n("again") && due) sum.lastChild.previousElementSibling.classList.add("acx-warn");
  }

  const list = $("#acxList");
  list.replaceChildren();
  list.hidden = !rows.length;
  const cards = acxView === "cards";
  list.classList.toggle("acx-cards", cards);
  for (const r of rows) list.append(cards ? acxCard(r) : acxRow(r));
  // every row's actions as wide as the widest, so the windows line up
  list.style.removeProperty("--acx-acts");
  const widest = Math.max(0, ...[...list.querySelectorAll(".row.acx > .acts")].map((x) => x.getBoundingClientRect().width));
  if (widest) list.style.setProperty("--acx-acts", Math.ceil(widest) + "px");
  const note = $("#acxNote");
  note.replaceChildren();
  if (!all.length) {
    note.append(el("span", "", t("No subscription account yet. Add one with Add account, or in Providers: it is listed here once signed in.") + " "));
    const go = el("button", "link", t("Open Providers"));
    go.onclick = () => show("providers");
    note.append(go);
  } else if (!rows.length) note.textContent = t("No account matches these filters.");
  else note.textContent = t("Requests are the gateway's of the last 5 hours. Windows are as the vendor last told them.");
}

// acxActs: what can be done to an account — its tick's own action or Sign
// in again, Read again and Open — for its row and its card alike
function acxActs(r) {
  const { p, a, l, q } = r;
  const acts = el("span", "acts");
  if (r.again) {
    const b = el("button", "text primary acx-again", t("Sign in again"));
    b.type = "button";
    b.onclick = async () => {
      // the editor opens, the sign-in under way in it, as Providers' line does
      await openProvider(p.id);
      if (view !== "providers") return;
      // one added from a setup-token takes a new one, as its editor row does
      if (l.token) startClaudeToken();
      else startSignIn(a.agent, false, signInAgainSite(a.agent, l.user), { user: l.user });
    };
    acts.append(b);
  } else if (r.act) {
    const [to, said] = r.act;
    const b = el("button", "text acx-use", t(said));
    b.type = "button";
    // the On toggle's words, told from Disable: out of rotation it stays signed in and read
    if (said === "Take out of rotation") b.title = t("Take it out of the gateway's rotation: it stays signed in and its usage is still read, unlike Disable");
    b.onclick = () => {
      b.classList.add("busy");
      accountAction("login/" + to, { agent: a.agent, user: l.user }).then(() => acxDraw());
    };
    acts.append(b);
  }
  // a banned or disabled one's allowance isn't read (provider/banned.go)
  if (q && !r.off && !r.authFailed) acts.append(quotaRefresh(q, () => acxDraw()));
  if (r.authFailed) {
    // its rest after authentication_failed, lifted: routed to again
    const b = el("button", "text acx-lift", t("Use again"));
    b.type = "button";
    b.title = t("Anthropic answered it authentication_failed: it may be banned. Use it again only if it isn't: the gateway routes to it once more");
    b.onclick = () => { b.classList.add("busy"); accountAction("login/lift", { agent: a.agent, user: l.user }).then(() => acxDraw()); };
    acts.append(b);
  }
  // the user's marks, banned by its vendor or disabled by them, or their lifting
  for (const [on, set, unset, cls, word, unword, tip, untip] of [
    [r.banned, "ban", "unban", "acx-ban", "Mark banned", "Lift ban", "Its vendor banned it: the gateway never sends it a request, and its usage history stays", "Use it again: the gateway routes to it and its allowance is read"],
    [r.disabled, "disable", "enable", "acx-disable", "Disable", "Enable", "Turn it off: the gateway never sends it a request, and its usage history stays", "Use it again: the gateway routes to it and its allowance is read"],
  ]) {
    const b = el("button", "text " + cls, t(on ? unword : word));
    b.type = "button";
    b.title = t(on ? untip : tip);
    b.onclick = () => {
      b.classList.add("busy");
      accountAction("login/" + (on ? unset : set), { agent: a.agent, user: l.user }).then(() => acxDraw());
    };
    acts.append(b);
  }
  const open = el("button", "text acx-open", t("Open"));
  open.type = "button";
  open.title = t("Open {name} in Providers, with this account's settings", { name: p.name });
  open.onclick = () => openProvider(p.id);
  acts.append(open);
  return acts;
}

// acxGrowthSaid: how the growth policy holds an account, from the gateway's
// own count (provider.AccountGrowthOf): ramping up, its day of the days it
// holds (from when it was added, for one the gateway doesn't serve; from
// its first use, not yet, for one not used), the share it holds the 5
// hours and the week to now, and a stage's next day. Nothing is worked out
// here.
function acxGrowthSaid(g) {
  const parts = [t("Ramping up")];
  if (g.waiting) parts.push(t("starts at first use"));
  else if (g.byAdded) parts.push(g.days ? t("day {day} of {days} since added", { day: g.day, days: g.days }) : t("day {day} since added", { day: g.day }));
  else parts.push(g.days ? t("day {day} of {days}", { day: g.day, days: g.days }) : t("day {day}, held for good", { day: g.day }));
  // its policy counts only the days it was used on (T-134)
  if (g.usedDays && !g.waiting) parts.push(t("counting days used"));
  parts.push(g.fiveHours === g.week ? t("now {n}%", { n: g.fiveHours }) : t("now 5h {five}% · week {week}%", { five: g.fiveHours, week: g.week }));
  if (g.next) parts.push(t("next step on day {day}", { day: g.next }));
  return parts.join(" · ");
}

// acxGrowth: an account's ramp-up as a button, or the growth policy it
// picked itself while that holds none of its windows (T-133). It offers
// the policies to pick for the account alone (growthAccountOpts, none
// until they are read) and, while ramping up, Skip ramp-up: the account
// kept skipped apart from its windows' caps (provider.SkipGrowth, T-140),
// so a cap set later on a window or the account holds as the user's
function acxGrowth(r) {
  const { p, l, growth: g } = r;
  const picked = growthAccountPicked(p, l.user);
  const b = el("button", "acx-grow", g ? acxGrowthSaid(g) : t("Growth policy: {name}", { name: picked.policy ? growthName(picked.policy) : picked.id }));
  b.type = "button";
  const choices = growthAccountOpts(p).length > 0;
  b.title = (g ? t("{policy} holds this new account to a share of its windows, rising to 100%. Click to skip the ramp-up", { policy: g.builtin ? t(g.name) : g.name })
    + (g.byAdded ? "\n" + t("Its days count from when it was added: its agent asks its vendor itself, so magpie never sees its first use") : "")
    + (g.usedDays ? "\n" + t("Only the days magpie served a request of it count: a day it isn't used doesn't move it on") : "")
    + (g.calendarOnly ? "\n" + t("Its policy counts the days used, but magpie doesn't send this account's requests, so every day counts") : "")
    + (g.usedDaysUnknown ? "\n" + t("Its policy counts the days used, but magpie hasn't read them from its usage yet, so every day counts till then") : "")
    + (g.waiting ? "\n" + t("Its days start on the first request its vendor serves through magpie") : "")
    + (choices ? "\n" + t("Its growth policy can be picked here too, for this account alone") : "")
    : t("Picked for this account alone, before its subscription's. Click to pick another"))
    + (picked.gone ? "\n" + t("The policy it picked ({id}) is gone: it is held by its subscription's until you pick another", { id: picked.id }) : "");
  b.setAttribute("aria-haspopup", "menu");
  b.onclick = (e) => acxGrowthMenu(e, b, r, !!g);
  return b;
}

// acxGrowthMenu: the account's growth policy to pick (Follow the
// subscription clears its own), with Skip ramp-up when skip; a pick that
// is gone ticks nothing, so Follow the subscription saves too
async function acxGrowthMenu(e, b, r, skip) {
  e.stopPropagation();
  if (b.classList.contains("open")) return closeProtoMenu();
  const { p, l } = r;
  await growthListLoad();
  const picked = growthAccountPicked(p, l.user);
  const opts = growthAccountOpts(p);
  if (skip) opts.push({ v: "!skip", name: "Skip ramp-up", note: "Its windows go to 100%, or to a cap you set on one or on the account" });
  if (!opts.length) return;
  const head = skip && opts.length === 1 ? "Ramping up" : "Growth policy this account is held by";
  openProtoMenu(b, opts, picked.gone ? "!gone" : picked.id, (v) => {
    b.classList.add("busy");
    const done = v === "!skip"
      ? accountAction("provider/skipgrowth", { id: p.id, account: l.user }, t("{who} skipped its ramp-up", { who: accountNameOf(p, l.user) || l.user }))
      : growthAccountSave(p, l.user, v);
    done.then(() => acxDraw());
  }, head);
}

// acxSkipped: an account whose ramp-up the user skipped (T-158), as a
// button offering Resume ramp-up (provider/resumegrowth): its skip goes,
// and its growth policy holds it again from the day its days count from.
// The policies to pick for it alone stay in its menu (a pick ends the skip
// too, provider.SetAccountGrowthPolicy), and one it picked is said
function acxSkipped(r) {
  const { p, l } = r;
  const picked = growthAccountPicked(p, l.user);
  const b = el("button", "acx-grow acx-skipped", t("Skipped ramp-up"));
  b.type = "button";
  b.title = t("Its growth policy holds none of its windows: you skipped its ramp-up. Click to resume it")
    + (picked.id ? "\n" + t("Growth policy: {name}", { name: picked.policy ? growthName(picked.policy) : picked.id }) : "");
  b.setAttribute("aria-haspopup", "menu");
  b.onclick = async (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    await growthListLoad();
    const now = growthAccountPicked(p, l.user);
    const opts = [{ v: "!resume", name: "Resume ramp-up", note: "Its growth policy holds it again, from the day its days count from" }].concat(growthAccountOpts(p));
    openProtoMenu(b, opts, now.gone ? "!gone" : now.id, (v) => {
      b.classList.add("busy");
      const done = v === "!resume"
        ? accountAction("provider/resumegrowth", { id: p.id, account: l.user }, t("{who} resumed its ramp-up", { who: accountNameOf(p, l.user) || l.user }))
        : growthAccountSave(p, l.user, v);
      done.then(() => acxDraw());
    }, "Skipped ramp-up");
  };
  return b;
}

// acxGrowthFact: a card's Growth policy, for every account the policy
// counts the days of: the one it picked, or its subscription's followed
function acxGrowthFact(r) {
  const { p, l } = r;
  const picked = growthAccountPicked(p, l.user);
  const b = el("button", "acx-grow acx-gp", picked.id ? (picked.policy ? growthName(picked.policy) : picked.id) : t("Follows its subscription"));
  b.type = "button";
  b.title = picked.gone ? t("The policy it picked ({id}) is gone: it is held by its subscription's until you pick another", { id: picked.id }) : t("Growth policy this account is held by");
  b.setAttribute("aria-haspopup", "menu");
  b.onclick = (e) => acxGrowthMenu(e, b, r, false);
  return b;
}

// acxWho: an account's name or alias, its state's badge and its ramp-up
function acxWho(r) {
  const { p, l } = r;
  const who = el("div", "who");
  const alias = accountNameOf(p, l.user);
  const name = el("div", "name");
  name.append(el("span", "acx-name", alias || l.user));
  const badge = el("span", "acx-state " + r.state, t(ACX_SAID[r.state]));
  if (r.state === "off") badge.title = t("Not in rotation: still kept signed in and its usage read");
  if (r.again && l.lapsed) badge.title = t(l.lapsed);
  if (r.authFailed) badge.title = t("Anthropic answered a request of it authentication_failed. Nothing is sent it until you sign it in again, mark it banned or use it again");
  name.append(badge);
  // both marks: both said
  if (r.banned && r.disabled) name.append(el("span", "acx-state disabled", t("Disabled")));
  // skipped: the policy holds none of it till resumed; one ramped up is
  // held no more either way, so it says that alone
  if (r.skipped && !r.growth && !r.graduated) name.append(acxSkipped(r));
  else if (r.growth || r.pick) name.append(acxGrowth(r));
  if (r.graduated && !r.growth) {
    const done = el("span", "acx-grow acx-grad", t("Ramped up"));
    done.title = t("Its ramp-up was done by the calendar when its growth policy began counting only the days used, so the policy holds it no more");
    name.append(done);
  }
  name.append(acxRenew(r));
  who.append(name);
  return [who, alias];
}

// acxRenew: the account's paid period as a pill (renewPill, shared with
// Models › Proxy IPs), or its start date being picked; a pick is kept
// with provider/accountopened, and the server's answer draws the list
function acxRenew(r) {
  const { p, l } = r;
  const key = p.id + "/" + acxLow(l.user);
  if (acxRenewing !== key) return renewPill(r.renew, () => { acxRenewing = key; acxDraw(); });
  return renewEditor(key, r.renew, (opened) => accountAction("provider/accountopened", { id: p.id, account: l.user, opened }),
    () => { acxRenewing = ""; acxDraw(); });
}

// the kinds of allowance a card groups its windows by (需求方 2026-11-13),
// in this order
const ACX_QKINDS = [["5h", "5 hours"], ["7d", "Weekly"], ["month", "Monthly"], ["model", "Per model"], ["credit", "Credits and balance"], ["other", "Other"]];
// the names that say no more than their kind: the kind's heading says it,
// and the window's bar is drawn without it
const ACX_QPLAIN = [["5h", /^5 ?h(ours?)?$/i], ["7d", /^(7 days|weekly|week)$/i], ["month", /^(month|monthly|30 days)$/i]];
const acxQPlain = (k, w) => ACX_QPLAIN.some(([q, re]) => q === k && re.test(String(w.name || "").trim()));
// acxQMoney: a window counted in money or credits, by its unit or name
const acxQMoney = (w) => !!w.unit || /\$|credit|spend|on-demand|extra usage|balance/i.test(String(w.name || "") + " " + String(w.display || ""));
// acxQKind: a window's kind — by how the menu bar names it (trayKind), else
// by its name; a wallet or a count in a unit is credit, one model's or a
// pool's per model, a row of text other
function acxQKind(w) {
  if (w.text) return "other";
  if (w.balance != null) return "credit";
  // one the menu bar shows only when picked (trayExtra) is one model's, a
  // family's or a pool's, or money set aside (review M2)
  if (w.trayExtra) return w.family || w.pool || w.capsSome || !acxQMoney(w) ? "model" : "credit";
  const tk = w.trayKind || "";
  if (tk && !tk.startsWith("name:")) {
    // "5h:<name>", "7d:<name>": two of one span, Antigravity's pools
    for (const k of ["5h", "7d", "month"]) if (tk === k || tk.startsWith(k + ":")) return k;
  }
  const n = String(w.name || "").trim();
  for (const [k, re] of ACX_QPLAIN) if (re.test(n)) return k;
  if (w.unit) return "credit";
  if (w.family || w.pool || w.trayExtra) return "model";
  return "other";
}

// acxQText: a window that is a figure, not a share: its name and what it
// says (a wallet's balance left, a plugin's row of text)
function acxQText(w) {
  const row = el("div", "acx-qtext");
  const said = w.display || (w.balance != null ? fmtN(w.balance) + (w.unit ? " " + w.unit : "") : "");
  row.append(el("span", "acx-wn", t(w.name)), el("span", "acx-qv", said || "—"));
  row.title = t(w.name) + (said ? " · " + said : "");
  return row;
}

// acxQuotaBox: a card's allowances, each kind under its name: the windows
// with a share as bars with their reset, a wallet's or a plugin's figure
// as text, and an API key's balance as credit
function acxQuotaBox(q) {
  const box = el("div", "acx-wins");
  if (q?.error) {
    box.append(el("span", "acx-err", t("Couldn't read its usage")));
    return box;
  }
  const by = new Map();
  for (const w of q?.windows || []) {
    const k = acxQKind(w);
    if (!by.has(k)) by.set(k, []);
    by.get(k).push(w);
  }
  if (q?.balance) {
    if (!by.has("credit")) by.set("credit", []);
    by.get("credit").push({ name: "Balance", display: q.balance, text: true });
  }
  if (!by.size) {
    box.append(el("span", "acx-none", "—"));
    return box;
  }
  for (const [k, said] of ACX_QKINDS) {
    const ws = by.get(k);
    if (!ws) continue;
    const g = el("div", "acx-qg");
    g.dataset.kind = k;
    g.append(el("div", "acx-qk", t(said)));
    for (const w of ws) {
      if (w.text || w.balance != null) { g.append(acxQText(w)); continue; }
      const m = acxMeter(w, true);
      if (acxQPlain(k, w)) m.classList.add("plain");
      g.append(m);
    }
    box.append(g);
  }
  return box;
}

// acxMeter: a window as a bar, its name and the share used; with its
// reset beside it on a card (long), and its count where the vendor counts
// one (120 / 500 credits)
function acxMeter(w, long) {
  const pct = Math.max(0, Math.min(100, Math.round(w.unlimited ? 0 : w.used || 0)));
  const m = el("div", "acx-meter" + (pct >= 90 ? " full" : pct >= 70 ? " high" : ""));
  const count = w.limit > 0 ? fmtN(w.amount || 0) + " / " + fmtN(w.limit) + (w.unit ? " " + w.unit : "") : "";
  const reset = [count, w.resetsAt ? resetText(new Date(w.resetsAt)) : ""].filter(Boolean).join(" · ");
  m.title = t(w.name) + " · " + (w.unlimited ? t("Unlimited") : t("{n}% used", { n: pct })) + (reset ? " · " + reset : "");
  const bar = el("span", "acx-bar");
  const fill = el("span", "");
  fill.style.width = pct + "%";
  bar.append(fill);
  m.append(el("span", "acx-wn", t(w.name)), bar, el("span", "acx-pct", w.unlimited ? "∞" : pct + "%"));
  if (long) m.append(el("span", "acx-reset", reset));
  return m;
}

// acxDay: a time as its date, and how long ago in its title
function acxDay(at) {
  const d = new Date(at);
  const s = el("span", "", d.toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" }) + " · " + ago(d));
  s.title = d.toLocaleString(intlLang());
  return s;
}

// acxProxyOf: the proxy an account's requests go through, as
// provider.ProxyOfLogin picks it: its own, else its subscription's, which
// a subscription's only account signed in through a proxy keeps
// (keepSignInProxy; 需求方 2026-11-13: the first account's showed none);
// "" for the global one, "direct" for none (said as Direct, review 9)
function acxProxyOf(p, user) {
  return String(p.accountProxies?.[acxLow(user)] || p.proxy || "").trim();
}

// acxCard: an account's card (after CLIProxyAPI's credentials page) —
// who and its state; its provider, plan, when it was added, when its
// plan ends and its proxy; every window with its reset; and the last 5
// hours' requests, failures, tokens and cost through the gateway
function acxCard(r) {
  const { p, a, l, q } = r;
  const card = el("div", "acx-card " + r.state);
  card.dataset.provider = p.id;
  card.dataset.user = l.user;
  const head = el("div", "acx-head");
  const [who, alias] = acxWho(r);
  who.append(el("div", "sub", [p.name, alias ? l.user : ""].filter(Boolean).join(" · ")));
  // its letter on a grey of its own (the same each time), its vendor's
  // logo at the right (需求方 2026-11-13, after the screenshot)
  head.append(acxAvatar(alias || l.user), who, icon(p.icon || a.agent, true));
  card.append(head);

  const facts = el("dl", "acx-facts");
  const fact = (k, cls, v) => {
    const d = el("dd", cls);
    d.append(v);
    facts.append(el("dt", "", t(k)), d);
  };
  if (l.plan) fact("Plan", "acx-plan", accountPlan({ agent: a.agent, builtin: a.builtin, plan: l.plan }));
  if (r.added) fact("Added", "acx-added", acxDay(r.added));
  else {
    const u = el("span", "acx-unknown", t("Unknown"));
    u.title = t("Added before magpie kept it");
    fact("Added", "acx-added", u);
  }
  // the growth policy counts its days from its first use (provider/growth.go)
  if (r.firstUsed) fact("First used", "acx-used", acxDay(r.firstUsed));
  else if (r.added && p.firstUsedKnown) fact("First used", "acx-used", el("span", "acx-unknown", t("Never used")));
  // its growth policy, picked here for it alone (T-133), once the policies are read
  if (r.added && growthAccountOpts(p).length) fact("Growth policy", "acx-gpf", acxGrowthFact(r));
  if (l.until) fact("Plan ends", "acx-until", acxDay(l.until));
  const proxy = acxProxyOf(p, l.user);
  if (proxy) fact("Proxy", "acx-proxy", proxyName(proxy));
  card.append(facts);

  card.append(acxQuotaBox(q));

  const stats = el("div", "acx-stats");
  stats.title = t("In the last 5 hours, through the gateway: {n} tokens", { n: fmtN(r.tokens) });
  const stat = (cls, v, k) => {
    const s = el("div", "acx-stat " + cls);
    s.append(el("b", "", v), el("span", "", t(k)));
    stats.append(s);
  };
  stat("calls", fmtN(r.calls), "Requests");
  stat("errors" + (r.errors ? " bad" : ""), fmtN(r.errors), "Failed");
  stat("tokens", fmtN(r.tokens), "Tokens");
  stat("cost", fmtCost({ cost: r.cost, unpriced: r.unpriced }) || "—", "Cost");
  card.append(stats, acxSpansBox(r));

  card.append(acxActs(r));
  return card;
}

// acxAvatar: a name's first letter on a grey worked out from the name
function acxAvatar(name) {
  const s = String(name || "?").trim() || "?";
  let h = 0;
  for (const c of acxLow(s)) h = (h * 31 + c.codePointAt(0)) % 360;
  const a = el("span", "acx-avatar", [...s][0].toUpperCase());
  // one of a few greys, the same each time, so cards side by side tell apart
  a.dataset.tone = String(h % 4);
  a.setAttribute("aria-hidden", "true");
  return a;
}

// acxSpansBox: today's, the last 7 and 30 days' cost, requests and tokens
// through the gateway, each "—" till read
function acxSpansBox(r) {
  const box = el("div", "acx-spans");
  for (const [k, n] of ACX_SPANS) {
    const x = r.spans[k];
    const c = el("div", "acx-span");
    c.dataset.span = k;
    const cost = x ? fmtCost(x) || (x.calls ? "—" : fmtCost({ cost: 0, unpriced: 0 })) : "—";
    c.append(el("span", "acx-span-k", t(n)), el("b", "", cost),
      el("span", "acx-span-n", x ? t("{n} req · {tok} tok", { n: fmtN(x.calls), tok: fmtN(x.tokens) }) : t("Not loaded yet")));
    if (x) c.title = t("{period}, through the gateway: {n} requests, {tok} tokens", { period: t(n), n: fmtN(x.calls), tok: fmtN(x.tokens) });
    box.append(c);
  }
  return box;
}

// acxRow: an account's row — who, its state, its windows, its requests,
// its proxy, and what can be done to it
function acxRow(r) {
  const { p, a, l, q } = r;
  const row = el("div", "row acx " + r.state);
  row.dataset.provider = p.id;
  row.dataset.user = l.user;
  row.append(icon(p.icon || a.agent, true));
  const [who, alias] = acxWho(r);
  const proxy = acxProxyOf(p, l.user);
  const sub = [p.name, alias ? l.user : "", l.plan ? accountPlan({ agent: a.agent, builtin: a.builtin, plan: l.plan }) : "", proxy ? t("Proxy: {proxy}", { proxy: proxyName(proxy) }) : ""].filter(Boolean);
  who.append(el("div", "sub", sub.join(" · ")));
  row.append(who);

  // its first two windows (the 5 hours, the week), each a bar
  const meters = el("div", "acx-meters");
  const ws = acxWindows(q);
  for (const w of ws.slice(0, 2)) meters.append(acxMeter(w, false));
  if (q?.error) meters.append(el("span", "acx-err", t("Couldn't read its usage")));
  else if (!ws.length) meters.append(el("span", "acx-none", q?.balance ? q.balance : "—"));
  row.append(meters);

  const calls = el("div", "acx-calls");
  calls.append(el("b", "", fmtN(r.calls)), el("span", "", t(r.calls === 1 ? "request" : "requests")));
  calls.title = t("In the last 5 hours, through the gateway: {n} tokens", { n: fmtN(r.tokens) });
  row.append(calls);

  row.append(acxActs(r));
  return row;
}

// Add account (需求方 2026-11-13: 账号页加添加按钮，先选账号类型再进入
// 添加流程，当前只需支持 Claude): its type first, then the sign-in
// Providers runs (readySignIn, renderSigning in app.js) drawn here. The
// sign-in draws itself with renderProviders, so while this is open on this
// page that draws here instead: the providers' page is drawn on its next
// opening (providersBehind), and the editor signedIn opens on the account
// added stays shut. A sign-in canceled goes back to the type; one under
// way is canceled when Add account is shut or the page left (review H1, M1).
const ACX_ADD_TYPES = ["claude"];
const acxRenderProviders = renderProviders;
renderProviders = function (quiet) {
  if (view !== "accounts" || !acxAdding) return acxRenderProviders(quiet);
  closeProtoMenu(); // a menu open on a button this redraw takes out
  providersBehind = true;
  if (!signing && editing !== null) { editing = null; adding = false; draft = null; }
  acxAddDraw();
};

// signedIn names the account its sign-in added: one of ours is said here
// (review L1: not justAdded, which is cleared after 2s)
const acxSignedIn = signedIn;
signedIn = function (st) {
  if (acxAdding && acxAdding !== "type" && signing?.agent === acxAdding) acxAdded1 = st.user || subOf(st.agent)?.name || st.agent;
  return acxSignedIn(st);
};

// acxAddClose: Add account shut, its sign-in canceled
function acxAddClose() {
  const ours = acxAdding && acxAdding !== "type" && signing?.agent === acxAdding;
  acxAdding = acxAdded1 = "";
  if (ours) cancelSignIn();
  acxAddDraw();
}

function acxAddDraw() {
  const box = $("#acxAdding");
  if (!box) return;
  $("#acxAdd").classList.toggle("on", !!acxAdding);
  box.hidden = !acxAdding;
  box.replaceChildren();
  if (!acxAdding) return;
  // its sign-in ended: added (signedIn names it), or canceled
  if (acxAdding !== "type" && signing?.agent !== acxAdding && !acxAdded1) acxAdding = "type";
  const head = el("div", "acx-add-head");
  head.append(el("b", "", t("Add account")));
  if (acxAdding !== "type") head.append(el("span", "acx-add-step", subOf(acxAdding)?.name || acxAdding));
  head.append(el("span", "grow"));
  if (acxAdding !== "type" && !acxAdded1) {
    const back = el("button", "text acx-add-back", t("Back to the types"));
    back.type = "button";
    back.title = t("Pick another type of account");
    back.onclick = () => { const ours = signing?.agent === acxAdding; acxAdding = "type"; if (ours) cancelSignIn(); acxAddDraw(); };
    head.append(back);
  }
  const close = el("button", "text acx-add-close", t("Close"));
  close.type = "button";
  close.onclick = acxAddClose;
  head.append(close);
  box.append(head);
  if (acxAdding === "type") {
    box.append(el("p", "acx-add-note", t("Pick the type of account to add.")));
    const tiles = el("div", "acx-add-types");
    for (const agent of ACX_ADD_TYPES) {
      const sub = subOf(agent);
      if (!sub) continue;
      const b = el("button", "acx-add-type");
      b.type = "button";
      b.dataset.agent = agent;
      const words = el("span", "acx-add-words");
      words.append(el("b", "", sub.name), el("span", "", sub.plans || ""));
      b.append(icon(sub.icon || agent, true), words);
      b.onclick = () => { acxAdding = agent; acxAdded1 = ""; readySignIn(agent); };
      tiles.append(b);
    }
    box.append(tiles);
    return;
  }
  if (acxAdded1) {
    const done = el("div", "acx-add-done");
    done.append(el("span", "", t("{user} added: it is listed below", { user: acxAdded1 })));
    const more = el("button", "text primary acx-add-more", t("Add one more account"));
    more.type = "button";
    more.onclick = () => { acxAdded1 = ""; readySignIn(acxAdding); };
    done.append(more);
    box.append(done);
    return;
  }
  box.append(renderSigning(subOf(acxAdding)));
}
