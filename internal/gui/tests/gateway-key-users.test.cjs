// Bound by user, a gateway key's users are its own until the owner lets
// it share them with the other keys that do (T-089): turned on from its
// row's menu, said in its row while on, turned off the same way; an
// issued key's are always its own, its item there, off, saying why.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const words = {
  en: { more: "Key actions", share: "Share users with other keys", own: "Keep users to this key", tag: "Users shared", shared: "Server shares its users with other keys", kept: "Server keeps its users to itself", why: "An issued key's users are its own" },
  zh: { more: "密钥操作", share: "与其他密钥共用用户", own: "用户只归此密钥", tag: "共用用户", shared: "Server 已与其他密钥共用用户", kept: "Server 的用户已只归它自己", why: "签发的密钥的用户只归它自己" },
  "zh-TW": { more: "金鑰操作", share: "與其他金鑰共用使用者", own: "使用者只歸此金鑰", tag: "共用使用者", shared: "Server 已與其他金鑰共用使用者", kept: "Server 的使用者已只歸它自己", why: "簽發的金鑰的使用者只歸它自己" },
  ja: { more: "キーの操作", share: "ユーザーを他のキーと共有", own: "ユーザーをこのキー専用にする", tag: "ユーザー共有", shared: "Server のユーザーを他のキーと共有しました", kept: "Server のユーザーをこのキー専用にしました", why: "発行したキーのユーザーはそのキー専用です" },
  de: { more: "Schlüsselaktionen", share: "Nutzer mit anderen Schlüsseln teilen", own: "Nutzer nur für diesen Schlüssel", tag: "Nutzer geteilt", shared: "Server teilt seine Nutzer mit anderen Schlüsseln", kept: "Server behält seine Nutzer für sich", why: "Die Nutzer eines ausgestellten Schlüssels gehören nur ihm" },
};

const keys = () => [
  { id: "front", name: "Front", masked: "sk-magpie-key-…111111", sharesUsers: true },
  { id: "server", name: "Server", masked: "sk-magpie-key-…222222" },
  { id: "ci", name: "CI run", masked: "sk-magpie-key-…333333", issuer: "front", expires: "2099-01-01T00:00:00Z" },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width} ${lang}: a gateway key shares its users and keeps them again`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const events = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", events, { lan: true, keys: keys() }));
        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#gatewayKeys .acc[data-key]").last().waitFor();
        const row = (id) => page.locator(`#gatewayKeys .acc[data-key="${id}"]`);
        const tag = (id) => row(id).locator(".key-users");
        const item = (name) => page.locator(".row-menu [role=menuitem]").filter({ hasText: name });
        const menu = async (id) => {
          await row(id).hover();
          await row(id).getByRole("button", { name: w.more, exact: true }).click();
          await page.locator(".row-menu").waitFor();
        };
        // shared says so always, not only on hover; a key of its own says nothing
        await page.mouse.move(0, 0);
        assert.equal((await tag("front").textContent()).trim(), w.tag);
        assert.ok((await tag("front").boundingBox()).width > 0, "the shared tag is hidden");
        assert.ok((await tag("front").getAttribute("title")).includes("X-Magpie-User"), "the tag says what sharing users means");
        assert.equal(await tag("server").count(), 0);

        await menu("server");
        assert.equal(await item(w.own).count(), 0);
        await item(w.share).click();
        await page.waitForFunction(() => !!document.querySelector('#gatewayKeys .acc[data-key="server"] .key-users'));
        assert.deepEqual(events.filter((e) => e.action === "users-key").map((e) => e.body), [{ key: "server", sharesUsers: true }]);
        assert.equal((await page.locator("#status").textContent()).trim(), w.shared);
        assert.equal((await tag("server").textContent()).trim(), w.tag);

        await menu("server");
        assert.equal(await item(w.share).count(), 0);
        await item(w.own).click();
        await page.waitForFunction(() => !document.querySelector('#gatewayKeys .acc[data-key="server"] .key-users'));
        assert.deepEqual(events.filter((e) => e.action === "users-key").at(-1).body, { key: "server", sharesUsers: false });
        assert.equal((await page.locator("#status").textContent()).trim(), w.kept);

        // an issued key: the item is there, off, and says why
        await menu("ci");
        const off = item(w.share);
        assert.equal(await off.isDisabled(), true);
        assert.equal((await off.locator(".rm-why").textContent()).trim(), w.why);
        await page.keyboard.press("Escape");
        assert.equal(events.filter((e) => e.action === "users-key" && e.body.key === "ci").length, 0);

        // the row still fits: nothing runs past the window's edge
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        const rowBox = await row("front").boundingBox(), b = await tag("front").boundingBox();
        assert.ok(b.x >= rowBox.x && b.x + b.width <= rowBox.x + rowBox.width + 1, "the tag runs out of its row");
        assert.deepEqual(errors, []);
      });
    }
  }
}
