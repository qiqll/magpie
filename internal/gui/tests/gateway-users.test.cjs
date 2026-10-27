// Run with Node's test runner and Playwright on the module path; see README.md.
// The Gateway page's User bindings: bound by user, shown with nobody
// bound yet and saying so; bound by session, hidden while nobody is; with
// users, each provider's accounts counted, as a first or a spare, each
// said out and until when, able to answer, or unknown (never able to
// answer when the serving magpie didn't say); the users with their first
// account and spares in order, each with its state, provider and gateway
// key; an account no longer on its provider said so; how the gateway
// binds said, by user with each tier's most accounts and how many share
// an account, and when a seat is let go, and bound by
// session the bindings kept and shown as they are; each user's tier, the
// default for one bound before tiers; each user's last use, none said for
// one bound before it was kept; a search that asks again and keeps its focus and the accounts'
// states; a page of users said as such, and an unreadable file said,
// never shown as nobody. In every language, wide and narrow, in Chromium
// and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = new Date(Date.now() - 3 * 3600e3).toISOString();
// three hours off: untilText says "in 3h" however long the run takes
const back = new Date(Date.now() + 3 * 3600e3 + 10 * 60e3).toISOString();
// two hours ago: ago() says "2 hours ago" however long the run takes
const used = new Date(Date.now() - 2 * 3600e3 - 60e3).toISOString();
const providers = {
  providers: [{ id: "antigravity", name: "Antigravity", icon: "generic", models: [{ id: "m", name: "M", on: true }], agents: [] }],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls: [], groups: [] },
};
const seat = (p, a) => `${p}@${a}`;
const everyone = [
  { user: "1", provider: "antigravity", providerName: "Antigravity", account: "a@x.com", seat: seat("antigravity", "a@x.com"), spares: [{ account: "b@x.com", seat: seat("antigravity", "b@x.com"), at }], key: "k1", keyName: "Tenant A", at, tier: "pro", used },
  { user: "2", provider: "antigravity", providerName: "Antigravity", account: "b@x.com", seat: seat("antigravity", "b@x.com"), spares: [{ account: "f@x.com", seat: seat("antigravity", "f@x.com"), at }], key: "k1", keyName: "Tenant A", at },
  { user: "3", provider: "antigravity", providerName: "Antigravity", account: "a@x.com", seat: seat("antigravity", "a@x.com"), spares: [{ account: "e@x.com", seat: seat("antigravity", "e@x.com"), at }], key: "", keyName: "", at },
  { user: "4", provider: "codex", providerName: "ChatGPT", account: "c@x.com", seat: seat("codex", "c@x.com"), key: "gone", keyName: "", keyGone: true, at },
  { user: "a-very-long-user-id-from-the-service-0123456789abcdef", provider: "codex", providerName: "ChatGPT", account: "c@x.com", seat: seat("codex", "c@x.com"), key: "k1", keyName: "Tenant A", at },
  // a key the list couldn't be read for: named by its id, not said removed
  { user: "6", provider: "codex", providerName: "ChatGPT", account: "c@x.com", seat: seat("codex", "c@x.com"), key: "0123456789abcdef", keyName: "", at: "0001-01-01T00:00:00Z" },
];
// user-tiers.json as the serving magpie holds to it
const tiers = { default: "free", tiers: [{ name: "free", credits: 1000, max_accounts: 2, share: 2 }, { name: "pro", credits: 5000, max_accounts: 4, share: 1 }, { name: "team", credits: 0, max_accounts: 6, share: 0 }] };
const accounts = [
  { provider: "antigravity", providerName: "Antigravity", account: "a@x.com", seat: seat("antigravity", "a@x.com"), users: 2, spares: 0, out: "quota", outUntil: back, outKnown: true },
  { provider: "antigravity", providerName: "Antigravity", account: "b@x.com", seat: seat("antigravity", "b@x.com"), users: 2, spares: 1, outKnown: true },
  { provider: "codex", providerName: "ChatGPT", account: "c@x.com", seat: seat("codex", "c@x.com"), users: 3, spares: 0, gone: true, outKnown: false },
  { provider: "removed", providerName: "removed", account: "d@x.com", seat: seat("removed", "d@x.com"), users: 1, spares: 0, gone: true, providerGone: true, outKnown: false },
  // the serving magpie didn't say what rests: unknown, never "can answer"
  { provider: "antigravity", providerName: "Antigravity", account: "e@x.com", seat: seat("antigravity", "e@x.com"), users: 1, spares: 1, outKnown: false },
  { provider: "antigravity", providerName: "Antigravity", account: "f@x.com", seat: seat("antigravity", "f@x.com"), users: 1, spares: 1, out: "rate", outModel: "m-1", outKnown: true },
  // its time passed while the page kept it (a search): unknown again
  { provider: "antigravity", providerName: "Antigravity", account: "g@x.com", seat: seat("antigravity", "g@x.com"), users: 1, spares: 0, out: "restricted", outUntil: new Date(Date.now() - 60e3).toISOString(), outKnown: true },
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
      const hay = (u) => `${u.user} ${u.account} ${(u.spares || []).map((s) => s.account).join(" ")} ${u.providerName} ${u.keyName}`.toLowerCase();
      const hit = users.all.filter((u) => words.every((w) => hay(u).includes(w)));
      // a search leaves the accounts and the mode out, as the server does
      const out = { total: users.all.length, matched: hit.length + (users.more || 0), users: hit };
      if (!url.searchParams.get("q")) Object.assign(out, { accounts: users.all.length ? accounts : [], mode: users.mode, tiers: users.tiers });
      return json(out);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    head: "User bindings", two: "2 users", gone: "no longer on its provider", provGone: "no longer in magpie", none: "without a gateway key", removed: "a removed gateway key", keyID: "gateway key 01234567", bound: "bound", match: "No users match “nobody”", more: "Showing 6 of 300", unread: "user-accounts.json can't be read: unexpected end",
    out: "out · quota used up · back in 3h", can: "can answer", unknown: "status unknown", model: "out · rate limited for m-1", spareNote: "2 users, 1 as a spare", first: "first", spare: "spare",
    byUser: "Bound by user: accounts of each provider each user may have, by its tier: free up to 2, each shared by up to 2 (default) · pro up to 4, each its own alone · team up to 6, shared by any number A user that sends a provider nothing for 3 days lets go of its accounts there.", empty: "No user is bound yet", lastUsed: "last used 2 hours ago", tierPro: "tier pro", tierFree: "tier free", tiersUnknown: "Bound by user; the tiers in user-tiers.json are unknown", off: "Binding by user is off (start option --bind=user)",
  },
  zh: {
    head: "用户绑定", two: "2 个用户", gone: "已不在该供应商下", provGone: "已不在 magpie 中", none: "未使用网关密钥", removed: "已删除的网关密钥", keyID: "网关密钥 01234567", bound: "绑定", match: "没有和“nobody”匹配的用户", more: "显示 300 个中的 6 个", unread: "无法读取 user-accounts.json：unexpected end",
    out: "暂不可用 · 额度用尽 · 3 小时后恢复", can: "可用", unknown: "状态未知", model: "暂不可用 · m-1 限流", spareNote: "2 个用户，其中 1 个作为备用", first: "主账号", spare: "备用",
    byUser: "按用户绑定：每个用户在每个供应商下的账号上限按等级：free 最多 2 个，每个号最多 2 人共用（默认） · pro 最多 4 个，每个号独占 · team 最多 6 个，共用人数不限 用户 3 天没有向某个供应商发请求，会让出在那里的账号。", empty: "还没有用户被绑定", lastUsed: "最后使用 2小时前", tierPro: "等级 pro", tierFree: "等级 free", tiersUnknown: "按用户绑定；user-tiers.json 中的等级未知", off: "按用户绑定未开启（启动参数 --bind=user）",
  },
  "zh-TW": {
    head: "使用者綁定", two: "2 位使用者", gone: "已不在該供應商下", provGone: "已不在 magpie 中", none: "未使用閘道金鑰", removed: "已刪除的閘道金鑰", keyID: "閘道金鑰 01234567", bound: "綁定", match: "沒有和「nobody」匹配的使用者", more: "顯示 300 位中的 6 位", unread: "無法讀取 user-accounts.json：unexpected end",
    out: "暫不可用 · 額度用盡 · 3 小時後恢復", can: "可用", unknown: "狀態未知", model: "暫不可用 · m-1 限流", spareNote: "2 位使用者，其中 1 位作為備用", first: "主帳號", spare: "備用",
    byUser: "依使用者綁定：每位使用者在每個供應商下的帳號上限依等級：free 最多 2 個，每個帳號最多 2 人共用（預設） · pro 最多 4 個，每個帳號獨占 · team 最多 6 個，共用人數不限 使用者 3 天沒有向某個供應商發送請求，會讓出在那裡的帳號。", empty: "還沒有使用者被綁定", lastUsed: "最後使用 2小時前", tierPro: "等級 pro", tierFree: "等級 free", tiersUnknown: "依使用者綁定；user-tiers.json 中的等級未知", off: "依使用者綁定未開啟（啟動參數 --bind=user）",
  },
  ja: {
    head: "ユーザーの割り当て", two: "2 ユーザー", gone: "プロバイダにもうありません", provGone: "magpie にもうありません", none: "ゲートウェイキーなし", removed: "削除されたゲートウェイキー", keyID: "ゲートウェイキー 01234567", bound: "割り当て", match: "「nobody」に一致するユーザーはいません", more: "300 件中 6 件を表示中", unread: "user-accounts.json を読み込めません：unexpected end",
    out: "利用不可 · クォータ切れ · 3 時間後に復帰", can: "利用可能", unknown: "状態は不明", model: "利用不可 · m-1 でレート制限", spareNote: "2 ユーザー（うち 1 は予備）", first: "最初", spare: "予備",
    byUser: "ユーザーごとに割り当て：各ユーザーのプロバイダごとのアカウント上限はティアで決まります：free 最大 2、各アカウントを最大 2 人で共有（デフォルト） · pro 最大 4、各アカウントを専有 · team 最大 6、共有人数は無制限 3 日間プロバイダにリクエストを送らなかったユーザーは、そのアカウントを手放します。", empty: "まだ割り当てられたユーザーはいません", lastUsed: "最終利用 2 時間前", tierPro: "ティア pro", tierFree: "ティア free", tiersUnknown: "ユーザーごとに割り当て。user-tiers.json のティアは不明です", off: "ユーザーごとの割り当てはオフです（起動オプション --bind=user）",
  },
  de: {
    head: "Nutzerbindungen", two: "2 Nutzer", gone: "nicht mehr beim Anbieter", provGone: "nicht mehr in magpie", none: "ohne Zugangsschlüssel", removed: "ein entfernter Zugangsschlüssel", keyID: "Zugangsschlüssel 01234567", bound: "gebunden", match: "Keine Nutzer passen zu „nobody“", more: "6 von 300 angezeigt", unread: "user-accounts.json kann nicht gelesen werden: unexpected end",
    out: "nicht verfügbar · Kontingent aufgebraucht · zurück in 3 Std.", can: "verfügbar", unknown: "Status unbekannt", model: "nicht verfügbar · Rate-Limit für m-1", spareNote: "2 Nutzer, 1 als Ersatz", first: "erstes", spare: "Ersatz",
    byUser: "Nach Nutzer gebunden: Konten je Anbieter für jeden Nutzer nach seiner Stufe: free bis zu 2, jedes von bis zu 2 geteilt (Standard) · pro bis zu 4, jedes für sich allein · team bis zu 6, von beliebig vielen geteilt Ein Nutzer, der einem Anbieter 3 Tage lang nichts sendet, gibt seine Konten dort frei.", empty: "Noch kein Nutzer gebunden", lastUsed: "zuletzt genutzt vor 2 Stunden", tierPro: "Stufe pro", tierFree: "Stufe free", tiersUnknown: "Nach Nutzer gebunden; die Stufen in user-tiers.json sind unbekannt", off: "Bindung nach Nutzer ist aus (Startoption --bind=user)",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [960, 420]) {
      test(`${engine} ${lang} ${width}px: User bindings`, async (t) => {
        assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const users = { all: [], asked: [], mode: "user", tiers };
        await page.route("**/*", serve(lang, users));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-gateway-users.png`), fullPage: true });
          }
          await browser.close();
        });
        const w = words[lang];
        const text = async (l) => (await l.textContent()).replace(/\s+/g, " ").trim();

        // nobody bound, by session: the section stays hidden
        users.mode = "session";
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gateway .name").waitFor();
        await page.waitForFunction(() => typeof userBinds !== "undefined" && userBinds !== null);
        assert(await page.locator("#userBindsBlock").evaluate((b) => b.hidden), "hidden while nobody is bound, by session");
        // nobody bound, by user: shown, how it binds said, and nobody yet
        users.mode = "user";
        await page.reload();
        await page.locator("#userBinds .none").waitFor();
        assert(!(await page.locator("#userBindsBlock").evaluate((b) => b.hidden)), "shown by user with nobody bound");
        assert.equal(await text(page.locator("#userBindsMode")), w.byUser);
        assert((await text(page.locator("#userBinds .none"))).startsWith(w.empty), "nobody bound yet is said");
        assert.equal((await page.locator("#userBindsCount").textContent()).trim(), "0");

        // users: accounts counted, users with account, provider and key
        users.all = everyone;
        await page.reload();
        await page.locator("#userBinds .user-bind").first().waitFor();
        const block = page.locator("#userBindsBlock");
        assert.equal((await block.locator(".row-head .label").textContent()).trim(), w.head);
        assert.equal((await page.locator("#userBindsCount").textContent()).trim(), "6");
        assert.equal(await text(page.locator("#userBindsMode")), w.byUser, "how the gateway binds, and the most accounts");
        const accountRows = page.locator("#userBindAccounts .user-account");
        assert.equal(await accountRows.count(), 7);
        assert.equal((await accountRows.nth(0).locator(".note").textContent()).trim(), w.two);
        assert.match(await accountRows.nth(2).textContent(), new RegExp(w.gone));
        assert(await accountRows.nth(2).evaluate((r) => r.classList.contains("gone")));
        assert.match(await accountRows.nth(3).textContent(), new RegExp(w.provGone));

        // each account's state: out until when, able to answer, unknown
        const sub = (i) => text(accountRows.nth(i).locator(".sub"));
        assert.equal(await sub(0), `Antigravity · ${w.out}`, "out, and until when");
        assert.equal(await sub(1), `Antigravity · ${w.can}`);
        assert.equal(await sub(4), `Antigravity · ${w.unknown}`, "not told is unknown, never able to answer");
        assert.equal(await sub(5), `Antigravity · ${w.model}`, "one model out says which");
        assert.equal(await sub(6), `Antigravity · ${w.unknown}`, "a time passed is unknown, never able to answer");
        assert.equal((await accountRows.nth(1).locator(".note").textContent()).trim(), w.spareNote);
        for (const [i, out] of [[0, true], [1, false], [4, true], [5, true]]) {
          assert.equal(await accountRows.nth(i).evaluate((r) => r.classList.contains("out")), out, `account ${i} marked out`);
        }

        const userRows = page.locator("#userBinds .user-bind");
        assert.equal(await userRows.count(), 6);
        const first = await text(userRows.nth(0));
        assert(first.includes("Antigravity · Tenant A"), first);
        assert(first.includes(w.tierPro), `the tier its last request named: ${first}`);
        assert((await text(userRows.nth(1))).includes(w.tierFree), "one bound before tiers is of the default tier");
        assert.match(first, new RegExp(w.bound));
        // Intl spaces "2 小時前" in one engine and not the other
        assert(first.replace(/\s/g, "").includes(w.lastUsed.replace(/\s/g, "")), `its last use: ${first}`);
        assert.equal(await userRows.nth(1).locator(".user-used").count(), 0, "no last use said for one bound before it was kept");
        // its first, then its spares in order, each with its state
        const chips = userRows.nth(0).locator(".user-seat");
        assert.equal(await chips.count(), 2);
        assert.equal(await text(chips.nth(0)), `a@x.com (${w.first} · ${w.out})`);
        assert.equal(await text(chips.nth(1)), `b@x.com (${w.spare} · ${w.can})`);
        assert.deepEqual(await chips.evaluateAll((cs) => cs.map((c) => [...c.classList].filter((k) => k !== "user-seat").join(" "))), ["first out", "spare"]);
        assert.equal(await text(userRows.nth(2).locator(".user-seat").nth(1)), `e@x.com (${w.spare} · ${w.unknown})`);
        assert.equal(await text(userRows.nth(3).locator(".user-seat")), `c@x.com (${w.first} · ${w.gone})`);
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

        // the search asks again, narrows, keeps its focus and the states;
        // Escape clears it
        const find = page.locator("#findUser");
        assert(await find.isVisible(), "five users have a search");
        await find.click();
        await find.pressSequentially("f@x");
        await page.waitForFunction(() => document.querySelectorAll("#userBinds .user-bind").length === 1);
        assert.equal(await text(userRows.nth(0).locator(".name").first()), "2", "a spare's account matches");
        assert.equal(await text(userRows.nth(0).locator(".user-seat").nth(1)), `f@x.com (${w.spare} · ${w.model})`, "a search keeps the accounts' states");
        assert.equal(await accountRows.count(), 7, "a search keeps the accounts the page has");
        assert.equal(await text(page.locator("#userBindsMode")), w.byUser, "and how the gateway binds");
        assert((await text(userRows.nth(0))).includes(w.tierFree), "a search keeps the default tier");
        assert(await find.evaluate((f) => document.activeElement === f), "typing keeps the search's focus");
        assert(users.asked.includes("f@x"), "the words go to the server");
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

        // bound by session: said, the bindings shown as they are, read-only
        users.mode = "session";
        await page.reload();
        await page.locator("#userBinds .user-bind").first().waitFor();
        assert((await text(page.locator("#userBindsMode"))).startsWith(w.off), "by session, binding by user is said off");
        assert.equal(await userRows.count(), 6, "the bindings are kept");
        assert.equal(await accountRows.count(), 7);
        assert.deepEqual(await block.locator("button").evaluateAll((bs) => bs.filter((b) => !b.classList.contains("copy")).length), 0, "nothing to change them with");
        // by user, its tiers not known (user-tiers.json unread): said so,
        // and a user is said only the tier its request named
        users.mode = "user";
        users.tiers = undefined;
        await page.reload();
        await page.locator("#userBinds .user-bind").first().waitFor();
        assert.equal(await text(page.locator("#userBindsMode")), w.tiersUnknown);
        assert((await text(userRows.nth(0))).includes(w.tierPro));
        assert(!(await text(userRows.nth(1))).includes(w.tierFree), "no default said while the tiers are unknown");
        // an older magpie that doesn't say how it binds: nothing said
        users.mode = undefined;
        await page.reload();
        await page.locator("#userBinds .user-bind").first().waitFor();
        assert(await page.locator("#userBindsMode").evaluate((m) => m.hidden));

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
