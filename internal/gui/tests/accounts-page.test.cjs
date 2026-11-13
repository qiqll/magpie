// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-04): 模型下面增加一个账号页面 (after CLIProxyAPI's
// credentials page). Models › Accounts lists every subscription account of
// every provider, each with its state, its windows, its last 5 hours'
// requests and its proxy (no password), and:
// - finds them by state, provider and a word (an account's own name too);
// - Add to rotation, Pause and Take out of rotation post the editor tick's
//   own call (2026-11-09: renamed from Use and Stop using, apart from Disable)
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
// 需求方 (2026-11-09): a card says when the account was first used (or
//   Never used), the day the growth policy counts from.
// 需求方 (2026-11-08): an account its vendor banned is marked so by hand:
// - Status filters it out by default (Normal), Banned, Disabled and All
//   show it, kept in magpie.acxFilter, a word finds it then;
// - one the user disabled is held the same, said Disabled, with Disable and
//   Enable; one with both marks says both and stays out until both go;
// - its badge says Banned, Mark banned and Lift ban post login/ban and
//   login/unban;
// - the provider's editor leaves it out of its accounts, folded under
//   them as "1 banned account · Show", opened to Lift ban or Remove;
// - the Switch accounts only menu doesn't offer it.
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
        accountAdded: { "alice@example.com": daysAgo(3), "bob@example.com": daysAgo(20) }, accountFirstUsed: { "alice@example.com": daysAgo(2) }, firstUsedKnown: true }),
      prov("codex", "Codex", "codex", [
        { user: "ops@example.com", plan: "pro", active: true, on: true },
        ...(seen.holds ? [{ user: "gus@example.com", plan: "pro", on: true, authFailed: !seen.lifted }] : []),
        { user: "qa@example.com", plan: "plus", on: seen.on.has("qa@example.com"), banned: seen.banned?.has("qa@example.com"), disabled: seen.disabled?.has("qa@example.com") },
        ...(seen.banned ? [
          { user: "dave@example.com", plan: "pro", on: true, banned: seen.banned.has("dave@example.com"), disabled: seen.disabled.has("dave@example.com") },
          { user: "erin@example.com", plan: "pro", on: true, banned: seen.banned.has("erin@example.com"), disabled: seen.disabled.has("erin@example.com") },
          { user: "frank@example.com", plan: "pro", on: true, banned: seen.banned.has("frank@example.com"), disabled: seen.disabled.has("frank@example.com") },
        ] : []),
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
  // a count in credits and a row of text, beside its 5 hours and week: a
  // card shows each under its kind (需求方 2026-11-13), a row the two bars
  { provider: "codex", name: "Codex", user: "ops@example.com", windows: [w("5 hours", 73, "5h"), w("Weekly", 100, "7d"), { name: "Credits", used: 24, amount: 120, limit: 500, unit: "credits", resetsAt: later }, { name: "Extra usage", used: 0, text: true, display: "$3.20 spent" }] },
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
    if (url.pathname === "/api/auth-holds" || url.pathname.startsWith("/api/auth-holds/")) {
      if (url.pathname.endsWith("/release")) { seen.posts.push([url.pathname, req.postDataJSON()]); seen.holds = []; }
      if (url.pathname.endsWith("/hours")) { seen.posts.push([url.pathname, req.postDataJSON()]); seen.hours = req.postDataJSON().hours; }
      return json({ holds: seen.holds || [], hours: seen.hours ?? 12 });
    }
    if (url.pathname === "/api/login/lift") {
      seen.posts.push([url.pathname, req.postDataJSON()]);
      seen.lifted = true;
      return json(providersOf(seen));
    }
    if (url.pathname === "/api/accounts/keys") { seen.keys = (seen.keys || 0) + 1; return json(keys); }
    if (url.pathname === "/api/login/on" || url.pathname === "/api/login/off") {
      const b = req.postDataJSON();
      seen.posts.push([url.pathname, b]);
      if (url.pathname.endsWith("/on")) { seen.on.add(b.user); seen.off.delete(b.user); } else { seen.off.add(b.user); seen.on.delete(b.user); }
      return json(providersOf(seen));
    }
    if (url.pathname === "/api/login/disable" || url.pathname === "/api/login/enable") {
      const b = req.postDataJSON();
      seen.posts.push([url.pathname, b]);
      url.pathname.endsWith("/disable") ? seen.disabled.add(b.user) : seen.disabled.delete(b.user);
      return json(providersOf(seen));
    }
    if (url.pathname === "/api/login/ban" || url.pathname === "/api/login/unban") {
      const b = req.postDataJSON();
      seen.posts.push([url.pathname, b]);
      url.pathname.endsWith("/ban") ? seen.banned.add(b.user) : seen.banned.delete(b.user);
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
// every action button that reaches past either side of its row or card,
// or of the window, with how far (T-142 at 380px, T-146 in cards at 1000px)
const actsOut = (page, sel) => page.evaluate((sel) => [...document.querySelectorAll(sel)].flatMap((box) => {
  const b = box.getBoundingClientRect(), left = Math.max(b.left, 0), right = Math.min(b.right, document.documentElement.clientWidth);
  return [...box.querySelectorAll(".acts > *")].map((x) => {
    const r = x.getBoundingClientRect();
    return [box.dataset.user + " " + x.textContent, Math.round(Math.max(left - r.left, r.right - right))];
  }).filter(([, d]) => d > 0);
}), sel);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    // 700: over the narrow rule, where the actions' measured width holds them (review M1 of T-142)
    for (const width of [1000, 700, 380]) {
      test(`${engine} ${lang} ${width}: Models › Accounts lists every account and acts on it`, async (t) => {
        const seen = { posts: [], starts: [], usage: [], on: new Set(), off: new Set() };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 6 && document.querySelector('#acxList .row.acx[data-user="alice@example.com"] .acx-calls b')?.textContent === "128");

        // its tab heads Resources, then IPs (T-171)
        if (width > 600) {
          const tabs = await page.$$eval("#nav > *", (xs) => xs.map((e) => e.dataset.view || "|" + e.textContent));
          assert.deepEqual(tabs.slice(tabs.indexOf("accounts") - 1, tabs.indexOf("accounts") + 2), ["|" + x("Resources"), "accounts", "ips"]);
        }
        assert.equal(await page.locator("#pageTitle").textContent(), x("Accounts"));

        let rows = await rowsOf(page);
        assert.deepEqual(rows.map((r) => r.user), ["alice@example.com", "bob@example.com", "carol@example.com", "Claude setup-token 3204d0", "ops@example.com", "qa@example.com"], "every account, not the key provider");
        assert.deepEqual(rows.map((r) => r.state), [x("In rotation"), x("In rotation"), x("Sign in again"), x("Sign in again"), x("In rotation"), x("Not in rotation")]);
        assert.deepEqual(rows.map((r) => r.calls), ["128", "0", "0", "0", "9", "0"]);
        assert.deepEqual(seen.usage, ["last:5h"], "the requests are the last 5 hours'");
        const bob = rows.find((r) => r.user === "bob@example.com");
        assert.match(bob.sub, /10\.0\.0\.9:3128/);
        assert.doesNotMatch(bob.sub, /s3cret/, "the proxy's password isn't shown");
        // the editor tick's own action on each
        assert.deepEqual(rows.map((r) => r.acts), [
          [x("Pause"), x("Mark banned"), x("Disable"), x("Open")], [x("Take out of rotation"), x("Mark banned"), x("Disable"), x("Open")], [x("Sign in again"), x("Mark banned"), x("Disable"), x("Open")], [x("Sign in again"), x("Mark banned"), x("Disable"), x("Open")], [x("Mark banned"), x("Disable"), x("Open")], [x("Add to rotation"), x("Mark banned"), x("Disable"), x("Open")],
        ]);
        // Take out of rotation says it stays signed in and read, unlike Disable;
        // Pause says nothing of it; Not in rotation's badge says it is still read
        const tip = (u, sel) => page.locator(`#acxList .row.acx[data-user="${u}"] ${sel}`).getAttribute("title");
        assert.equal(await tip("bob@example.com", ".acx-use"), x("Take it out of the gateway's rotation: it stays signed in and its usage is still read, unlike Disable"));
        assert.equal(await tip("alice@example.com", ".acx-use"), null, "Pause keeps no rotation tooltip");
        assert.equal(await tip("qa@example.com", ".acx-state"), x("Not in rotation: still kept signed in and its usage read"));
        assert.equal(await tip("ops@example.com", ".acx-state"), null);
        assert.equal(await page.locator("#acxSum").textContent(), [x("{n} accounts", { n: 6 }), x("{n} in rotation", { n: 3 }), x("{n} to sign in again", { n: 2 })].join(" · "));
        if (width > 600) assert.equal(new Set(rows.filter((r) => r.meters != null).map((r) => r.meters)).size, 1, "the windows line up");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing wider than the window");
        assert.deepEqual(await actsOut(page, "#acxList .row.acx"), [], "every row's actions inside its row and the window");

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

        // Add to rotation and Pause post the tick's call, and the row says so
        await page.locator('#acxList .row.acx[data-user="qa@example.com"] .acx-use').click();
        await page.waitForFunction((s) => document.querySelector('#acxList .row.acx[data-user="qa@example.com"] .acx-state')?.textContent === s, x("In rotation"));
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
  kinds: [...c.querySelectorAll(".acx-qg")].map((g) => [g.dataset.kind, g.querySelectorAll(".acx-meter").length, g.querySelectorAll(".acx-qtext").length]),
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
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
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
        // its first use, the growth policy's day 0; one added and not used yet says so,
        // one from before magpie kept the day says nothing of it
        assert.match(card("alice@example.com").facts["acx-used"], /\d/);
        assert.equal(card("bob@example.com").facts["acx-used"], x("Never used"));
        assert.equal(card("qa@example.com").facts["acx-used"], undefined);
        // Codex's ledger not read yet (no firstUsedKnown): ops@, added, says nothing of it
        assert.equal(card("ops@example.com").facts["acx-used"], undefined);
        assert.ok(card("alice@example.com").facts["acx-plan"], "its plan");
        assert.match(card("bob@example.com").facts["acx-proxy"], /10\.0\.0\.9:3128/);
        assert.doesNotMatch(card("bob@example.com").facts["acx-proxy"], /s3cret/, "the proxy's password isn't shown");
        // every window, each with its reset
        assert.deepEqual(card("alice@example.com").windows, [["42%", true], ["18%", true]]);
        assert.deepEqual(card("carol@example.com").windows, []);
        // each window under its kind: 5 hours, weekly (a pool's week too),
        // a count in credits with its count, a row of text as its text
        assert.deepEqual(card("alice@example.com").kinds, [["5h", 1, 0], ["7d", 1, 0]]);
        assert.deepEqual(card("qa@example.com").kinds, [["5h", 1, 0], ["7d", 2, 0]]);
        assert.deepEqual(card("ops@example.com").kinds, [["5h", 1, 0], ["7d", 1, 0], ["credit", 1, 0], ["other", 0, 1]]);
        assert.deepEqual(card("carol@example.com").kinds, []);
        const ops = page.locator('#acxList .acx-card[data-user="ops@example.com"]');
        assert.match(await ops.locator('.acx-qg[data-kind="credit"] .acx-reset').textContent(), new RegExp("^" + (await page.evaluate(() => fmtN(120) + " / " + fmtN(500))) + " credits"));
        assert.equal(await ops.locator('.acx-qg[data-kind="other"] .acx-qv').textContent(), "$3.20 spent");
        // the last 5 hours: requests, failed, tokens, cost
        const [calls, failed, tokens, cost] = card("alice@example.com").stats;
        assert.deepEqual([calls, failed, cost], ["128", "3", "$1.50"]);
        assert.equal(tokens, await page.evaluate(() => fmtN(912345)));
        assert.deepEqual(card("qa@example.com").stats.slice(0, 2), ["0", "0"]);
        // the same actions as a row's
        assert.deepEqual(cards.map((c) => c.acts), [
          [x("Pause"), x("Mark banned"), x("Disable"), x("Open")], [x("Take out of rotation"), x("Mark banned"), x("Disable"), x("Open")], [x("Sign in again"), x("Mark banned"), x("Disable"), x("Open")], [x("Sign in again"), x("Mark banned"), x("Disable"), x("Open")], [x("Mark banned"), x("Disable"), x("Open")], [x("Add to rotation"), x("Mark banned"), x("Disable"), x("Open")],
        ]);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing wider than the window");
        assert.deepEqual(await actsOut(page, "#acxList .acx-card"), [], "every card's actions inside its card, none in the next one's");

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

        // Add to rotation on a card posts the tick's call
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.locator('#acxList .acx-card[data-user="qa@example.com"] .acx-use').click();
        await page.waitForFunction((s) => document.querySelector('#acxList .acx-card[data-user="qa@example.com"] .acx-state')?.textContent === s, x("In rotation"));
        assert.deepEqual(seen.posts, [["/api/login/on", { agent: "codex", user: "qa@example.com" }]]);
        assert.deepEqual(errors, []);
      });
    }
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: an account marked banned is filtered out, folded in the editor and not switched to`, async (t) => {
        const seen = { posts: [], starts: [], usage: [], on: new Set(), off: new Set(), banned: new Set(["dave@example.com", "frank@example.com"]), disabled: new Set(["erin@example.com", "frank@example.com"]) };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 6);
        const users = async () => (await rowsOf(page)).map((r) => r.user);
        const pick = async (id, name) => {
          await page.locator(id).click();
          await page.locator(".sess-menu .pm-item").filter({ has: page.locator(".pm-name").getByText(name, { exact: true }) }).click();
          await settle(page);
        };
        // left out by default, said in the sum
        assert.ok(!(await users()).includes("dave@example.com"), "a banned one is left out by default");
        assert.equal(await page.locator("#acxBanned").textContent(), x("Normal"));
        assert.ok(!(await users()).some((u) => /erin|frank/.test(u)), "neither banned nor disabled ones by default");
        const sumSaid = await page.locator("#acxSum").textContent();
        assert.ok(sumSaid.includes(x("{n} banned", { n: 2 })) && sumSaid.includes(x("{n} disabled", { n: 2 })), sumSaid);
        await page.locator("#acxQ").fill("dave");
        await settle(page);
        assert.deepEqual(await users(), [], "not found while left out");
        // Banned only: it, its badge, its actions, found by a word
        await pick("#acxBanned", x("Banned"));
        let rows = await rowsOf(page);
        assert.deepEqual(rows.map((r) => [r.user, r.state]), [["dave@example.com", x("Banned")]]);
        assert.deepEqual(rows[0].acts, [x("Lift ban"), x("Disable"), x("Open")], "no Use, no Sign in again");
        await page.locator("#acxQ").fill("");
        await settle(page);
        assert.deepEqual(await users(), ["dave@example.com", "frank@example.com"]);
        // one with both marks says both
        const badges = await page.$$eval('#acxList .row.acx[data-user="frank@example.com"] .acx-state', (bs) => bs.map((b) => b.textContent));
        assert.deepEqual(badges, [x("Banned"), x("Disabled")]);
        // Disabled: those turned off, the one with both too
        await pick("#acxBanned", x("Disabled"));
        rows = await rowsOf(page);
        assert.deepEqual(rows.map((r) => [r.user, r.state]), [["erin@example.com", x("Disabled")], ["frank@example.com", x("Banned")]]);
        assert.deepEqual(rows[0].acts, [x("Mark banned"), x("Enable"), x("Open")]);
        // kept as the page opens again
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 2);
        assert.equal(JSON.parse(await page.evaluate(() => localStorage.getItem("magpie.acxFilter"))).b, "disabled");
        // All: every one
        await pick("#acxBanned", x("All"));
        assert.equal((await users()).length, 9);
        // Mark banned and Lift ban post their calls
        await page.locator('#acxList .row.acx[data-user="qa@example.com"] .acx-ban').click();
        await page.waitForFunction((s) => document.querySelector('#acxList .row.acx[data-user="qa@example.com"] .acx-state')?.textContent === s, x("Banned"));
        await page.locator('#acxList .row.acx[data-user="dave@example.com"] .acx-ban').click();
        await page.waitForFunction((s) => document.querySelector('#acxList .row.acx[data-user="dave@example.com"] .acx-state')?.textContent === s, x("In rotation"));
        // Enable on one disabled, and on one with both: that one stays out, banned
        await page.locator('#acxList .row.acx[data-user="erin@example.com"] .acx-disable').click();
        await page.waitForFunction((s) => document.querySelector('#acxList .row.acx[data-user="erin@example.com"] .acx-state')?.textContent === s, x("In rotation"));
        await page.locator('#acxList .row.acx[data-user="frank@example.com"] .acx-disable').click();
        await page.waitForFunction((s) => document.querySelectorAll('#acxList .row.acx[data-user="frank@example.com"] .acx-state').length === 1 && document.querySelector('#acxList .row.acx[data-user="frank@example.com"] .acx-state').textContent === s, x("Banned"));
        assert.deepEqual(seen.posts, [["/api/login/ban", { agent: "codex", user: "qa@example.com" }], ["/api/login/unban", { agent: "codex", user: "dave@example.com" }],
          ["/api/login/enable", { agent: "codex", user: "erin@example.com" }], ["/api/login/enable", { agent: "codex", user: "frank@example.com" }]]);
        seen.disabled.add("frank@example.com"); // both marks again, for the editor
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing wider than the window");

        // the editor: qa folded under the accounts, opened to lift or remove
        await page.goto("http://magpie.test/?view=providers&edit=codex");
        await page.locator(".editor .acc-banned-head").waitFor();
        const listed = await page.$$eval(".editor .accts > .acc", (rs) => rs.map((r) => r.dataset.accountId));
        assert.ok(!listed.includes("qa@example.com") && !listed.includes("frank@example.com") && listed.includes("dave@example.com"), "a banned or disabled one is among the accounts: " + listed);
        assert.equal(await page.locator(".editor .acc-banned-head").textContent(), x("{n} banned accounts", { n: 2 }) + " · " + x("Show"));
        await page.locator(".editor .acc-banned-head").click();
        const both = page.locator('.editor .acc-banned-row[data-account-id="frank@example.com"]');
        await both.waitFor();
        assert.deepEqual(await both.locator(".using").allTextContents(), [x("Banned"), x("Disabled")]);
        assert.deepEqual(await both.locator(".text").allTextContents(), [x("Lift ban"), x("Enable"), x("Remove")]);
        const row = page.locator('.editor .acc-banned-row[data-account-id="qa@example.com"]');
        assert.deepEqual(await row.locator(".text").allTextContents(), [x("Lift ban"), x("Remove")]);
        await row.locator(".acc-ban").click();
        await page.waitForFunction((s) => document.querySelector(".editor .acc-banned-head")?.textContent.startsWith(s), x("1 banned account"));
        assert.deepEqual(seen.posts.at(-1), ["/api/login/unban", { agent: "codex", user: "qa@example.com" }]);
        // only disabled: the fold says so
        seen.banned.delete("frank@example.com");
        await page.goto("http://magpie.test/?view=providers&edit=codex");
        await page.locator(".editor .acc-banned-head").waitFor();
        assert.equal(await page.locator(".editor .acc-banned-head").textContent(), x("1 disabled account") + " · " + x("Show"));

        // Switch accounts only: neither a banned nor a disabled one is offered
        seen.banned.add("qa@example.com");
        await page.evaluate(() => loadProviders());
        const offered = await page.evaluate(async () => {
          const e = switchEntry({ id: "codex", name: "Codex" });
          document.body.append(e.button);
          e.button.click();
          await new Promise((r) => setTimeout(r, 100));
          const names = [...document.querySelectorAll(".sess-menu .pm-name")].map((n) => n.textContent);
          closeProtoMenu();
          e.button.remove();
          return names;
        });
        assert.ok(offered.includes("ops@example.com") && offered.includes("dave@example.com") && !offered.includes("qa@example.com") && !offered.includes("frank@example.com"), "offered " + offered);
        assert.deepEqual(errors, []);
      });
    }
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: callers held after authentication_failed are listed and released, and the account it came from says so`, async (t) => {
        const until = new Date(Date.now() + 12 * 3600e3).toISOString();
        const seen = { posts: [], starts: [], usage: [], on: new Set(), off: new Set(),
          holds: [{ kind: "user", who: "bob", holder: "user:bob", at: new Date().toISOString(), until, provider: "codex", account: "gus@example.com" }] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=accounts");
        await page.locator("#acxHeld .acx-held-row").waitFor();
        // the held caller, its end, and the hold's length
        assert.equal(await page.locator("#acxHeld .acx-held-head > span").first().textContent(), x("1 caller held after authentication_failed"));
        assert.equal(await page.locator("#acxHeld .acx-held-who").textContent(), "bob");
        assert.equal(await page.locator("#acxHeld .acx-hold-hours").textContent(), x("Held {n} hours", { n: 12 }));
        // the account it came from is shown, said to be refused, with Use again and Mark banned
        const row = page.locator('#acxList .row.acx[data-user="gus@example.com"]');
        await row.waitFor();
        assert.equal(await row.locator(".acx-state").textContent(), x("Sign-in refused · may be banned"));
        assert.deepEqual(await row.locator(".acts .text").allTextContents(), [x("Use again"), x("Mark banned"), x("Disable"), x("Open")]);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "nothing wider than the window");
        // the hold's length picked: posted
        await page.locator("#acxHeld .acx-hold-hours").click();
        await page.locator(".sess-menu .pm-item").filter({ has: page.locator(".pm-name").getByText(x("Hold nobody"), { exact: true }) }).click();
        await page.waitForFunction((s) => document.querySelector("#acxHeld .acx-hold-hours")?.textContent === s, x("Hold nobody"));
        // released: gone from the line
        await page.locator("#acxHeld .acx-release").click();
        await page.waitForFunction(() => !document.querySelector("#acxHeld .acx-held-row"));
        // used again: in rotation once more
        await row.locator(".acx-lift").click();
        await page.waitForFunction((s) => document.querySelector('#acxList .row.acx[data-user="gus@example.com"] .acx-state')?.textContent === s, x("In rotation"));
        assert.deepEqual(seen.posts, [["/api/auth-holds/hours", { hours: 0 }], ["/api/auth-holds/release", { holder: "user:bob" }], ["/api/login/lift", { agent: "codex", user: "gus@example.com" }]]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
