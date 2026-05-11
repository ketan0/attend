// content.js — runs at document_start on every page. Looks up rules from
// chrome.storage.local (populated by background.js) and applies the strictest
// matching rule by replacing the page with a block / friction overlay.
//
// Friction state (cooldowns) is stored locally per-rule under
// `cooldowns[<rule.id>]` in chrome.storage.local.

// targetMatches and parseDurationMs are defined in match.js, which loads
// before this script per manifest.json's content_scripts ordering.
const { targetMatches, parseDurationMs } = self.attendMatch;

(function attendContentScript() {
  if (window.__attendInjected) return;
  window.__attendInjected = true;

  let lastUrl = window.location.href;
  // Once we mount a blocking overlay we wipe the DOM, so further re-evaluation
  // is unnecessary (and would re-render the overlay on storage changes).
  let mounted = false;

  async function evaluate() {
    if (mounted) return;
    const url = new URL(window.location.href);
    if (url.protocol !== "http:" && url.protocol !== "https:") return;

    const data = await chrome.storage.local.get([
      "rules", "activeNow", "paused", "cooldowns",
    ]);
    if (data.paused) return;

    const active = new Set(data.activeNow || []);
    const cooldowns = data.cooldowns || {};
    const now = Date.now();

    // Allow rules win over everything. If any allow rule matches, no
    // overlay is shown — the page loads normally. This is what makes the
    // "block reddit.com, allow reddit.com/r/LocalLLaMA" pattern work.
    const matching = (data.rules || []).filter((r) =>
      active.has(r.id) &&
      targetMatches(r.target, url) &&
      !(cooldowns[r.id] && cooldowns[r.id] > now)
    );
    if (matching.length === 0) return;
    if (matching.some((r) => r.action === "allow")) return;

    const priority = { block: 3, friction: 2, nudge: 1 };
    const chosen = matching.reduce((acc, r) =>
      !acc || priority[r.action] > priority[acc.action] ? r : acc, null);

    if (chosen.action === "block") {
      showBlock(chosen);
      mounted = true;
    } else if (chosen.action === "friction") {
      showFriction(chosen);
      mounted = true;
    } else if (chosen.action === "nudge") {
      showNudge(chosen);
    }
  }

  evaluate();

  // SPA navigations (history.pushState/replaceState) don't trigger a new
  // content script run, so document_start evaluation misses them. LinkedIn,
  // for example, loads at "/" and then client-side-replaces the URL with
  // "/feed" once it knows the user is logged in. Poll location.href so we
  // catch the change and re-evaluate against the new URL.
  setInterval(() => {
    if (mounted) return;
    if (window.location.href !== lastUrl) {
      lastUrl = window.location.href;
      evaluate();
    }
  }, 250);

  // Re-evaluate if rules change while the page is open (e.g. user runs
  // `attend block ...` in a terminal with a matching tab already loaded).
  chrome.storage.onChanged.addListener((changes, area) => {
    if (area !== "local") return;
    if (changes.rules || changes.activeNow || changes.paused || changes.cooldowns) {
      evaluate();
    }
  });
})();

function clearPage() {
  try { window.stop(); } catch (e) { /* not all browsers */ }
  document.documentElement.innerHTML = "<head></head><body></body>";
}

function showBlock(rule) {
  clearPage();
  const root = mountOverlay();
  root.innerHTML = `
    <div class="attend-card">
      <div class="attend-label">attend — blocked</div>
      <h1>${escapeHtml(rule.target.value)}</h1>
      <p>This page is blocked by an attend rule.</p>
      <p class="attend-tip">to unblock, run <code>attend rm ${escapeHtml(rule.id)}</code></p>
    </div>
  `;
}

function showNudge(rule) {
  // Non-blocking banner that fades away.
  const root = mountOverlay({ blocking: false });
  root.classList.add("attend-nudge");
  root.innerHTML = `
    <div class="attend-toast">
      <div class="attend-label">attend nudge</div>
      <p>${escapeHtml(rule.message || "noticed you opened " + rule.target.value)}</p>
    </div>
  `;
  setTimeout(() => root.remove(), 6000);
}

function showFriction(rule) {
  clearPage();
  const root = mountOverlay();
  const level = rule.friction?.level || "intent";
  const cooldownMs = parseDurationMs(rule.friction?.cooldown) || 5 * 60 * 1000;

  const onPass = () => {
    const cd = (window._attendCooldowns ||= {});
    cd[rule.id] = Date.now() + cooldownMs;
    chrome.storage.local.get("cooldowns").then((d) => {
      const merged = Object.assign({}, d.cooldowns || {}, cd);
      chrome.storage.local.set({ cooldowns: merged }).then(() => {
        location.reload();
      });
    });
  };

  if (level === "timer") {
    renderTimer(root, rule, onPass);
  } else if (level === "intent") {
    renderIntent(root, rule, onPass);
  } else if (level === "phrase") {
    renderPhrase(root, rule, onPass);
  } else if (level === "math") {
    renderMath(root, rule, onPass);
  } else {
    renderTimer(root, rule, onPass); // fallback
  }
}

function mountOverlay({ blocking = true } = {}) {
  const root = document.createElement("div");
  root.id = "attend-overlay";
  if (blocking) root.classList.add("attend-blocking");
  document.documentElement.appendChild(root);
  return root;
}

function renderTimer(root, rule, onPass) {
  const total = (rule.friction?.timer_seconds || 10);
  let remaining = total;
  root.innerHTML = `
    <div class="attend-card">
      <div class="attend-label">attend</div>
      <h1>${escapeHtml(rule.target.value)}</h1>
      <div class="attend-timer">${remaining}</div>
      <p class="attend-tip">wait, then proceed.</p>
    </div>
  `;
  const el = root.querySelector(".attend-timer");
  const interval = setInterval(() => {
    remaining -= 1;
    if (remaining <= 0) { clearInterval(interval); onPass(); return; }
    el.textContent = String(remaining);
  }, 1000);
}

function renderIntent(root, rule, onPass) {
  root.innerHTML = `
    <div class="attend-card">
      <div class="attend-label">attend</div>
      <h1>${escapeHtml(rule.target.value)}</h1>
      <p>why are you opening this?</p>
      <textarea class="attend-input" rows="4" autofocus></textarea>
      <button class="attend-btn" disabled>proceed</button>
    </div>
  `;
  const ta = root.querySelector("textarea");
  const btn = root.querySelector("button");
  ta.addEventListener("input", () => {
    btn.disabled = ta.value.trim().length < 8;
  });
  btn.addEventListener("click", onPass);
}

function renderPhrase(root, rule, onPass) {
  const phrase = rule.friction?.phrase || "I am opening this on purpose.";
  root.innerHTML = `
    <div class="attend-card">
      <div class="attend-label">attend</div>
      <h1>${escapeHtml(rule.target.value)}</h1>
      <p>type this to proceed:</p>
      <code class="attend-phrase">${escapeHtml(phrase)}</code>
      <input class="attend-input" type="text" autofocus />
      <button class="attend-btn" disabled>proceed</button>
    </div>
  `;
  const inp = root.querySelector("input");
  const btn = root.querySelector("button");
  inp.addEventListener("input", () => { btn.disabled = inp.value !== phrase; });
  btn.addEventListener("click", onPass);
}

function renderMath(root, rule, onPass) {
  const a = 12 + Math.floor(Math.random() * 38);
  const b = 12 + Math.floor(Math.random() * 38);
  root.innerHTML = `
    <div class="attend-card">
      <div class="attend-label">attend</div>
      <h1>${escapeHtml(rule.target.value)}</h1>
      <p>${a} × ${b} = ?</p>
      <input class="attend-input" type="text" autofocus />
      <button class="attend-btn" disabled>proceed</button>
    </div>
  `;
  const inp = root.querySelector("input");
  const btn = root.querySelector("button");
  inp.addEventListener("input", () => {
    btn.disabled = parseInt(inp.value, 10) !== a * b;
  });
  btn.addEventListener("click", onPass);
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
