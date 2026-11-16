"use strict";

const CELL_H = 20;
const SCALE = 8;
const TOKEN_KEY = "ace-spy.token";

const $ = (sel, root = document) => root.querySelector(sel);
const listEl = $("#glyphs");
const stateEl = $("#state");
const tokenEl = $("#token");
const dateFmt = new Intl.DateTimeFormat("vi-VN", { dateStyle: "short", timeStyle: "short" });

try { tokenEl.value = localStorage.getItem(TOKEN_KEY) || ""; } catch { /* storage unavailable */ }
tokenEl.addEventListener("change", () => {
  try { localStorage.setItem(TOKEN_KEY, tokenEl.value.trim()); } catch { /* storage unavailable */ }
});

function draw(canvas, cols, color) {
  canvas.width = cols.length * SCALE;
  canvas.height = CELL_H * SCALE;
  const ctx = canvas.getContext("2d");
  ctx.fillStyle = "#0b1621";
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.fillStyle = `#${color}`;
  cols.forEach((c, x) => {
    for (let r = 0; r < CELL_H; r++) {
      if ((c >>> r) & 1) ctx.fillRect(x * SCALE, r * SCALE, SCALE - 1, SCALE - 1);
    }
  });
}

function contextEl(text) {
  const span = document.createElement("span");
  span.className = "ctx";
  const [before, ...rest] = text.split("□");
  span.append(before);
  if (rest.length) {
    const mark = document.createElement("mark");
    mark.textContent = "□";
    span.append(mark, rest.join("□"));
  }
  return span;
}

async function save(g, input, status) {
  const token = tokenEl.value.trim();
  if (!token) { status.textContent = "Nhập token trước"; tokenEl.focus(); return; }
  status.textContent = "Đang lưu…";
  try {
    const r = await fetch(`api/glyphs/${g.hash}/label`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify({ label: input.value.trim() }),
    });
    if (!r.ok) throw new Error(r.status === 401 ? "sai token" : `${r.status}`);
    g.label = input.value.trim();
    status.textContent = g.label ? "Đã lưu" : "Đã bỏ nhãn";
  } catch (err) {
    status.textContent = `Lỗi: ${err.message}`;
  }
}

function card(g) {
  const li = document.createElement("li");
  const canvas = document.createElement("canvas");
  draw(canvas, g.cols, g.color);
  const info = document.createElement("div");
  info.className = "info";
  const meta = document.createElement("div");
  meta.className = "meta";
  meta.textContent = `${g.region} · gặp ${g.count} lần · lần cuối ${dateFmt.format(new Date(g.lastSeen))}`;
  const form = document.createElement("form");
  const input = document.createElement("input");
  input.value = g.label;
  input.maxLength = 4;
  input.placeholder = "Ký tự";
  input.setAttribute("aria-label", "Ký tự đúng");
  const btn = document.createElement("button");
  btn.type = "submit";
  btn.textContent = "Lưu";
  const status = document.createElement("span");
  status.className = "status-text";
  form.append(input, btn, status);
  form.addEventListener("submit", (ev) => { ev.preventDefault(); save(g, input, status); });
  info.append(contextEl(g.context), meta, form);
  li.append(canvas, info);
  return li;
}

(async function load() {
  stateEl.textContent = "Đang tải…";
  try {
    const r = await fetch("api/glyphs");
    if (!r.ok) throw new Error(`${r.status}`);
    const gs = await r.json();
    stateEl.textContent = gs.length ? `${gs.length} ký tự (chưa gán nhãn ở trên)` : "Chưa có ký tự lạ nào — OCR đang đọc được mọi thứ.";
    listEl.replaceChildren(...gs.map(card));
  } catch (err) {
    stateEl.textContent = `Không tải được: ${err.message}`;
  }
})();
