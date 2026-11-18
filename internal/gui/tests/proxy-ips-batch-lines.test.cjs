// Run with Node's test runner; see README.md.
// T-214 (需求方 2026-11-18, Q5): an area's POST /ips/batch reads its lines
// with magpie's own Go reader (internal/renewal/batch.go), and the two
// share their test lines: internal/renewal/testdata/batch_lines.json. This
// holds the window's ipsBatchParse to the same lines Go's
// TestAreaBatchLinesReadAsTheWindowReads does, so neither reads a format the
// other doesn't. No browser: the reader is run alone, with ips.js'
// ipsCanon beside it.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");

const assets = path.resolve(__dirname, "../assets");
const lines = JSON.parse(fs.readFileSync(path.resolve(__dirname, "../../renewal/testdata/batch_lines.json"), "utf8"));

function reader() {
  const ips = fs.readFileSync(path.join(assets, "plugins/accounts/ips.js"), "utf8");
  const at = ips.indexOf("function ipsCanon(");
  assert.ok(at >= 0, "ips.js has no ipsCanon");
  const canon = ips.slice(at, ips.indexOf("\n}\n", at) + 3);
  // t as the English page has it: the words as written, {args} filled in
  const ctx = { URL, t: (s, a = {}) => Object.entries(a).reduce((x, [k, v]) => x.replaceAll(`{${k}}`, v), s) };
  vm.runInNewContext(canon + fs.readFileSync(path.join(assets, "plugins/accounts/ipsbatch.js"), "utf8") + "\nthis.parse = ipsBatchParse;", ctx);
  return ctx.parse;
}

test("each shared batch line reads in the window as Go reads it", () => {
  const parse = reader();
  assert.ok(lines.length >= 40, `only ${lines.length} lines`);
  for (const l of lines) {
    const x = parse(l.line, l.type);
    if (l.err) {
      assert.equal(x.err, l.err, JSON.stringify(l.line));
      continue;
    }
    assert.equal(x.err, undefined, JSON.stringify(l.line) + ": " + x.err);
    const got = { type: x.type, host: x.host, v6: x.v6, port: x.port, user: x.user, pass: x.pass, ip: x.ip, url: x.url };
    assert.deepEqual(got, l.want, JSON.stringify(l.line));
  }
});
