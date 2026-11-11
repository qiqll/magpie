// A paid period's pill and its date editor, which Models › Accounts (an
// account's, T-154) and Models › Proxy IPs (an IP's, T-155) both draw. The
// server works the period out (renewal.Of: 30 days from the opening, due
// from 5 days before its end); nothing is counted here, so the two pages
// never say different things of the same dates.

// renewSaid: a period's words, its class and its title — none told, its
// end, a renewal due within days, or ended
function renewSaid(st) {
  if (!st) return { text: t("Set start date"), cls: "none", title: t("When its 30 days were paid from: magpie reminds you 5 days before they end") };
  const ends = new Date(st.ends).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" });
  const title = t("Started {start}, its 30 days end {end}. Click to change", { start: new Date(st.opened).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" }), end: ends });
  if (st.due === "ended") return { text: t("Expired: renew"), cls: "ended", title };
  if (st.due === "soon") return { text: t(st.daysLeft === 1 ? "Renew within 1 day" : "Renew within {n} days", { n: st.daysLeft }), cls: "soon", title };
  return { text: t("Ends {date}", { date: ends }), cls: "ok", title };
}

// renewPill: the period as a button, which opens its editor (open)
function renewPill(st, open) {
  const s = renewSaid(st);
  const b = el("button", "renew-pill " + s.cls, s.text);
  b.type = "button";
  b.title = s.title;
  b.onclick = (e) => { e.stopPropagation(); open(); };
  return b;
}

// renewDay: a time as the date box's value, in the reader's own day
const renewDay = (at) => {
  const d = new Date(at);
  return d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
};

// the date typed in the open editor, by the editor's key, kept over a
// redraw of its page; another editor opened starts from its own date
// (T-154 review M1)
let renewTyped = { key: "", day: "" };

// renewEditor: the start date to pick, Save, Clear (once one is told) and
// Cancel, for the account or IP key. save gets the day's start in the
// reader's zone as an ISO time, "" to clear, and returns whether it was
// kept; done closes the editor.
function renewEditor(key, st, save, done) {
  const box = el("span", "renew-edit");
  box.onclick = (e) => e.stopPropagation();
  const day = el("input", "renew-date");
  day.type = "date";
  if (renewTyped.key !== key) renewTyped = { key, day: "" };
  day.value = renewTyped.day || (st ? renewDay(st.opened) : renewDay(Date.now()));
  day.oninput = () => { renewTyped.day = day.value; };
  day.setAttribute("aria-label", t("Start date"));
  const keep = (v, b) => {
    // one save at a time: Enter twice sends one (T-154 review L3)
    if (box.querySelector(".busy")) return;
    b.classList.add("busy");
    Promise.resolve(save(v)).then((ok) => { b.classList.remove("busy"); if (ok !== false) end(); });
  };
  const end = () => { renewTyped = { key: "", day: "" }; done(); };
  const ok = el("button", "text primary renew-save", t("Save"));
  ok.type = "button";
  ok.onclick = () => {
    const [y, m, d] = day.value.split("-").map(Number);
    if (!y || !m || !d) return day.focus();
    keep(new Date(y, m - 1, d).toISOString(), ok);
  };
  day.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter") ok.click();
    if (e.key === "Escape") end();
  };
  box.append(day, ok);
  if (st) {
    const clear = el("button", "text renew-clear", t("Clear"));
    clear.type = "button";
    clear.title = t("Forget its start date: no reminder is shown");
    clear.onclick = () => keep("", clear);
    box.append(clear);
  }
  const no = el("button", "text renew-cancel", t("Cancel"));
  no.type = "button";
  no.onclick = () => end();
  box.append(no);
  setTimeout(() => day.focus(), 0);
  return box;
}
