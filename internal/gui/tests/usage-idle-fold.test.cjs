// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-04): 用量概览默认只展示 5 小时有使用的账号，未使用的账号
// 默认折叠. On a card of several accounts, one with nothing used in its
// vendor's 5-hour window (0%, or a window renewed since it was read) is
// folded behind "Show N unused accounts" at the card's foot; one with
// something used, one with no 5-hour window (it can't tell), one whose
// reading failed and the one carrying the check-in switch stay in sight. A
// card of one account is never folded. The button opens them and folds them
// again, remembered across a reload. Every language, Chromium and WebKit,
// at a wide and a phone's width; no backend, the API is faked here.
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
const now = new Date().toISOString();
const later = new Date(Date.now() + 3 * 3600e3).toISOString();
const before = new Date(Date.now() - 600e3).toISOString();
const acct = (provider, name, user, windows, extra) => ({ provider, name, kind: "subscription", icon: "generic", plan: "Pro", user, readAt: now, windows, ...extra });
const five = (used, resetsAt = later) => ({ name: "5 hours", used, resetsAt });
const week = (used) => ({ name: "7 days", used, resetsAt: later });
const quotas = () => [
  acct("claude", "Claude", "busy@example.com", [five(30), week(40)]),
  acct("claude", "Claude", "idle1@example.com", [five(0), week(12)]),
  // renewed since it was read: nothing used in this one
  acct("claude", "Claude", "renewed@example.com", [five(55, before), week(20)]),
  acct("claude", "Claude", "failed@example.com", [five(0)], { error: "couldn't read" }),
  acct("claude", "Claude", "idle2@example.com", [five(0), week(0)]),
  // no 5-hour window: can't tell, so in sight
  acct("trae", "Trae", "t1@example.com", [{ name: "Daily", used: 0, resetsAt: later }]),
  acct("trae", "Trae", "t2@example.com", [{ name: "Daily", used: 10, resetsAt: later }]),
  // the check-in switch's account stays in sight though idle
  acct("workbuddy", "WorkBuddy", "w1@example.com", [five(0)], { checkins: true }),
  acct("workbuddy", "WorkBuddy", "w2@example.com", [five(0)], { checkins: true }),
  acct("workbuddy", "WorkBuddy", "w3@example.com", [five(7)], { checkins: true }),
  // every account idle: no head button that does nothing (review M3)
  acct("kimi", "Kimi", "k1@example.com", [five(0)]),
  acct("kimi", "Kimi", "k2@example.com", [five(0)]),
  // one account alone: never folded
  acct("codex", "Codex", "solo@example.com", [five(0), week(5)]),
];

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// each card: the accounts in sight and its unused-accounts button
const cards = (page) => page.evaluate(() => Object.fromEntries([...document.querySelectorAll("#subscriptionUsage > .subscription-card")].map((c) => [c.dataset.key, {
  users: [...c.querySelectorAll(".subscription-account")].filter((u) => u.offsetParent).map((u) => u.querySelector(".user")?.title || u.textContent),
  idle: c.querySelector(".quota-idle-more")?.textContent ?? null,
}])));
const has = (got, key, user) => got[key].users.some((u) => u.includes(user));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [900, 380]) {
      test(`${engine} ${lang} ${width}: accounts unused in their 5-hour window fold behind a button`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); localStorage.setItem("magpie.quotaRange", "off"); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector('#subscriptionUsage > [data-key="claude"] .subscription-account', { state: "attached" });

        let got = await cards(page);
        for (const u of ["busy@", "failed@"]) assert(has(got, "claude", u), `${u} in sight`);
        for (const u of ["idle1@", "idle2@", "renewed@"]) assert(!has(got, "claude", u), `${u} folded`);
        assert.equal(got.claude.idle, tr(lang, "Show {n} unused accounts", { n: 3 }));
        assert.equal(got.trae.users.length, 2, "no 5-hour window: both in sight");
        assert.equal(got.trae.idle, null);
        assert(has(got, "workbuddy", "w1@") && has(got, "workbuddy", "w3@"), "the check-in switch's account and the used one in sight");
        assert(!has(got, "workbuddy", "w2@"));
        assert.equal(got.workbuddy.idle, tr(lang, "Show 1 unused account"));
        assert.equal(await page.locator('#subscriptionUsage > [data-key="codex"] .quota-idle-more').count(), 0, "one account alone isn't folded");
        assert.equal(got.kimi.idle, tr(lang, "Show {n} unused accounts", { n: 2 }));
        assert.equal(await page.locator('#subscriptionUsage > [data-key="kimi"] .quota-more').count(), 0, "every account folded: no button for the ones in sight");

        // opened, then remembered across a reload
        const btn = page.locator('#subscriptionUsage > [data-key="claude"] .quota-idle-more');
        assert.equal(await btn.getAttribute("aria-expanded"), "false");
        await btn.scrollIntoViewIfNeeded();
        await btn.click();
        await page.waitForFunction(() => document.querySelector('#subscriptionUsage > [data-key="claude"] .quota-idle-more')?.getAttribute("aria-expanded") === "true");
        got = await cards(page);
        for (const u of ["busy@", "failed@", "idle1@", "idle2@", "renewed@"]) assert(has(got, "claude", u), `${u} in sight once opened`);
        assert.equal(got.claude.idle, tr(lang, "Hide unused accounts"));
        assert.equal(got.workbuddy.idle, tr(lang, "Show 1 unused account"), "another card stays as it was");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing wider than the window");
        await page.reload();
        await page.waitForSelector('#subscriptionUsage > [data-key="claude"] .quota-idle-more');
        got = await cards(page);
        assert(has(got, "claude", "idle1@"), "still open after a reload");

        // and folded again
        await page.locator('#subscriptionUsage > [data-key="claude"] .quota-idle-more').click();
        await page.waitForFunction(() => document.querySelector('#subscriptionUsage > [data-key="claude"] .quota-idle-more')?.getAttribute("aria-expanded") === "false");
        got = await cards(page);
        assert(!has(got, "claude", "idle1@"));
        assert.deepEqual(errors, []);
      });
    }
  }
}
