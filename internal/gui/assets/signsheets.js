// Sign-ins that ask one value before they start. app.js imports this only
// when one is asked for (signAsk), so it weighs nothing on the core.

// Copilot's enterprise on GHE.com: its host, asked before its sign-in starts
window.copilotGHESheet = function () {
  const box = el("div", "signing copilot-ghe");
  const tt = el("span", "tt");
  box.append(tt);
  const flow = signing;
  const label = t("Your enterprise on GHE.com");
  tt.append(el("span", "n", label),
    el("span", "s", t("The address you sign in to GitHub at, like acme.ghe.com. An account on github.com doesn't need it.")));
  const form = el("form", "callback-form");
  const inp = input(flow.host || "", "acme.ghe.com");
  inp.setAttribute("aria-label", label);
  const go = el("button", "text primary", t("Sign in"));
  go.type = "submit";
  go.disabled = !GHE_HOST.test(inp.value.trim());
  const why = el("span", "s why", "");
  inp.oninput = () => { flow.host = inp.value; go.disabled = !GHE_HOST.test(inp.value.trim()); why.textContent = ""; };
  const submit = (e) => {
    e.preventDefault();
    if (signing !== flow) return;
    const host = inp.value.trim().replace(/^https?:\/\//i, "").replace(/\/$/, "").toLowerCase();
    if (!GHE_HOST.test(host)) { why.textContent = t("Type your enterprise's <name>.ghe.com"); return; }
    startSignIn("copilot", true, host);
  };
  form.onsubmit = submit;
  go.onclick = submit;
  form.append(inp, go);
  tt.append(form, why);
  const close = el("button", "text", t("Cancel"));
  close.onclick = cancelSignIn;
  const back = el("button", "text", "github.com");
  back.title = t("Sign in on github.com instead");
  back.onclick = () => startSignIn("copilot", true);
  box.append(close, back);
  setTimeout(() => { if (inp.isConnected) inp.focus({ preventScroll: true }); });
  return box;
};

// a Claude account from the token `claude setup-token` prints (sfss5362 on
// X; claude_setup_token.go)
window.claudeTokenSheet = function () {
  const box = el("div", "signing claude-token");
  const tt = el("span", "tt");
  box.append(tt);
  const flow = signing;
  const label = t("Add a Claude account by setup-token");
  tt.append(el("span", "n", label),
    el("span", "s", t("Paste the token {cmd} printed. magpie gives it to Claude Code for this account's requests only; Claude Code itself stays signed in as it is.", { cmd: "claude setup-token" })),
    el("span", "s", t("A setup-token can't tell its email or allowance: the account is named by the token, and its allowance shows once a reply tells it.")));
  // the proxy its requests go through, picked here or before Use a
  // setup-token… (需求方 2026-11-13: one picked before was dropped, and
  // had to be set again in the provider's editor); none picked leaves the
  // proxies as they are
  const via = el("button", "link via token-via", t("Proxy: {proxy}", { proxy: flow.picked === undefined ? t("Global proxy") : proxyName(flow.picked) }));
  via.type = "button";
  via.setAttribute("aria-haspopup", "menu");
  via.onclick = (e) => {
    e.stopPropagation();
    openProtoMenu(via, signInProxyChoices(subOf("claude") || { agent: "claude", name: "Claude" }), flow.askProxy ? "\x01" : flow.picked || "", (v) => {
      if (signing !== flow) return;
      flow.askProxy = v === "\x01";
      if (!flow.askProxy) flow.picked = v;
      renderProviders();
    }, "Sign in through");
  };
  tt.append(el("span", "acts", via));
  let cp = null;
  if (flow.askProxy) {
    const ask = el("span", "callback-form proxy-ask");
    cp = input(flow.picked || "", "http://127.0.0.1:7890");
    cp.className = "token-proxy";
    cp.setAttribute("aria-label", t("Proxy"));
    const login = proxyLoginRow(cp, (v) => { flow.picked = v.trim() || undefined; });
    for (const f of [cp, ...login.querySelectorAll("input")]) f.onkeydown = (e) => e.stopPropagation();
    ask.append(cp, login);
    tt.append(ask);
  }
  const form = el("form", "callback-form");
  const inp = input(flow.token || "", "sk-ant-oat01-…", "password");
  inp.setAttribute("aria-label", label);
  const go = el("button", "text primary", t("Add"));
  go.type = "submit";
  go.disabled = !inp.value.trim() || !!flow.busy;
  const why = el("span", "s why", flow.error || "");
  inp.oninput = () => { flow.token = inp.value; go.disabled = !inp.value.trim(); why.textContent = ""; };
  const submit = async (e) => {
    e.preventDefault();
    if (signing !== flow || flow.busy || !inp.value.trim()) return;
    flow.busy = true;
    go.disabled = true;
    try {
      const r = await api("signin/claude-token", { token: inp.value, ...(flow.picked !== undefined ? { proxy: flow.picked } : {}) });
      if (signing !== flow) return;
      await signedIn({ agent: "claude", user: r.user, token: true });
      if (r.error) status(r.error, "err");
    } catch (err) {
      if (signing !== flow) return;
      flow.busy = false;
      flow.error = err.message;
      renderProviders();
    }
  };
  form.onsubmit = submit;
  go.onclick = submit;
  form.append(inp, go);
  tt.append(form, why);
  const close = el("button", "text", t("Cancel"));
  close.onclick = cancelSignIn;
  box.append(close);
  // the token box, or the proxy's address once Custom… is picked, unless
  // a box of the sheet has the focus already: a late focus took what was
  // typed in the proxy's login into the token (需求方 2026-11-13)
  setTimeout(() => {
    if (!inp.isConnected || box.contains(document.activeElement) && document.activeElement !== box) return;
    (cp && !cp.value ? cp : inp).focus({ preventScroll: true });
  });
  return box;
};

// An account being added, before anything is asked of its vendor
// (需求方 2026-11-04: 点击添加账号时会自动发起请求到 claude.ai，改为默认
// 不发起): its proxy picked first, then Start sign-in (startSignIn).
window.readySheet = function (sub) {
  const box = el("div", "signing ready");
  const tt = el("span", "tt");
  tt.append(el("span", "n", t("Add a {name} account", { name: sub.name })),
    el("span", "s", t("Nothing is sent to {name} until you start: magpie then starts the sign-in and opens its page.", { name: sub.name })));
  const o = { proxy: signing.picked };
  // the other ways in, offered before anything is sent (review M1): a
  // Claude setup-token, Copilot's GHE.com, ChatGPT's device code
  const acts = el("span", "acts", signInProxyLink(sub));
  if (sub.agent === "claude" && !sub.plugin) {
    const tok = el("button", "link ready-token", t("Use a setup-token instead…"));
    tok.onclick = () => startClaudeToken();
    acts.append(tok);
  }
  const ghe = copilotHostSwitch(sub);
  if (ghe) acts.append(ghe);
  if (sub.agent === "codex" && !sub.plugin) acts.append(codexWay(sub, false, o));
  tt.append(acts);
  if (signing.askProxy) tt.append(signInProxyForm(sub));
  box.append(tt);
  const go = el("button", "text primary sign-start", t("Start sign-in"));
  go.onclick = () => startSignIn(sub.agent, false, undefined, o);
  const close = el("button", "text", t("Cancel"));
  close.onclick = cancelSignIn;
  if (sub.importable) {
    const imp = el("button", "text", importSay(sub.agent).instead || t("Import instead…"));
    imp.title = importSay(sub.agent).from;
    imp.onclick = () => startImport(sub.agent);
    box.append(close, imp, go);
  } else box.append(close, go);
  return box;
};
