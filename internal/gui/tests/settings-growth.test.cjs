// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Growth policy (T-078, T-130): the policies are listed, magpie's
// four and the user's own stages, the default marked and shown first. Its
// stage rows read as before (a row's days run to the next row of its own
// window) with a line saying a window a later stage leaves out keeps its
// share (T-136). The chart is what growth/preview answers for the policy
// shown, its numbers the server's (11% and 22% on day 0 here, which no
// formula of the page's would give), the account on its day in the
// tooltip, from the keyboard too. A built-in one is read only and copied;
// the copy's ramp out of range is said in the page's language and nothing
// is saved; a change asks for its chart again, and one the server can't
// draw says why; a refused save is said in the
// page's language; the policies are saved whole with the default. Every
// language, at a narrow and a wide window, in each engine asked for; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
// until waits for what the faked API was sent, a state, not a time
const until = async (ok) => { for (let i = 0; !ok(); i++) { if (i > 500) throw new Error("never came"); await new Promise((r) => setTimeout(r, 10)); } };
const STAGES = [
  { from: 0, window: "5h", cap: 50 }, { from: 3, window: "7d", cap: 80 },
  { from: 7, window: "5h", cap: 90 }, { from: 14, window: "5h", cap: 95 },
];
const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, countDays: "used", builtin: true },
  { id: "gentle", name: "Gentle", kind: "ramp", start: 70, days: 7, countDays: "used", builtin: true },
  { id: "cautious", name: "Cautious", kind: "ramp", start: 30, days: 30, countDays: "used", builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];
// the server's curve: numbers of its own, so a chart drawn from anything
// else shows other ones
const POINTS = [{ day: 0, fiveHours: 11, week: 22 }, { day: 1, fiveHours: 33, week: 44 }, ...Array.from({ length: 8 }, (_, i) => ({ day: i + 2, fiveHours: 100, week: 100 }))];
const ACCOUNTS = [{ provider: "codex", name: "Codex", account: "new@example.com", day: 1 }];

function server(lang, saves, previews) {
  let cur = {
    theme: "light", lang, tray: "panel", currency: "usd", proxy: "", redactWords: [],
    version: "0.1.1220", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    growthPolicyList: [...BUILTINS, { id: "mine", name: "My stages", kind: "stages", stages: STAGES }],
    growthInEffect: "mine",
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/growth/preview") {
      const policy = req.postDataJSON().policy;
      previews.push(policy);
      if (policy.start === 41) return route.fulfill({ status: 400, json: { error: "a growth ramp starts at 1% to 99%, not 41%" } });
      return json({ points: POINTS, accounts: ACCOUNTS });
    }
    if (url.pathname === "/api/settings/growth-policies") {
      const body = req.postDataJSON();
      if (body.policies.some((p) => p.name === "refused")) return route.fulfill({ status: 400, json: { error: 'two growth policies are named "refused"' } });
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

const want = {
  en: { rampLine: "Starts at {start} % reaches 100% in {days} days", tab: "Growth policy", first: "Days 0 to 7", week: "From day 3 on", firstToThree: "Days 0 to 3", threeToSeven: "Days 3 to 7", last: "From day 14 on", standard: "Standard", note: "used to let such a window go", ramp: "A ramp starts at 1% to 99% and reaches 100% in 1 to 365 days", refused: "magpie didn't save the policies:", draw: "magpie couldn't draw this policy:", from: "from its first use" },
  zh: { rampLine: "起点 {start} % {days} 天到 100%", tab: "成长策略", first: "第 0–7 天", week: "第 3 天起", firstToThree: "第 0–3 天", threeToSeven: "第 3–7 天", last: "第 14 天起", standard: "标准", note: "以前会直接放开", ramp: "爬坡的起点为 1% 到 99%，在 1 到 365 天内到 100%", refused: "magpie 没有保存这些策略：", draw: "magpie 画不出这个策略：", from: "从首次使用算起" },
  "zh-TW": { rampLine: "起點 {start} % {days} 天到 100%", tab: "成長策略", first: "第 0–7 天", week: "第 3 天起", firstToThree: "第 0–3 天", threeToSeven: "第 3–7 天", last: "第 14 天起", standard: "標準", note: "以前會直接放開", ramp: "爬坡的起點為 1% 到 99%，在 1 到 365 天內到 100%", refused: "magpie 沒有儲存這些策略：", draw: "magpie 畫不出這個策略：", from: "從首次使用算起" },
  ja: { rampLine: "開始 {start} % {days} 日で 100%", tab: "成長ポリシー", first: "0〜7 日目", week: "3 日目から", firstToThree: "0〜3 日目", threeToSeven: "3〜7 日目", last: "14 日目から", standard: "標準", note: "制限を外していました", ramp: "ランプは 1%〜99% から始まり、1〜365 日で 100% に達します", refused: "magpie はポリシーを保存できませんでした：", draw: "magpie はこのポリシーを描けませんでした：", from: "初めて使った時点" },
  de: { rampLine: "Beginnt bei {start} % erreicht 100 % in {days} Tagen", tab: "Wachstumsregel", first: "Tag 0 bis 7", week: "Ab Tag 3", firstToThree: "Tag 0 bis 3", threeToSeven: "Tag 3 bis 7", last: "Ab Tag 14", standard: "Standard", note: "Früher gab magpie", ramp: "Ein Anstieg beginnt bei 1 % bis 99 % und erreicht 100 % in 1 bis 365 Tagen", refused: "magpie hat die Regeln nicht gespeichert:", draw: "magpie konnte diese Regel nicht zeichnen:", from: "ab seiner ersten Nutzung" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 1000]) {
    for (const lang of Object.keys(want)) {
      test(`${engine} ${width} ${lang}: the growth policies are listed, drawn, edited and saved`, async (t) => {
        const w = want[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saves = [], previews = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, saves, previews));
        t.after(() => browser.close());
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=growth");
        await settingsDrawn(page);
        assert.equal((await page.locator("#setTab-growth").textContent()).trim(), w.tab);
        const list = page.locator("#growthList");
        const items = list.locator(".gp-item");
        await items.first().waitFor();
        assert.equal(await items.count(), 5, "magpie's four and the user's own");
        assert.equal((await items.nth(0).locator(".gp-name").textContent()).trim(), w.standard);
        // the default is shown, marked
        assert.equal(await list.locator(".gp-item.on").getAttribute("data-id"), "mine");
        assert.equal(await list.locator('.gp-item[data-id="mine"] .gp-tag.def').count(), 1);
        assert((await list.locator(".growth-head .sub").textContent()).includes(w.from), "the days aren't said to count from first use");

        // its stages: a row's days run to the next row of its own window
        // (T-128), and the page says so (T-136)
        const rows = list.locator(".growth-stage");
        assert.equal(await rows.count(), 4);
        assert.equal((await rows.nth(0).locator(".gp-days").textContent()).trim(), w.first);
        assert.equal((await rows.nth(1).locator(".gp-days").textContent()).trim(), w.week);
        assert.equal((await rows.nth(3).locator(".gp-days").textContent()).trim(), w.last);
        assert((await list.locator(".growth-stages-note").textContent()).includes(w.note));
        await rows.nth(1).locator(".segs .opt").nth(0).click();
        assert.equal((await rows.nth(0).locator(".gp-days").textContent()).trim(), w.firstToThree);
        assert.equal((await rows.nth(1).locator(".gp-days").textContent()).trim(), w.threeToSeven);
        await rows.nth(1).locator(".segs .opt").nth(1).click();
        assert.equal((await rows.nth(0).locator(".gp-days").textContent()).trim(), w.first);
        assert(await list.locator(".growth-delete").isDisabled(), "the default can be deleted");

        // the chart is the server's answer for the policy shown
        await until(() => previews.some((p) => p.kind === "stages"));
        assert.deepEqual(previews.find((p) => p.kind === "stages").stages, STAGES);
        await list.locator(".gp-svg path.ln.five").waitFor();
        assert.equal(await list.locator(".gp-svg path.ln.week").count(), 1);
        assert.equal(await list.locator(".gp-svg circle.acct").count(), 1, "the account on its day");
        await list.locator(".gp-svg").focus();
        const tip = list.locator(".gp-tip");
        await tip.waitFor();
        assert.match(await tip.textContent(), /11%.*22%/);
        await page.keyboard.press("ArrowRight");
        assert.match(await tip.textContent(), /33%.*44%.*new@example\.com/);

        // a built-in policy is read only, and copied
        await list.locator('.gp-item[data-id="standard"]').click();
        assert.equal(await list.locator(".growth-name").count(), 0);
        assert.equal(await list.locator(".growth-delete").count(), 0);
        await until(() => previews.some((p) => p.id === "standard"));
        await list.locator(".growth-copy").click();
        assert.equal(await items.count(), 6);
        assert.match(await list.locator(".gp-item.on").getAttribute("data-id"), /^p-/);
        const name = await list.locator(".growth-name").inputValue();
        assert(name.includes(w.standard), `the copy is named ${name}`);
        assert(!(await list.locator(".growth-save").isDisabled()), "a copy leaves Save off");
        // the ramp's days read as days, not the core's weekday word (T-147)
        const rampLine = await list.locator(".gp-line:has(.growth-days)").evaluate((line) =>
          [...line.children].map((c) => (c.matches(".growth-days") ? "{days}" : c.matches(".growth-start") ? "{start}" : c.textContent.trim())).join(" "));
        assert.equal(rampLine, w.rampLine);

        // a ramp out of range is said in the page's language; nothing is saved
        await list.locator(".growth-start").fill("120");
        await list.locator(".growth-save").click();
        await list.locator(".growth-head .sub.err").waitFor();
        assert((await list.locator(".growth-head .sub.err").textContent()).includes(w.ramp));
        assert.deepEqual(saves, []);
        // a chart the server can't draw says why, in the page's language
        await list.locator(".growth-start").fill("41");
        await page.waitForFunction((s) => document.querySelector("#growthList .growth-chart-err")?.textContent.startsWith(s), w.draw);
        // a share typed asks for its chart again
        await list.locator(".growth-start").fill("40");
        await until(() => previews.some((p) => p.start === 40 && p.days === 14));
        await page.waitForFunction(() => !document.querySelector("#growthList .growth-chart-err"));
        await list.locator(".growth-make-default").click();
        assert.equal(await list.locator(".gp-item.on .gp-tag.def").count(), 1);

        // the server's refusal, in the page's language
        await list.locator(".growth-name").fill("refused");
        await list.locator(".growth-save").click();
        await page.waitForFunction((s) => document.querySelector("#growthList .growth-head .sub.err")?.textContent.startsWith(s), w.refused);

        // saved whole, with the default; the copy of Standard counts the
        // days used, as Standard does (需求方 2026-11-11)
        await list.locator(".growth-name").fill("Slow");
        const id = await list.locator(".gp-item.on").getAttribute("data-id");
        await list.locator(".growth-save").click();
        await until(() => saves.length === 1);
        await page.waitForFunction(() => document.querySelector("#growthList .growth-save")?.disabled);
        assert.deepEqual(saves[0], { policies: [
          { id: "mine", name: "My stages", kind: "stages", stages: STAGES },
          { id, name: "Slow", kind: "ramp", start: 40, days: 14, countDays: "used" },
        ], default: id });
        assert.equal(await list.locator(`.gp-item[data-id="${id}"] .gp-tag.def`).count(), 1);
        assert.match(await list.locator(".growth-inuse").textContent(), /Slow/);

        // a policy no longer the default is deleted
        await list.locator('.gp-item[data-id="mine"]').click();
        await list.locator(".growth-delete").click();
        assert.equal(await items.count(), 5);
        await list.locator(".growth-save").click();
        await until(() => saves.length === 2);
        assert.deepEqual(saves[1].policies.map((p) => p.id), [id]);
        assert(await page.locator("#view-settings").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), "the settings scroll sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
