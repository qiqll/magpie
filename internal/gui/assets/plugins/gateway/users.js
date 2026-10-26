// The Gateway page's User bindings (plugins/gateway): each provider's
// accounts with how many of the caller's users the gateway keeps on them
// and whether each is out, and the users with their first account and
// spares, as /api/gateway/users reads user-accounts.json. Users a service
// names in X-Magpie-User (or the header MAGPIE_USER_HEADER names) are
// bound only while the gateway binds by user (--bind=user); bound by
// session, the bindings are shown as they are and nothing is bound by
// them. Read-only: a user is moved or forgotten in the file, which the
// gateway reads again when it changes. The section stays hidden while
// nobody is bound.
//
// Classic script, loaded before client.js and app.js: nothing here runs
// as it loads.

let userBinds = null;    // what /api/gateway/users said last, or {error}
let userAccounts = [];   // the accounts, from the last read without a search (one with leaves them out)
let userBindMode = null; // {mode, tiers}, from the same read
let userQuery = "";      // the words typed
let userShownFor = "";   // the words userBinds' users were narrowed by
let userBindsRead = 0;   // a newer read wins over an older one still out
let userFindTimer = 0;

async function loadUserBinds() {
  clearTimeout(userFindTimer);
  const read = ++userBindsRead;
  const q = userQuery.trim();
  let out;
  try { out = await api("gateway/users" + (q ? "?q=" + encodeURIComponent(q) : "")); } catch (e) {
    out = { error: e.why ? t("user-accounts.json can't be read: {why}", { why: e.why }) : e.message };
  }
  if (read !== userBindsRead) return;
  userBinds = out || { total: 0 };
  userShownFor = q;
  if (out?.accounts) {
    userAccounts = out.accounts;
    userBindMode = { mode: out.mode || "", tiers: out.tiers || null };
  }
  renderUserBinds();
}

// the gateway key a user came with, as the page names it
function userKeyLabel(u) {
  if (!u.key) return t("without a gateway key");
  if (u.keyName) return u.keyName;
  return u.keyGone ? t("a removed gateway key") : t("gateway key {id}", { id: u.key.slice(0, 8) });
}

// what an account's row says under its name
function userAccountSays(a) {
  if (a.providerGone) return `${a.providerName} · ${t("no longer in magpie: its users aren't served")}`;
  if (a.gone) return `${a.providerName} · ${t("no longer on its provider: its users are bound afresh on their next request")}`;
  return `${a.providerName} · ${userSeatState(a)}`;
}

// why an account rests, as the gateway names it
const USER_OUT = { quota: "quota used up", credit: "out of credit", rate: "rate limited", cap: "at its usage cap", auth: "sign-in required", verify: "needs verification", refused: "refused (safety filter)", proxy: "proxy not reachable", restricted: "restricted by the vendor" };

// whether an account can answer now: out, and until when, or unknown when
// the magpie serving the gateway didn't say, never "can answer" then. a
// is undefined for one the page hasn't the accounts of.
function userSeatState(a) {
  if (a?.gone) return t("no longer on its provider");
  // a time passed while the page kept the accounts (a search): not
  // known again until they are read
  if (!a?.outKnown || (a.outUntil && new Date(a.outUntil) <= Date.now())) return t("status unknown");
  if (!a.out) return t("can answer");
  let why = t(USER_OUT[a.out] || "failed");
  if (a.outModel) why = t("{why} for {model}", { why, model: a.outModel });
  return a.outUntil ? t("out · {why} · back {in}", { why, in: untilText(new Date(a.outUntil)) }) : t("out · {why}", { why });
}

// the account of a provider a user's seat names, as the accounts list has it
const userAccountOf = (p, seat) => userAccounts.find((a) => a.provider === p && a.seat === seat);

// one of a user's accounts: its name, first or spare, and its state
function userSeatChip(u, account, seat, first) {
  const a = userAccountOf(u.provider, seat);
  const chip = el("span", "user-seat " + (first ? "first" : "spare") + (!a?.outKnown || a.out || a.gone ? " out" : ""));
  chip.append(el("span", "name", account), el("span", "", ` (${t(first ? "first" : "spare")} · ${userSeatState(a)})`));
  return chip;
}

// how the gateway binds, said over the lists: bound by user, each tier
// of user-tiers.json with the most accounts it gives, the default marked;
// bound by session, nothing is bound by them, and they are kept as they
// are
function userModeSays(m) {
  if (m?.mode === "session") return t("Binding by user is off (start option --bind=user). The bindings below are kept as they are, and used again once it is on.");
  if (m?.mode !== "user") return "";
  const tiers = m.tiers?.tiers || [];
  if (!tiers.length) return t("Bound by user; the tiers in user-tiers.json are unknown");
  const each = tiers.map((x) => t(x.name === m.tiers.default ? "{tier} up to {n} (default)" : "{tier} up to {n}", { tier: x.name, n: x.max_accounts }));
  return t("Bound by user: accounts of each provider each user may have, by its tier: {tiers}", { tiers: each.join(" · ") });
}

// a user's tier: the one its last request named, the default for one
// bound before tiers; "" while the tiers aren't known
const userTierOf = (u) => u.tier || userBindMode?.tiers?.default || "";

function renderUserBinds() {
  const block = $("#userBindsBlock");
  if (!block) return;
  const out = userBinds;
  const accounts = $("#userBindAccounts"), list = $("#userBinds");
  accounts.replaceChildren();
  list.replaceChildren();
  if (!out || (!out.error && !out.total)) {
    block.hidden = true;
    return;
  }
  block.hidden = false;
  $("#userBindsCount").textContent = out.error ? "" : String(out.total);
  accounts.hidden = !!out.error || !userAccounts.length;
  const mode = $("#userBindsMode");
  mode.textContent = out.error ? "" : userModeSays(userBindMode);
  mode.hidden = !mode.textContent;
  if (out.error) {
    list.append(el("div", "none", out.error));
    return;
  }

  for (const a of userAccounts) {
    const row = el("div", "row model user-account" + (a.gone ? " gone" : !a.outKnown || a.out ? " out" : ""));
    const who = el("div", "who");
    who.append(el("div", "name", a.account), el("div", "sub wraps", userAccountSays(a)));
    const n = t(a.users === 1 ? "{n} user" : "{n} users", { n: a.users });
    row.append(icon(a.icon || "generic"), who, el("span", "note", a.spares ? t("{users}, {n} as a spare", { users: n, n: a.spares }) : n));
    accounts.append(row);
  }

  // the search sits in the section head; typing asks again for the users
  // and redraws, the field kept, so it keeps its focus. A read still out
  // for words no longer typed is dropped as soon as they change.
  let q = $("#findUser");
  if (!q) {
    q = input(userQuery, t("Find a user…"));
    q.id = "findUser";
    q.className = "find";
    q.oninput = () => {
      userQuery = q.value;
      userBindsRead++;
      clearTimeout(userFindTimer);
      userFindTimer = setTimeout(loadUserBinds, 200);
    };
    q.onkeydown = (e) => {
      e.stopPropagation();
      if (e.key === "Escape" && q.value) { q.value = userQuery = ""; loadUserBinds(); }
    };
    $("#userBindsHead").append(q);
  }
  q.title = t("Words match a user, its account, its provider or its gateway key");
  q.hidden = out.total < 5 && !userQuery;

  const users = out.users || [];
  if (!users.length) {
    list.append(el("div", "none", t("No users match “{q}”", { q: userShownFor })));
    return;
  }
  for (const u of users) {
    const row = el("div", "row model user-bind");
    const who = el("div", "who");
    // its first account, then its spares in order, each with its state
    const seats = el("div", "sub wraps user-seats");
    seats.append(userSeatChip(u, u.account, u.seat, true));
    for (const sp of u.spares || []) seats.append(userSeatChip(u, sp.account, sp.seat, false));
    const tier = userTierOf(u);
    const sub = el("div", "sub wraps", [u.providerName, userKeyLabel(u), ...(tier ? [t("tier {tier}", { tier })] : [])].join(" · "));
    // a file edited by hand may leave the time out
    if (u.at && !u.at.startsWith("0001-")) {
      const when = el("span", "", " · " + t("bound {when}", { when: ago(u.at) }));
      when.title = stamp(u.at);
      sub.append(when);
    }
    const name = el("div", "name", u.user);
    name.title = u.user; // a long id is cut short in its row
    who.append(name, seats, sub);
    row.append(icon(u.icon || "generic"), who, copyBtn(u.user, t("User id")));
    list.append(row);
  }
  if ((out.matched || 0) > users.length) {
    list.append(el("div", "none", t("Showing {n} of {total}; type to narrow them", { n: users.length, total: out.matched })));
  }
}
