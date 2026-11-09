// An account the user marked banned (provider/banned.go), in the provider
// editor. app.js imports this when an editor draws an account (banJS), so
// it weighs nothing on the core.

// banDress: b as account l of a's Mark banned, or Lift ban once it is
window.banDress = function (b, a, l) {
  b.type = "button";
  b.textContent = t(l.banned ? "Lift ban" : "Mark banned");
  b.title = t(l.banned ? "Use it again: the gateway routes to it and its allowance is read" : "Its vendor banned it: the gateway never sends it a request, and its usage history stays");
  b.onclick = () => { b.classList.add("busy"); accountAction("login/" + (l.banned ? "unban" : "ban"), { agent: a.agent, user: l.user }, t(l.banned ? "{user} is used again" : "{user} is marked banned", { user: l.user })); };
};

// bannedFill: box as the line under an editor's accounts for those marked
// banned, opened to list each with Lift ban and Remove (kept open per
// provider while the window is)
const bannedOpen = new Set();
window.bannedFill = function (box, a, p, banned) {
  const key = p?.id || a.agent;
  box.replaceChildren();
  const head = el("button", "link acc-banned-head", t(banned.length === 1 ? "1 banned account" : "{n} banned accounts", { n: banned.length }) + " · " + t(bannedOpen.has(key) ? "Hide" : "Show"));
  head.type = "button";
  head.setAttribute("aria-expanded", bannedOpen.has(key));
  head.onclick = () => { bannedOpen.has(key) ? bannedOpen.delete(key) : bannedOpen.add(key); bannedFill(box, a, p, banned); };
  box.append(head);
  if (!bannedOpen.has(key)) return;
  for (const l of banned) {
    const row = el("div", "acc-banned-row");
    row.dataset.accountId = l.user;
    const forget = el("button", "text quiet", t("Remove"));
    forget.title = t("magpie forgets this account's sign-in; the account itself is untouched");
    forget.onclick = () => accountAction("login/forget", { agent: a.agent, user: l.user }, t("{user} removed", { user: l.user }));
    const ban = el("button", "text acc-ban");
    banDress(ban, a, l);
    row.append(accountNameButton(p, l.user), el("span", "using banned", t("Banned")), el("span", "grow"), ban, forget);
    box.append(row);
  }
};
