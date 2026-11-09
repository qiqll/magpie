// An account the user marked banned or disabled (provider/banned.go), in
// the provider editor. app.js imports this when an editor draws an account
// (banJS), so it weighs nothing on the core.

// banDress: box as account l of a's marks: Mark banned or Lift ban, and
// Disable or Enable
window.banDress = function (box, a, l) {
  box.replaceChildren();
  for (const [on, set, unset, cls, word, unword, tip, said, unsaid] of [
    [l.banned, "ban", "unban", "acc-ban", "Mark banned", "Lift ban", "Its vendor banned it: the gateway never sends it a request, and its usage history stays", "{user} is marked banned", "{user} is used again"],
    [l.disabled, "disable", "enable", "acc-disable", "Disable", "Enable", "Turn it off: the gateway never sends it a request, and its usage history stays", "{user} is disabled", "{user} is used again"],
  ]) {
    const b = el("button", "text " + cls, t(on ? unword : word));
    b.type = "button";
    b.title = t(on ? "Use it again: the gateway routes to it and its allowance is read" : tip);
    b.onclick = () => { b.classList.add("busy"); accountAction("login/" + (on ? unset : set), { agent: a.agent, user: l.user }, t(on ? unsaid : said, { user: l.user })); };
    box.append(b);
  }
};

// bannedFill: box as the line under an editor's accounts for those marked
// banned or disabled, opened to list each with its marks' badges, the
// buttons that lift them, and Remove (kept open per provider while the
// window is)
const bannedOpen = new Set();
window.bannedFill = function (box, a, p, off) {
  const key = p?.id || a.agent, n = off.length;
  const banned = off.every((l) => l.banned), disabled = off.every((l) => l.disabled && !l.banned);
  const resting = off.every((l) => l.authFailed && !l.banned && !l.disabled);
  const words = resting ? ["1 account to look at", "{n} accounts to look at"] : banned ? ["1 banned account", "{n} banned accounts"] : disabled ? ["1 disabled account", "{n} disabled accounts"] : ["1 banned or disabled account", "{n} banned or disabled accounts"];
  box.replaceChildren();
  const head = el("button", "link acc-banned-head", t(n === 1 ? words[0] : words[1], { n }) + " · " + t(bannedOpen.has(key) ? "Hide" : "Show"));
  head.type = "button";
  head.setAttribute("aria-expanded", bannedOpen.has(key));
  head.onclick = () => { bannedOpen.has(key) ? bannedOpen.delete(key) : bannedOpen.add(key); bannedFill(box, a, p, off); };
  box.append(head);
  if (!bannedOpen.has(key)) return;
  for (const l of off) {
    const row = el("div", "acc-banned-row");
    row.dataset.accountId = l.user;
    const forget = el("button", "text quiet", t("Remove"));
    forget.title = t("magpie forgets this account's sign-in; the account itself is untouched");
    forget.onclick = () => accountAction("login/forget", { agent: a.agent, user: l.user }, t("{user} removed", { user: l.user }));
    const marks = el("span", "acc-marks");
    banDress(marks, a, l);
    // only the buttons that lift a mark it has
    // and Mark banned for one resting after authentication_failed
    for (const b of [...marks.children]) if (b.classList.contains("acc-ban") ? !l.banned && !l.authFailed : !l.disabled) b.remove();
    row.append(accountNameButton(p, l.user));
    if (l.banned) row.append(el("span", "using banned", t("Banned")));
    if (l.disabled) row.append(el("span", "using disabled", t("Disabled")));
    if (l.authFailed && !l.banned) {
      // Anthropic answered it authentication_failed: it may be banned
      const why = el("span", "using banned", t("Sign-in refused · may be banned"));
      why.title = t("Anthropic answered a request of it authentication_failed. Nothing is sent it until you sign it in again, mark it banned or use it again");
      row.append(why);
      const lift = el("button", "text acc-lift", t("Use again"));
      lift.type = "button";
      lift.onclick = () => { lift.classList.add("busy"); accountAction("login/lift", { agent: a.agent, user: l.user }); };
      marks.append(lift);
    }
    row.append(el("span", "grow"), marks, forget);
    box.append(row);
  }
};
