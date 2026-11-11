// Run with Node's test runner and Playwright on the module path; see README.md.
// T-156 (需求方 2026-11-11): a page's words and numbers can be selected and
// copied, not only a request's story (user-select.test.cjs): an account's
// name, state and requests on Accounts, a provider's name on Providers, a
// row's name in Settings, what Growth policy says of a policy, a
// Usage figure, a skill in Library, a plugin on Plugins. Its controls
// still can't: buttons, the policy list, the view switch. A drag across a
// provider's, a skill's or a plugin's name selects it and leaves what its
// row opens shut; a click still opens it. Every language, wide and narrow, in
// each engine asked for; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const later = (h) => new Date(Date.now() + h * 3600e3).toISOString();
const BUILTINS = [
  { id: "standard", name: "Standard", kind: "ramp", start: 50, days: 14, countDays: "used", builtin: true },
  { id: "gentle", name: "Gentle", kind: "ramp", start: 70, days: 7, countDays: "used", builtin: true },
  { id: "cautious", name: "Cautious", kind: "ramp", start: 30, days: 30, countDays: "used", builtin: true },
  { id: "off", name: "Off", kind: "off", builtin: true },
];
const POINTS = Array.from({ length: 15 }, (_, day) => ({ day, fiveHours: Math.min(100, 50 + day * 4), week: Math.min(100, 50 + day * 4) }));
const users = ["alice@example.com", "bob@example.com"];
const quotas = users.map((user, i) => ({ provider: "claude", name: "Claude", user, windows: [
  { name: "5 hours", used: [42, 7][i], resetsAt: later(2) }, { name: "7 days", used: [18, 3][i], resetsAt: later(50) },
] }));
const library = {
  dir: "/Users/me/.magpie/library", backups: "/Users/me/.magpie/backups", home: "/Users/me",
  agents: [{ id: "claude", name: "Claude Code", icon: "", skills: "/Users/me/.claude/skills" }],
  instructions: { agents: [] }, servers: [], skills: [{ name: "grill-me", description: "Asks hard questions about a plan", kind: "folder", agents: ["claude"] }], foundServers: [], projects: [], foundSkills: [],
};
const usage = { calls: 1234, errors: 5, input: 987654, output: 12345, cache_read: 0, cache_write: 0, reasoning: 0, cost: 4.5, unpriced: 0, accounts: [], callerKeys: [], agents: [], models: [], series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" };

function serve(lang) {
  const settings = {
    theme: "light", lang, tray: "panel", currency: "usd", proxy: "", redactWords: [],
    version: "0.1.1220", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    growthPolicyList: BUILTINS, growthInEffect: "standard",
  };
  const providers = { providers: [{
    id: "claude", name: "Claude", icon: "claudecode-color", models: [], agents: [], fallback: [], headers: {}, keyList: [],
    account: { agent: "claude", agentName: "Claude Code", user: users[0], plan: "max", logins: users.map((user, i) => ({ user, plan: "max", active: i === 0, on: true })) },
  }], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/login/usage") return json(Object.fromEntries(quotas.map((q) => [q.user, q])));
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/growth/preview") return json({ points: POINTS, accounts: [], uses: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.3.0", plugins: [{ spec: "opencode-copilot-auth", version: "0.0.7", providers: [] }] });
    if (url.pathname === "/api/plugins/npm") return json({ npm: { "opencode-copilot-auth": { version: "0.0.7" } } });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/library") return json(library);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

const selectOf = (loc) => loc.evaluate((e) => { const s = getComputedStyle(e); return s.userSelect || s.webkitUserSelect; });
// drag drags the mouse across loc's text, as a reader selecting it does,
// and gives what ended up selected
async function drag(page, loc) {
  await page.evaluate(() => getSelection().removeAllRanges());
  await loc.evaluate((e) => e.scrollIntoView({ block: "center", behavior: "instant" }));
  // a page is still opening when it is first drawn: wait till it stays
  let b = await loc.boundingBox();
  for (let i = 0; i < 50; i++) {
    await page.waitForTimeout(60);
    const n = await loc.boundingBox();
    if (n.x === b.x && n.y === b.y && n.width === b.width && n.width > 1) break;
    b = n;
  }
  const y = b.y + Math.min(b.height / 2, 8);
  await page.mouse.move(b.x + 1, y);
  await page.mouse.down();
  await page.mouse.move(b.x + b.width / 2, y, { steps: 4 });
  await page.mouse.move(b.x + b.width - 1, y, { steps: 4 });
  await page.mouse.up();
  await page.waitForTimeout(100); // a click the drag ended in has run
  return page.evaluate(() => String(getSelection()));
}
// every control on the page shown that could be selected: none may be
const loose = (page) => page.evaluate(() => {
  const sel = (e) => { const s = getComputedStyle(e); return s.userSelect || s.webkitUserSelect; };
  return [...document.querySelectorAll("main.view:not([hidden]) :is(button, button *, [role=tab], [role=button], .seg, .opt)")].filter((e) => sel(e) !== "none").map((e) => e.className || e.tagName);
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}: a page's words and numbers can be selected, its controls can't`, { timeout: 120e3 }, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang));

        // Accounts: its name, its state, its requests; its actions are buttons
        await page.goto("http://magpie.test/?view=accounts");
        const alice = page.locator('#acxList .row.acx[data-user="alice@example.com"]');
        await alice.locator(".acx-calls b").waitFor();
        for (const sel of [".acx-name", ".acx-state", ".acx-calls b", ".acx-wn", ".acx-pct"]) assert.equal(await selectOf(alice.locator(sel).first()), "text", "Accounts' " + sel);
        assert.equal(await drag(page, alice.locator(".acx-name")), await alice.locator(".acx-name").textContent());
        assert.equal(await selectOf(alice.locator(".acts button").first()), "none");
        assert.deepEqual(await loose(page), []);

        // Providers: a drag across its name selects it and opens nothing; a
        // click opens its editor
        await page.goto("http://magpie.test/?view=providers");
        const row = page.locator(".row.provider", { hasText: "Claude" }).first();
        const name = row.locator(".who .name");
        await name.waitFor();
        assert.equal(await selectOf(name), "text");
        assert.match(await drag(page, name), /^Claude/);
        assert.equal(await page.locator(".editor").count(), 0, "a drag across its name opened the editor");
        assert.match(await drag(page, row.locator(".who .sub")), new RegExp(users[0].replace(".", "\\.")));
        assert.equal(await page.locator(".editor").count(), 0);
        assert.deepEqual(await loose(page), []);
        await name.click();
        await page.locator(".editor").waitFor();

        // Settings: a row's name and what it says; its choices are controls
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings");
        await settingsDrawn(page);
        const pref = page.locator("#colorblindRow");
        assert.equal(await selectOf(pref.locator(".who .name")), "text");
        assert.equal((await drag(page, pref.locator(".who .name"))).trim(), (await pref.locator(".who .name").textContent()).trim());
        assert.equal(await selectOf(pref.locator(".segs button").first()), "none");
        assert.deepEqual(await loose(page), []);

        // Growth policy: its description; the policies are a list of buttons
        await page.goto("http://magpie.test/?view=settings&tab=growth");
        await settingsDrawn(page);
        const says = page.locator("#growthList .gp-edit .gp-note").first();
        await says.waitFor();
        assert.equal(await selectOf(says), "text");
        // a drag along its first line selects that line: all of it when it
        // fits on one
        const line = async (loc) => {
          const got = (await drag(page, loc)).trim();
          assert.ok(got.length > 8 && (await loc.textContent()).trim().startsWith(got), "selected " + JSON.stringify(got));
        };
        await line(says);
        await line(page.locator("#growthList .growth-inuse"));
        assert.equal(await selectOf(page.locator("#growthList .gp-item").first()), "none");

        // Usage: a figure and its label
        await page.goto("http://magpie.test/?view=usage");
        const kpi = page.locator("#view-usage .kpi:not(.loading-kpi) b").first();
        await kpi.waitFor();
        assert.equal(await selectOf(kpi), "text");
        assert.equal(await drag(page, kpi), await kpi.textContent());
        assert.deepEqual(await loose(page), []);

        // Library: a drag across a skill's name opens nothing; a click reads it
        await page.goto("http://magpie.test/");
        await page.evaluate(() => { show("library"); }); // not its promise, which waits on the page's loads
        // its Skills tab
        const kinds = page.locator("#view-library .lib-tabs");
        await kinds.waitFor();
        await kinds.locator(".opt").nth((await kinds.getAttribute("data-kind")).split("|").indexOf("skills")).click();
        const skill = page.locator("#view-library .lib-row.lib-skill .who .name").first();
        await skill.waitFor();
        assert.match(await drag(page, skill), /^grill-me/);
        assert.equal(await page.locator(".lib-skillfile").count(), 0, "a drag across a skill's name opened it");
        await skill.click();
        await page.locator(".lib-skillfile").waitFor();

        // Plugins: the same for an installed plugin's row and its detail
        await page.goto("http://magpie.test/?view=plugins");
        await page.locator('#view-plugins .lib-tabs .opt[data-sub="installed"]').click();
        const plugin = page.locator("#view-plugins .pm-row .name").first();
        await plugin.waitFor();
        assert.match(await drag(page, plugin), /opencode-copilot/); // its end may be cut off at 420px
        assert.equal(await page.locator(".pm-detail").count(), 0, "a drag across a plugin's name opened its detail");
        await plugin.click();
        await page.locator(".pm-detail").waitFor();
        assert.deepEqual(errors, []);
      });
    }
  }
}
