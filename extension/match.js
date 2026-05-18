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
    const value = normalizeWebTargetValue(target.value || "");
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
      if (tPath.includes("*")) {
        return globPrefixMatches(tPath, path + search);
      }
      return (path + search).startsWith(tPath);
    }
    return false;
  }

  function globPrefixMatches(pattern, value) {
    const re = new RegExp("^" + escapeRegExp(pattern).replace(/\\\*/g, ".*"));
    return re.test(value);
  }

  function escapeRegExp(s) {
    return String(s).replace(/[|\\{}()[\]^$+*?.]/g, "\\$&");
  }

  function normalizeWebTargetValue(value) {
    return String(value).trim().toLowerCase()
      .replace(/^https?:\/\//, "");
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

  // compileMatchPattern returns a function (urlString) -> boolean for a
  // Chrome match pattern. Used by the CSS dispatcher; JS injections go
  // through chrome.userScripts which validates patterns natively.
  //
  // Grammar:
  //   <all_urls>
  //   <scheme>://<host>/<path>
  //     scheme = "*" (= http or https) | http | https | file | ftp
  //     host   = "*" | "*." domain | exact domain | "" (file only)
  //     path   = glob (only "*" is special; matches any chars)
  function compileMatchPattern(pattern) {
    if (pattern === "<all_urls>") {
      return function () { return true; };
    }
    const m = String(pattern).match(/^(\*|http|https|file|ftp):\/\/([^/]*)(\/.*)$/);
    if (!m) return function () { return false; };
    const scheme = m[1], hostPat = m[2], pathPat = m[3];

    const schemeRe = scheme === "*" ? /^https?:$/ : new RegExp("^" + scheme + ":$");

    let hostRe;
    if (scheme === "file") {
      if (hostPat !== "") return function () { return false; };
      hostRe = /^$/;
    } else if (hostPat === "*") {
      hostRe = /.*/;
    } else if (hostPat.startsWith("*.")) {
      const suffix = escapeRegExp(hostPat.slice(2));
      hostRe = new RegExp("^([^.]+(\\.[^.]+)*\\.)?" + suffix + "$");
    } else {
      hostRe = new RegExp("^" + escapeRegExp(hostPat) + "$");
    }

    const pathRe = new RegExp(
      "^" + pathPat.split("*").map(escapeRegExp).join(".*") + "$"
    );

    return function (urlStr) {
      let u;
      try { u = new URL(urlStr); } catch (_) { return false; }
      return schemeRe.test(u.protocol)
        && hostRe.test(u.hostname)
        && pathRe.test(u.pathname + u.search);
    };
  }

  // injectionMatches returns true if any of inj.match matches the URL and
  // none of inj.exclude does.
  function injectionMatches(injection, urlStr) {
    const matchers = (injection.match || []).map(compileMatchPattern);
    if (!matchers.some(function (f) { return f(urlStr); })) return false;
    const excluders = (injection.exclude || []).map(compileMatchPattern);
    if (excluders.some(function (f) { return f(urlStr); })) return false;
    return true;
  }

  return {
    targetMatches: targetMatches,
    parseDurationMs: parseDurationMs,
    pickEffective: pickEffective,
    compileMatchPattern: compileMatchPattern,
    injectionMatches: injectionMatches,
  };
});
