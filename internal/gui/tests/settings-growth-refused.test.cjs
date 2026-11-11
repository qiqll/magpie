// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Growth policy (T-139): a save the server refuses is said in
// the page's language by the refusal's code (settings.GrowthError), each
// of them: a range, a name or id twice, a built-in one changed, a default
// gone, a policy a subscription picked deleted, settings.json or
// providers.json that can't be read; a code the page doesn't know is the
// page's own words around what the server said. A chart the server can't
// draw is said by its code too. Every language, in each engine asked for;
// no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
// say is key in lang with vals filled in, as the page's t() has it
const say = (lang, key, vals = {}) => (lang === "en" ? key : I18N[lang][key] ?? key).replace(/\{(\w+)\}/g, (m, k) => vals[k] ?? m);

const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, countDays: "used", builtin: true },
  { id: "gentle", name: "Gentle", kind: "ramp", start: 70, days: 7, countDays: "used", builtin: true },
  { id: "cautious", name: "Cautious", kind: "ramp", start: 30, days: 30, countDays: "used", builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];
const RANGE = "A ramp starts at 1% to 99% and reaches 100% in 1 to 365 days";
const STAGE = "Each stage starts on a day from 0 to 365 and holds its window at 1% to 99%";
const WEEK = { en: "7 days", zh: "7 天", "zh-TW": "7 天", ja: "7 日", de: "7 Tage" };
// each refusal: what the server sends beside its English error, and the
// page's words for it
const REFUSALS = [
  [{ growth: "stage_day", got: "366" }, STAGE],
  [{ growth: "stage_cap", got: "100" }, STAGE],
  [{ growth: "stage_window", got: "1h" }, "A stage holds the 5 hours or the 7 days, not {got}"],
  [{ growth: "stage_twice", window: "7d", day: "3" }, "Two stages hold the {window} from day {day}"],
  [{ growth: "builtin", id: "gentle" }, "{id} is one of magpie's own policies, which can't be changed: copy it and change the copy"],
  [{ growth: "id", got: "a b" }, "A policy's id is up to 64 letters, digits, - and _, not {got}"],
  [{ growth: "id_twice", id: "r" }, "Two policies have the id {id}"],
  [{ growth: "name_needed", id: "r" }, "A policy needs a name"],
  [{ growth: "name_long", name: "x" }, "A policy's name is up to 64 characters"],
  [{ growth: "name_twice", name: "Slow" }, "Two policies are named {name}"],
  [{ growth: "default_gone", id: "gone" }, "The default policy ({id}) is gone: make another policy the default"],
  [{ growth: "count_days", got: "hours" }, "A policy counts calendar days or the days used, not {got}"],
  [{ growth: "ramp_start", got: "0" }, RANGE],
  [{ growth: "ramp_days", got: "366" }, RANGE],
  [{ growth: "kind", got: "off" }, "A policy ramps or has stages, not {got}"],
  [{ growth: "unreadable", path: "/h/.config/magpie/settings.json", cause: "is a directory" }, "magpie can't read {path}, so what it holds is unknown: {why}"],
  [{ growth: "picked", policy: "Slow", subs: "Codex · a@example.com" }, "{subs} picked the policy {policy}: pick another in its editor, or on its account's row, before deleting this one"],
  [{ growth: "picks_unreadable", cause: "is a directory" }, "magpie can't read which subscriptions picked a policy, so none is deleted: {why}"],
];
// what a few say, written out, so a dictionary the page doesn't read
// can't pass
const WRITTEN = {
  zh: { ramp_start: "magpie 没有保存这些策略：爬坡的起点为 1% 到 99%，在 1 到 365 天内到 100%", default_gone: "magpie 没有保存这些策略：默认策略（gone）已不存在：请把另一个策略设为默认", stage_twice: "magpie 没有保存这些策略：有两个阶段从第 3 天起都限制「7 天」窗口" },
  "zh-TW": { name_twice: "magpie 沒有儲存這些策略：有兩個策略都叫「Slow」" },
  ja: { builtin: "magpie はポリシーを保存できませんでした：穏やか は magpie の組み込みポリシーで変更できません：コピーしてコピーを変更してください" },
  de: { picked: "magpie hat die Regeln nicht gespeichert: Die Regel Slow ist gewählt von Codex · a@example.com: wähle dort im Editor oder in der Zeile des Kontos eine andere, bevor du diese löschst" },
};

function server(lang, answer) {
  const cur = {
    theme: "light", lang, tray: "panel", currency: "usd", proxy: "", redactWords: [],
    version: "0.1.1220", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    growthPolicyList: [...BUILTINS, { id: "slow", name: "Slow", kind: "ramp", start: 20, days: 60, countDays: "used" }],
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
      if (req.postDataJSON().policy.start === 41) return route.fulfill({ status: 400, json: { error: "a growth ramp starts at 1% to 99%, not 41%", code: "growth_refused", growth: "ramp_start", got: "41" } });
      return json({ points: [{ day: 0, fiveHours: 20, week: 20 }, { day: 60, fiveHours: 100, week: 100 }], accounts: [] });
    }
    if (url.pathname === "/api/settings/growth-policies") return route.fulfill({ status: 400, json: answer.next });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a refused growth policy is said in the page's language by its code`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1000, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], answer = { next: null };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, answer));
      t.after(() => browser.close());
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=growth");
      await settingsDrawn(page);
      const list = page.locator("#growthList");
      await list.locator(".growth-name").waitFor();
      await list.locator(".growth-name").fill("Slower");
      const wrap = say(lang, "magpie didn't save the policies: {why}", { why: "\u0000" });
      // the text shown once a save is refused with body
      const refused = async (body) => {
        answer.next = { error: `the English for ${body.growth || "nothing"}`, ...body };
        await page.evaluate(() => { const s = document.querySelector("#growthList .growth-head .sub"); if (s) s.textContent = ""; });
        await list.locator(".growth-save").click();
        await page.waitForFunction(() => document.querySelector("#growthList .growth-head .sub.err")?.textContent);
        return (await list.locator(".growth-head .sub.err").textContent()).trim();
      };

      for (const [vals, key] of REFUSALS) {
        const filled = vals.growth === "stage_twice" ? { ...vals, window: WEEK[lang] } : vals.growth === "builtin" ? { id: say(lang, "Gentle") } : vals.cause ? { ...vals, why: vals.cause } : vals;
        const want = wrap.replace("\u0000", say(lang, key, filled));
        const got = await refused({ code: "growth_refused", ...vals });
        assert.equal(got, want, vals.growth);
        if (lang !== "en") assert(!got.includes("the English for"), `${vals.growth} said in English`);
        if (WRITTEN[lang]?.[vals.growth]) assert.equal(got, WRITTEN[lang][vals.growth], vals.growth);
      }
      // a code the page doesn't know, or none: its own words around the
      // server's
      assert.equal(await refused({ code: "growth_refused", growth: "later_rule" }), wrap.replace("\u0000", "the English for later_rule"));
      assert.equal(await refused({}), wrap.replace("\u0000", "the English for nothing"));

      // a chart the server can't draw, by its code
      await list.locator(".growth-start").fill("41");
      const draw = say(lang, "magpie couldn't draw this policy: {why}", { why: say(lang, RANGE) });
      await page.waitForFunction((s) => document.querySelector("#growthList .growth-chart-err")?.textContent === s, draw);
      assert.deepEqual(errors, []);
    });
  }
}
