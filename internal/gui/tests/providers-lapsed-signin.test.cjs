// Run with Node's test runner and Playwright on the module path; see README.md.
// An account in use whose sign-in has to be made again is said over the
// Providers list (T-008, renderLapsed): "Codex: spare@example.com has to be
// signed in again", why, and Sign in again, which opens Codex's editor with
// that account's sign-in under way; once it is signed in again the line
// goes. An account still served, one not in use, or one of a provider
// turned off, has no line. A sign-in that fails with a reason the server
// left out (provider.SignInSaid: it may hold the sign-in's code or key, said
// by its i18n.js key and status) says the sentence in the reader's language
// in the sign-in's box, and on the status line when the box isn't on the
// page (saySignInFailed), once, naming the provider as the line does.
// Chromium and WebKit, 420 and 1100 wide, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const lapse = (user) => `${user}'s sign-in was revoked; sign in again`;
const held = "The vendor answered HTTP 400; its answer is left out here, as it may hold the sign-in's code or key";
// what provider.SignInState.Said sends in place of the reason: the
// sentence, its i18n.js key and the vendor's status
const failed = { id: "s1", agent: "codex", state: "failed", error: held, said: "The vendor answered HTTP {status}; its answer is left out here, as it may hold the sign-in's code or key", status: 400 };

const logins = (signedIn) => [
  { user: "me@example.com", plan: "Plus", active: true, on: true },
  { user: "spare@example.com", plan: "Pro", on: true, ...(signedIn ? {} : { lapsed: lapse("spare@example.com") }) },
  { user: "old@example.com", plan: "Plus", lapsed: lapse("old@example.com") },
  // the agent's own, paused: not in use, so no line
  { user: "rest@example.com", plan: "Plus", active: true, paused: true, lapsed: lapse("rest@example.com") },
];
const codex = (signedIn) => ({
  id: "codex", name: "Codex", icon: "openai", chat: "", responses: "https://chatgpt.com/backend-api/codex", anthropic: "", catalog: "",
  models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
  account: { agent: "codex", agentName: "Codex", agentIcon: "openai", builtin: true, user: "me@example.com", plan: "Plus", logins: logins(signedIn) },
});
// a provider turned off: its account in use to sign in again has no line
const claude = {
  id: "claude", name: "Claude Code", icon: "anthropic", off: true, chat: "", responses: "", anthropic: "https://api.anthropic.com", catalog: "",
  models: [{ id: "claude-sonnet-5-5", name: "Claude Sonnet 5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
  account: { agent: "claude", agentName: "Claude Code", agentIcon: "anthropic", builtin: true, user: "off@example.com", plan: "Max", logins: [
    { user: "off@example.com", plan: "Max", active: true, on: true, lapsed: lapse("off@example.com") },
  ] },
};

// serve fakes the API. The sign-in waits until sign.fail or sign.done is
// set, then fails with the reason the server left out, or is done, the
// account no longer to sign in again.
function serve(lang, posts, sign) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: [codex(sign.done), claude], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [], onPlugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/login/usage") return json({});
    if (route.request().method() === "POST") {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      if (url.pathname === "/api/signin") return json({ id: "s1", agent: "codex", state: "waiting", url: "https://auth.openai.test/authorize" });
      return json({});
    }
    if (url.pathname === "/api/signin/s1") {
      if (sign.fail) { sign.failedSent++; return json(failed); }
      if (sign.done) return json({ id: "s1", agent: "codex", state: "done", user: "spare@example.com", again: true });
      return json({ id: "s1", agent: "codex", state: "waiting", url: "https://auth.openai.test/authorize" });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: {
    head: "Codex: spare@example.com has to be signed in again", why: "magpie leaves it out of requests until it is signed in again.", again: "Sign in again",
    held: "The vendor answered HTTP 400; its answer is left out here, as it may hold the sign-in's code or key", status: (e) => `Sign-in to Codex didn't finish: ${e}`,
  },
  zh: {
    head: "Codex：spare@example.com 需要重新登录", why: "重新登录之前，magpie 不会把请求发给它。", again: "重新登录",
    held: "服务商返回了 HTTP 400；它的回复可能含有登录的授权码或 Key，这里不显示", status: (e) => `Codex 登录未完成：${e}`,
  },
  "zh-TW": {
    head: "Codex：spare@example.com 需要重新登入", why: "重新登入之前，magpie 不會把請求送給它。", again: "重新登入",
    held: "服務商回傳了 HTTP 400；它的回覆可能含有登入的授權碼或 Key，這裡不顯示", status: (e) => `Codex 登入未完成：${e}`,
  },
  ja: {
    head: "Codex: spare@example.com は再度サインインが必要です", why: "再度サインインするまで、magpie はこのアカウントにリクエストを送りません。", again: "再度サインイン",
    held: "ベンダーは HTTP 400 を返しました。応答にサインインのコードやキーが含まれている可能性があるため、ここには表示しません", status: (e) => `Codex へのサインインが完了しませんでした: ${e}`,
  },
  de: {
    head: "Codex: spare@example.com muss neu angemeldet werden", why: "Bis zur erneuten Anmeldung schickt magpie keine Anfragen an dieses Konto.", again: "Erneut anmelden",
    held: "Der Anbieter antwortete mit HTTP 400; die Antwort wird hier nicht gezeigt, da sie den Code oder Schlüssel der Anmeldung enthalten kann", status: (e) => `Anmeldung bei Codex nicht abgeschlossen: ${e}`,
  },
};

const overlap = (a, b) => a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an account to sign in again is said over Providers, and a failed sign-in says why in the reader's language", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(W)) {
      const w = W[lang];
      for (const width of [420, 1100]) {
        const open = async () => {
          const posts = [], sign = { fail: false, done: false, failedSent: 0 };
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, posts, sign));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: "Codex" }).waitFor();
          const lines = page.locator("#lapsedAccounts .deprecation.lapsed");
          await lines.first().waitFor();
          return { page, errors, posts, sign, lines };
        };
        const signInAgain = async (page, posts, lines) => {
          await lines.first().getByRole("button", { name: W[lang].again, exact: true }).click();
          for (let i = 0; i < 50 && !posts.some((p) => p.path === "/api/signin"); i++) await page.waitForTimeout(50);
          assert.deepEqual(posts.filter((p) => p.path === "/api/signin").map((p) => { const { proxy, ...b } = p.body; return b; }), [{ agent: "codex", user: "spare@example.com" }]);
        };

        await t.test(`${lang} ${width}: the account in use to sign in again has a line, with Sign in again`, async () => {
          const { page, errors, posts, lines } = await open();
          assert.equal(await lines.count(), 1, "only the account in use that has to be signed in again, of a provider turned on");
          assert.equal(await page.locator("#lapsedAccounts", { hasText: "off@example.com" }).count(), 0, "a provider turned off has no line");
          const line = lines.first();
          assert.equal(await line.locator(".dep-head").innerText(), w.head);
          assert.equal(await line.locator(".dep-why").innerText(), w.why);
          const again = line.getByRole("button", { name: w.again, exact: true });
          assert.equal(await again.count(), 1);
          assert.ok(await again.isVisible());
          const box = await line.boundingBox(), b = await again.boundingBox(), head = await line.locator(".dep-head").boundingBox();
          assert.ok(b.x >= box.x - 1 && b.x + b.width <= box.x + box.width + 1, "Sign in again runs out of the line");
          assert.ok(!overlap(b, head), "Sign in again is over the line's words");
          const docW = await page.evaluate(() => document.documentElement.scrollWidth);
          assert.ok(docW <= width, `the page scrolls sideways (${docW} > ${width})`);
          if (process.env.ARTIFACT_DIR) await line.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `providers-lapsed-${engine}-${lang}-${width}.png`) });

          await signInAgain(page, posts, lines);
          await page.locator(".editor").first().waitFor();
          assert.deepEqual(errors, []);
          await page.context().close();
        });

        await t.test(`${lang} ${width}: a sign-in failed in its box says the left-out reason there, not on the status line`, async () => {
          const { page, errors, posts, sign, lines } = await open();
          await signInAgain(page, posts, lines);
          sign.fail = true;
          const why = page.locator(".signing.failed .s");
          await why.waitFor();
          assert.equal(await why.innerText(), w.held);
          const r = await why.boundingBox();
          assert.ok(r.width >= 120, `the reason is left ${r.width}px`);
          for (const b of await page.locator(".signing.failed > button").all()) assert.ok(!overlap(await b.boundingBox(), r), `${await b.innerText()} is over the reason`);
          if (process.env.ARTIFACT_DIR) await page.locator(".signing.failed").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `providers-lapsed-failed-${engine}-${lang}-${width}.png`) });
          assert.ok(!(await page.locator("#status").textContent()).includes(w.held), "the box is on the page: the status line doesn't say it too");
          assert.deepEqual(errors, []);
          await page.context().close();
        });

        await t.test(`${lang} ${width}: a sign-in failed with its box off the page says so on the status line`, async () => {
          const { page, errors, posts, sign, lines } = await open();
          await signInAgain(page, posts, lines);
          await page.evaluate(() => show("routing"));
          await page.locator("#view-routing").waitFor();
          const say = w.status(w.held);
          // every time the status line is set to say it
          await page.evaluate((s) => {
            window.saidFailed = 0;
            new MutationObserver(() => { if (document.querySelector("#status").textContent.trim() === s) window.saidFailed++; })
              .observe(document.querySelector("#status"), { childList: true, characterData: true, subtree: true });
          }, say);
          sign.fail = true;
          await page.waitForFunction((s) => document.querySelector("#status").textContent.includes(s), say);
          assert.equal((await page.locator("#status").textContent()).trim(), say);
          // a sign-in that went on asking would say it again on each answer
          // (every 800ms): give it three answers' time
          for (let i = 0; i < 60 && sign.failedSent < 3; i++) await page.waitForTimeout(50);
          assert.equal(await page.evaluate(() => window.saidFailed), 1, "the status line says the failure once");
          assert.deepEqual(errors, []);
          await page.context().close();
        });

        await t.test(`${lang} ${width}: once the account is signed in again its line goes`, async () => {
          const { page, errors, posts, sign, lines } = await open();
          await signInAgain(page, posts, lines);
          assert.equal(await lines.count(), 1, "the line stays while the sign-in is under way");
          sign.done = true;
          await page.waitForFunction(() => !document.querySelector("#lapsedAccounts .deprecation.lapsed"));
          assert.ok(await page.locator("#lapsedAccounts").evaluate((b) => b.hidden), "the empty box is hidden");
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });
}
