// Models › Accounts' code (client.js has its markup and hooks): run after
// app.js, whose providers, allowances (quotas) and account helpers it
// reads. An account's Use, Pause and Stop follow its row's tick in the
// provider's editor (renderAccounts), so the two never say different
// things; Sign in again opens that editor with the sign-in under way.

// the filters, kept per machine as the reader left them
let acxQuery = "", acxProv = "", acxState = "all";
try { ({ q: acxQuery = "", p: acxProv = "", s: acxState = "all" } = JSON.parse(localStorage.getItem("magpie.acxFilter") || "{}")); } catch {}
const acxKeep = () => { try { localStorage.setItem("magpie.acxFilter", JSON.stringify({ q: acxQuery, p: acxProv, s: acxState })); } catch {} };
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
      out.push({ p, a, l, sub, state, again, act, q, calls: used.reduce((n, g) => n + (g.calls || 0), 0), tokens: used.reduce((n, g) => n + (g.tokens || 0), 0) });
    }
  }
  return out;
}

const ACX_STATES = [["all", "All"], ["on", "In use"], ["again", "Sign in again"], ["off", "Not in use"]];
const ACX_SAID = { on: "In use", paused: "Paused", again: "Sign in again", off: "Not in use" };

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
  const st = $("#acxState");
  st.replaceChildren(segs(ACX_STATES.map(([k, n]) => [k, t(n)]), acxState, (k) => { acxState = k; acxKeep(); acxDraw(); }));
  st.hidden = !all.length;

  const words = acxLow(acxQuery);
  const rows = all.filter((r) => (!acxProv || r.p.id === acxProv)
    && (acxState === "all" || r.state === acxState || (acxState === "on" && r.state === "paused"))
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
  for (const r of rows) list.append(acxRow(r));
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

// acxRow: an account's row — who, its state, its windows, its requests,
// its proxy, and what can be done to it
function acxRow(r) {
  const { p, a, l, q } = r;
  const row = el("div", "row acx " + r.state);
  row.dataset.provider = p.id;
  row.dataset.user = l.user;
  row.append(icon(p.icon || a.agent, true));
  const who = el("div", "who");
  const alias = accountNameOf(p, l.user);
  const name = el("div", "name");
  name.append(el("span", "acx-name", alias || l.user));
  const badge = el("span", "acx-state " + r.state, t(ACX_SAID[r.state]));
  if (r.again && l.lapsed) badge.title = t(l.lapsed);
  name.append(badge);
  who.append(name);
  const proxy = p.accountProxies?.[acxLow(l.user)] || "";
  const sub = [p.name, alias ? l.user : "", l.plan ? accountPlan({ agent: a.agent, builtin: a.builtin, plan: l.plan }) : "", proxy ? t("Proxy: {proxy}", { proxy: proxyShown(proxy) }) : ""].filter(Boolean);
  who.append(el("div", "sub", sub.join(" · ")));
  row.append(who);

  // its first two windows (the 5 hours, the week), each a bar
  const meters = el("div", "acx-meters");
  const ws = (q && !q.error ? q.windows || [] : []).filter((w) => !w.text);
  for (const w of ws.slice(0, 2)) {
    const pct = Math.max(0, Math.min(100, Math.round(w.unlimited ? 0 : w.used || 0)));
    const m = el("div", "acx-meter" + (pct >= 90 ? " full" : pct >= 70 ? " high" : ""));
    m.title = t(w.name) + " · " + (w.unlimited ? t("Unlimited") : t("{n}% used", { n: pct })) + (w.resetsAt ? " · " + resetText(new Date(w.resetsAt)) : "");
    const bar = el("span", "acx-bar");
    const fill = el("span", "");
    fill.style.width = pct + "%";
    bar.append(fill);
    m.append(el("span", "acx-wn", t(w.name)), bar, el("span", "acx-pct", w.unlimited ? "∞" : pct + "%"));
    meters.append(m);
  }
  if (q?.error) meters.append(el("span", "acx-err", t("Couldn't read its usage")));
  else if (!ws.length) meters.append(el("span", "acx-none", q?.balance ? q.balance : "—"));
  row.append(meters);

  const calls = el("div", "acx-calls");
  calls.append(el("b", "", fmtN(r.calls)), el("span", "", t(r.calls === 1 ? "request" : "requests")));
  calls.title = t("In the last 5 hours, through the gateway: {n} tokens", { n: fmtN(r.tokens) });
  row.append(calls);

  const acts = el("span", "acts");
  if (r.again) {
    const b = el("button", "text primary acx-again", t("Sign in again"));
    b.type = "button";
    b.onclick = async () => {
      // the editor opens, the sign-in under way in it, as Providers' line does
      await openProvider(p.id);
      if (view !== "providers") return;
      startSignIn(a.agent, false, signInAgainSite(a.agent, l.user), { user: l.user });
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
  row.append(acts);
  return row;
}
