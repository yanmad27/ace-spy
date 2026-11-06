"use strict";

const MAX_ROWS = 5000;
const PAGE = 200;

const $ = (id) => document.getElementById(id);
const feed = $("feed");
const main = document.querySelector("main");
const nav = $("regions");
const olderBtn = $("older");
const statusEl = $("status");
const searchEl = $("search");
const autoEl = $("autoscroll");

const state = {
  region: new URLSearchParams(location.search).get("region") || "",
  regions: new Map(),
  unread: new Map(),
  oldestId: 0,
  lastId: 0,
  streamFrom: 0,
  filter: "",
};

const timeFmt = new Intl.DateTimeFormat("vi-VN", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const dateFmt = new Intl.DateTimeFormat("vi-VN", { dateStyle: "short", timeStyle: "medium" });

function api(path, signal) {
  return fetch(path, { credentials: "same-origin", signal }).then((r) => {
    if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
    return r.json();
  });
}

function qs(params) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== "" && v != null) p.set(k, v);
  const s = p.toString();
  return s ? `?${s}` : "";
}

function highlight(el, text) {
  el.textContent = "";
  const f = state.filter;
  if (!f) { el.textContent = text; return; }
  const lower = text.toLowerCase();
  let i = 0;
  for (let j = lower.indexOf(f, i); j !== -1; j = lower.indexOf(f, i)) {
    el.append(text.slice(i, j));
    const m = document.createElement("mark");
    m.textContent = text.slice(j, j + f.length);
    el.append(m);
    i = j + f.length;
  }
  el.append(text.slice(i));
}

// In-game channel symbols: ! green, @ purple, # $ % orange, ^ red.
const CHANNELS = { "!": "ch-green", "@": "ch-purple", "#": "ch-orange", "$": "ch-orange", "%": "ch-orange", "^": "ch-red" };

function row(m) {
  const li = document.createElement("li");
  const ch = CHANNELS[m.text.charAt(0)];
  if (ch && m.text.charAt(1) === "[") li.classList.add(ch);
  li.dataset.id = m.id;
  li.dataset.text = m.text.toLowerCase();
  const t = document.createElement("time");
  const d = new Date(m.ts);
  t.dateTime = d.toISOString();
  t.textContent = timeFmt.format(d);
  t.title = `${dateFmt.format(d)} · ${m.client || "?"}`;
  const tag = document.createElement("span");
  tag.className = "tag";
  tag.textContent = m.region;
  const text = document.createElement("span");
  text.className = "text";
  text.dataset.raw = m.text;
  highlight(text, m.text);
  li.append(t, tag, text);
  applyFilter(li);
  return li;
}

function applyFilter(li) {
  li.classList.toggle("hidden", !!state.filter && !li.dataset.text.includes(state.filter));
}

function nearBottom() {
  return main.scrollHeight - main.scrollTop - main.clientHeight < 80;
}

function append(msgs) {
  if (!msgs.length) return;
  const stick = autoEl.checked && nearBottom();
  const frag = document.createDocumentFragment();
  for (const m of msgs) frag.append(row(m));
  feed.append(frag);
  while (feed.childElementCount > MAX_ROWS) {
    feed.firstElementChild.remove();
    state.oldestId = Number(feed.firstElementChild.dataset.id);
    olderBtn.hidden = false;
  }
  if (stick) main.scrollTop = main.scrollHeight;
}

function prepend(msgs) {
  const before = main.scrollHeight;
  const frag = document.createDocumentFragment();
  for (const m of msgs) frag.append(row(m));
  feed.prepend(frag);
  main.scrollTop += main.scrollHeight - before;
}

function renderRegions() {
  nav.textContent = "";
  const all = [{ region: "", count: [...state.regions.values()].reduce((a, r) => a + r.count, 0) }, ...state.regions.values()];
  for (const r of all) {
    const b = document.createElement("button");
    b.type = "button";
    if (r.region === state.region) b.classList.add("active");
    const unread = r.region ? state.unread.get(r.region) || 0 : 0;
    if (unread) b.classList.add("unread");
    const name = document.createElement("span");
    name.textContent = r.region || "All regions";
    const count = document.createElement("span");
    count.className = "count";
    count.textContent = unread ? `+${unread}` : String(r.count);
    b.append(name, count);
    b.addEventListener("click", () => selectRegion(r.region));
    nav.append(b);
  }
}

function trackRegion(m) {
  const r = state.regions.get(m.region) || { region: m.region, count: 0, lastTs: 0 };
  r.count += 1;
  r.lastTs = m.ts;
  state.regions.set(m.region, r);
  if (state.region && state.region !== m.region) {
    state.unread.set(m.region, (state.unread.get(m.region) || 0) + 1);
  }
}

async function loadRegions() {
  const list = await api("api/regions");
  state.regions = new Map(list.map((r) => [r.region, r]));
  renderRegions();
}

async function loadInitial() {
  feed.textContent = "";
  state.oldestId = 0;
  state.lastId = 0;
  const latest = await api("api/messages?limit=1");
  state.streamFrom = latest.length ? latest[0].id : 0;
  const msgs = await api(`api/messages${qs({ region: state.region, limit: PAGE })}`);
  append(msgs);
  if (msgs.length) {
    state.oldestId = msgs[0].id;
    state.lastId = msgs[msgs.length - 1].id;
  }
  olderBtn.hidden = msgs.length < PAGE;
  main.scrollTop = main.scrollHeight;
}

async function loadOlder() {
  if (!state.oldestId) return;
  olderBtn.disabled = true;
  try {
    const msgs = await api(`api/messages${qs({ region: state.region, before: state.oldestId, limit: PAGE })}`);
    if (msgs.length) {
      state.oldestId = msgs[0].id;
      prepend(msgs);
    }
    olderBtn.hidden = msgs.length < PAGE;
  } finally {
    olderBtn.disabled = false;
  }
}

const POLL_MS = 2000;
const POLL_HIDDEN_MS = 15000;
const POLL_BATCH = 500;
let pollTimer = 0;
let pollAbort = null;
let generation = 0;

// Polls all regions (not just the selected one) so other regions' unread counters update.
async function poll() {
  clearTimeout(pollTimer);
  if (pollAbort) pollAbort.abort();
  const ctrl = new AbortController();
  pollAbort = ctrl;
  const gen = generation;
  let full = false;
  try {
    const msgs = await api(`api/messages${qs({ after: state.streamFrom, limit: POLL_BATCH })}`, ctrl.signal);
    if (gen !== generation || ctrl.signal.aborted) return;
    setStatus(true);
    const visible = [];
    for (const m of msgs) {
      if (m.id <= state.streamFrom) continue;
      state.streamFrom = m.id;
      trackRegion(m);
      if ((!state.region || state.region === m.region) && m.id > state.lastId) {
        state.lastId = m.id;
        visible.push(m);
      }
    }
    append(visible);
    if (msgs.length) renderRegionsThrottled();
    full = msgs.length === POLL_BATCH;
  } catch (err) {
    if (gen !== generation || ctrl.signal.aborted) return;
    setStatus(false);
  }
  if (gen === generation && pollAbort === ctrl) {
    pollTimer = setTimeout(poll, full ? 0 : document.hidden ? POLL_HIDDEN_MS : POLL_MS);
  }
}

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) poll();
});

let renderPending = false;
function renderRegionsThrottled() {
  if (renderPending) return;
  renderPending = true;
  requestAnimationFrame(() => { renderPending = false; renderRegions(); });
}

function setStatus(online) {
  statusEl.textContent = online ? "live" : "offline, retrying…";
  statusEl.className = `status ${online ? "online" : "offline"}`;
}

async function selectRegion(region) {
  state.region = region;
  state.unread.delete(region);
  history.replaceState(null, "", region ? `?region=${encodeURIComponent(region)}` : location.pathname);
  renderRegions();
  const gen = ++generation;
  clearTimeout(pollTimer);
  if (pollAbort) pollAbort.abort();
  try {
    await loadInitial();
  } catch (err) {
    console.error(err);
    setStatus(false);
  }
  if (gen === generation) poll();
}

let filterTimer = 0;
searchEl.addEventListener("input", () => {
  clearTimeout(filterTimer);
  filterTimer = setTimeout(() => {
    state.filter = searchEl.value.trim().toLowerCase();
    for (const li of feed.children) {
      applyFilter(li);
      const t = li.querySelector(".text");
      highlight(t, t.dataset.raw);
    }
  }, 150);
});
olderBtn.addEventListener("click", loadOlder);

(async function init() {
  try {
    await loadRegions();
  } catch (err) {
    console.error(err);
  }
  await selectRegion(state.region);
})();
