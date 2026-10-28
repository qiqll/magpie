// Run with Node's test runner and Playwright on the module path; see README.md.
// A Codex, Claude or Antigravity account's row has Export, which downloads
// its sign-in as CLIProxyAPI's auth file. It asks first, saying the file
// holds the refresh token (and, for ChatGPT and Claude, that a refresh on
// one side ends the other's copy); Cancel sends nothing. In magpie web the
// browser saves the file under CLIProxyAPI's name; in the app it goes to
// Downloads and the status says where. Another account (Copilot) has no
// Export, nor one whose Claude sign-in is gone. At 420px nothing runs past
// the page. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const sub = (id, name, logins) => ({
  id, name, icon: id + "-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "smart",
  models: [{ id: id + "-m", name: "M", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: id, agentName: name, agentIcon: id + "-color", user: logins[0].user, plan: "PRO", logins },
});
const file = JSON.stringify({ type: "codex", refresh_token: "r", email: "me@example.com" });

function serve(lang, web, posts) {
  const providers = {
    providers: [
      sub("codex", "Codex", [{ user: "me@example.com", plan: "PLUS", active: true, on: true }]),
      sub("claude", "Claude Code", [{ user: "ok@example.com", plan: "max", active: true, on: true }, { user: "gone@example.com", plan: "max", lapsed: "signed out" }]),
      sub("copilot", "Copilot", [{ user: "gh", plan: "PRO", active: true, on: true }]),
    ], presets: [], excluded: [], gateway: { running: true, window: true },
  };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/login/cliproxy") {
      posts.push(req.postDataJSON());
      return json(web ? { name: "codex-1a2b3c4d-me@example.com-plus.json", file } : { path: "~/Downloads/codex-1a2b3c4d-me@example.com-plus.json" });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const f = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(f)];
    await route.fulfill({ body: await fs.readFile(f), contentType });
  };
}

const words = {
  en: { export: "Export", cancel: "Cancel", ask: "Export me@example.com for CLIProxyAPI?", warn: /refresh token/, shared: /about once a day/, saved: "Saved to ~/Downloads/codex-1a2b3c4d-me@example.com-plus.json" },
  zh: { export: "导出", cancel: "取消", ask: "为 CLIProxyAPI 导出 me@example.com？", warn: /refresh token/, shared: /每天一次/, saved: "已保存到 ~/Downloads/codex-1a2b3c4d-me@example.com-plus.json" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, web, width = 900) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce", acceptDownloads: true })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      const posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, web, posts));
      await page.goto("http://magpie.test/?view=providers");
      return { page, errors, posts };
    };
    const edit = async (page, name) => {
      await page.locator(".row.provider", { hasText: name }).first().click();
      await page.locator(".editor .acc").first().waitFor();
    };

    test(`${engine} ${lang}: magpie web downloads the account's CLIProxyAPI file, asked first`, async (t) => {
      const { page, errors, posts } = await open(t, true);
      await edit(page, "Codex");
      const exp = page.locator(".editor .acc", { hasText: "me@example.com" }).getByRole("button", { name: w.export, exact: true });
      assert.equal(await exp.count(), 1);
      assert.ok(await exp.getAttribute("title"));
      await exp.click();
      const confirm = page.locator("dialog.action-confirm[open]");
      await confirm.waitFor();
      assert.match(await confirm.textContent(), new RegExp(w.ask.replace(/[.?]/g, "\\$&")));
      assert.match(await confirm.textContent(), w.warn);
      assert.match(await confirm.textContent(), w.shared);
      await confirm.getByRole("button", { name: w.cancel, exact: true }).click();
      assert.deepEqual(posts, [], "Cancel sends nothing");

      await exp.click();
      const [download] = await Promise.all([page.waitForEvent("download"), confirm.getByRole("button", { name: w.export, exact: true }).click()]);
      assert.equal(download.suggestedFilename(), "codex-1a2b3c4d-me@example.com-plus.json");
      assert.equal(await fs.readFile(await download.path(), "utf8"), file);
      assert.deepEqual(posts, [{ agent: "codex", user: "me@example.com" }]);
      const missing = await page.evaluate(() => [
        "Download this account's sign-in as a CLIProxyAPI auth file", "Export {user} for CLIProxyAPI?",
        "The file holds this account's sign-in, its refresh token too: anyone with it can use the account. Keep it private.",
        "magpie renews this sign-in by itself, about once a day, and the copy in the file then stops working: to use the account in CLIProxyAPI, remove it from magpie after exporting.",
      ].filter((k) => !I18N.zh[k] || !I18N["zh-TW"][k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its zh, zh-TW, ja and de");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: in the app it goes to Downloads, and the status says where`, async (t) => {
      const { page, errors, posts } = await open(t, false);
      await edit(page, "Codex");
      await page.locator(".editor .acc", { hasText: "me@example.com" }).getByRole("button", { name: w.export, exact: true }).click();
      await page.locator("dialog.action-confirm[open]").getByRole("button", { name: w.export, exact: true }).click();
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), w.saved);
      assert.deepEqual(posts, [{ agent: "codex", user: "me@example.com" }]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: only accounts CLIProxyAPI takes, with a sign-in, have Export; nothing past 420px`, async (t) => {
      const { page, errors } = await open(t, true, 420);
      await edit(page, "Claude Code");
      const row = (user) => page.locator(".editor .acc", { hasText: user }).getByRole("button", { name: w.export, exact: true });
      assert.equal(await row("ok@example.com").count(), 1);
      assert.equal(await row("gone@example.com").count(), 0, "a Claude account whose sign-in is gone");
      // nothing hovers on a phone: Export shows as Remove does, a finger's size
      const shown = await row("ok@example.com").evaluate((b) => { const r = b.getBoundingClientRect(); return [getComputedStyle(b).opacity, Math.round(r.width), Math.round(r.height)]; });
      assert.equal(shown[0], "1", "Export is hidden on a phone");
      assert.ok(shown[1] >= 44 && shown[2] >= 44, `Export is ${shown[1]}×${shown[2]} on a phone`);
      const over = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
      assert.ok(over <= 0, `the page runs ${over}px past 420px`);
      await page.goto("http://magpie.test/?view=providers");
      await edit(page, "Copilot");
      assert.equal(await page.locator(".editor .acc").getByRole("button", { name: w.export, exact: true }).count(), 0, "Copilot has none");
      assert.deepEqual(errors, []);
    });
  }
}
