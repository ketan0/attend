// match.js — pure URL-vs-rule-target matching. Loaded as a script before
// content.js (via manifest.json's content_scripts ordering) and also exported
// for the node test suite.

(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory();
  } else {
    root.attendMatch = factory();
  }
})(typeof self !== "undefined" ? self : this, function () {

  // targetMatches: rule.target vs a parsed URL.
  function targetMatches(target, url) {
    const value = (target.value || "").toLowerCase();
    const host = (url.hostname || "").toLowerCase();
    // Path is also lowercased: web hosts (esp. reddit) treat URL paths
    // case-insensitively, so /r/LocalLLaMA and /r/locallama are the same
    // page. The matcher should agree.
    const path = (url.pathname || "/").toLowerCase();
    const search = (url.search || "").toLowerCase();

    if (target.kind === "domain") {
      return host === value || host.endsWith("." + value);
    }
    if (target.kind === "path") {
      const slash = value.indexOf("/");
      const tHost = slash >= 0 ? value.slice(0, slash) : value;
      const tPath = slash >= 0 ? value.slice(slash) : "/";
      if (host !== tHost && !host.endsWith("." + tHost)) return false;
      return (path + search).startsWith(tPath);
    }
    return false;
  }

  // pickEffective applies attend's precedence rule:
  //   allow > block > friction > nudge.
  // Returns the chosen rule or null. Mirrors Go's rules.PickEffective.
  function pickEffective(matching) {
    if (!matching || matching.length === 0) return null;
    for (const r of matching) {
      if (r.action === "allow") return r;
    }
    const priority = { block: 3, friction: 2, nudge: 1 };
    return matching.reduce((acc, r) =>
      !acc || priority[r.action] > priority[acc.action] ? r : acc, null);
  }

  // parseDurationMs accepts Go-style duration strings.
  function parseDurationMs(s) {
    if (!s) return null;
    let total = 0;
    const re = /(\d+)(ms|s|m|h)/g;
    let m;
    while ((m = re.exec(s)) !== null) {
      const n = parseInt(m[1], 10);
      switch (m[2]) {
        case "ms": total += n; break;
        case "s":  total += n * 1000; break;
        case "m":  total += n * 60 * 1000; break;
        case "h":  total += n * 60 * 60 * 1000; break;
      }
    }
    return total || null;
  }

  return { targetMatches, parseDurationMs, pickEffective };
});
