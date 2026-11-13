// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-13), after a screenshot of another accounts page:
// - Models › Accounts has Add account, Refresh all usage and Refresh list
//   over the list. Add account asks the account's type first (Claude only
//   for now), then runs the sign-in Providers runs, drawn on this page:
//   Start sign-in posts /api/signin, Back cancels it and goes back to the
//   type, a setup-token added says so here and opens no provider editor,
//   and the account is listed;
// - Refresh all usage reads every account's allowance (usage/quotas
//   asked=1&all=1);
// - a card has its letter and today's, 7 and 30 days' cost and requests;
//   Sort orders the accounts, kept per machine;
// - Proxy IPs' form takes a proxy with its username and password in boxes
//   of their own, as Settings › Network's saved proxies do: Save keeps it
//   with the IP, its login escaped in the address, and Read its exit IP
//   fills the address from it (proxy/check).
// Every language, at a wide and a phone's width; no backend, the API is
// faked here.
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
const later = new Date(Date.now() + 2 * 3600e3).toISOString();
const daysAgo = (n) => new Date(Date.now() - n * 864e5).toISOString();

function providersOf(seen) {
  const logins = [
    { user: "alice@example.com", plan: "max", active: true, on: true },
    { user: "bob@example.com", plan: "pro", on: true },
    ...(seen.added ? [{ user: seen.added, token: true, on: true }] : []),
  ];
  return {
    providers: [{ id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", models: [], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
      account: { agent: "claude", agentName: "Claude", user: "alice@example.com", plan: "max", logins },
      accountAdded: { "alice@example.com": daysAgo(20), "bob@example.com": daysAgo(3) } }],
    presets: [], excluded: [], gateway: { running: true, window: true },
  };
}
const w = (name, used, trayKind) => ({ name, used, resetsAt: later, trayKind });
const quotas = [
  { provider: "claude", name: "Claude", user: "alice@example.com", windows: [w("5 hours", 42, "5h"), w("7 days", 18, "7d")] },
  { provider: "claude", name: "Claude", user: "bob@example.com", windows: [w("5 hours", 90, "5h"), w("7 days", 64, "7d")] },
];
// each period's usage by account: bob costs the most over 30 days
const usageOf = (period) => ({ accounts: {
  "last:5h": [{ provider: "claude", account: "alice@example.com", calls: 3, errors: 0, input: 900, output: 100, cost: 0.5 }],
  today: [{ provider: "claude", account: "alice@example.com", calls: 8, errors: 0, input: 1000000, output: 92802, cost: 4.25 }],
  "7d": [{ provider: "claude", account: "alice@example.com", calls: 20, errors: 0, input: 2e6, output: 1e5, cost: 6 },
    { provider: "claude", account: "bob@example.com", calls: 4, errors: 0, input: 1000, output: 0, cost: 1 }],
  "30d": [{ provider: "claude", account: "alice@example.com", calls: 30, errors: 0, input: 3e6, output: 1e5, cost: 7 },
    { provider: "claude", account: "bob@example.com", calls: 50, errors: 0, input: 4e6, output: 2e5, cost: 9 }],
}[period] || [] });

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", proxyNow: "", proxySource: "none", trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [], visionModels: [], imageGenModels: [], workbuddyCheckins: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providersOf(seen));
    if (url.pathname === "/api/usage/quotas") { seen.quotaReads.push(url.search); return json(quotas); }
    if (url.pathname === "/api/usage") { seen.usage.push(url.searchParams.get("period")); return json(usageOf(url.searchParams.get("period"))); }
    if (url.pathname === "/api/signin") { seen.starts.push(req.postDataJSON()); return json({ id: "s1", agent: "claude", state: "waiting", url: "https://vendor.example/oauth" }); }
    if (url.pathname === "/api/signin/s1/cancel") { seen.cancels++; return json({}); }
    if (url.pathname === "/api/signin/s1") return json({ id: "s1", state: "waiting", url: "https://vendor.example/oauth" });
    if (url.pathname === "/api/signin/claude-token") { seen.added = "Claude setup-token 9f9f9f"; return json({ user: seen.added }); }
    if (url.pathname === "/api/ips") {
      if (req.method() === "POST") { const b = req.postDataJSON(); seen.ipPosts.push(b); seen.ips = b.ips; seen.rev++; }
      return json({ ips: seen.ips, rev: "r" + seen.rev, days: 30, warnDays: 5 });
    }
    if (url.pathname === "/api/proxy/check") {
      seen.checks.push(req.postDataJSON());
      return json({ source: "settings", through: { ip: req.postDataJSON().proxy.includes("plain") ? "198.51.100.88" : "198.51.100.77", loc: "US", ms: 80 }, direct: { ip: "192.0.2.1" } });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

const settle = (page) => page.waitForTimeout(150);
const wider = (page, id) => page.evaluate((id) => Math.max(0, ...[...document.querySelectorAll(id + " *")].filter((e) => e.offsetParent).map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth, id);

async function open(t, engine, lang, width, view, seen) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, seen));
  await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
  await page.goto("http://magpie.test/?view=" + view);
  return [page, errors];
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: Accounts adds a Claude account in place`, async (t) => {
        const seen = { starts: [], usage: [], quotaReads: [], cancels: 0, ipPosts: [], checks: [], ips: [], rev: 1 };
        const [page, errors] = await open(t, engine, lang, width, "accounts", seen);
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 2);
        for (const [id, s] of [["#acxAdd", "Add account"], ["#acxReadAll", "Refresh all usage"], ["#acxReload", "Refresh list"]]) {
          assert.equal((await page.locator(id).textContent()).trim(), x(s));
          assert.ok(await page.locator(id).isVisible(), id + " in sight");
        }

        // Refresh all usage reads every account's allowance
        await page.locator("#acxReadAll").click();
        await page.waitForFunction(() => !document.querySelector("#acxReadAll.busy"));
        assert.ok(seen.quotaReads.some((q) => q.includes("asked=1") && q.includes("all=1")), "every account's allowance asked for: " + seen.quotaReads.join(" "));

        // the type first: Claude alone
        await page.locator("#acxAdd").click();
        assert.deepEqual(await page.locator("#acxAdding .acx-add-type").evaluateAll((bs) => bs.map((b) => b.dataset.agent)), ["claude"]);
        assert.equal(await page.locator("#acxAdding .acx-add-note").textContent(), x("Pick the type of account to add."));
        assert.ok(await wider(page, "#acxAdding") <= 0.5, "the types inside the window");
        // then Providers' own sign-in, here: nothing is sent till it starts
        await page.locator("#acxAdding .acx-add-type").click();
        await page.locator("#acxAdding .signing.ready").waitFor();
        assert.equal(seen.starts.length, 0);
        assert.ok(await page.locator("#view-accounts").isVisible(), "still on Accounts");
        assert.equal(await page.locator("#modal").isVisible(), false, "no provider editor opened");
        await page.locator("#acxAdding .sign-start").click();
        // Claude with accounts already asks nothing more: its page opens
        await page.waitForFunction(() => document.querySelector("#acxAdding .signing") && !document.querySelector("#acxAdding .signing.ready"));
        assert.deepEqual(seen.starts.map((s) => s.agent), ["claude"]);
        assert.ok(await wider(page, "#acxAdding") <= 0.5, "the sign-in inside the window");
        // Back cancels it and asks the type again
        await page.locator("#acxAdding .acx-add-back").click();
        await page.locator("#acxAdding .acx-add-type").waitFor();
        await page.waitForFunction(() => signing === null);
        assert.equal(seen.cancels, 1);

        // a setup-token: added here, said here, listed below, no editor
        await page.locator("#acxAdding .acx-add-type").click();
        await page.locator("#acxAdding .ready-token").click();
        await page.locator("#acxAdding .claude-token input").fill("sk-ant-oat01-test");
        await page.locator("#acxAdding .claude-token button.primary").click();
        await page.locator("#acxAdding .acx-add-done").waitFor();
        assert.equal(await page.locator("#acxAdding .acx-add-done span").textContent(), x("{user} added: it is listed below", { user: "Claude setup-token 9f9f9f" }));
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 3);
        await settle(page);
        assert.equal(await page.locator("#modal").isVisible(), false, "signedIn's editor stays shut");
        assert.ok(await page.locator("#view-accounts").isVisible());
        assert.equal(await page.evaluate(() => editing), null);
        // Close shuts it
        await page.locator("#acxAdding .acx-add-close").click();
        assert.equal(await page.locator("#acxAdding").isVisible(), false);
        // the Providers page is drawn when opened: Claude's three accounts
        await page.evaluate(() => show("providers"));
        await page.waitForFunction(() => document.querySelector('#providers .row.provider[data-id="claude"]'));
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: Accounts' cards say each period, and Sort orders them`, async (t) => {
        const seen = { starts: [], usage: [], quotaReads: [], cancels: 0, ipPosts: [], checks: [], ips: [], rev: 1 };
        const [page, errors] = await open(t, engine, lang, width, "accounts", seen);
        await page.waitForFunction(() => document.querySelectorAll("#acxList .row.acx").length === 2);
        // the list alone reads no period but the last 5 hours
        assert.deepEqual(seen.usage.filter((p) => p !== "last:5h"), []);
        await page.locator("#acxView button", { hasText: x("Card view") }).click();
        await page.waitForFunction(() => document.querySelector('#acxList .acx-card[data-user="bob@example.com"] .acx-span[data-span="30d"] b')?.textContent.includes("9"));
        assert.deepEqual([...new Set(seen.usage)].sort(), ["30d", "7d", "last:5h", "today"]);
        const card = (u) => page.evaluate((u) => {
          const c = document.querySelector(`#acxList .acx-card[data-user="${u}"]`);
          return { letter: c.querySelector(".acx-avatar").textContent, spans: [...c.querySelectorAll(".acx-span")].map((s) => [s.dataset.span, s.querySelector(".acx-span-k").textContent, s.querySelector("b").textContent, s.querySelector(".acx-span-n").textContent]) };
        }, u);
        const fmt = (n) => page.evaluate((n) => fmtN(n), n);
        assert.deepEqual(await card("alice@example.com"), { letter: "A", spans: [
          ["today", x("Today"), "$4.25", x("{n} req · {tok} tok", { n: "8", tok: await fmt(1092802) })],
          ["7d", x("7 days"), "$6.00", x("{n} req · {tok} tok", { n: "20", tok: await fmt(2100000) })],
          ["30d", x("30 days"), "$7.00", x("{n} req · {tok} tok", { n: "30", tok: await fmt(3100000) })],
        ] });
        // none today: $0, not unknown
        assert.equal((await card("bob@example.com")).spans[0][2], "$0.000");
        assert.ok(await wider(page, "#acxList") <= 0.5, "the cards inside the window");

        const users = () => page.locator("#acxList .acx-card").evaluateAll((cs) => cs.map((c) => c.dataset.user));
        assert.deepEqual(await users(), ["alice@example.com", "bob@example.com"]);
        assert.equal(await page.locator("#acxSort").textContent(), x("Providers' order"));
        const pick = async (name) => {
          await page.locator("#acxSort").click();
          await page.locator(".sess-menu .pm-item").filter({ has: page.locator(".pm-name").getByText(name, { exact: true }) }).click();
          await settle(page);
        };
        await pick(x("30 days' cost, most first"));
        assert.deepEqual(await users(), ["bob@example.com", "alice@example.com"]);
        await pick(x("5 hours used, most first"));
        assert.deepEqual(await users(), ["bob@example.com", "alice@example.com"]);
        await pick(x("Newest first"));
        assert.deepEqual(await users(), ["bob@example.com", "alice@example.com"]);
        await pick(x("Name"));
        assert.deepEqual(await users(), ["alice@example.com", "bob@example.com"]);
        await pick(x("7 days used, most first"));
        assert.deepEqual(await users(), ["bob@example.com", "alice@example.com"]);
        // kept per machine
        await page.reload();
        await page.waitForFunction(() => document.querySelectorAll("#acxList .acx-card").length === 2);
        assert.equal(await page.locator("#acxSort").textContent(), x("7 days used, most first"));
        assert.deepEqual(await users(), ["bob@example.com", "alice@example.com"]);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: an IP takes a proxy with its username and password`, async (t) => {
        const seen = { starts: [], usage: [], quotaReads: [], cancels: 0, ipPosts: [], checks: [], ips: [{ ip: "203.0.113.7", name: "Osaka" }], rev: 1 };
        const [page, errors] = await open(t, engine, lang, width, "ips", seen);
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 1);
        await page.locator("#ipsAdd").click();
        const form = page.locator("#ipsList .ips-form");
        assert.equal(await form.locator(".ips-in-proxy").getAttribute("aria-label"), x("Proxy address"));
        assert.equal(await form.locator(".ips-in-user").getAttribute("placeholder"), x("Username (optional)"));
        assert.equal(await form.locator(".ips-in-pass").getAttribute("placeholder"), x("Password (optional)"));
        assert.equal(await form.locator(".ips-in-pass").getAttribute("type"), "password");
        assert.equal(await form.locator(".ips-read-exit").isDisabled(), true, "nothing to read yet");
        await form.locator(".ips-in-proxy").fill("socks5://us.example:1080");
        await form.locator(".ips-in-user").fill("me@x");
        await form.locator(".ips-in-pass").fill("p:w/d#1");
        assert.ok(await wider(page, "#view-ips") <= 0.5, "the form inside the window");
        // Read its exit IP asks through the typed proxy, login and all
        await form.locator(".ips-read-exit").click();
        await page.waitForFunction(() => document.querySelector("#ipsList .ips-in-ip")?.value === "198.51.100.77");
        const typed = "socks5://me%40x:p%3Aw%2Fd%231@us.example:1080";
        assert.deepEqual(seen.checks.map((c) => c.proxy), [typed]);
        // the login stayed in its boxes over the redraw
        assert.equal(await page.locator("#ipsList .ips-in-user").inputValue(), "me@x");
        assert.equal(await page.locator("#ipsList .ips-in-pass").inputValue(), "p:w/d#1");
        await page.locator("#ipsList .ips-in-name").fill("US line");
        await page.locator("#ipsList .ips-save").click();
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 2);
        assert.deepEqual(seen.ipPosts.at(-1).ips.at(-1), { ip: "198.51.100.77", name: "US line", proxies: [typed] });

        // Save with no address but a proxy: its exit is the address
        seen.checks.length = 0;
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.7"] .ips-edit').click();
        await page.locator("#ipsList .ips-in-ip").fill("");
        await page.locator("#ipsList .ips-in-proxy").fill("http://plain.example:3128");
        await page.locator("#ipsList .ips-save").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .ips-form"));
        assert.deepEqual(seen.checks.map((c) => c.proxy), ["http://plain.example:3128"]);
        assert.deepEqual(seen.ipPosts.at(-1).ips[0], { ip: "198.51.100.88", name: "Osaka", proxies: ["http://plain.example:3128"] });
        assert.deepEqual(errors, []);
      });
    }
  }
}
