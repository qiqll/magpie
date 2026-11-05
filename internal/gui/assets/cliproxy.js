// An account's sign-in as CLIProxyAPI's auth file. app.js imports this when
// the provider editor draws an account's Export (cliproxyButton), so it
// weighs nothing on the core.

// cliproxyDress: Export's button b for account l of a. An icon, not a word:
// "Exportieren" took a line of its own beside Remove and a cap pill
window.cliproxyDress = function (b, a, l) {
  b.setAttribute("aria-label", t("Export"));
  b.append(svg("M8 3v7M5 7.5l3 3 3-3M3.5 12.5h9", 12, 1.5));
  b.title = t("Download this account's sign-in as a CLIProxyAPI auth file");
  b.onclick = () => cliproxyExport(a, l, b);
};

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
