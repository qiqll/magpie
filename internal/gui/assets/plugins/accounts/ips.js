// Models › Proxy IPs (T-155, client.js has its markup and hooks): the
// proxy exit IPs the user pays for by the month, each with its start date
// and the same period pill as Models › Accounts (renew.js), the period
// worked out by the server (/api/ips, renewal.Of). An IP is typed, or
// read as the exit of a saved proxy (Settings › Network, proxy/check);
// the list is kept whole, as Settings' saved proxies are. Run after app.js.

// the list as the server last said it, the form open (the index edited,
// -1 for a new one, null for none) and its draft, the IP whose start date
// is being picked, and the last error
let ipsList = null, ipsEdit = null, ipsDraft = { ip: "", name: "" }, ipsRenewing = "", ipsErr = "";

function ipsMount() {
  const add = $("#ipsAdd");
  add.onclick = () => { ipsEdit = -1; ipsDraft = { ip: "", name: "" }; ipsErr = ""; ipsDraw(); };
}

// ipsLoad: the list, drawn as it comes in; a read that fails says so and
// offers nothing to write over it
async function ipsLoad() {
  if (!ipsList) ipsDraw();
  try {
    ipsList = (await api("ips")).ips || [];
    ipsErr = "";
  } catch (e) {
    ipsList = null;
    ipsErr = t("Couldn't read the IPs: {error}", { error: e.message });
  }
  if (view === "ips") ipsDraw();
}

// ipsKeep: the whole list, each IP as it is kept (its period is the
// server's), saved; true once kept
async function ipsKeep(next) {
  try {
    ipsList = (await api("ips", { ips: next.map(({ ip, name, opened }) => ({ ip, name, opened })) })).ips || [];
    ipsErr = "";
    ipsDraw();
    return true;
  } catch (e) {
    ipsErr = t(e.message);
    ipsDraw();
    return false;
  }
}

function ipsDraw() {
  const list = $("#ipsList");
  if (!list) return;
  const xs = ipsList || [];
  const sum = $("#ipsSum");
  sum.replaceChildren();
  if (xs.length) {
    const due = xs.filter((x) => x.renewal?.due).length;
    sum.append(el("span", "", t(xs.length === 1 ? "1 IP" : "{n} IPs", { n: xs.length })));
    if (due) sum.append(" · ", el("span", "acx-warn", t(due === 1 ? "1 to renew" : "{n} to renew", { n: due })));
  }
  list.replaceChildren();
  list.hidden = !xs.length && ipsEdit === null;
  xs.forEach((x, i) => list.append(ipsEdit === i ? ipsForm(i) : ipsRow(x, i)));
  if (ipsEdit === -1) list.append(ipsForm(-1));
  $("#ipsAdd").hidden = !ipsList || ipsEdit !== null;
  const err = $("#ipsErr");
  err.textContent = ipsErr;
  err.hidden = !ipsErr;
  $("#ipsNote").textContent = ipsList && !xs.length && ipsEdit === null
    ? t("No IP yet. Add the exit IPs of the proxies you pay for by the month: magpie reminds you here 5 days before each one's 30 days end.")
    : t("Each IP's 30 days count from its start date. Kept on this computer only.");
}

// ipsRow: an IP, its name, its period's pill (or its start date being
// picked), Edit and Remove
function ipsRow(x, i) {
  const row = el("div", "row ips-row" + (x.renewal?.due ? " " + x.renewal.due : ""));
  row.dataset.ip = x.ip;
  const who = el("div", "who");
  const name = el("div", "name");
  name.append(el("code", "ips-ip", x.ip));
  if (ipsRenewing === x.ip) {
    name.append(renewEditor(x.renewal, (opened) => ipsKeep(ipsList.map((y, j) => (j === i ? { ...y, opened: opened || undefined } : y))),
      () => { ipsRenewing = ""; ipsDraw(); }));
  } else name.append(renewPill(x.renewal, () => { ipsRenewing = x.ip; ipsDraw(); }));
  who.append(name);
  if (x.name) who.append(el("div", "sub", x.name));
  row.append(who);
  const acts = el("span", "acts");
  const edit = el("button", "text ips-edit", t("Edit"));
  edit.type = "button";
  edit.onclick = () => { ipsEdit = i; ipsDraft = { ip: x.ip, name: x.name || "" }; ipsErr = ""; ipsDraw(); };
  const rm = el("button", "text ips-remove", t("Remove"));
  rm.type = "button";
  rm.onclick = () => { rm.classList.add("busy"); ipsKeep(ipsList.filter((_, j) => j !== i)); };
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
    const next = i < 0 ? [...ipsList, v] : ipsList.map((y, j) => (j === i ? { ...y, ...v } : y));
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
