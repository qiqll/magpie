// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-10, T-132): an account the growth policy holds says so on
// its row in Models › Accounts — ramping up, its day of the policy's days,
// the share it is held to now, a stage's next day — and its button offers
// Skip ramp-up (provider/skipgrowth). The numbers are the server's
// (provider.AccountGrowthOf, the gateway's own hold), never worked out in
// the page:
// - one not used yet says its days start at its first use;
// - one the gateway doesn't serve (Claude or Codex kept to the agent) says
//   its days count from when it was added;
// - one past its last day, or skipped, has no marker (the server sends none);
// - the row and the card both say it, nothing is wider than the window.
// Every language, at a wide and a phone's width; no backend, the API is
// faked here. SHOTS=<dir> keeps a screenshot of each list.
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
const later = new Date(Date.now() + 2 * 3600e3).toISOString();
const daysAgo = (n) => new Date(Date.now() - n * 864e5).toISOString();

// as provider.AccountGrowthOf answers: Standard's day 2, one not used yet,
// and a staged policy counting a switch-only Codex account from its adding
const standard = { policy: "standard", name: "Standard", builtin: true, kind: "ramp" };
const growthOf = (skipped) => ({
  claude: {
    ...(skipped.has("new@example.com") ? {} : { "new@example.com": { ...standard, day: 2, days: 14, fiveHours: 57, week: 57 } }),
    "fresh@example.com": { ...standard, day: 0, days: 14, fiveHours: 50, week: 50, waiting: true },
  },
  codex: {
    "ops@example.com": { policy: "mine", name: "Mine", kind: "stages", day: 4, fiveHours: 50, week: 80, next: 7, nextFiveHours: 90, nextWeek: 80, byAdded: true },
  },
});
const prov = (id, name, agent, logins, growth, extra = {}) => ({ id, name, icon: id + "-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], account: { agent, agentName: name, user: logins[0].user, plan: logins[0].plan, logins }, proxy: "", accountGrowth: growth, ...extra });
const providersOf = (seen) => {
  const g = growthOf(seen.skipped);
  return {
    providers: [
      prov("claude", "Claude", "claude", [
        { user: "new@example.com", plan: "max", active: true, on: true },
        { user: "fresh@example.com", plan: "pro", on: true },
        { user: "old@example.com", plan: "pro", on: true },
      ], g.claude, { accountAdded: { "new@example.com": daysAgo(9), "fresh@example.com": daysAgo(1), "old@example.com": daysAgo(40) }, accountFirstUsed: { "new@example.com": daysAgo(2), "old@example.com": daysAgo(39) }, firstUsedKnown: true }),
      prov("codex", "Codex", "codex", [{ user: "ops@example.com", plan: "pro", active: true, on: true }], g.codex, { accountAdded: { "ops@example.com": daysAgo(4) } }),
    ],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
};
const w = (name, used, trayKind) => ({ name, used, resetsAt: later, trayKind });
const quotas = ["new@example.com", "fresh@example.com", "old@example.com"].map((user, i) => ({ provider: "claude", name: "Claude", user, windows: [w("5 hours", [40, 0, 12][i], "5h"), w("7 days", [20, 0, 64][i], "7d")] }))
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: an account ramping up says its day and share, and skips it`, async (t) => {
        const seen = { posts: [], skipped: new Set() };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 4 && document.querySelectorAll("#acxList .acx-grow").length === 3);

        const said = (...parts) => parts.join(" · ");
        const expect = {
          "new@example.com": said(x("Ramping up"), x("day {day} of {days}", { day: 2, days: 14 }), x("now {n}%", { n: 57 })),
          "fresh@example.com": said(x("Ramping up"), x("starts at first use"), x("now {n}%", { n: 50 })),
          "old@example.com": null,
          "ops@example.com": said(x("Ramping up"), x("day {day} since added", { day: 4 }), x("now 5h {five}% · week {week}%", { five: 50, week: 80 }), x("next step on day {day}", { day: 7 })),
        };
        let m = await marks(page, "#acxList .row.acx");
        assert.deepEqual(Object.fromEntries(Object.entries(m).map(([u, g]) => [u, g && g.text])), expect);
        const ask = x("{policy} holds this new account to a share of its windows, rising to 100%. Click to skip the ramp-up", { policy: x("Standard") });
        assert.equal(m["new@example.com"].title, ask);
        assert.equal(m["fresh@example.com"].title, ask + "\n" + x("Its days start on the first request its vendor serves through magpie"));
        assert.match(m["ops@example.com"].title, /^.*Mine/);
        assert.ok(m["ops@example.com"].title.endsWith(x("Its days count from when it was added: its agent asks its vendor itself, so magpie never sees its first use")));
        assert.ok(await wider(page) <= 0.5, `a marker reaches ${await wider(page)}px past the window`);
        assert.deepEqual(await cut(page), [], "every marker says all its words");
        if (process.env.SHOTS) await page.screenshot({ path: path.join(process.env.SHOTS, `rows-${engine}-${lang}-${width}.png`), fullPage: true });

        // Skip ramp-up posts the account, and the server's answer drops its marker
        await page.locator('#acxList .row.acx[data-user="new@example.com"] .acx-grow').click();
        const item = page.locator(".proto-menu .pm-item");
        assert.equal(await item.locator(".pm-name").textContent(), x("Skip ramp-up"));
        assert.equal(await page.locator(".proto-menu .pm-head").textContent(), x("Ramping up"));
        assert.equal(await item.locator(".pm-note").textContent(), x("Its windows go to 100%, or to a cap you set on one or on the account"));
        await item.click();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-grow").length === 2);
        assert.deepEqual(seen.posts, [{ id: "claude", account: "new@example.com" }]);
        m = await marks(page, "#acxList .row.acx");
        assert.equal(m["new@example.com"], null);

        // the card says it too
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card").length === 4);
        m = await marks(page, "#acxList .acx-card");
        assert.equal(m["fresh@example.com"]?.text, expect["fresh@example.com"]);
        assert.equal(m["ops@example.com"]?.text, expect["ops@example.com"]);
        assert.ok(await wider(page) <= 0.5, `a card marker reaches ${await wider(page)}px past the window`);
        assert.deepEqual(await cut(page), [], "every marker says all its words");
        if (process.env.SHOTS) await page.screenshot({ path: path.join(process.env.SHOTS, `cards-${engine}-${lang}-${width}.png`), fullPage: true });
        assert.deepEqual(errors, []);
      });
    }
  }
}
