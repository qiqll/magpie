// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-10, T-133): an account picks its own growth policy on its
// row in Models › Accounts — Follow the subscription (its subscription's
// policy named) or one of the policies Settings › Growth policy lists —
// saved at once (provider/accountgrowthpolicy). The account's pick comes
// before its subscription's, which comes before the default:
// - an account ramping up opens the policies and Skip ramp-up from its
//   marker, its pick ticked, Follow the subscription for none;
// - one the policy holds none of the windows of (Off picked) still says its
//   pick, so it can be picked away again;
// - one whose pick is gone ticks nothing and says so, and Follow the
//   subscription saves, so the id gone is cleared;
// - a card says its Growth policy as a fact, which opens the same picks;
// - nothing is wider than the window, the menu neither.
// Every language, at a wide and a phone's width; no backend, the API is
// faked here. SHOTS=<dir> keeps a screenshot of each list and menu.
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

const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, builtin: true },
  { id: "gentle", name: "Gentle", kind: "ramp", start: 70, days: 7, builtin: true },
  { id: "cautious", name: "Cautious", kind: "ramp", start: 30, days: 30, builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];
const SLOW = { id: "slow", name: "Slow", kind: "ramp", start: 20, days: 60 };
const ramp = (policy, name, builtin, day, days, n) => ({ policy, name, builtin, kind: "ramp", day, days, fiveHours: n, week: n });

// Claude follows the default, Standard; Codex picked Gentle. Claude's
// new@ has no pick of its own, fresh@ picked Slow, old@ picked Off (no
// ramp-up marker then); Codex's ops@ picked a policy gone since.
const providersOf = (seen) => {
  const picks = seen.picks;
  const growth = (id, user, g) => (picks[id]?.[user] === "off" ? {} : { [user]: g });
  const prov = (id, name, agent, logins, extra) => ({ id, name, icon: id + "-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], account: { agent, agentName: name, user: logins[0].user, plan: logins[0].plan, logins }, proxy: "", accountGrowthPolicy: Object.keys(picks[id] || {}).length ? picks[id] : undefined, ...extra });
  return {
    providers: [
      prov("claude", "Claude", "claude", [
        { user: "new@example.com", plan: "max", active: true, on: true },
        { user: "fresh@example.com", plan: "pro", on: true },
        { user: "old@example.com", plan: "pro", on: true },
      ], {
        accountGrowth: { ...growth("claude", "new@example.com", ramp("standard", "Standard", true, 2, 14, 57)), ...growth("claude", "fresh@example.com", ramp("slow", "Slow", false, 2, 60, 21)) },
        accountAdded: { "new@example.com": daysAgo(3), "fresh@example.com": daysAgo(3), "old@example.com": daysAgo(3) }, firstUsedKnown: true,
        accountFirstUsed: { "new@example.com": daysAgo(2), "fresh@example.com": daysAgo(2), "old@example.com": daysAgo(2) },
      }),
      prov("codex", "Codex", "codex", [{ user: "ops@example.com", plan: "pro", active: true, on: true }], {
        growthPolicy: "gentle", accountGrowth: growth("codex", "ops@example.com", ramp("gentle", "Gentle", true, 1, 7, 74)), accountAdded: { "ops@example.com": daysAgo(1) },
      }),
    ],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
};
const w = (name, used, trayKind) => ({ name, used, resetsAt: later, trayKind });
const quotas = ["new@example.com", "fresh@example.com", "old@example.com"].map((user) => ({ provider: "claude", name: "Claude", user, windows: [w("5 hours", 20, "5h"), w("7 days", 10, "7d")] }))
  .concat([{ provider: "codex", name: "Codex", user: "ops@example.com", windows: [w("5 hours", 30, "5h"), w("Weekly", 50, "7d")] }]);

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [], growthPolicyList: [...BUILTINS, SLOW], growthInEffect: "standard" };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providersOf(seen));
    if (url.pathname === "/api/provider/accountgrowthpolicy") {
      const b = req.postDataJSON();
      seen.posts.push(b);
      const m = { ...(seen.picks[b.id] || {}) };
      if (b.growthPolicy) m[b.account] = b.growthPolicy;
      else delete m[b.account];
      seen.picks[b.id] = m;
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
const pill = (page, sel) => page.evaluate((sel) => Object.fromEntries([...document.querySelectorAll(sel)].map((r) => {
  const g = r.querySelector(".name .acx-grow");
  return [r.dataset.user, g && { text: g.textContent, title: g.title }];
})), sel);
const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#acxList .acx-grow")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: an account picks its own growth policy on its row and card`, async (t) => {
        const seen = { posts: [], picks: { claude: { "fresh@example.com": "slow", "old@example.com": "off" }, codex: { "ops@example.com": "deleted" } } };
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
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 4 && document.querySelectorAll("#acxList .acx-grow").length === 4);
        // a menu open is shot as the window shows it, once it has faded in
        const shot = async (name) => {
          if (!process.env.SHOTS) return;
          if (name === "menu") await page.waitForFunction(() => getComputedStyle(document.querySelector(".proto-menu")).opacity === "1");
          await page.screenshot({ path: path.join(process.env.SHOTS, `${name}-${engine}-${lang}-${width}.png`), fullPage: name !== "menu" });
        };

        // Off picked: no ramp-up, its pick said
        let m = await pill(page, "#acxList .row.acx");
        assert.equal(m["old@example.com"].text, x("Growth policy: {name}", { name: x("Off") }));
        assert.equal(m["old@example.com"].title, x("Picked for this account alone, before its subscription's. Click to pick another"));
        assert.ok(m["new@example.com"].title.endsWith(x("Its growth policy can be picked here too, for this account alone")));
        assert.ok(m["ops@example.com"].title.endsWith(x("The policy it picked ({id}) is gone: it is held by its subscription's until you pick another", { id: "deleted" })));
        assert.ok(await wider(page) <= 0.5, `a marker reaches ${await wider(page)}px past the window`);
        await shot("rows");

        // ramping up, no pick: the policies, Follow the subscription ticked, then Skip
        // a marker is opened from the middle of the window and its menu read
        // once faded in: one at the window's edge (ops@ at 380px) can leave
        // a fixed menu past it under load
        const open = async (sel) => {
          await page.locator(sel).evaluate((e) => e.scrollIntoView({ block: "center" }));
          await page.locator(sel).click();
          await page.waitForFunction(() => getComputedStyle(document.querySelector(".proto-menu")).opacity === "1");
          return menu(page);
        };
        const names = [...BUILTINS.map((p) => x(p.name)), "Slow"];
        let mu = await open('#acxList .row.acx[data-user="new@example.com"] .acx-grow');
        assert.equal(mu.head, x("Growth policy this account is held by"));
        assert.deepEqual(mu.items, [[x("Follow the subscription ({name})", { name: x("Standard") }), true], ...names.map((n) => [n, false]), [x("Skip ramp-up"), false]]);
        assert.ok(mu.past <= 0.5, `the menu reaches ${mu.past}px past the window`);
        await shot("menu");
        await page.locator(".proto-menu .pm-item", { hasText: x("Cautious") }).click();
        await page.waitForFunction(() => !document.querySelector(".proto-menu"));
        assert.deepEqual(seen.posts, [{ id: "claude", account: "new@example.com", growthPolicy: "cautious" }]);

        // the pick ticked when opened again
        await page.waitForFunction(() => document.querySelector('#acxList .row.acx[data-user="new@example.com"] .acx-grow:not(.busy)'));
        mu = await open('#acxList .row.acx[data-user="new@example.com"] .acx-grow');
        assert.deepEqual(mu.items.filter(([, on]) => on).map(([n]) => n), [x("Cautious")]);
        await page.keyboard.press("Escape");
        await page.mouse.click(1, 790);
        await page.waitForFunction(() => !document.querySelector(".proto-menu"));

        // a pick gone ticks nothing; Follow the subscription (Codex's Gentle) clears it
        mu = await open('#acxList .row.acx[data-user="ops@example.com"] .acx-grow');
        assert.deepEqual(mu.items.filter(([, on]) => on), []);
        assert.equal(mu.items[0][0], x("Follow the subscription ({name})", { name: x("Gentle") }));
        await page.locator(".proto-menu .pm-item").first().click();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 4);
        assert.deepEqual(seen.posts[1], { id: "codex", account: "ops@example.com", growthPolicy: "" });

        // the card says each account's Growth policy and opens the same picks
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card .acx-gpf").length === 4);
        const facts = await page.evaluate(() => Object.fromEntries([...document.querySelectorAll("#acxList .acx-card")].map((c) => [c.dataset.user, c.querySelector(".acx-gpf").textContent])));
        assert.deepEqual(facts, {
          "new@example.com": x("Cautious"), "fresh@example.com": "Slow", "old@example.com": x("Off"), "ops@example.com": x("Follows its subscription"),
        });
        assert.ok(await wider(page) <= 0.5, `a card's pick reaches ${await wider(page)}px past the window`);
        await shot("cards");
        mu = await open('#acxList .acx-card[data-user="old@example.com"] .acx-gpf .acx-grow');
        assert.deepEqual(mu.items.filter(([, on]) => on).map(([n]) => n), [x("Off")]);
        assert.ok(!mu.items.some(([n]) => n === x("Skip ramp-up")), "a card's fact offers Skip ramp-up");
        await page.locator(".proto-menu .pm-item").first().click();
        await page.waitForFunction(() => !document.querySelector(".proto-menu"));
        assert.deepEqual(seen.posts[2], { id: "claude", account: "old@example.com", growthPolicy: "" });
        assert.deepEqual(errors, []);
      });
    }
  }
}
