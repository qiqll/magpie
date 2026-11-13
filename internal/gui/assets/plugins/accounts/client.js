// Models › Accounts: every subscription account magpie keeps, of every
// provider, on one page (需求方 2026-11-04, after CLIProxyAPI's credentials
// page): each one's state, its allowances' windows, the requests it served
// in the last 5 hours and its proxy, found by provider, state or a word,
// and used, paused, signed in again or read again in place. Add account
// signs one in here, as Providers does (需求方 2026-11-13: Claude for now);
// removing one stays in the provider's editor, which Open goes to.
//
// A page the accounts plugin adds to the window (kernel.js), its words in
// i18n.js beside this file. This file registers the page and its markup,
// before app.js; page.js, after app.js, is the page's code. It adds Proxy
// IPs too (ips.js), which shares the paid period's pill (renew.js). The shell
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
    <button class="sess-pick" id="acxKey" type="button"></button>
    <button class="sess-pick" id="acxAdded" type="button"></button>
    <button class="sess-pick" id="acx5h" type="button"></button>
    <button class="sess-pick" id="acx7d" type="button"></button>
    <button class="sess-pick" id="acxBanned" type="button"></button>
    <button class="sess-pick" id="acxSort" type="button"></button>
    <span class="grow"></span>
    <div id="acxState"></div>
    <div id="acxView"></div>
  </div>
  <div class="acx-top">
    <button class="text primary acx-add" id="acxAdd" type="button" data-t>Add account</button>
    <button class="text acx-read-all" id="acxReadAll" type="button" data-t>Refresh all usage</button>
    <button class="text acx-reload" id="acxReload" type="button" data-t>Refresh list</button>
  </div>
  <div class="acx-adding" id="acxAdding" hidden></div>
  <div class="acx-held" id="acxHeld" hidden></div>
  <p class="acx-sum" id="acxSum" role="status"></p>
  <div class="list acx-list" id="acxList"></div>
  <p class="usage-note acx-note" id="acxNote"></p>
`,
    mount() { acxMount(); },
    open() { return acxLoad(false); },
    refresh(again) { return acxLoad(again); },
    reload() { return acxLoad(true); },
  });
  // Models › Proxy IPs (T-155): the proxy exit IPs paid by the month, each
  // with its start date and the period pill Accounts' rows have (renew.js),
  // and the accounts going out through it (T-170), found by a word, its
  // period or its accounts (T-176), and proxies are pasted in a batch
  // (ipsbatch.js); ips.js, after app.js and page.js, is its code
  ctx.slots.add("page", {
    key: "ips",
    title: "Proxy IPs",
    html: `
  <div class="sess-tools ips-top"><input class="sess-filter ips-q" id="ipsQ" type="search" spellcheck="false" autocomplete="off" placeholder="Search IPs" aria-label="Search IPs" data-tp hidden><button class="sess-pick" id="ipsRenew" type="button" hidden></button><button class="sess-pick" id="ipsFilter" type="button" hidden></button></div>
  <p class="acx-sum" id="ipsSum" role="status"></p>
  <div class="list ips-list" id="ipsList" hidden></div>
  <p class="acx-err ips-err" id="ipsErr" role="alert" hidden></p>
  <p class="ips-said" id="ipsSaid" role="status" hidden></p>
  <div class="ips-tools"><button class="text ips-add" id="ipsAdd" type="button" hidden data-t>Add IP</button><button class="text ips-import" id="ipsImport" type="button" hidden data-t>Import from accounts</button><button class="text ips-batch" id="ipsBatch" type="button" hidden data-t>Batch add</button></div>
  <div class="ips-batch-box" id="ipsBatchBox" hidden></div>
  <p class="usage-note acx-note" id="ipsNote"></p>
`,
    mount() { ipsMount(); },
    open() { return ipsLoad(); },
    refresh() { return ipsLoad(); },
    reload() { return ipsLoad(); },
  });
});
