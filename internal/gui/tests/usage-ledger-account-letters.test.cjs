// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests tell each request's account by its first two
// letters, before its provider's name (需求方: 「每个详细请求展示带上账号的名称
// 的前两个字母，好区分每个请求归属于哪个账号」): the account the record names,
// or the one its host was reached as, by the name the user gave it at that
// provider when there is one (one address may be named at one provider and
// not another), a Chinese name by its first two characters. Its whole name is
// in the tooltip. A key's request, and one read from a session file (whose
// account made the session, not the call), have none. Hidden with the
// accounts, the name the user gave too; the provider's name beside it is
// what is cut, the table runs no wider for it at 700 or 1100 wide and nothing runs
// past the window at 420, in both engines and every language.
// The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const row = (i, extra) => ({
  t: at(i), agent: "codex", agentName: "Codex", icon: "codex", provider: "relay", providerName: "Relay",
  req: "gpt-5", model: "gpt-5", served: "gpt-5", in: 300, out: 40, ms: 2000, status: 200, rid: "r-" + i, ...extra,
});
const ROWS = [
  // a named account: its name's first two, a Chinese one's characters
  row(0, { provider: "codex", providerName: "Codex", host: "chatgpt.com as ann@example.com", providerAccount: "ann@example.com", accountName: "工作号" }),
  // an account the record knows only by the host it went to
  row(1, { provider: "claude", providerName: "Claude", host: "api.anthropic.com as bob@example.org" }),
  // a key's: no account
  row(2, { host: "relay.dev" }),
  // read from a session file: its account made the session
  row(3, { source: "log", session_account: "carol@example.net", session_provider: "openai" }),
  // the same address at another provider, where it has no name
  row(4, { provider: "claude", providerName: "Claude", host: "api.anthropic.com as ann@example.com" }),
];
const ACCOUNTS = [
  { id: "ann@example.com", name: "工作号", providers: ["Codex"] },
  { id: "bob@example.org", providers: ["Claude"] },
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
    for (const width of [420, 700, 1100]) {
      test(`${engine} ${lang} ${width}: each request names its account by two letters`, async (t) => {
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
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-account-letters.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.addInitScript(() => { try { localStorage.setItem("magpie.maskEmails", "0"); } catch {} });
        await page.goto("http://magpie.test/?view=usage");
        await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        const rows = page.locator(".led tbody tr.led-row");
        await rows.nth(4).waitFor({ timeout: 15000 });

        const cells = await rows.evaluateAll((trs) => trs.map((tr) => {
          const w = tr.querySelector("td.where"), ab = w.querySelector(".acct-ab");
          return { ab: ab ? ab.textContent : null, title: ab ? ab.title : null, name: w.querySelector(".where-name").textContent };
        }));
        assert.deepEqual(cells[0], { ab: "工作", title: "工作号 · ann@example.com", name: "Codex · chatgpt.com as ann@example.com" });
        assert.deepEqual(cells[1], { ab: "bo", title: "bob@example.org", name: "Claude · api.anthropic.com as bob@example.org" });
        assert.equal(cells[2].ab, null, "a key's request names no account");
        assert.equal(cells[2].name, "Relay · relay.dev");
        assert.equal(cells[3].ab, null, "a session file's account made the session, not the call");
        assert.equal(cells[3].name, "carol@example.net");
        assert.deepEqual(cells[4], { ab: "an", title: "ann@example.com", name: "Claude · api.anthropic.com as ann@example.com" }, "named at Codex only");

        // whole, on the provider's line, and the page never runs sideways
        const fit = await rows.first().locator("td.where").evaluate((td) => {
          const ab = td.querySelector(".acct-ab").getBoundingClientRect(), name = td.querySelector(".where-name").getBoundingClientRect(), c = td.getBoundingClientRect();
          return { line: Math.abs(ab.top + ab.height / 2 - (name.top + name.height / 2)) < 3, inside: ab.left >= c.left - 0.5 && ab.right <= name.left + 0.5, whole: td.querySelector(".acct-ab").scrollWidth <= Math.ceil(ab.width) + 1 };
        });
        assert.deepEqual(fit, { line: true, inside: true, whole: true });
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page runs past the window");
        // narrowed, the chip takes its room from the name, not the table's:
        // the table runs no further past its box than it does without it
        // (German's headers do at 700 already)
        const over = () => page.locator("#ledWrap").evaluate((w) => w.scrollWidth - w.clientWidth);
        if (width >= 700) {
          const withChip = await over();
          const off = await page.addStyleTag({ content: ".led .acct-ab { display: none; }" });
          const without = await over();
          await off.evaluate((s) => s.remove());
          assert(withChip <= without + 1, `the chip widens the table: ${withChip}px past its box, ${without}px without it`);
        }

        // hidden with the accounts, for a screenshot, and back after
        const blurred = () => rows.first().locator(".acct-ab").evaluate((e) => getComputedStyle(e).filter);
        assert.equal(await blurred(), "none");
        await page.evaluate(() => window.hideAccounts.set(true));
        assert.match(await blurred(), /blur/);
        const tip = await rows.first().locator(".acct-ab").getAttribute("title");
        assert.doesNotMatch(tip, /ann@example\.com/, "the tooltip's address is masked too");
        assert.doesNotMatch(tip, /工作号/, "and the name the user gave it");
        await page.evaluate(() => window.hideAccounts.set(false));
        assert.equal(await blurred(), "none");
        assert.deepEqual(errors, []);
      });
    }
  }
}
