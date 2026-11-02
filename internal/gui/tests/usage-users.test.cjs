// Run with Node's test runner and Playwright on the module path; see README.md.
// T-020 (Usage › Overview): 用量的概览中，增加按用户维度的统计. The Overview
// lists the calls by the caller's user (X-Magpie-User, --bind=user's), each
// user's tokens, calls and cost as the other lists have them, and the calls
// that named none as one "No user" row. The list folds under its heading
// like the others. A period where no call named a user (no --bind=user, or
// a magpie from before) still has the Users heading, with a note that only
// requests naming X-Magpie-User since the update are counted by user (需求方
// 打回 2026-11-02: 线上用量概览里没看到按用户维度的统计). Every language,
// both engines, 1000 and 420 wide; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const G = (id, name, calls, input, output, cost, extra) => ({ id, name, icon: "generic", calls, errors: 0, input, output, cache_read: 0, cache_write: 0, reasoning: 0, cost, unpriced: 0, ...extra });
const AGENTS = [G("codex", "Codex", 6, 6000, 600, 6)];
const USERS = [G("alice", "alice", 3, 3000, 300, 3, { user: "alice" }), G("bob@example.com", "bob@example.com", 2, 2000, 200, 2, { user: "bob@example.com" }), G("", "", 1, 1000, 100, 1)];
const usageOf = (users) => ({
  calls: 6, errors: 0, input: 6000, output: 600, cache_read: 0, cache_write: 0, reasoning: 0, cost: 6, unpriced: 0, bucket: "day",
  series: [{ label: "Oct 31", time: new Date().toISOString(), calls: 6, input: 6000, output: 600, cache_read: 0, cache_write: 0, cost: 6 }],
  path: "~/.config/magpie/usage.jsonl", agents: AGENTS, models: [], accounts: [], callerKeys: [], ...(users ? { users } : {}),
});

function server(lang, usage) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const settings = { lang, theme: "light" };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { head: "Users", none: "No user", fold: "Fold the users away", show: "Show the users", note: "Only requests that name their user in X-Magpie-User, from this update on, are counted by user" },
  zh: { head: "用户", none: "无用户", fold: "收起用户", show: "展开用户", note: "升级后带 X-Magpie-User 的请求才会按用户统计" },
  "zh-TW": { head: "使用者", none: "無使用者", fold: "收起使用者", show: "展開使用者", note: "升級後帶 X-Magpie-User 的請求才會按使用者統計" },
  ja: { head: "ユーザー", none: "ユーザーなし", fold: "ユーザーを折りたたむ", show: "ユーザーを表示", note: "アップデート後に X-Magpie-User を付けたリクエストだけがユーザー別に集計されます" },
  de: { head: "Benutzer", none: "Kein Benutzer", fold: "Benutzer einklappen", show: "Benutzer anzeigen", note: "Nach Benutzer gezählt werden nur Anfragen mit X-Magpie-User ab diesem Update" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Overview lists the calls by user, the ones without a user as one row", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      for (const width of [1000, 420]) {
        await t.test(`${lang} ${width}`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
          t.after(() => ctx.close());
          const errors = [];
          const open = async (usage) => {
            const p = await ctx.newPage();
            p.setDefaultTimeout(5000);
            p.on("pageerror", (e) => errors.push(e.message));
            await p.route("**/*", server(lang, usage));
            await p.goto("http://magpie.test/?view=usage");
            await p.locator("#usageAgents .row.stat").first().waitFor({ state: "attached" });
            return p;
          };

          let p = await open(usageOf(USERS));
          const head = p.locator("#usageUsersHead");
          assert.equal(await head.isHidden(), false, "the Users heading shows");
          assert.equal((await head.textContent()).trim(), w.head);
          assert.equal(await p.locator("#usageUsersNote").isHidden(), true, "no note once a call named a user");
          const rows = p.locator("#usageUsers .row.stat");
          assert.deepEqual(await rows.locator(".name").allTextContents(), ["alice", "bob@example.com", w.none]);
          assert.deepEqual(await rows.locator(".name").evaluateAll((ns) => ns.map((n) => n.classList.contains("faint"))), [false, false, true], "the no-user row is faint, as an unrecorded account is");
          assert.deepEqual(await rows.locator(".cost").allTextContents(), ["≈$3.00", "≈$2.00", "≈$1.00"]);
          assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => r.querySelector(".share i").style.width)), ["50%", "33.3%", "16.7%"]);
          // nothing runs off the side, narrow too
          assert(await rows.evaluateAll((rs) => rs.every((r) => r.scrollWidth <= r.clientWidth + 1)), "a user's row overflows");

          // it folds like the other lists, and stays folded across a reload
          const fold = p.locator('#usagePane .row-head .fold[data-fold="users"]');
          assert.equal(await fold.getAttribute("aria-expanded"), "true");
          assert.equal(await fold.getAttribute("title"), w.fold);
          await fold.scrollIntoViewIfNeeded();
          await fold.click();
          assert.equal(await fold.getAttribute("aria-expanded"), "false");
          assert.equal(await fold.getAttribute("title"), w.show);
          assert.equal(await p.locator("#usageUsers").evaluate((e) => !!e.offsetParent), false, "the users are folded away");
          await p.close();
          p = await open(usageOf(USERS));
          assert.equal(await p.locator('#usagePane .row-head .fold[data-fold="users"]').getAttribute("aria-expanded"), "false");
          await p.locator('#usagePane .row-head .fold[data-fold="users"]').click();
          assert.equal(await p.locator("#usageUsers").evaluate((e) => !!e.offsetParent), true);
          await p.close();

          // no call named a user (calls from before the update, or without
          // the header), or a server without users: the heading still
          // shows, with why, and folds the note away with the list
          for (const users of [[G("", "", 6, 6000, 600, 6)], [], undefined]) {
            const of = JSON.stringify(users);
            p = await open(usageOf(users));
            assert.equal(await p.locator("#usageUsersHead").isHidden(), false, "the Users heading shows for " + of);
            const note = p.locator("#usageUsersNote");
            assert.equal(await note.isVisible(), true, "the note shows for " + of);
            assert.equal((await note.textContent()).trim(), w.note);
            assert(await note.evaluate((n) => n.scrollWidth <= n.clientWidth + 1), "the note overflows");
            if (users?.length) {
              assert.deepEqual(await p.locator("#usageUsers .row.stat .name").allTextContents(), [w.none]);
            } else {
              assert.equal(await p.locator("#usageUsers").isHidden(), true, "no list without groups for " + of);
            }
            const f = p.locator('#usagePane .row-head .fold[data-fold="users"]');
            await f.scrollIntoViewIfNeeded();
            await f.click();
            assert.equal(await note.isVisible(), false, "the note folds away with the users");
            await f.click();
            await p.close();
          }
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
