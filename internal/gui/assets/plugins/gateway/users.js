// The Gateway page's User bindings (plugins/gateway): each provider's
// accounts with how many of the caller's users the gateway keeps on them,
// and the users, as /api/gateway/users reads user-accounts.json. Users a
// service names in X-Magpie-User (or the header MAGPIE_USER_HEADER names)
// each stay on one account of each provider. Read-only: a user is moved or
// forgotten in the file, which the gateway reads again when it changes.
// The section stays hidden while nobody is bound.
//
// Classic script, loaded before client.js and app.js: nothing here runs
// as it loads.

let userBinds = null;    // what /api/gateway/users said last, or {error}
let userAccounts = [];   // the accounts, from the last read without a search (one with leaves them out)
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
  if (out?.accounts) userAccounts = out.accounts;
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
  return a.providerName;
}

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
  if (out.error) {
    list.append(el("div", "none", out.error));
    return;
  }

  for (const a of userAccounts) {
    const row = el("div", "row model user-account" + (a.gone ? " gone" : ""));
    const who = el("div", "who");
    who.append(el("div", "name", a.account), el("div", "sub wraps", userAccountSays(a)));
    row.append(icon(a.icon || "generic"), who, el("span", "note", t(a.users === 1 ? "{n} user" : "{n} users", { n: a.users })));
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
    const sub = el("div", "sub wraps", [u.account, u.providerName, userKeyLabel(u)].join(" · "));
    // a file edited by hand may leave the time out
    if (u.at && !u.at.startsWith("0001-")) {
      const when = el("span", "", " · " + t("bound {when}", { when: ago(u.at) }));
      when.title = stamp(u.at);
      sub.append(when);
    }
    const name = el("div", "name", u.user);
    name.title = u.user; // a long id is cut short in its row
    who.append(name, sub);
    row.append(icon(u.icon || "generic"), who, copyBtn(u.user, t("User id")));
    list.append(row);
  }
  if ((out.matched || 0) > users.length) {
    list.append(el("div", "none", t("Showing {n} of {total}; type to narrow them", { n: users.length, total: out.matched })));
  }
}
