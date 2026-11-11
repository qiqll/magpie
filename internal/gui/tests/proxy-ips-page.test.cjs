// Run with Node's test runner and Playwright on the module path; see README.md.
// T-155: Models › Proxy IPs lists the proxy exit IPs from /api/ips, each
// with the period pill Models › Accounts' rows have (renew.js), its period
// the server's (renewal.Of). An IP is added, its name edited, its start
// date set and cleared, and removed, each posting the whole list; one
// added from a saved proxy takes that proxy's exit (proxy/check) and its
// name. A list the server refuses says why and keeps the form. The page's
// tab is in the sidebar under Accounts. Every language, at a wide and a
// phone's width; no backend, the API is faked here.
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
// the server's answer: each IP with its period, worked out from a fixed
// "now" so the fixture says the same days wherever it runs
const NOW = Date.parse("2026-11-11T04:00:00Z");
const periodOf = (opened) => {
  if (!opened) return undefined;
  const ends = Date.parse(opened) + 30 * day, left = ends - NOW;
  const daysLeft = left <= 0 ? 0 : Math.ceil(left / day);
  return { opened, ends: new Date(ends).toISOString(), daysLeft, ...(left <= 0 ? { due: "ended" } : daysLeft <= 5 ? { due: "soon" } : {}) };
};
const answer = (ips, rev) => ({ ips: ips.map((x) => ({ ...x, ...(x.opened ? { renewal: periodOf(x.opened) } : {}) })), rev: "r" + rev, days: 30, warnDays: 5 });

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", version: "test", dir: "/test", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    proxies: [{ name: "Tokyo line", url: "http://me:pw@tokyo.example:3128" }] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/ips") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        seen.posts.push(body);
        if (body.rev !== "r" + seen.rev) return json({ error: "the IPs were changed elsewhere since they were read" }, 400);
        if (body.ips.some((x) => x.ip === "bad")) return json({ error: "\"bad\" is not an IP address" }, 400);
        seen.ips = body.ips.map((x) => Object.fromEntries(Object.entries(x).filter(([, v]) => v !== undefined && v !== "")));
        seen.rev++;
      }
      return json(answer(seen.ips, seen.rev));
    }
    if (url.pathname === "/api/proxy/check") {
      seen.checks.push(req.postDataJSON());
      return json({ source: "settings", through: { ip: "198.51.100.42", loc: "JP", ms: 80 }, direct: { ip: "192.0.2.1" } });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

const rows = (page) => page.evaluate(() => [...document.querySelectorAll("#ipsList .ips-row")].map((r) => {
  const b = r.querySelector(".renew-pill");
  return [r.dataset.ip, r.querySelector(".sub")?.textContent || "", b.textContent, [...b.classList].filter((c) => c !== "renew-pill").join(" ")];
}));
const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#view-ips *")].filter((e) => e.offsetParent).map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: proxy IPs are listed, added, edited, renewed and removed`, async (t) => {
        const seen = { posts: [], checks: [], rev: 1, ips: [
          { ip: "203.0.113.7", name: "Osaka", opened: "2026-10-17T04:00:00Z" },
          { ip: "203.0.113.8", opened: "2026-10-18T04:00:00Z" },
          { ip: "2001:db8::1", name: "Old", opened: "2026-09-01T00:00:00Z" },
        ] };
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce", timezoneId: "Asia/Shanghai" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, seen));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=ips");
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 3);
        assert.equal(await page.locator("#pageTitle").textContent(), x("Proxy IPs"));
        assert.equal(await page.locator('#nav button[data-view="ips"] span').textContent(), x("Proxy IPs"));
        const ends = (iso) => page.evaluate((iso) => new Date(iso).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" }), iso);

        assert.deepEqual(await rows(page), [
          ["203.0.113.7", "Osaka", x("Renew within {n} days", { n: 5 }), "soon"],
          ["203.0.113.8", "", x("Ends {date}", { date: await ends("2026-11-17T04:00:00.000Z") }), "ok"],
          ["2001:db8::1", "Old", x("Expired: renew"), "ended"],
        ]);
        assert.equal(await page.locator("#ipsSum").textContent(), x("{n} IPs", { n: 3 }) + " · " + x("{n} to renew", { n: 2 }));
        assert.ok(await wider(page) <= 0.5, `the page reaches ${await wider(page)}px past the window`);

        // add one from the saved proxy: its exit and its name fill the form
        await page.locator("#ipsAdd").click();
        await page.locator("#ipsList .ips-from").click();
        await page.locator(".proto-menu .pm-item", { hasText: "Tokyo line" }).click();
        await page.waitForFunction(() => document.querySelector("#ipsList .ips-in-ip")?.value === "198.51.100.42");
        assert.deepEqual(seen.checks, [{ proxy: "http://me:pw@tokyo.example:3128" }]);
        assert.equal(await page.locator("#ipsList .ips-in-name").inputValue(), "Tokyo line");
        assert.ok(await wider(page) <= 0.5, `the form reaches ${await wider(page)}px past the window`);
        await page.locator("#ipsList .ips-save").click();
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 4 && !document.querySelector("#ipsList .ips-form"));
        assert.deepEqual(seen.posts.at(-1).ips.at(-1), { ip: "198.51.100.42", name: "Tokyo line" });
        assert.equal(seen.posts.at(-1).ips.length, 4);
        assert.deepEqual((await rows(page))[3], ["198.51.100.42", "Tokyo line", x("Set start date"), "none"]);

        // its start date set from the pill
        await page.locator('#ipsList .ips-row[data-ip="198.51.100.42"] .renew-pill').click();
        await page.locator("#ipsList .renew-date").fill("2026-11-11");
        await page.locator("#ipsList .renew-save").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .renew-edit"));
        assert.equal(seen.posts.at(-1).ips[3].opened, "2026-11-10T16:00:00.000Z");
        assert.deepEqual((await rows(page))[3].slice(2), [x("Ends {date}", { date: await ends("2026-12-10T16:00:00.000Z") }), "ok"]);

        // an expired one's date cleared: no time is sent for it
        await page.locator('#ipsList .ips-row[data-ip="2001:db8::1"] .renew-pill').click();
        await page.locator("#ipsList .renew-clear").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .renew-edit"));
        assert.equal(seen.posts.at(-1).ips[2].opened, undefined);
        assert.deepEqual((await rows(page))[2].slice(2), [x("Set start date"), "none"]);
        assert.equal(await page.locator("#ipsSum").textContent(), x("{n} IPs", { n: 4 }) + " · " + x("1 to renew"));

        // a name edited; a refused address says why and keeps the form
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-edit').click();
        await page.locator("#ipsList .ips-in-name").fill("Seoul");
        await page.locator("#ipsList .ips-in-ip").fill("bad");
        await page.locator("#ipsList .ips-save").click();
        await page.waitForFunction(() => !document.querySelector("#ipsErr").hidden);
        assert.equal(await page.locator("#ipsList .ips-form").count(), 1);
        assert.equal(await page.locator("#ipsList .ips-in-name").inputValue(), "Seoul");
        await page.locator("#ipsList .ips-in-ip").fill("203.0.113.8");
        await page.locator("#ipsList .ips-in-ip").press("Enter");
        await page.waitForFunction(() => !document.querySelector("#ipsList .ips-form"));
        assert.deepEqual((await rows(page))[1].slice(0, 2), ["203.0.113.8", "Seoul"]);
        assert.equal(seen.posts.at(-1).ips[1].opened, "2026-10-18T04:00:00Z", "an edit keeps the start date");
        assert.ok(await page.locator("#ipsErr").isHidden());

        // a date typed for one IP is not the next one's (review M1)
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.7"] .renew-pill').click();
        await page.locator("#ipsList .renew-date").fill("2026-01-05");
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .renew-pill').click();
        assert.equal(await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .renew-date').inputValue(), "2026-10-18");
        await page.locator("#ipsList .renew-cancel").click();
        await page.waitForFunction(() => !document.querySelector("#ipsList .renew-edit"));

        // removed, once asked; Cancel removes nothing
        const posted = seen.posts.length;
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.7"] .ips-remove').click();
        await page.locator("dialog.action-confirm button:not(.primary)").click();
        assert.equal(seen.posts.length, posted);
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.7"] .ips-remove').click();
        await page.locator("dialog.action-confirm button.primary").click();
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 3);
        assert.deepEqual(seen.posts.at(-1).ips.map((x) => x.ip), ["203.0.113.8", "2001:db8::1", "198.51.100.42"]);
        assert.equal(await page.locator("#ipsSum").textContent(), x("{n} IPs", { n: 3 }));

        // another window's change meanwhile: the save is refused, the list
        // is read again with it and says so (review M2)
        seen.ips = [...seen.ips, { ip: "192.0.2.77", name: "Elsewhere" }];
        seen.rev++;
        await page.locator('#ipsList .ips-row[data-ip="203.0.113.8"] .ips-remove').click();
        await page.locator("dialog.action-confirm button.primary").click();
        await page.waitForFunction(() => !document.querySelector("#ipsErr").hidden);
        assert.equal(await page.locator("#ipsErr").textContent(), x("The list was changed in another window and is read again: make your change again"));
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 4);
        assert.deepEqual((await rows(page)).map((r) => r[0]), ["203.0.113.8", "2001:db8::1", "198.51.100.42", "192.0.2.77"]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
