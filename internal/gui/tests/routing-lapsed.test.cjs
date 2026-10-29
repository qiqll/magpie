// Run with Node's test runner and Playwright on the module path; see README.md.
// An account whose sign-in has to be made again (Anthropic refused it, its
// refresh was refused, its vendor revoked it) is left out of a request
// while another account can answer it, and the Routing page says so: the
// request's steps say why it was left out, never as a plan without the
// model, and nothing runs off a 420px window. In every
// language, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = new Date(now.getTime() - 60e3).toISOString();
const acct = (user, extra = {}) => ({ id: `claude@${user}`, provider: "claude", name: "Claude Code", kind: "account", who: user, model: "claude-sonnet-5", plan: "Max", ...extra });
const healthy = acct("b@example.com");
const lapsed = acct("a@example.com", { id: "claude", unlisted: true, lapsed: "Claude Code could not authenticate this account; sign in again in magpie" });
const route = {
  id: 1, seq: 1, time: at, agent: "claude-code", model: "claude/claude-sonnet-5", provider: "claude", done: true, status: 200, ms: 900, tokens: 1200,
  order: [healthy], left: [lapsed],
  tries: [{ id: healthy.id, model: "claude-sonnet-5", start: at, done: true, status: 200, ms: 900 }],
};

function serve(lang) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude-code", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 1 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

const keys = [
  "to be signed in again in magpie",
  "{who} is left out: its sign-in has to be made again, so it can't answer until it is signed in again in magpie.",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1000]) {
      test(`${engine} ${lang} ${width}px: Routing says an account was left out to be signed in again`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
          const s = (lang !== "en" && I18N[lang]?.[key]) || key;
          return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
        }, [lang, key, vars]);
        const req = page.locator(".rt-req").first();
        await req.waitFor();
        if (await req.getAttribute("aria-pressed") !== "true") await req.click();
        await page.locator(".rt-steps li").first().waitFor();
        const why = await say(keys[1], { who: "a@example.com" });
        await page.waitForFunction((w) => [...document.querySelectorAll(".rt-steps li")].some((e) => e.textContent.includes(w)), why);
        // never told as a plan that doesn't list the model, nor as barred
        const steps = await page.locator(".rt-steps li").evaluateAll((ls) => ls.map((l) => l.textContent));
        const unlisted = await say("{who} is left out: its plan doesn't list {model}.", { who: "a@example.com", model: "claude-sonnet-5" });
        assert(!steps.some((s) => s.includes(unlisted)), JSON.stringify(steps));
        if (lang !== "en") {
          const missing = await page.evaluate(([keys, lang]) => keys.filter((k) => !I18N[lang][k]), [keys, lang]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
