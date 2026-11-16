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
const brandEl = $("#brand");
const searchEl = $("#search");
const autoEl = $("#autoscroll");
const singleEl = $("#single");
const alignEl = $("#align");

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

const NOTICE_PREFIX = "§ ";

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

// Game colours that have a themed equivalent; other player colours are shown as-is.
const GAME_COLORS = { ea2222: "var(--ch-red)", "00ffff": "var(--ch-cyan)", feae00: "var(--ch-gold)" };

// Splits the body into runs of equal captured colour. m.colors holds the colours of
// the last m.colors.length tokens of m.text; null colour = inherit the channel colour.
function bodyRuns(body, colors) {
  const toks = body.split(/\s+/).filter(Boolean);
  if (!colors || !colors.length) return [[body, null]];
  const cs = colors.slice(-toks.length);
  const pad = toks.length - cs.length;
  const runs = [];
  toks.forEach((tok, i) => {
    const raw = i < pad ? null : cs[i - pad];
    const c = gameColor(raw);
    const last = runs[runs.length - 1];
    if (last && last[1] === c) last[0] += ` ${tok}`;
    else runs.push([runs.length ? ` ${tok}` : tok, c]);
  });
  return runs;
}

function gameColor(raw) {
  return !raw || raw === "-" ? null : GAME_COLORS[raw] || `#${raw}`;
}

// Captured colours of the header groups: m.colors covers the trailing tokens of
// m.text, and the groups follow the "^[hh:mm]" token.
function groupColors(m, groups) {
  const toks = m.text.split(/\s+/).filter(Boolean);
  const colors = m.colors || [];
  const pad = toks.length - colors.length;
  const out = [];
  let i = 1;
  for (const [cls, g] of groups) {
    const n = g.split(/\s+/).length;
    if (toks.slice(i, i + n).join(" ") !== g) break;
    out.push(i < pad ? null : gameColor(colors[i - pad]));
    i += n;
  }
  return out;
}

function row(m, showRegion) {
  const li = document.createElement("li");
  li.dataset.id = m.id;
  li.dataset.text = m.text.toLowerCase();
  const ch = CHANNELS[m.text.charAt(0)];
  if (ch && m.text.charAt(1) === "[") li.classList.add(ch);
  const d = new Date(m.ts);
  li.title = `${m.text}\nNhận lúc ${dateFmt.format(d)} · ${m.client || "?"}${showRegion ? ` · ${m.region}` : ""}`;
  // Header (region tag, channel, time, names) and message are two columns, so every
  // message starts at the same x and wraps within its own column.
  const hw = document.createElement("span");
  hw.className = "hw";
  const msg = document.createElement("span");
  msg.className = "msg";
  li.append(hw, msg);
  if (showRegion) {
    const tag = document.createElement("span");
    tag.className = "tag";
    tag.textContent = m.region;
    tag.dataset.region = m.region.toUpperCase();
    hw.append(tag);
  }
  const notice = m.text.startsWith(NOTICE_PREFIX);
  if (notice) li.classList.add("notice");
  const f = notice ? null : formatted(m.text);
  const text = notice ? m.text.slice(NOTICE_PREFIX.length) : m.text;
  const body = f ? bodyRuns(f.body, m.colors).map(([t, c]) => ["text", t, c]) : [["text", text, null]];
  const gc = f ? groupColors(m, f.groups) : [];
  const groups = f ? f.groups.map(([cls, g], i) => [cls, g, gc[i] || null]) : [];
  const head = f ? [["chan", f.channel], ["head", f.head], ...groups] : notice ? [["chan", ""]] : [];
  for (const [parts, parent] of [[head, hw], [body, msg]]) {
    for (const [cls, value, color] of parts) {
      const span = document.createElement("span");
      span.className = cls;
      if (color) span.style.color = color;
      if (cls === "chan") span.dataset.ch = value;
      span.dataset.raw = value;
      highlight(span, value);
      parent.append(span);
    }
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
    this.unread = 0;
    this.jumpBtn = document.createElement("button");
    this.jumpBtn.type = "button";
    this.jumpBtn.className = "jump";
    this.jumpBtn.hidden = true;
    el.append(this.jumpBtn);
    this.jumpBtn.addEventListener("click", () => this.scroll.scrollTo({ top: this.scroll.scrollHeight, behavior: "smooth" }));
    this.scroll.addEventListener("scroll", () => this.updateJump(), { passive: true });
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

  // Widens the header column to the widest header shown (CSS caps it), measured on
  // the content, not the column's current min-width.
  fitHeader(lis) {
    let w = this.headW || 0;
    for (const li of lis) {
      const hw = li.firstElementChild;
      const first = hw.firstElementChild, last = hw.lastElementChild;
      if (!first || li.classList.contains("notice")) continue;
      // min-width is border-box: include the column's own padding.
      const pad = parseFloat(getComputedStyle(hw).paddingRight) || 0;
      w = Math.max(w, Math.ceil(last.getBoundingClientRect().right - first.getBoundingClientRect().left + pad));
    }
    if (w !== this.headW) {
      this.headW = w;
      this.feed.style.setProperty("--head-w", `${w}px`);
    }
  }

  updateJump() {
    const away = !this.nearBottom();
    if (!away) this.unread = 0;
    this.jumpBtn.hidden = !away;
    this.jumpBtn.textContent = this.unread ? `↓ ${this.unread} tin mới` : "↓ Mới nhất";
  }

  nearBottom() {
    return this.scroll.scrollHeight - this.scroll.scrollTop - this.scroll.clientHeight < 80;
  }

  // Inserts messages ordered by in-game [hh:mm], ties by capture order (id),
  // skipping messages already shown (same region and text, ignoring accents/case).
  insert(msgs) {
    let added = 0;
    const fresh = [];
    for (const m of msgs) {
      if (m.replaces) {
        const old = this.feed.querySelector(`li[data-id="${m.replaces}"]`);
        if (old) {
          this.seen.delete(old.dataset.dup);
          old.remove();
          added--;
        }
      }
      const dup = `${m.region}|${foldText(m.text)}`;
      if (this.seen.has(dup)) continue;
      this.seen.add(dup);
      const li = row(m, !this.region);
      li.dataset.dup = dup;
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
      fresh.push(li);
      added++;
    }
    this.fitHeader(fresh);
    while (this.feed.childElementCount > MAX_ROWS) {
      this.feed.firstElementChild.remove();
      this.olderBtn.hidden = false;
    }
    return Math.max(added, 0);
  }

  append(msgs) {
    if (!msgs.length) return;
    const stick = autoEl.checked && this.nearBottom();
    const added = this.insert(msgs);
    if (stick) this.scroll.scrollTop = this.scroll.scrollHeight;
    else if (!this.nearBottom()) this.unread += added;
    this.updateCount();
    this.updateJump();
  }

  showSkeleton() {
    const frag = document.createDocumentFragment();
    for (let i = 0; i < 14; i++) {
      const li = document.createElement("li");
      li.className = "skeleton";
      li.setAttribute("aria-hidden", "true");
      const bar = document.createElement("span");
      bar.style.width = `${35 + ((i * 37) % 55)}%`;
      li.append(bar);
      frag.append(li);
    }
    this.feed.append(frag);
    this.countEl.textContent = "đang tải…";
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
    this.headW = 0;
    this.feed.style.setProperty("--head-w", "0px");
    this.seen = new Set();
    this.oldestId = 0;
    this.lastId = 0;
    this.updateCount();
    this.showSkeleton();
    let msgs;
    try {
      msgs = await api(`api/messages${qs({ region, limit: PAGE })}`);
    } finally {
      if (gen === this.gen) this.feed.textContent = "";
    }
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
// Header widths measured before the web font loaded are off by a few px: refit.
document.fonts?.ready.then(() => {
  for (const p of panes) { p.headW = 0; p.fitHeader([...p.feed.children]); }
});
const URL_KEYS = ["left", "right"];

function setSingle(on) {
  singleEl.checked = on;
  document.body.classList.toggle("single", on);
  save("ace-spy.single", on ? "1" : "0");
}

singleEl.addEventListener("change", () => { setSingle(singleEl.checked); syncURL(); });

// Settings live in localStorage: align (ace-spy.align), single (ace-spy.single), autoscroll.
const AUTOSCROLL_KEY = "ace-spy.autoscroll";
autoEl.checked = load(AUTOSCROLL_KEY) !== "0";
autoEl.addEventListener("change", () => save(AUTOSCROLL_KEY, autoEl.checked ? "1" : "0"));

function syncURL() {
  const p = new URLSearchParams();
  if (singleEl.checked) p.set("view", "1");
  panes.forEach((pane, i) => { if (pane.region) p.set(URL_KEYS[i], pane.region); });
  const s = p.toString();
  history.replaceState(null, "", s ? `?${s}` : location.pathname);
}

// LIVE = the server is reachable and at least one ace-spy client sent a capture
// heartbeat recently; idle = reachable but nothing is capturing.
const live = { connected: null, clients: null, regions: [] };

const STATE_COLORS = { online: "#50ff8c", idle: "#ffbe3c", offline: "#ff5a50", "": "#3ec8ff" };
const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)");

// Sweeps the new state colour along the logo line; the line keeps its old colour
// until the sweep reaches the end.
function wipeLine(color, first) {
  if (first || reduceMotion.matches) {
    brandEl.style.setProperty("--line", color);
    return;
  }
  brandEl.style.setProperty("--to", color);
  brandEl.classList.remove("wiping");
  void brandEl.offsetWidth; // restart the animation
  brandEl.classList.add("wiping");
}

brandEl.addEventListener("animationend", (ev) => {
  if (ev.animationName !== "beat-fill") return;
  brandEl.style.setProperty("--line", brandEl.style.getPropertyValue("--to"));
  brandEl.classList.remove("wiping");
});

function renderStatus() {
  let text = "đang kết nối…", cls = "", title = "";
  if (live.connected === false) {
    text = "mất kết nối, thử lại…";
    cls = "offline";
  } else if (live.connected && live.clients > 0) {
    text = "live";
    cls = "online";
    title = `${live.clients} client đang capture: ${live.regions.join(", ")}`;
  } else if (live.connected && live.clients === 0) {
    text = "không có client";
    cls = "idle";
    title = "Không có ace-spy nào đang capture chat";
  }
  statusEl.textContent = text;
  statusEl.dataset.short = { online: "LIVE", idle: "IDLE", offline: "OFFLINE" }[cls] || "…";
  statusEl.className = `status ${cls}`.trim();
  if (brandEl.dataset.state !== cls) wipeLine(STATE_COLORS[cls] || STATE_COLORS[""], brandEl.dataset.state === undefined);
  brandEl.dataset.state = cls;
  brandEl.className = `brand ${cls}${brandEl.classList.contains("wiping") ? " wiping" : ""}`;
  brandEl.title = title ? `${text.toUpperCase()} · ${title}` : text;
}

function setStatus(online) {
  live.connected = online;
  renderStatus();
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

// Message column: "auto" (widest header shown), a fixed width in px, or "off".
const ALIGN_KEY = "ace-spy.align";
function setAlign(v) {
  if (![...alignEl.options].some((o) => o.value === v)) v = "auto";
  alignEl.value = v;
  document.body.dataset.align = /^\d+$/.test(v) ? "fixed" : v;
  document.body.style.setProperty("--head-fixed", /^\d+$/.test(v) ? `${v}px` : "0px");
  save(ALIGN_KEY, v);
}
setAlign(load(ALIGN_KEY) || "auto");
alignEl.addEventListener("change", () => setAlign(alignEl.value));

const settingsEl = $("#settings");
document.addEventListener("click", (ev) => { if (!settingsEl.contains(ev.target)) settingsEl.open = false; });
document.addEventListener("keydown", (ev) => { if (ev.key === "Escape") settingsEl.open = false; });

// Presence: one random id per browser, so several tabs count as one viewer.
const HEARTBEAT_MS = 15000;
const viewersEl = $("#viewers");
const viewerId = (() => {
  const key = "ace-spy.viewer";
  let id = load(key);
  if (!id || !/^[A-Za-z0-9_-]{16,64}$/.test(id)) {
    id = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");
    save(key, id);
  }
  return id;
})();

async function heartbeat() {
  try {
    const r = await fetch("api/presence", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: viewerId }),
    });
    if (!r.ok) throw new Error(`${r.status}`);
    const { online, clients, regions } = await r.json();
    $(".n", viewersEl).textContent = online;
    viewersEl.hidden = false;
    live.clients = clients ?? null;
    live.regions = regions || [];
  } catch {
    viewersEl.hidden = true;
    live.clients = null;
  }
  renderStatus();
}

heartbeat();
setInterval(heartbeat, HEARTBEAT_MS);

(async function init() {
  try {
    const latest = await api("api/messages?limit=1");
    state.streamFrom = latest.length ? latest[0].id : 0;
  } catch {
    setStatus(false);
  }
  await refreshRegions();
  const params = new URLSearchParams(location.search);
  setSingle(params.has("view") ? params.get("view") === "1" : load("ace-spy.single") === "1");
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
