// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-04): 模型下面增加一个账号页面 (after CLIProxyAPI's
// credentials page). Models › Accounts lists every subscription account of
// every provider, each with its state, its windows, its last 5 hours'
// requests and its proxy (no password), and:
// - finds them by state, provider and a word (an account's own name too);
// - Use, Pause and Stop using post the editor tick's own call
//   (login/on, login/off) and the row says the new state;
// - Sign in again opens the provider's editor with that account's sign-in
//   under way; Open opens the editor;
// - the windows line up from row to row, and nothing is wider than the
//   window.
// 需求方 (2026-11-06/07): a card view with more of each account, as
// CLIProxyAPI shows it, and when it was added; filters by gateway key, by
// when it was added, and by whether its 5 hours and its week have room:
// - List and Cards switch the view, kept per machine;
// - a card says its plan, when it was added (Unknown before magpie kept
//   it), its proxy (no password), every window with its reset, and the
//   last 5 hours' requests, failures, tokens and cost;
// - a gateway key keeps the accounts /api/accounts/keys says it may use.
// Every language, Chromium and WebKit, at a wide and a phone's width; no
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
const later = new Date(Date.now() + 2 * 3600e3).toISOString();
const daysAgo = (n) => new Date(Date.now() - n * 864e5).toISOString();

const prov = (id, name, agent, logins, extra = {}) => ({ id, name, icon: id + "-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], account: { agent, agentName: name, user: logins[0]?.user, plan: logins[0]?.plan, logins }, proxy: "", ...extra });
function providersOf(seen) {
  const off = (u) => seen.off.has(u);
  return {
    providers: [
      prov("claude", "Claude", "claude", [
        { user: "alice@example.com", plan: "max", active: true, on: true, paused: off("alice@example.com") },
        { user: "bob@example.com", plan: "pro", on: true },
        { user: "carol@example.com", plan: "pro", lapsed: "Its sign-in was refused (401)" },
        { user: "Claude setup-token 3204d0", token: true, lapsed: "Its token was refused (401)" },
      ], { accountNames: { "bob@example.com": "Work" }, accountProxies: { "bob@example.com": "http://u:s3cret@10.0.0.9:3128" },
        accountAdded: { "alice@example.com": daysAgo(3), "bob@example.com": daysAgo(20) } }),
      prov("codex", "Codex", "codex", [
        { user: "ops@example.com", plan: "pro", active: true, on: true },
        { user: "qa@example.com", plan: "plus", on: seen.on.has("qa@example.com") },
      ], { accountAdded: { "ops@example.com": daysAgo(60) } }),
      // a key provider: no accounts, not listed
      { id: "openrouter", name: "OpenRouter", icon: "openrouter", models: [], agents: [], fallback: [], headers: {}, chat: "https://openrouter.ai/api/v1", responses: "", anthropic: "", catalog: "", key: { set: true, masked: "sk-or-…abcd" }, ready: true, keyList: [{ id: "k1", name: "main", masked: "sk-or-…abcd" }] },
    ],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
}
// each window as /api/usage/quotas says it, the menu bar's kind (trayKind) on it
const w = (name, used, trayKind) => ({ name, used, resetsAt: later, trayKind });
const quotas = [
  { provider: "claude", name: "Claude", user: "alice@example.com", windows: [w("5 hours", 42, "5h"), w("7 days", 18, "7d")] },
  { provider: "claude", name: "Claude", user: "bob@example.com", windows: [w("5 hours", 100, "5h"), w("7 days", 64, "7d")] },
  { provider: "claude", name: "Claude", user: "carol@example.com", error: "401" },
  { provider: "codex", name: "Codex", user: "ops@example.com", windows: [w("5 hours", 73, "5h"), w("Weekly", 100, "7d")] },
  // two weeks of one span, as Antigravity's pools: the menu bar's kinds
  // are "7d:<name>" (trayChoices); one used up, one with room
  { provider: "codex", name: "Codex", user: "qa@example.com", windows: [w("5 hours", 0, "5h"), w("Gemini · 7 days", 100, "7d:gemini · 7 days"), w("Claude · 7 days", 7, "7d:claude · 7 days")] },
];
// an account's usage as /api/usage groups it: its tokens are in and out
const usage = { accounts: [
  { provider: "claude", account: "alice@example.com", calls: 128, errors: 3, input: 900000, output: 12345, cost: 1.5 },
  { provider: "codex", account: "ops@example.com", calls: 9, errors: 0, input: 30000, output: 10000, cost: 0.2 },
] };
// the gateway keys, each with the accounts it may use (accountsKeyRoutes)
const keys = { keys: [{ id: "k-all", name: "Laptop", all: true, accounts: [] }, { id: "k-ci", name: "CI", accounts: ["claude/bob@example.com", "codex/ops@example.com"] }] };

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", proxyNow: "", proxySource: "none", trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [], visionModels: [], imageGenModels: [], workbuddyCheckins: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providersOf(seen));
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") { seen.usage.push(url.searchParams.get("period")); return json(usage); }
    if (url.pathname === "/api/accounts/keys") { seen.keys = (seen.keys || 0) + 1; return json(keys); }
    if (url.pathname === "/api/login/on" || url.pathname === "/api/login/off") {
      const b = req.postDataJSON();
      seen.posts.push([url.pathname, b]);
      if (url.pathname.endsWith("/on")) { seen.on.add(b.user); seen.off.delete(b.user); } else { seen.off.add(b.user); seen.on.delete(b.user); }
      return json(providersOf(seen));
    }
    if (url.pathname === "/api/signin") { seen.starts.push(req.postDataJSON()); return json({ id: "s1", agent: req.postDataJSON().agent, state: "waiting", url: "https://vendor.example/oauth" }); }
    if (url.pathname.startsWith("/api/signin/")) return json({ id: "s1", state: "waiting" });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

// each row: its account, its state's words, its requests, its actions
const rowsOf = (page) => page.evaluate(() => [...document.querySelectorAll("#acxList .row.acx")].map((r) => ({
  user: r.dataset.user, provider: r.dataset.provider, state: r.querySelector(".acx-state").textContent,
  sub: r.querySelector(".sub").textContent, calls: r.querySelector(".acx-calls b").textContent,
  acts: [...r.querySelectorAll(".acts .text")].map((b) => b.textContent),
  meters: r.querySelector(".acx-meter") ? Math.round(r.querySelector(".acx-meters").getBoundingClientRect().left) : null,
})));
const settle = (page) => page.waitForTimeout(150);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: Models › Accounts lists every account and acts on it`, async (t) => {
        const seen = { posts: [], starts: [], usage: [], on: new Set(), off: new Set() };
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
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 6 && document.querySelector('#acxList .row.acx[data-user="alice@example.com"] .acx-calls b')?.textContent === "128");

        // its tab, under Models after Providers
        if (width > 600) {
          const tabs = await page.$$eval("#nav > *", (xs) => xs.map((e) => e.dataset.view || "|" + e.textContent));
          assert.deepEqual(tabs.slice(tabs.indexOf("providers") - 1, tabs.indexOf("providers") + 2), ["|" + x("Models"), "providers", "accounts"]);
        }
        assert.equal(await page.locator("#pageTitle").textContent(), x("Accounts"));

        let rows = await rowsOf(page);
        assert.deepEqual(rows.map((r) => r.user), ["alice@example.com", "bob@example.com", "carol@example.com", "Claude setup-token 3204d0", "ops@example.com", "qa@example.com"], "every account, not the key provider");
        assert.deepEqual(rows.map((r) => r.state), [x("In use"), x("In use"), x("Sign in again"), x("Sign in again"), x("In use"), x("Not in use")]);
        assert.deepEqual(rows.map((r) => r.calls), ["128", "0", "0", "0", "9", "0"]);
        assert.deepEqual(seen.usage, ["last:5h"], "the requests are the last 5 hours'");
        const bob = rows.find((r) => r.user === "bob@example.com");
        assert.match(bob.sub, /10\.0\.0\.9:3128/);
        assert.doesNotMatch(bob.sub, /s3cret/, "the proxy's password isn't shown");
        // the editor tick's own action on each
        assert.deepEqual(rows.map((r) => r.acts), [
          [x("Pause"), x("Open")], [x("Stop using"), x("Open")], [x("Sign in again"), x("Open")], [x("Sign in again"), x("Open")], [x("Open")], [x("Use"), x("Open")],
        ]);
        assert.equal(await page.locator("#acxSum").textContent(), [x("{n} accounts", { n: 6 }), x("{n} in use", { n: 3 }), x("{n} to sign in again", { n: 2 })].join(" · "));
        if (width > 600) assert.equal(new Set(rows.filter((r) => r.meters != null).map((r) => r.meters)).size, 1, "the windows line up");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing wider than the window");

        // found by state, by provider and by a word (its own name too)
        await page.locator("#acxState button", { hasText: x("Sign in again") }).click();
        await settle(page);
        assert.deepEqual((await rowsOf(page)).map((r) => r.user), ["carol@example.com", "Claude setup-token 3204d0"]);
        await page.locator("#acxState button", { hasText: x("All") }).first().click();
        await page.locator("#acxProvider").click();
        await page.locator(".sess-menu .pm-item", { hasText: "Codex" }).click();
        await settle(page);
        assert.deepEqual((await rowsOf(page)).map((r) => r.user), ["ops@example.com", "qa@example.com"]);
        await page.locator("#acxProvider").click();
        await page.locator(".sess-menu .pm-item").first().click();
        await page.locator("#acxQ").fill("work");
        await settle(page);
        assert.deepEqual((await rowsOf(page)).map((r) => r.user), ["bob@example.com"]);
        await page.locator("#acxQ").fill("nobody");
        await settle(page);
        assert.equal(await page.locator("#acxNote").textContent(), x("No account matches these filters."));
        await page.locator("#acxQ").fill("");
        await settle(page);

        // Use and Pause post the tick's call, and the row says so
        await page.locator('#acxList .row.acx[data-user="qa@example.com"] .acx-use').click();
        await page.waitForFunction((s) => document.querySelector('#acxList .row.acx[data-user="qa@example.com"] .acx-state')?.textContent === s, x("In use"));
        await page.locator('#acxList .row.acx[data-user="alice@example.com"] .acx-use').click();
        await page.waitForFunction((s) => document.querySelector('#acxList .row.acx[data-user="alice@example.com"] .acx-state')?.textContent === s, x("Paused"));
        assert.deepEqual(seen.posts, [["/api/login/on", { agent: "codex", user: "qa@example.com" }], ["/api/login/off", { agent: "claude", user: "alice@example.com" }]]);
        assert.equal(await page.locator('#acxList .row.acx[data-user="alice@example.com"] .acx-use').textContent(), x("Resume"));

        // Sign in again: the editor, that account's sign-in under way
        await page.locator('#acxList .row.acx[data-user="carol@example.com"] .acx-again').click();
        for (let i = 0; i < 40 && !seen.starts.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(seen.starts.map((s) => [s.agent, s.user]), [["claude", "carol@example.com"]]);
        await page.waitForFunction(() => !document.querySelector("#view-providers").hidden && !!document.querySelector(".editor"));

        // a setup-token's: its token sheet, no OAuth sign-in (review M2)
        await page.goto("http://magpie.test/?view=accounts");
        await page.locator('#acxList .row.acx[data-user="Claude setup-token 3204d0"] .acx-again').click();
        await page.locator(".editor .signing.claude-token").waitFor();
        assert.equal(seen.starts.length, 1, "no sign-in started for it");

        // Open: the provider's editor
        await page.goto("http://magpie.test/?view=accounts");
        await page.locator('#acxList .row.acx[data-user="ops@example.com"] .acx-open').click();
        await page.waitForFunction(() => new URLSearchParams(location.search).get("edit") === "codex");
        assert.deepEqual(errors, []);
      });
    }
  }
}

// each card: its account, its state's words, its facts, its windows, its
// numbers and its actions
const cardsOf = (page) => page.evaluate(() => [...document.querySelectorAll("#acxList .acx-card")].map((c) => ({
  user: c.dataset.user, state: c.querySelector(".acx-state").textContent,
  facts: Object.fromEntries([...c.querySelectorAll(".acx-facts dd")].map((d) => [d.className, d.textContent])),
  windows: [...c.querySelectorAll(".acx-meter")].map((m) => [m.querySelector(".acx-pct").textContent, !!m.querySelector(".acx-reset")?.textContent]),
  stats: [...c.querySelectorAll(".acx-stat b")].map((b) => b.textContent),
  acts: [...c.querySelectorAll(".acts .text")].map((b) => b.textContent),
})));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: Models › Accounts shows cards and filters by key, added and room`, async (t) => {
        const seen = { posts: [], starts: [], usage: [], on: new Set(), off: new Set() };
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
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 6 && document.querySelector('#acxList .row.acx[data-user="alice@example.com"] .acx-calls b')?.textContent === "128" && !document.querySelector("#acxKey").hidden);
        assert.ok(seen.keys >= 1, "the gateway keys are read");
        // a row's tokens are its in and out (the usage has no tokens of its own)
        const tokensSaid = await page.evaluate((s) => s.replace("{n}", fmtN(912345)), x("In the last 5 hours, through the gateway: {n} tokens"));
        assert.equal(await page.locator('#acxList .row.acx[data-user="alice@example.com"] .acx-calls').getAttribute("title"), tokensSaid);

        // Cards: every account a card, kept as the view when the page opens again
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card").length === 6);
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card").length === 6 && document.querySelector('#acxList .acx-card[data-user="alice@example.com"] .acx-stat b')?.textContent === "128");
        assert.equal(await page.locator("#acxView button.on").textContent(), x("Card view"));
        const cards = await cardsOf(page);
        assert.deepEqual(cards.map((c) => c.user), ["alice@example.com", "bob@example.com", "carol@example.com", "Claude setup-token 3204d0", "ops@example.com", "qa@example.com"]);
        const card = (u) => cards.find((c) => c.user === u);
        // when it was added, Unknown for one from before magpie kept it
        assert.match(card("alice@example.com").facts["acx-added"], /\d/);
        assert.equal(card("carol@example.com").facts["acx-added"], x("Unknown"));
        assert.equal(card("qa@example.com").facts["acx-added"], x("Unknown"));
        assert.ok(card("alice@example.com").facts["acx-plan"], "its plan");
        assert.match(card("bob@example.com").facts["acx-proxy"], /10\.0\.0\.9:3128/);
        assert.doesNotMatch(card("bob@example.com").facts["acx-proxy"], /s3cret/, "the proxy's password isn't shown");
        // every window, each with its reset
        assert.deepEqual(card("alice@example.com").windows, [["42%", true], ["18%", true]]);
        assert.deepEqual(card("carol@example.com").windows, []);
        // the last 5 hours: requests, failed, tokens, cost
        const [calls, failed, tokens, cost] = card("alice@example.com").stats;
        assert.deepEqual([calls, failed, cost], ["128", "3", "$1.50"]);
        assert.equal(tokens, await page.evaluate(() => fmtN(912345)));
        assert.deepEqual(card("qa@example.com").stats.slice(0, 2), ["0", "0"]);
        // the same actions as a row's
        assert.deepEqual(cards.map((c) => c.acts), [
          [x("Pause"), x("Open")], [x("Stop using"), x("Open")], [x("Sign in again"), x("Open")], [x("Sign in again"), x("Open")], [x("Open")], [x("Use"), x("Open")],
        ]);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing wider than the window");

        // the filters: a menu's option picked by its name
        const users = async () => (await cardsOf(page)).map((c) => c.user);
        const pick = async (id, name) => {
          await page.locator(id).click();
          await page.locator(".sess-menu .pm-item").filter({ has: page.locator(".pm-name").getByText(name, { exact: true }) }).click();
          await settle(page);
        };
        await pick("#acxKey", "CI");
        assert.deepEqual(await users(), ["bob@example.com", "ops@example.com"], "the accounts the key may use");
        await pick("#acxKey", "Laptop");
        assert.equal((await users()).length, 6, "a key held to none: every account");
        await pick("#acxKey", x("Any gateway key"));
        await pick("#acxAdded", x("Added in the last 7 days"));
        assert.deepEqual(await users(), ["alice@example.com"]);
        await pick("#acxAdded", x("Added in the last 30 days"));
        assert.deepEqual(await users(), ["alice@example.com", "bob@example.com"], "the last 30 days hold the last 7");
        await pick("#acxAdded", x("Added over 30 days ago"));
        assert.deepEqual(await users(), ["ops@example.com"]);
        await pick("#acxAdded", x("Added before magpie kept it"));
        assert.deepEqual(await users(), ["carol@example.com", "Claude setup-token 3204d0", "qa@example.com"]);
        await pick("#acxAdded", x("Added any time"));
        await pick("#acx5h", x("5 hours: used up"));
        assert.deepEqual(await users(), ["bob@example.com"]);
        await pick("#acx5h", x("5 hours: room left"));
        assert.deepEqual(await users(), ["alice@example.com", "ops@example.com", "qa@example.com"], "one with its 5 hours unread is neither");
        await pick("#acx7d", x("7 days: used up"));
        assert.deepEqual(await users(), ["ops@example.com"], "Codex's Weekly is its week");
        await pick("#acx7d", x("7 days: room left"));
        assert.deepEqual(await users(), ["alice@example.com", "qa@example.com"], "a pool with room is room: qa's Claude week");
        // the filters are kept as the page opens again, the list's view too
        await page.locator("#acxView button", { hasText: x("List view") }).click();
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 2);
        assert.deepEqual(await page.$$eval("#acxList .row.acx", (rs) => rs.map((r) => r.dataset.user)), ["alice@example.com", "qa@example.com"]);
        await pick("#acx5h", x("5 hours: any"));
        await pick("#acx7d", x("7 days: any"));
        assert.equal(await page.locator("#acxList .row.acx").count(), 6);

        // Use on a card posts the tick's call
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.locator('#acxList .acx-card[data-user="qa@example.com"] .acx-use').click();
        await page.waitForFunction((s) => document.querySelector('#acxList .acx-card[data-user="qa@example.com"] .acx-state')?.textContent === s, x("In use"));
        assert.deepEqual(seen.posts, [["/api/login/on", { agent: "codex", user: "qa@example.com" }]]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
