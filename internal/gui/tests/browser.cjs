// Starting a test's browser, with a bound on each step. On a busy machine
// Chromium can stop answering once it has started. Playwright bounds launch()
// itself (3 minutes), but newContext() and newPage() then wait forever, and
// node --test has no timeout of its own, so one stuck browser kept a whole
// file, and the run it was in, from ever ending. Here a new context and a new
// page each fail after SETUP_MS with the step that didn't answer. The test's
// own browser.close() ends a browser that doesn't answer (Playwright kills it
// after 30s), and the file goes on to its next test.
const { chromium, webkit } = require("playwright");

// a context and a page take well under a second; launch keeps Playwright's
// own 3 minutes, since a cold start on a loaded machine can take a while
const SETUP_MS = 60e3;
// a whole test, its setup included: about 6x the slowest (~45s under load),
// room for a launch at its 3 minutes and a context at its bound
const TEST_MS = 5 * 60e3;

// A step that answers after its bound is left to the browser: a context made
// late is the browser's, and the test's browser.close() closes it with it.
function within(what, step, ms = SETUP_MS) {
  let timer;
  const late = new Promise((_, fail) => {
    timer = setTimeout(() => fail(new Error(`${what} didn't answer in ${ms / 1e3}s; the browser may be stuck on a busy machine`)), ms);
  });
  return Promise.race([step, late]).finally(() => clearTimeout(timer));
}

function launch(engine) {
  return engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });
}

async function newPage(browser, engine, options) {
  const context = await within(`${engine}'s new context`, browser.newContext(options));
  return within(`${engine}'s new page`, context.newPage());
}

module.exports = { SETUP_MS, TEST_MS, within, launch, newPage };
