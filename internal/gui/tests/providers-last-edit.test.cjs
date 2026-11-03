// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's editor opened by its address (?view=providers&edit=<id>)
// while the Providers page draws the kept list first (#1519): the kept
// list leaves out each provider's proxies, headers and extra body
// (keptProvider), so an editor drawn on it showed every account on the
// provider's proxy, and a Save, then or after the fresh list came in,
// wrote that over the proxies the user had set (T-083). The editor now
// opens only on the fresh list, as a row clicked on the kept one does,
// and a save asked while only the kept list is in leaves those fields out,
// for the server to keep; a magpie://import link's Add and Replace, which
// can be drawn on the kept list, send all of theirs. A key provider and a
// subscription, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const SLOW = 1500; // the fresh read of the list
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [],
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false },
  keyList: [{ id: "aaaaaaaaaa", name: "Team", masked: "sk-…one", on: true, active: true }, { id: "bbbbbbbbbb", name: "", masked: "sk-…two", on: true }],
  proxy: "http://10.0.0.2:7890",
  accountProxies: { aaaaaaaaaa: "socks5://10.0.0.5:1080", bbbbbbbbbb: "direct" },
  headers: { "X-Team": "t1" },
  extraBody: { prompt_cache_key: "k" },
};
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS",
    logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }, { user: "spare@example.com", plan: "PLUS", on: true }],
  },
  proxy: "http://10.0.0.2:7890",
  accountProxies: { "me@example.com": "socks5://10.0.0.5:1080", "spare@example.com": "direct" },
};
const list = (ps) => ({ providers: ps, presets: [], excluded: [], gateway: { running: true, window: true } });
// what keptProvider keeps of each
const bare = ({ proxy, accountProxies, headers, extraBody, ...p }) => p;
const fresh = list([relay, codex]);
const kept = list([bare(relay), bare(codex)]);
// a magpie://import link replacing Relay, with headers and extra body
const linked = {
  replaces: "Relay",
  provider: { id: "relay", name: "Relay", chat: "https://linked.example.com/v1", key: "sk-linked", models: ["big"], headers: { "X-Link": "l1" }, extraBody: { route: "fast" } },
};

function serve(lang, reads, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(url.searchParams.get("last") === "1" ? null : state);
    if (url.pathname === "/api/providers") {
      if (url.searchParams.get("last") === "1") return json(kept);
      reads.asked++;
      await new Promise((r) => setTimeout(r, SLOW));
      reads.done++;
      return json(fresh);
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/import/link") return json(linked);
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(fresh);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, t, lang, query) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const reads = { asked: 0, done: 0 }, posts = [];
  await page.route("**/*", serve(lang, reads, posts));
  await page.goto("http://magpie.test/?" + query);
  return { page, errors, reads, posts };
}

const row = (page) => page.locator("#providers .row.provider:not(.pv-sk-row)");
async function save(page, posts) {
  const n = posts.length;
  await page.locator(".editor .bar button.primary").last().click();
  for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
  assert.equal(posts.length, n + 1, "Save posted nothing");
  return posts.at(-1);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const [p, want] of [
      [relay, { proxy: relay.proxy, accountProxies: relay.accountProxies, headers: relay.headers, extraBody: relay.extraBody }],
      [codex, { proxy: codex.proxy, accountProxies: codex.accountProxies }],
    ]) {
      test(`${engine} ${lang}: ${p.name} opened by its address on the kept list keeps its proxies on Save`, async (t) => {
        const { page, errors, reads, posts } = await open(engine, t, lang, `view=providers&edit=${p.id}`);
        await row(page).first().waitFor();
        assert.equal(reads.done, 0, "the kept list was not drawn first");
        assert.equal(await page.locator(".editor").count(), 0, "the editor opened on the kept list");
        await page.locator(".editor .acct-proxy").first().waitFor({ timeout: SLOW + 4000 });
        assert(reads.done >= 1, "the editor opened before the fresh list was in");
        assert.equal(await page.locator(".editor .proxy-pick .proxy-url").first().inputValue(), p.proxy, "the provider's proxy not shown");
        const { body } = await save(page, posts);
        assert.equal(body.id, p.id);
        for (const [k, v] of Object.entries(want)) assert.deepEqual(body[k], v, `${k} saved as ${JSON.stringify(body[k])}`);
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: a row clicked before the fresh list opens its editor on it`, async (t) => {
      const { page, errors, reads } = await open(engine, t, lang, "view=providers&edit=relay");
      await row(page).first().waitFor();
      assert.equal(reads.done, 0, "the kept list was not drawn first");
      await page.locator(".row.provider", { hasText: "Relay" }).first().click();
      await page.locator(".editor .acct-proxy").first().waitFor({ timeout: SLOW + 4000 });
      assert.equal(await page.locator(".editor .proxy-pick .proxy-url").first().inputValue(), relay.proxy);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: an import link drawn on the kept list sends its headers and extra body`, async (t) => {
      const { page, errors, reads, posts } = await open(engine, t, lang, "view=providers&import=link");
      await page.locator(".editor.import .bar button.primary").waitFor();
      const n = posts.length;
      await page.locator(".editor.import .bar button.primary").click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      assert.equal(reads.done, 0, "the fresh list was in already");
      const { body } = posts.at(-1);
      assert.deepEqual(body.headers, linked.provider.headers);
      assert.deepEqual(body.extraBody, linked.provider.extraBody);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a save asked while only the kept list is in leaves its proxies, headers and extra body out`, async (t) => {
      const { page, errors, reads, posts } = await open(engine, t, lang, "view=providers");
      await row(page).first().waitFor();
      const sent = await page.evaluate(async () => {
        const all = { proxy: "", accountProxies: {}, headers: {}, extraBody: {} };
        const k = providers;
        editing = "relay";
        await providerAction("save", { id: "relay", ...all });
        // an Add's are its own: sent as they are
        providers = k;
        editing = { custom: true };
        await providerAction("save", { id: "other", new: true, ...all });
        return k.last;
      });
      assert.equal(sent, true, "the save was not asked on the kept list");
      assert.equal(reads.done, 0, "the fresh list was in already");
      assert.equal(posts.length, 2);
      for (const k of ["proxy", "accountProxies", "headers", "extraBody"]) {
        assert(!(k in posts[0].body), `${k} sent from the kept list`);
        assert(k in posts[1].body, `an Add's ${k} left out`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
