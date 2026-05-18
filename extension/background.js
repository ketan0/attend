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
