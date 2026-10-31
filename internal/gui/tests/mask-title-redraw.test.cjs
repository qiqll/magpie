// Run with Node's test runner and Playwright on the module path; see README.md.
// Hide accounts keeps a tooltip hidden when the page writes it again: a
// filter button (sessPick) sets its title on each draw, and the address
// it wrote the second time showed, since a title masked once was never
// looked at again. Shown again, it says what it was written last.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve() {
  const providers = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a tooltip written again stays hidden`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve());
    t.after(() => browser.close());
    await page.goto("http://magpie.test/?view=routing");
    await page.evaluate(() => { try { localStorage.removeItem("magpie.maskEmails"); } catch {} });
    await page.evaluate(() => {
      const b = document.createElement("button");
      b.id = "sample";
      b.textContent = "Account";
      b.title = "dev.one@example.com";
      document.querySelector("#view-routing").append(b);
    });
    await page.locator("#rtMask").click();
    await page.waitForFunction(() => document.querySelector("#sample").title === "•••.•••@•••••••.•••");

    // drawn again with another pick, then with the first again
    for (const [raw, want] of [["two@example.org", "•••@•••••••.•••"], ["dev.one@example.com", "•••.•••@•••••••.•••"]]) {
      await page.evaluate((raw) => { document.querySelector("#sample").title = raw; }, raw);
      await page.waitForFunction((w) => document.querySelector("#sample").title === w, want);
      assert.equal(await page.locator("#sample").getAttribute("title"), want);
    }
    // a pick with no address in it is left as it is, and isn't swapped
    // for an earlier address when the accounts show again
    await page.evaluate(() => { document.querySelector("#sample").title = "All computers"; });
    await page.waitForFunction(() => !document.querySelector("#sample[data-pii-title]"));
    assert.equal(await page.locator("#sample").getAttribute("title"), "All computers");
    // a title taken away keeps no address behind it in the page
    await page.evaluate(() => { document.querySelector("#sample").title = "two@example.org"; });
    await page.waitForFunction(() => document.querySelector("#sample[data-pii-title]"));
    await page.evaluate(() => {
      const b = document.querySelector("#sample");
      b.removeAttribute("title");
      b.parentElement.append(document.createElement("i")); // the batch's root is its parent
    });
    await page.waitForFunction(() => !document.querySelector("#sample[data-pii-title]"));
    assert.equal(await page.locator("#sample").getAttribute("title"), null);
    await page.evaluate(() => { document.querySelector("#sample").title = "two@example.org"; });
    await page.waitForFunction(() => document.querySelector("#sample").title === "•••@•••••••.•••");

    await page.locator("#rtMask").click();
    await page.waitForFunction(() => !document.querySelector("#sample[data-pii-title]"));
    assert.equal(await page.locator("#sample").getAttribute("title"), "two@example.org");
    assert.deepEqual(errors, []);
  });
}
