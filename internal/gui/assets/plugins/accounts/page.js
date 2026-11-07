// Models › Accounts' code (client.js has its markup and hooks): run after
// app.js, whose providers, allowances (quotas) and account helpers it
// reads. An account's Use, Pause and Stop follow its row's tick in the
// provider's editor (renderAccounts), so the two never say different
// things; Sign in again opens that editor with the sign-in under way.

// the filters and the view (rows or cards), kept per machine as the
// reader left them: a word, the provider, the state, the gateway key, when
// it was added, and whether its 5 hours and its week have room
let acxQuery = "", acxProv = "", acxState = "all", acxKey = "", acxAdded = "", acx5h = "", acx7d = "", acxView = "list";
try { ({ q: acxQuery = "", p: acxProv = "", s: acxState = "all", k: acxKey = "", a: acxAdded = "", h5: acx5h = "", h7: acx7d = "", v: acxView = "list" } = JSON.parse(localStorage.getItem("magpie.acxFilter") || "{}")); } catch {}
const acxKeep = () => { try { localStorage.setItem("magpie.acxFilter", JSON.stringify({ q: acxQuery, p: acxProv, s: acxState, k: acxKey, a: acxAdded, h5: acx5h, h7: acx7d, v: acxView })); } catch {} };
// the gateway keys that work, each with the accounts it may use, as the
// gateway holds it (/api/accounts/keys), and when read
let acxKeys = null, acxKeysAt = 0;
// the last 5 hours' requests by account (usage's accounts), and when read
let acxUsage = null, acxUsageAt = 0;
// what the list was drawn from, so a change elsewhere draws it again
let acxSeen = null, acxWatch = 0;

function acxMount() {
  const q = $("#acxQ");
  q.value = acxQuery;
  q.setAttribute("aria-label", t("Search accounts"));
  q.oninput = () => { acxQuery = q.value; acxKeep(); acxDraw(); };
  q.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape" && q.value) { q.value = ""; q.oninput(); } };
}

// acxLoad: the providers (the shell's, loads: "providers"), the allowances
// as the Usage page reads them, and the last 5 hours' requests; drawn once
// each comes in, and kept drawn while the page is open (acxFollow)
async function acxLoad(again) {
  acxFollow();
  if (providers) acxDraw();
  else acxSkeleton();
  const reads = [
    loadProviders().catch(statusErr),
    (!quotas || again || Date.now() - quotasAt > 60e3 ? loadQuotas() : Promise.resolve()).catch(() => {}),
    (!acxUsage || again || Date.now() - acxUsageAt > 30e3 ? api("usage?period=" + encodeURIComponent("last:5h") + (typeof usageZoneQ === "function" ? usageZoneQ() : ""))
      .then((u) => { acxUsage = u; acxUsageAt = Date.now(); }, () => {}) : Promise.resolve()),
    (!acxKeys || again || Date.now() - acxKeysAt > 30e3 ? api("accounts/keys")
      .then((k) => { acxKeys = k?.keys || []; acxKeysAt = Date.now(); }, () => {}) : Promise.resolve()),
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
      if (view !== "accounts") { clearInterval(acxWatch); acxWatch = 0; }
      return;
    }
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
    const unusable = (l) => a.agent === "claude" && !!l.lapsed;
    const isOn = (l) => !unusable(l) && !l.paused && (l.active || l.on);
    const several = ls.filter((l) => !unusable(l) && ((l.active && !l.paused) || l.on)).length > 1;
    const pausable = (a.agent === "claude" || a.agent === "codex") && ls.some((l) => !unusable(l) && !l.active && l.on);
    for (const l of ls) {
      const again = unusable(l) || signInLapsed(a.agent, l);
      const state = again ? "again" : l.paused ? "paused" : isOn(l) ? "on" : "off";
      // the tick's own action, or none (the agent's own account, fixed)
      let act = null;
      if (unusable(l)) act = null;
      else if (l.active && (pausable || l.paused)) act = l.paused ? ["on", "Resume"] : ["off", "Pause"];
      else if (l.active && sub?.own && several) act = ["off", "Stop using"];
      else if (!l.active) act = isOn(l) ? ["off", "Stop using"] : ["on", "Use"];
      const q = (quotas || []).find((x) => x.provider === p.id && acxLow(x.user) === acxLow(l.user));
      const used = (acxUsage?.accounts || []).filter((g) => g.provider === p.id && acxLow(g.account) === acxLow(l.user));
      // tokens are in and out, as tokensOf counts them: an account's
      // usage has no tokens of its own
      const sum = (k) => used.reduce((n, g) => n + (g[k] || 0), 0);
      const added = p.accountAdded?.[acxLow(l.user)] || "";
      out.push({ p, a, l, sub, state, again, act, q, added, calls: sum("calls"), errors: sum("errors"), tokens: sum("input") + sum("output"), cost: sum("cost"), unpriced: sum("unpriced") });
    }
  }
  return out;
}

const ACX_STATES = [["all", "All"], ["on", "In use"], ["again", "Sign in again"], ["off", "Not in use"]];
const ACX_SAID = { on: "In use", paused: "Paused", again: "Sign in again", off: "Not in use" };
const ACX_VIEWS = [["list", "List view"], ["cards", "Card view"]];
const ACX_ADDED = [["7", "Added in the last 7 days"], ["30", "Added in the last 30 days"], ["old", "Added over 30 days ago"], ["unknown", "Added before magpie kept it"]];
// a window's room: "room" left in it, "out" used up
const ACX_ROOM = { "5h": [["room", "5 hours: room left"], ["out", "5 hours: used up"]], "7d": [["room", "7 days: room left"], ["out", "7 days: used up"]] };

// acxWindows: an account's windows as bars, a failed reading's none
const acxWindows = (q) => (q && !q.error ? q.windows || [] : []).filter((w) => !w.text);
// acxWindowOf: its 5 hours ("5h") or its week ("7d"), as the menu bar
// tells them (trayKind), else by name; a per-model one only when there
// is no window of the whole
function acxWindowOf(q, kind) {
  const named = kind === "5h" ? /^5 ?h(ours?)?$/i : /^(7 days|weekly|week)$/i;
  const ws = acxWindows(q).filter((w) => w.trayKind ? w.trayKind === kind : named.test(String(w.name || "").trim()));
  return ws.find((w) => !w.trayExtra) || ws[0] || null;
}
// acxRoom: whether the window has room ("room"), is used up ("out"), or
// the account has none of it read ("")
function acxRoom(q, kind) {
  const w = acxWindowOf(q, kind);
  if (!w) return "";
  return w.unlimited || (w.used || 0) < 100 ? "room" : "out";
}
// acxAddedIn: which of ACX_ADDED an account added at added is in
function acxAddedIn(added) {
  const at = added ? new Date(added).getTime() : NaN;
  if (!(at > 0)) return "unknown";
  const days = (Date.now() - at) / 864e5;
  return days <= 7 ? "7" : days <= 30 ? "30" : "old";
}
// acxKeyAllows: the gateway key k may use the account (all, for a key held
// to none): accountsKeyRoutes' list, by provider and name in lower case
const acxKeyAllows = (k, r) => !k || k.all || (k.accounts || []).includes(acxLow(r.p.id + "/" + r.l.user));
// acxPickOpts: a fixed menu's options, translated
const acxPickOpts = (xs) => xs.map(([v, n]) => ({ v, name: t(n), note: "" }));

function acxDraw() {
  const page = $("#view-accounts");
  if (!page || !providers) return;
  acxSeen = { providers, quotas: quotasAt };
  const all = acxRows();
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
  $("#acxKey").hidden = !keys.length;
  const fixed = (id, all, xs, value, head, set) => {
    const b = $(id);
    sessPick(b, all, value, acxPickOpts(xs), head, (v) => { set(v); acxKeep(); acxDraw(); });
    b.hidden = !all.length || !xs.length;
    b.title = "";
  };
  if (!ACX_ADDED.some(([v]) => v === acxAdded)) acxAdded = "";
  if (!ACX_ROOM["5h"].some(([v]) => v === acx5h)) acx5h = "";
  if (!ACX_ROOM["7d"].some(([v]) => v === acx7d)) acx7d = "";
  fixed("#acxAdded", "Added any time", ACX_ADDED, acxAdded, "Added", (v) => { acxAdded = v; });
  fixed("#acx5h", "5 hours: any", ACX_ROOM["5h"], acx5h, "5 hours", (v) => { acx5h = v; });
  fixed("#acx7d", "7 days: any", ACX_ROOM["7d"], acx7d, "7 days", (v) => { acx7d = v; });
  if (!ACX_VIEWS.some(([v]) => v === acxView)) acxView = "list";
  const vw = $("#acxView");
  vw.replaceChildren(segs(ACX_VIEWS.map(([k, n]) => [k, t(n)]), acxView, (k) => { acxView = k; acxKeep(); acxDraw(); }));
  vw.hidden = !all.length;
  const st = $("#acxState");
  st.replaceChildren(segs(ACX_STATES.map(([k, n]) => [k, t(n)]), acxState, (k) => { acxState = k; acxKeep(); acxDraw(); }));
  st.hidden = !all.length;

  const words = acxLow(acxQuery);
  const rows = all.filter((r) => (!acxProv || r.p.id === acxProv)
    && (acxState === "all" || r.state === acxState || (acxState === "on" && r.state === "paused"))
    && acxKeyAllows(key, r)
    && (!acxAdded || acxAddedIn(r.added) === acxAdded)
    && (!acx5h || acxRoom(r.q, "5h") === acx5h)
    && (!acx7d || acxRoom(r.q, "7d") === acx7d)
    && (!words || [r.l.user, accountNameOf(r.p, r.l.user), r.p.name, r.l.plan].some((s) => acxLow(s).includes(words))));

  const n = (k) => all.filter((r) => r.state === k).length;
  const sum = $("#acxSum");
  sum.replaceChildren();
  if (all.length) {
    const parts = [t(all.length === 1 ? "1 account" : "{n} accounts", { n: all.length }), t("{n} in use", { n: n("on") })];
    if (n("paused")) parts.push(t("{n} paused", { n: n("paused") }));
    if (n("again")) parts.push(t("{n} to sign in again", { n: n("again") }));
    sum.append(...parts.flatMap((s, i) => (i ? [" · ", el("span", "", s)] : [el("span", "", s)])));
    if (n("again")) sum.lastChild.classList.add("acx-warn");
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
    note.append(el("span", "", t("No subscription account yet. Add one in Providers: it is listed here once signed in.") + " "));
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
    b.onclick = () => {
      b.classList.add("busy");
      accountAction("login/" + to, { agent: a.agent, user: l.user }).then(() => acxDraw());
    };
    acts.append(b);
  }
  if (q) acts.append(quotaRefresh(q, () => acxDraw()));
  const open = el("button", "text acx-open", t("Open"));
  open.type = "button";
  open.title = t("Open {name} in Providers, with this account's settings", { name: p.name });
  open.onclick = () => openProvider(p.id);
  acts.append(open);
  return acts;
}

// acxWho: an account's name or alias and its state's badge
function acxWho(r) {
  const { p, l } = r;
  const who = el("div", "who");
  const alias = accountNameOf(p, l.user);
  const name = el("div", "name");
  name.append(el("span", "acx-name", alias || l.user));
  const badge = el("span", "acx-state " + r.state, t(ACX_SAID[r.state]));
  if (r.again && l.lapsed) badge.title = t(l.lapsed);
  name.append(badge);
  who.append(name);
  return [who, alias];
}

// acxMeter: a window as a bar, its name and the share used; with its
// reset beside it on a card (long)
function acxMeter(w, long) {
  const pct = Math.max(0, Math.min(100, Math.round(w.unlimited ? 0 : w.used || 0)));
  const m = el("div", "acx-meter" + (pct >= 90 ? " full" : pct >= 70 ? " high" : ""));
  const reset = w.resetsAt ? resetText(new Date(w.resetsAt)) : "";
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
  head.append(icon(p.icon || a.agent, true), who);
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
  if (l.until) fact("Plan ends", "acx-until", acxDay(l.until));
  const proxy = p.accountProxies?.[acxLow(l.user)] || "";
  if (proxy) fact("Proxy", "acx-proxy", proxyShown(proxy));
  card.append(facts);

  const wins = el("div", "acx-wins");
  const ws = acxWindows(q);
  for (const w of ws) wins.append(acxMeter(w, true));
  if (q?.error) wins.append(el("span", "acx-err", t("Couldn't read its usage")));
  else if (!ws.length) wins.append(el("span", "acx-none", q?.balance ? q.balance : "—"));
  card.append(wins);

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
  card.append(stats);

  card.append(acxActs(r));
  return card;
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
  const proxy = p.accountProxies?.[acxLow(l.user)] || "";
  const sub = [p.name, alias ? l.user : "", l.plan ? accountPlan({ agent: a.agent, builtin: a.builtin, plan: l.plan }) : "", proxy ? t("Proxy: {proxy}", { proxy: proxyShown(proxy) }) : ""].filter(Boolean);
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
