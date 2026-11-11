// Models › Accounts: every subscription account magpie keeps, of every
// provider, on one page (需求方 2026-11-04, after CLIProxyAPI's credentials
// page): each one's state, its allowances' windows, the requests it served
// in the last 5 hours and its proxy, found by provider, state or a word,
// and used, paused, signed in again or read again in place. Adding and
// removing one stay in the provider's editor, which Open goes to.
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
    <span class="grow"></span>
    <div id="acxState"></div>
    <div id="acxView"></div>
  </div>
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
  // with its start date and the period pill Accounts' rows have (renew.js);
  // ips.js, after app.js, is its code
  ctx.slots.add("page", {
    key: "ips",
    title: "Proxy IPs",
    html: `
  <p class="acx-sum" id="ipsSum" role="status"></p>
  <div class="list ips-list" id="ipsList" hidden></div>
  <p class="acx-err ips-err" id="ipsErr" role="alert" hidden></p>
  <div class="ips-tools"><button class="text ips-add" id="ipsAdd" type="button" hidden data-t>Add IP</button></div>
  <p class="usage-note acx-note" id="ipsNote"></p>
`,
    mount() { ipsMount(); },
    open() { return ipsLoad(); },
    refresh() { return ipsLoad(); },
    reload() { return ipsLoad(); },
  });
});
