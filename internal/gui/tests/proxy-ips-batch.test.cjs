// Run with Node's test runner and Playwright on the module path; see README.md.
// 需求方 (2026-11-13):
// - An IP added on Models › Proxy IPs wasn't offered when adding an
//   account. Now the proxy each IP goes out through is offered in the
//   sign-in's Through menu (signInProxyChoices), with no password shown,
//   and picking it sends it with the pasted address; and in Saved… beside
//   a typed proxy's login, with no proxy saved in Settings at all.
// - Proxy IPs' Batch add takes proxies one a line, after another proxy
//   manager's 批量添加代理: type://host:port:user:password[refresh URL],
//   type://user:password@host:port, IPv6 in [ ], user:password:host:port,
//   spaces between the parts, no type (SOCKS5, or the type picked). Each
//   line is listed as read or why not; Add keeps each under its host's IP,
//   one whose host is a name under its exit (proxy/check), skips one listed
//   already and says so; Check proxies reads every exit first.
// Every language, at a wide and a phone's width, Chromium and WebKit; no
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
const OSAKA = "socks5://u:s3cret@203.0.113.7:1080", OSAKA_SHOWN = "socks5://203.0.113.7:1080";
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};

function serve(lang, seen) {
  const settings = { lang, theme: "light", tray: "panel", currency: "usd", textSize: 100, proxy: "", proxyNow: "", proxySource: "none", version: "test", dir: "/test", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, proxies: [] };
  const flows = {};
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
        seen.ips = body.ips.map((x) => Object.fromEntries(Object.entries(x).filter(([, v]) => v !== undefined && v !== "")));
        seen.rev++;
      }
      return json({ ips: seen.ips, rev: "r" + seen.rev, days: 30, warnDays: 5 });
    }
    if (url.pathname === "/api/proxy/check") {
      const asked = req.postDataJSON().proxy;
      seen.checks.push(asked);
      if (seen.hold) await (seen.held ||= new Promise((r) => { seen.release = r; }));
      const exit = seen.exits[asked];
      if (!exit) return json({ source: "saved", through: { error: "dial tcp: i/o timeout" }, direct: { ip: "192.0.2.1" } });
      return json({ source: "saved", through: { ip: exit, loc: "JP", ms: 80 }, direct: { ip: "192.0.2.1" } });
    }
    if (url.pathname === "/api/providers") return json({ providers: [codex], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/signin") {
      const b = req.postDataJSON();
      const id = "s" + (Object.keys(flows).length + 1);
      flows[id] = { id, agent: b.agent, state: "waiting", url: "https://vendor.example/oauth?state=" + id, proxy: b.proxy ?? "", pasteCallback: true };
      return json(flows[id]);
    }
    const m = url.pathname.match(/^\/api\/signin\/(s\d+)(\/cancel|\/callback)?$/);
    if (m) {
      if (m[2] === "/cancel") flows[m[1]].state = "canceled";
      if (m[2] === "/callback") { seen.pastes.push(req.postDataJSON()); return route.fulfill({ status: 204, body: "" }); }
      return json(flows[m[1]]);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

async function open(engine, width, lang, seen, at, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, seen));
  await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); sessionStorage.setItem("set", "1"); } } catch {} });
  await page.goto("http://magpie.test/" + at);
  return { page, errors };
}
const wider = (page) => page.evaluate(() => Math.max(0, ...[...document.querySelectorAll("#view-ips *")].filter((e) => e.offsetParent).map((e) => e.getBoundingClientRect().right)) - document.documentElement.clientWidth);

// what the user pastes: every format the other manager reads, a line read
// twice, one listed already, one of a type magpie can't keep, and one that
// isn't a proxy
const PASTED = [
  "192.168.0.1:8000",
  "192.168.0.2:8000:alice:pw1",
  "socks5://192.168.0.3:8000[https://refresh.example/x]",
  "HTTP://192.168.0.4:8000:bob:pw2[https://refresh.example/y]",
  "http://carol:pw3@192.168.0.5:8000",
  "HTTP://[2001:db8:2de:0:0:0:0:e13]:8000:dave:pw4[https://refresh.example/z]",
  "dave:pw5:192.168.0.6:8000",
  "proxy.example.com 9000 erin pw6",
  "ssh://192.168.0.7:22",
  "not a proxy",
  "socks5://u:s3cret@203.0.113.7:1080",
  "192.168.0.1:8000",
].join("\n");
const KEPT = [
  ["192.168.0.1", "socks5://192.168.0.1:8000"],
  ["192.168.0.2", "socks5://alice:pw1@192.168.0.2:8000"],
  ["192.168.0.3", "socks5://192.168.0.3:8000"],
  ["192.168.0.4", "http://bob:pw2@192.168.0.4:8000"],
  ["192.168.0.5", "http://carol:pw3@192.168.0.5:8000"],
  ["2001:db8:2de::e13", "http://dave:pw4@[2001:db8:2de::e13]:8000"],
  ["192.168.0.6", "socks5://dave:pw5@192.168.0.6:8000"],
  ["198.51.100.42", "socks5://erin:pw6@proxy.example.com:9000"],
];

test("each format a batch line is written in is read as the proxy it names", async (t) => {
  const seen = { posts: [], checks: [], pastes: [], rev: 1, ips: [], exits: {} };
  const { page, errors } = await open("chromium", 1000, "en", seen, "?view=ips", t);
  await page.waitForFunction(() => typeof ipsBatchParse === "function");
  const read = (line, type = "socks5") => page.evaluate(([l, ty]) => { const x = ipsBatchParse(l, ty); return x.err ? "ERR" : x.url; }, [line, type]);
  const cases = [
    ["192.168.0.1:8000", "socks5://192.168.0.1:8000"],
    ["192.168.0.1:8000", "http://192.168.0.1:8000", "http"],
    ["192.168.0.1:8000:user:pass", "socks5://user:pass@192.168.0.1:8000"],
    ["socks5://192.168.0.1:8000[https://x.example/r?a=1]", "socks5://192.168.0.1:8000"],
    ["HTTP://192.168.0.1:8000:user:pass[https://x.example/r]", "http://user:pass@192.168.0.1:8000"],
    ["https://user:pass@192.168.0.1:8000", "https://user:pass@192.168.0.1:8000"],
    ["socks5h://user:pass@[2001:db8::1]:8000[https://x.example/r]", "socks5h://user:pass@[2001:db8::1]:8000"],
    ["HTTP://[2001:db8:2de:0:0:0:0:e13]:8000", "http://[2001:db8:2de::e13]:8000"],
    ["HTTP://[2001:db8:2de:0:0:0:0:e13]:8000:user:pass[https://x.example/r]", "http://user:pass@[2001:db8:2de::e13]:8000"],
    ["user:pass:192.168.0.1:8000", "socks5://user:pass@192.168.0.1:8000"],
    ["user:pass:[2001:db8::1]:8000", "socks5://user:pass@[2001:db8::1]:8000"],
    ["192.168.0.1:8000@user:pass", "socks5://user:pass@192.168.0.1:8000"],
    ["user:p:a@ss@192.168.0.1:8000", "socks5://user:p%3Aa%40ss@192.168.0.1:8000"],
    ["192.168.0.1\t8000\tuser\tpass", "socks5://user:pass@192.168.0.1:8000"],
    ["gate.Example.com:9000:user:pass", "socks5://user:pass@gate.example.com:9000"],
    ["socks://192.168.0.1:1080", "socks5://192.168.0.1:1080"],
    ["192.168.0.1:8000:user", "socks5://user@192.168.0.1:8000"],
    ["ssh://192.168.0.1:22", "ERR"],
    ["192.168.0.1", "ERR"],
    ["192.168.0.1:70000", "ERR"],
    ["300.1.1.1:8000", "ERR"],
    ["user:pass:other:8000", "ERR"],
    // review: a dotted username with a numeric password reads as the login
    ["proxy.user:12345:1.2.3.4:8080", "socks5://proxy.user:12345@1.2.3.4:8080"],
    ["1.2.3.4:80:5.6.7.8:90", "ERR"],
    // a login escaped in a pasted URL isn't escaped twice
    ["http://u%40x:p%3Aq@1.2.3.4:80", "http://u%40x:p%3Aq@1.2.3.4:80"],
    // a comment, an empty username, a refresh URL of hex digits
    ["1.2.3.4:80 # office", "socks5://1.2.3.4:80"],
    ["1.2.3.4 8080 user pass with space", "ERR"],
    ["1.2.3.4:80::pass", "socks5://:pass@1.2.3.4:80"],
    ["1.2.3.4:80:u:p[deadbeef]", "socks5://u:p@1.2.3.4:80"],
    ["1.2.3.4:80:u:p:q", "ERR"],
  ];
  for (const [line, want, type] of cases) assert.equal(await read(line, type), want, line);
  assert.deepEqual(errors, []);
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    const x = (s, a) => tr(lang, s, a);
    for (const width of [1000, 380]) {
      test(`${engine} ${lang} ${width}: an IP's proxy is offered when an account is added`, async (t) => {
        const seen = { posts: [], checks: [], pastes: [], rev: 1, ips: [{ ip: "203.0.113.7", name: "Osaka", proxies: [OSAKA] }], exits: {} };
        const { page, errors } = await open(engine, width, lang, seen, "?view=providers", t);
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts .acc.add").first().click();
        await page.locator(".editor .signing .sign-start").click();
        const box = page.locator(".editor .signing");
        const pp = box.locator("button.paste-via");
        await pp.waitFor();
        // the IPs are read as the sign-in is drawn
        await page.waitForFunction(() => window.ipProxies().length === 1);
        await pp.scrollIntoViewIfNeeded();
        await pp.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), [x("Global proxy"), x("Direct"), OSAKA_SHOWN, x("Custom…")]);
        assert.match(await menu.locator(".pm-item", { hasText: OSAKA_SHOWN }).textContent(), /203\.0\.113\.7 · Osaka/);
        assert.doesNotMatch(await menu.textContent(), /s3cret/, "the IP's proxy's password isn't shown");
        await menu.locator(".pm-item", { hasText: OSAKA_SHOWN }).click();
        await page.waitForFunction((s) => document.querySelector(".editor .signing button.paste-via")?.textContent.includes(s), OSAKA_SHOWN);
        await box.locator("form.callback-form input").first().fill("http://localhost:1455/auth/callback?code=c&state=s1");
        await box.locator("form.callback-form button.primary").click();
        for (let i = 0; i < 40 && !seen.pastes.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(seen.pastes, [{ url: "http://localhost:1455/auth/callback?code=c&state=s1", proxy: OSAKA }]);

        // Saved… beside a typed proxy's login: no proxy is saved in
        // Settings, so it is there for the IP's alone
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Codex" }).first().click();
        await page.locator(".editor .accts .acc.add").first().click();
        await page.locator(".editor .signing .sign-start").click();
        await pp.waitFor();
        await pp.scrollIntoViewIfNeeded();
        await pp.click();
        await page.locator(".proto-menu .pm-item", { hasText: x("Custom…") }).click();
        const typed = box.locator("input.paste-proxy");
        await typed.waitFor();
        const saved = box.locator(".proxy-ask .saved-proxy");
        await saved.waitFor();
        await saved.click();
        await page.locator(".proto-menu .pm-item", { hasText: "203.0.113.7 · Osaka" }).click();
        assert.equal(await typed.inputValue(), OSAKA_SHOWN);
        assert.deepEqual(await box.locator(".proxy-ask input.proxy-credential").evaluateAll((fs) => fs.map((f) => f.value)), ["u", "s3cret"]);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: proxies pasted in a batch are read, listed and added`, async (t) => {
        const seen = { posts: [], checks: [], pastes: [], rev: 1, ips: [{ ip: "203.0.113.7", name: "Osaka", proxies: [OSAKA] }],
          exits: { "socks5://erin:pw6@proxy.example.com:9000": "198.51.100.42" } };
        const { page, errors } = await open(engine, width, lang, seen, "?view=ips", t);
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 1);
        await page.locator("#ipsBatch").click();
        const box = page.locator("#ipsBatchBox");
        await box.waitFor();
        assert.equal(await page.locator("#ipsAdd").isVisible(), false, "Add IP is put away while the batch is open");
        assert.equal(await box.locator(".ips-batch-head").textContent(), x("Batch add proxies"));
        await box.locator(".ips-batch-in").fill(PASTED);
        assert.equal(await box.locator(".ips-batch-said").textContent(), x("{n} proxies read", { n: 9 }) + " · " + x("{n} lines not read", { n: 3 }));
        const rows = await box.locator(".ips-batch-row").evaluateAll((rs) => rs.map((r) => [r.dataset.line, r.classList.contains("err"), r.querySelector(".ips-batch-addr")?.textContent || r.querySelector(".ips-batch-why")?.textContent]));
        assert.deepEqual(rows, [
          ["1", false, "192.168.0.1:8000"], ["2", false, "192.168.0.2:8000"], ["3", false, "192.168.0.3:8000"], ["4", false, "192.168.0.4:8000"],
          ["5", false, "192.168.0.5:8000"], ["6", false, "[2001:db8:2de::e13]:8000"], ["7", false, "192.168.0.6:8000"], ["8", false, "proxy.example.com:9000"],
          ["9", true, x("{type} proxies aren't supported: use HTTP, HTTPS or SOCKS5", { type: "SSH" })],
          ["10", true, x("No host and port found in it")],
          ["11", false, "203.0.113.7:1080"],
          ["12", true, x("The same proxy as a line above")],
        ]);
        assert.equal(await box.locator('.ips-batch-row[data-line="11"] .ips-batch-exit').textContent(), x("Listed already"));
        assert.doesNotMatch(await box.locator(".ips-batch-lines").textContent(), /pw[1-6]|s3cret/, "no password is shown");
        assert.ok(await wider(page) <= 0.5, `the form reaches ${await wider(page)}px past the window`);

        await box.locator(".ips-batch-add").click();
        await page.waitForFunction(() => !document.querySelector("#ipsBatchBox .ips-batch-in") && document.querySelectorAll("#ipsList .ips-row").length === 9);
        // only the host that is a name was asked its exit
        assert.deepEqual(seen.checks, ["socks5://erin:pw6@proxy.example.com:9000"]);
        assert.equal(seen.posts.length, 1);
        assert.deepEqual(seen.posts[0].ips, [{ ip: "203.0.113.7", name: "Osaka", proxies: [OSAKA] }, ...KEPT.map(([ip, p]) => ({ ip, name: "", proxies: [p] }))]);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("{n} IPs added", { n: 8 }) + " · "
          + x("Not checked, so kept under the IP they connect to: {list}", { list: KEPT.slice(0, 7).map(([, p]) => p.replace(/^.*[@/]/, "")).join(", ") })
          + " · " + x("Listed already, so skipped: {list}", { list: "203.0.113.7:1080" }));
        assert.equal(await page.locator("#ipsBatch").isVisible(), true);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a batch's proxies are checked, kept under their exits, and a type is picked`, async (t) => {
        const seen = { posts: [], checks: [], pastes: [], rev: 1, ips: [{ ip: "203.0.113.7", name: "Osaka", proxies: [OSAKA] }],
          exits: { "https://10.0.0.1:3128": "203.0.113.7", "https://10.0.0.2:3128": "198.51.100.9" } };
        const { page, errors } = await open(engine, width, lang, seen, "?view=ips", t);
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 1);
        await page.locator("#ipsBatch").click();
        const box = page.locator("#ipsBatchBox");
        await box.locator(".ips-batch-type-pick").click();
        await page.locator(".proto-menu .pm-item", { hasText: "HTTPS" }).first().click();
        await box.locator(".ips-batch-in").fill("10.0.0.1:3128\n10.0.0.2:3128\n10.0.0.3:3128\nhost.example.net:3128");
        await box.locator(".ips-batch-check").click();
        await page.waitForFunction(() => !document.querySelector("#ipsBatchBox .ips-batch-check.busy") && document.querySelectorAll("#ipsBatchBox .ips-batch-exit").length === 4 && [...document.querySelectorAll("#ipsBatchBox .ips-batch-exit")].every((e) => e.textContent));
        assert.deepEqual(seen.checks.sort(), ["https://10.0.0.1:3128", "https://10.0.0.2:3128", "https://10.0.0.3:3128", "https://host.example.net:3128"]);
        assert.deepEqual(await box.locator(".ips-batch-exit").allTextContents(), [x("Exit {ip}", { ip: "203.0.113.7" }), x("Exit {ip}", { ip: "198.51.100.9" }), x("Didn't answer"), x("Didn't answer")]);
        await box.locator(".ips-batch-add").click();
        await page.waitForFunction(() => !document.querySelector("#ipsBatchBox .ips-batch-in"));
        // the first goes out through the IP listed, the third keeps its own
        // host, the name that didn't answer has no IP and is said
        assert.deepEqual(seen.posts.at(-1).ips, [
          { ip: "203.0.113.7", name: "Osaka", proxies: [OSAKA, "https://10.0.0.1:3128"] },
          { ip: "198.51.100.9", name: "", proxies: ["https://10.0.0.2:3128"] },
          { ip: "10.0.0.3", name: "", proxies: ["https://10.0.0.3:3128"] },
        ]);
        assert.equal(await page.locator("#ipsSaid").textContent(), x("{n} IPs added", { n: 2 }) + " · " + x("1 proxy told to an IP listed") + " · " + x("Not checked, so kept under the IP they connect to: {list}", { list: "10.0.0.3:3128" }));
        assert.equal(await page.locator("#ipsErr").textContent(), x("These proxies didn't answer, so their exit isn't known: {list}", { list: "host.example.net:3128" }));
        // the type picked is kept for the next batch
        await page.locator("#ipsBatch").click();
        await box.locator(".ips-batch-in").fill("10.0.0.9:3128");
        assert.equal(await box.locator(".ips-batch-type").textContent(), "HTTPS");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a batch past 500 IPs keeps what fits, and rows wait while it is added`, async (t) => {
        const many = Array.from({ length: 498 }, (_, i) => ({ ip: "10.1." + (i >> 8) + "." + (i & 255) }));
        const seen = { posts: [], checks: [], pastes: [], rev: 1, ips: many, exits: {}, hold: true };
        const { page, errors } = await open(engine, width, lang, seen, "?view=ips", t);
        await page.waitForFunction(() => document.querySelectorAll("#ipsList .ips-row").length === 498);
        await page.locator("#ipsBatch").click();
        const box = page.locator("#ipsBatchBox");
        await box.locator(".ips-batch-in").fill("192.0.2.1:80\n192.0.2.2:80\n192.0.2.3:80\nslow.example.com:80");
        await box.locator(".ips-batch-add").click();
        // the name host is being checked: no row can be edited or removed
        await page.waitForFunction(() => document.querySelector("#ipsBatchBox .ips-batch-add.busy"));
        assert.equal(await page.locator("#ipsList .ips-row .ips-edit").first().isDisabled(), true);
        assert.equal(await page.locator("#ipsList .ips-row .ips-remove").first().isDisabled(), true);
        seen.release();
        await page.waitForFunction(() => !document.querySelector("#ipsBatchBox .ips-batch-in"));
        assert.equal(seen.posts.length, 1);
        assert.deepEqual(seen.posts[0].ips.slice(498).map((x) => x.ip), ["192.0.2.1", "192.0.2.2"]);
        assert.equal(await page.locator("#ipsErr").textContent(), x("These proxies didn't answer, so their exit isn't known: {list}", { list: "slow.example.com:80" }) + " "
          + x("At most {n} IPs are kept, so these weren't added: {list}", { n: 500, list: "192.0.2.3:80" }));
        assert.equal(await page.locator("#ipsList .ips-row .ips-edit").first().isDisabled(), false);
        assert.deepEqual(errors, []);
      });
    }
  }
}
