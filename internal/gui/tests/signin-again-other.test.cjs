// Run with Node's test runner and Playwright on the module path; see README.md.
// Sign in again on an account's row (T-056). One that lands on another
// account says so, naming the one that still has to be signed in again
// (st.for), where it said the other was added. And an editor open on the
// accounts when the window comes back reads them again, drawn anew only
// when they changed: an account signed in again in a terminal (`claude
// auth login`) loses its Sign-in required without a reload. Chromium and
// WebKit, in every language; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const codex = (back) => ({
  id: "codex", name: "Codex", icon: "openai", chat: "", responses: "https://chatgpt.com/backend-api/codex", anthropic: "", catalog: "",
  models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
  account: { agent: "codex", agentName: "Codex", agentIcon: "openai", builtin: true, user: "me@example.com", plan: "Plus", logins: [
    { user: "me@example.com", plan: "Plus", active: true, on: true },
    { user: "bob@example.com", plan: "Pro", on: true, ...(back ? {} : { lapsed: "bob@example.com's sign-in has expired — add the account again to use it" }) },
  ] },
});

// serve fakes the API: the sign-in is done at once, on done; the accounts
// are as signed in again once back.back is set
function serve(lang, done, back, asked) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") { asked.providers++; return json({ providers: [codex(back.back)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [], onPlugins: [] }); }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/signin") return json({ id: "s1", agent: "codex", state: "waiting", url: "https://auth.openai.test/authorize" });
    if (url.pathname === "/api/signin/s1") return json({ id: "s1", agent: "codex", state: "done", ...done });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { again: "Sign in again", required: "Sign-in required", other: "Signed in to carol@example.com, another account: bob@example.com still has to be signed in again" },
  zh: { again: "重新登录", required: "需要重新登录", other: "登录的是另一个账号 carol@example.com：bob@example.com 仍需重新登录" },
  "zh-TW": { again: "重新登入", required: "需要重新登入", other: "登入的是另一個帳號 carol@example.com：bob@example.com 仍需重新登入" },
  ja: { again: "再度サインイン", required: "サインインが必要です", other: "別のアカウント carol@example.com にサインインしました。bob@example.com はまだサインインし直す必要があります" },
  de: { again: "Erneut anmelden", required: "Anmeldung erforderlich", other: "Bei einem anderen Konto angemeldet, carol@example.com: bob@example.com muss weiterhin neu angemeldet werden" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Sign in again says when it landed on another account, and an open editor reads its accounts on return", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(W)) {
      const w = W[lang];
      const open = async (done) => {
        const back = { back: false }, asked = { providers: 0 };
        const page = await (await browser.newContext({ viewport: { width: 1000, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, done, back, asked));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Codex" }).click();
        const bob = page.locator(".editor .accts .acc", { hasText: "bob@example.com" });
        await bob.getByText(w.required, { exact: true }).waitFor();
        return { page, errors, back, asked, bob };
      };

      await t.test(`${lang}: signed in to another account says the row's still needs it`, async () => {
        const { page, errors, bob } = await open({ user: "carol@example.com", for: "bob@example.com" });
        await bob.getByRole("button", { name: w.again, exact: true }).click();
        await page.waitForFunction((s) => document.querySelector("#status").textContent.includes(s), w.other);
        assert.equal((await page.locator("#status").textContent()).trim(), w.other);
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(`${lang}: the window back on an open editor shows an account signed in again meanwhile`, async () => {
        const { page, errors, back, asked, bob } = await open({});
        // nothing changed: asked again, the editor not drawn again
        const before = asked.providers;
        await page.evaluate(() => { document.querySelector(".editor").dataset.kept = "1"; window.dispatchEvent(new Event("focus")); });
        for (let i = 0; i < 100 && asked.providers === before; i++) await page.waitForTimeout(20);
        assert.ok(asked.providers > before, "the accounts weren't read again");
        await page.waitForTimeout(200);
        assert.equal(await page.locator(".editor[data-kept]").count(), 1, "drawn again with nothing changed");
        assert.equal(await bob.getByText(w.required, { exact: true }).count(), 1);
        // signed in again in a terminal meanwhile
        back.back = true;
        await page.waitForTimeout(1100); // loadBack's one read a second
        await page.evaluate(() => window.dispatchEvent(new Event("focus")));
        await bob.getByText(w.required, { exact: true }).waitFor({ state: "detached" });
        assert.equal(await bob.count(), 1, "the account is still listed");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
