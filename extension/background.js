// background.js — polls attendd for the latest rule + status snapshot and
// stores it in chrome.storage.local. The content script reads from there at
// page load time. Polling cadence is intentionally cheap; immediate enforcement
// happens on document_start, so a slow poll is fine.
//
// Injections (persistent userscript-style page modifications) are reconciled
// against chrome.userScripts after every successful poll:
//   - JS payloads → chrome.userScripts.register (inline code, native match
//     patterns and run-at). Requires the user to enable Developer Mode in
//     chrome://extensions one-time.
//   - CSS payloads → chrome.scripting.insertCSS, dispatched from
//     webNavigation.onCommitted (CSS can't be statically registered with
//     inline content).

importScripts("match.js");

const DAEMON_URL = "http://127.0.0.1:7723";
const USER_SCRIPT_PREFIX = "attend_inj_";

async function poll() {
  try {
    const resp = await fetch(`${DAEMON_URL}/v1/status`, { cache: "no-store" });
    if (!resp.ok) return;
    const data = await resp.json();
    await chrome.storage.local.set({
      rules: data.rules || [],
      activeNow: data.active_now || [],
      paused: !!data.paused,
      injections: data.injections || [],
      polledAt: Date.now(),
    });
    await reconcileUserScripts(data.injections || []);
  } catch (e) {
    // Daemon may be down or unreachable. Leave the cache as-is so blocks
    // from the last successful poll still apply (fail-closed for the user's
    // benefit).
  }
}

// reconcileUserScripts unregisters every attend-owned userScript and
// re-registers the current set. Cheap (Chrome handles it in-process) and
// avoids a stateful diff.
async function reconcileUserScripts(injections) {
  if (!chrome.userScripts) return; // browser without userScripts API
  const jsInjections = injections.filter(function (i) { return i.js && i.js.trim() !== ""; });

  let existing = [];
  try {
    existing = await chrome.userScripts.getScripts();
  } catch (e) {
    // getScripts throws when Developer Mode is off — same condition that
    // would block register(). Surface once per session and bail.
    logDevModeIfNeeded(e);
    return;
  }

  const stale = existing
    .filter(function (s) { return s.id.startsWith(USER_SCRIPT_PREFIX); })
    .map(function (s) { return s.id; });
  if (stale.length) {
    try {
      await chrome.userScripts.unregister({ ids: stale });
    } catch (e) {
      console.warn("attend: unregister userScripts failed", e);
    }
  }

  if (jsInjections.length === 0) return;

  const scripts = jsInjections.map(function (i) {
    const world = i.world === "ISOLATED" ? "USER_SCRIPT" : "MAIN";
    return {
      id: USER_SCRIPT_PREFIX + i.id,
      matches: i.match,
      excludeMatches: i.exclude || [],
      js: [{ code: i.js }],
      runAt: i.run_at || "document_idle",
      world: world,
      allFrames: !!i.all_frames,
    };
  });

  try {
    await chrome.userScripts.register(scripts);
  } catch (e) {
    logDevModeIfNeeded(e);
    console.warn("attend: register userScripts failed", e);
  }
}

let devModeWarned = false;
function logDevModeIfNeeded(err) {
  if (devModeWarned) return;
  const msg = String(err && err.message || err || "");
  if (msg.toLowerCase().includes("developer mode")) {
    devModeWarned = true;
    console.warn(
      "attend: chrome.userScripts requires Developer Mode. " +
      "Enable it at chrome://extensions (toggle, top-right) to use JS injections."
    );
  }
}

// CSS dispatcher: insertCSS doesn't have a static-registration form for
// inline strings, so we apply it on every navigation commit. Fast enough
// that most users won't see a flash.
chrome.webNavigation.onCommitted.addListener(async function (details) {
  if (details.frameId !== 0) return; // top frame for now; allFrames TODO
  let store;
  try {
    store = await chrome.storage.local.get("injections");
  } catch (_) { return; }
  const injections = (store.injections || []).filter(function (i) {
    return i.css && i.css.trim() !== "";
  });
  if (injections.length === 0) return;

  for (const inj of injections) {
    if (!attendMatch.injectionMatches(inj, details.url)) continue;
    try {
      await chrome.scripting.insertCSS({
        target: { tabId: details.tabId, allFrames: !!inj.all_frames },
        css: inj.css,
      });
    } catch (e) {
      // Privileged URLs (chrome://, about:) reject. Silently skip.
    }
  }
});

chrome.runtime.onInstalled.addListener(poll);
chrome.runtime.onStartup.addListener(poll);

// chrome.alarms wakes the service worker reliably, unlike setInterval.
chrome.alarms.create("attend-poll", { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === "attend-poll") poll();
});

// Allow content scripts to request a fresh poll (e.g. after passing a
// challenge so the cooldown is current).
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg && msg.type === "attend-poll-now") {
    poll().then(() => sendResponse({ ok: true }));
    return true; // async response
  }
});

// --- page-job consumer ------------------------------------------------------
//
// The CLI submits ephemeral RPC jobs to attendd; the daemon brokers them to
// the extension via long-poll on /v1/page/jobs/next. We keep one outstanding
// long-poll open at all times, dispatch by job.kind, and post the result.
//
// Service workers can be terminated when idle, but an outstanding fetch keeps
// the SW alive, so this loop self-sustains. On SW restart, onStartup /
// onInstalled / the alarm wake us up and we re-enter the loop.

let pageJobLoopRunning = false;
async function ensurePageJobLoop() {
  if (pageJobLoopRunning) return;
  pageJobLoopRunning = true;
  try {
    while (true) {
      let job;
      try {
        const resp = await fetch(`${DAEMON_URL}/v1/page/jobs/next?wait=25s`);
        if (resp.status === 204) continue;
        if (!resp.ok) {
          await sleep(2000);
          continue;
        }
        job = await resp.json();
      } catch (e) {
        await sleep(2000);
        continue;
      }
      const result = await runPageJob(job).catch((e) => ({
        ok: false,
        error: String(e && e.message || e),
      }));
      try {
        await fetch(`${DAEMON_URL}/v1/page/jobs/${job.id}/result`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(result),
        });
      } catch (e) {
        // Daemon went away mid-flight; nothing to do — caller will time out.
      }
    }
  } finally {
    pageJobLoopRunning = false;
  }
}

function sleep(ms) { return new Promise((r) => setTimeout(r, ms)); }

async function runPageJob(job) {
  switch (job.kind) {
    case "tabs.list":
      return await jobTabsList();
    case "page.dump":
      return await jobPageDump(job.payload || {});
    case "page.exec":
      return await jobPageExec(job.payload || {});
    default:
      return { ok: false, error: "unknown job kind: " + job.kind };
  }
}

async function jobTabsList() {
  const tabs = await chrome.tabs.query({});
  const out = tabs.map(function (t) {
    return {
      tab_id: t.id,
      url: t.url || "",
      title: t.title || "",
      active: !!t.active,
      window_id: t.windowId,
    };
  });
  return { ok: true, value: out };
}

async function resolveTab(selector) {
  selector = selector || {};
  if (selector.tab_id) {
    return await chrome.tabs.get(selector.tab_id);
  }
  if (selector.url_pattern) {
    const matches = await chrome.tabs.query({ url: selector.url_pattern });
    if (!matches.length) throw new Error("no tab matches url_pattern: " + selector.url_pattern);
    return matches[0];
  }
  // Default: active tab in the focused window.
  const active = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (!active.length) {
    // Fall back to active in any window.
    const any = await chrome.tabs.query({ active: true });
    if (!any.length) throw new Error("no active tab found");
    return any[0];
  }
  return active[0];
}

async function jobPageDump(payload) {
  const tab = await resolveTab(payload.tab);
  const results = await chrome.scripting.executeScript({
    target: { tabId: tab.id },
    world: "MAIN",
    func: function () { return document.documentElement.outerHTML; },
  });
  const html = (results[0] && typeof results[0].result === "string") ? results[0].result : "";
  return {
    ok: true,
    value: {
      tab_id: tab.id,
      url: tab.url || "",
      title: tab.title || "",
      html: html,
    },
  };
}

async function jobPageExec(payload) {
  const tab = await resolveTab(payload.tab);
  const code = String(payload.code || "");
  if (!code) return { ok: false, error: "page.exec requires non-empty code" };
  const world = payload.world === "ISOLATED" ? "ISOLATED" : "MAIN";

  // We wrap the user-provided code in a function body so a bare expression
  // like 'document.title' becomes the return value. Falls back to eval for
  // statements that aren't valid expressions.
  const wrapper = function (src) {
    try {
      return (0, eval)("(function(){return (" + src + ")})()");
    } catch (_) {
      return (0, eval)("(function(){" + src + "})()");
    }
  };

  const results = await chrome.scripting.executeScript({
    target: { tabId: tab.id },
    world: world,
    func: wrapper,
    args: [code],
  });
  const first = results[0] || {};
  if (first.error) {
    return { ok: false, error: String(first.error.message || first.error) };
  }
  return {
    ok: true,
    value: {
      tab_id: tab.id,
      url: tab.url || "",
      value: first.result === undefined ? null : first.result,
    },
  };
}

// Wake the job loop on every plausible service-worker resurrection.
chrome.runtime.onInstalled.addListener(ensurePageJobLoop);
chrome.runtime.onStartup.addListener(ensurePageJobLoop);
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === "attend-poll") ensurePageJobLoop();
});
// And once on script load — the import is itself a wake.
ensurePageJobLoop();
