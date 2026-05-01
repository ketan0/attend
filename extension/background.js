// background.js — polls attendd for the latest rule + status snapshot and
// stores it in chrome.storage.local. The content script reads from there at
// page load time. Polling cadence is intentionally cheap; immediate enforcement
// happens on document_start, so a slow poll is fine.

const DAEMON_URL = "http://127.0.0.1:7723";

async function poll() {
  try {
    const resp = await fetch(`${DAEMON_URL}/v1/status`, { cache: "no-store" });
    if (!resp.ok) return;
    const data = await resp.json();
    await chrome.storage.local.set({
      rules: data.rules || [],
      activeNow: data.active_now || [],
      paused: !!data.paused,
      polledAt: Date.now(),
    });
  } catch (e) {
    // Daemon may be down or unreachable. Leave the cache as-is so blocks
    // from the last successful poll still apply (fail-closed for the user's
    // benefit).
  }
}

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
