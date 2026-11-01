// A page as Safari 15.0 (macOS 12's window, #220) reads it, for a test to
// load beside the real one and compare. Its stylesheets: no container
// queries (container and container-type are properties it doesn't know,
// @container a rule it skips, and @supports not (container-type:
// inline-size) holds) and no :has(), whose rule it drops whole. Not
// covered: a plain @supports (container-type: …), which still holds here,
// and a :has() inside :is() or :where(), where only that one selector of
// the list is dropped rather than the rule.
const asSafari15 = (css) => css.replace(/\/\*[\s\S]*?\*\//g, "")
  .replace(/@supports not \(container-type: inline-size\)/g, "@supports (display: block)")
  .replace(/@container[^{]*\{/g, "@media not all {")
  .replace(/(^|[{;\s])container(-type)?\s*:/g, "$1x-container$2:")
  .replace(/:has\(/g, ":x-has(");

// and CSS.supports() as it answers about them, for page.addInitScript
const asSafari15Supports = () => {
  const supports = CSS.supports;
  CSS.supports = (...a) => !/container|:has\(/.test(a.join(" ")) && supports.apply(CSS, a);
};

module.exports = { asSafari15, asSafari15Supports };
