// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-19, T-243): how many users an account of a provider may
// be bound to, by the user's tier, is set per provider on Settings ›
// Growth policy, beside the idle time (T-242):
// - a provider with shares of its own is a row, an input a tier: its
//   share, or empty with user-tiers.json's as the placeholder;
// - a share typed is saved at once (settings/provider-shares) with the
//   row's others;
// - a provider is added from a menu of those without shares, and Remove
//   sends none for it, the row gone;
// - a share out of 0 to 1000 is said and not sent;
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

// each row shown: the provider and its inputs' values and placeholders
const rows = (page) => page.evaluate(() => {
  const g = document.querySelector("#growthList .growth-shares-grid");
  if (!g) return [];
  return [...g.querySelectorAll(".gs-name")].map((n) => {
    const out = [n.textContent];
    for (let c = n.nextElementSibling; c && !c.classList.contains("gs-name"); c = c.nextElementSibling) {
      const i = c.querySelector(".gs-share");
      if (i) out.push(`${i.dataset.tier}=${i.value || "(" + i.placeholder + ")"}`);
    }
    return out.join(" ");
  });
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [420, 1000]) {
      test(`${engine} ${lang} ${width}: users per account are set by provider and tier`, async (t) => {
        const seen = {
          posts: [],
          settings: {
            lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], redactWords: [], visionModels: [], imageGenModels: [],
            fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [], growthPolicyList: BUILTINS, growthInEffect: "standard", bindIdleDefault: 60, bindIdleMost: 720,
            shareTiers: TIERS, shareProviders: PROVIDERS, shareMost: 1000, providerShares: { codex: { free: 10 } },
          },
        };
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
        assert.equal((await row.locator(".who .name").textContent()).trim(), x("Users per account, by provider"));
        assert.equal((await row.locator(".who .sub").textContent()).trim(), x("With the gateway bound by user, how many users one account of a provider may be bound to, by each user's tier. Empty follows user-tiers.json (its number shown); 1 is the user's own alone, 0 any number. An account is shared by users of one tier only; users of several tiers bound to one before keep it."));
        assert.deepEqual(await rows(page), ["Codex free=10 plus=(1) pro=(1) max=(1)"]);
        const wide = async (what) => {
          const past = await page.evaluate(() => Math.max(...[...document.querySelectorAll("#growthList .growth-shares, #growthList .growth-shares *")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);
          assert.ok(past <= 0.5, `${what}: the row reaches ${past}px past the window`);
        };
        await wide("one provider");

        // a share typed is saved at once, with the row's others
        const plus = row.locator('.gs-share[data-tier="plus"]').first();
        await reach(page, plus);
        await plus.fill("2");
        await plus.press("Enter");
        await until(() => seen.posts.length === 1);
        assert.deepEqual(seen.posts[0], { provider: "codex", shares: { free: 10, plus: 2 } });
        await page.waitForFunction((said) => document.querySelector("#status.ok")?.textContent === said, x("Saved"));

        // a provider added from the menu, its row empty until a share is typed
        // brought into sight as a reader does: the app puts back a scroll
        // that isn't the reader's, and a menu open then is closed by it
        await reach(page, row.locator(".gs-add"));
        await row.locator(".gs-add").click();
        await page.waitForFunction(() => document.querySelector(".proto-menu"));
        const menu = await page.evaluate(() => {
          const m = document.querySelector(".proto-menu"), r = m.getBoundingClientRect();
          return { head: m.querySelector(".pm-head").textContent, items: [...m.querySelectorAll(".pm-name")].map((n) => n.textContent), past: Math.max(r.right - document.documentElement.clientWidth, -r.left) };
        });
        assert.equal(menu.head, x("A provider with shares of its own"));
        assert.deepEqual(menu.items, ["Claude"]);
        assert.ok(menu.past <= 0.5, `the menu reaches ${menu.past}px past the window`);
        await page.locator(".proto-menu .pm-item").first().click();
        await page.waitForFunction(() => document.querySelectorAll("#growthList .growth-shares-grid .gs-name").length === 2);
        assert.deepEqual(await rows(page), ["Claude free=(2) plus=(1) pro=(1) max=(1)", "Codex free=10 plus=2 pro=(1) max=(1)"]);
        assert.equal(await page.locator("#growthList .gs-add").isDisabled(), true);
        await wide("two providers");
        const free = page.locator('#growthList .growth-shares-grid .gs-share[data-tier="free"]').first();
        await reach(page, free);
        await free.fill("3");
        await free.press("Enter");
        await until(() => seen.posts.length === 2);
        assert.deepEqual(seen.posts[1], { provider: "claude", shares: { free: 3 } });

        // Remove sends none for it, and its row goes
        const remove = page.locator("#growthList .growth-shares-grid .gs-remove").nth(1);
        await reach(page, remove);
        await remove.click();
        await until(() => seen.posts.length === 3);
        assert.deepEqual(seen.posts[2], { provider: "codex", shares: null });
        await page.waitForFunction(() => document.querySelectorAll("#growthList .growth-shares-grid .gs-name").length === 1);
        assert.deepEqual(await rows(page), ["Claude free=3 plus=(1) pro=(1) max=(1)"]);
        // one out of range is said, not sent, and put back (last: a long
        // refusal floats over the page's foot)
        const left = page.locator('#growthList .growth-shares-grid .gs-share[data-tier="free"]').first();
        await reach(page, left);
        await left.fill("1001");
        await left.press("Enter");
        await page.waitForFunction(() => document.querySelector("#status.err"));
        assert.equal((await page.locator("#status").textContent()).trim(), x("An account is shared by 1 to {n} users, or 0 for any number", { n: 1000 }));
        assert.equal(await left.inputValue(), "3");
        assert.equal(seen.posts.length, 3);
        assert.deepEqual(errors, []);
      });
    }
  }
}
