// Models › Proxy IPs' Batch add (需求方 2026-11-13, after another proxy
// manager's 批量添加代理): proxies pasted one a line, the login before or
// after the host and port, IPv4 or IPv6, a type written or the one picked,
// a refresh URL in [ ] read past. Each line is read as it is typed and
// listed with what was read, or why not; Check asks each proxy its exit
// (proxy/check, four at a time). Add keeps each under its exit once
// checked, else under its host when that is an IP (a host that is a name
// is checked first), with ips.js' list kept whole as one save. A proxy
// listed already is skipped and said. Run after ips.js.

const IPS_BATCH_MAX = 100;
// the most IPs the server keeps (renewal.MaxIPs)
const IPS_BATCH_MAX_IPS = 500;
// the types a proxy can be kept as (settings.CheckProxy), by what a line
// may write; "socks" is SOCKS5
const IPS_BATCH_TYPES = { http: "http", https: "https", socks5: "socks5", socks5h: "socks5h", socks: "socks5" };
// open, what is typed, the type of a line that writes none (SOCKS5 unless
// picked, as the format the user follows says), each proxy's check by its
// address ({ busy } | { ip } | { err }), and an Add or Check under way
let ipsBatchOn = false, ipsBatchText = "", ipsBatchType = "socks5", ipsBatchChecks = {}, ipsBatchBusy = false, ipsBatchErr = "";
try { ipsBatchType = IPS_BATCH_TYPES[localStorage.getItem("magpie.ipsBatchType")] || "socks5"; } catch {}

// ipsBatchHost: a host as written, bracketed when IPv6, as { host, ip }
// (ip the address when it is one), or null
function ipsBatchHost(h) {
  if (/^\[[0-9a-f:.]+\]$/i.test(h)) {
    try { const v = new URL("http://" + h).hostname.slice(1, -1); return { host: v, v6: true, ip: ipsCanon(v) }; } catch { return null; }
  }
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(h)) return h.split(".").every((o) => +o <= 255) ? { host: h, ip: h } : null;
  // a name: a dot and a letter in it, so a username is never taken for one
  if (/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i.test(h) && /[a-z]/i.test(h) && h.length <= 253) return { host: h.toLowerCase(), ip: "" };
  return /^localhost$/i.test(h) ? { host: "localhost", ip: "" } : null;
}
function ipsBatchHostPort(h, p) {
  if (!/^\d{1,5}$/.test(p || "") || +p < 1 || +p > 65535) return null;
  const x = ipsBatchHost(h || "");
  return x && { ...x, port: String(+p) };
}

// ipsBatchColons: s split at each : outside [ ], an empty part kept
function ipsBatchColons(s) {
  const parts = [];
  let cur = "";
  for (let i = 0; i < s.length; i++) {
    const j = s[i] === "[" ? s.indexOf("]", i) : -1;
    if (j > 0) { cur += s.slice(i, j + 1); i = j; } else if (s[i] === ":") { parts.push(cur); cur = ""; } else cur += s[i];
  }
  return [...parts, cur];
}

// ipsBatchParse: a line as { type, host, port, user, pass, ip, url }, or
// { err }. Read: [type://]host:port[:user[:password]],
// [type://]user:password:host:port, [type://]user:password@host:port,
// host:port@user:password, each with an IPv6 host in [ ], the parts split
// by : or by spaces; a [refresh URL] and a " # comment" at the end are
// read past. A line that reads as host:port:user:password and as
// user:password:host:port alike takes the one whose host is an IP, and is
// said ambiguous when both or neither are (review H1)
function ipsBatchParse(line, type) {
  let s = line.trim().replace(/\s+#.*$/, "").replace(/\[[^\]]*\]$/, "").trim();
  const m = /^([a-z][a-z0-9+.-]*):\/\//i.exec(s);
  if (m) {
    type = IPS_BATCH_TYPES[m[1].toLowerCase()];
    if (!type) return { err: t("{type} proxies aren't supported: use HTTP, HTTPS or SOCKS5", { type: m[1].toUpperCase() }) };
    s = s.slice(m[0].length);
  }
  s = s.replace(/\/+$/, "");
  let hp = null, user = "", pass = "";
  const at = s.lastIndexOf("@");
  if (at >= 0) {
    const a = s.slice(0, at), b = s.slice(at + 1), split = (v) => /^(\[[^\]]*\]|[^:]*):(.*)$/.exec(v) || [];
    let login = a;
    if (!(hp = ipsBatchHostPort(...split(b).slice(1)))) {
      hp = ipsBatchHostPort(...split(a).slice(1));
      login = b;
    }
    // a login pasted from a URL is escaped already (review H2)
    const dec = (v) => { try { return decodeURIComponent(v); } catch { return v; } };
    const c = login.indexOf(":");
    [user, pass] = (c < 0 ? [login, ""] : [login.slice(0, c), login.slice(c + 1)]).map(dec);
  } else {
    // the parts, an empty one kept (::pass is no username, review M2)
    const parts = /\s/.test(s) ? s.split(/\s+/) : ipsBatchColons(s);
    if (parts.length > 4) return { err: t("Too many parts: write it as username:password@host:port") };
    const first = parts.length >= 2 && ipsBatchHostPort(parts[0], parts[1]);
    const last = parts.length === 4 && ipsBatchHostPort(parts[2], parts[3]);
    if (first && last && !first.ip === !last.ip) return { err: t("Read either way: write it as username:password@host:port") };
    if (first && (!last || first.ip)) [hp, user, pass] = [first, parts[2] || "", parts[3] || ""];
    else if (last) [hp, user, pass] = [last, parts[0], parts[1]];
  }
  if (!hp) return { err: t("No host and port found in it") };
  const enc = encodeURIComponent;
  const url = type + "://" + (user || pass ? enc(user) + (pass ? ":" + enc(pass) : "") + "@" : "") + (hp.v6 ? "[" + hp.host + "]" : hp.host) + ":" + hp.port;
  return { type, host: hp.host, v6: !!hp.v6, port: hp.port, user, pass, ip: hp.ip, url };
}

// ipsBatchLines: each line typed that says something, as its number and
// what was read; one read twice is said so
function ipsBatchLines() {
  const seen = new Set();
  return ipsBatchText.split(/\r?\n/).map((text, i) => ({ n: i + 1, text })).filter((l) => l.text.trim() && !l.text.trim().startsWith("#")).map((l) => {
    const x = { ...l, ...ipsBatchParse(l.text, ipsBatchType) };
    if (x.url && seen.has(x.url)) return { ...l, err: t("The same proxy as a line above") };
    if (x.url) seen.add(x.url);
    return x;
  });
}

// ipsBatchCheck: the exit of each proxy given not checked yet, four at a
// time; draw is called as each answers
async function ipsBatchCheck(rows, draw) {
  const todo = rows.filter((x) => !ipsBatchChecks[x.url]?.ip);
  for (const x of todo) ipsBatchChecks[x.url] = { busy: true };
  draw();
  let at = 0;
  const work = async () => {
    while (at < todo.length) {
      const x = todo[at++];
      ipsBatchChecks[x.url] = await api("proxy/check", { proxy: x.url }).then((out) => (out?.through?.ip ? { ip: ipsCanon(out.through.ip) } : { err: out?.through?.error || "" }), (e) => ({ err: e.message }));
      draw();
    }
  };
  await Promise.all([work(), work(), work(), work()]);
}

// ipsBatchMerge: the list with each proxy read kept under its IP: told to
// the IP listed there, or a new one. Skipped, and said: one listed already,
// one with no IP known, one whose IP is told IPS_MAX_PROXIES already
function ipsBatchMerge(list, rows) {
  const next = list.map((x) => ({ ...x, proxies: [...(x.proxies || [])] }));
  const out = { next, added: 0, linked: 0, listed: [], unknown: [], full: [], over: [], unchecked: [] };
  for (const x of rows) {
    if (next.some((y) => y.proxies.includes(x.url))) { out.listed.push(x); continue; }
    const ip = ipsBatchChecks[x.url]?.ip || x.ip;
    if (!ip) { out.unknown.push(x); continue; }
    const y = next.find((z) => ipsCanon(z.ip) === ip);
    if (!y && next.length >= IPS_BATCH_MAX_IPS) { out.over.push(x); continue; }
    if (!ipsBatchChecks[x.url]?.ip && (!y || y.proxies.length < IPS_MAX_PROXIES)) out.unchecked.push(x);
    if (!y) { next.push({ ip, name: "", proxies: [x.url] }); out.added++; }
    else if (y.proxies.length >= IPS_MAX_PROXIES) out.full.push(x);
    else { y.proxies.push(x.url); out.linked++; }
  }
  return out;
}

const ipsBatchListed = (x) => (ipsList || []).some((y) => (y.proxies || []).includes(x.url));
const ipsBatchAddr = (x) => (x.v6 ? "[" + x.host + "]" : x.host) + ":" + x.port;

function ipsBatchOpen() {
  ipsBatchOn = true;
  ipsBatchErr = "";
  ipsSaid = "";
  ipsErr = "";
  ipsDraw();
  ipsBatchDraw();
  setTimeout(() => $("#ipsBatchBox .ips-batch-in")?.focus(), 0);
}
function ipsBatchClose() {
  ipsBatchOn = false;
  ipsBatchText = "";
  ipsBatchChecks = {};
  ipsBatchErr = "";
  $("#ipsBatchBox").replaceChildren();
  ipsDraw();
}

// ipsBatchDraw: the form, drawn whole when opened; what is typed redraws
// the lines read under it, not the box typed in
function ipsBatchDraw() {
  const box = $("#ipsBatchBox");
  box.hidden = !ipsBatchOn;
  if (!ipsBatchOn) return box.replaceChildren();
  const help = el("div", "ips-batch-help");
  help.append(el("div", "ips-batch-head", t("Batch add proxies")),
    el("pre", "ips-batch-formats", ["type://host:port:username:password[refresh URL]", "type://username:password@host:port[refresh URL]", "type://[IPv6]:port:username:password", "type://username:password@[IPv6]:port", "username:password:host:port", "host port username password"].join("\n")));
  const rules = el("ol", "ips-batch-rules");
  for (const s of [
    t("One proxy a line, at most {n} at a time. A host and a port are needed; the username and password are optional and may come before or after them.", { n: IPS_BATCH_MAX }),
    t("A line that writes no type takes the one picked below. HTTP, HTTPS, SOCKS5 and SOCKS5h are supported; SSH isn't."),
    t("A refresh URL in [ ] at the end of a line is read past and not kept."),
    t("A password with : or @ in it is read only as username:password@host:port; otherwise add that proxy on its own with Add IP."),
    t("Each proxy is kept under its exit IP once checked, else under its host when that is an IP address. A proxy listed already is skipped."),
  ]) rules.append(el("li", "", s));
  help.append(rules);
  const ta = el("textarea", "ips-batch-in");
  ta.spellcheck = false;
  ta.autocomplete = "off";
  ta.rows = 8;
  ta.value = ipsBatchText;
  ta.placeholder = t("For example:") + "\n192.168.0.1:8000\n192.168.0.1:8000:username:password\nsocks5://192.168.0.1:8000[https://example.com/refresh]\nhttp://username:password@192.168.0.1:8000\nhttp://[2001:db8::e13]:8000:username:password";
  ta.setAttribute("aria-label", t("Proxies, one a line"));
  ta.onkeydown = (e) => e.stopPropagation();
  const bar = el("div", "ips-batch-bar");
  const typeLabel = el("span", "ips-batch-type-label", t("Type when a line writes none"));
  const typePick = el("button", "sess-pick ips-batch-type-pick");
  typePick.type = "button";
  const said = el("span", "ips-batch-said");
  said.setAttribute("role", "status");
  const check = el("button", "text ips-batch-check", t("Check proxies"));
  check.type = "button";
  check.title = t("Ask Cloudflare through each proxy for the address its requests leave from");
  bar.append(typeLabel, typePick, el("span", "grow"), said, check);
  const lines = el("div", "ips-batch-lines");
  const err = el("p", "acx-err ips-batch-err");
  err.setAttribute("role", "alert");
  const acts = el("div", "ips-batch-acts");
  const add = el("button", "text primary ips-batch-add");
  add.type = "button";
  const no = el("button", "text ips-batch-cancel", t("Cancel"));
  no.type = "button";
  no.onclick = () => { if (!ipsBatchBusy) ipsBatchClose(); };
  acts.append(add, no);

  const redraw = () => {
    if (!ta.isConnected) return;
    const all = ipsBatchLines(), ok = all.filter((x) => x.url), bad = all.length - ok.length;
    const over = ok.length > IPS_BATCH_MAX;
    sessPick(typePick, "SOCKS5", ipsBatchType === "socks5" ? "" : ipsBatchType, [["http", "HTTP"], ["https", "HTTPS"], ["socks5h", "SOCKS5h"]].map(([v, name]) => ({ v, name })), "Proxy type", (v) => {
      ipsBatchType = v || "socks5";
      try { localStorage.setItem("magpie.ipsBatchType", ipsBatchType); } catch {}
      redraw();
    });
    typePick.hidden = false;
    typePick.setAttribute("aria-label", t("Type when a line writes none"));
    said.textContent = all.length ? t(ok.length === 1 ? "1 proxy read" : "{n} proxies read", { n: ok.length }) + (bad ? " · " + t(bad === 1 ? "1 line not read" : "{n} lines not read", { n: bad }) : "") : "";
    lines.replaceChildren();
    for (const x of all) {
      const row = el("div", "ips-batch-row" + (x.err ? " err" : ""));
      row.dataset.line = x.n;
      row.append(el("span", "ips-batch-n", String(x.n)));
      if (x.err) {
        row.append(el("code", "ips-batch-text", x.text.trim()), el("span", "ips-batch-why", x.err));
      } else {
        const c = ipsBatchChecks[x.url], listed = ipsBatchListed(x);
        row.append(el("span", "ips-batch-type", x.type.toUpperCase()), el("code", "ips-batch-addr", ipsBatchAddr(x)));
        if (x.user) row.append(el("span", "ips-batch-user", x.user));
        if (x.pass) row.append(el("span", "ips-batch-pass", "••••••"));
        const exit = el("span", "ips-batch-exit");
        if (listed) exit.textContent = t("Listed already");
        else if (c?.busy) exit.textContent = t("Checking…");
        else if (c?.ip) exit.textContent = t("Exit {ip}", { ip: c.ip });
        else if (c) { exit.textContent = t("Didn't answer"); exit.title = c.err; exit.classList.add("bad"); }
        row.append(exit);
      }
      lines.append(row);
    }
    lines.hidden = !all.length;
    const errs = [];
    if (over) errs.push(t("At most {n} proxies at a time: {m} are read", { n: IPS_BATCH_MAX, m: ok.length }));
    if (ipsBatchErr) errs.push(ipsBatchErr);
    err.textContent = errs.join(" ");
    err.hidden = !errs.length;
    check.disabled = ipsBatchBusy || !ok.length || over;
    check.classList.toggle("busy", ipsBatchBusy);
    add.textContent = t(ok.length === 1 ? "Add 1 proxy" : "Add {n} proxies", { n: ok.length });
    add.disabled = ipsBatchBusy || !ok.length || over || !ipsList;
    add.classList.toggle("busy", ipsBatchBusy);
    ta.readOnly = ipsBatchBusy;
  };
  ta.oninput = () => { ipsBatchText = ta.value; ipsBatchErr = ""; redraw(); };
  check.onclick = async () => {
    if (ipsBatchBusy) return;
    ipsBatchBusy = true;
    ipsBatchErr = "";
    try { await ipsBatchCheck(ipsBatchLines().filter((x) => x.url && !ipsBatchListed(x)), redraw); } finally { ipsBatchBusy = false; redraw(); }
  };
  add.onclick = async () => {
    if (ipsBatchBusy || ipsImporting || !ipsList) return;
    ipsBatchBusy = true;
    ipsBatchErr = "";
    redraw();
    ipsDraw();
    let kept = false, out;
    try {
      const rows = ipsBatchLines().filter((x) => x.url);
      // a host that is a name has no IP till its exit is read
      await ipsBatchCheck(rows.filter((x) => !x.ip && !ipsBatchChecks[x.url]?.ip && !ipsBatchListed(x)), redraw);
      while (ipsSaving) await new Promise((r) => setTimeout(r, 50));
      if (!ipsList) return;
      out = ipsBatchMerge(ipsList, rows);
      kept = !(out.added || out.linked) || await ipsKeep(out.next);
      // said in the form, not under the list as well
      if (!kept) { ipsBatchErr = ipsErr || t("The import couldn't be saved: try it again"); ipsErr = ""; ipsDraw(); }
    } finally {
      ipsBatchBusy = false;
      if (!kept) { redraw(); ipsDraw(); }
    }
    if (!kept) return;
    const parts = [];
    if (out.added) parts.push(t(out.added === 1 ? "1 IP added" : "{n} IPs added", { n: out.added }));
    if (out.linked) parts.push(t(out.linked === 1 ? "1 proxy told to an IP listed" : "{n} proxies told to IPs listed", { n: out.linked }));
    if (out.unchecked.length) parts.push(t("Not checked, so kept under the IP they connect to: {list}", { list: out.unchecked.map(ipsBatchAddr).join(", ") }));
    if (out.listed.length) parts.push(t("Listed already, so skipped: {list}", { list: out.listed.map(ipsBatchAddr).join(", ") }));
    const errs = [];
    if (out.unknown.length) errs.push(t("These proxies didn't answer, so their exit isn't known: {list}", { list: out.unknown.map(ipsBatchAddr).join(", ") }));
    if (out.over.length) errs.push(t("At most {n} IPs are kept, so these weren't added: {list}", { n: IPS_BATCH_MAX_IPS, list: out.over.map(ipsBatchAddr).join(", ") }));
    if (out.full.length) errs.push(t("These IPs are told {n} proxies already, so no more were added: {list}", { n: IPS_MAX_PROXIES, list: [...new Set(out.full.map((x) => ipsBatchChecks[x.url]?.ip || x.ip))].join(", ") }));
    ipsBatchClose();
    ipsSaid = parts.join(" · ");
    ipsErr = errs.join(" ");
    ipsDraw();
  };
  box.replaceChildren(help, ta, bar, lines, err, acts);
  redraw();
}
