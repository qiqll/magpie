// A subscription's growth policy in its provider editor (T-131): the
// policy its accounts are held by, picked from the ones Settings › Growth
// policy lists, or the default followed. The pick is saved at once
// (provider/growthpolicy) and read by the gateway, switching and each
// account's row alike. One it picked that is gone since (deleted, or
// another magpie's) holds by the default, and the field says so. Run
// after settings.js, whose growthName and growthSummary it shares.

function growthPickField(p) {
  const box = el("div", "growth-pick");
  const draw = () => {
    const all = prefs?.growthPolicyList || [];
    const def = all.find((x) => x.id === prefs?.growthInEffect) || all.find((x) => x.id === "standard");
    const gone = !!p.growthPolicy && !!prefs && !all.some((x) => x.id === p.growthPolicy);
    const opts = [{ v: "", pill: t("Follow the default ({name})", { name: def ? growthName(def) : t("Standard") }), note: def ? growthSummary(def) : "" }]
      .concat(all.map((x) => ({ v: x.id, pill: growthName(x), note: growthSummary(x) })));
    for (const o of opts) [o.name, o.literalName] = [o.pill, true];
    const head = "Growth policy this subscription's accounts are held by";
    const save = (v) => accountAction("provider/growthpolicy", { id: p.id, growthPolicy: v }, t("Saved"));
    const pill = pickPill(opts, gone ? "" : p.growthPolicy || "", "", head, head, save);
    // a pick that is gone shows as the default it holds by, and Follow the
    // default saves too, so the id gone is cleared (review M1)
    if (gone) pill.onclick = (e) => {
      e.stopPropagation();
      if (pill.classList.contains("open")) return closeProtoMenu();
      openProtoMenu(pill, opts, "", save, head);
    };
    pill.classList.add("growth-pill");
    box.replaceChildren(pill);
    if (gone) box.append(el("div", "hint warn growth-gone", t("The policy it picked ({id}) is gone: its accounts are held by the default until you pick another", { id: p.growthPolicy })));
  };
  if (prefs) draw();
  else loadSettings().then(() => box.isConnected && draw(), () => {});
  return field(t("Growth policy"), box, t("How its new accounts are held while they ramp up: a share of each window that rises with their days. Policies are made in Settings › Growth policy."));
}

// An account's own growth policy (T-133), picked on its row in Models ›
// Accounts: before its subscription's, which is before the default
// (settings.GrowthFor). Follow the subscription clears it. One it picked
// that is gone holds by its subscription's. No choices until the policies
// are read (growthPrefs), so a row asks for nothing it can't save.
function growthAccountOpts(p) {
  const s = growthPrefs(), all = s?.growthPolicyList || [];
  if (!all.length) return [];
  const def = all.find((x) => x.id === s.growthInEffect) || all.find((x) => x.id === "standard");
  const sub = all.find((x) => x.id === p.growthPolicy) || def;
  const opts = [{ v: "", name: t("Follow the subscription ({name})", { name: sub ? growthName(sub) : t("Standard") }), note: sub ? growthSummary(sub) : "" }]
    .concat(all.map((x) => ({ v: x.id, name: growthName(x), note: growthSummary(x) })));
  for (const o of opts) o.literalName = true;
  return opts;
}

// growthAccountPicked: the policy the account user of p picked itself —
// its id ("" for none), the policy (null when gone or none), and whether
// it is gone
function growthAccountPicked(p, user) {
  const id = p.accountGrowthPolicy?.[String(user).trim().toLowerCase()] || "";
  const list = growthPrefs()?.growthPolicyList;
  const policy = id ? (list || []).find((x) => x.id === id) || null : null;
  return { id, policy, gone: !!id && !!list && !policy };
}

// growthPrefs: the policies and the default, from Settings when it was
// read (prefs, kept current by its page), else from growthListLoad's own
// read, which Models › Accounts asks for without drawing Settings
let growthListRead = null;
const growthPrefs = () => (prefs?.growthPolicyList ? prefs : growthListRead);
function growthListLoad(again) {
  if (prefs?.growthPolicyList || growthListRead && !again) return Promise.resolve();
  return api("settings").then((s) => {
    if (Array.isArray(s?.growthPolicyList)) growthListRead = { growthPolicyList: s.growthPolicyList, growthInEffect: s.growthInEffect };
  }, () => {});
}

const growthAccountSave = (p, user, v) => accountAction("provider/accountgrowthpolicy", { id: p.id, account: user, growthPolicy: v }, t("Saved"));
