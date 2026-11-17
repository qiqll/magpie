// T-198: Models › Proxy IPs gives an IP to an area and takes it back
// (/api/ips/area). An IP with no area offers the areas in the app's own
// menu; one an area has says which, and takes it back. With no area
// made, the button can't be pressed. The row stays inside the window.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [] };
  const answer = () => ({ ips: seen.ips, rev: "r" + seen.rev, days: 30, warnDays: 5, areas: seen.areas });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/ips") return json(answer());
    if (url.pathname === "/api/ips/area") {
      const body = req.postDataJSON();
      seen.posts.push(body);
      seen.ips = seen.ips.map((x) => (x.ip === body.ip ? { ...x, area: body.area || undefined } : x));
      seen.rev++;
      return json(answer());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#view-ips *")].filter((e) => e.offsetParent).map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: an IP is given to an area and taken back`, async (t) => {
        const seen = { posts: [], rev: 1, areas: [{ id: "ar_1", note: "学习" }, { id: "ar_2", note: "工作" }],
          ips: [{ ip: "203.0.113.7", name: "Osaka" }, { ip: "203.0.113.8", area: "ar_2" }] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.goto("http://magpie.test/?view=ips");
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 2, null, { timeout: 30000 });
        const button = (ip) => page.locator(`#ipsList .ips-row[data-ip="${ip}"] .ips-area`);
        assert.equal(await button("203.0.113.7").textContent(), x("Give to area…"));
        assert.equal(await button("203.0.113.8").textContent(), x("Take back from {area}", { area: "工作" }));
        assert.ok(await wider(page) <= 0.5, `the page reaches ${await wider(page)}px past the window`);

        // given from the app's own menu, no <select>
        const y = await page.evaluate(() => scrollY);
        await button("203.0.113.7").click();
        assert.equal(await page.locator("select").count(), 0);
        await page.locator(".proto-menu .pm-item", { hasText: "学习" }).click();
        await page.waitForFunction((want) => document.querySelector('#ipsList .ips-row[data-ip="203.0.113.7"] .ips-area')?.textContent === want, x("Take back from {area}", { area: "学习" }));
        assert.deepEqual(seen.posts, [{ ip: "203.0.113.7", area: "ar_1" }]);
        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");

        // taken back at once
        await button("203.0.113.8").click();
        await page.waitForFunction((want) => document.querySelector('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-area')?.textContent === want, x("Give to area…"));
        assert.deepEqual(seen.posts.at(-1), { ip: "203.0.113.8", area: "" });

        // no area made: nothing to give to
        seen.areas = [];
        await page.goto("http://magpie.test/?view=ips");
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 2, null, { timeout: 30000 });
        const none = page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-area');
        assert.ok(await none.isDisabled());
        assert.equal(await none.getAttribute("title"), x("No area yet: make one in Gateway › Areas"));
        assert.deepEqual(errors, []);
      });
    }
  }
}
