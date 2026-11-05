// Models › Accounts: every subscription account magpie keeps, of every
// provider, on one page (需求方 2026-11-04, after CLIProxyAPI's credentials
// page): each one's state, its allowances' windows, the requests it served
// in the last 5 hours and its proxy, found by provider, state or a word,
// and used, paused, signed in again or read again in place. Adding and
// removing one stay in the provider's editor, which Open goes to.
//
// A page the accounts plugin adds to the window (kernel.js), its words in
// i18n.js beside this file. This file registers the page and its markup,
// before app.js; page.js, after app.js, is the page's code. The shell
// loads the providers for it (loads: "providers").
magpie.plugin("accounts", (ctx) => {
  ctx.slots.add("page", {
    key: "accounts",
    title: "Accounts",
    loads: "providers",
    html: `
  <div class="sess-tools acx-tools">
    <input class="sess-filter acx-q" id="acxQ" type="search" spellcheck="false" autocomplete="off" placeholder="Search accounts" aria-label="Search accounts" data-tp>
    <button class="sess-pick" id="acxProvider" type="button"></button>
    <span class="grow"></span>
    <div id="acxState"></div>
  </div>
  <p class="acx-sum" id="acxSum" role="status"></p>
  <div class="list acx-list" id="acxList"></div>
  <p class="usage-note acx-note" id="acxNote"></p>
`,
    mount() { acxMount(); },
    open() { return acxLoad(false); },
    refresh(again) { return acxLoad(again); },
    reload() { return acxLoad(true); },
  });
});
