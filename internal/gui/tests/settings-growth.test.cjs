// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Growth policy (T-078): magpie's default stages are shown, one
// row each (the days it holds, to the next row of its window; its window;
// its share); a stage's share changed, one added and one removed are saved
// whole through settings/growth, and Use default posts null. A share out of
// 1–99 is said in the page and nothing is posted. Every language, Chromium
// and WebKit, at a narrow and a wide window; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
// until waits for what the faked API was sent, a state, not a time
const until = async (ok) => { for (let i = 0; !ok(); i++) { if (i > 500) throw new Error("never came"); await new Promise((r) => setTimeout(r, 10)); } };
const DEFAULT = [
  { from: 0, window: "5h", cap: 50 }, { from: 3, window: "7d", cap: 80 },
  { from: 7, window: "5h", cap: 90 }, { from: 14, window: "5h", cap: 95 },
];

function server(lang, posted) {
  let cur = {
    theme: "light", lang, tray: "panel", currency: "usd", proxy: "", redactWords: [],
    version: "0.1.1220", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    growthStages: DEFAULT,
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings/growth") {
      const body = req.postDataJSON();
      posted.push(body);
      cur = { ...cur, growth: body.stages, growthStages: body.stages ?? DEFAULT };
      return json(cur);
    }
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { tab: "Growth policy", first: "Days 0 to 7", week: "From day 3 on", firstToThree: "Days 0 to 3", threeToSeven: "Days 3 to 7", last: "From day 14 on", five: "5 hours", weekName: "7 days", from: "from its first use" },
  zh: { tab: "成长策略", first: "第 0–7 天", week: "第 3 天起", firstToThree: "第 0–3 天", threeToSeven: "第 3–7 天", last: "第 14 天起", five: "5 小时", weekName: "7 天", from: "从首次使用算起" },
  "zh-TW": { tab: "成長策略", first: "第 0–7 天", week: "第 3 天起", firstToThree: "第 0–3 天", threeToSeven: "第 3–7 天", last: "第 14 天起", from: "從首次使用算起" },
  ja: { tab: "成長ポリシー", first: "0〜7 日目", week: "3 日目から", firstToThree: "0〜3 日目", threeToSeven: "3〜7 日目", last: "14 日目から", from: "初めて使った時点" },
  de: { tab: "Wachstumsregel", first: "Tag 0 bis 7", week: "Ab Tag 3", firstToThree: "Tag 0 bis 3", threeToSeven: "Tag 3 bis 7", last: "Ab Tag 14", from: "ab seiner ersten Nutzung" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 900]) {
    for (const lang of Object.keys(want)) {
      test(`${engine} ${width} ${lang}: the growth policy's stages are shown, edited and saved`, async (t) => {
        const w = want[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 720 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posted));
        t.after(() => browser.close());
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=growth");
        await settingsDrawn(page);
        assert.equal((await page.locator("#setTab-growth").textContent()).trim(), w.tab);
        const rows = page.locator("#growthList .growth-stage");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 4, "magpie's four default stages");
        // the days count from an account's first use, not from when it was added
        assert((await page.locator("#growthList .growth-head .sub").textContent()).includes(w.from), "the days aren't said to count from first use");
        // a row's days run to the next row of its own window (T-128): the
        // 5 hours' 50% to day 7, past the week's stage on day 3
        assert.equal((await rows.nth(0).locator(".name").textContent()).trim(), w.first);
        assert.equal((await rows.nth(1).locator(".name").textContent()).trim(), w.week);
        assert.equal((await rows.nth(3).locator(".name").textContent()).trim(), w.last);
        if (w.five) {
          assert.equal((await rows.nth(0).locator(".segs .opt.on").textContent()).trim(), w.five);
          assert.equal((await rows.nth(1).locator(".segs .opt.on").textContent()).trim(), w.weekName);
        }
        assert.equal(await rows.nth(1).locator(".growth-cap").inputValue(), "80");
        assert(await page.locator("#growthList .growth-save").isDisabled(), "nothing to save before an edit");

        // the day-3 row turned to the 5 hours: the first row's days now run
        // to it, and its own to day 7; turned back, as they were
        await rows.nth(1).locator(".segs .opt").nth(0).click();
        assert.equal((await rows.nth(0).locator(".name").textContent()).trim(), w.firstToThree);
        assert.equal((await rows.nth(1).locator(".name").textContent()).trim(), w.threeToSeven);
        await rows.nth(1).locator(".segs .opt").nth(1).click();
        assert.equal((await rows.nth(0).locator(".name").textContent()).trim(), w.first);
        assert.equal((await rows.nth(1).locator(".name").textContent()).trim(), w.week);
        assert(await page.locator("#view-settings").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), "the settings scroll sideways");

        // a share out of range is said, and nothing is posted; typing it
        // lights Save at once
        await rows.nth(0).locator(".growth-cap").fill("120");
        assert(!(await page.locator("#growthList .growth-save").isDisabled()), "typing a share leaves Save off");
        await page.locator("#growthList .growth-save").click();
        await page.locator("#growthList .growth-head .sub.err").waitFor();
        assert.deepEqual(posted, []);

        // 60%, the last stage removed, one added: saved whole
        await rows.nth(0).locator(".growth-cap").fill("60");
        await rows.nth(3).locator(".growth-del").click();
        await page.locator("#growthList .growth-add").click();
        assert.equal(await rows.count(), 4);
        await page.locator("#growthList .growth-save").click();
        await until(() => posted.length === 1);
        await page.waitForFunction(() => document.querySelector("#growthList .growth-save")?.disabled);
        assert.deepEqual(posted, [{ stages: [
          { from: 0, window: "5h", cap: 60 }, { from: 3, window: "7d", cap: 80 },
          { from: 7, window: "5h", cap: 90 }, { from: 8, window: "5h", cap: 90 },
        ] }]);

        // Use default puts magpie's own back
        await page.locator("#growthList .growth-default").click();
        await until(() => posted.length === 2);
        await page.waitForFunction(() => document.querySelector("#growthList .growth-default")?.hidden);
        assert.deepEqual(posted.at(-1), { stages: null });
        assert.equal(await rows.nth(0).locator(".growth-cap").inputValue(), "50");
        assert.deepEqual(errors, []);
      });
    }
  }
}
