// Accounts to sign in again. app.js imports this only once one is asked
// for (signAgainJS), so it weighs nothing on the core.

// lapsedLine: the line over Providers for account l of p, in use and to be
// signed in again (renderLapsed), with its Sign in again
window.lapsedLine = function (p, l) {
  const card = el("div", "deprecation lapsed");
  card.setAttribute("role", "note");
  card.dataset.id = p.id;
  card.dataset.user = l.user;
  const body = el("div", "dep-body");
  const head = el("div", "dep-head", t("{name}: {user} has to be signed in again", { name: p.name, user: l.user }));
  head.title = t(l.lapsed);
  body.append(head, el("p", "dep-why", t("magpie leaves it out of requests until it is signed in again.")));
  const acts = el("div", "dep-acts");
  const b = el("button", "text action", t("Sign in again"));
  b.onclick = () => {
    // the editor opens over the list, the sign-in under way in it
    editing = p.id; draft = null; adding = false;
    startSignIn(p.account.agent, false, signInAgainSite(p.account.agent, l.user), { user: l.user });
  };
  acts.append(b);
  card.append(el("span", "dep-dot"), body, acts);
  return card;
};

// lapsedDraw: box over the list, each account in use to sign in again
// (lapsedInUse), which the gateway leaves out till then, with its Sign in
// again, which opens the editor with the sign-in under way (renderLapsed)
window.lapsedDraw = function (box) {
  if (!box) return;
  const lines = [];
  for (const p of providers.providers) if (!p.off && p.account) for (const l of p.account.logins || []) if (lapsedInUse(p, l)) lines.push(lapsedLine(p, l));
  box.replaceChildren(...lines);
  box.hidden = !lines.length;
};

// signInProxySaid: the proxy a failed sign-in's CLI was given and whose
// it was (proxyUsed, T-103), "" when it isn't known
const signInProxyFrom = {
  picked: "picked for this sign-in", account: "this account's own", subscription: "this subscription's own",
  settings: "magpie's global proxy", environment: "magpie's own environment", system: "the system's proxy",
  direct: "magpie's global proxy is set to none", none: "no proxy is set anywhere",
  invalid: "the proxy set can't be read, so magpie's own environment was kept",
};
window.signInProxySaid = function (u) {
  if (!u?.from) return "";
  const from = t(signInProxyFrom[u.from] || u.from);
  return u.proxy ? t("Proxy given to it: {proxy} ({from})", { proxy: u.proxy, from }) : t("No proxy given to it ({from})", { from });
};

// signInName: agent's name on the status line, its provider's in the list
// as the line over it says
const signInName = (agent) => providers.providers.find((p) => p.account?.agent === agent)?.name || subOf(agent)?.name || agent;

// sayFailed: a failed sign-in whose box is out of sight said on the status
// line (saySignInFailed); one drawn before this loaded is drawn again with
// its proxy (signInProxySaid)
window.sayFailed = function () {
  if (signInProxySaid(signing?.proxyUsed) && !$(".signing.failed .proxy-used")) renderProviders();
  if (signing?.state !== "failed" || $(".signing.failed")?.getClientRects().length) return;
  const name = signInName(signing.agent);
  status(signing.error ? t("Sign-in to {name} didn't finish: {error}", { name, error: signing.error }) : t("Sign-in to {name} didn't finish", { name }), "err");
};

// acctsBack: an open editor redrawn on return if its accounts changed
window.acctsBack = async function () {
  const ls = (r) => JSON.stringify(r?.providers?.find((p) => p.id === editing)?.account?.logins?.map((l) => [l.user, l.lapsed, l.active, l.paused]));
  const next = await api("providers").catch(() => null);
  if (next && typeof editing == "string" && ls(next) !== ls(providers) && !$(".editor :is(input,textarea,select):focus")) { providers = next; renderProviders(); }
};
