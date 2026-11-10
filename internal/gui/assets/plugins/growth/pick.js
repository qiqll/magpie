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
