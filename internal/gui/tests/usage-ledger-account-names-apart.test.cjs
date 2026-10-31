// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests offer one Account filter choice per address, and
// a name is given at a provider (T-033): an address named 工作号 at Codex and
// 私人号 at Claude was offered by the first name found, at both. The choice
// is now named by both, and its note says which provider gave which, beside
// a provider that gave none. An address named alike everywhere keeps its one
// name. Hidden with the accounts, each provider's name is hidden too. In both
// engines, every language, narrow and wide.
// The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const row = (i, extra) => ({
  t: new Date(now.getTime() - (i + 1) * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex",
  req: "gpt-5", model: "gpt-5", served: "gpt-5", in: 300, out: 40, ms: 2000, status: 200, rid: "r-" + i, ...extra,
});
const ROWS = [
  row(0, { provider: "codex", providerName: "Codex", providerAccount: "dan@example.com", accountName: "工作号" }),
  // dan's Claude calls are past this page: 私人号 is known by the filter alone
  row(1, { provider: "relay", providerName: "Relay", providerAccount: "dan@example.com" }),
  row(2, { provider: "codex", providerName: "Codex", providerAccount: "eve@example.com", accountName: "同名" }),
];
const ACCOUNTS = [
  { id: "dan@example.com", providers: ["Codex", "Claude", "Relay"], names: ["工作号", "私人号", ""] },
  { id: "eve@example.com", name: "同名", providers: ["Codex", "Claude"] },
];

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") {
      return json({ period: url.searchParams.get("period"), agents: [{ id: "codex", name: "Codex", icon: "codex" }], accounts: ACCOUNTS,
        rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 1200, output: 160, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: ROWS.length });
    }
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 4, errors: 0, input: 1200, output: 160, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 4, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1100]) {
      test(`${engine} ${lang} ${width}: the Account filter names an address at each provider apart`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-account-names-apart.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.addInitScript(() => { try { localStorage.setItem("magpie.maskEmails", "0"); } catch {} });
        await page.goto("http://magpie.test/?view=usage");
        await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await page.locator(".led tbody tr.led-row").nth(2).waitFor({ timeout: 15000 });

        const choices = async () => {
          await page.locator("#ledAccount").click();
          const items = page.locator(".sess-menu .pm-item");
          await items.nth(2).waitFor();
          const got = await items.evaluateAll((bs) => bs.slice(1).map((b) => ({ v: b.title, name: b.querySelector(".pm-name").textContent, note: b.querySelector(".pm-note").textContent })));
          await page.keyboard.press("Escape");
          await page.locator(".sess-menu").waitFor({ state: "detached" });
          return got;
        };
        assert.deepEqual(await choices(), [
          { v: "dan@example.com", name: "工作号 / 私人号", note: "dan@example.com · Codex: 工作号 · Claude: 私人号 · Relay" },
          { v: "eve@example.com", name: "同名", note: "eve@example.com · Codex · Claude" },
        ]);

        // a few letters of either name find it
        await page.locator("#ledAccount").click();
        await page.locator(".sess-menu .sc-q").fill("私人");
        assert.deepEqual(await page.locator(".sess-menu .pm-item").evaluateAll((bs) => bs.map((b) => b.title)), ["dan@example.com"]);
        await page.keyboard.press("Escape");
        await page.locator(".sess-menu").waitFor({ state: "detached" });

        // hidden with the accounts, each provider's name too
        await page.evaluate(() => window.hideAccounts.set(true));
        await page.locator("#ledAccount").click();
        const menu = page.locator(".sess-menu");
        await menu.locator(".pm-item").nth(2).waitFor();
        const shown = (await menu.textContent()) + (await menu.locator(".pm-item").evaluateAll((bs) => bs.map((b) => b.title).join(" ")));
        await page.keyboard.press("Escape");
        await page.evaluate(() => window.hideAccounts.set(false));
        for (const s of ["工作号", "私人号", "同名", "dan@example.com"]) assert(!shown.includes(s), `${s} shows with the accounts hidden: ${shown}`);

        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page runs past the window");
        assert.deepEqual(errors, []);
      });
    }
  }
}
