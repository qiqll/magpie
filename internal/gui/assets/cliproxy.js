// An account's sign-in as CLIProxyAPI's auth file. app.js imports this only
// when an account's Export is pressed (cliproxyButton), so it weighs
// nothing on the core.

// cliproxyExport: account l of a, asked first: the file holds the refresh
// token, and a ChatGPT or Claude one refreshed on one side stops working on
// the other. To the browser in magpie web, to Downloads in the app; b is
// busy meanwhile.
window.cliproxyExport = async function (a, l, b) {
  const shared = a.agent === "antigravity" ? "" : " " + t("magpie renews this sign-in by itself, about once a day, and the copy in the file then stops working: to use the account in CLIProxyAPI, remove it from magpie after exporting.");
  if (!await confirmAction(t("Export {user} for CLIProxyAPI?", { user: l.user }),
    t("The file holds this account's sign-in, its refresh token too: anyone with it can use the account. Keep it private.") + shared, t("Export"))) return;
  b.classList.add("busy");
  b.disabled = true;
  try {
    const r = await api("login/cliproxy", { agent: a.agent, user: l.user });
    if (web) {
      const link = el("a");
      link.href = URL.createObjectURL(new Blob([r.file], { type: "application/json" }));
      link.download = r.name;
      link.click();
      setTimeout(() => URL.revokeObjectURL(link.href), 1000);
    } else status(t("Saved to {path}", { path: r.path }), "ok");
  } catch (e) {
    status(t(e.message), "err");
  } finally {
    b.classList.remove("busy");
    b.disabled = false;
  }
};
