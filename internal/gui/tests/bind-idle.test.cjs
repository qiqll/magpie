// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-19, T-242): how long a user bound to a provider's accounts
// (the gateway bound by user) may send it nothing before they are let go
// is set on Settings › Growth policy, and on each account's card in
// Models › Accounts as its growth policy is:
// - Settings' field is empty for the default (its 60 hours the
//   placeholder); a time typed is saved at once (settings/bind-idle), an
//   emptied one saves null, and one out of 1 to 720 is said and not sent;
// - a card says the account's own time, or Follows Settings, and its menu
//   offers Follow Settings (Settings' time named) and the hours; a pick is
//   saved with provider/accountbindidle, Follow Settings as 0;
// - nothing is wider than the window, the menu neither.
// Every language, at a narrow and a wide window; no backend, the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);
// until waits for what the faked API was sent, a state, not a time
const until = async (ok) => { for (let i = 0; !ok(); i++) { if (i > 500) throw new Error("never came"); await new Promise((r) => setTimeout(r, 10)); } };
const later = new Date(Date.now() + 2 * 3600e3).toISOString();

const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, countDays: "used", builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];

// Claude's two accounts: one with its own 12 hours, one following Settings
const providersOf = (seen) => ({
  providers: [{
    id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
    account: { agent: "claude", agentName: "Claude", user: "own@example.com", plan: "max", logins: [{ user: "own@example.com", plan: "max", active: true, on: true }, { user: "plain@example.com", plan: "pro", on: true }] },
    accountBindIdle: Object.keys(seen.idle).length ? seen.idle : undefined,
  }],
  presets: [], excluded: [], gateway: { running: true, window: true },
});
const quotas = ["own@example.com", "plain@example.com"].map((user) => ({ provider: "claude", name: "Claude", user, windows: [{ name: "5 hours", used: 20, resetsAt: later, trayKind: "5h" }] }));

function serve(lang, seen) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: seen.settings.fx });
    if (url.pathname === "/api/settings/bind-idle") {
      const b = req.postDataJSON();
      seen.posts.push(["settings", b]);
      seen.settings = { ...seen.settings, bindIdleHours: b.hours ?? undefined };
      return json(seen.settings);
    }
    if (url.pathname === "/api/settings") return json(seen.settings);
    if (url.pathname === "/api/providers") return json(providersOf(seen));
    if (url.pathname === "/api/provider/accountbindidle") {
      const b = req.postDataJSON();
      seen.posts.push(["account", b]);
      if (b.hours) seen.idle = { ...seen.idle, [b.account]: b.hours };
      else delete seen.idle[b.account];
      return json(providersOf(seen));
    }
    if (url.pathname === "/api/growth/preview") return json({ points: [{ day: 0, fiveHours: 50, week: 50 }, { day: 1, fiveHours: 100, week: 100 }], accounts: [] });
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

// the menu open: its head, each item's name and whether it is ticked, and
// how far it reaches past the window
const menu = (page) => page.evaluate(() => {
  const m = document.querySelector(".proto-menu");
  if (!m) return null;
  const r = m.getBoundingClientRect();
  return {
    head: m.querySelector(".pm-head").textContent,
    items: [...m.querySelectorAll(".pm-item")].map((b) => [b.querySelector(".pm-name").textContent, b.getAttribute("aria-checked") === "true"]),
    past: Math.max(r.right - document.documentElement.clientWidth, -r.left),
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    const hours = (n) => (n === 1 ? x("1 hour") : x("{n} hours", { n }));
    for (const width of [420, 1000]) {
      test(`${engine} ${lang} ${width}: a bound user's idle time is set in Settings and on an account's card`, async (t) => {
        const seen = {
          posts: [], idle: { "own@example.com": 12 },
          settings: {
            lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], redactWords: [], visionModels: [], imageGenModels: [],
            fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [], growthPolicyList: BUILTINS, growthInEffect: "standard", bindIdleDefault: 60, bindIdleMost: 720,
          },
        };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await watchSettingsReads(page);

        // Settings › Growth policy: empty, the default its placeholder
        await page.goto("http://magpie.test/?view=settings&tab=growth");
        await settingsDrawn(page);
        const row = page.locator("#growthList .growth-idle");
        await row.waitFor();
        assert.equal((await row.locator(".name").textContent()).trim(), x("Let go of an idle user's accounts after"));
        assert.equal((await row.locator(".sub").textContent()).trim(), x("With the gateway bound by user, a user that sends a provider nothing this long lets go of its accounts there, for other users to take, and is bound afresh on its next request. Empty is {n} hours. An account can have a time of its own on its card in Models › Accounts, which comes first.", { n: 60 }));
        const field = row.locator(".growth-idle-hours");
        assert.equal(await field.inputValue(), "");
        assert.equal(await field.getAttribute("placeholder"), "60");
        const past = await page.evaluate(() => Math.max(...[...document.querySelectorAll("#growthList .growth-idle, #growthList .growth-idle *")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);
        assert.ok(past <= 0.5, `the row reaches ${past}px past the window`);
        // a time typed is saved at once
        await field.fill("24");
        await field.press("Enter");
        await until(() => seen.posts.length === 1);
        assert.deepEqual(seen.posts[0], ["settings", { hours: 24 }]);
        // its answer in, so the "Saved" it says doesn't come over what follows
        await page.waitForFunction((said) => document.querySelector("#status.ok")?.textContent === said, x("Saved"));
        // one out of range is said, not sent, and the field put back
        await field.fill("0");
        await field.press("Enter");
        await page.waitForFunction(() => document.querySelector("#status.err"));
        assert.equal((await page.locator("#status").textContent()).trim(), x("An idle user's accounts are let go after 1 to {n} hours", { n: 720 }));
        assert.equal(await field.inputValue(), "24");
        assert.equal(seen.posts.length, 1);
        // emptied, the default again
        await field.fill("");
        await field.press("Enter");
        await until(() => seen.posts.length === 2);
        assert.deepEqual(seen.posts[1], ["settings", { hours: null }]);
        await field.fill("24");
        await field.press("Enter");
        await until(() => seen.posts.length === 3);

        // Models › Accounts, Card view: each card's own time, or Settings'
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 2);
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card .acx-idlef").length === 2);
        const facts = await page.evaluate(() => Object.fromEntries([...document.querySelectorAll("#acxList .acx-card")].map((c) => [c.dataset.user, [c.querySelector(".acx-idlef").previousElementSibling.textContent, c.querySelector(".acx-idle").textContent]])));
        assert.deepEqual(facts, { "own@example.com": [x("Idle user let go after"), hours(12)], "plain@example.com": [x("Idle user let go after"), x("Follows Settings")] });
        const wider = await page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#acxList .acx-idle")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);
        assert.ok(wider <= 0.5, `a card's time reaches ${wider}px past the window`);
        // the cards are drawn again as the usage and allowances come in, which
        // can take the button, or its menu, away under a click: it is
        // clicked again until its menu stays open
        const open = async (user) => {
          const b = page.locator(`#acxList .acx-card[data-user="${user}"] .acx-idle`);
          for (let i = 0; ; i++) {
            await b.click().catch((e) => { if (i >= 9) throw e; });
            const shown = await page.waitForFunction(() => getComputedStyle(document.querySelector(".proto-menu") || document.body).opacity === "1" && document.querySelector(".proto-menu"), null, { timeout: 1500 }).then(() => true, () => false);
            if (shown) break;
            if (i >= 9) throw new Error(`${user}'s menu never stayed open`);
          }
          return menu(page);
        };
        // pick picks the nth item of user's menu, opened again when a redraw
        // took it away before the pick was sent
        const pick = async (user, nth, posts) => {
          for (let i = 0; ; i++) {
            await page.locator(".proto-menu .pm-item").nth(nth).click({ timeout: 1500 }).catch(() => {});
            if (await until(() => seen.posts.length === posts).then(() => true, () => false)) return;
            if (i >= 4) throw new Error(`${user}'s pick never sent`);
            await open(user);
          }
        };
        const ALL = [1, 6, 12, 24, 48, 72, 168, 336, 720];
        let mu = await open("plain@example.com");
        assert.equal(mu.head, x("Let go of an idle user after"));
        assert.deepEqual(mu.items, [[x("Follow Settings ({hours})", { hours: hours(24) }), true], ...ALL.map((n) => [hours(n), false])]);
        assert.ok(mu.past <= 0.5, `the menu reaches ${mu.past}px past the window`);
        await pick("plain@example.com", 2, 4);
        assert.deepEqual(seen.posts[3], ["account", { id: "claude", account: "plain@example.com", hours: 6 }]);
        // its own ticked; Follow Settings takes it off
        await page.waitForFunction((want) => document.querySelector('#acxList .acx-card[data-user="plain@example.com"] .acx-idle')?.textContent === want, hours(6));
        mu = await open("own@example.com");
        assert.deepEqual(mu.items.filter(([, on]) => on).map(([n]) => n), [hours(12)]);
        await pick("own@example.com", 0, 5);
        assert.deepEqual(seen.posts[4], ["account", { id: "claude", account: "own@example.com", hours: 0 }]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
