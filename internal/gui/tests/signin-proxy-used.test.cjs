// Run with Node's test runner and Playwright on the module path; see README.md.
// A Claude sign-in that fails says, under why, the proxy `claude auth
// login` was given and whose it was (T-103, provider.SignInProxy): from a
// start the server refused (/api/signin's proxyUsed and proxyFrom), and
// from a sign-in that failed later (its state's proxyUsed). No proxy
// given is said too, with why. A failure with nothing known of its proxy
// says nothing more. Chromium and WebKit, 420 and 1100 wide, in every
// language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const lapse = "me@example.com's sign-in was revoked; sign in again";
const why = "Login failed: proxy refused the connection";
const claude = {
  id: "claude", name: "Claude Code", icon: "anthropic", chat: "", responses: "", anthropic: "https://api.anthropic.com", catalog: "",
  models: [{ id: "claude-sonnet-5-5", name: "Claude Sonnet 5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
  account: { agent: "claude", agentName: "Claude Code", agentIcon: "anthropic", builtin: true, user: "me@example.com", plan: "Max", logins: [
    { user: "me@example.com", plan: "Max", active: true, on: true, lapsed: lapse },
  ] },
};

// serve fakes the API; how is "start" (the start refused, with the
// proxy), "later" (failed once waiting, the proxy in its state), "none"
// (no proxy given, the global one set to none) or "unknown" (nothing
// said of it).
function serve(lang, how) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: [claude], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [], onPlugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/login/usage") return json({});
    if (url.pathname === "/api/signin" && route.request().method() === "POST") {
      if (how === "start") return json({ error: why, proxyUsed: "http://192.168.1.2:7890", proxyFrom: "settings" }, 400);
      return json({ id: "s1", agent: "claude", state: "waiting", url: "https://claude.test/authorize" });
    }
    if (url.pathname === "/api/signin/s1") {
      const used = { later: { proxy: "http://10.0.0.1:3128", from: "account" }, none: { proxy: "", from: "direct" } }[how];
      return json({ id: "s1", agent: "claude", state: "failed", error: why, ...(used ? { proxyUsed: used } : {}) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { again: "Sign in again", start: "Proxy given to it: http://192.168.1.2:7890 (magpie's global proxy)", later: "Proxy given to it: http://10.0.0.1:3128 (this account's own)", none: "No proxy given to it (magpie's global proxy is set to none)" },
  zh: { again: "重新登录", start: "交给它的代理：http://192.168.1.2:7890（magpie 的全局代理）", later: "交给它的代理：http://10.0.0.1:3128（此账号自己的）", none: "没有交给它代理（magpie 的全局代理设为不使用）" },
  "zh-TW": { again: "重新登入", start: "交給它的代理：http://192.168.1.2:7890（magpie 的全域代理）", later: "交給它的代理：http://10.0.0.1:3128（此帳號自己的）", none: "沒有交給它代理（magpie 的全域代理設為不使用）" },
  ja: { again: "再度サインイン", start: "渡したプロキシ：http://192.168.1.2:7890（magpie のグローバルプロキシ）", later: "渡したプロキシ：http://10.0.0.1:3128（このアカウント独自のもの）", none: "プロキシは渡していません（magpie のグローバルプロキシは「なし」）" },
  de: { again: "Erneut anmelden", start: "Übergebener Proxy: http://192.168.1.2:7890 (magpies globaler Proxy)", later: "Übergebener Proxy: http://10.0.0.1:3128 (der eigene dieses Kontos)", none: "Kein Proxy übergeben (magpies globaler Proxy ist auf keinen gesetzt)" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a failed Claude sign-in says the proxy it was given, and whose", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(W)) {
      for (const width of [420, 1100]) {
        for (const how of ["start", "later", "none", "unknown"]) {
          await t.test(`${lang} ${width} ${how}`, async () => {
            const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
            page.setDefaultTimeout(5000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang, how));
            await page.goto("http://magpie.test/?view=providers");
            const line = page.locator("#lapsedAccounts .deprecation.lapsed").first();
            await line.waitFor();
            await line.getByRole("button", { name: W[lang].again, exact: true }).click();
            const box = page.locator(".signing.failed");
            await box.waitFor();
            assert.equal(await box.locator(".s").first().innerText(), why);
            const used = box.locator(".s.proxy-used");
            if (how === "unknown") {
              assert.equal(await used.count(), 0);
            } else {
              assert.equal(await used.innerText(), W[lang][how]);
              const r = await used.boundingBox(), b = await box.boundingBox();
              assert.ok(r.x >= b.x - 1 && r.x + r.width <= b.x + b.width + 1, "the proxy runs out of the box");
              const docW = await page.evaluate(() => document.documentElement.scrollWidth);
              assert.ok(docW <= width, `the page scrolls sideways (${docW} > ${width})`);
            }
            if (process.env.ARTIFACT_DIR) await box.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `signin-proxy-used-${engine}-${lang}-${width}-${how}.png`) });
            assert.deepEqual(errors, []);
            await page.context().close();
          });
        }
      }
    }
  });
}
