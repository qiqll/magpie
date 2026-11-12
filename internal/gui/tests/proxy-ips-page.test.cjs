// Run with Node's test runner and Playwright on the module path; see README.md.
// T-155: Models › Proxy IPs lists the proxy exit IPs from /api/ips, each
// with the period pill Models › Accounts' rows have (renew.js), its period
// the server's (renewal.Of). An IP is added, its name edited, its start
// date set and cleared, and removed, each posting the whole list; one
// added from a saved proxy takes that proxy's exit (proxy/check) and its
// name. A list the server refuses says why and keeps the form. The page's
// tab is in the sidebar under Accounts. T-170: Import from accounts reads
// the exit of each proxy an account goes through and adds what isn't
// listed, each IP names its accounts with their state, and the list is
// found by that state. T-172: an import checks every proxy again and
// moves one whose exit moved; an IP's form lists its proxies, each
// unlinked there. Every language, at a wide and a phone's width; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);
const day = 864e5;
// the server's answer: each IP with its period, worked out from a fixed
// "now" so the fixture says the same days wherever it runs
const NOW = Date.parse("2026-11-11T04:00:00Z");
const periodOf = (opened) => {
  if (!opened) return undefined;
  const ends = Date.parse(opened) + 30 * day, left = ends - NOW;
  const daysLeft = left <= 0 ? 0 : Math.ceil(left / day);
  return { opened, ends: new Date(ends).toISOString(), daysLeft, ...(left <= 0 ? { due: "ended" } : daysLeft <= 5 ? { due: "soon" } : {}) };
};
const answer = (ips, rev) => ({ ips: ips.map((x) => ({ ...x, ...(x.opened ? { renewal: periodOf(x.opened) } : {}) })), rev: "r" + rev, days: 30, warnDays: 5 });

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    proxies: [{ name: "Tokyo line", url: "http://me:pw@tokyo.example:3128" }] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/ips") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        seen.posts.push(body);
        if (body.rev !== "r" + seen.rev) return json({ error: "the IPs were changed elsewhere since they were read" }, 400);
        if (body.ips.some((x) => x.ip === "bad")) return json({ error: "\"bad\" is not an IP address" }, 400);
        seen.ips = body.ips.map((x) => Object.fromEntries(Object.entries(x).filter(([, v]) => v !== undefined && v !== "")));
        seen.rev++;
      }
      return json(answer(seen.ips, seen.rev));
    }
    if (url.pathname === "/api/proxy/check") {
      const asked = req.postDataJSON();
      seen.checks.push(asked);
      // held till the test lets the checks answer
      if (seen.hold) await (seen.held ||= new Promise((r) => { seen.release = () => { seen.hold = false; r(); }; }));
      const exit = seen.exits ? seen.exits[asked.proxy] : "198.51.100.42";
      if (!exit) return json({ source: "saved", through: { error: "dial tcp: i/o timeout" }, direct: { ip: "192.0.2.1" } });
      return json({ source: "settings", through: { ip: exit, loc: "JP", ms: 80 }, direct: { ip: "192.0.2.1" } });
    }
    if (url.pathname === "/api/providers") return json({ providers: seen.providers || [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

const rows = (page) => page.evaluate(() => [...document.querySelectorAll("#ipsList .ips-row")].map((r) => {
  const b = r.querySelector(".renew-pill");
  return [r.dataset.ip, r.querySelector(".sub")?.textContent || "", b.textContent, [...b.classList].filter((c) => c !== "renew-pill").join(" ")];
}));
const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#view-ips *")].filter((e) => e.offsetParent).map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: proxy IPs are listed, added, edited, renewed and removed`, async (t) => {
        const seen = { posts: [], checks: [], rev: 1, ips: [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z" },
          { ip: "203.0.113.8", opened: "2026-10-18T04:00:00Z" },
          { ip: "2001:db8::1", name: "Old", opened: "2026-09-01T00:00:00Z" },
        ] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=ips");
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 3);
        assert.equal(await page.locator("#pageTitle").textContent(), x("Proxy IPs"));
        assert.equal(await page.locator('#nav button[data-view="ips"] span').textContent(), x("Proxy IPs"));
        const ends = (iso) => page.evaluate((iso) => new Date(iso).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" }), iso);

        assert.deepEqual(await rows(page), [
          ["203.0.113.7", "Osaka", x("Renew within {n} days", { n: 5 }), "soon"],
          ["203.0.113.8", "", x("Ends {date}", { date: await ends("2026-11-17T04:00:00.000Z") }), "ok"],
          ["2001:db8::1", "Old", x("Expired: renew"), "ended"],
        ]);
        assert.equal(await page.locator("#ipsSum").textContent(), x("{n} IPs", { n: 3 }) + " · " + x("{n} to renew", { n: 2 }));
        assert.ok(await wider(page) <= 0.5, `the page reaches ${await wider(page)}px past the window`);
        // the accounts filter shows with no IP bound to an account (T-176)
        assert.ok(await page.locator("#ipsFilter").isVisible());

        // add one from the saved proxy: its exit and its name fill the form
        await page.locator("#ipsAdd").click();
        await page.locator("#ipsList .ips-from").click();
        await page.locator(".proto-menu .pm-item", { hasText: "Tokyo line" }).click();
        await page.waitForFunction(() => document.querySelector("#ipsList .ips-in-ip")?.value === "198.51.100.42");
        assert.deepEqual(seen.checks, [{ proxy: "http://me:pw@tokyo.example:3128" }]);
        assert.equal(await page.locator("#ipsList .ips-in-name").inputValue(), "Tokyo line");
        assert.ok(await wider(page) <= 0.5, `the form reaches ${await wider(page)}px past the window`);
        await page.locator("#ipsList .ips-save").click();
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 4 && !document.querySelector("#ipsList .ips-form"));
        assert.deepEqual(seen.posts.at(-1).ips.at(-1), { ip: "198.51.100.42", name: "Tokyo line", proxies: ["http://me:pw@tokyo.example:3128"] });
        assert.equal(seen.posts.at(-1).ips.length, 4);
        assert.deepEqual((await rows(page))[3], ["198.51.100.42", "Tokyo line", x("Set start date"), "none"]);

        // its start date set from the pill
        await page.locator('#ipsList .ips-row[data-ip="198.51.100.42"] .renew-pill').click();
        await page.locator("#ipsList .renew-date").fill("2026-11-11");
        await page.locator("#ipsList .renew-save").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .renew-edit"));
        assert.equal(seen.posts.at(-1).ips[3].opened, "2026-11-10T16:00:00.000Z");
        assert.deepEqual((await rows(page))[3].slice(2), [x("Ends {date}", { date: await ends("2026-12-10T16:00:00.000Z") }), "ok"]);

        // an expired one's date cleared: no time is sent for it
        await page.locator('#ipsList .ips-row[data-ip="2001:db8::1"] .renew-pill').click();
        await page.locator("#ipsList .renew-clear").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .renew-edit"));
        assert.equal(seen.posts.at(-1).ips[2].opened, undefined);
        assert.deepEqual((await rows(page))[2].slice(2), [x("Set start date"), "none"]);
        assert.equal(await page.locator("#ipsSum").textContent(), x("{n} IPs", { n: 4 }) + " · " + x("1 to renew"));

        // a name edited; a refused address says why and keeps the form
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-edit').click();
        await page.locator("#ipsList .ips-in-name").fill("Seoul");
        await page.locator("#ipsList .ips-in-ip").fill("bad");
        await page.locator("#ipsList .ips-save").click();
        await page.waitForFunction(() => !document.querySelector("#ipsErr").hidden);
        assert.equal(await page.locator("#ipsList .ips-form").count(), 1);
        assert.equal(await page.locator("#ipsList .ips-in-name").inputValue(), "Seoul");
        await page.locator("#ipsList .ips-in-ip").fill("203.0.113.8");
        await page.locator("#ipsList .ips-in-ip").press("Enter");
        await page.waitForFunction(() => !document.querySelector("#ipsList .ips-form"));
        assert.deepEqual((await rows(page))[1].slice(0, 2), ["203.0.113.8", "Seoul"]);
        assert.equal(seen.posts.at(-1).ips[1].opened, "2026-10-18T04:00:00Z", "an edit keeps the start date");
        assert.ok(await page.locator("#ipsErr").isHidden());

        // a date typed for one IP is not the next one's (review M1)
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.7"] .renew-pill').click();
        await page.locator("#ipsList .renew-date").fill("2026-01-05");
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .renew-pill').click();
        assert.equal(await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .renew-date').inputValue(), "2026-10-18");
        await page.locator("#ipsList .renew-cancel").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .renew-edit"));

        // removed, once asked; Cancel removes nothing
        const posted = seen.posts.length;
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.7"] .ips-remove').click();
        await page.locator("dialog.action-confirm button:not(.primary)").click();
        assert.equal(seen.posts.length, posted);
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.7"] .ips-remove').click();
        await page.locator("dialog.action-confirm button.primary").click();
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 3);
        assert.deepEqual(seen.posts.at(-1).ips.map((x) => x.ip), ["203.0.113.8", "2001:db8::1", "198.51.100.42"]);
        assert.equal(await page.locator("#ipsSum").textContent(), x("{n} IPs", { n: 3 }));

        // another window's change meanwhile: the save is refused, the list
        // is read again with it and says so (review M2)
        seen.ips = [...seen.ips, { ip: "192.0.2.77", name: "Elsewhere" }];
        seen.rev++;
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-remove').click();
        await page.locator("dialog.action-confirm button.primary").click();
        await page.waitForFunction(() => !document.querySelector("#ipsErr").hidden);
        assert.equal(await page.locator("#ipsErr").textContent(), x("The list was changed in another window and is read again: make your change again"));
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 4);
        assert.deepEqual((await rows(page)).map((r) => r[0]), ["203.0.113.8", "2001:db8::1", "198.51.100.42", "192.0.2.77"]);
        assert.deepEqual(errors, []);
      });
    }
  }
}

// the accounts the import reads, as /api/providers says them: Claude's
// alice through a proxy at a listed IP, bob through the saved Tokyo line,
// carol (to sign in again) through her subscription's proxy, which
// doesn't answer; Codex's ops goes direct, qa (banned) through Tokyo too
const prov = (id, name, agent, logins, extra = {}) => ({ id, name, icon: id + "-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], account: { agent, agentName: name, user: logins[0]?.user, plan: logins[0]?.plan, logins }, proxy: "", ...extra });
const TOKYO = "http://me:pw@tokyo.example:3128", AT_IP = "socks5://u:s3cret@203.0.113.7:1080", HK = "http://hk:pw@hk.example:8080";
const accountProviders = () => [
  prov("claude", "Claude", "claude", [
    { user: "alice@example.com", plan: "max", active: true, on: true },
    { user: "bob@example.com", plan: "pro", on: true },
    { user: "carol@example.com", plan: "pro", lapsed: "Its sign-in was refused (401)" },
  ], { proxy: HK, accountNames: { "bob@example.com": "Work" }, accountProxies: { "alice@example.com": AT_IP, "bob@example.com": TOKYO } }),
  prov("codex", "Codex", "codex", [
    { user: "ops@example.com", plan: "pro", active: true, on: true },
    { user: "qa@example.com", plan: "plus", on: true, banned: true },
  ], { proxy: TOKYO, accountProxies: { "ops@example.com": "direct" } }),
  // a provider of keys (review H1): Main through Tokyo by its id, the
  // other on the global proxy, so through none
  { id: "relay", name: "Relay", icon: "generic", models: [], agents: [], fallback: [], headers: {}, chat: "https://relay.example/v1", responses: "", anthropic: "", catalog: "", proxy: "",
    keyList: [{ id: "k1", name: "Main", masked: "sk-…a1", on: true, active: true }, { id: "k2", masked: "sk-…b2", on: false }], accountProxies: { k1: TOKYO } },
];

const accountsOf = (page) => page.evaluate(() => Object.fromEntries([...document.querySelectorAll("#ipsList .ips-row")].map((r) => [r.dataset.ip,
  r.querySelector(".ips-none") ? "none" : [...r.querySelectorAll(".ips-acct")].map((a) => a.querySelector(".ips-acct-name").textContent + " " + a.querySelector(".acx-state").className.replace("acx-state ", "") + " " + a.querySelector(".acx-state").textContent)])));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: the accounts' proxies are imported, each IP names its accounts and is found by their state`, async (t) => {
        const seen = { posts: [], checks: [], rev: 1, providers: accountProviders(), exits: { [TOKYO]: "198.51.100.42", [AT_IP]: "203.0.113.7" }, ips: [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z" },
          { ip: "203.0.113.8" },
        ] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=ips");
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row .ips-accts").length === 2);
        const on = "on " + x("In rotation"), banned = "banned " + x("Banned");

        // before any import: the proxy at a listed IP names its account
        assert.deepEqual(await accountsOf(page), { "203.0.113.7": ["alice@example.com " + on], "203.0.113.8": "none" });
        assert.equal(await page.locator("#ipsSum").textContent(), x("{n} IPs", { n: 2 }) + " · " + x("1 bound to accounts") + " · " + x("1 to renew"));

        // imported: each proxy an account goes through is checked once,
        // direct and the global one are none; one exit is added with the
        // saved proxy's name, one is told to the IP at its address, and
        // the one that didn't answer is named without its login
        seen.hold = true;
        await page.locator("#ipsImport").click();
        // while it reads, nothing else changes the list (review M1)
        await page.waitForFunction(() => document.querySelector("#ipsImport").disabled);
        assert.equal(await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-remove').isDisabled(), true);
        assert.equal(await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-edit').isDisabled(), true);
        seen.release();
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 3 && !document.querySelector("#ipsImport").disabled);
        assert.deepEqual(seen.checks.map((c) => c.proxy).sort(), [AT_IP, HK, TOKYO].sort());
        assert.equal(seen.posts.length, 1);
        assert.deepEqual(seen.posts[0].ips, [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z", proxies: [AT_IP] },
          { ip: "203.0.113.8" },
          { ip: "198.51.100.42", name: "Tokyo line", proxies: [TOKYO] },
        ]);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("1 IP added") + " · " + x("1 proxy told to an IP listed"));
        assert.equal(await page.locator("#ipsErr").textContent(), x("These proxies didn't answer, so their exit isn't known: {list}", { list: "hk.example:8080" }));
        assert.deepEqual(await accountsOf(page), {
          "203.0.113.7": ["alice@example.com " + on],
          "203.0.113.8": "none",
          "198.51.100.42": ["Work " + on, "qa@example.com " + banned, "Relay · Main " + on],
        });
        assert.ok(await wider(page) <= 0.5, `the page reaches ${await wider(page)}px past the window`);

        // found by the accounts' state, kept as picked
        const pick = async (name) => {
          await page.locator("#ipsFilter").click();
          await page.locator(".proto-menu .pm-item", { hasText: name }).first().click();
        };
        const listed = () => page.evaluate(() => [...document.querySelectorAll("#ipsList .ips-row")].map((r) => r.dataset.ip));
        await pick(x("Not bound to an account"));
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 1);
        assert.deepEqual(await listed(), ["203.0.113.8"]);
        await pick(x("An account: {state}", { state: x("Banned") }));
        await page.waitForFunction(() => document.querySelector("#ipsList .ips-row")?.dataset.ip === "198.51.100.42");
        assert.deepEqual(await listed(), ["198.51.100.42"]);
        await pick(x("Bound to accounts"));
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 2);
        assert.deepEqual(await listed(), ["203.0.113.7", "198.51.100.42"]);
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.ipsFilter")), "bound");
        await pick(x("All IPs"));
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 3);

        // imported again: every proxy is checked again (T-172), and with
        // no exit moved nothing is saved
        const imported = async () => {
          const checked = seen.checks.length;
          await page.locator("#ipsImport").click();
          await page.waitForFunction(() => !document.querySelector("#ipsImport").disabled);
          return seen.checks.slice(checked).map((c) => c.proxy).sort();
        };
        assert.deepEqual(await imported(), [AT_IP, HK, TOKYO].sort());
        assert.equal(seen.posts.length, 1);
        assert.ok(await page.locator("#ipsSaid").isHidden());
        assert.equal(await page.locator("#ipsErr").textContent(), x("These proxies didn't answer, so their exit isn't known: {list}", { list: "hk.example:8080" }));

        // the Tokyo line's exit moved to a listed IP: imported, the proxy
        // leaves the IP it was read from for its new exit, and says so;
        // its accounts go with it (T-172)
        seen.exits[TOKYO] = "203.0.113.8";
        assert.deepEqual(await imported(), [AT_IP, HK, TOKYO].sort());
        assert.equal(seen.posts.length, 2);
        assert.deepEqual(seen.posts[1].ips, [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z", proxies: [AT_IP] },
          { ip: "203.0.113.8", proxies: [TOKYO] },
          { ip: "198.51.100.42", name: "Tokyo line" },
        ]);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("Moved to a new exit: {list}", { list: "tokyo.example:3128 (198.51.100.42 → 203.0.113.8)" }));
        assert.deepEqual(await accountsOf(page), {
          "203.0.113.7": ["alice@example.com " + on],
          "203.0.113.8": ["Work " + on, "qa@example.com " + banned, "Relay · Main " + on],
          "198.51.100.42": "none",
        });

        // an IP's form lists the proxies it was read from, and an edit
        // keeps them
        const chips = () => page.evaluate(() => [...document.querySelectorAll("#ipsList .ips-form .ips-proxy code")].map((c) => c.textContent));
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-edit').click();
        assert.deepEqual(await chips(), ["tokyo.example:3128"]);
        assert.ok(await page.locator("#ipsList .ips-moved").isHidden());
        assert.ok(await wider(page) <= 0.5, `the form reaches ${await wider(page)}px past the window`);
        await page.locator("#ipsList .ips-in-name").fill("Tokyo 2");
        await page.locator("#ipsList .ips-save").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .ips-form"));
        assert.deepEqual(seen.posts.at(-1).ips[1], { ip: "203.0.113.8", name: "Tokyo 2", proxies: [TOKYO] });

        // an address typed over says the proxies were read as the old
        // one's exit; one unlinked is gone from what is saved, and its
        // accounts with it
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-edit').click();
        await page.locator("#ipsList .ips-in-ip").fill("203.0.113.9");
        assert.equal(await page.locator("#ipsList .ips-moved").textContent(), x("The address changed: these were read as {ip}'s exit, so remove any that doesn't go out through the new one", { ip: "203.0.113.8" }));
        assert.ok(await page.locator("#ipsList .ips-moved").isVisible());
        await page.locator("#ipsList .ips-in-ip").fill("203.0.113.8");
        assert.ok(await page.locator("#ipsList .ips-moved").isHidden());
        assert.equal(await page.locator("#ipsList .ips-proxy-rm").getAttribute("aria-label"), x("Unlink {proxy}", { proxy: "tokyo.example:3128" }));
        await page.locator("#ipsList .ips-proxy-rm").click();
        assert.deepEqual(await chips(), []);
        assert.equal(await page.locator("#ipsList .ips-in-name").inputValue(), "Tokyo 2");
        await page.locator("#ipsList .ips-save").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .ips-form"));
        assert.deepEqual(seen.posts.at(-1).ips[1], { ip: "203.0.113.8", name: "Tokyo 2" });
        assert.equal((await accountsOf(page))["203.0.113.8"], "none");

        // alice's proxy, at 203.0.113.7 itself, now leaves from .8: moved,
        // her account goes with it and no longer shows at the proxy's own
        // address (review M2); carol's, answering now, is a new IP; Tokyo,
        // not answering, is named and put nowhere
        Object.assign(seen.exits, { [AT_IP]: "203.0.113.8", [HK]: "192.0.2.50", [TOKYO]: undefined });
        await imported();
        assert.deepEqual(seen.posts.at(-1).ips, [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z" },
          { ip: "203.0.113.8", name: "Tokyo 2", proxies: [AT_IP] },
          { ip: "198.51.100.42", name: "Tokyo line" },
          { ip: "192.0.2.50", name: "", proxies: [HK] },
        ]);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("1 IP added") + " · "
          + x("Moved to a new exit: {list}", { list: "203.0.113.7:1080 (203.0.113.7 → 203.0.113.8)" }));
        assert.equal(await page.locator("#ipsErr").textContent(), x("These proxies didn't answer, so their exit isn't known: {list}", { list: "tokyo.example:3128" }));
        let bound = await accountsOf(page);
        assert.equal(bound["203.0.113.7"], "none");
        assert.deepEqual(bound["203.0.113.8"], ["alice@example.com " + on]);
        assert.deepEqual(bound["192.0.2.50"].map((a) => a.split(" ")[0]), ["carol@example.com"]);

        // a proxy an IP was read from that doesn't answer stays there;
        // Tokyo, answering again, is told to the IP at its exit
        Object.assign(seen.exits, { [HK]: undefined, [TOKYO]: "198.51.100.42" });
        const posted = seen.posts.length;
        await imported();
        assert.equal(seen.posts.length, posted + 1);
        assert.deepEqual(seen.posts.at(-1).ips.slice(2), [{ ip: "198.51.100.42", name: "Tokyo line", proxies: [TOKYO] }, { ip: "192.0.2.50", proxies: [HK] }]);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("1 proxy told to an IP listed"));
        assert.equal(await page.locator("#ipsErr").textContent(), x("These proxies didn't answer, so their exit isn't known: {list}", { list: "hk.example:8080" }));

        // every one answering as listed: nothing saved, and said
        seen.exits[HK] = "192.0.2.50";
        await imported();
        assert.equal(seen.posts.length, posted + 1);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("Every proxy an account goes through is listed already"));
        assert.ok(await page.locator("#ipsErr").isHidden());
        assert.deepEqual(errors, []);
      });
    }
  }
}

// T-176: the list is found by a word (an address, a name, a proxy as
// shown, an account), by its period as the server worked it out, and by
// its accounts, each alone and together; nothing found says so, the
// filters are kept over a reload, and cleared every IP is back
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: proxy IPs are found by a word, their period and their accounts`, async (t) => {
        const seen = { posts: [], checks: [], rev: 1, providers: accountProviders(), ips: [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z" },
          { ip: "203.0.113.8", proxies: [TOKYO] },
          { ip: "2001:db8::1", name: "Old", opened: "2026-09-01T00:00:00Z" },
          { ip: "192.0.2.9", name: "Seoul", opened: "2026-11-01T00:00:00Z" },
        ] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=ips");
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row .ips-accts").length === 4);
        const listed = () => page.evaluate(() => [...document.querySelectorAll("#ipsList .ips-row")].map((r) => r.dataset.ip));
        const shows = (ips) => page.waitForFunction((ips) => [...document.querySelectorAll("#ipsList .ips-row")].map((r) => r.dataset.ip).join() === ips.join(), ips).then(listed);
        const pick = async (id, name) => {
          await page.locator(id).click();
          await page.locator(".proto-menu .pm-item", { hasText: name }).first().click();
        };
        const search = (s) => page.locator("#ipsQ").fill(s);
        const all = ["203.0.113.7", "203.0.113.8", "2001:db8::1", "192.0.2.9"];
        assert.equal(await page.locator("#ipsQ").getAttribute("placeholder"), x("Search IPs"));
        assert.ok(await page.locator("#ipsRenew").isVisible());
        assert.ok(await page.locator("#ipsFilter").isVisible());
        assert.ok(await wider(page) <= 0.5, `the tools reach ${await wider(page)}px past the window`);

        // by its period, as its pill says it
        await pick("#ipsRenew", x("No start date"));
        assert.deepEqual(await shows(["203.0.113.8"]), ["203.0.113.8"]);
        await pick("#ipsRenew", x("Renew soon"));
        assert.deepEqual(await shows(["203.0.113.7"]), ["203.0.113.7"]);
        await pick("#ipsRenew", x("Period ended"));
        assert.deepEqual(await shows(["2001:db8::1"]), ["2001:db8::1"]);
        await pick("#ipsRenew", x("In its period"));
        assert.deepEqual(await shows(["192.0.2.9"]), ["192.0.2.9"]);
        await pick("#ipsRenew", x("Start date: any"));
        await shows(all);

        // by a word: a name, an address in any case, a proxy as shown, an
        // account and its subscription; a proxy's login is never searched
        await search("seoul");
        assert.deepEqual(await shows(["192.0.2.9"]), ["192.0.2.9"]);
        await search("2001:DB8");
        assert.deepEqual(await shows(["2001:db8::1"]), ["2001:db8::1"]);
        await search("tokyo.example");
        assert.deepEqual(await shows(["203.0.113.8"]), ["203.0.113.8"]);
        await search("alice");
        assert.deepEqual(await shows(["203.0.113.7"]), ["203.0.113.7"]);
        await search("relay");
        assert.deepEqual(await shows(["203.0.113.8"]), ["203.0.113.8"]);
        await search("me:pw");
        assert.deepEqual(await shows([]), []);
        assert.equal(await page.locator("#ipsNote").textContent(), x("No IP matches the filter."));

        // together: a word, a period and the accounts narrow each other
        await search("203.0.113");
        assert.deepEqual(await shows(["203.0.113.7", "203.0.113.8"]), ["203.0.113.7", "203.0.113.8"]);
        await pick("#ipsRenew", x("No start date"));
        assert.deepEqual(await shows(["203.0.113.8"]), ["203.0.113.8"]);
        await pick("#ipsFilter", x("An account: {state}", { state: x("Banned") }));
        assert.deepEqual(await shows(["203.0.113.8"]), ["203.0.113.8"]);
        await pick("#ipsFilter", x("Not bound to an account"));
        assert.deepEqual(await shows([]), []);
        assert.equal(await page.locator("#ipsNote").textContent(), x("No IP matches the filter."));
        await pick("#ipsRenew", x("Start date: any"));
        await search("");
        assert.deepEqual(await shows(["2001:db8::1", "192.0.2.9"]), ["2001:db8::1", "192.0.2.9"]);
        await search("old");
        assert.deepEqual(await shows(["2001:db8::1"]), ["2001:db8::1"]);
        await pick("#ipsRenew", x("Period ended"));
        assert.deepEqual(await shows(["2001:db8::1"]), ["2001:db8::1"]);
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.ipsRenew")), "ended");
        assert.ok(await wider(page) <= 0.5, `the tools reach ${await wider(page)}px past the window`);

        // kept on this machine over a reload
        await page.reload();
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row .ips-accts").length === 1);
        assert.deepEqual(await listed(), ["2001:db8::1"]);
        assert.equal(await page.locator("#ipsQ").inputValue(), "old");
        assert.equal(await page.locator("#ipsRenew").textContent(), x("Period ended"));
        assert.equal(await page.locator("#ipsFilter").textContent(), x("Not bound to an account"));

        // cleared, every IP is back: Escape empties the search
        await page.locator("#ipsQ").press("Escape");
        assert.equal(await page.locator("#ipsQ").inputValue(), "");
        await pick("#ipsRenew", x("Start date: any"));
        await pick("#ipsFilter", x("All IPs"));
        await shows(all);
        assert.equal(await page.locator("#ipsNote").textContent(), x("Each IP's 30 days count from its start date. Kept on this computer only."));
        assert.deepEqual(errors, []);
      });
    }
  }
}

// T-177: an import gives an IP with no start date the earliest of its
// accounts' (accountRenewal, as /api/providers says it), keeps one the
// user told and says when its accounts' earliest is another day, leaves
// one whose accounts have none without, and run again changes nothing
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: an import gives an IP its accounts' start date, and keeps one told`, async (t) => {
        // alice (at 203.0.113.7) from 10-21 in Shanghai; bob and qa
        // through Tokyo from 11-02 and 10-26, Relay's key with none;
        // carol through HK with none
        const providers = accountProviders();
        providers[0].accountRenewal = { "alice@example.com": periodOf("2026-10-20T16:00:00Z"), "bob@example.com": periodOf("2026-11-01T16:00:00Z") };
        providers[1].accountRenewal = { "qa@example.com": periodOf("2026-10-25T16:00:00Z") };
        const seen = { posts: [], checks: [], rev: 1, providers, exits: { [TOKYO]: "203.0.113.8", [AT_IP]: "203.0.113.7", [HK]: "192.0.2.50" }, ips: [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z" },
          { ip: "203.0.113.8", proxies: [TOKYO] },
          { ip: "192.0.2.9", name: "Spare" },
        ] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=ips");
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row .ips-accts").length === 3);
        const shown = (iso) => page.evaluate((iso) => new Date(iso).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" }), iso);
        // the click marks it under way at once, so this waits for its end
        const imported = async () => {
          await page.locator("#ipsImport").click();
          await page.waitForFunction(() => !document.querySelector("#ipsImport").disabled);
        };
        const kept = async (own, theirs) => x("{ip}: its own {own}, its accounts' earliest {theirs}", { ip: "203.0.113.7", own: await shown(own), theirs: await shown(theirs) });
        const osaka = await kept("2026-10-17T04:00:00Z", "2026-10-20T16:00:00Z");

        // imported: Tokyo's IP takes qa's, the earliest of its accounts';
        // Osaka keeps its own and says alice's is another day; HK's new
        // IP and the spare one have no account with a date, so none
        await imported();
        assert.equal(seen.posts.length, 1);
        assert.deepEqual(seen.posts[0].ips, [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z", proxies: [AT_IP] },
          { ip: "203.0.113.8", opened: "2026-10-25T16:00:00Z", proxies: [TOKYO] },
          { ip: "192.0.2.9", name: "Spare" },
          { ip: "192.0.2.50", name: "", proxies: [HK] },
        ]);
        assert.equal(await page.locator("#ipsSaid").textContent(), [x("1 IP added"), x("1 proxy told to an IP listed"), x("1 IP given its accounts' start date"),
          x("These IPs keep the start date told them: {list}", { list: osaka })].join(" · "));
        assert.ok(await page.locator("#ipsErr").isHidden());
        const pill = (ip) => page.locator(`#ipsList .ips-row[data-ip="${ip}"] .renew-pill`);
        await page.waitForFunction(() => document.querySelector('#ipsList .ips-row[data-ip="203.0.113.8"] .renew-pill.ok'));
        assert.equal(await pill("192.0.2.9").getAttribute("class"), "renew-pill none");
        assert.equal(await pill("192.0.2.50").getAttribute("class"), "renew-pill none");
        assert.ok(await wider(page) <= 0.5, `the page reaches ${await wider(page)}px past the window`);

        // run again: nothing is saved, and the one told still says so
        await imported();
        assert.equal(seen.posts.length, 1);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("Every proxy an account goes through is listed already") + " · " + x("These IPs keep the start date told them: {list}", { list: osaka }));

        // a date the user picks for Tokyo's IP is theirs: an import keeps it
        await pill("203.0.113.8").click();
        await page.locator("#ipsList .renew-date").fill("2026-11-05");
        await page.locator("#ipsList .renew-save").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .renew-edit"));
        assert.equal(seen.posts.length, 2);
        assert.equal(seen.posts[1].ips[1].opened, "2026-11-04T16:00:00.000Z");
        await imported();
        assert.equal(seen.posts.length, 2);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("Every proxy an account goes through is listed already") + " · " + x("These IPs keep the start date told them: {list}", {
          list: osaka + "; " + x("{ip}: its own {own}, its accounts' earliest {theirs}", { ip: "203.0.113.8", own: await shown("2026-11-04T16:00:00.000Z"), theirs: await shown("2026-10-25T16:00:00Z") }) }));

        // carol's date told since: with no proxy new, her IP taking it is
        // saved on its own
        seen.providers[0].accountRenewal["carol@example.com"] = periodOf("2026-11-03T16:00:00Z");
        await page.reload();
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row .ips-accts").length === 4);
        await imported();
        assert.equal(seen.posts.length, 3);
        assert.equal(seen.posts[2].ips[3].opened, "2026-11-03T16:00:00Z");
        assert.deepEqual(seen.posts[2].ips.map((y) => y.opened), ["2026-10-17T04:00:00Z", "2026-11-04T16:00:00.000Z", undefined, "2026-11-03T16:00:00Z"]);
        assert.ok((await page.locator("#ipsSaid").textContent()).startsWith(x("1 IP given its accounts' start date") + " · "));
        assert.deepEqual(errors, []);
      });
    }
  }
}
