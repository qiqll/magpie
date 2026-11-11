// Run with Node's test runner and Playwright on the module path; see README.md.
// T-158: an account whose ramp-up the user skipped says so on its row in
// Models › Accounts (accountGrowthSkipped in /api/providers, by the
// account in lower case), and its button offers Resume ramp-up
// (provider/resumegrowth). The server's answer then holds it again, and
// the row says it ramping up. One ramped up and skipped both says Ramped
// up alone: resuming would hold it no more. Every language, at a wide and
// a phone's width; no backend, the API is faked here.
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

const standard = { policy: "standard", name: "Standard", builtin: true, kind: "ramp" };
const providersOf = (seen) => {
  const skipped = seen.resumed ? {} : { "new@example.com": daysAgo(1) };
  return {
    providers: [{
      id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
      account: { agent: "claude", agentName: "Claude", user: "new@example.com", plan: "max", logins: [
        { user: "new@example.com", plan: "max", active: true, on: true },
        { user: "fresh@example.com", plan: "pro", on: true },
        { user: "old@example.com", plan: "pro", on: true },
      ] },
      accountGrowth: {
        ...(seen.resumed ? { "new@example.com": { ...standard, day: 2, days: 14, fiveHours: 57, week: 57 } } : {}),
        "fresh@example.com": { ...standard, day: 0, days: 14, fiveHours: 50, week: 50, waiting: true },
      },
      accountGrowthSkipped: { ...skipped, "old@example.com": daysAgo(20) },
      accountGraduated: { "old@example.com": daysAgo(3) },
      accountAdded: { "new@example.com": daysAgo(9), "fresh@example.com": daysAgo(1), "old@example.com": daysAgo(40) },
      accountFirstUsed: { "new@example.com": daysAgo(2), "old@example.com": daysAgo(39) }, firstUsedKnown: true,
    }],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
};
const w = (name, used, trayKind) => ({ name, used, resetsAt: later, trayKind });
const quotas = ["new@example.com", "fresh@example.com", "old@example.com"].map((user, i) => ({ provider: "claude", name: "Claude", user, windows: [w("5 hours", [40, 0, 12][i], "5h"), w("7 days", [20, 0, 64][i], "7d")] }));

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
    if (url.pathname === "/api/provider/resumegrowth") {
      seen.posts.push(req.postDataJSON());
      seen.resumed = true;
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

// each account's markers' words, in order
const marks = (page, sel) => page.evaluate((sel) => Object.fromEntries([...document.querySelectorAll(sel)].map((r) =>
  [r.dataset.user, [...r.querySelectorAll(".acx-grow")].map((g) => g.textContent)])), sel);
const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#acxList .acx-grow")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);
const cut = (page) => page.evaluate(() => [...document.querySelectorAll("#acxList .acx-grow")].filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => e.textContent));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: a skipped ramp-up says so and is resumed`, async (t) => {
        const seen = { posts: [], resumed: false };
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
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 3 && document.querySelectorAll("#acxList .acx-grow").length === 3);

        const ramping = (...parts) => [x("Ramping up"), ...parts].join(" · ");
        assert.deepEqual(await marks(page, "#acxList .row.acx"), {
          "new@example.com": [x("Ramp-up skipped")],
          "fresh@example.com": [ramping(x("starts at first use"), x("now {n}%", { n: 50 }))],
          "old@example.com": [x("Ramped up")],
        });
        const b = page.locator('#acxList .row.acx[data-user="new@example.com"] .acx-skipped');
        assert.equal(await b.getAttribute("title"), x("Its growth policy holds none of its windows: you skipped its ramp-up. Click to resume it"));
        assert.ok(await wider(page) <= 0.5, `a marker reaches ${await wider(page)}px past the window`);
        assert.deepEqual(await cut(page), [], "every marker says all its words");

        // Resume ramp-up posts the account, and the server's answer holds it again
        await b.click();
        const item = page.locator(".proto-menu .pm-item");
        assert.equal(await item.count(), 1);
        assert.equal(await page.locator(".proto-menu .pm-head").textContent(), x("Ramp-up skipped"));
        assert.equal(await item.locator(".pm-name").textContent(), x("Resume ramp-up"));
        assert.equal(await item.locator(".pm-note").textContent(), x("Its growth policy holds it again, from the day its days count from"));
        await item.click();
        await page.waitForFunction(() => !document.querySelector("#acxList .acx-skipped"));
        assert.deepEqual(seen.posts, [{ id: "claude", account: "new@example.com" }]);
        const m = await marks(page, "#acxList .row.acx");
        assert.deepEqual(m["new@example.com"], [ramping(x("day {day} of {days}", { day: 2, days: 14 }), x("now {n}%", { n: 57 }))]);
        assert.deepEqual(m["old@example.com"], [x("Ramped up")]);

        // the card says a skip too
        seen.resumed = false;
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card").length === 3);
        await page.reload();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card .acx-skipped").length === 1);
        assert.ok(await wider(page) <= 0.5, `a card marker reaches ${await wider(page)}px past the window`);
        assert.deepEqual(await cut(page), [], "every marker says all its words");
        assert.deepEqual(errors, []);
      });
    }
  }
}
