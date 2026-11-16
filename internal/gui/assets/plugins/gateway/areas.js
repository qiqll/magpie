// The Gateway page's Areas (plugins/gateway, docs/subsystems/areas.md):
// the owner makes an area (学习, 工作…), picks the providers and
// subscriptions its admin may add, gives it accounts magpie has already
// or takes them back, and copies or rotates its admin key. The console's
// key, which reads every area and makes them, and the admin allow list,
// the addresses the console may reach /v1/magpie/admin from, are here
// too. What an area's admin does is the gateway's admin routes', from
// magpie-console; nothing here routes a request.
//
// Classic script, loaded before client.js and app.js: nothing here runs
// as it loads.

let areasState = null; // what /api/areas said last, or {error}
let areaOpen = "";      // the area whose details are shown
let areaDraft = null;   // a new area's note being typed, or null

// loadAreas never throws: it is read with the page's other blocks
// (gateway.open), and a failure here must not take the keys down with it
async function loadAreas() {
  try {
    const out = await api("areas");
    areasState = Array.isArray(out?.areas) ? out : { error: "magpie couldn't read its areas" };
  } catch (e) { areasState = { error: e.message }; }
  try { renderAreas(); } catch (e) { console.error("[magpie] areas:", e); }
}

async function areaAction(action, body, said) {
  try {
    const out = await api("areas/" + action, body);
    if (Array.isArray(out?.state?.areas)) areasState = out.state;
    renderAreas();
    if (said) status(t(said), "ok");
    return out;
  } catch (e) { status(t(e.message), "err"); return null; }
}

// areaSecret: an admin key, shown once as it is made or rotated, with a Copy
async function areaSecret(out, what) {
  if (!out?.secret) return;
  try { await copy(out.secret, t(what)); } catch {}
  status(t("{what} copied: give it to its holder. magpie shows it only on Copy.", { what: t(what) }), "ok");
}

function renderAreas() {
  const box = $("#areas");
  if (!box) return;
  $("#areasBlock").hidden = false;
  $("#addArea").onclick = () => { areaDraft = ""; renderAreas(); };
  box.replaceChildren();
  if (!areasState) return;
  if (areasState.error) { box.append(el("div", "none", t(areasState.error))); return; }
  const list = el("div", "accts");
  if (areaDraft !== null) list.append(areaForm());
  if (!areasState.areas.length && areaDraft === null) list.append(el("div", "none", t("No areas yet")));
  for (const a of areasState.areas) list.append(areaRow(a));
  box.append(list, consoleRow(), adminAllowRow());
}

function areaForm() {
  const row = el("div", "acc editing");
  const note = input(areaDraft, t("Note, such as Study or Work"));
  note.oninput = () => { areaDraft = note.value; };
  const save = el("button", "text primary", t("Add area"));
  const go = async () => {
    const out = await areaAction("create", { note: note.value }, "Area added");
    if (out) { areaDraft = null; renderAreas(); areaSecret(out, "Area admin key"); }
  };
  save.onclick = go;
  note.onkeydown = (e) => { if (e.key === "Enter") go(); else if (e.key === "Escape") { areaDraft = null; renderAreas(); } };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = () => { areaDraft = null; renderAreas(); };
  row.append(note, el("span", "grow"), cancel, save);
  setTimeout(() => note.focus(), 0);
  return row;
}

function areaRow(a) {
  const wrap = el("div", "area");
  const row = el("div", "acc" + (a.off ? " off" : " in-use"));
  const tick = el("button", "dot tick");
  tick.title = t(a.off ? "Turn the area on" : "Turn the area off");
  tick.setAttribute("aria-label", tick.title);
  if (!a.off) tick.append(svg(CHECK, 10, 2.2));
  tick.onclick = () => areaAction("update", { id: a.id, off: !a.off });
  const name = el("button", "text", a.note);
  name.setAttribute("aria-expanded", String(areaOpen === a.id));
  name.onclick = () => { areaOpen = areaOpen === a.id ? "" : a.id; renderAreas(); };
  const count = el("span", "note", t("{n} account(s)", { n: a.accounts }));
  const today = el("span", "note mono", t("today {n} calls", { n: a.today?.requests || 0 }));
  const cp = copyBtn("", t("Area admin key"));
  cp.setAttribute("aria-label", t("Copy area admin key"));
  cp.onclick = async () => {
    const out = await areaAction("copy", { id: a.id });
    if (out?.secret) await copy(out.secret, t("Area admin key"), cp);
  };
  const rot = el("button", "text", t("Rotate key"));
  rot.onclick = () => askArea(a, "rotate");
  const rm = el("button", "text", t("Delete"));
  rm.onclick = () => askArea(a, "delete");
  row.append(tick, name, count, today, el("span", "grow"), cp, rot, rm);
  wrap.append(row);
  if (areaOpen === a.id) wrap.append(areaDetails(a));
  return wrap;
}

// areaDetails: what its admin may add, and the accounts it owns
function areaDetails(a) {
  const d = el("div", "area-details");
  const cat = areasState.catalog || { providers: [], signIns: [] };
  const picks = (label, all, chosen, key) => {
    const line = el("div", "row");
    line.append(el("span", "label", t(label)));
    for (const o of all) {
      const id = o.id || o.agent, on = chosen.includes(id);
      const b = el("button", "chip" + (on ? " set" : ""), o.name);
      b.setAttribute("aria-pressed", String(on));
      b.onclick = () => areaAction("update", { id: a.id, [key]: on ? chosen.filter((x) => x !== id) : [...chosen, id] });
      line.append(b);
    }
    if (!all.length) line.append(el("span", "note", t("None")));
    return line;
  };
  d.append(picks("Its admin may add keys of", cat.providers, a.providers, "providers"),
    picks("Its admin may sign in to", cat.signIns, a.signIns, "signIns"));
  const owned = el("div", "accts");
  for (const acc of a.list) {
    const r = el("div", "acc" + (acc.gone ? " off" : ""));
    const back = el("button", "text", t("Take back"));
    back.title = t("The account stays in magpie, and is no area's");
    back.onclick = () => areaAction("release", { ref: acc.id }, "Taken back");
    r.append(el("span", "", acc.providerName || acc.provider), el("span", "plan mono", acc.masked || acc.name),
      ...(acc.gone ? [el("span", "note", t("removed from magpie"))] : []), el("span", "grow"), back);
    owned.append(r);
  }
  if (!a.list.length) owned.append(el("div", "none", t("This area owns no account yet")));
  d.append(owned);
  const unowned = areasState.unowned || [];
  if (unowned.length) {
    const give = el("select");
    give.setAttribute("aria-label", t("Give it an account"));
    give.append(new Option(t("Give it an account…"), ""));
    for (const acc of unowned) give.append(new Option(`${acc.providerName || acc.provider} · ${acc.masked || acc.name}`, acc.id));
    give.onchange = () => { if (give.value) areaAction("claim", { id: a.id, ref: give.value }, "Account given"); };
    d.append(give);
  }
  return d;
}

function askArea(a, what) {
  const ed = el("div", "editor");
  const head = el("div", "ehead");
  head.append(el("b", "", t(what === "rotate" ? "Rotate the area admin key?" : "Delete area?")));
  ed.append(head, el("p", "lib-confirm", t(what === "rotate"
    ? "{name}'s admin will need the new key: the old one stops working at once."
    : a.accounts ? "{name} still owns accounts: its admin removes them, or you take them back, before it can be deleted."
      : "{name} and its admin key are deleted. Its accounts' usage history is kept.", { name: a.note })));
  const bar = el("div", "bar");
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelAsk;
  const go = el("button", "text primary", t(what === "rotate" ? "Rotate key" : "Delete"));
  go.disabled = what === "delete" && a.accounts > 0;
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    const out = await areaAction(what, { id: a.id }, what === "rotate" ? "" : "Area deleted");
    closeConfirmAsk();
    if (what === "rotate") areaSecret(out, "Area admin key");
  };
  bar.append(cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
}

// consoleRow: magpie-console's key, one at most
function consoleRow() {
  const row = el("div", "row area-console");
  row.append(el("span", "label", t("Console key")));
  const k = areasState.consoleKey;
  if (!k) {
    const add = el("button", "text", t("Make the console's key"));
    add.onclick = async () => areaSecret(await areaAction("console-add", {}, ""), "Console key");
    row.append(el("span", "note", t("None: magpie-console can't reach this magpie")), el("span", "grow"), add);
    return row;
  }
  const tick = el("button", "dot tick");
  tick.title = t(k.off ? "Enable key" : "Disable key");
  tick.setAttribute("aria-label", tick.title);
  if (!k.off) tick.append(svg(CHECK, 10, 2.2));
  tick.onclick = () => areaAction(k.off ? "console-on" : "console-off", {});
  const cp = copyBtn("", t("Console key"));
  cp.setAttribute("aria-label", t("Copy console key"));
  cp.onclick = async () => {
    const out = await areaAction("console-copy", {});
    if (out?.secret) await copy(out.secret, t("Console key"), cp);
  };
  const rot = el("button", "text", t("Rotate key"));
  rot.onclick = async () => areaSecret(await areaAction("console-rotate", {}, ""), "Console key");
  const rm = el("button", "text", t("Remove"));
  rm.onclick = () => areaAction("console-remove", {}, "Console key removed");
  row.append(tick, el("span", "plan mono", k.masked || ""), el("span", "grow"), cp, rot, rm);
  return row;
}

// adminAllowRow: the addresses the admin routes answer besides this computer
function adminAllowRow() {
  const row = el("div", "row area-allow");
  row.append(el("span", "label", t("Admin allow list")));
  const list = areasState.adminAllowIPs || [];
  if (areasState.adminAllowIPsEnv) {
    row.append(el("span", "note mono", list.join(", ")), el("span", "note", t("set by {env}", { env: areasState.adminAllowIPsEnv })));
    return row;
  }
  const box = input(list.join(", "), t("This computer only. Add IPs or ranges, such as 192.168.1.20 or 10.0.0.0/24"));
  box.setAttribute("aria-label", t("Admin allow list"));
  const save = el("button", "text", t("Save"));
  save.onclick = () => areaAction("allow-ips", { ips: box.value.split(/[\s,]+/).filter(Boolean) }, "Admin allow list saved");
  row.append(box, save);
  return row;
}
