// Models › Proxy IPs (T-155, client.js has its markup and hooks): the
// proxy exit IPs the user pays for by the month, each with its start date
// and the same period pill as Models › Accounts (renew.js), the period
// worked out by the server (/api/ips, renewal.Of). An IP is typed, or
// read as the exit of a saved proxy (Settings › Network, proxy/check);
// the list is kept whole, as Settings' saved proxies are. Import from
// accounts (T-170) reads the exit of every proxy an account goes through
// and adds what isn't listed; each IP names the accounts that go out
// through it with their state (page.js' acxRows), found by that state.
// Run after app.js and page.js.

// the list as the server last said it and the revision it was read at
// (a save made from an older one is refused), the form open (the index
// edited, -1 for a new one, null for none) and its draft, the IP whose
// start date is being picked, the last error, and whether a save is under
// way: the list takes no other change till it is answered (T-155 review M2)
let ipsList = null, ipsRev = "", ipsEdit = null, ipsDraft = { ip: "", name: "", proxy: "" }, ipsRenewing = "", ipsErr = "", ipsSaving = false;
// Import from accounts under way, and what the last one did
let ipsImporting = false, ipsSaid = "";
// the accounts filter, kept per machine: "" every IP, "bound" those an
// account goes through, "unbound" those none does, or an account state
// (ACX_SAID's) one of its accounts is in
let ipsFilter = "";
try { ipsFilter = localStorage.getItem("magpie.ipsFilter") || ""; } catch {}

function ipsMount() {
  const add = $("#ipsAdd");
  add.onclick = () => { ipsEdit = -1; ipsDraft = { ip: "", name: "", proxy: "" }; ipsErr = ""; ipsSaid = ""; ipsDraw(); };
  $("#ipsImport").onclick = ipsImport;
}

// ipsProxyOf: the proxy an account's requests go out through when it is
// one of its own or its subscription's (provider.ProxyOfLogin), "" for
// the global one or none (direct)
function ipsProxyOf(r) {
  const v = String(r.p.accountProxies?.[acxLow(r.l.user)] || r.p.proxy || "").trim();
  return v === "direct" ? "" : v;
}

// ipsHostOf: the proxy's host when it is an IP address, in lower case
// and without brackets; "" for a name
function ipsHostOf(proxy) {
  try {
    const h = new URL(proxy.includes("://") ? proxy : "http://" + proxy).hostname.replace(/^\[|\]$/g, "").toLowerCase();
    return /^\d{1,3}(\.\d{1,3}){3}$/.test(h) || h.includes(":") ? h : "";
  } catch { return ""; }
}

// ipsAccountsOf: the accounts going out through x: their proxy is one x
// was read as the exit of, or one at x itself
const ipsAccountsOf = (x, rows) => rows.filter((r) => {
  const p = ipsProxyOf(r);
  return p && ((x.proxies || []).includes(p) || ipsHostOf(p) === x.ip.toLowerCase());
});

// ipsShown: a proxy as it can be said, its login left out
function ipsShown(proxy) {
  try { return new URL(proxy.includes("://") ? proxy : "http://" + proxy).host || "[proxy]"; } catch { return "[proxy]"; }
}

// ipsImport: the exit of each proxy an account goes through that no IP
// was read from yet (proxy/check, four at a time), added as an IP of its
// own, or told to the IP listed at that address; kept as one save. A
// proxy that didn't answer is named, and nothing is guessed for it
async function ipsImport() {
  if (ipsImporting || ipsSaving || !ipsList) return;
  ipsErr = "";
  ipsSaid = "";
  const known = new Set(ipsList.flatMap((x) => x.proxies || []));
  const all = [...new Set(acxRows().map(ipsProxyOf).filter(Boolean))];
  const todo = all.filter((p) => !known.has(p));
  if (!todo.length) {
    ipsSaid = t(all.length ? "Every proxy an account goes through is listed already" : "No account has a proxy of its own or its subscription's");
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
    if (!ipsList) return;
    // in the accounts' order, so the list reads the same each time
    found.sort((a, b) => todo.indexOf(a[0]) - todo.indexOf(b[0]));
    const saved = (prefs || state?.settings)?.proxies || [];
    const next = ipsList.map((x) => ({ ...x, proxies: [...(x.proxies || [])] }));
    let added = 0, linked = 0;
    for (const [p, ip] of found) {
      const x = next.find((y) => y.ip.toLowerCase() === ip.toLowerCase());
      if (!x) {
        next.push({ ip, name: saved.find((s) => s.url === p)?.name || "", proxies: [p] });
        added++;
      } else if (!x.proxies.includes(p)) {
        x.proxies.push(p);
        linked++;
      }
    }
    if ((added || linked) && !await ipsKeep(next)) return;
    const parts = [t(added === 1 ? "1 IP added" : "{n} IPs added", { n: added })];
    if (linked) parts.push(t(linked === 1 ? "1 proxy told to an IP listed" : "{n} proxies told to IPs listed", { n: linked }));
    ipsSaid = parts.join(" · ");
    if (failed.length) ipsErr = t("These proxies didn't answer, so their exit isn't known: {list}", { list: [...new Set(failed.map(ipsShown))].join(", ") });
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

function ipsDraw() {
  const list = $("#ipsList");
  if (!list) return;
  const xs = ipsList || [];
  // each IP's accounts, once the providers are read
  const rows = providers ? acxRows() : [];
  const accts = new Map(xs.map((x) => [x, ipsAccountsOf(x, rows)]));
  const bound = xs.filter((x) => accts.get(x).length).length;
  const sum = $("#ipsSum");
  sum.replaceChildren();
  if (xs.length) {
    const due = xs.filter((x) => x.renewal?.due).length;
    sum.append(el("span", "", t(xs.length === 1 ? "1 IP" : "{n} IPs", { n: xs.length })));
    if (bound) sum.append(" · ", el("span", "", t(bound === 1 ? "1 bound to accounts" : "{n} bound to accounts", { n: bound })));
    if (due) sum.append(" · ", el("span", "acx-warn", t(due === 1 ? "1 to renew" : "{n} to renew", { n: due })));
  }
  // the filter: offered once an IP has an account; a state no IP's
  // account is in is dropped
  const states = IPS_STATES.filter((k) => xs.some((x) => accts.get(x).some((r) => r.state === k)));
  if (ipsFilter && !["bound", "unbound", ...states].includes(ipsFilter) && providers) ipsFilter = "";
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
  pick.hidden = !bound && !ipsFilter;
  const shows = (x) => !ipsFilter || !providers || (ipsFilter === "bound" ? accts.get(x).length > 0
    : ipsFilter === "unbound" ? !accts.get(x).length : accts.get(x).some((r) => r.state === ipsFilter));
  // the box typed in is the one given back the focus once drawn again
  const typing = document.activeElement?.closest?.("#ipsList .ips-form") ? document.activeElement.className : "";
  list.replaceChildren();
  // the one being edited stays, filtered out or not
  xs.forEach((x, i) => { if (ipsEdit === i) list.append(ipsForm(i)); else if (shows(x)) list.append(ipsRow(x, i, accts.get(x), !!providers)); });
  if (ipsEdit === -1) list.append(ipsForm(-1));
  list.hidden = !list.childElementCount;
  if (typing) list.querySelector(".ips-form ." + typing.split(" ")[0])?.focus();
  $("#ipsAdd").hidden = !ipsList || ipsEdit !== null;
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
    name.append(renewEditor("ip/" + x.ip, x.renewal, (opened) => ipsKeep(ipsList.map((y, j) => (j === i ? { ...y, opened: opened || undefined } : y))),
      () => { ipsRenewing = ""; ipsDraw(); }));
  } else name.append(renewPill(x.renewal, () => { ipsRenewing = x.ip; ipsDraw(); }));
  who.append(name);
  if (x.name) who.append(el("div", "sub", x.name));
  if (read) {
    const box = el("div", "ips-accts");
    if (!accts.length) box.append(el("span", "ips-none", t("Not bound to an account")));
    for (const r of accts) {
      const a = el("span", "ips-acct " + r.state);
      a.dataset.account = r.p.id + "/" + acxLow(r.l.user);
      a.append(el("span", "ips-acct-name", accountShownOf(r.p, r.l.user)), el("span", "acx-state " + r.state, t(ACX_SAID[r.state])));
      a.title = r.p.name + " · " + r.l.user;
      box.append(a);
    }
    who.append(box);
  }
  row.append(who);
  const acts = el("span", "acts");
  const edit = el("button", "text ips-edit", t("Edit"));
  edit.type = "button";
  edit.onclick = () => { ipsEdit = i; ipsDraft = { ip: x.ip, name: x.name || "", proxy: "" }; ipsErr = ""; ipsSaid = ""; ipsDraw(); };
  const rm = el("button", "text ips-remove", t("Remove"));
  rm.type = "button";
  rm.onclick = async () => {
    if (ipsSaving || !await confirmRemoval(x.name ? x.ip + " (" + x.name + ")" : x.ip, "Its start date and reminder go with it.")) return;
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
  ip.oninput = () => { ipsDraft.ip = ip.value; };
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
            ipsDraft.proxy = url;
            if (!ipsDraft.name.trim()) ipsDraft.name = p.name;
          } else ipsErr = t("The proxy didn't answer: {error}", { error: out?.through?.error || "" });
        }, (e) => { ipsErr = t(e.message); }).finally(ipsDraw);
      }, "Saved proxies");
    };
    fields.append(from);
  }
  form.append(fields);
  const acts = el("span", "acts");
  const ok = el("button", "text primary ips-save", t("Save"));
  ok.type = "button";
  ok.onclick = async () => {
    const v = { ip: ipsDraft.ip.trim(), name: ipsDraft.name.trim() };
    if (!v.ip) return ip.focus();
    if (ok.classList.contains("busy")) return;
    ok.classList.add("busy");
    // read from a saved proxy: the IP is told it is that one's exit
    const told = (ps) => (ipsDraft.proxy && !(ps || []).includes(ipsDraft.proxy) ? [...(ps || []), ipsDraft.proxy] : ps);
    const next = i < 0 ? [...ipsList, { ...v, proxies: told([]) }] : ipsList.map((y, j) => (j === i ? { ...y, ...v, proxies: told(y.proxies) } : y));
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
