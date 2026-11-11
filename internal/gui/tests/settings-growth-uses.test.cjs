// Run with Node's test runner and Playwright on the module path; see README.md.
// T-145: Settings › Growth policy's In use lists every pick the server
// refuses the policy's delete by, as provider.GrowthUses answers: a
// subscription turned off that picked it, one Providers has no row of,
// and an account its subscription no longer lists. Delete is disabled
// for each; the two with no other place to be changed are cleared here
// (provider/growthpolicy, provider/accountgrowthpolicy with ""), and the
// chart is asked again, whose In use then leaves them out; an answer
// asked before the clear that comes back after it is not drawn. Every
// language, at a wide and a narrow width; no backend, the API is faked
// here. SHOTS=<dir> keeps a screenshot of each. A policy shown again has
// Delete off from the start, from the In use that came back before (T-144).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);

const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, countDays: "used", builtin: true },
  { id: "gentle", name: "Gentle", kind: "ramp", start: 70, days: 7, countDays: "used", builtin: true },
  { id: "cautious", name: "Cautious", kind: "ramp", start: 30, days: 30, countDays: "used", builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];
const SLOW = { id: "slow", name: "Slow", kind: "ramp", start: 20, days: 60 };
const POINTS = Array.from({ length: 61 }, (_, d) => ({ day: d, fiveHours: Math.min(100, 20 + Math.floor((80 * d) / 60)), week: Math.min(100, 20 + Math.floor((80 * d) / 60)) }));

function serve(lang, ctl) {
  const settings = () => ({ lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], proxies: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, growthPolicyList: [...BUILTINS, SLOW], growthInEffect: ctl.def || "standard" });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings(), fx: settings().fx });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/provider/growthpolicy" || url.pathname === "/api/provider/accountgrowthpolicy") {
      const b = req.postDataJSON();
      ctl.posts.push([url.pathname.slice(5), b]);
      if (ctl.refuse) return route.fulfill({ status: 400, json: { error: "providers.json can't be read" } });
      if (url.pathname.endsWith("/growthpolicy")) ctl.claudePicked = false;
      else ctl.gone = ctl.gone.filter((g) => !(g.id === b.id && g.account === b.account));
      return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    }
    if (url.pathname === "/api/growth/preview") {
      ctl.previews++;
      const id = req.postDataJSON().policy.id;
      // as provider.GrowthUses answers: Codex, turned off, picked Slow and
      // an account it no longer lists picked it too; Claude, signed out
      // and so with no row, picked it, and one of its accounts
      const goneOf = (sub) => ctl.gone.filter((g) => g.id === sub).map((g) => g.account);
      const uses = [];
      if (id === "slow") {
        if (!ctl.noCodex) uses.push({ provider: "codex", name: "Codex", picked: true, accounts: 0, accountsPicked: goneOf("codex").length || undefined, off: true, gone: goneOf("codex") });
        const claude = goneOf("claude");
        if (ctl.claudePicked || claude.length) uses.push({ provider: "claude", name: "Claude", picked: ctl.claudePicked || undefined, accounts: 0, accountsPicked: claude.length || undefined, accountsOnly: !ctl.claudePicked || undefined, unlisted: true, gone: claude });
      }
      const body = { points: POINTS, accounts: [], uses: JSON.parse(JSON.stringify(uses)) };
      if (ctl.hold) {
        // answered with what was so when asked, once the test lets it go
        ctl.hold = false;
        await new Promise((r) => { ctl.release = r; ctl.onHeld(); });
      }
      return json(body);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
const lines = (page) => page.evaluate(() => [...document.querySelectorAll("#growthList .growth-uses .growth-use")].map((e) => e.textContent.trim()));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}: In use lists every pick that refuses the delete, and the leftovers are cleared there`, async (t) => {
        const ctl = { posts: [], previews: 0, refuse: false, claudePicked: true, gone: [{ id: "codex", account: "gone@example.com" }, { id: "claude", account: "old@example.com" }] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        // a held answer is let go before the browser closes
        t.after(() => { ctl.release?.(); return browser.close(); });
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, ctl));
        await page.goto("http://magpie.test/?view=settings&tab=growth");
        await page.locator("#setTab-growth").click();
        const list = page.locator("#growthList");
        await list.locator('.gp-item[data-id="slow"]').click();
        await list.locator(".growth-uses .growth-gone-pick").first().waitFor();

        const clear = x("Clear the pick");
        const own1 = x("1 account picked it");
        const codex = x("{name} picked it", { name: x("{name} (turned off)", { name: "Codex" }) }) + " · " + own1;
        const codexGone = x("{account} picked it, but {name} no longer lists it", { account: "gone@example.com", name: "Codex" }) + " " + clear;
        const claude = x("{name} picked it", { name: x("{name} (not in Providers)", { name: "Claude" }) }) + " · " + own1 + " " + clear;
        const claudeGone = x("{account} picked it, but {name} no longer lists it", { account: "old@example.com", name: "Claude" }) + " " + clear;
        assert.deepEqual(await lines(page), [codex, codexGone, claude, claudeGone]);
        // the one turned off has its editor: nothing to clear on its own line
        assert.equal(await list.locator(".growth-use").first().locator(".growth-clear").count(), 0);
        const del = list.locator(".growth-delete");
        assert.equal(await del.isDisabled(), true);
        assert.equal(await del.getAttribute("title"), x("{names} picked it: clear what is left here, or pick another policy in its editor or on its accounts' rows, first", { names: "Codex, Claude" }));
        assert.ok(await sideways(page) <= 1, "In use scrolls sideways");
        if (process.env.SHOTS) {
          // tall enough that the foot is on screen, then back
          await page.setViewportSize({ width, height: 2400 });
          await list.locator(".growth-foot").screenshot({ path: path.join(process.env.SHOTS, `uses-${engine}-${lang}-${width}.png`) });
          await page.setViewportSize({ width, height: 900 });
        }

        // shown again after another, its Delete is built off from the In use
        // that came back before, while its chart is still being asked (T-144)
        await list.locator('.gp-item[data-id="standard"]').click();
        await page.waitForFunction(() => !document.querySelector("#growthList .growth-uses .growth-gone-pick"));
        const again = new Promise((r) => { ctl.onHeld = r; });
        ctl.hold = true;
        await list.locator('.gp-item[data-id="slow"]').click();
        await Promise.race([again, new Promise((_, no) => setTimeout(() => no(new Error("Slow's chart wasn't asked again")), 5000))]);
        assert.equal(await list.locator(".growth-uses .growth-use").count(), 0, "In use is still being asked");
        assert.equal(await del.isDisabled(), true, "Delete could be pressed before In use came back");
        assert.equal(await del.getAttribute("title"), x("{names} picked it: clear what is left here, or pick another policy in its editor or on its accounts' rows, first", { names: "Codex, Claude" }));
        ctl.release();
        await list.locator(".growth-uses .growth-gone-pick").first().waitFor();
        assert.equal(await del.isDisabled(), true);

        // a clear the server refuses says so, and stays to be tried again
        ctl.refuse = true;
        await list.locator(".growth-gone-pick .growth-clear").first().click();
        await page.waitForFunction(() => !document.querySelector("#growthList .growth-gone-pick .growth-clear")?.disabled);
        assert.equal(ctl.posts.length, 1);
        assert.deepEqual(await lines(page), [codex, codexGone, claude, claudeGone]);
        ctl.refuse = false;
        ctl.posts = [];

        // an answer asked before a clear that comes back after it is not
        // drawn: only the last ask's is (the chart asked again on a draw,
        // a request still out as the clear's comes back)
        const held = new Promise((r) => { ctl.onHeld = r; });
        ctl.hold = true;
        await list.locator('.gp-item[data-id="slow"]').click();
        await Promise.race([held, new Promise((_, no) => setTimeout(() => no(new Error("the chart wasn't asked again on a click")), 5000))]);
        await list.locator('.gp-item[data-id="slow"]').click();
        await page.waitForFunction(() => document.querySelectorAll("#growthList .growth-gone-pick").length === 2);

        // an account gone: its pick cleared by its name, the chart asked again
        const asked = ctl.previews;
        await list.locator(".growth-gone-pick", { hasText: "gone@example.com" }).locator(".growth-clear").click();
        await page.waitForFunction((n) => document.querySelectorAll("#growthList .growth-gone-pick").length === n, 1);
        assert.deepEqual(ctl.posts, [["provider/accountgrowthpolicy", { id: "codex", account: "gone@example.com", growthPolicy: "" }]]);
        assert.ok(ctl.previews > asked, "the chart wasn't asked again");
        const late = page.waitForResponse((r) => r.url().endsWith("/api/growth/preview"));
        ctl.release();
        await (await late).finished();
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        assert.equal(await list.locator(".growth-gone-pick").count(), 1, "an answer asked before the clear was drawn over it");
        // Claude's own pick, with no row to change it on
        await list.locator(".growth-use", { hasText: x("{name} (not in Providers)", { name: "Claude" }) }).locator(".growth-clear").click();
        const claudeLeft = x("{name}: {own}", { name: x("{name} (not in Providers)", { name: "Claude" }), own: own1 });
        await page.waitForFunction((want) => [...document.querySelectorAll("#growthList .growth-uses .growth-use")].some((e) => e.textContent.trim() === want), claudeLeft);
        assert.deepEqual(ctl.posts.at(-1), ["provider/growthpolicy", { id: "claude", growthPolicy: "" }]);
        // and its account's: Claude is no longer listed, Codex is, picked
        await list.locator(".growth-gone-pick", { hasText: "old@example.com" }).locator(".growth-clear").click();
        const codexLeft = x("{name} picked it", { name: x("{name} (turned off)", { name: "Codex" }) });
        await page.waitForFunction((want) => { const ls = [...document.querySelectorAll("#growthList .growth-uses .growth-use")].map((e) => e.textContent.trim()); return ls.length === 1 && ls[0] === want; }, codexLeft);
        assert.deepEqual(ctl.posts.at(-1), ["provider/accountgrowthpolicy", { id: "claude", account: "old@example.com", growthPolicy: "" }]);
        assert.equal(await del.isDisabled(), true);
        assert.equal(await del.getAttribute("title"), x("{names} picked it: pick another policy in its editor first", { names: "Codex" }));
        assert.ok(await sideways(page) <= 1, "In use scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}

// the default's Delete stays off with its own reason, whatever picks it
// and once nothing does (review H2 of T-144: Clear the pick turned it on)
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    test(`${engine} ${lang}: the default policy's Delete stays off after its picks are cleared`, async (t) => {
      const ctl = { posts: [], previews: 0, refuse: false, claudePicked: true, def: "slow", noCodex: true, gone: [{ id: "claude", account: "old@example.com" }] };
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => { ctl.release?.(); return browser.close(); });
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, ctl));
      await page.goto("http://magpie.test/?view=settings&tab=growth");
      await page.locator("#setTab-growth").click();
      const list = page.locator("#growthList");
      await list.locator('.gp-item[data-id="slow"]').click();
      await list.locator(".growth-uses .growth-gone-pick").waitFor();
      const del = list.locator(".growth-delete");
      const why = x("Make another policy the default first");
      assert.equal(await del.isDisabled(), true);
      assert.equal(await del.getAttribute("title"), why);
      await list.locator(".growth-use", { hasText: x("{name} (not in Providers)", { name: "Claude" }) }).locator(".growth-clear").first().click();
      await page.waitForFunction(() => ![...document.querySelectorAll("#growthList .growth-uses .growth-use:not(.growth-gone-pick) .growth-clear")].length);
      await list.locator(".growth-gone-pick .growth-clear").click();
      await page.waitForFunction((none) => [...document.querySelectorAll("#growthList .growth-uses .growth-use")].map((e) => e.textContent.trim()).join() === none, x("No subscription is held by it"));
      assert.equal(await del.isDisabled(), true, "the default's Delete could be pressed");
      assert.equal(await del.getAttribute("title"), why);
      assert.deepEqual(errors, []);
    });
  }
}
