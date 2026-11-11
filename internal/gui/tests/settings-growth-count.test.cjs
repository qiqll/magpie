// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Growth policy (T-134): a policy of the user's counts its days
// on the calendar (every day since its first use) or by use (only the days
// the gateway served the account), and says what each means, an account
// magpie doesn't send requests for counting calendar days. A policy saved
// before says Calendar days and is saved as it was (no countDays); one
// switched to Days used asks for its chart and is saved with countDays
// "used", and the list says so. A built-in one has no choice: its ramp
// counts the days used (需求方 2026-11-11), said under it and in the list,
// and a new policy starts on Days used. Every
// language, at a narrow and a wide window, in each engine asked for; no
// backend, the API is faked here. SHOTS=<dir> keeps a screenshot of each.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test, after } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);
const until = async (ok) => { for (let i = 0; !ok(); i++) { if (i > 500) throw new Error("never came"); await new Promise((r) => setTimeout(r, 10)); } };
const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, countDays: "used", builtin: true },
  { id: "gentle", name: "Gentle", kind: "ramp", start: 70, days: 7, countDays: "used", builtin: true },
  { id: "cautious", name: "Cautious", kind: "ramp", start: 30, days: 30, countDays: "used", builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];
const SLOW = { id: "slow", name: "Slow", kind: "ramp", start: 20, days: 30 };
const IDLE = { id: "idle", name: "Idle", kind: "stages", stages: [{ from: 0, window: "5h", cap: 40 }], countDays: "used" };
const USED_NOTE = "Only a day the gateway served a request of the account counts, so an idle account stays on its day. An account magpie doesn't send requests for (Claude Code kept to the agent, Codex switch-only) counts calendar days.";
const POINTS = Array.from({ length: 10 }, (_, i) => ({ day: i, fiveHours: 20 + i, week: 20 + i }));

function server(lang, saves, previews) {
  let cur = {
    theme: "light", lang, tray: "panel", currency: "usd", proxy: "", redactWords: [],
    version: "0.1.1220", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    growthPolicyList: [...BUILTINS, SLOW, IDLE],
    growthInEffect: "slow",
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/growth/preview") {
      previews.push(req.postDataJSON().policy);
      return json({ points: POINTS, accounts: [], uses: [] });
    }
    if (url.pathname === "/api/settings/growth-policies") {
      const body = req.postDataJSON();
      saves.push(body);
      cur = { ...cur, growthPolicyList: [...BUILTINS, ...body.policies], growthInEffect: body.default };
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

// one browser an engine for the file, each test in a context of its own:
// fewer launches for a busy machine to hang on
const browsers = {};
const browserOf = (engine) => (browsers[engine] ||= engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
after(async () => { for (const b of Object.values(browsers)) await (await b).close(); });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 1000]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const x = (s, a) => tr(lang, s, a);
      test(`${engine} ${width} ${lang}: a policy counts calendar days or the days used`, async (t) => {
        const context = await (await browserOf(engine)).newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        t.after(() => context.close());
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saves = [], previews = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, saves, previews));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=growth");
        await settingsDrawn(page);
        const list = page.locator("#growthList");
        await list.locator(".gp-item").first().waitFor();
        const on = () => list.locator(".growth-count .opt.on").textContent();
        const note = () => list.locator(".growth-count-note").textContent();

        // saved before T-134: calendar days, said so
        assert.equal(await list.locator(".gp-item.on").getAttribute("data-id"), "slow");
        assert.equal((await on()).trim(), x("Calendar days"));
        assert.equal(await list.locator(".growth-count .segs").getAttribute("aria-label"), x("How its days are counted"));
        assert.equal((await note()).trim(), x("Every day since its first use counts, used or not."));
        assert.equal((await list.locator('.gp-item[data-id="slow"] .gp-sum').textContent()).trim(), x("{start}% to 100% in {days} days", { start: 20, days: 30 }));
        // one by use says so in the list, and is shown so
        assert.equal((await list.locator('.gp-item[data-id="idle"] .gp-sum').textContent()).trim(), x("{said} · days used", { said: x("1 stage") }));
        await list.locator('.gp-item[data-id="idle"]').click();
        assert.equal((await on()).trim(), x("Days used"));
        assert.equal((await note()).trim(), x(USED_NOTE));
        await until(() => previews.some((p) => p.id === "idle" && p.countDays === "used"));
        // a built-in one has no choice
        await list.locator('.gp-item[data-id="standard"]').click();
        assert.equal(await list.locator(".growth-count").count(), 0);
        assert.equal((await note()).trim(), x(USED_NOTE));
        assert.equal((await list.locator('.gp-item[data-id="standard"] .gp-sum').textContent()).trim(), x("{said} · days used", { said: x("{start}% to 100% in {days} days", { start: 50, days: 14 }) }));
        await list.locator('.gp-item[data-id="off"]').click();
        assert.equal(await list.locator(".growth-count-note").count(), 0);

        // switched to the days used: its chart is asked again, and it is saved so
        await list.locator('.gp-item[data-id="slow"]').click();
        await list.locator(".growth-count .opt", { hasText: x("Days used") }).click();
        assert.equal((await on()).trim(), x("Days used"));
        assert.equal((await note()).trim(), x(USED_NOTE));
        await until(() => previews.some((p) => p.id === "slow" && p.countDays === "used"));
        assert.equal((await list.locator('.gp-item[data-id="slow"] .gp-sum').textContent()).trim(), x("{said} · days used", { said: x("{start}% to 100% in {days} days", { start: 20, days: 30 }) }));
        assert(await page.locator("#view-settings").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), "the settings scroll sideways");
        if (process.env.SHOTS) await list.screenshot({ path: path.join(process.env.SHOTS, `policy-${engine}-${lang}-${width}.png`) });
        await list.locator(".growth-save").click();
        await until(() => saves.length === 1);
        assert.deepEqual(saves[0], { policies: [
          { id: "slow", name: "Slow", kind: "ramp", start: 20, days: 30, countDays: "used" },
          { id: "idle", name: "Idle", kind: "stages", stages: IDLE.stages, countDays: "used" },
        ], default: "slow" });

        // back to calendar days: saved with no countDays, as before T-134
        await page.waitForFunction(() => document.querySelector("#growthList .growth-save")?.disabled);
        await list.locator(".growth-count .opt", { hasText: x("Calendar days") }).click();
        await list.locator(".growth-save").click();
        await until(() => saves.length === 2);
        assert.deepEqual(saves[1].policies[0], { id: "slow", name: "Slow", kind: "ramp", start: 20, days: 30 });

        // a new policy counts the days used until changed
        await page.waitForFunction(() => document.querySelector("#growthList .growth-save")?.disabled);
        await list.locator(".growth-new").click();
        assert.equal((await on()).trim(), x("Days used"));
        assert.equal((await note()).trim(), x(USED_NOTE));
        await list.locator(".growth-save").click();
        await until(() => saves.length === 3);
        const made = saves[2].policies[2];
        assert.deepEqual({ ...made, id: "" }, { id: "", name: x("New policy"), kind: "ramp", start: 50, days: 14, countDays: "used" });
        assert.deepEqual(saves[2].policies[0], { id: "slow", name: "Slow", kind: "ramp", start: 20, days: 30 });
        assert.deepEqual(errors, []);
      });
    }
  }
}
