// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-25, T-279): beside each provider's users per account
// (T-243), Settings › Growth policy sets its low use: under that daily
// share of its week an account takes one free user more.
// - a last input a provider row, its low use or empty ("Off");
// - a provider with only a low use saved is a row too;
// - a low use typed is saved at once (settings/provider-low-use), empty
//   sends null, and a row with nothing else saved then goes;
// - Remove sends none for both its shares and its low use;
// - one out of 1 to 100 is said, not sent, and put back;
// - with no free tier there is no such input;
// - nothing is wider than the window.
// Every language, at a narrow and a wide window; no backend, the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);
// until waits for what the faked API was sent, a state, not a time
const until = async (ok) => { for (let i = 0; !ok(); i++) { if (i > 500) throw new Error("never came"); await new Promise((r) => setTimeout(r, 10)); } };

const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, countDays: "used", builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];
const TIERS = [{ name: "free", aliases: ["1"], share: 2 }, { name: "plus", aliases: ["2"], share: 1 }, { name: "pro", aliases: ["3"], share: 1 }, { name: "max", aliases: ["4"], share: 1 }];
const PROVIDERS = [{ id: "codex", name: "Codex" }, { id: "claude", name: "Claude" }];

function serve(lang, seen) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: seen.settings.fx });
    if (url.pathname === "/api/settings/provider-low-use") {
      const b = req.postDataJSON();
      seen.posts.push({ low: b });
      const all = { ...seen.settings.providerLowUse };
      if (b.percent != null) all[b.provider] = b.percent;
      else delete all[b.provider];
      seen.settings = { ...seen.settings, providerLowUse: all };
      return json(seen.settings);
    }
    if (url.pathname === "/api/settings/provider-shares") {
      const b = req.postDataJSON();
      seen.posts.push(b);
      const all = { ...seen.settings.providerShares };
      if (b.shares) all[b.provider] = b.shares;
      else delete all[b.provider];
      seen.settings = { ...seen.settings, providerShares: all };
      return json(seen.settings);
    }
    if (url.pathname === "/api/settings") return json(seen.settings);
    if (url.pathname === "/api/growth/preview") return json({ points: [{ day: 0, fiveHours: 50, week: 50 }, { day: 1, fiveHours: 100, week: 100 }], accounts: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

// each row shown: the provider, its shares set and its low use
const rows = (page) => page.evaluate(() => {
  const g = document.querySelector("#growthList .growth-shares-grid");
  if (!g) return [];
  return [...g.querySelectorAll(".gs-name")].map((n) => {
    const out = [n.textContent];
    for (let c = n.nextElementSibling; c && !c.classList.contains("gs-name"); c = c.nextElementSibling) {
      const i = c.querySelector(".gs-share");
      if (i && i.value) out.push(`${i.dataset.tier}=${i.value}`);
      const l = c.querySelector(".gs-low");
      if (l) out.push(`low=${l.value || "(" + l.placeholder + ")"}`);
    }
    return out.join(" ");
  });
});

const base = (lang, tiers) => ({
  lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], redactWords: [], visionModels: [], imageGenModels: [],
  fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [], growthPolicyList: BUILTINS, growthInEffect: "standard", bindIdleDefault: 60, bindIdleMost: 720,
  shareTiers: tiers, shareProviders: PROVIDERS, shareMost: 1000, lowUseMost: 100, providerShares: { codex: { free: 10 } }, providerLowUse: { claude: 10 },
});

async function open(t, engine, lang, width, seen) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, seen));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=growth");
  await settingsDrawn(page);
  const row = page.locator("#growthList .growth-shares");
  await row.waitFor();
  return { page, row, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [420, 1000]) {
      test(`${engine} ${lang} ${width}: free +1 under a low use is set by provider`, async (t) => {
        const seen = { posts: [], settings: base(lang, TIERS) };
        const { page, row, errors } = await open(t, engine, lang, width, seen);
        assert.equal((await row.locator(".growth-low-says").textContent()).trim(), x("Free +1: an account whose week is used, on average, less than this share a day (from its second day) takes one free user more than its share, once; a user who goes idle frees the seat, given again only while the use is still low. Empty is off."));
        assert.equal((await row.locator(".gs-low-head").textContent()).trim(), x("free +1 under"));
        const off = x("Off");
        assert.deepEqual(await rows(page), [`Claude low=10`, `Codex free=10 low=(${off})`]);
        const wide = async (what) => {
          const past = await page.evaluate(() => Math.max(...[...document.querySelectorAll("#growthList .growth-shares, #growthList .growth-shares *")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);
          assert.ok(past <= 0.5, `${what}: the row reaches ${past}px past the window`);
        };
        await wide("two providers");

        // a low use typed is saved at once
        const codexLow = row.locator(".gs-low").nth(1);
        await reach(page, codexLow);
        await codexLow.fill("12");
        await codexLow.press("Enter");
        await until(() => seen.posts.length === 1);
        assert.deepEqual(seen.posts[0], { low: { provider: "codex", percent: 12 } });
        await page.waitForFunction((said) => document.querySelector("#status.ok")?.textContent === said, x("Saved"));

        // one out of range, or 0, is said, not sent, and put back
        for (const bad of ["101", "0"]) {
          await codexLow.fill(bad);
          await codexLow.press("Enter");
          await page.waitForFunction(() => document.querySelector("#status.err"));
          assert.equal((await page.locator("#status").textContent()).trim(), x("Free +1 is under 1% to {n}% a day, or empty for off", { n: 100 }));
          assert.equal(await codexLow.inputValue(), "12");
          assert.equal(seen.posts.length, 1);
          await page.evaluate(() => document.querySelector("#status")?.classList.remove("err"));
        }

        // emptied, Claude's row has nothing else saved and goes
        const claudeLow = row.locator(".gs-low").first();
        await reach(page, claudeLow);
        await claudeLow.fill("");
        await claudeLow.press("Enter");
        await until(() => seen.posts.length === 2);
        assert.deepEqual(seen.posts[1], { low: { provider: "claude", percent: null } });
        await page.waitForFunction(() => document.querySelectorAll("#growthList .growth-shares-grid .gs-name").length === 1);
        assert.deepEqual(await rows(page), ["Codex free=10 low=12"]);

        // Remove sends none for its shares and its low use, and the row goes
        const remove = page.locator("#growthList .growth-shares-grid .gs-remove").first();
        await reach(page, remove);
        await remove.click();
        await until(() => seen.posts.length === 4);
        assert.deepEqual(seen.posts.slice(2), [{ provider: "codex", shares: null }, { low: { provider: "codex", percent: null } }]);
        await page.waitForFunction(() => !document.querySelector("#growthList .growth-shares-grid .gs-name"));
        assert.deepEqual(errors, []);
      });
    }
    test(`${engine} ${lang}: no free tier, no free +1`, async (t) => {
      const seen = { posts: [], settings: base(lang, TIERS.filter((x) => x.name !== "free")) };
      const { page, row, errors } = await open(t, engine, lang, 1000, seen);
      assert.equal(await row.locator(".gs-low, .gs-low-head, .growth-low-says").count(), 0);
      // a provider with only a low use saved isn't a row then
      assert.deepEqual(await rows(page), ["Codex"]);
      assert.deepEqual(errors, []);
    });
  }
}
