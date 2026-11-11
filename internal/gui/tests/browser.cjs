// Starting a test's browser, with a bound on each step. On a busy machine
// Chromium can stop answering once it has started. Playwright bounds launch()
// itself (3 minutes), but newContext() and newPage() then wait forever, and
// node --test has no timeout of its own, so one stuck browser kept a whole
// file, and the run it was in, from ever ending. Here each step fails after
// SETUP_MS with the step that didn't answer. The test's own browser.close()
// ends a browser that doesn't answer (Playwright kills it after 30s), and the
// file goes on to its next test.
const { chromium, webkit } = require("playwright");

// a launch takes about a second, a context and a page well under one
const SETUP_MS = 60e3;
// a whole test, its setup included; the slowest take about 45s under load
const TEST_MS = 5 * 60e3;

function within(what, step, ms = SETUP_MS) {
  let timer;
  const late = new Promise((_, fail) => {
    timer = setTimeout(() => fail(new Error(`${what} didn't answer in ${ms / 1e3}s; the browser may be stuck on a busy machine`)), ms);
  });
  return Promise.race([step, late]).finally(() => clearTimeout(timer));
}

function launch(engine) {
  return engine === "webkit" ? webkit.launch({ timeout: SETUP_MS }) : chromium.launch({ channel: "chromium", timeout: SETUP_MS });
}

async function newPage(browser, engine, options) {
  const context = await within(`${engine}'s new context`, browser.newContext(options));
  return within(`${engine}'s new page`, context.newPage());
}

module.exports = { SETUP_MS, TEST_MS, within, launch, newPage };
