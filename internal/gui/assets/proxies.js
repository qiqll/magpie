// Settings › Network's saved proxies (T-110: 每加一个账户就需要修改一次全局
// 代理): proxies the user named, each with a Test, to pick from where a
// proxy is picked (savedProxyFill below, signInProxyChoices in app.js). Picking
// one copies its address, so editing or removing one here changes nothing
// picked before. app.js imports this when Settings draws Network, so it
// weighs nothing on the core.

// what each one's Test found, by its address, and the form open: the
// index edited, -1 for a new one, null for none; kept over redraws
const spChecks = {};
let spEdit = null;
let spDraft = { name: "", url: "" };
let spErr = "";

window.savedProxiesFill = function (row, s) {
  if (!row) return;
  const list = s.proxies || [];
  row.hidden = false;
  const who = el("div", "who");
  who.append(el("div", "name", t("Saved proxies")),
    el("div", "sub", t("Name the proxies you use, then pick one when adding an account, for a provider or an account, or above. Picking one copies its address: a change here changes nothing picked before.")));
  const box = el("div", "stack saved-proxies");
  row.replaceChildren(who, box);
  const redraw = () => savedProxiesFill(row.isConnected ? row : document.getElementById("savedProxies"), prefs || s);
  const keep = (next) => api("settings/proxies", { proxies: next }).then((st) => {
    prefs = st;
    if (state) state.settings = st;
    spEdit = null;
    spErr = "";
    redraw();
  }, (e) => { spErr = e.message; redraw(); });
  list.forEach((p, i) => {
    const line = el("div", "sp-line");
    line.dataset.name = p.name;
    const words = el("span", "sp-words");
    words.append(el("span", "sp-name", p.name), el("code", "sp-addr", proxyShown(p.url)));
    const c = spChecks[p.url];
    const test = el("button", "link sp-test", c?.busy ? t("Testing…") : t("Test"));
    test.type = "button";
    test.disabled = !!c?.busy;
    test.title = t("Ask Cloudflare through the proxy and direct, and compare the addresses the two leave from");
    test.onclick = () => {
      spChecks[p.url] = { busy: true };
      redraw();
      api("proxy/check", { proxy: p.url })
        .then((out) => { spChecks[p.url] = { out }; }, (e) => { spChecks[p.url] = { err: t(e.message) }; })
        .finally(redraw);
    };
    const edit = el("button", "link sp-edit", t("Edit"));
    edit.type = "button";
    edit.onclick = () => { spEdit = i; spDraft = { ...p }; spErr = ""; redraw(); };
    const rm = el("button", "link sp-remove", t("Remove"));
    rm.type = "button";
    rm.onclick = () => keep(list.filter((_, j) => j !== i));
    const acts = el("span", "acts");
    acts.append(test, edit, rm);
    line.append(words, acts);
    box.append(line);
    const [text, bad] = proxyCheckText(c);
    if (text) {
      const said = el("div", "sub sp-check", text);
      said.setAttribute("role", "status");
      said.classList.toggle("err", bad);
      box.append(said);
    }
    if (spEdit === i) box.append(spForm(list, keep, redraw));
  });
  if (spEdit === -1) box.append(spForm(list, keep, redraw));
  else {
    const add = el("button", "text sp-add", t("Add a proxy"));
    add.type = "button";
    add.onclick = () => { spEdit = -1; spDraft = { name: "", url: "" }; spErr = ""; redraw(); };
    box.append(add);
  }
};

// spForm: a saved proxy's name and address, with its login in fields of
// their own as the other proxy rows have (proxyLoginRow).
function spForm(list, keep, redraw) {
  const form = el("form", "stack sp-form");
  const name = input(spDraft.name, t("Name, like Home or Office"));
  name.className = "sp-name-in";
  name.setAttribute("aria-label", t("Name"));
  const addr = input(spDraft.url, "http://127.0.0.1:7890");
  addr.className = "sp-url-in";
  addr.setAttribute("aria-label", t("Proxy address"));
  name.oninput = () => { spDraft.name = name.value; };
  const login = proxyLoginRow(addr, (v) => { spDraft.url = v; });
  const save = el("button", "text primary sp-save", t("Save"));
  save.type = "submit";
  const cancel = el("button", "text sp-cancel", t("Cancel"));
  cancel.type = "button";
  cancel.onclick = () => { spEdit = null; spErr = ""; redraw(); };
  form.onsubmit = (e) => {
    e.preventDefault();
    const p = { name: (spDraft.name || "").trim(), url: (spDraft.url || addr.value || "").trim() };
    if (!p.name || !p.url) { spErr = t("A saved proxy needs a name and an address"); return redraw(); }
    const next = list.slice();
    if (spEdit >= 0) next[spEdit] = p;
    else next.push(p);
    keep(next);
  };
  const acts = el("span", "acts");
  acts.append(cancel, save);
  form.append(name, addr, login, acts);
  if (spErr) form.append(el("div", "sub err sp-err", spErr));
  for (const f of form.querySelectorAll("input")) f.onkeydown = (e) => e.stopPropagation();
  setTimeout(() => { if (name.isConnected && !name.value) name.focus(NO_SCROLL); });
  return form;
}

// savedProxyFill adds Saved… to a proxy's login row (auth): the saved
// proxy picked is put in addr, its login moved into the row's fields as a
// pasted one is, then after is called. None in the saved list's own form.
window.savedProxyFill = function (auth, addr, after) {
  const ps = (state?.settings || prefs)?.proxies || [];
  auth.querySelector(".saved-proxy")?.remove();
  if (!ps.length || auth.closest(".saved-proxies")) return;
  const b = el("button", "link saved-proxy", t("Saved…"));
  b.type = "button";
  b.onclick = (e) => {
    e.stopPropagation();
    openProtoMenu(b, ps.map((x) => ({ v: x.url, name: x.name, literalName: true, note: proxyShown(x.url) })), "", (v) => {
      for (const f of auth.querySelectorAll("input.proxy-credential")) f.value = "";
      addr.value = v;
      addr.oninput();
      for (const f of auth.querySelectorAll("input.proxy-credential")) f.oninput?.();
      after?.();
    }, "Saved proxies");
  };
  auth.append(b);
};

// pastePickFill draws, beside a sign-in's Callback URL field, the proxy
// the address is traded through (T-110): the global one, Direct, a saved
// one, the subscription's or an account's, or one typed. A sign-in that
// keeps the proxy it started through (proxyFixed) says to pick from Proxy
// above instead. flow.viaBody is what Finish sign-in sends with it.
window.pastePickFill = function (box, sub, flow) {
  box.replaceChildren();
  if (flow.proxyFixed) {
    flow.viaBody = {};
    box.append(el("span", "s proxy-fixed", t("This sign-in keeps the proxy it started through. To finish it through another, pick it from Proxy above: the sign-in starts again.")));
    return;
  }
  const now = flow.proxy || "";
  const body = () => { flow.viaBody = flow.pasteVia != null && flow.pasteVia !== now ? { proxy: flow.pasteVia } : {}; };
  body();
  const via = flow.pasteVia ?? now;
  const b = el("button", "link via paste-via", t("Through: {proxy}", { proxy: proxyName(via) }));
  b.type = "button";
  b.title = t("The address is traded for the account through this proxy, which is kept as the account's. The global proxy stays as it is.");
  b.setAttribute("aria-haspopup", "menu");
  b.onclick = (e) => {
    e.stopPropagation();
    openProtoMenu(b, signInProxyChoices(sub), flow.askVia ? "\x01" : via, (v) => {
      if (signing !== flow) return;
      flow.askVia = v === "\x01";
      if (!flow.askVia) flow.pasteVia = v;
      renderProviders();
    }, "Finish through");
  };
  box.append(b);
  if (!flow.askVia) return;
  const ask = el("span", "callback-form proxy-ask");
  const cp = input(flow.pasteVia && flow.pasteVia !== now ? flow.pasteVia : "", "http://127.0.0.1:7890");
  cp.className = "paste-proxy";
  cp.setAttribute("aria-label", t("Proxy"));
  const login = proxyLoginRow(cp, (v) => { flow.pasteVia = v.trim() || null; body(); });
  for (const f of [cp, ...login.querySelectorAll("input")]) f.onkeydown = (e) => e.stopPropagation();
  ask.append(cp, login);
  box.append(ask);
};
