// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's editor opens on a 570ms height morph (slotMorph), cut
// off at its foot till it ends. A control the keyboard reached there, the
// rule's ⋯, scrolled the editor itself to bring it into sight, and that
// scroll shut the ⋯'s menu just opened (scrollCloses), so a reader quick on
// the keys saw it flash and go. The editor is clipped without becoming a
// box that scrolls, so the menu stays; a reader's own wheel still closes it.
// Chromium and WebKit, every language, at a wide and a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/flash", name: "Deepseek-V4.1-Flash", providerName: "WorkBuddy AI", icon: "generic", context: 1000000, ready: true },
];
const member = (id) => ({ id, ready: true, context: 1000000, images: true });
const rule = (use, more) => ({ use, pause: false, tokens: 0, images: false, effort: "", agents: [], intent: "", compact: false, folders: [], exceptFolders: [], time: null, ...more });
const groups = () => ({
  models, deciders: [],
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/flash"], routing: "order", ready: true,
    rules: [rule("b/flash", { images: true })], memberInfo: [member("a/opus"), member("b/flash")] }],
  pools: [],
});

const words = { en: "Edit", zh: "编辑", "zh-TW": "編輯", ja: "編集", de: "Bearbeiten" };

function serve(lang) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404 }); }
  };
}

// still: the control held in one place for three frames in turn
const still = (loc) => loc.evaluate((e) => new Promise((done, fail) => {
  let last = NaN, same = 0, frames = 0;
  const look = () => {
    if (!e.isConnected) return done();
    const y = e.getBoundingClientRect().top;
    same = y === last ? same + 1 : 0;
    last = y;
    if (same >= 3) done();
    else if (++frames > 300) fail(new Error("still: kept moving, its top at " + y));
    else requestAnimationFrame(look);
  };
  requestAnimationFrame(look);
}));

// wheel to a control, as the reader would, and return where it is once it
// holds still
async function reach(page, loc) {
  const height = page.viewportSize().height;
  await page.mouse.move(Math.min(550, (page.viewportSize().width / 2) | 0), 300);
  for (let i = 0; i < 30; i++) {
    await still(loc);
    const y = (await loc.boundingBox()).y;
    if (y > 80 && y < height - 160) break;
    await page.mouse.wheel(0, y < 80 ? -300 : 300);
    await page.waitForTimeout(150);
  }
  await still(loc);
  return loc.boundingBox();
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}: a rule's ⋯ opened by keyboard while its editor opens keeps its menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");

        const group = page.locator(".rt-group").first();
        await group.waitFor();
        const b = await reach(page, group.locator("button", { hasText: words[lang] }));
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        // the editor's own height morph, held at its start: the ⋯ is past
        // its foot, cut off, as a quick Tab finds it
        await page.waitForFunction(() => {
          const e = document.querySelector(".rt-gedit");
          const grow = e?.getAnimations().find((a) => a.effect?.target === e);
          if (!grow) return false;
          grow.pause();
          grow.currentTime = 0;
          return true;
        });
        const cut = await page.evaluate(() => {
          const e = document.querySelector(".rt-gedit");
          return e.querySelector(".rt-rmore").getBoundingClientRect().top - e.getBoundingClientRect().bottom;
        });
        assert(cut > 0, `the ⋯ is past the opening editor's foot (${cut}px)`);

        // Tab reaches it (Option-Tab in WebKit, which as Safari tabs to
        // fields alone), bringing it into sight, and it is opened in the
        // same moment, as Enter right after would: the focus's scroll event
        // comes in a later frame. (WebKit scrolls the clipped editor for
        // Tab, not for focus() from script.)
        const TAB = engine === "webkit" ? "Alt+Tab" : "Tab";
        await page.evaluate(() => {
          const more = document.querySelector(".rt-gedit .rt-rmore");
          more.addEventListener("focusin", () => queueMicrotask(() => {
            more.click();
            window.t047 = !!document.querySelector(".row-menu");
          }), { once: true });
        });
        await page.locator(".rt-gedit .rt-rule .rt-model").first().focus({ preventScroll: true });
        for (let i = 0; i < 6 && !(await page.evaluate(() => "t047" in window)); i++) await page.keyboard.press(TAB);
        assert(await page.evaluate(() => "t047" in window), "Tab reaches the rule's ⋯");
        const after = await page.evaluate(() => new Promise((done) => {
          const e = document.querySelector(".rt-gedit");
          let n = 0;
          const look = () => {
            if (++n < 10) return requestAnimationFrame(look);
            done({ opened: window.t047, open: !!document.querySelector(".row-menu"), scrolled: e.scrollTop });
          };
          requestAnimationFrame(look);
        }));
        assert.equal(after.opened, true, "Enter on the ⋯ opens its menu");
        assert.equal(after.open, true, "the menu is still open once the focus's scroll has come");
        assert.equal(after.scrolled, 0, "the opening editor doesn't scroll itself");

        // the morph runs on to its end and the menu stays
        await page.evaluate(() => {
          const e = document.querySelector(".rt-gedit");
          return Promise.all(e.getAnimations().map((a) => { a.play(); return a.finished; }));
        });
        await page.waitForFunction(() => document.querySelector(".rt-gedit").style.overflow === "");
        assert(await page.locator(".row-menu").isVisible(), "the menu stays as the editor ends its morph");

        // a reader's own wheel still closes it, toward whichever end the
        // page has room to go
        const room = await page.evaluate(() => {
          const v = document.getElementById("view-routing");
          return { down: v.scrollHeight - v.clientHeight - v.scrollTop, up: v.scrollTop };
        });
        assert(room.down > 50 || room.up > 50, `the page has room to wheel (${JSON.stringify(room)})`);
        await page.mouse.move(Math.min(550, (width / 2) | 0), 300);
        await page.mouse.wheel(0, room.down > 50 ? 200 : -200);
        await page.waitForFunction(() => !document.querySelector(".row-menu"));
        assert.deepEqual(errors, []);
      });
    }
  }
}
