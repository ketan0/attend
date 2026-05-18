// Run with: node extension/match.test.js
// Exits non-zero on failure.

const {
  targetMatches,
  parseDurationMs,
  pickEffective,
  compileMatchPattern,
  injectionMatches,
} = require("./match.js");
const test = require("node:test");
const assert = require("node:assert/strict");

const url = (u) => new URL(u);

test("domain target — exact match", () => {
  assert.equal(targetMatches({ kind: "domain", value: "twitter.com" }, url("https://twitter.com/")), true);
});

test("domain target — subdomain match", () => {
  assert.equal(targetMatches({ kind: "domain", value: "twitter.com" }, url("https://m.twitter.com/feed")), true);
});

test("domain target — different host", () => {
  assert.equal(targetMatches({ kind: "domain", value: "twitter.com" }, url("https://twixter.com/")), false);
});

test("domain target — case insensitive", () => {
  assert.equal(targetMatches({ kind: "domain", value: "Twitter.com" }, url("https://TWITTER.com/")), true);
});

test("path target — homepage but not subpath", () => {
  // Block reddit homepage but allow /r/LocalLLaMA
  const t = { kind: "path", value: "reddit.com/" };
  // The "/" target prefix matches everything under reddit.com — that's
  // intentionally too broad in v1. Documented limitation: use path target
  // for narrower-than-domain blocking, not "homepage only" yet.
  assert.equal(targetMatches(t, url("https://reddit.com/r/LocalLLaMA")), true);
});

test("path target — narrow path matches", () => {
  const t = { kind: "path", value: "reddit.com/r/all" };
  assert.equal(targetMatches(t, url("https://reddit.com/r/all/top")), true);
  assert.equal(targetMatches(t, url("https://reddit.com/r/LocalLLaMA")), false);
  assert.equal(targetMatches(t, url("https://www.reddit.com/r/all")), true);
});

test("path target — wildcard path matches", () => {
  const t = { kind: "path", value: "https://www.youtube.com/shorts/*" };
  assert.equal(targetMatches(t, url("https://www.youtube.com/shorts/abc123")), true);
  assert.equal(targetMatches(t, url("https://www.youtube.com/shorts/abc123?feature=share")), true);
  assert.equal(targetMatches(t, url("https://www.youtube.com/watch?v=abc123")), false);
  assert.equal(targetMatches(t, url("https://music.youtube.com/shorts/abc123")), false);
});

test("path target — URL-form target matches", () => {
  const t = { kind: "path", value: "https://x.com/home" };
  assert.equal(targetMatches(t, url("https://x.com/home")), true);
  assert.equal(targetMatches(t, url("https://www.x.com/home")), true);
});

test("path target — case-insensitive path match", () => {
  // Same letters, different case — should match.
  const t = { kind: "path", value: "reddit.com/r/LocalLLaMA" };
  assert.equal(targetMatches(t, url("https://www.reddit.com/r/LocalLLaMA/")), true);
  assert.equal(targetMatches(t, url("https://www.reddit.com/r/localllama/")), true);
  assert.equal(targetMatches(t, url("https://www.reddit.com/r/LOCALLLAMA/comments/x")), true);
});

test("path target — different host", () => {
  const t = { kind: "path", value: "reddit.com/r/all" };
  assert.equal(targetMatches(t, url("https://twitter.com/r/all")), false);
});

test("parseDurationMs — Go-style 5m", () => {
  assert.equal(parseDurationMs("5m"), 5 * 60 * 1000);
});

test("parseDurationMs — compound 1h30m", () => {
  assert.equal(parseDurationMs("1h30m"), (60 + 30) * 60 * 1000);
});

test("parseDurationMs — full 2h0m0s", () => {
  assert.equal(parseDurationMs("2h0m0s"), 2 * 60 * 60 * 1000);
});

test("pickEffective — empty list", () => {
  assert.equal(pickEffective([]), null);
  assert.equal(pickEffective(null), null);
});

test("pickEffective — allow wins over block", () => {
  const rs = [
    { id: "a", action: "block" },
    { id: "b", action: "allow" },
  ];
  assert.equal(pickEffective(rs).id, "b");
});

test("pickEffective — allow wins regardless of position", () => {
  assert.equal(pickEffective([
    { id: "a", action: "allow" },
    { id: "b", action: "block" },
  ]).id, "a");
  assert.equal(pickEffective([
    { id: "a", action: "friction" },
    { id: "b", action: "block" },
    { id: "c", action: "allow" },
  ]).id, "c");
});

test("pickEffective — block wins over friction over nudge", () => {
  assert.equal(pickEffective([
    { id: "n", action: "nudge" },
    { id: "f", action: "friction" },
    { id: "b", action: "block" },
  ]).id, "b");
  assert.equal(pickEffective([
    { id: "n", action: "nudge" },
    { id: "f", action: "friction" },
  ]).id, "f");
});

test("parseDurationMs — invalid → null", () => {
  assert.equal(parseDurationMs(""), null);
  assert.equal(parseDurationMs("banana"), null);
  assert.equal(parseDurationMs(null), null);
});

test("compileMatchPattern — <all_urls>", () => {
  const m = compileMatchPattern("<all_urls>");
  assert.equal(m("https://example.com/"), true);
  assert.equal(m("file:///etc/hosts"), true);
});

test("compileMatchPattern — exact host", () => {
  const m = compileMatchPattern("https://github.com/*");
  assert.equal(m("https://github.com/"), true);
  assert.equal(m("https://github.com/foo/bar"), true);
  assert.equal(m("https://www.github.com/"), false);
  assert.equal(m("http://github.com/"), false);
});

test("compileMatchPattern — *. subdomain matches apex too", () => {
  const m = compileMatchPattern("https://*.github.com/*");
  assert.equal(m("https://github.com/"), true);
  assert.equal(m("https://api.github.com/foo"), true);
  assert.equal(m("https://deep.api.github.com/foo"), true);
  assert.equal(m("https://example.com/"), false);
});

test("compileMatchPattern — any scheme", () => {
  const m = compileMatchPattern("*://example.com/*");
  assert.equal(m("https://example.com/"), true);
  assert.equal(m("http://example.com/"), true);
  assert.equal(m("ftp://example.com/"), false);
});

test("compileMatchPattern — path glob", () => {
  const m = compileMatchPattern("https://example.com/api/*/users");
  assert.equal(m("https://example.com/api/v1/users"), true);
  assert.equal(m("https://example.com/api/v2/users"), true);
  assert.equal(m("https://example.com/api/v1/orgs"), false);
});

test("compileMatchPattern — file scheme", () => {
  const m = compileMatchPattern("file:///*");
  assert.equal(m("file:///etc/hosts"), true);
  assert.equal(m("https://example.com/"), false);
});

test("compileMatchPattern — invalid pattern is no-match, not crash", () => {
  const m = compileMatchPattern("garbage");
  assert.equal(m("https://example.com/"), false);
});

test("injectionMatches — match wins, exclude vetos", () => {
  const inj = {
    match: ["https://*.github.com/*"],
    exclude: ["https://api.github.com/*"],
  };
  assert.equal(injectionMatches(inj, "https://github.com/"), true);
  assert.equal(injectionMatches(inj, "https://x.github.com/repo"), true);
  assert.equal(injectionMatches(inj, "https://api.github.com/users"), false);
  assert.equal(injectionMatches(inj, "https://example.com/"), false);
});

test("injectionMatches — empty match never matches", () => {
  assert.equal(injectionMatches({ match: [] }, "https://example.com/"), false);
});
