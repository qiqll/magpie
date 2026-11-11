// Run with Node's test runner and Playwright on the module path; see README.md.
// T-154: each account on Models › Accounts has a pill for its paid period,
// from accountRenewal in /api/providers (renewal.Of on the server: nothing
// is counted on the page). None told says Set start date; 6 days left says
// its end; 5 days left and less asks for a renewal; one past its end says
// Expired. The summary counts those due. The pill opens a date box: Save
// posts provider/accountopened with the day's start, Clear posts "". Every
// language, at a wide and a phone's width, in the list and the cards; no
// backend, the API is faked here.
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
const day = 864e5;
// a period as the server says it: opened 30 days before it ends
const period = (opened, daysLeft, due) => ({ opened, ends: new Date(new Date(opened).getTime() + 30 * day).toISOString(), daysLeft, ...(due ? { due } : {}) });
const USERS = ["none@example.com", "six@example.com", "five@example.com", "gone@example.com"];

const providersOf = (seen) => {
  const renewal = {
    "six@example.com": period("2026-10-17T00:00:00Z", 6),
    "five@example.com": period("2026-10-16T00:00:00Z", 5, "soon"),
    "gone@example.com": period("2026-09-01T00:00:00Z", 0, "ended"),
  };
  if (seen.opened) renewal["none@example.com"] = period(seen.opened, 30);
  if (seen.cleared) delete renewal["gone@example.com"];
  return {
    providers: [{
      id: "codex", name: "ChatGPT", icon: "openai", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
      account: { agent: "codex", agentName: "Codex", user: USERS[0], plan: "plus", logins: USERS.map((user, i) => ({ user, plan: "plus", active: i === 0, on: true })) },
      accountRenewal: renewal,
    }],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
};

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providersOf(seen));
    if (url.pathname === "/api/provider/accountopened") {
      const body = req.postDataJSON();
      seen.posts.push(body);
      if (body.opened) seen.opened = body.opened;
      else seen.cleared = true;
      return json(providersOf(seen));
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ accounts: [] });
    if (url.pathname === "/api/accounts/keys") return json({ keys: [] });
    if (url.pathname === "/api/auth-holds") return json({ holds: [], hours: 12 });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

// each account's pill: its words and class
const pills = (page, sel) => page.evaluate((sel) => Object.fromEntries([...document.querySelectorAll(sel)].map((r) => {
  const b = r.querySelector(".renew-pill");
  return [r.dataset.user, b ? [b.textContent, [...b.classList].filter((c) => c !== "renew-pill").join(" ")] : null];
})), sel);
const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#acxList .renew-pill, #acxList .renew-edit > *")].map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: an account's paid period is said, set and cleared`, async (t) => {
        const seen = { posts: [] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=accounts");
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx .renew-pill").length === 4);

        const ends = (iso) => page.evaluate((iso) => new Date(iso).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" }), iso);
        const want = {
          "none@example.com": [x("Set start date"), "none"],
          "six@example.com": [x("Ends {date}", { date: await ends(period("2026-10-17T00:00:00Z", 6).ends) }), "ok"],
          "five@example.com": [x("Renew within {n} days", { n: 5 }), "soon"],
          "gone@example.com": [x("Expired: renew"), "ended"],
        };
        assert.deepEqual(await pills(page, "#acxList .row.acx"), want);
        assert.ok((await page.locator("#acxSum").textContent()).includes(x("{n} to renew", { n: 2 })), "the summary counts the two due");
        assert.equal(await page.locator("#acxSum .acx-warn").last().textContent(), x("{n} to renew", { n: 2 }));
        assert.ok(await wider(page) <= 0.5, `a pill reaches ${await wider(page)}px past the window`);

        // the pill opens a date box; Save posts the day's start in the reader's zone
        await page.locator('#acxList .row.acx[data-user="none@example.com"] .renew-pill').click();
        const box = page.locator('#acxList .row.acx[data-user="none@example.com"] .renew-edit');
        assert.equal(await box.locator(".renew-clear").count(), 0, "nothing to clear yet");
        assert.ok(await wider(page) <= 0.5, `the date box reaches ${await wider(page)}px past the window`);
        await box.locator(".renew-date").fill("2026-11-02");
        await box.locator(".renew-save").click();
        await page.waitForFunction(() => !document.querySelector("#acxList .renew-edit"));
        assert.deepEqual(seen.posts, [{ id: "codex", account: "none@example.com", opened: "2026-11-01T16:00:00.000Z" }]);
        const now = await pills(page, "#acxList .row.acx");
        assert.deepEqual(now["none@example.com"], [x("Ends {date}", { date: await ends(period("2026-11-01T16:00:00.000Z", 30).ends) }), "ok"]);

        // Clear posts "" and the pill says none told again
        await page.locator('#acxList .row.acx[data-user="gone@example.com"] .renew-pill').click();
        const gone = page.locator('#acxList .row.acx[data-user="gone@example.com"] .renew-edit');
        assert.equal(await gone.locator(".renew-date").inputValue(), "2026-09-01");
        await gone.locator(".renew-clear").click();
        await page.waitForFunction(() => !document.querySelector("#acxList .renew-edit"));
        assert.deepEqual(seen.posts[1], { id: "codex", account: "gone@example.com", opened: "" });
        assert.deepEqual((await pills(page, "#acxList .row.acx"))["gone@example.com"], [x("Set start date"), "none"]);
        assert.equal(await page.locator("#acxSum .acx-warn").last().textContent(), x("1 to renew"));

        // Cancel and Escape close the box and post nothing
        await page.locator('#acxList .row.acx[data-user="six@example.com"] .renew-pill').click();
        await page.locator("#acxList .renew-cancel").click();
        await page.locator('#acxList .row.acx[data-user="six@example.com"] .renew-pill').click();
        await page.locator("#acxList .renew-date").press("Escape");
        await page.waitForFunction(() => !document.querySelector("#acxList .renew-edit"));
        assert.equal(seen.posts.length, 2);

        // the cards say the same
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card .renew-pill").length === 4);
        const cards = await pills(page, "#acxList .acx-card");
        assert.deepEqual(cards["five@example.com"], want["five@example.com"]);
        assert.deepEqual(cards["gone@example.com"], [x("Set start date"), "none"]);
        assert.ok(await wider(page) <= 0.5, `a card's pill reaches ${await wider(page)}px past the window`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
