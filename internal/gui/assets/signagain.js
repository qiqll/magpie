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

// signInName: agent's name on the status line, its provider's in the list
// as the line over it says
const signInName = (agent) => providers.providers.find((p) => p.account?.agent === agent)?.name || subOf(agent)?.name || agent;

// sayFailed: a failed sign-in whose box is out of sight said on the status
// line (saySignInFailed)
window.sayFailed = function () {
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
