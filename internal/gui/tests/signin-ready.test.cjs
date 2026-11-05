// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 2026-11-04 (点击添加账号时会自动发起请求到 claude.ai，改为默认不发起):
// adding an account asks nothing of its vendor until Start sign-in.
// - The Add sheet's ChatGPT tile and an editor's "Add another … account"
//   row draw "Add a {name} account" (.signing.ready): no /api/signin, and
//   no page opened.
// - A proxy picked there from Proxy: … is only recorded; Start sign-in
//   then makes exactly one /api/signin, through that proxy. Cancel makes
//   none.
// Every language, Chromium and WebKit, at a wide and a phone's width;
// no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);
const HOME = "http://me:s3cret@10.0.0.9:3128", HOME_SHOWN = "http://10.0.0.9:3128";
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};

function serve(lang, seen) {
  const settings = () => ({
    lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", proxyNow: "", proxySource: "none",
    trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [], visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [{ name: "Home", url: HOME }],
  });
  const providers = { providers: [codex], presets: [], excluded: [], gateway: { running: true, window: true } };
  const flows = {};
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(), fx: settings().fx });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/open") { seen.opens.push(req.postDataJSON()); return json({}); }
    if (url.pathname === "/api/signin") {
      const b = req.postDataJSON();
      seen.starts.push(b);
      const id = "s" + seen.starts.length;
      flows[id] = { id, agent: b.agent, state: "waiting", url: "https://vendor.example/oauth?state=" + id, proxy: b.proxy ?? "", pasteCallback: true };
      return json(flows[id]);
    }
    const m = url.pathname.match(/^\/api\/signin\/(s\d+)(\/cancel)?$/);
    if (m) {
      if (m[2]) flows[m[1]].state = "canceled";
      return json(flows[m[1]]);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
const sizes = [{ width: 900, height: 800 }, { width: 380, height: 760 }];

async function open(engine, size, lang, seen, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: size, reducedMotion: "reduce" })).newPage();
  t.after(() => browser.close());
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, seen));
  await page.goto("http://magpie.test/?view=providers");
  return { page, errors };
}

// settle: room for a request the click would have made to arrive
const settle = (page) => page.evaluate(() => new Promise((r) => setTimeout(r, 300)));

// pick: a proxy picked from the ready sheet's Proxy: … link (wheeled to:
// magpie puts back a scroll no reader made, Playwright's own among them)
async function pick(page, box, name) {
  const via = box.locator("button.via");
  await reach(page, via);
  await via.click();
  const item = page.locator(".proto-menu .pm-item", { hasText: name }).first();
  await reach(page, item);
  await item.click();
  await page.waitForFunction((s) => document.querySelector(".signing.ready button.via")?.textContent.includes(s), name);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const w = (s, a) => tr(lang, s, a);
    for (const size of sizes) {
      test(`${engine} ${lang} ${size.width}: the Add sheet's tile asks nothing of the vendor until Start sign-in`, async (t) => {
        const seen = { starts: [], opens: [] };
        const { page, errors } = await open(engine, size, lang, seen, t);
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        await sheet.locator(".tile").first().waitFor();
        await sheet.locator(".tile").filter({ has: page.locator(".n", { hasText: /^ChatGPT$/ }) }).click();
        const box = sheet.locator(".signing.ready");
        await box.waitFor();
        assert.equal(await box.locator(".tt .n").textContent(), w("Add a {name} account", { name: "ChatGPT" }));
        assert.equal((await box.locator("button.sign-start").textContent()).trim(), w("Start sign-in"));
        await settle(page);
        assert.deepEqual(seen.starts, [], "the tile starts no sign-in");
        assert.deepEqual(seen.opens, [], "and opens no page");

        await pick(page, box, HOME_SHOWN);
        await settle(page);
        assert.deepEqual(seen.starts, [], "picking a proxy starts nothing");
        assert.deepEqual(seen.opens, []);
        assert.equal(await wide(page), false, "nothing wider than the window");

        await reach(page, box.locator("button.sign-start"));
        await box.locator("button.sign-start").click();
        for (let i = 0; i < 40 && !seen.starts.length; i++) await page.waitForTimeout(50);
        await settle(page);
        assert.equal(seen.starts.length, 1, "Start sign-in starts it once");
        assert.equal(seen.starts[0].agent, "codex");
        assert.equal(seen.starts[0].proxy, HOME, "through the proxy picked");
        await sheet.locator(".signing:not(.ready)").waitFor();
        assert.equal(await wide(page), false, "nothing wider than the window");
        assert.deepEqual(errors, []);
      });

      // the other ways in are there before anything is sent (review M1): a
      // Claude setup-token opens its own sheet and starts no sign-in, and
      // ChatGPT's device code is offered beside the proxy
      test(`${engine} ${lang} ${size.width}: the ready sheet offers the other ways in, sending nothing`, async (t) => {
        const seen = { starts: [], opens: [] };
        const { page, errors } = await open(engine, size, lang, seen, t);
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        await sheet.locator(".tile").first().waitFor();
        await sheet.locator(".tile").filter({ has: page.locator(".n", { hasText: /^ChatGPT$/ }) }).click();
        await sheet.locator(".signing.ready button.codex-way").waitFor();
        assert.equal(await sheet.locator(".signing.ready button.codex-way").textContent(), w("Sign in with a code instead"));
        await sheet.locator('.tile[data-pick="Claude"]').click();
        const tok = sheet.locator(".signing.ready button.ready-token");
        await reach(page, tok);
        assert.equal(await tok.textContent(), w("Use a setup-token instead…"));
        await tok.click();
        await sheet.locator(".signing.claude-token").waitFor();
        await settle(page);
        assert.deepEqual(seen.starts, [], "a setup-token asks nothing of claude.ai");
        assert.deepEqual(seen.opens, []);
        assert.equal(await wide(page), false, "nothing wider than the window");
        assert.deepEqual(errors, []);
      });

      // its words keep their room beside the buttons: the title unclipped,
      // the proxy link wide enough to read and to click
      test(`${engine} ${lang} ${size.width}: the ready sheet keeps room for its title and proxy link`, async (t) => {
        const seen = { starts: [], opens: [] };
        const { page, errors } = await open(engine, size, lang, seen, t);
        const room = (box) => box.evaluate((b) => {
          const n = b.querySelector(".tt .n"), v = b.querySelector("button.via");
          return { clipped: n.scrollWidth > n.clientWidth + 1, title: Math.round(n.clientWidth), via: Math.round(v.clientWidth) };
        });
        await page.locator("#addProvider").click();
        await page.locator("#addSheet .tile").filter({ has: page.locator(".n", { hasText: /^ChatGPT$/ }) }).click();
        const sheet = page.locator("#addSheet .signing.ready");
        await sheet.waitFor();
        const a = await room(sheet);
        await page.keyboard.press("Escape");
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts .acc.add").first().click();
        const ed = page.locator(".editor .signing.ready");
        await ed.waitFor();
        const e = await room(ed);
        for (const [where, r] of [["Add sheet", a], ["editor", e]]) {
          assert.equal(r.clipped, false, `${where}: the title is cut short (${JSON.stringify(r)})`);
          assert.ok(r.via >= 60, `${where}: the proxy link is ${r.via}px wide (${JSON.stringify(r)})`);
        }
        assert.equal(await wide(page), false, "nothing wider than the window");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${size.width}: an editor's add row asks nothing until Start sign-in, and Cancel nothing at all`, async (t) => {
        const seen = { starts: [], opens: [] };
        const { page, errors } = await open(engine, size, lang, seen, t);
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        const add = page.locator(".editor .accts .acc.add").first();
        await add.click();
        const box = page.locator(".editor .signing.ready");
        await box.waitFor();
        assert.equal(await box.locator(".tt .n").textContent(), w("Add a {name} account", { name: "ChatGPT" }));
        await settle(page);
        assert.deepEqual(seen.starts, [], "the add row starts no sign-in");
        assert.deepEqual(seen.opens, [], "and opens no page");
        assert.equal(await wide(page), false, "nothing wider than the window");

        await box.locator(":scope > button", { hasText: w("Cancel") }).click();
        await box.waitFor({ state: "detached" });
        await settle(page);
        assert.deepEqual(seen.starts, [], "Cancel starts nothing");
        assert.deepEqual(seen.opens, []);

        await add.click();
        await box.waitFor();
        await pick(page, box, w("Direct"));
        await settle(page);
        assert.deepEqual(seen.starts, [], "picking a proxy starts nothing");
        await reach(page, box.locator("button.sign-start"));
        await box.locator("button.sign-start").click();
        for (let i = 0; i < 40 && !seen.starts.length; i++) await page.waitForTimeout(50);
        await settle(page);
        assert.equal(seen.starts.length, 1, "Start sign-in starts it once");
        assert.equal(seen.starts[0].agent, "codex");
        assert.equal(seen.starts[0].proxy, "direct", "through the proxy picked");
        await page.locator(".editor .signing:not(.ready)").waitFor();
        assert.equal(await wide(page), false, "nothing wider than the window");
        assert.deepEqual(errors, []);
      });
    }
  }
}
