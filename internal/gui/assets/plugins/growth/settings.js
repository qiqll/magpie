// Settings › Growth policy (T-078): how far a subscription account added to
// magpie lately is used, by the days since its first use (settings.Growth,
// provider/growth.go). A row is a stage: the day it starts on, the window
// it holds and the share; rows that start on one day are one stage. A
// row's share lasts until the next row of the same window starts. The rows
// are edited as a draft and saved whole; Use default puts magpie's own
// back. Run after app.js, whose helpers it shares (el, input, segs, api,
// writingPrefs, status), and before start.js draws Settings.

// growthDraft is the stages as typed, null while they are the saved ones.
let growthDraft = null, growthErr = "";
const GROWTH_WINDOWS = [["5h", "5 hours"], ["7d", "7 days"]];

function renderGrowth(s) {
  const box = $("#growthList");
  const saved = (s.growthStages || []).map((g) => ({ from: String(g.from), window: g.window, cap: String(g.cap) }));
  const d = growthDraft || saved;
  const redraw = () => renderGrowth(prefs);
  // an edit lights Save and Use default as it is typed, before any redraw
  const edit = () => {
    growthDraft = d; growthErr = "";
    const save = box.querySelector(".growth-save"), back = box.querySelector(".growth-default");
    if (save) save.disabled = false;
    if (back) back.hidden = false;
  };
  box.replaceChildren();

  const head = el("div", "row pref growth-head");
  const who = el("div", "who");
  who.append(el("div", "name", t("New accounts are used gently")),
    el("div", "sub" + (growthErr ? " err" : ""), growthErr || t("A subscription account added to magpie is held at the share of a window its stage gives, counted in days from its first use (the first request its vendor served through magpie; one not used yet is on day 0), and routing goes on to your other accounts until that window renews. Each window keeps the share of the latest stage that lists it: a stage that leaves a window out leaves it as it was. An account added before magpie kept the day is never held, and a cap you set on an account or a window comes first.")));
  head.append(who);
  box.append(head);

  // a stage's days, as the starts typed say them: its window's share lasts
  // until the next stage that holds the same window (T-128)
  const starts = [...new Set(d.map((g) => Number(g.from)).filter((n) => Number.isInteger(n) && n >= 0))].sort((a, b) => a - b);
  const startsOf = (win) => d.filter((g) => g.window === win).map((g) => Number(g.from)).filter((n) => Number.isInteger(n) && n >= 0).sort((a, b) => a - b);
  const days = (from, win) => {
    const next = startsOf(win).find((n) => n > from);
    return next === undefined ? t("From day {from} on", { from }) : t("Days {from} to {to}", { from, to: next });
  };
  d.forEach((g, i) => {
    const r = el("div", "row pref growth-stage");
    const w = el("div", "who");
    w.append(el("div", "name", Number.isInteger(Number(g.from)) && g.from !== "" ? days(Number(g.from), g.window) : t("A stage with no start day")));
    const val = el("div", "val");
    const from = input(g.from, "0");
    from.className = "words growth-from";
    from.style.cssText = "width:56px;flex:none";
    from.inputMode = "numeric";
    from.setAttribute("aria-label", t("Starts on day"));
    from.oninput = () => { g.from = from.value.trim(); edit(); };
    from.onchange = redraw;
    const win = segs(GROWTH_WINDOWS.map(([id, name]) => [id, t(name)]), g.window, (v) => { g.window = v; edit(); redraw(); });
    win.setAttribute("aria-label", t("Window held"));
    const cap = input(g.cap, "80");
    cap.className = "words growth-cap";
    cap.style.cssText = "width:48px;flex:none";
    cap.inputMode = "numeric";
    cap.setAttribute("aria-label", t("Share of the window, in percent"));
    cap.oninput = () => { g.cap = cap.value.trim(); edit(); };
    const del = el("button", "text growth-del", "×");
    del.type = "button";
    del.title = t("Remove this stage");
    del.setAttribute("aria-label", del.title);
    del.onclick = () => { d.splice(i, 1); edit(); redraw(); };
    val.append(from, win, cap, el("span", "growth-pct", "%"), del);
    r.append(w, val);
    box.append(r);
  });

  const foot = el("div", "row pref growth-foot");
  const fw = el("div", "who");
  fw.append(el("div", "sub", s.growth == null && !growthDraft ? t("magpie's default stages") : t("Your own stages")));
  const fv = el("div", "val");
  const add = el("button", "text growth-add", t("Add a stage"));
  add.type = "button";
  add.onclick = () => {
    const last = starts.at(-1) ?? -1;
    d.push({ from: String(last + 1), window: "5h", cap: "90" });
    edit();
    redraw();
    [...box.querySelectorAll(".growth-stage")].at(-1)?.querySelector(".growth-from")?.focus(NO_SCROLL);
  };
  const put = (stages) => writingPrefs(api("settings/growth", { stages }))
    .then((ns) => { growthDraft = null; growthErr = ""; prefs = state.settings = ns; renderSettings(); status(t("Saved"), "ok", 1500); })
    .catch((e) => { growthErr = t(e.message); redraw(); });
  const save = el("button", "text growth-save", t("Save"));
  save.type = "button";
  save.disabled = !growthDraft;
  save.onclick = () => {
    const out = [];
    for (const g of d) {
      const from = Number(g.from), c = Number(g.cap);
      if (!/^\d+$/.test(g.from) || from > 365 || !/^\d+$/.test(g.cap) || c < 1 || c > 99) {
        growthErr = t("Each stage starts on a day from 0 to 365 and holds its window at 1% to 99%");
        return redraw();
      }
      out.push({ from, window: g.window, cap: c });
    }
    put(out);
  };
  const back = el("button", "text growth-default", t("Use default"));
  back.type = "button";
  back.hidden = s.growth == null && !growthDraft;
  back.onclick = () => put(null);
  fv.append(add, save, back);
  foot.append(fw, fv);
  box.append(foot);
}
