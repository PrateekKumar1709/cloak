(() => {
  const $ = (s) => document.querySelector(s);
  const feed = $("#live-feed");
  const titles = {
    live: ["Live stream", "Every request, what was redacted, and detection overhead. Values stay on this machine unless you reveal them."],
    stats: ["Stats", "Redactions by category and detection latency overhead."],
    entities: ["Entities", "Session-consistent pseudonyms. Real values are local-only; click reveal to show."],
    policy: ["Policy", "Per-category actions: redact, block, allow, or audit."],
    settings: ["Settings", "Upstream, Lemonade detector, and Private Mode configuration."],
  };

  document.querySelectorAll(".nav").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll(".nav").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      const view = btn.dataset.view;
      document.querySelectorAll(".view").forEach((v) => v.classList.add("hidden"));
      $(`#view-${view}`).classList.remove("hidden");
      $("#view-title").textContent = titles[view][0];
      $("#view-sub").textContent = titles[view][1];
      if (view === "stats") loadStats();
      if (view === "entities") loadEntities();
      if (view === "policy") loadPolicy();
      if (view === "settings") loadSettings();
    });
  });

  $("#clear-live").addEventListener("click", () => { feed.innerHTML = ""; });
  $("#refresh-entities").addEventListener("click", loadEntities);
  $("#reveal-entities").addEventListener("change", loadEntities);
  $("#run-test").addEventListener("click", runTest);

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    }[c]));
  }

  function highlight(text, redactions, sanitized) {
    const showSan = $("#show-sanitized").checked;
    let src = showSan && sanitized ? sanitized : text;
    if (!src) return "<em style='color:var(--muted)'>(no text content scanned)</em>";
    let out = esc(src);
    const tokens = (redactions || []).map((r) => showSan ? r.placeholder : r.real_masked || r.placeholder);
    for (const t of tokens) {
      if (!t) continue;
      out = out.split(esc(t)).join(`<mark>${esc(t)}</mark>`);
    }
    return out;
  }

  function renderEvent(ev) {
    const el = document.createElement("article");
    el.className = "card" + (ev.private ? " private" : "") + (ev.blocked ? " blocked" : "");
    const cats = Object.entries(ev.categories || {}).map(([k, v]) =>
      `<span class="chip">${esc(k)} ×${v}</span>`).join("");
    let badge;
    if (ev.private) {
      badge = `<span class="badge local">🔒 answered on-device</span>`;
    } else if (ev.blocked) {
      badge = `<span class="badge block">blocked</span>`;
    } else {
      badge = `<span class="badge">+${ev.detection_ms ?? 0} ms</span>`;
    }
    el.innerHTML = `
      <div class="card-head">
        ${badge}
        <span class="meta">${esc(ev.route || "")}</span>
        <span class="meta">${esc(ev.model || "-")}</span>
        <span class="meta">${new Date(ev.ts).toLocaleTimeString()}</span>
      </div>
      <div class="preview">${highlight(ev.original_preview, ev.redactions, ev.sanitized_preview)}</div>
      <div class="chips">${cats || '<span class="chip">clean</span>'}</div>
    `;
    feed.prepend(el);
    while (feed.children.length > 100) feed.lastChild.remove();
  }

  function connectSSE() {
    const health = $("#health");
    const es = new EventSource("/api/events");
    es.onopen = () => { health.textContent = "live"; health.className = "pill ok"; };
    es.onerror = () => { health.textContent = "reconnecting…"; health.className = "pill"; };
    es.onmessage = (msg) => {
      try { renderEvent(JSON.parse(msg.data)); loadHero(); } catch (_) {}
    };
  }

  async function loadHero() {
    try {
      const st = await (await fetch("/api/stats")).json();
      const redactions = Object.values(st.categories || {}).reduce((a, b) => a + b, 0);
      animate($("#hm-requests"), st.requests ?? 0);
      animate($("#hm-redactions"), redactions);
      animate($("#hm-local"), st.local_answered ?? 0);
      animate($("#trust-local"), st.local_answered ?? 0);
      $("#hm-latency").textContent = `${Math.round(st.p50_detection_ms || 0)} ms`;
    } catch (_) {}
  }

  function animate(el, to) {
    const from = parseInt(el.textContent.replace(/\D/g, ""), 10) || 0;
    if (from === to) { el.textContent = to; return; }
    const step = Math.max(1, Math.round((to - from) / 12));
    let cur = from;
    const t = setInterval(() => {
      cur += step;
      if ((step > 0 && cur >= to) || (step < 0 && cur <= to)) { cur = to; clearInterval(t); }
      el.textContent = cur;
    }, 24);
  }

  async function loadHardware() {
    try {
      const hw = await (await fetch("/api/hardware")).json();
      const status = $("#lc-status");
      if (hw.healthy) {
        status.textContent = "● online";
        status.className = "lc-status ok";
      } else {
        status.textContent = "● start Lemonade";
        status.className = "lc-status warn";
      }
      const tps = hw.tokens_per_sec ? `${hw.tokens_per_sec.toFixed(1)} tok/s` : "-";
      const rows = [
        ["Device", hw.device || "-", "hw-device"],
        ["Backend", hw.backend || "-", ""],
        ["Detector", hw.loaded_model || hw.detector_model || "-", ""],
        ["Local throughput", tps, "hw-tps"],
        ["Private Mode", hw.private_mode ? "on; sensitive prompts stay local" : "off", hw.private_mode ? "on" : "off"],
        ["Omni (voice)", hw.omni_asr ? (hw.whisper_model || "Whisper") : "off", ""],
      ];
      $("#lc-grid").innerHTML = rows.map(([k, v, cls]) =>
        `<div class="lc-item ${cls === "on" ? "on" : cls === "off" ? "off" : ""}">
           <div class="lc-k">${esc(k)}</div>
           <div class="lc-v ${cls === "hw-device" || cls === "hw-tps" ? "accent" : ""}">${esc(v)}</div>
         </div>`).join("");
    } catch (_) {
      $("#lc-status").textContent = "● unavailable";
    }
  }

  async function loadStats() {
    const st = await (await fetch("/api/stats")).json();
    $("#stat-grid").innerHTML = [
      ["Requests", st.requests ?? 0],
      ["Blocked → local", st.local_answered ?? 0],
      ["p50 detect", `${Math.round(st.p50_detection_ms || 0)} ms`],
      ["p95 detect", `${Math.round(st.p95_detection_ms || 0)} ms`],
    ].map(([k, v]) => `<div class="stat"><div class="k">${k}</div><div class="v">${v}</div></div>`).join("");

    const cats = Object.entries(st.categories || {}).sort((a, b) => b[1] - a[1]);
    const max = cats.reduce((m, [, n]) => Math.max(m, n), 1);
    $("#cat-bars").innerHTML = cats.length
      ? cats.map(([k, n]) => `
          <div class="bar-row">
            <div>${esc(k)}</div>
            <div class="bar-track"><div class="bar-fill" style="width:${(n / max) * 100}%"></div></div>
            <div>${n}</div>
          </div>`).join("")
      : `<div class="hint">No redactions yet.</div>`;
  }

  async function loadEntities() {
    const reveal = $("#reveal-entities").checked ? "1" : "0";
    const rows = await (await fetch(`/api/entities?reveal=${reveal}`)).json();
    const tb = $("#entities-table tbody");
    tb.innerHTML = (rows || []).map((r) => `
      <tr>
        <td>${esc(r.placeholder)}</td>
        <td>${esc(r.category)}</td>
        <td>${esc(reveal === "1" ? r.real : r.real_masked)}</td>
        <td title="${esc(r.session_id)}">${esc((r.session_id || "").slice(0, 12))}…</td>
      </tr>`).join("") || `<tr><td colspan="4" style="color:var(--muted)">No entities yet.</td></tr>`;
  }

  async function loadPolicy() {
    const pol = await (await fetch("/api/policy")).json();
    const actions = ["redact", "block", "allow", "audit"];
    $("#policy-grid").innerHTML = Object.entries(pol).sort().map(([cat, act]) => `
      <div class="policy-item">
        <label>${esc(cat)}</label>
        <select data-cat="${esc(cat)}">
          ${actions.map((a) => `<option value="${a}" ${a === act ? "selected" : ""}>${a}</option>`).join("")}
        </select>
      </div>`).join("");
    $("#policy-grid").querySelectorAll("select").forEach((sel) => {
      sel.addEventListener("change", async () => {
        await fetch("/api/policy", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ [sel.dataset.cat]: sel.value }),
        });
      });
    });
  }

  async function loadSettings() {
    const s = await (await fetch("/api/settings")).json();
    $("#settings-panel").innerHTML = `
      <h2>Runtime</h2>
      <div class="bars">
        <div class="bar-row"><div>Listen</div><div class="meta">${esc(s.listen)}</div><div></div></div>
        <div class="bar-row"><div>Cloud upstream</div><div class="meta">${esc(s.upstream)}</div><div></div></div>
        <div class="bar-row"><div>Engine</div><div class="meta">${esc(s.engine || "lemonade")}</div><div></div></div>
        <div class="bar-row"><div>Lemonade</div><div class="meta">${esc(s.lemonade)}</div><div></div></div>
        <div class="bar-row"><div>Detector</div><div class="meta">${esc(s.lemonade_model)} (${s.lemonade_enabled ? "on" : "off"})</div><div></div></div>
        <div class="bar-row"><div>Loaded</div><div class="meta">${esc(s.lemonade_loaded || "-")}</div><div></div></div>
        <div class="bar-row"><div>Private Mode</div><div class="meta">${s.private_mode ? "on → " + esc(s.chat_model) : "off"}</div><div></div></div>
        <div class="bar-row"><div>Omni ASR</div><div class="meta">${s.omni_asr ? esc(s.whisper_model || "Whisper") : "off"}</div><div></div></div>
        <div class="bar-row"><div>OpenAI key</div><div class="meta">${s.has_openai_key ? "set" : "demo / missing"}</div><div></div></div>
        <div class="bar-row"><div>Anthropic key</div><div class="meta">${s.has_anthropic_key ? "set" : "demo / missing"}</div><div></div></div>
      </div>
      <p class="hint" style="margin-top:1rem">Point clients at <code>http://localhost:7777/v1</code> (OpenAI) or <code>…/anthropic</code> (Claude). Voice: <code>POST /v1/audio/transcriptions</code>. With Private Mode on, <code>block</code>-policy prompts on the OpenAI path are answered by the local Lemonade model; the Anthropic path hard-blocks them instead.</p>
    `;
  }

  async function runTest() {
    const text = $("#test-input").value;
    const res = await fetch("/api/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text }),
    });
    const data = await res.json();
    $("#test-out").textContent = JSON.stringify(data, null, 2);
  }

  connectSSE();
  loadHero();
  loadHardware();
  setInterval(loadHero, 5000);
  setInterval(loadHardware, 20000);
  fetch("/healthz").then((r) => {
    if (r.ok) { $("#health").textContent = "live"; $("#health").className = "pill ok"; }
  }).catch(() => { $("#health").textContent = "down"; $("#health").className = "pill bad"; });
})();
