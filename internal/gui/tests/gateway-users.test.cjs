// Run with Node's test runner and Playwright on the module path; see README.md.
// The Gateway page's User bindings: hidden while nobody is bound; with
// users, each provider's accounts counted, the users with their account,
// provider and gateway key, an account no longer on its provider said so,
// a search that asks again and keeps its focus, a page of users said as
// such, and an unreadable file said, never shown as nobody. In English and
// Chinese, wide and narrow, in Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = new Date(Date.now() - 3 * 3600e3).toISOString();
const providers = {
  providers: [{ id: "antigravity", name: "Antigravity", icon: "generic", models: [{ id: "m", name: "M", on: true }], agents: [] }],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls: [], groups: [] },
};
const everyone = [
  { user: "1", provider: "antigravity", providerName: "Antigravity", account: "a@x.com", key: "k1", keyName: "Tenant A", at },
  { user: "2", provider: "antigravity", providerName: "Antigravity", account: "b@x.com", key: "k1", keyName: "Tenant A", at },
  { user: "3", provider: "antigravity", providerName: "Antigravity", account: "a@x.com", key: "", keyName: "", at },
  { user: "4", provider: "codex", providerName: "ChatGPT", account: "c@x.com", key: "gone", keyName: "", keyGone: true, at },
  { user: "a-very-long-user-id-from-the-service-0123456789abcdef", provider: "codex", providerName: "ChatGPT", account: "c@x.com", key: "k1", keyName: "Tenant A", at },
  // a key the list couldn't be read for: named by its id, not said removed
  { user: "6", provider: "codex", providerName: "ChatGPT", account: "c@x.com", key: "0123456789abcdef", keyName: "", at: "0001-01-01T00:00:00Z" },
];
const accounts = [
  { provider: "antigravity", providerName: "Antigravity", account: "a@x.com", users: 2 },
  { provider: "antigravity", providerName: "Antigravity", account: "b@x.com", users: 1 },
  { provider: "codex", providerName: "ChatGPT", account: "c@x.com", users: 3, gone: true },
  { provider: "removed", providerName: "removed", account: "d@x.com", users: 1, gone: true, providerGone: true },
];

function serve(lang, users) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/gateway/trace") return url.searchParams.get("wait") ? new Promise(() => {}) : json({ mine: true, now: new Date().toISOString(), seq: 1, totals: {}, routes: [] });
    if (url.pathname === "/api/gateway/users") {
      users.asked.push(url.searchParams.get("q") || "");
      if (users.error) return json({ error: "user-accounts.json can't be read: unexpected end of JSON input", why: "unexpected end of JSON input" }, 400);
      const words = (url.searchParams.get("q") || "").toLowerCase().split(/\s+/).filter(Boolean);
      const hit = users.all.filter((u) => words.every((w) => `${u.user} ${u.account} ${u.providerName} ${u.keyName}`.toLowerCase().includes(w)));
      // a search leaves the accounts out, as the server does
      const out = { total: users.all.length, matched: hit.length + (users.more || 0), users: hit };
      if (!url.searchParams.get("q")) out.accounts = users.all.length ? accounts : [];
      return json(out);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { head: "User bindings", two: "2 users", gone: "no longer on its provider", provGone: "no longer in magpie", none: "without a gateway key", removed: "a removed gateway key", keyID: "gateway key 01234567", bound: "bound", match: "No users match “nobody”", more: "Showing 6 of 300", unread: "user-accounts.json can't be read: unexpected end" },
  zh: { head: "用户绑定", two: "2 个用户", gone: "已不在该供应商下", provGone: "已不在 magpie 中", none: "未使用网关密钥", removed: "已删除的网关密钥", keyID: "网关密钥 01234567", bound: "绑定", match: "没有和“nobody”匹配的用户", more: "显示 300 个中的 6 个", unread: "无法读取 user-accounts.json：unexpected end" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [960, 420]) {
      test(`${engine} ${lang} ${width}px: User bindings`, async (t) => {
        assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const users = { all: [], asked: [] };
        await page.route("**/*", serve(lang, users));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-gateway-users.png`), fullPage: true });
          }
          await browser.close();
        });
        const w = words[lang];

        // nobody bound: the section stays hidden
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gateway .name").waitFor();
        await page.waitForFunction(() => typeof userBinds !== "undefined" && userBinds !== null);
        assert(await page.locator("#userBindsBlock").evaluate((b) => b.hidden), "hidden while nobody is bound");

        // users: accounts counted, users with account, provider and key
        users.all = everyone;
        await page.reload();
        await page.locator("#userBinds .user-bind").first().waitFor();
        const block = page.locator("#userBindsBlock");
        assert.equal((await block.locator(".row-head .label").textContent()).trim(), w.head);
        assert.equal((await page.locator("#userBindsCount").textContent()).trim(), "6");
        const accountRows = page.locator("#userBindAccounts .user-account");
        assert.equal(await accountRows.count(), 4);
        assert.equal((await accountRows.nth(0).locator(".note").textContent()).trim(), w.two);
        assert.match(await accountRows.nth(2).textContent(), new RegExp(w.gone));
        assert(await accountRows.nth(2).evaluate((r) => r.classList.contains("gone")));
        assert.match(await accountRows.nth(3).textContent(), new RegExp(w.provGone));
        const userRows = page.locator("#userBinds .user-bind");
        assert.equal(await userRows.count(), 6);
        const first = await userRows.nth(0).textContent();
        assert.match(first, /a@x\.com · Antigravity · Tenant A/);
        assert.match(first, new RegExp(w.bound));
        assert.match(await userRows.nth(2).textContent(), new RegExp(w.none));
        assert.match(await userRows.nth(3).textContent(), new RegExp(w.removed));
        const unknownKey = await userRows.nth(5).textContent();
        assert.match(unknownKey, new RegExp(w.keyID), "a key not known to be removed is named by its id");
        assert.doesNotMatch(unknownKey, new RegExp(w.removed));
        assert.doesNotMatch(unknownKey, new RegExp(w.bound), "no time is said for a binding without one");
        assert.equal(await userRows.nth(0).locator("button.copy").count(), 1, "the user's id has a copy button");

        // nothing runs past the page's width, the long id included
        const over = await page.locator("#view-gateway").evaluate((v) => {
          const r = v.getBoundingClientRect();
          return [...v.querySelectorAll("#userBindsBlock *")].filter((e) => e.getBoundingClientRect().right > r.right + 1).map((e) => e.className || e.tagName);
        });
        assert.deepEqual(over, [], "nothing runs past the page");

        // the search asks again, narrows, keeps its focus; Escape clears it
        const find = page.locator("#findUser");
        assert(await find.isVisible(), "five users have a search");
        await find.click();
        await find.pressSequentially("b@x");
        await page.waitForFunction(() => document.querySelectorAll("#userBinds .user-bind").length === 1);
        assert.match(await userRows.nth(0).textContent(), /b@x\.com/);
        assert.equal(await accountRows.count(), 4, "a search keeps the accounts the page has");
        assert(await find.evaluate((f) => document.activeElement === f), "typing keeps the search's focus");
        assert(users.asked.includes("b@x"), "the words go to the server");
        await find.fill("nobody");
        await page.locator("#userBinds .none").waitFor();
        assert.match(await page.locator("#userBinds .none").textContent(), new RegExp(w.match));
        await find.press("Escape");
        await page.waitForFunction(() => document.querySelectorAll("#userBinds .user-bind").length === 6);
        assert.equal(await find.inputValue(), "");

        // more than a page: said, with how many match
        users.more = 294;
        await find.fill("x");
        await page.waitForFunction(() => /300/.test(document.querySelector("#userBinds")?.textContent || ""));
        assert.match(await page.locator("#userBinds .none").last().textContent(), new RegExp(w.more));
        users.more = 0;

        // a file that doesn't read is said, not shown as nobody
        users.error = true;
        await page.reload();
        await page.locator("#userBinds .none").waitFor();
        assert(!(await block.evaluate((b) => b.hidden)), "an unreadable file shows the section");
        assert.match(await page.locator("#userBinds .none").textContent(), new RegExp(w.unread), "said in the reader's language");
        assert.deepEqual(errors, []);
      });
    }
  }
}
