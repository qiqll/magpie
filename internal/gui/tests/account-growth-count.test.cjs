// Run with Node's test runner and Playwright on the module path; see README.md.
// T-134: an account whose growth policy counts the days used says so on its
// row and card in Models › Accounts ("days used"), and its title says a day
// it isn't used doesn't move it on; one the gateway doesn't serve under
// such a policy says every day counts for it, and one whose days used
// aren't read yet says every day counts till then. The numbers are the
// server's (provider.AccountGrowthOf), never worked out in the page. Every
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

// as provider.AccountGrowthOf answers: a ramp by use on its day 3 (used 4
// days, idle 5), and a switch-only Codex account under it, by its adding
const byUse = { policy: "idle", name: "By use", kind: "ramp" };
const growthOf = () => ({
  claude: {
    "new@example.com": { ...byUse, day: 3, days: 20, fiveHours: 23, week: 23, usedDays: true },
    "idle@example.com": { ...byUse, day: 8, days: 20, fiveHours: 46, week: 46, usedDaysUnknown: true },
  },
  codex: { "ops@example.com": { ...byUse, day: 8, days: 20, fiveHours: 46, week: 46, byAdded: true, calendarOnly: true } },
});
const prov = (id, name, agent, logins, growth, extra = {}) => ({ id, name, icon: id + "-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], account: { agent, agentName: name, user: logins[0].user, plan: logins[0].plan, logins }, proxy: "", accountGrowth: growth, ...extra });
const providersOf = (seen) => {
  const g = growthOf();
  return {
    providers: [
      prov("claude", "Claude", "claude", [{ user: "new@example.com", plan: "max", active: true, on: true }, { user: "idle@example.com", plan: "pro", on: true }], g.claude,
        { accountAdded: { "new@example.com": daysAgo(9), "idle@example.com": daysAgo(9) }, accountFirstUsed: { "new@example.com": daysAgo(8), "idle@example.com": daysAgo(8) }, firstUsedKnown: true }),
      prov("codex", "Codex", "codex", [{ user: "ops@example.com", plan: "pro", active: true, on: true }], g.codex, { accountAdded: { "ops@example.com": daysAgo(8) } }),
    ],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
};
const w = (name, used, trayKind) => ({ name, used, resetsAt: later, trayKind });
const quotas = [{ provider: "claude", name: "Claude", user: "new@example.com", windows: [w("5 hours", 10, "5h"), w("7 days", 20, "7d")] }]
  .concat([{ provider: "codex", name: "Codex", user: "ops@example.com", windows: [w("5 hours", 30, "5h"), w("Weekly", 50, "7d")] }]);

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providersOf(seen));
    if (url.pathname === "/api/provider/skipgrowth") {
      const b = req.postDataJSON();
      seen.posts.push(b);
      seen.skipped.add(b.account);
      return json(providersOf(seen));
    }
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

// each account's marker as drawn, null for none, and how far a marker
// reaches past the window (the row's actions at a phone's width reach
// past it on main already, apart from this)
const marks = (page, sel) => page.evaluate((sel) => Object.fromEntries([...document.querySelectorAll(sel)].map((r) => {
  const g = r.querySelector(".acx-grow");
  return [r.dataset.user, g && { text: g.textContent, title: g.title, right: g.getBoundingClientRect().right }];
})), sel);
const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#acxList .acx-grow")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);
// the markers whose words don't fit in them, cut off where the row ends
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
      test(`${engine} ${lang} ${width}: an account counted by the days used says so`, async (t) => {
        const seen = { posts: [], skipped: new Set() };
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
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 3 && document.querySelectorAll("#acxList .acx-grow").length === 3);

        const said = (...parts) => parts.join(" · ");
        const expect = {
          "new@example.com": said(x("Ramping up"), x("day {day} of {days}", { day: 3, days: 20 }), x("counting days used"), x("now {n}%", { n: 23 })),
          "idle@example.com": said(x("Ramping up"), x("day {day} of {days}", { day: 8, days: 20 }), x("now {n}%", { n: 46 })),
          "ops@example.com": said(x("Ramping up"), x("day {day} of {days} since added", { day: 8, days: 20 }), x("now {n}%", { n: 46 })),
        };
        for (const sel of ["#acxList .row.acx", "#acxList .acx-card"]) {
          if (sel.endsWith("card")) {
            await page.locator("#acxView button", { hasText: x("Card view") }).click();
            await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card").length === 3);
          }
          const m = await marks(page, sel);
          assert.deepEqual(Object.fromEntries(Object.entries(m).map(([u, g]) => [u, g && g.text])), expect);
          assert.ok(m["new@example.com"].title.includes(x("Only the days magpie served a request of it count: a day it isn't used doesn't move it on")), m["new@example.com"].title);
          assert.ok(!m["new@example.com"].title.includes(x("Its policy counts the days used, but magpie doesn't send this account's requests, so every day counts")));
          assert.ok(m["ops@example.com"].title.includes(x("Its days count from when it was added: its agent asks its vendor itself, so magpie never sees its first use")));
          assert.ok(m["ops@example.com"].title.includes(x("Its policy counts the days used, but magpie doesn't send this account's requests, so every day counts")), m["ops@example.com"].title);
          assert.ok(m["idle@example.com"].title.includes(x("Its policy counts the days used, but magpie hasn't read them from its usage yet, so every day counts till then")), m["idle@example.com"].title);
          assert.ok(!m["new@example.com"].title.includes(x("Its policy counts the days used, but magpie hasn't read them from its usage yet, so every day counts till then")));
          assert.ok(await wider(page) <= 0.5, `a marker reaches ${await wider(page)}px past the window`);
          assert.deepEqual(await cut(page), [], "every marker says all its words");
          if (process.env.SHOTS) await page.screenshot({ path: path.join(process.env.SHOTS, `used-days-${sel.endsWith("card") ? "cards" : "rows"}-${engine}-${lang}-${width}.png`), fullPage: true });
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
