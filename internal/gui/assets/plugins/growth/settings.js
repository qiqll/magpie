// Settings › Growth policy (T-078, T-130): how far a subscription account
// added to magpie lately is used, by the days since its first use. The
// policies are listed on the left: magpie's own (read only, each can be
// copied) and the user's, one of them the default every account is held
// by. The one picked is edited on the right: a ramp (start %, days to
// 100%) or stages (a row each: the day it starts on, the window it holds
// and the share). Its chart is each day's shares as the gateway holds an
// account to them, asked of POST /api/growth/preview (the same function
// the gateway reads), never worked out here, with each account marked on
// its day. Every change is a draft, saved whole through
// settings/growth-policies. Run after app.js, whose helpers it shares
// (el, input, segs, api, writingPrefs, status), and before start.js draws
// Settings.

// growthDraft is the user's policies and the default as edited, null
// while they are the saved ones; growthSel the policy shown.
let growthDraft = null, growthErr = "", growthSel = null;
// growthChart is the last chart asked for: its policy (key), what came
// back, and the box it is drawn in
const growthChart = { key: "", want: "", data: null, err: "", box: null, timer: 0, ro: null, day: -1 };
const GROWTH_WINDOWS = [["5h", "5 hours"], ["7d", "7 days"]];
const GROWTH_BUILTIN = { standard: "Standard", gentle: "Gentle", cautious: "Cautious", off: "Off" };
const GROWTH_SVG = "http://www.w3.org/2000/svg";
// what the page says under its name, in each language through t()
const GROWTH_SAYS = "A subscription account added to magpie is held to a share of its windows that grows with its days, counted from its first use (the first request its vendor served through magpie; one not used yet is on day 0), and routing goes on to your other accounts until the window renews. The default policy holds every subscription that picked none; a subscription can pick another in its editor. An account added before magpie kept the day is never held, and a cap you set on an account or a window comes first.";

const growthName = (p) => (p.builtin ? t(GROWTH_BUILTIN[p.id] || p.name) : p.name);
// a policy of the user's as the inputs hold it: numbers as typed
const growthEditable = (p) => ({
  id: p.id, name: p.name, kind: p.kind, start: String(p.start ?? ""), days: String(p.days ?? ""),
  stages: (p.stages || []).map((g) => ({ from: String(g.from), window: g.window, cap: String(g.cap) })),
});

function growthSaved(s) {
  const all = s.growthPolicyList || [];
  return { own: all.filter((p) => !p.builtin).map(growthEditable), def: s.growthInEffect || "standard" };
}

// growthOut is a policy as the API takes it, or the reason it can't be,
// in the page's language
function growthOut(p) {
  if (p.builtin) return [{ id: p.id, name: p.name, kind: p.kind, start: p.start, days: p.days, builtin: true }];
  const name = p.name.trim();
  if (!name) return [null, t("A policy needs a name")];
  if ([...name].length > 64) return [null, t("A policy's name is up to 64 characters")];
  if (p.kind === "ramp") {
    if (!/^\d+$/.test(p.start) || +p.start < 1 || +p.start > 99 || !/^\d+$/.test(p.days) || +p.days < 1 || +p.days > 365) {
      return [null, t("A ramp starts at 1% to 99% and reaches 100% in 1 to 365 days")];
    }
    return [{ id: p.id, name, kind: "ramp", start: +p.start, days: +p.days }];
  }
  const stages = [], seen = new Set();
  for (const g of p.stages) {
    if (!/^\d+$/.test(g.from) || +g.from > 365 || !/^\d+$/.test(g.cap) || +g.cap < 1 || +g.cap > 99) {
      return [null, t("Each stage starts on a day from 0 to 365 and holds its window at 1% to 99%")];
    }
    if (seen.has(+g.from + g.window)) return [null, t("Two stages hold the same window from one day")];
    seen.add(+g.from + g.window);
    stages.push({ from: +g.from, window: g.window, cap: +g.cap });
  }
  return [{ id: p.id, name, kind: "stages", stages }];
}

function growthSummary(p) {
  if (p.kind === "ramp") return t("{start}% to 100% in {days} days", { start: p.start, days: p.days });
  if (p.kind === "stages") return p.stages.length === 1 ? t("1 stage") : t("{n} stages", { n: p.stages.length });
  return t("Holds nothing");
}

// a name no policy has: name, or name 2, name 3…
function growthFreeName(all, name) {
  const taken = new Set(all.map((p) => growthName(p).trim().toLowerCase()));
  for (const b of Object.values(GROWTH_BUILTIN)) taken.add(b.toLowerCase());
  const cut = (s, room) => [...s].slice(0, 64 - room).join("");
  let out = cut(name, 0);
  for (let n = 2; taken.has(out.toLowerCase()); n++) out = `${cut(name, String(n).length + 1)} ${n}`;
  return out;
}

const growthFreeID = (all) => {
  let id = "p-" + Date.now().toString(36);
  for (let n = 2; all.some((p) => p.id === id); n++) id = `p-${Date.now().toString(36)}-${n}`;
  return id;
};

function renderGrowth(s) {
  const box = $("#growthList");
  const d = growthDraft || growthSaved(s);
  const builtins = (s.growthPolicyList || []).filter((p) => p.builtin);
  const all = [...builtins, ...d.own];
  const cur = all.find((p) => p.id === growthSel) || all.find((p) => p.id === d.def) || all[0];
  const redraw = () => renderGrowth(prefs);
  // an edit lights Save and Discard as it is typed, before any redraw
  const edit = () => {
    growthDraft = d;
    if (growthErr) {
      growthErr = "";
      const sub = box.querySelector(".growth-head .sub");
      sub.classList.remove("err");
      sub.textContent = t(GROWTH_SAYS);
    }
    for (const b of box.querySelectorAll(".growth-save, .growth-discard")) b.disabled = false;
    growthAsk(cur);
  };
  box.replaceChildren();

  const head = el("div", "row pref growth-head");
  const who = el("div", "who");
  who.append(el("div", "name", t("New accounts are used gently")),
    el("div", "sub" + (growthErr ? " err" : ""), growthErr || t(GROWTH_SAYS)));
  head.append(who);
  box.append(head);
  if (!cur) return;
  // the saved policies' chart is asked again on each draw: its accounts'
  // days move on while the page stays open (review L4)
  if (!growthDraft) growthChart.key = "";

  const wrap = el("div", "row pref gp");
  const side = el("div", "gp-side");
  const list = el("div", "gp-list");
  list.setAttribute("role", "group");
  list.setAttribute("aria-label", t("Growth policies"));
  for (const p of all) {
    const item = el("button", "gp-item" + (p === cur ? " on" : ""));
    item.type = "button";
    item.dataset.id = p.id;
    item.setAttribute("aria-pressed", String(p === cur));
    const top = el("span", "gp-top");
    top.append(el("span", "gp-name", growthName(p)));
    if (p.id === d.def) top.append(el("span", "gp-tag def", t("Default")));
    else if (p.builtin) top.append(el("span", "gp-tag", t("Built in")));
    item.append(top, el("span", "gp-sum", growthSummary(p)));
    item.onclick = () => { growthSel = p.id; redraw(); };
    list.append(item);
  }
  const add = el("button", "text growth-new", t("New policy"));
  add.type = "button";
  add.onclick = () => {
    const p = { id: growthFreeID(all), name: growthFreeName(all, t("New policy")), kind: "ramp", start: "50", days: "14", stages: [] };
    d.own.push(p);
    growthSel = p.id;
    edit();
    redraw();
    box.querySelector(".growth-name")?.select();
  };
  side.append(list, add);

  const main = el("div", "gp-main");
  main.append(growthEditor(cur, d, all, edit, redraw));
  const chart = el("div", "gp-chart");
  growthChart.box = chart;
  growthChart.ro?.disconnect();
  let seenW = 0;
  growthChart.ro = new ResizeObserver(() => {
    if (chart.isConnected && Math.abs(chart.clientWidth - seenW) > 1) { seenW = chart.clientWidth; growthDraw(); }
  });
  growthChart.ro.observe(chart);
  main.append(chart, growthLegend(), growthTable());
  wrap.append(side, main);
  box.append(wrap);
  growthAsk(cur);

  const foot = el("div", "row pref growth-foot");
  const fw = el("div", "who");
  const inUse = all.find((p) => p.id === d.def);
  // the subscriptions the policy shown holds, as growth/preview tells them
  // (T-131): filled in as its chart comes back (growthUseFill)
  fw.append(el("div", "name", t("In use")), el("div", "gp-note growth-inuse", t("{name} is the default: a subscription that picked no policy follows it", { name: inUse ? growthName(inUse) : t("Standard") })), el("div", "gp-note growth-uses"));
  const fv = el("div", "val");
  const save = el("button", "text growth-save", t("Save"));
  save.type = "button";
  save.disabled = !growthDraft;
  save.onclick = () => {
    const out = [], names = new Set(builtins.map((b) => growthName(b).toLowerCase()).concat(Object.values(GROWTH_BUILTIN).map((n) => n.toLowerCase())));
    for (const p of d.own) {
      const [o, why] = growthOut(p);
      if (!o) { growthErr = `${p.name.trim() || t("A policy")}: ${why}`; growthSel = p.id; return redraw(); }
      if (names.has(o.name.toLowerCase())) { growthErr = t("Two policies are named {name}", { name: o.name }); growthSel = p.id; return redraw(); }
      names.add(o.name.toLowerCase());
      out.push(o);
    }
    writingPrefs(api("settings/growth-policies", { policies: out, default: d.def }))
      .then((ns) => { growthDraft = null; growthErr = ""; prefs = state.settings = ns; renderSettings(); status(t("Saved"), "ok", 1500); })
      .catch((e) => { growthErr = t("magpie didn't save the policies: {why}", { why: e.message }); redraw(); });
  };
  const discard = el("button", "text growth-discard", t("Discard"));
  discard.type = "button";
  discard.disabled = !growthDraft;
  discard.onclick = () => { growthDraft = null; growthErr = ""; redraw(); };
  fv.append(save, discard);
  foot.append(fw, fv);
  box.append(foot);
}

// growthEditor is the policy p's name, kind and numbers, read only for
// magpie's own, with Make default, Copy and Delete
function growthEditor(p, d, all, edit, redraw) {
  const ed = el("div", "gp-edit");
  const top = el("div", "gp-line");
  if (p.builtin) {
    top.append(el("span", "gp-title", growthName(p)));
  } else {
    const name = input(p.name, t("Name"));
    name.className = "words growth-name";
    name.setAttribute("aria-label", t("Name"));
    name.maxLength = 64;
    name.oninput = () => {
      p.name = name.value;
      const shown = ed.closest(".gp")?.querySelector(`.gp-item[data-id="${CSS.escape(p.id)}"] .gp-name`);
      if (shown) shown.textContent = p.name;
      edit();
    };
    top.append(name);
  }
  const acts = el("span", "gp-acts");
  if (p.id !== d.def) {
    const def = el("button", "text growth-make-default", t("Make default"));
    def.type = "button";
    def.onclick = () => { d.def = p.id; edit(); redraw(); };
    acts.append(def);
  }
  if (p.kind !== "off") {
    const copy = el("button", "text growth-copy", t("Copy"));
    copy.type = "button";
    copy.onclick = () => {
      const c = growthEditable(p);
      c.id = growthFreeID(all);
      c.name = growthFreeName(all, t("Copy of {name}", { name: growthName(p) }));
      d.own.push(c);
      growthSel = c.id;
      edit();
      redraw();
    };
    acts.append(copy);
  }
  if (!p.builtin) {
    const del = el("button", "text growth-delete", t("Delete"));
    del.type = "button";
    if (p.id === d.def) {
      del.disabled = true;
      del.title = t("Make another policy the default first");
    }
    del.onclick = () => { d.own = d.own.filter((x) => x !== p); growthSel = d.def; edit(); redraw(); };
    acts.append(del);
  }
  top.append(acts);
  ed.append(top);

  if (p.builtin) {
    ed.append(el("div", "gp-note", p.kind === "off" ? t("magpie's own policy: no account is held.") : t("magpie's own policy, read only. Copy it to change it.")));
    return ed;
  }
  const kind = segs([["ramp", t("Ramp")], ["stages", t("In stages")]], p.kind, (v) => {
    p.kind = v;
    if (v === "ramp" && !p.start) [p.start, p.days] = ["50", "14"];
    if (v === "stages" && !p.stages.length) p.stages.push({ from: "0", window: "5h", cap: p.start || "50" });
    edit();
    redraw();
  });
  kind.setAttribute("aria-label", t("How it grows"));
  ed.append(kind);

  if (p.kind === "ramp") {
    const line = el("div", "gp-line gp-ramp");
    const start = input(p.start, "50");
    start.className = "words growth-start";
    start.inputMode = "numeric";
    start.setAttribute("aria-label", t("Share on day 0, in percent"));
    start.oninput = () => { p.start = start.value.trim(); edit(); };
    const days = input(p.days, "14");
    days.className = "words growth-days";
    days.inputMode = "numeric";
    days.setAttribute("aria-label", t("Days to reach 100%"));
    days.oninput = () => { p.days = days.value.trim(); edit(); };
    line.append(el("span", "gp-lab", t("Starts at")), start, el("span", "growth-pct", "%"), el("span", "gp-lab", t("reaches 100% in")), days, el("span", "gp-lab", t("days")));
    ed.append(line, el("div", "gp-note", t("Every window, the 5 hours and the week alike, is held at this share on day 0, rising each day to 100% on the last.")));
    return ed;
  }

  // a stage's days, as the starts typed say them: its window's share lasts
  // until the next stage that holds the same window (T-128)
  const startsOf = (win) => p.stages.filter((g) => g.window === win).map((g) => Number(g.from)).filter((n) => Number.isInteger(n) && n >= 0).sort((a, b) => a - b);
  const daysOf = (from, win) => {
    const next = startsOf(win).find((n) => n > from);
    return next === undefined ? t("From day {from} on", { from }) : t("Days {from} to {to}", { from, to: next });
  };
  p.stages.forEach((g, i) => {
    const r = el("div", "gp-line growth-stage");
    r.append(el("span", "gp-days", Number.isInteger(Number(g.from)) && g.from !== "" ? daysOf(Number(g.from), g.window) : t("A stage with no start day")));
    const from = input(g.from, "0");
    from.className = "words growth-from";
    from.inputMode = "numeric";
    from.setAttribute("aria-label", t("Starts on day"));
    from.oninput = () => { g.from = from.value.trim(); edit(); };
    from.onchange = redraw;
    const win = segs(GROWTH_WINDOWS.map(([id, name]) => [id, t(name)]), g.window, (v) => { g.window = v; edit(); redraw(); });
    win.setAttribute("aria-label", t("Window held"));
    const cap = input(g.cap, "80");
    cap.className = "words growth-cap";
    cap.inputMode = "numeric";
    cap.setAttribute("aria-label", t("Share of the window, in percent"));
    cap.oninput = () => { g.cap = cap.value.trim(); edit(); };
    const del = el("button", "text growth-del", "×");
    del.type = "button";
    del.title = t("Remove this stage");
    del.setAttribute("aria-label", del.title);
    del.onclick = () => { p.stages.splice(i, 1); edit(); redraw(); };
    r.append(from, win, cap, el("span", "growth-pct", "%"), del);
    ed.append(r);
  });
  const add = el("button", "text growth-add", t("Add a stage"));
  add.type = "button";
  add.onclick = () => {
    const last = Math.max(-1, ...p.stages.map((g) => Number(g.from)).filter(Number.isInteger));
    p.stages.push({ from: String(last + 1), window: "5h", cap: "90" });
    edit();
    redraw();
    [...ed.querySelectorAll(".growth-stage")].at(-1)?.querySelector(".growth-from")?.focus(NO_SCROLL);
  };
  // stages saved before T-128 read differently now (T-136)
  ed.append(add, el("div", "gp-note growth-stages-note", t("Each window keeps the share of the latest stage that lists it: a stage that leaves a window out leaves it as it was. magpie used to let such a window go.")));
  return ed;
}

// growthAsk asks for the chart of p as the page has it, once its numbers
// are a policy; until then, and while it is asked, the last chart stays,
// faded
function growthAsk(p) {
  const [out] = growthOut(p);
  const key = out ? JSON.stringify({ id: out.id, kind: out.kind, start: out.start, days: out.days, stages: out.stages }) : "";
  growthChart.want = key;
  growthChart.err = "";
  clearTimeout(growthChart.timer);
  if (key && key === growthChart.key) return growthDraw();
  growthDraw();
  if (!key) return;
  growthChart.timer = setTimeout(() => {
    api("growth/preview", { policy: out }).then((data) => {
      if (growthChart.want !== key) return;
      growthChart.key = key;
      growthChart.data = data;
      growthChart.err = "";
      growthDraw();
    }).catch((e) => {
      if (growthChart.want !== key) return;
      growthChart.err = t("magpie couldn't draw this policy: {why}", { why: e.message });
      growthDraw();
    });
  }, growthChart.data ? 120 : 0);
}

const gsvg = (tag, attrs = {}) => {
  const e = document.createElementNS(GROWTH_SVG, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  return e;
};

// growthDraw draws the last chart that came back into its box: a line a
// window, a step a day, and each account a dot on its day
function growthDraw() {
  const box = growthChart.box;
  if (!box) return;
  const data = growthChart.data;
  // a redraw (a resize, the answer coming in) keeps the day a reader is on
  const had = box.contains(document.activeElement) ? growthChart.day : -1;
  box.classList.toggle("stale", growthChart.key !== growthChart.want || !data);
  box.replaceChildren();
  const pts = data?.points || [];
  const W = Math.max(240, box.clientWidth || 480), H = 180, L = 38, R = 12, T = 10, B = 24;
  const last = Math.max(1, pts.length - 1);
  const x = (day) => L + ((W - L - R) * Math.min(day, last)) / last;
  const y = (v) => T + ((H - T - B) * (100 - v)) / 100;
  const chart = gsvg("svg", { width: W, height: H, viewBox: `0 0 ${W} ${H}`, class: "gp-svg", tabindex: "0", role: "group" });
  chart.setAttribute("aria-label", t("Each day's share of the 5 hours and the week"));
  for (const v of [0, 25, 50, 75, 100]) {
    chart.append(gsvg("line", { x1: L, x2: W - R, y1: y(v), y2: y(v), class: v ? "grid" : "base" }));
    const lab = gsvg("text", { x: L - 6, y: y(v) + 3.5, class: "tick", "text-anchor": "end" });
    lab.textContent = `${v}%`;
    chart.append(lab);
  }
  const step = Math.max(1, Math.ceil(last / Math.max(2, Math.floor((W - L - R) / 56))));
  for (let day = 0; day <= last; day += step) {
    if (day && last - day < step / 2 && day !== last) continue;
    const lab = gsvg("text", { x: x(day), y: H - 7, class: "tick", "text-anchor": day ? "middle" : "start" });
    lab.textContent = String(day);
    chart.append(lab);
  }
  if (last % step) {
    const lab = gsvg("text", { x: x(last), y: H - 7, class: "tick", "text-anchor": "end" });
    lab.textContent = String(last);
    chart.append(lab);
  }
  for (const [k, cls] of [["fiveHours", "five"], ["week", "week"]]) {
    if (!pts.length) break;
    let path = `M${x(0)} ${y(pts[0][k])}`;
    for (let i = 1; i < pts.length; i++) path += `H${x(pts[i].day)}V${y(pts[i][k])}`;
    chart.append(gsvg("path", { d: path, class: "ln " + cls, "data-window": cls }));
  }
  // the accounts on each day: past the last day drawn, on its edge
  const byDay = new Map();
  for (const a of data?.accounts || []) {
    const day = Math.min(a.day, last);
    if (!byDay.has(day)) byDay.set(day, []);
    byDay.get(day).push(a);
  }
  const at = (day) => pts[Math.min(day, pts.length - 1)];
  for (const [day, as] of byDay) {
    if (!at(day)) continue;
    chart.append(gsvg("circle", { cx: x(day), cy: y(Math.min(at(day).fiveHours, at(day).week)), r: as.length > 1 ? 5.5 : 4.5, class: "acct", "data-day": day }));
  }
  const hair = gsvg("line", { y1: T, y2: H - B, class: "hair" });
  hair.style.display = "none";
  chart.append(hair);
  const tip = el("div", "gp-tip");
  tip.hidden = true;
  let shown = -1;
  const show = (day) => {
    if (!pts.length) return;
    day = Math.max(0, Math.min(last, day));
    shown = day;
    growthChart.day = day;
    const p = at(day);
    hair.setAttribute("x1", x(day));
    hair.setAttribute("x2", x(day));
    hair.style.display = "";
    tip.replaceChildren(el("b", "", t("Day {day}", { day })));
    for (const [k, cls, name] of [["fiveHours", "five", "5 hours"], ["week", "week", "7 days"]]) {
      const r = el("div", "gp-tip-row");
      r.append(el("i", "gp-key " + cls), el("span", "", t(name)), el("em", "", `${p[k]}%`));
      tip.append(r);
    }
    for (const a of byDay.get(day) || []) {
      const r = el("div", "gp-tip-acct");
      r.append(el("span", "", a.account), el("em", "", a.waiting ? t("not used yet") : t("now on day {day}", { day: a.day })));
      r.title = a.name;
      tip.append(r);
    }
    tip.hidden = false;
    const left = x(day) + 12;
    tip.style.left = "";
    tip.style.right = "";
    if (left + 200 > W) tip.style.right = `${W - x(day) + 12}px`;
    else tip.style.left = `${left}px`;
  };
  const hide = () => { shown = -1; tip.hidden = true; hair.style.display = "none"; };
  chart.addEventListener("pointermove", (e) => {
    const r = chart.getBoundingClientRect();
    show(Math.round(((e.clientX - r.left - L) / (W - L - R)) * last));
  });
  chart.addEventListener("pointerleave", () => { if (document.activeElement !== chart) hide(); });
  chart.addEventListener("focus", () => show(0));
  chart.addEventListener("blur", hide);
  chart.addEventListener("keydown", (e) => {
    const go = { ArrowRight: 1, ArrowLeft: -1, End: last, Home: -last }[e.key];
    if (go === undefined) return;
    e.preventDefault();
    show((shown < 0 ? 0 : shown) + go);
  });
  box.append(chart, tip);
  if (growthChart.err && growthChart.key !== growthChart.want) box.append(el("div", "gp-note err growth-chart-err", growthChart.err));
  if (had >= 0) { chart.focus(NO_SCROLL); show(had); }
  growthTableFill();
  growthUseFill();
}

// growthUseFill says which subscriptions the policy shown holds and how
// many of their accounts ramp up now; a policy one picked can't be
// deleted until it picks another (T-131), as the server refuses too
function growthUseFill() {
  const box = growthChart.box?.closest("#growthList"), say = box?.querySelector(".growth-uses");
  if (!say) return;
  const uses = growthChart.key === growthChart.want ? growthChart.data?.uses : null;
  if (!uses) return say.replaceChildren();
  say.replaceChildren(...(uses.length ? uses.map((u) => el("div", "growth-use", t(u.picked ? "{name} picked it · {n} ramping up" : "{name} follows the default · {n} ramping up", { name: u.name, n: u.accounts })))
    : [el("div", "growth-use", t("No subscription is held by it"))]));
  const picked = uses.filter((u) => u.picked).map((u) => u.name), del = box.querySelector(".growth-delete");
  if (del && picked.length) {
    del.disabled = true;
    del.title = t("{names} picked it: pick another policy in its editor first", { names: picked.join(", ") });
  }
}

function growthLegend() {
  const lg = el("div", "gp-legend");
  for (const [cls, name] of [["five", "5 hours"], ["week", "7 days"]]) {
    const k = el("span", "gp-leg");
    k.append(el("i", "gp-key " + cls), el("span", "", t(name)));
    lg.append(k);
  }
  const a = el("span", "gp-leg");
  a.append(el("i", "gp-dot"), el("span", "", t("An account, on its day")));
  lg.append(a);
  return lg;
}

// the chart's days as a table, each day a share changes and each day an
// account is on, for a reader who doesn't hover
function growthTable() {
  const det = el("details", "gp-table");
  det.append(el("summary", "", t("Days as a table")));
  det.ontoggle = growthTableFill;
  return det;
}

function growthTableFill() {
  const det = growthChart.box?.parentElement?.querySelector(".gp-table");
  if (!det?.open) return;
  const pts = growthChart.data?.points || [], accts = growthChart.data?.accounts || [];
  const tbl = el("table");
  const hr = el("tr");
  for (const h of [t("Day of use"), t("5 hours"), t("7 days"), t("Accounts")]) hr.append(el("th", "", h));
  tbl.append(hr);
  const lastDay = pts.length - 1;
  pts.forEach((p, i) => {
    const on = accts.filter((a) => Math.min(a.day, lastDay) === p.day);
    const prev = pts[i - 1];
    if (i && i !== lastDay && !on.length && prev.fiveHours === p.fiveHours && prev.week === p.week) return;
    const r = el("tr");
    r.append(el("td", "", String(p.day)), el("td", "", `${p.fiveHours}%`), el("td", "", `${p.week}%`), el("td", "", on.map((a) => a.account).join(", ")));
    tbl.append(r);
  });
  det.querySelector("table")?.remove();
  det.append(tbl);
}
