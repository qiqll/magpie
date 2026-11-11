// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-11, T-160): an account the growth policy holds while it
// ramps up is not "No cap" in the provider editor. Its cap pill says the
// share the gateway holds it to now, from the server's own count
// (accountGrowth, provider.AccountGrowthOf, read through the gateway's
// withGrowth), the 5 hours and the week apart when they differ, and its
// title names the policy. One skipped or past its ramp (the server sends
// no accountGrowth), says No cap; one with a cap of its own says its cap.
// The pool's "No cap on …" note leaves a ramping account out. Every
// language, at a phone's width and a wide one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const tr = (lang, s, args = {}) => Object.entries(args).reduce((a, [k, v]) => a.replaceAll(`{${k}}`, v), lang === "en" ? s : I18N[lang]?.[s] ?? s);
const inHours = (h) => new Date(Date.now() + h * 3600e3 + 60e3).toISOString();
const standard = { policy: "standard", name: "Standard", builtin: true, kind: "ramp" };
const ME = "me@example.com", NEW = "New@Example.com", MINE = "mine@example.com", DONE = "done@example.com", OWN = "own@example.com", WIN = "win@example.com";
const providers = () => ({
  providers: [{
    id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
    models: [{ id: "gpt-6", name: "", on: true }],
    agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
    account: {
      agent: "codex", agentName: "Codex", user: ME, plan: "PLUS",
      logins: [ME, NEW, MINE, DONE, OWN, WIN].map((user, i) => ({ user, plan: "PLUS", on: true, active: i === 0 })),
    },
    // as provider.AccountGrowthOf answers: me on Standard's day 2 at 57%,
    // the new one at 50% of its 5 hours and 80% of its week (a stages
    // policy), keyed in lower case; done is past its ramp or skipped, and
    // own has a cap of its own, so neither has one
    accountGrowth: {
      [ME]: { ...standard, day: 2, days: 14, fiveHours: 57, week: 57 },
      [NEW.toLowerCase()]: { policy: "mine", name: "Mine", kind: "stages", day: 4, fiveHours: 50, week: 80, next: 7 },
      // win capped its 5 hours itself; the policy still holds its week
      [WIN]: { ...standard, day: 2, days: 14, fiveHours: 100, week: 57 },
    },
    accountWindowCaps: { [WIN]: { "5 hours": 50 } },
    accountCaps: { [OWN]: 70 },
  }],
  presets: [], excluded: [], gateway: { running: true, window: true },
});
const win = (name, used) => ({ name, capId: name.toLowerCase(), used, resetsAt: inHours(4), capped: true });
const use = () => Object.fromEntries([ME, NEW, MINE, DONE, OWN, WIN].map((u) => [u, { provider: "codex", windows: [win("5 hours", 10), win("Weekly", 10)] }]));

function serve(lang) {
  const ps = providers();
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(ps);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/login/usage") return json(url.searchParams.get("agent") === "codex" ? use() : {});
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const KEYS = ["Ramping up {n}%", "Ramping up 5h {five}% · week {week}%",
  "Ramping up: {policy} holds this account at {five}% of its 5 hours and {week}% of its week for now, and more as its days go by. A cap set here holds in its place. Click to change"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 1100]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      test(`${engine} ${width} ${lang}: a ramping account's cap pill says the share the policy holds it to now`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts .acc .acap").first().waitFor();
        const pill = (id) => page.locator(`.editor .accts .acc[data-account-id="${id}"] .acap`);

        // the same share on both windows: one number, set, on the row's line
        assert.equal(await pill(ME).textContent(), tr(lang, "Ramping up {n}%", { n: 57 }));
        assert.match(await pill(ME).getAttribute("class"), /\bset\b/);
        const title = await pill(ME).getAttribute("title");
        assert.ok(title.startsWith(tr(lang, KEYS[2], { policy: "Standard", five: 57, week: 57 })), title);
        // the 5 hours and the week apart, its policy named
        assert.equal(await pill(NEW).textContent(), tr(lang, KEYS[1], { five: 50, week: 80 }));
        assert.ok((await pill(NEW).getAttribute("title")).startsWith(tr(lang, KEYS[2], { policy: "Mine", five: 50, week: 80 })));
        // no hold from the server: No cap; a cap of its own: its cap
        for (const id of [MINE, DONE]) assert.equal(await pill(id).textContent(), tr(lang, "No cap"), id);
        assert.equal(await pill(OWN).textContent(), tr(lang, "Cap {n}%", { n: 70 }));
        // a window capped by the user beside a ramp: caps by window, and its
        // title still says the week the policy holds (T-160 review M1)
        assert.equal(await pill(WIN).textContent(), tr(lang, "Caps by window"));
        assert.ok((await pill(WIN).getAttribute("title")).startsWith(tr(lang, KEYS[2], { policy: "Standard", five: 100, week: 57 })));

        // the pool's note names the accounts used to 100% only
        const note = page.locator(".editor .pool-ends .note");
        assert.equal(await note.textContent(), tr(lang, "No cap on {users}: used to 100% of each window", { users: `${MINE}, ${DONE}` }));

        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "no page scroll sideways");
        // its menu ticks nothing: No cap is not what holds it (review M2)
        await pill(ME).click();
        await page.locator(".proto-menu .pm-item").first().waitFor();
        assert.equal(await page.locator(".proto-menu .pm-item.on").count(), 0);
        await pill(OWN).click();
        await page.locator(".proto-menu .pm-item.on").first().waitFor();
        if (lang !== "en") {
          const missing = await page.evaluate(([l, keys]) => keys.filter((k) => !I18N[l][k]), [lang, KEYS]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
