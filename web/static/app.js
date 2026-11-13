"use strict";

const MAX_ROWS = 3000;
const PAGE = 200;
const POLL_MS = 2000;
const POLL_HIDDEN_MS = 15000;
const POLL_BATCH = 500;
const ALL = "";

// In-game channel colours, measured from captures: ^ red, * cyan, ! green,
// # pale green, $ gold, @ purple, no symbol grey.
const CHANNELS = { "^": "ch-red", "*": "ch-cyan", "!": "ch-green", "#": "ch-pale", "$": "ch-gold", "@": "ch-purple", "%": "ch-gold" };

const $ = (sel, root = document) => root.querySelector(sel);
const statusEl = $("#status");
const searchEl = $("#search");
const autoEl = $("#autoscroll");

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

// "*[14:11] [Name] [BC] text" -> channel, time, names, body.
const MSG_RE = /^([!*@#$%^]?)\[(\d{2}:\d{2})\]((?:\s*\[[^\]]*\])*)\s*:?\s*([\s\S]*)$/;

// Header groups after [hh:mm], measured from game captures:
//   * -> [Title][Player]   (name second, no map)
//   ^ -> [Player][Map]
//   others -> [Player]
// Further [..] groups are item links and belong to the message.
const HEADER = { "*": { count: 2, player: 1 }, "^": { count: 2, player: 0 } };
const DEFAULT_HEADER = { count: 1, player: 0 };

function formatted(text) {
  const m = MSG_RE.exec(text);
  if (!m) return null;
  const groups = m[3].match(/\[[^\]]*\]/g) || [];
  const layout = HEADER[m[1]] || DEFAULT_HEADER;
  const header = groups.slice(0, layout.count);
  const extra = groups.slice(layout.count).join(" ");
  const player = Math.min(layout.player, header.length - 1);
  return {
    channel: m[1],
    head: `[${m[2]}]`,
    groups: header.map((g, i) => [i === player ? "player" : "group", g]),
    body: extra ? `${extra} ${m[4]}`.trim() : m[4],
  };
}

function row(m, showRegion) {
  const li = document.createElement("li");
  li.dataset.id = m.id;
  li.dataset.text = m.text.toLowerCase();
  const ch = CHANNELS[m.text.charAt(0)];
  if (ch && m.text.charAt(1) === "[") li.classList.add(ch);
  const d = new Date(m.ts);
  li.title = `${m.text}\nNhận lúc ${dateFmt.format(d)} · ${m.client || "?"}${showRegion ? ` · ${m.region}` : ""}`;
  if (showRegion) {
    const tag = document.createElement("span");
    tag.className = "tag";
    tag.textContent = m.region;
    li.append(tag);
  }
  const f = formatted(m.text);
  const parts = f ? [["chan", f.channel], ["head", f.head], ...f.groups, ["sep", " "], ["text", f.body]] : [["text", m.text]];
  for (const [cls, value] of parts) {
    const span = document.createElement("span");
    span.className = cls;
    if (cls === "chan") span.dataset.ch = value;
    span.dataset.raw = value;
    highlight(span, value);
    li.append(span);
  }
  applyFilter(li);
  return li;
}

// Game clock is Vietnam time (UTC+7). [hh:mm] has no date, so each message is
// placed on the day that puts it closest before the time it was received.
const GAME_UTC_OFFSET_MIN = 7 * 60;
const TIME_RE = /^[!*@#$%^~]?\[(\d{2}):(\d{2})\]/;

function orderKey(m) {
  const recv = Math.floor((m.receivedAt || m.ts) / 60000) + GAME_UTC_OFFSET_MIN;
  const t = TIME_RE.exec(m.text);
  if (!t) return recv;
  let k = Math.floor(recv / 1440) * 1440 + Number(t[1]) * 60 + Number(t[2]);
  while (k > recv + 120) k -= 1440; // game clock may run slightly ahead
  while (k <= recv - 1320) k += 1440;
  return k;
}

function before(k, id, li) {
  const lk = Number(li.dataset.k);
  return k < lk || (k === lk && id < Number(li.dataset.id));
}

function foldText(s) {
  return s.normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/đ/gi, "d").toLowerCase().replace(/[^a-z0-9]+/g, "");
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
    this.seen = new Set();
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

  // Inserts messages ordered by in-game [hh:mm], ties by capture order (id),
  // skipping messages already shown (same region and text, ignoring accents/case).
  insert(msgs) {
    let added = false;
    for (const m of msgs) {
      const dup = `${m.region}|${foldText(m.text)}`;
      if (this.seen.has(dup)) continue;
      this.seen.add(dup);
      const li = row(m, !this.region);
      const k = orderKey(m);
      li.dataset.k = k;
      const kids = this.feed.children;
      let lo = 0, hi = kids.length;
      const last = kids[hi - 1];
      if (last && !before(k, m.id, last)) {
        lo = hi;
      } else {
        while (lo < hi) {
          const mid = (lo + hi) >> 1;
          if (before(k, m.id, kids[mid])) hi = mid; else lo = mid + 1;
        }
      }
      this.feed.insertBefore(li, kids[lo] || null);
      added = true;
    }
    while (this.feed.childElementCount > MAX_ROWS) {
      this.feed.firstElementChild.remove();
      this.olderBtn.hidden = false;
    }
    return added;
  }

  append(msgs) {
    if (!msgs.length) return;
    const stick = autoEl.checked && this.nearBottom();
    this.insert(msgs);
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
    this.seen = new Set();
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
        const height = this.scroll.scrollHeight;
        this.insert(msgs);
        this.scroll.scrollTop += this.scroll.scrollHeight - height;
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
        for (const span of li.querySelectorAll("[data-raw]")) highlight(span, span.dataset.raw);
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
