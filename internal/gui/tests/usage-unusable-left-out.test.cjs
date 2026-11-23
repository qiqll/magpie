// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage → Overview leaves out an account that can't be used now, banned or
// signed out (需求方 2026-11-23: 当前周期已经无法使用的账号（被封、未在登录
// 状态）就不要显示了，会占用地方): the backend marks its card unusable
// (provider.WithUnusable). A card of several accounts keeps the usable ones;
// a card whose every account is unusable isn't drawn at all. No text is
// added, so one language; a wide window and a phone, both engines; no
// backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const win = (name, used) => ({ name, used, resetSecs: 3 * 3600 });
const QUOTAS = [
  { provider: "codex", name: "Codex", icon: "openai", plan: "pro", user: "ok@example.com", windows: [win("5h", 40), win("7d", 10)] },
  { provider: "codex", name: "Codex", icon: "openai", plan: "pro", user: "banned@example.com", windows: [], error: "this account is marked banned", unusable: true },
  { provider: "copilot", name: "GitHub Copilot", icon: "githubcopilot", user: "gone", windows: [], error: "not signed in", unusable: true },
  { provider: "deepseek", name: "DeepSeek", icon: "deepseek-color", windows: [], balance: "$3.00" },
];

function server(web) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const settings = { lang: "en", theme: "light" };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], clients: [], profiles: [], settings, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/quotas") return json(QUOTAS);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, cache_read: 0, cache_write: 0, unpriced: 0, cost: 0, bucket: "day", series: [], path: "~/.config/magpie/usage.jsonl", agents: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const SIZES = [{ width: 1200, web: false }, { width: 390, web: true }];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an account that can't be used is left out of the Overview", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const { width, web } of SIZES) {
      await t.test(`${width}${web ? " web" : ""}`, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce", hasTouch: web, isMobile: web && engine === "chromium" });
        const page = await ctx.newPage();
        page.setDefaultNavigationTimeout(60000);
        try {
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(web));
          await page.goto("http://magpie.test/?view=usage");
          const cards = page.locator("#subscriptionUsage > .subscription-card");
          // the first load of the whole app, on a busy machine
          await page.locator("#subscriptionUsage .subscription-card[data-provider]").first().waitFor({ timeout: 20000 });
          const providers = await cards.evaluateAll((cs) => cs.map((c) => c.dataset.provider));
          assert.deepEqual(providers.sort(), ["codex", "deepseek"], "the Copilot card, all unusable, is left out");
          const text = await page.locator("#subscriptionUsage").innerText();
          assert.match(text, /ok@example\.com/);
          assert.doesNotMatch(text, /banned@example\.com/, "the banned account's section is left out");
          assert.doesNotMatch(text, /not signed in/i);
          // not counted as hidden by the user either
          assert.equal(await page.locator("#quotaHid").isHidden(), true);
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
