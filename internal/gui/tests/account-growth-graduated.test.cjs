// Run with Node's test runner and Playwright on the module path; see README.md.
// T-150: an account whose ramp-up the calendar had done when its growth
// policy began counting the days used is marked once (provider.GraduateOnce)
// and held no more; its row and card in Models › Accounts say "Ramp-up
// done", with a title that says why, beside an account still ramping up
// that says its day as before. The mark is the server's
// (provider.AccountGraduatedOf), never worked out in the page. Every
// language, at a wide and a phone's width, nothing cut or wider than the
// window; no backend, the API is faked here. SHOTS=<dir> keeps a
// screenshot of each list.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test, after } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);
const later = new Date(Date.now() + 2 * 3600e3).toISOString();
const daysAgo = (n) => new Date(Date.now() - n * 864e5).toISOString();

// as /api/providers answers: grad@ marked done 20 days after it was added
// (no hold, so no accountGrowth), new@ ramping up by the days used
const byUse = { policy: "standard", name: "Standard", builtin: true, kind: "ramp" };
const prov = (id, name, agent, logins, extra = {}) => ({ id, name, icon: id + "-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], account: { agent, agentName: name, user: logins[0].user, plan: logins[0].plan, logins }, proxy: "", ...extra });
const providersOf = () => ({
  providers: [
    prov("claude", "Claude", "claude", [{ user: "grad@example.com", plan: "max", active: true, on: true }, { user: "new@example.com", plan: "pro", on: true }], {
      accountAdded: { "grad@example.com": daysAgo(21), "new@example.com": daysAgo(9) },
      accountFirstUsed: { "grad@example.com": daysAgo(20), "new@example.com": daysAgo(8) },
      firstUsedKnown: true,
      accountGraduated: { "grad@example.com": daysAgo(1) },
      accountGrowth: { "new@example.com": { ...byUse, day: 3, days: 14, fiveHours: 60, week: 60, usedDays: true } },
    }),
  ],
  presets: [], excluded: [], gateway: { running: true, window: true },
});
const w = (name, used, trayKind) => ({ name, used, resetsAt: later, trayKind });
const quotas = [{ provider: "claude", name: "Claude", user: "grad@example.com", windows: [w("5 hours", 10, "5h"), w("7 days", 20, "7d")] }];

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providersOf());
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json({ accounts: [] });
    if (url.pathname === "/api/accounts/keys") return json({ keys: [] });
    if (url.pathname === "/api/auth-holds") return json({ holds: [], hours: 12 });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

// each account's markers as drawn: its ramp-up done, and its ramp-up
const marks = (page, sel) => page.evaluate((sel) => Object.fromEntries([...document.querySelectorAll(sel)].map((r) => {
  const d = r.querySelector(".acx-grad"), g = r.querySelector(".acx-grow:not(.acx-grad)");
  return [r.dataset.user, { done: d && { text: d.textContent, title: d.title, tag: d.tagName }, ramp: g && g.textContent }];
})), sel);
const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#acxList .acx-grow")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);
const cut = (page) => page.evaluate(() => [...document.querySelectorAll("#acxList .acx-grow")].filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => e.textContent));

// one browser an engine for the file, each test in a context of its own:
// fewer launches for a busy machine to hang on
const browsers = {};
const browserOf = (engine) => (browsers[engine] ||= engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
after(async () => { for (const b of Object.values(browsers)) await (await b).close(); });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: an account whose ramp-up was found done says so`, async (t) => {
        const seen = {};
        const context = await (await browserOf(engine)).newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        t.after(() => context.close());
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 2 && document.querySelectorAll("#acxList .acx-grow").length === 2);

        const why = x("Its ramp-up was done by the calendar when its growth policy began counting only the days used, so the policy holds it no more");
        for (const sel of ["#acxList .row.acx", "#acxList .acx-card"]) {
          if (sel.endsWith("card")) {
            await page.locator("#acxView button", { hasText: x("Card view") }).click();
            await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card").length === 2);
          }
          const m = await marks(page, sel);
          assert.deepEqual(m["grad@example.com"], { done: { text: x("Ramped up"), title: why, tag: "SPAN" }, ramp: null }, sel);
          assert.equal(m["new@example.com"].done, null, sel);
          assert.equal(m["new@example.com"].ramp, [x("Ramping up"), x("day {day} of {days}", { day: 3, days: 14 }), x("counting days used"), x("now {n}%", { n: 60 })].join(" · "), sel);
          if (lang !== "en") assert.notEqual(x("Ramped up"), "Ramped up", `${lang} has no words for it`);
          assert.ok(await wider(page) <= 0.5, `a marker reaches ${await wider(page)}px past the window`);
          assert.deepEqual(await cut(page), [], "every marker says all its words");
          if (process.env.SHOTS) await page.screenshot({ path: path.join(process.env.SHOTS, `graduated-${sel.endsWith("card") ? "cards" : "rows"}-${engine}-${lang}-${width}.png`), fullPage: true });
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
