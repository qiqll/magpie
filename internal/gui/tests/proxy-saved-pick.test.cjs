// Run with Node's test runner and Playwright on the module path; see README.md.
// T-110 (需求方: 每加一个账户就需要修改一次全局代理): an account is added
// through a proxy picked as its callback address is pasted, and proxies
// the user named in Settings › Network are offered there.
// - The sign-in's Callback URL field has "Through: …" beside it: its menu
//   offers the global proxy, Direct, a saved proxy (its password left
//   out) and Custom…; picking one starts nothing again, and Finish sign-in
//   sends the address with the proxy picked. Custom… asks for the address
//   in place, and that one is sent.
// - A sign-in that keeps the proxy it started through (proxyFixed, Claude
//   Code's own) has no such pick, and says to pick it from Proxy above.
// - Settings › Network lists the saved proxies (no password shown) with a
//   Test that checks that one proxy, and adds and removes them as a list.
// Every language, Chromium and WebKit, at a wide and a phone's width;
// no backend, the API is faked here.
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
const HOME = "http://me:s3cret@10.0.0.9:3128", HOME_SHOWN = "http://10.0.0.9:3128";
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};
const claude = {
  id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "claude-opus-5", name: "Claude Opus 5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "claude", agentName: "Claude Code", user: "alice@example.com", plan: "MAX", logins: [{ user: "alice@example.com", plan: "MAX", active: true, on: true }] },
  proxy: "",
};

function serve(lang, seen) {
  const settings = () => ({
    lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", proxyNow: "", proxySource: "none",
    trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [], visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: seen.proxies,
  });
  const providers = { providers: [codex, claude], presets: [], excluded: [], gateway: { running: true, window: true } };
  const flows = {};
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(), fx: settings().fx });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/proxies") {
      seen.saves.push(req.postDataJSON());
      seen.proxies = req.postDataJSON().proxies;
      return json(settings());
    }
    if (url.pathname === "/api/proxy/check") {
      seen.checks.push(req.postDataJSON());
      return json({ proxy: HOME_SHOWN, source: "saved", through: { ip: "203.0.113.9", loc: "US", ms: 312, proxied: true }, direct: { ip: "198.51.100.7", loc: "CN", ms: 41 } });
    }
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/signin") {
      const b = req.postDataJSON();
      seen.starts.push(b);
      const id = "s" + seen.starts.length;
      flows[id] = { id, agent: b.agent, state: "waiting", url: "https://vendor.example/oauth?state=" + id, proxy: b.proxy ?? "", pasteCallback: true, proxyFixed: b.agent === "claude" };
      return json(flows[id]);
    }
    const m = url.pathname.match(/^\/api\/signin\/(s\d+)(\/cancel|\/callback)?$/);
    if (m) {
      if (m[2] === "/cancel") flows[m[1]].state = "canceled";
      if (m[2] === "/callback") { seen.pastes.push(req.postDataJSON()); return route.fulfill({ status: 204, body: "" }); }
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

async function open(engine, size, lang, seen, at, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: size, reducedMotion: "reduce" })).newPage();
  t.after(() => browser.close());
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, seen));
  if (at.includes("settings")) await watchSettingsReads(page);
  await page.goto("http://magpie.test/" + at);
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const w = (s, a) => tr(lang, s, a);
    for (const size of sizes) {
      test(`${engine} ${lang} ${size.width}: a proxy picked beside the pasted address goes with it`, async (t) => {
        const seen = { starts: [], pastes: [], saves: [], checks: [], proxies: [{ name: "Home", url: HOME }] };
        const { page, errors } = await open(engine, size, lang, seen, "?view=providers", t);
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts .acc.add").first().click();
        const box = page.locator(".editor .signing");
        const pp = box.locator("button.paste-via");
        await pp.waitFor();
        assert.equal((await pp.textContent()).trim(), w("Through: {proxy}", { proxy: w("Global proxy") }));
        await pp.scrollIntoViewIfNeeded();
        await pp.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), [w("Global proxy"), w("Direct"), HOME_SHOWN, w("Custom…")]);
        assert.match(await menu.locator(".pm-item", { hasText: HOME_SHOWN }).textContent(), /Home/);
        assert.doesNotMatch(await menu.textContent(), /s3cret/, "the saved proxy's password isn't shown");
        await menu.locator(".pm-item", { hasText: HOME_SHOWN }).click();
        await page.waitForFunction((s) => document.querySelector(".editor .signing button.paste-via")?.textContent.includes(s), HOME_SHOWN);
        assert.equal(seen.starts.length, 1, "picking it beside the address starts nothing again");
        const url = box.locator("form.callback-form input").first();
        await url.fill("http://localhost:1455/auth/callback?code=c&state=s1");
        await box.locator("form.callback-form button.primary").click();
        for (let i = 0; i < 40 && !seen.pastes.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(seen.pastes, [{ url: "http://localhost:1455/auth/callback?code=c&state=s1", proxy: HOME }]);
        assert.equal(await wide(page), false, "nothing wider than the window");
        const fit = await pp.evaluate((v) => { const r = v.getBoundingClientRect(), b = v.closest(".signing").getBoundingClientRect(); return r.left >= b.left - 0.5 && r.right <= b.right + 0.5; });
        assert(fit, "the pick stays inside the sheet");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${size.width}: a proxy typed beside the pasted address, and a sign-in that keeps its own`, async (t) => {
        const seen = { starts: [], pastes: [], saves: [], checks: [], proxies: [] };
        const { page, errors } = await open(engine, size, lang, seen, "?view=providers", t);
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts .acc.add").first().click();
        const box = page.locator(".editor .signing");
        const pp = box.locator("button.paste-via");
        await pp.waitFor();
        await pp.scrollIntoViewIfNeeded();
        await pp.click();
        await page.locator(".proto-menu .pm-item", { hasText: w("Custom…") }).click();
        const typed = box.locator("input.paste-proxy");
        await typed.waitFor();
        await typed.fill("socks5://127.0.0.1:1080");
        await box.locator("form.callback-form input").first().fill("http://localhost:1455/auth/callback?code=c&state=s1");
        await box.locator("form.callback-form button.primary").click();
        for (let i = 0; i < 40 && !seen.pastes.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(seen.pastes, [{ url: "http://localhost:1455/auth/callback?code=c&state=s1", proxy: "socks5://127.0.0.1:1080" }]);
        assert.equal(seen.starts.length, 1);
        assert.equal(await wide(page), false, "nothing wider than the window");

        // Claude Code's own sign-in keeps the proxy it started through
        await page.keyboard.press("Escape");
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Claude" }).first().click();
        await page.locator(".editor .accts .acc.add").first().click();
        const fixed = page.locator(".editor .signing .proxy-fixed");
        await fixed.waitFor();
        assert.equal(await fixed.textContent(), w("This sign-in keeps the proxy it started through. To finish it through another, pick it from Proxy above: the sign-in starts again."));
        assert.equal(await page.locator(".editor .signing button.paste-via").isVisible(), false, "no pick beside the address");
        await page.locator(".editor .signing form.callback-form input").first().fill("code#state");
        await page.locator(".editor .signing form.callback-form button.primary").click();
        for (let i = 0; i < 40 && seen.pastes.length < 2; i++) await page.waitForTimeout(50);
        assert.deepEqual(seen.pastes[1], { url: "code#state" }, "no proxy sent with it");
        assert.equal(await wide(page), false, "nothing wider than the window");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${size.width}: Settings › Network keeps named proxies, each with a Test`, async (t) => {
        const seen = { starts: [], pastes: [], saves: [], checks: [], proxies: [{ name: "Home", url: HOME }] };
        const { page, errors } = await open(engine, size, lang, seen, "?view=settings&tab=network", t);
        await settingsDrawn(page);
        const row = page.locator("#savedProxies");
        await row.locator(".sp-line").first().waitFor();
        assert.equal(await row.locator(".name").first().textContent(), w("Saved proxies"));
        assert.equal(await row.locator(".sp-addr").first().textContent(), HOME_SHOWN);
        assert.doesNotMatch(await row.textContent(), /s3cret/, "no password shown");

        await row.locator(".sp-test").click();
        const said = row.locator(".sp-check");
        await said.waitFor();
        assert.deepEqual(seen.checks, [{ proxy: HOME }]);
        assert.equal(await said.textContent(), w("Works: through the proxy requests leave from {where}, {ms} ms; direct ones from {direct}", { where: "203.0.113.9 (US)", ms: 312, direct: "198.51.100.7 (CN)" }));

        await row.locator(".sp-add").click();
        const form = row.locator("form.sp-form");
        await form.waitFor();
        await form.locator("input.sp-name-in").fill("Office");
        await form.locator("input.sp-url-in").fill("http://10.1.1.1:8080");
        await form.locator(".sp-save").click();
        await page.waitForFunction(() => document.querySelectorAll("#savedProxies .sp-line").length === 2);
        assert.deepEqual(seen.saves.at(-1), { proxies: [{ name: "Home", url: HOME }, { name: "Office", url: "http://10.1.1.1:8080" }] });
        assert.equal(await wide(page), false, "nothing wider than the window");

        // the editor of one keeps its login out of the address field
        await row.locator(".sp-line", { hasText: "Home" }).locator(".sp-edit").click();
        assert.equal(await row.locator("form.sp-form input.sp-url-in").inputValue(), "http://10.0.0.9:3128");
        await row.locator("form.sp-form .sp-cancel").click();

        await row.locator(".sp-line", { hasText: "Home" }).locator(".sp-remove").click();
        await page.waitForFunction(() => document.querySelectorAll("#savedProxies .sp-line").length === 1);
        assert.deepEqual(seen.saves.at(-1), { proxies: [{ name: "Office", url: "http://10.1.1.1:8080" }] });

        // a saved one fills the global proxy's Custom address
        await page.locator("#proxySegs .segs button", { hasText: w("Custom") }).click();
        const pick = page.locator("#proxySegs .saved-proxy");
        await pick.waitFor();
        assert.equal((await pick.textContent()).trim(), w("Saved…"));
        assert.deepEqual(errors, []);
      });
    }
  }
}
