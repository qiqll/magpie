// Models › Proxy IPs (T-155, client.js has its markup and hooks): the
// proxy exit IPs the user pays for by the month, each with its start date
// and the same period pill as Models › Accounts (renew.js), the period
// worked out by the server (/api/ips, renewal.Of). An IP is typed, or
// read as the exit of a saved proxy (Settings › Network, proxy/check);
// the list is kept whole, as Settings' saved proxies are. Import from
// accounts (T-170) reads the exit of every proxy an account goes through
// and adds what isn't listed; each IP names the accounts that go out
// through it with their state (page.js' acxRows), found by that state.
// An IP's form lists the proxies it was read from, each unlinked there,
// and an import checks every proxy again: one whose exit moved is moved to
// the IP at its new exit, and said (T-172); one with no start date takes
// its accounts' earliest (T-177). The list is found by a word,
// its period as the pill says it, and its accounts, together (T-176).
// Run after app.js and page.js.

// the list as the server last said it and the revision it was read at
// (a save made from an older one is refused), the form open (the index
// edited, -1 for a new one, null for none) and its draft (proxies: those
// the IP keeps, one read from a saved proxy among them), the IP whose
// start date is being picked, the last error, and whether a save is under
// way: the list takes no other change till it is answered (T-155 review M2)
let ipsList = null, ipsRev = "", ipsEdit = null, ipsDraft = { ip: "", name: "", proxies: [] }, ipsRenewing = "", ipsErr = "", ipsSaving = false;
// Import from accounts under way, and what the last one did
let ipsImporting = false, ipsSaid = "";
// the accounts filter, kept per machine: "" every IP, "bound" those an
// account goes through, "unbound" those none does, or an account state
// (ACX_SAID's) one of its accounts is in
let ipsFilter = "";
// the words searched for and the period filter (T-176), kept per machine
// as the accounts filter is: "" every IP, else IPS_RENEW's
let ipsQuery = "", ipsRenew = "";
// the IPs whose start date the user cleared, by address: an import gives
// them none of their accounts' (T-177 review M1); one told a date again
// leaves it. Kept per machine, as ips.json is this computer's own
let ipsCleared = [];
try { ipsCleared = JSON.parse(localStorage.getItem("magpie.ipsCleared") || "[]"); } catch {}
if (!Array.isArray(ipsCleared)) ipsCleared = [];
function ipsClearedSet(ip, on) {
  ipsCleared = ipsCleared.filter((c) => c !== ipsCanon(ip));
  if (on) ipsCleared.push(ipsCanon(ip));
  try { localStorage.setItem("magpie.ipsCleared", JSON.stringify(ipsCleared)); } catch {}
}
try {
  ipsFilter = localStorage.getItem("magpie.ipsFilter") || "";
  ipsQuery = localStorage.getItem("magpie.ipsQuery") || "";
  ipsRenew = localStorage.getItem("magpie.ipsRenew") || "";
} catch {}

function ipsMount() {
  const q = $("#ipsQ");
  q.value = ipsQuery;
  q.setAttribute("aria-label", t("Search IPs"));
  q.oninput = () => {
    ipsQuery = q.value;
    try { localStorage.setItem("magpie.ipsQuery", ipsQuery); } catch {}
    ipsDraw();
  };
  q.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape" && q.value) { q.value = ""; q.oninput(); } };
  const add = $("#ipsAdd");
  add.onclick = () => { ipsEdit = -1; ipsDraft = { ip: "", name: "", proxies: [] }; ipsErr = ""; ipsSaid = ""; ipsDraw(); };
  $("#ipsImport").onclick = ipsImport;
}

// ipsBound: what can go out through a proxy of its own, each as
// { p, id, who, sub, state, proxy }: every signed-in account as Models ›
// Accounts lists it (acxRows), and every key of a provider of keys (T-170
// review H1), on or off as its editor ticks it. proxy is the one its
// requests go through as provider.ProxyChoice picks it, its own (an
// account by its name in lower case, a key by its id) or else its
// provider's; "" for the global one or none (direct). renew is an
// account's paid period as the server worked it out (accountRenewal,
// T-154), null for one with no start date and for a key
function ipsBound() {
  const via = (p, own) => { const v = String(p.accountProxies?.[own] || p.proxy || "").trim(); return v === "direct" ? "" : v; };
  const out = acxRows().map((r) => ({ p: r.p, id: r.p.id + "/" + acxLow(r.l.user), who: accountShownOf(r.p, r.l.user), sub: r.l.user, state: r.state, proxy: via(r.p, acxLow(r.l.user)), renew: r.renew || null }));
  for (const p of providers?.providers || []) {
    if (p.account?.logins?.length) continue;
    for (const k of p.keyList || []) out.push({ p, id: p.id + "/" + k.id, who: p.name + " · " + (k.name || k.masked), sub: k.masked, state: k.on ? "on" : "off", proxy: via(p, k.id), renew: null });
  }
  return out;
}
const ipsProxyOf = (r) => r.proxy;

// ipsHostOf: the proxy's host when it is an IP address, in lower case
// and without brackets; "" for a name
function ipsHostOf(proxy) {
  try {
    const h = new URL(proxy.includes("://") ? proxy : "http://" + proxy).hostname.replace(/^\[|\]$/g, "").toLowerCase();
    return /^\d{1,3}(\.\d{1,3}){3}$/.test(h) || h.includes(":") ? h : "";
  } catch { return ""; }
}

// ipsAccountsOf: the accounts and keys going out through x: their proxy
// is one x was read as the exit of, or one at x itself that no IP was
// read from (claimed): one read as another's exit, or unlinked, goes
// there or nowhere (T-172 review M2)
const ipsAccountsOf = (x, rows, claimed) => rows.filter((r) => {
  const p = ipsProxyOf(r);
  return p && ((x.proxies || []).includes(p) || (!claimed.has(p) && ipsCanon(ipsHostOf(p)) === ipsCanon(x.ip)));
});

// the most proxies the server keeps for one IP (renewal.MaxIPProxies)
const IPS_MAX_PROXIES = 20;

// ipsCanon: an address written one way, as the server writes it: IPv6 in
// its short lower-case form (what Cloudflare's trace says may differ),
// one mapped from IPv4 as the IPv4 (review M3, L2)
function ipsCanon(ip) {
  const s = String(ip || "").trim().toLowerCase();
  if (!s.includes(":")) return s;
  try {
    const h = new URL("http://[" + s.replace(/^\[|\]$/g, "") + "]").hostname.slice(1, -1);
    const m = /^::ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/.exec(h);
    return m ? [parseInt(m[1], 16) >> 8, parseInt(m[1], 16) & 255, parseInt(m[2], 16) >> 8, parseInt(m[2], 16) & 255].join(".") : h;
  } catch { return s; }
}

// ipsShown: a proxy as it can be said, its login left out
function ipsShown(proxy) {
  try { return new URL(proxy.includes("://") ? proxy : "http://" + proxy).host || "[proxy]"; } catch { return "[proxy]"; }
}

// ipsImport: the exit of each proxy an account goes through
// (proxy/check, four at a time), added as an IP of its own, or told to
// the IP listed at that address; one an IP was read from before whose
// exit is another now leaves that IP for its new exit, and is said
// (T-172). An IP with no start date then takes the earliest of its
// accounts' (T-177). Kept as one save. A proxy that didn't answer is
// named, and nothing is guessed or moved for it
async function ipsImport() {
  if (ipsImporting || !ipsList) return;
  ipsErr = "";
  ipsSaid = "";
  const bound = ipsBound();
  const todo = [...new Set(bound.map(ipsProxyOf).filter(Boolean))];
  if (!todo.length) {
    ipsSaid = t("No account has a proxy of its own or its subscription's");
    return ipsDraw();
  }
  ipsImporting = true;
  ipsDraw();
  const found = [], failed = [];
  try {
    let at = 0;
    const work = async () => {
      while (at < todo.length) {
        const p = todo[at++];
        const ip = await api("proxy/check", { proxy: p }).then((out) => out?.through?.ip || "", () => "");
        if (ip) found.push([p, ip]);
        else failed.push(p);
      }
    };
    await Promise.all([work(), work(), work(), work()]);
    // a save under way when the checks end (one begun before the import)
    // is waited for, and the list it kept is the one added to (review M1)
    while (ipsSaving) await new Promise((r) => setTimeout(r, 50));
    if (!ipsList) return;
    // in the accounts' order, so the list reads the same each time
    found.sort((a, b) => todo.indexOf(a[0]) - todo.indexOf(b[0]));
    const saved = (prefs || state?.settings)?.proxies || [];
    const next = ipsList.map((x) => ({ ...x, proxies: [...(x.proxies || [])] }));
    let added = 0, linked = 0;
    const full = [], moved = [], stuck = [];
    for (const [p, raw] of found) {
      const ip = ipsCanon(raw);
      const x = next.find((y) => ipsCanon(y.ip) === ip);
      // the IPs it was read from before at another address
      const was = next.filter((y) => ipsCanon(y.ip) !== ip && y.proxies.includes(p));
      // a full IP takes no more: one whose exit moved there stays where
      // it was, and is said apart (review M1)
      if (x && x.proxies.length >= IPS_MAX_PROXIES && !x.proxies.includes(p)) {
        if (was.length) stuck.push(ipsShown(p) + " (" + was.map((y) => y.ip).join(", ") + " → " + x.ip + ")");
        else full.push(x.ip);
        continue;
      }
      for (const y of was) y.proxies = y.proxies.filter((q) => q !== p);
      if (was.length) moved.push(ipsShown(p) + " (" + was.map((y) => y.ip).join(", ") + " → " + (x?.ip || ip) + ")");
      if (!x) {
        next.push({ ip, name: saved.find((s) => s.url === p)?.name || "", proxies: [p] });
        added++;
      } else if (!x.proxies.includes(p)) {
        x.proxies.push(p);
        if (!was.length) linked++;
      }
    }
    // each IP's accounts as the list now binds them: one with no start
    // date takes their earliest, so its reminder comes no later than any
    // of theirs; one told a date keeps it, the user's (T-177), and is
    // said when their earliest is another day. Run again, nothing changes
    // the accounts as they are now, not as read before the checks: the
    // providers may have been read again meanwhile (review M2)
    const now = ipsBound();
    const claimed = new Set(next.flatMap((y) => y.proxies));
    const shown = (at) => new Date(at).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" });
    let dated = 0;
    const apart = [];
    for (const y of next) {
      const first = ipsAccountsOf(y, now, claimed).map((r) => r.renew?.opened).filter(Boolean).sort((a, b) => Date.parse(a) - Date.parse(b))[0];
      if (!first) continue;
      if (!y.opened && !ipsCleared.includes(ipsCanon(y.ip))) {
        y.opened = first;
        dated++;
      } else if (y.opened && renewDay(y.opened) !== renewDay(first)) apart.push(t("{ip}: its own {own}, its accounts' earliest {theirs}", { ip: y.ip, own: shown(y.opened), theirs: shown(first) }));
    }
    if ((added || linked || moved.length || dated) && !await ipsKeep(next)) {
      if (!ipsErr) ipsErr = t("The import couldn't be saved: try it again");
      return;
    }
    const parts = [];
    if (added) parts.push(t(added === 1 ? "1 IP added" : "{n} IPs added", { n: added }));
    if (linked) parts.push(t(linked === 1 ? "1 proxy told to an IP listed" : "{n} proxies told to IPs listed", { n: linked }));
    if (moved.length) parts.push(t("Moved to a new exit: {list}", { list: moved.join(", ") }));
    if (dated) parts.push(t(dated === 1 ? "1 IP given its accounts' start date" : "{n} IPs given their accounts' start date", { n: dated }));
    // nothing new and nothing unknown: every exit is as listed
    if (!parts.length && !failed.length && !full.length && !stuck.length) parts.push(t("Every proxy an account goes through is listed already"));
    if (apart.length) parts.push(t("These IPs keep the start date told them: {list}", { list: apart.join("; ") }));
    ipsSaid = parts.join(" · ");
    const errs = [];
    if (failed.length) errs.push(t("These proxies didn't answer, so their exit isn't known: {list}", { list: [...new Set(failed.map(ipsShown))].join(", ") }));
    if (full.length) errs.push(t("These IPs are told {n} proxies already, so no more were added: {list}", { n: IPS_MAX_PROXIES, list: [...new Set(full)].join(", ") }));
    if (stuck.length) errs.push(t("These proxies' exit moved to an IP told {n} proxies already, so they stay where they were: {list}", { n: IPS_MAX_PROXIES, list: stuck.join(", ") }));
    ipsErr = errs.join(" ");
  } finally {
    ipsImporting = false;
    ipsDraw();
  }
}

// ipsLoad: the list, drawn as it comes in; a read that fails says so and
// offers nothing to write over it
async function ipsLoad() {
  if (!ipsList) ipsDraw();
  // the accounts going out through each IP: as Models › Accounts reads
  // them, drawn once they come in; none read, no IP names any
  const accounts = loadProviders().then(() => { if (view === "ips") ipsDraw(); }, () => {});
  try {
    const got = await api("ips");
    ipsList = got.ips || [];
    ipsRev = got.rev || "";
    ipsErr = "";
  } catch (e) {
    ipsList = null;
    ipsErr = t("Couldn't read the IPs: {error}", { error: e.message });
  }
  if (view === "ips") ipsDraw();
  await accounts;
}

// ipsKeep: the whole list, each IP as it is kept (its period is the
// server's), saved from the revision read; true once kept. One made from
// an older read (another window's change) reads the list again and says
// so, and nothing is written over
async function ipsKeep(next) {
  if (ipsSaving) return false;
  ipsSaving = true;
  try {
    const got = await api("ips", { rev: ipsRev, ips: next.map(({ ip, name, opened, proxies }) => ({ ip, name, opened, proxies: proxies?.length ? proxies : undefined })) });
    ipsList = got.ips || [];
    ipsRev = got.rev || "";
    ipsErr = "";
    return true;
  } catch (e) {
    // read again first: a read that went through clears the last error
    if (/changed elsewhere/.test(e.message)) {
      ipsEdit = null;
      ipsRenewing = "";
      await ipsLoad();
      if (ipsList) ipsErr = t("The list was changed in another window and is read again: make your change again");
    } else ipsErr = t(e.message);
    return false;
  } finally {
    ipsSaving = false;
    ipsDraw();
  }
}

// the filter's states, in Models › Accounts' order of what to act on
const IPS_STATES = ["on", "paused", "off", "again", "authfail", "banned", "disabled"];

// the period filter (T-176): its value and words, and whether an IP's
// period, as the server worked it out (renewal.Of, the one the pill
// says), is in it
const IPS_RENEW = [["none", "No start date"], ["ok", "In its period"], ["soon", "Renew soon"], ["ended", "Period ended"]];
function ipsRenewIs(x, v) {
  const st = x.renewal;
  return v === "none" ? !st : v === "ok" ? !!st && !st.due : !!st && st.due === v;
}

// ipsMatches: whether what is typed (acxLow'd, as Models › Accounts'
// search takes it) is in the IP's address, its name, a proxy it was read
// from (as shown, its login left out) or an account going out through it
function ipsMatches(x, accts, words) {
  return !words || [x.ip, x.name, ...(x.proxies || []).map(ipsShown), ...accts.flatMap((r) => [r.who, r.sub, r.p.name])].some((s) => acxLow(s).includes(words));
}

function ipsDraw() {
  const list = $("#ipsList");
  if (!list) return;
  const xs = ipsList || [];
  // each IP's accounts, once the providers are read
  const rows = providers ? ipsBound() : [];
  const claimed = new Set(xs.flatMap((x) => x.proxies || []));
  const accts = new Map(xs.map((x) => [x, ipsAccountsOf(x, rows, claimed)]));
  const bound = xs.filter((x) => accts.get(x).length).length;
  const sum = $("#ipsSum");
  sum.replaceChildren();
  if (xs.length) {
    const due = xs.filter((x) => x.renewal?.due).length;
    sum.append(el("span", "", t(xs.length === 1 ? "1 IP" : "{n} IPs", { n: xs.length })));
    if (bound) sum.append(" · ", el("span", "", t(bound === 1 ? "1 bound to accounts" : "{n} bound to accounts", { n: bound })));
    if (due) sum.append(" · ", el("span", "acx-warn", t(due === 1 ? "1 to renew" : "{n} to renew", { n: due })));
  }
  // the search and the period filter, offered once there is an IP; a
  // value no longer offered is dropped
  const q = $("#ipsQ");
  q.hidden = !xs.length && !ipsQuery;
  if (ipsRenew && !IPS_RENEW.some(([v]) => v === ipsRenew)) ipsRenew = "";
  const rpick = $("#ipsRenew");
  sessPick(rpick, "Start date: any", ipsRenew, IPS_RENEW.map(([v, name]) => ({ v, name: t(name), note: String(xs.filter((x) => ipsRenewIs(x, v)).length) })), "Start date", (v) => {
    ipsRenew = v;
    try { localStorage.setItem("magpie.ipsRenew", v); } catch {}
    ipsDraw();
  });
  rpick.title = "";
  rpick.hidden = !xs.length && !ipsRenew;
  // the accounts filter: offered once there is an IP, bound to an account
  // or not (T-176); a state no IP's account is in is dropped
  const states = IPS_STATES.filter((k) => xs.some((x) => accts.get(x).some((r) => r.state === k)));
  if (ipsFilter && !["bound", "unbound", ...states].includes(ipsFilter) && providers) {
    ipsFilter = "";
    try { localStorage.setItem("magpie.ipsFilter", ""); } catch {}
  }
  const n = (f) => xs.filter(f).length;
  const opts = [
    { v: "bound", name: t("Bound to accounts"), note: String(bound) },
    { v: "unbound", name: t("Not bound to an account"), note: String(xs.length - bound) },
    ...states.map((k) => ({ v: k, name: t("An account: {state}", { state: t(ACX_SAID[k]) }), note: String(n((x) => accts.get(x).some((r) => r.state === k))) })),
  ];
  const pick = $("#ipsFilter");
  sessPick(pick, "All IPs", ipsFilter, opts, "Accounts", (v) => {
    ipsFilter = v;
    try { localStorage.setItem("magpie.ipsFilter", v); } catch {}
    ipsDraw();
  });
  pick.title = "";
  pick.hidden = !xs.length && !ipsFilter;
  const words = acxLow(ipsQuery);
  const shows = (x) => (!ipsFilter || !providers || (ipsFilter === "bound" ? accts.get(x).length > 0
    : ipsFilter === "unbound" ? !accts.get(x).length : accts.get(x).some((r) => r.state === ipsFilter)))
    && (!ipsRenew || ipsRenewIs(x, ipsRenew)) && ipsMatches(x, accts.get(x), words);
  // the box typed in is the one given back the focus once drawn again
  const typing = document.activeElement?.closest?.("#ipsList .ips-form") ? document.activeElement.className : "";
  list.replaceChildren();
  // the one being edited stays, filtered out or not
  xs.forEach((x, i) => { if (ipsEdit === i) list.append(ipsForm(i)); else if (shows(x)) list.append(ipsRow(x, i, accts.get(x), !!providers)); });
  if (ipsEdit === -1) list.append(ipsForm(-1));
  list.hidden = !list.childElementCount;
  if (typing) list.querySelector(".ips-form ." + typing.split(" ")[0])?.focus();
  $("#ipsAdd").hidden = !ipsList || ipsEdit !== null;
  $("#ipsAdd").disabled = ipsImporting;
  const imp = $("#ipsImport");
  imp.hidden = !ipsList || ipsEdit !== null || !providers;
  imp.classList.toggle("busy", ipsImporting);
  imp.disabled = ipsImporting;
  imp.textContent = t(ipsImporting ? "Reading the accounts' proxies…" : "Import from accounts");
  imp.title = t("Add the exit IP of every proxy an account goes through, its own or its subscription's, as each proxy's requests leave from");
  const said = $("#ipsSaid");
  said.textContent = ipsSaid;
  said.hidden = !ipsSaid;
  const err = $("#ipsErr");
  err.textContent = ipsErr;
  err.hidden = !ipsErr;
  $("#ipsNote").textContent = ipsList && !xs.length && ipsEdit === null
    ? t("No IP yet. Add the exit IPs of the proxies you pay for by the month, or import those your accounts go through: magpie reminds you here 5 days before each one's 30 days end.")
    : xs.length && !list.childElementCount ? t("No IP matches the filter.")
    : t("Each IP's 30 days count from its start date. Kept on this computer only.");
}

// ipsRow: an IP, its name, its period's pill (or its start date being
// picked), the accounts going out through it with their state (none
// said while the accounts aren't read), Edit and Remove
function ipsRow(x, i, accts, read) {
  const row = el("div", "row ips-row" + (x.renewal?.due ? " " + x.renewal.due : ""));
  row.dataset.ip = x.ip;
  const who = el("div", "who");
  const name = el("div", "name");
  name.append(el("code", "ips-ip", x.ip));
  if (ipsRenewing === x.ip) {
    name.append(renewEditor("ip/" + x.ip, x.renewal, (opened) => ipsKeep(ipsList.map((y, j) => (j === i ? { ...y, opened: opened || undefined } : y))).then((ok) => { if (ok) ipsClearedSet(x.ip, !opened); return ok; }),
      () => { ipsRenewing = ""; ipsDraw(); }));
  } else name.append(renewPill(x.renewal, () => { if (ipsImporting) return; ipsRenewing = x.ip; ipsDraw(); }));
  who.append(name);
  if (x.name) who.append(el("div", "sub", x.name));
  if (read) {
    const box = el("div", "ips-accts");
    if (!accts.length) box.append(el("span", "ips-none", t("Not bound to an account")));
    for (const r of accts) {
      const a = el("span", "ips-acct " + r.state);
      a.dataset.account = r.id;
      a.append(el("span", "ips-acct-name", r.who), el("span", "acx-state " + r.state, t(ACX_SAID[r.state])));
      a.title = r.p.name + " · " + r.sub;
      box.append(a);
    }
    who.append(box);
  }
  row.append(who);
  const acts = el("span", "acts");
  const edit = el("button", "text ips-edit", t("Edit"));
  edit.type = "button";
  edit.disabled = ipsImporting;
  edit.onclick = () => { ipsEdit = i; ipsDraft = { ip: x.ip, name: x.name || "", proxies: [...(x.proxies || [])] }; ipsErr = ""; ipsSaid = ""; ipsDraw(); };
  const rm = el("button", "text ips-remove", t("Remove"));
  rm.type = "button";
  rm.disabled = ipsImporting;
  rm.onclick = async () => {
    if (ipsSaving || ipsImporting || !await confirmRemoval(x.name ? x.ip + " (" + x.name + ")" : x.ip, "Its start date and reminder go with it.")) return;
    rm.classList.add("busy");
    ipsKeep(ipsList.filter((y) => y.ip !== x.ip));
  };
  acts.append(edit, rm);
  row.append(acts);
  return row;
}

// ipsForm: an IP's address and name, new (i -1) or the one at i; with
// saved proxies, Read from a proxy fills the address with that proxy's
// exit as Cloudflare sees it
function ipsForm(i) {
  const form = el("div", "row ips-form");
  const fields = el("div", "ips-fields");
  const ip = el("input", "ips-in-ip");
  ip.type = "text";
  ip.spellcheck = false;
  ip.autocomplete = "off";
  ip.placeholder = "203.0.113.7";
  ip.value = ipsDraft.ip;
  ip.setAttribute("aria-label", t("Exit IP"));
  ip.oninput = () => { ipsDraft.ip = ip.value; moved.hidden = !movedOff(); };
  const nm = el("input", "ips-in-name");
  nm.type = "text";
  nm.autocomplete = "off";
  nm.placeholder = t("Its name (optional)");
  nm.value = ipsDraft.name;
  nm.setAttribute("aria-label", t("Its name (optional)"));
  nm.oninput = () => { ipsDraft.name = nm.value; };
  fields.append(ip, nm);
  const saved = (prefs || state?.settings)?.proxies || [];
  if (saved.length) {
    const from = el("button", "sess-pick ips-from", t("Read from a saved proxy"));
    from.type = "button";
    from.title = t("Fill in the address the proxy's requests leave from, as Cloudflare sees it");
    from.setAttribute("aria-haspopup", "menu");
    from.onclick = (e) => {
      e.stopPropagation();
      if (from.classList.contains("open")) return closeProtoMenu();
      openProtoMenu(from, saved.map((p) => ({ v: p.url, name: p.name, literalName: true, note: "" })), "", (url) => {
        const p = saved.find((s) => s.url === url);
        if (!p) return;
        from.classList.add("busy");
        ipsErr = "";
        api("proxy/check", { proxy: url }).then((out) => {
          const exit = out?.through?.ip;
          if (exit) {
            ipsDraft.ip = exit;
            if (!ipsDraft.proxies.includes(url)) ipsDraft.proxies = [...ipsDraft.proxies, url];
            if (!ipsDraft.name.trim()) ipsDraft.name = p.name;
          } else ipsErr = t("The proxy didn't answer: {error}", { error: out?.through?.error || "" });
        }, (e) => { ipsErr = t(e.message); }).finally(ipsDraw);
      }, "Saved proxies");
    };
    fields.append(from);
  }
  form.append(fields);
  // the proxies the IP was read from, each unlinked here (T-172): one whose
  // exit moved, or an address typed over, leaves them with nothing to say
  // they no longer go out through it
  // only those it was read from before: one read here goes out through it
  const was = i < 0 ? "" : ipsList[i].ip, had = i < 0 ? [] : ipsList[i].proxies || [];
  const movedOff = () => !!(was && ipsDraft.proxies.some((p) => had.includes(p)) && ipsCanon(ipsDraft.ip.trim()) !== ipsCanon(was));
  const moved = el("div", "ips-moved", t("The address changed: these were read as {ip}'s exit, so remove any that doesn't go out through the new one", { ip: was }));
  moved.hidden = !movedOff();
  if (ipsDraft.proxies.length) {
    const box = el("div", "ips-proxies");
    box.append(el("span", "ips-proxies-head", t("Proxies that go out through it")));
    for (const p of ipsDraft.proxies) {
      const chip = el("span", "ips-proxy");
      chip.dataset.proxy = p;
      const rm = el("button", "ips-proxy-rm", "×");
      rm.type = "button";
      rm.title = t("Unlink {proxy}", { proxy: ipsShown(p) });
      rm.setAttribute("aria-label", rm.title);
      rm.onclick = () => { ipsDraft.proxies = ipsDraft.proxies.filter((q) => q !== p); ipsDraw(); };
      chip.append(el("code", "", ipsShown(p)), rm);
      box.append(chip);
    }
    form.append(box, moved);
  }
  const acts = el("span", "acts");
  const ok = el("button", "text primary ips-save", t("Save"));
  ok.type = "button";
  ok.onclick = async () => {
    const v = { ip: ipsDraft.ip.trim(), name: ipsDraft.name.trim() };
    if (!v.ip) return ip.focus();
    if (ok.classList.contains("busy")) return;
    ok.classList.add("busy");
    // the proxies kept; one read from a saved proxy here leaves any other
    // IP it was read from before, as an import moves it (review L4)
    const ps = ipsDraft.proxies, fresh = ps.filter((p) => !had.includes(p));
    const off = (y) => (fresh.some((p) => (y.proxies || []).includes(p)) ? { ...y, proxies: y.proxies.filter((p) => !fresh.includes(p)) } : y);
    const next = i < 0 ? [...ipsList.map(off), { ...v, proxies: ps }] : ipsList.map((y, j) => (j === i ? { ...y, ...v, proxies: ps } : off(y)));
    // kept, the form closes; refused, ipsKeep drew it again with why
    if (await ipsKeep(next)) { ipsEdit = null; ipsDraw(); }
  };
  const no = el("button", "text ips-cancel", t("Cancel"));
  no.type = "button";
  no.onclick = () => { ipsEdit = null; ipsErr = ""; ipsDraw(); };
  for (const box of [ip, nm]) box.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter") ok.click();
    if (e.key === "Escape") no.click();
  };
  acts.append(ok, no);
  form.append(acts);
  setTimeout(() => (document.activeElement?.closest?.(".ips-form") ? null : ip.focus()), 0);
  return form;
}
