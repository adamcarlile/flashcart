const $ = (id) => document.getElementById(id);

// The pass sequence is the mental model, so it is rendered before any run
// rather than appearing for the first time during one.
const PASSES = [
  { id: "bios-pull",          n: 1, name: "BIOS",          dir: "in"  },
  { id: "roms-content-pull",  n: 2, name: "ROM content",   dir: "in"  },
  { id: "roms-metadata-pull", n: 3, name: "Metadata seed", dir: "in"  },
  { id: "roms-metadata-push", n: 4, name: "Metadata",      dir: "out" },
  { id: "saves-push",         n: 5, name: "Saves",         dir: "out" },
];

// BIOS pulls only, Saves pushes only, ROMs does both. The asymmetry is what
// teaches the split.
const TREES = [
  { key: "roms",  name: "ROMs",  tag: "split" },
  { key: "bios",  name: "BIOS",  tag: "pull"  },
  { key: "saves", name: "Saves", tag: "push"  },
];

const state = { status: null, plan: null, progress: {}, err: {}, busy: false };

function humanBytes(n) {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function log(kind, text) {
  const el = $("log");
  const div = document.createElement("div");
  div.className = kind;
  div.textContent = text;
  el.appendChild(div);
  el.scrollTop = el.scrollHeight;
}

/* ── rendering ─────────────────────────────────────────────── */

function renderTrees() {
  const byTree = {};
  for (const t of (state.plan?.trees || [])) byTree[t.tree] = t;

  $("trees").innerHTML = TREES.map(({ key, name, tag }) => {
    const t = byTree[key];
    let flows;

    if (!t) {
      flows = `<div class="flow nil">
        <span class="flow-arrow in">&middot;</span>
        <span class="flow-label"><b>not planned</b></span>
        <span class="flow-val">&mdash;</span></div>`;
    } else {
      flows = (t.passes || []).map((p) => {
        const nil = !p.files;
        return `<div class="flow ${nil ? "nil" : ""}">
          <span class="flow-arrow ${p.direction}">${p.direction === "in" ? "&#8595;" : "&#8593;"}</span>
          <span class="flow-label"><b>${esc(p.label)}</b></span>
          <span class="flow-val">${nil ? "&mdash;" : p.files.toLocaleString() + " files"}
            ${nil ? "" : `<span>${humanBytes(p.bytes)}</span>`}</span>
        </div>`;
      }).join("");
    }

    const n = t ? (t.drift || []).length : 0;
    const drift = n
      ? `<span class="drifted">${n} drifted</span>`
      : `<span>${t ? "no drift" : "&mdash;"}</span>`;

    return `<div class="tree">
      <div class="tree-head"><span class="tree-name">${name}</span><span class="tree-tag">${tag}</span></div>
      <div class="flows">${flows}</div>
      <div class="tree-foot"><span>/userdata/${key}</span>${drift}</div>
    </div>`;
  }).join("");
}

function renderPasses() {
  $("passes").innerHTML = PASSES.map((p) => {
    const v = state.progress[p.id];
    let cls = "idle", pct = 0, label = "&mdash;";

    if (v === "done")        { cls = "done";   pct = 100; label = "done"; }
    else if (v === "failed") { cls = "failed"; pct = 100; label = "failed"; }
    else if (typeof v === "number") { cls = "running"; pct = v; label = v + "%"; }
    else if (state.busy)     { label = "waiting"; }

    const err = state.err[p.id] ? `<div class="pass-err">${esc(state.err[p.id])}</div>` : "";

    return `<div class="pass ${cls}">
      <span class="pass-n">${p.n}</span>
      <span class="pass-name"><span class="dir ${p.dir}">${p.dir === "in" ? "&#8595;" : "&#8593;"}</span>${p.name}</span>
      <span class="track"><span class="fill" style="width:${pct}%"></span></span>
      <span class="pass-state">${label}</span>
      ${err}
    </div>`;
  }).join("");
}

function renderDrift() {
  const items = (state.plan?.trees || []).flatMap((t) => t.drift || []);
  $("drift-section").hidden = items.length === 0;
  if (!items.length) return;

  $("drift-title").textContent = `Drift — ${items.length} path${items.length === 1 ? "" : "s"}`;
  $("drift-rows").innerHTML = items.map((d, i) => `
    <label class="drift-row">
      <input type="checkbox" data-i="${i}">
      <span class="side ${esc(d.side)}">${esc(d.side).toUpperCase()}</span>
      <span class="drift-path">${esc(d.tree)}/${esc(d.rel)}</span>
    </label>`).join("");

  const boxes = () => [...$("drift-rows").querySelectorAll("input")];
  const update = () => {
    const n = boxes().filter((b) => b.checked).length;
    $("drift-btn").disabled = n === 0;
    $("drift-note").textContent = n === 0
      ? "Nothing ticked"
      : `${n} path${n === 1 ? "" : "s"} will be permanently deleted`;
  };
  boxes().forEach((b) => b.addEventListener("change", update));
  update();

  $("drift-btn").onclick = async () => {
    const chosen = boxes().filter((b) => b.checked).map((b) => items[+b.dataset.i]);
    if (!chosen.length) return;
    if (!confirm(`Permanently delete ${chosen.length} path(s)? This cannot be undone.`)) return;

    const res = await fetch("/api/drift/confirm", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ items: chosen }),
    });
    const body = await res.json();
    if (!res.ok) { log("e", "delete refused: " + body.err); return; }
    log("g", `deleted ${body.deleted.length} path(s)`);
    doPlan();
  };
}

function setBanner(kind, title, text) {
  const b = $("banner");
  if (!kind) { b.hidden = true; return; }
  b.hidden = false;
  b.className = "banner " + kind;
  b.innerHTML = `<strong>${esc(title)}</strong><span>${esc(text)}</span>`;
}

function renderControls() {
  const s = state.status;
  const reachable = s?.reachable;
  const blocked = !reachable || state.busy || (state.plan && !state.plan.sufficient);

  $("plan-btn").disabled = !reachable || state.busy;
  $("sync-btn").disabled = blocked;

  if (state.busy) {
    $("act-note").textContent = "Sync in progress — plan and sync are locked";
  } else if (!reachable) {
    $("act-note").textContent = "";
  } else if (!state.plan) {
    $("act-note").textContent = "Plan first to see what would move.";
  } else if (state.plan.sufficient) {
    $("act-note").textContent =
      `${humanBytes(state.plan.requiredBytes)} incoming · ${humanBytes(state.plan.freeBytes)} free`;
  } else {
    $("act-note").textContent = "";
  }
}

/* ── data ──────────────────────────────────────────────────── */

async function refreshStatus() {
  let s;
  try {
    s = await (await fetch("/api/status")).json();
  } catch {
    return;
  }
  state.status = s;
  state.busy = s.busy;

  const pill = $("nas-pill");
  pill.className = "pill " + (s.reachable ? "ok" : "bad");
  pill.textContent = s.reachable ? "NAS reachable" : "NAS unreachable";

  $("tagline").textContent = s.nasHost ? "NAS · " + s.nasHost : "";
  $("version").textContent = s.version ? "v" + s.version : "";
  $("last-sync").textContent = s.lastSyncAt
    ? "Last sync " + new Date(s.lastSyncAt).toLocaleString()
    : "Never synced";

  // Offline is the normal working state in a car, not a failure.
  if (!s.reachable) {
    setBanner("bad", "NAS unreachable",
      "Nothing to do until you are home. The library on this box is complete and playable.");
  } else if (state.plan && !state.plan.sufficient) {
    setBanner("bad", "Not enough space", state.plan.message);
  } else {
    setBanner(null);
  }

  if (s.fake) {
    $("fake-bar").hidden = false;
    const sel = $("scenario");
    if (!sel.options.length) {
      for (const name of s.scenarios || []) {
        const opt = document.createElement("option");
        opt.value = opt.textContent = name;
        sel.appendChild(opt);
      }
      sel.addEventListener("change", async () => {
        await fetch("/api/fake/scenario", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ scenario: sel.value }),
        });
        state.plan = null;
        state.progress = {};
        state.err = {};
        log("t", "scenario switched to " + sel.value);
        renderTrees(); renderPasses(); renderDrift();
        refreshStatus();
      });
    }
    sel.value = s.scenario;
  }

  renderControls();
}

async function doPlan() {
  state.busy = true;
  renderControls();
  log("t", "planning…");
  try {
    const res = await fetch("/api/plan", { method: "POST" });
    const body = await res.json();
    if (!res.ok) { log("e", "plan failed: " + body.err); return; }
    state.plan = body;
    renderTrees();
    renderDrift();
    const drifted = (body.trees || []).reduce((n, t) => n + (t.drift || []).length, 0);
    log(body.sufficient ? "g" : "e",
      `plan complete — ${humanBytes(body.requiredBytes)} in, ${drifted} drifted path(s)` +
      (body.sufficient ? "" : " — refused, insufficient space"));
  } finally {
    state.busy = false;
    await refreshStatus();
  }
}

async function doSync() {
  state.progress = {};
  state.err = {};
  state.busy = true;
  renderPasses();
  renderControls();
  log("t", "sync started");

  const res = await fetch("/api/sync", { method: "POST" });
  if (!res.ok) {
    const body = await res.json();
    log("e", "sync refused: " + body.err);
    state.busy = false;
    renderControls();
  }
}

function connectEvents() {
  const es = new EventSource("/api/events");
  es.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.type === "progress") {
      state.progress[m.passId] = m.percent;
      renderPasses();
    } else if (m.type === "pass") {
      state.progress[m.passId] = m.ok ? "done" : "failed";
      if (!m.ok) state.err[m.passId] = m.err;
      log(m.ok ? "g" : "e", `${m.label || m.passId}: ${m.ok ? "ok" : "FAILED"}`);
      renderPasses();
    } else if (m.type === "done") {
      state.busy = false;
      if (!m.ok) {
        setBanner("bad", "Sync failed",
          "Remaining passes were abandoned. Nothing was deleted. The NAS was unmounted.");
      }
      log(m.ok ? "g" : "e", m.ok ? "sync complete" : "sync failed: " + m.err);
      refreshStatus();
    }
  };
  es.onerror = () => log("t", "event stream interrupted, retrying");
}

$("plan-btn").addEventListener("click", doPlan);
$("sync-btn").addEventListener("click", doSync);

renderTrees();
renderPasses();
connectEvents();
refreshStatus();
setInterval(refreshStatus, 15000);
