"use strict";

const MAX_ROWS = 3000;
const PAGE = 200;
const POLL_MS = 2000;
const POLL_HIDDEN_MS = 15000;
const POLL_BATCH = 500;
const ALL = "";

// In-game channel symbols: ! green, @ purple, # $ % orange, ^ red.
const CHANNELS = { "!": "ch-green", "@": "ch-purple", "#": "ch-orange", "$": "ch-orange", "%": "ch-orange", "^": "ch-red" };

const $ = (sel, root = document) => root.querySelector(sel);
const statusEl = $("#status");
const searchEl = $("#search");
const autoEl = $("#autoscroll");

const timeFmt = new Intl.DateTimeFormat("vi-VN", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const dateFmt = new Intl.DateTimeFormat("vi-VN", { dateStyle: "short", timeStyle: "medium" });

const state = { regions: [], streamFrom: 0, filter: "" };

function load(key) {
  try { return localStorage.getItem(key); } catch { return null; }
}

function save(key, value) {
  try { localStorage.setItem(key, value); } catch { /* storage unavailable */ }
}

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

function applyFilter(li) {
  li.classList.toggle("hidden", !!state.filter && !li.dataset.text.includes(state.filter));
}

function row(m, showRegion) {
  const li = document.createElement("li");
  li.dataset.id = m.id;
  li.dataset.text = m.text.toLowerCase();
  const ch = CHANNELS[m.text.charAt(0)];
  if (ch && m.text.charAt(1) === "[") li.classList.add(ch);
  const t = document.createElement("time");
  const d = new Date(m.ts);
  t.dateTime = d.toISOString();
  t.textContent = timeFmt.format(d);
  t.title = `${dateFmt.format(d)} · ${m.client || "?"}`;
  li.append(t);
  if (showRegion) {
    const tag = document.createElement("span");
    tag.className = "tag";
    tag.textContent = m.region;
    li.append(tag);
  } else {
    li.classList.add("no-tag");
  }
  const text = document.createElement("span");
  text.className = "text";
  text.dataset.raw = m.text;
  highlight(text, m.text);
  li.append(text);
  applyFilter(li);
  return li;
}

class Pane {
  constructor(el, index) {
    this.index = index;
    this.select = $("select", el);
    this.countEl = $(".count", el);
    this.scroll = $(".scroll", el);
    this.feed = $(".feed", el);
    this.olderBtn = $(".older", el);
    this.region = ALL;
    this.oldestId = 0;
    this.lastId = 0;
    this.gen = 0;
    this.select.addEventListener("change", () => this.setRegion(this.select.value).catch(console.error));
    this.olderBtn.addEventListener("click", () => this.loadOlder().catch(console.error));
  }

  matches(m) {
    return !this.region || this.region === m.region;
  }

  renderOptions() {
    this.select.textContent = "";
    const names = state.regions.map((r) => r.region);
    if (this.region && !names.includes(this.region)) names.push(this.region);
    const opts = [[ALL, "Tất cả vùng"], ...names.map((n) => [n, n])];
    for (const [value, label] of opts) {
      const opt = document.createElement("option");
      opt.value = value;
      opt.textContent = label;
      opt.selected = value === this.region;
      this.select.append(opt);
    }
  }

  nearBottom() {
    return this.scroll.scrollHeight - this.scroll.scrollTop - this.scroll.clientHeight < 80;
  }

  append(msgs) {
    if (!msgs.length) return;
    const stick = autoEl.checked && this.nearBottom();
    const frag = document.createDocumentFragment();
    for (const m of msgs) frag.append(row(m, !this.region));
    this.feed.append(frag);
    while (this.feed.childElementCount > MAX_ROWS) {
      this.feed.firstElementChild.remove();
      this.oldestId = Number(this.feed.firstElementChild.dataset.id);
      this.olderBtn.hidden = false;
    }
    if (stick) this.scroll.scrollTop = this.scroll.scrollHeight;
    this.updateCount();
  }

  updateCount() {
    this.countEl.textContent = `${this.feed.childElementCount} tin`;
  }

  async setRegion(region) {
    this.region = region;
    save(`ace-spy.pane${this.index}`, region);
    syncURL();
    const gen = ++this.gen;
    this.feed.textContent = "";
    this.oldestId = 0;
    this.lastId = 0;
    this.updateCount();
    const msgs = await api(`api/messages${qs({ region, limit: PAGE })}`);
    if (gen !== this.gen) return;
    this.append(msgs);
    if (msgs.length) {
      this.oldestId = msgs[0].id;
      this.lastId = msgs[msgs.length - 1].id;
    }
    this.olderBtn.hidden = msgs.length < PAGE;
    this.scroll.scrollTop = this.scroll.scrollHeight;
  }

  async loadOlder() {
    if (!this.oldestId) return;
    this.olderBtn.disabled = true;
    try {
      const msgs = await api(`api/messages${qs({ region: this.region, before: this.oldestId, limit: PAGE })}`);
      if (msgs.length) {
        this.oldestId = msgs[0].id;
        const before = this.scroll.scrollHeight;
        const frag = document.createDocumentFragment();
        for (const m of msgs) frag.append(row(m, !this.region));
        this.feed.prepend(frag);
        this.scroll.scrollTop += this.scroll.scrollHeight - before;
        this.updateCount();
      }
      this.olderBtn.hidden = msgs.length < PAGE;
    } finally {
      this.olderBtn.disabled = false;
    }
  }

  receive(msgs) {
    const fresh = msgs.filter((m) => this.matches(m) && m.id > this.lastId);
    if (!fresh.length) return;
    this.lastId = fresh[fresh.length - 1].id;
    this.append(fresh);
  }
}

const panes = [...document.querySelectorAll(".pane")].map((el, i) => new Pane(el, i));
const URL_KEYS = ["left", "right"];

function syncURL() {
  const p = new URLSearchParams();
  panes.forEach((pane, i) => { if (pane.region) p.set(URL_KEYS[i], pane.region); });
  const s = p.toString();
  history.replaceState(null, "", s ? `?${s}` : location.pathname);
}

function setStatus(online) {
  statusEl.textContent = online ? "live" : "mất kết nối, thử lại…";
  statusEl.className = `status ${online ? "online" : "offline"}`;
}

async function refreshRegions() {
  try {
    state.regions = await api("api/regions");
  } catch (err) {
    console.error(err);
  }
  for (const p of panes) p.renderOptions();
}

let pollTimer = 0;
let pollAbort = null;

// One poll for all regions; each pane keeps the messages that match it.
async function poll() {
  clearTimeout(pollTimer);
  if (pollAbort) pollAbort.abort();
  const ctrl = new AbortController();
  pollAbort = ctrl;
  let full = false;
  try {
    const msgs = await api(`api/messages${qs({ after: state.streamFrom, limit: POLL_BATCH })}`, ctrl.signal);
    if (ctrl.signal.aborted) return;
    setStatus(true);
    const fresh = msgs.filter((m) => m.id > state.streamFrom);
    if (fresh.length) {
      state.streamFrom = fresh[fresh.length - 1].id;
      if (fresh.some((m) => !state.regions.some((r) => r.region === m.region))) refreshRegions();
      for (const p of panes) p.receive(fresh);
    }
    full = msgs.length === POLL_BATCH;
  } catch {
    if (ctrl.signal.aborted) return;
    setStatus(false);
  }
  if (pollAbort === ctrl) {
    pollTimer = setTimeout(poll, full ? 0 : document.hidden ? POLL_HIDDEN_MS : POLL_MS);
  }
}

document.addEventListener("visibilitychange", () => { if (!document.hidden) poll(); });

let filterTimer = 0;
searchEl.addEventListener("input", () => {
  clearTimeout(filterTimer);
  filterTimer = setTimeout(() => {
    state.filter = searchEl.value.trim().toLowerCase();
    for (const p of panes) {
      for (const li of p.feed.children) {
        applyFilter(li);
        const t = li.querySelector(".text");
        highlight(t, t.dataset.raw);
      }
    }
  }, 150);
});

(async function init() {
  try {
    const latest = await api("api/messages?limit=1");
    state.streamFrom = latest.length ? latest[0].id : 0;
  } catch {
    setStatus(false);
  }
  await refreshRegions();
  const params = new URLSearchParams(location.search);
  const names = state.regions.map((r) => r.region);
  const defaults = [names[0] ?? ALL, names[1] ?? names[0] ?? ALL];
  await Promise.all(panes.map((p, i) => {
    const saved = load(`ace-spy.pane${i}`);
    p.region = params.get(URL_KEYS[i]) ?? (saved !== null && (saved === ALL || names.includes(saved)) ? saved : defaults[i]);
    p.renderOptions();
    return p.setRegion(p.region).catch(console.error);
  }));
  poll();
})();
