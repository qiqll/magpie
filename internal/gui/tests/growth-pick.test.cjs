// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-10, T-131): a subscription picks the growth policy its
// accounts are held by in its provider editor — Follow the default or one
// of the policies Settings › Growth policy lists — saved at once
// (provider/growthpolicy). One it picked that is gone says its accounts
// are held by the default. Settings › Growth policy's In use lists the
// subscriptions the policy shown holds, as growth/preview tells them, and
// one a subscription picked can't be deleted until it picks another.
// Every language, at a wide and a narrow width; no backend, the API is
// faked here. SHOTS=<dir> keeps a screenshot of each.
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

const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, countDays: "used", builtin: true },
  { id: "gentle", name: "Gentle", kind: "ramp", start: 70, days: 7, countDays: "used", builtin: true },
  { id: "cautious", name: "Cautious", kind: "ramp", start: 30, days: 30, countDays: "used", builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];
const SLOW = { id: "slow", name: "Slow", kind: "ramp", start: 20, days: 60 };
const POINTS = Array.from({ length: 19 }, (_, d) => ({ day: d, fiveHours: Math.min(100, 50 + Math.floor((50 * d) / 14)), week: Math.min(100, 50 + Math.floor((50 * d) / 14)) }));

const codex = (pick) => ({
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "gpt-5.5", name: "", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  growthPolicy: pick || undefined,
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "pro", logins: [{ user: "me@example.com", plan: "pro", active: true, on: true }] },
});

function serve(lang, ctl) {
  const settings = () => ({ lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], proxies: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, growthPolicyList: [...BUILTINS, SLOW], growthInEffect: "standard" });
  const providers = () => ({ providers: [codex(ctl.pick)], presets: [], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(), fx: settings().fx });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/growthpolicy") {
      const b = req.postDataJSON();
      ctl.posts.push(b);
      ctl.pick = b.growthPolicy;
      return json(providers());
    }
    if (url.pathname === "/api/growth/preview") {
      const id = req.postDataJSON().policy.id;
      // as provider.GrowthUses answers: Codex picked Slow, so Standard, the
      // default, holds Claude alone
      const uses = id === "slow" ? [{ provider: "codex", name: "Codex", picked: true, accounts: 1 }]
        : id === "standard" ? [{ provider: "claude", name: "Claude", accounts: 2 }] : [];
      return json({ points: POINTS, accounts: [], uses });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}: a subscription picks its growth policy, and the policy page says who uses it`, async (t) => {
        const ctl = { posts: [], pick: "" };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, ctl));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        const pill = page.locator(".editor .growth-pick .growth-pill");
        await pill.waitFor();

        // following the default: the pill says so, with the default's name
        const follow = x("Follow the default ({name})", { name: x("Standard") });
        assert.equal((await pill.textContent()).trim(), follow);
        assert.equal(await page.locator(".editor .growth-gone").count(), 0);
        const label = await page.evaluate(() => [...document.querySelectorAll(".editor label")].map((l) => l.textContent));
        assert.ok(label.includes(x("Growth policy")), `${lang}: no Growth policy field`);
        assert.ok(await sideways(page) <= 1, "the editor scrolls sideways");
        if (process.env.SHOTS) await page.screenshot({ path: path.join(process.env.SHOTS, `editor-${engine}-${lang}-${width}.png`), fullPage: true });

        // the menu lists Follow the default and every policy; a pick posts it
        await pill.click();
        const items = page.locator(".proto-menu .pm-item");
        assert.equal(await items.count(), 1 + BUILTINS.length + 1);
        assert.equal(await page.locator(".proto-menu .pm-head").textContent(), x("Growth policy this subscription's accounts are held by"));
        assert.equal((await items.nth(3).locator(".pm-name").textContent()).trim(), x("Cautious"));
        assert.equal((await items.nth(3).locator(".pm-note").textContent()).trim(), x("{said} · days used", { said: x("{start}% to 100% in {days} days", { start: 30, days: 30 }) }));
        if (process.env.SHOTS) await page.screenshot({ path: path.join(process.env.SHOTS, `menu-${engine}-${lang}-${width}.png`) });
        await items.nth(5).click();
        await page.waitForFunction(() => document.querySelector(".editor .growth-pill")?.textContent.trim() === "Slow");
        assert.deepEqual(ctl.posts, [{ id: "codex", growthPolicy: "slow" }]);

        // a pick no policy has: held by the default, and the field says so
        ctl.pick = "deleted-one";
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        const gone = page.locator(".editor .growth-gone");
        await gone.waitFor();
        assert.equal((await gone.textContent()).trim(), x("The policy it picked ({id}) is gone: its accounts are held by the default until you pick another", { id: "deleted-one" }));
        assert.equal((await pill.textContent()).trim(), follow);
        assert.ok(await sideways(page) <= 1, "the warning scrolls sideways");
        // Follow the default clears the id gone (review M1)
        await pill.click();
        await items.first().click();
        await gone.waitFor({ state: "detached" });
        assert.deepEqual(ctl.posts.at(-1), { id: "codex", growthPolicy: "" });
        ctl.pick = "slow";

        // Settings › Growth policy: In use names who the policy shown holds,
        // and one a subscription picked can't be deleted
        await page.goto("http://magpie.test/?view=settings&tab=growth");
        await page.locator("#setTab-growth").click();
        const list = page.locator("#growthList");
        await list.locator(".growth-uses .growth-use").first().waitFor();
        assert.equal((await list.locator(".growth-uses").textContent()).trim(), x("{name} follows the default · {n} ramping up", { name: "Claude", n: 2 }));
        assert.equal((await list.locator(".growth-inuse").first().textContent()).trim(), x("{name} is the default: a subscription that picked no policy follows it", { name: x("Standard") }));
        await list.locator('.gp-item[data-id="slow"]').click();
        await page.waitForFunction((want) => document.querySelector("#growthList .growth-uses")?.textContent.trim() === want, x("{name} picked it · {n} ramping up", { name: "Codex", n: 1 }));
        const del = list.locator(".growth-delete");
        assert.equal(await del.isDisabled(), true);
        assert.equal(await del.getAttribute("title"), x("{names} picked it: pick another policy in its editor first", { names: "Codex" }));
        await list.locator('.gp-item[data-id="cautious"]').click();
        await page.waitForFunction((want) => document.querySelector("#growthList .growth-uses")?.textContent.trim() === want, x("No subscription is held by it"));
        assert.ok(await sideways(page) <= 1, "the policy page scrolls sideways");
        if (process.env.SHOTS) {
          await list.locator('.gp-item[data-id="slow"]').click();
          await list.locator(".growth-uses .growth-use").first().waitFor();
          await list.locator(".growth-foot").scrollIntoViewIfNeeded();
          await page.screenshot({ path: path.join(process.env.SHOTS, `inuse-${engine}-${lang}-${width}.png`) });
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
