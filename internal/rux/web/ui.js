// rux UI renderer: renders json-render style specs (https://json-render.dev)
// with the app's own design system. Specs come from agents (`rux ui`), so
// everything is built with DOM APIs (no innerHTML) and links are restricted.
// The catalog here must match uiCatalog (ui.go) and catalogDoc (uicli.go).
"use strict";
(function () {
  const css = `
.ui { display: grid; gap: 12px; min-width: 0; font-size: 13.5px; }
.ui-block { border: 1px solid var(--line); border-radius: 6px; background: var(--panel); padding: 12px; min-width: 0; position: relative; }
.ui-block > .ui-head { display: flex; gap: 10px; align-items: baseline; font-size: 12px; color: var(--muted); margin: -2px 0 10px; }
.ui-block > .ui-head b { font-weight: 400; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ui-block > .ui-head b::before { content: "# "; color: var(--faint); }
.ui-block.answered { opacity: .8; }
.ui-ans { color: var(--green-dim); font-size: 12px; }
.ui-mini { color: var(--faint); font-size: 12px; cursor: pointer; }
.ui-mini:hover { color: var(--green); }
.ui-err { color: var(--err); font-size: 12px; border: 1px dashed var(--err); border-radius: 4px; padding: 4px 8px; }
.ui-card { border: 1px solid var(--line); border-radius: 6px; padding: 12px; display: grid; gap: 10px; min-width: 0; background: var(--bg); }
.ui-card.green { border-color: var(--green-dim); } .ui-card.warn { border-color: var(--warn); } .ui-card.error { border-color: var(--err); }
.ui-card-h { display: grid; gap: 2px; }
.ui-card-t { color: var(--text); } .ui-card-t::before { content: "▸ "; color: var(--green-dim); }
.ui-card-s { color: var(--muted); font-size: 12px; }
.ui-stack { display: flex; flex-direction: column; min-width: 0; }
.ui-stack.row { flex-direction: row; align-items: center; }
.ui-stack.wrap { flex-wrap: wrap; }
.ui-stack > * { min-width: 0; }
.ui-grid { display: grid; min-width: 0; }
.ui-h1 { font-size: 17px; color: var(--green); text-shadow: var(--glow); font-weight: 700; }
.ui-h2 { font-size: 15px; color: var(--text); font-weight: 700; }
.ui-h3 { font-size: 13px; color: var(--muted); font-weight: 400; } .ui-h3::before { content: "# "; color: var(--faint); }
.ui-text { overflow-wrap: anywhere; } .ui-text.mono { white-space: pre-wrap; }
.tone-muted { color: var(--muted); } .tone-faint { color: var(--faint); } .tone-green { color: var(--green); } .tone-warn { color: var(--warn); } .tone-error { color: var(--err); }
.ui-badge { display: inline-block; font-size: 11.5px; border: 1px solid var(--line); border-radius: 3px; padding: 0 6px; color: var(--muted); white-space: nowrap; }
.ui-badge.green { color: var(--green); border-color: var(--green-dim); } .ui-badge.warn { color: var(--warn); border-color: var(--warn); } .ui-badge.error { color: var(--err); border-color: var(--err); }
.ui-hr { border: 0; border-top: 1px solid var(--line); margin: 2px 0; }
.ui-stat { min-width: 0; } .ui-stat .k { font-size: 11.5px; color: var(--muted); }
.ui-stat .v { font-size: 22px; line-height: 1.35; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ui-stat .v.green { color: var(--green); text-shadow: var(--glow); } .ui-stat .v.warn { color: var(--warn); } .ui-stat .v.error { color: var(--err); }
.ui-stat .s { font-size: 11.5px; color: var(--faint); } .ui-stat .up { color: var(--green); } .ui-stat .down { color: var(--err); }
.ui-meter .k { display: flex; justify-content: space-between; font-size: 12px; color: var(--muted); }
.ui-meter .bar { height: 6px; background: var(--line); border-radius: 3px; overflow: hidden; margin: 4px 0 2px; }
.ui-meter .bar i { display: block; height: 100%; background: var(--green); box-shadow: var(--glow); }
.ui-meter .s { font-size: 11.5px; color: var(--faint); }
.ui-kv { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 4px 14px; font-size: 13px; }
.ui-kv dt { color: var(--muted); } .ui-kv dd { margin: 0; overflow-wrap: anywhere; }
.ui-table-wrap { overflow-x: auto; min-width: 0; }
.ui-table { border-collapse: collapse; width: 100%; font-size: 12.5px; }
.ui-table th { text-align: left; font-weight: 400; color: var(--muted); border-bottom: 1px solid var(--line); padding: 4px 10px 6px 0; white-space: nowrap; }
.ui-table td { padding: 5px 10px 5px 0; border-bottom: 1px solid var(--line); vertical-align: top; overflow-wrap: anywhere; }
.ui-table tr:last-child td { border-bottom: 0; }
.ui-table .r { text-align: right; } .ui-table .m { color: var(--green); }
.ui-list { display: grid; }
.ui-li { display: flex; gap: 10px; align-items: baseline; padding: 6px 2px; border-bottom: 1px solid var(--line); min-width: 0; }
.ui-li:last-child { border-bottom: 0; }
.ui-li .dot { align-self: center; }
.ui-li .t { flex: 1; min-width: 0; } .ui-li .t div:first-child { overflow-wrap: anywhere; } .ui-li .s { font-size: 12px; color: var(--muted); }
a.ui-li:hover .t div:first-child { color: var(--green); }
.ui-tabs-bar { display: flex; gap: 2px; border-bottom: 1px solid var(--line); margin-bottom: 10px; overflow-x: auto; }
.ui-tabs-bar button { padding: 4px 12px; color: var(--muted); border-bottom: 2px solid transparent; white-space: nowrap; font-size: 12.5px; }
.ui-tabs-bar button.on { color: var(--green); border-bottom-color: var(--green); }
.ui-svg { display: block; width: 100%; overflow: visible; }
.ui-svg text { fill: var(--faint); font: 10.5px var(--mono); }
.ui-svg .grid { stroke: var(--line); stroke-width: 1; }
.ui-legend { display: flex; flex-wrap: wrap; gap: 4px 14px; font-size: 11.5px; color: var(--muted); margin-top: 6px; }
.ui-legend i { display: inline-block; width: 10px; height: 3px; margin-right: 6px; vertical-align: middle; }
.ui-tl { display: grid; grid-template-columns: minmax(70px, max-content) minmax(0, 1fr); gap: 6px 10px; align-items: center; font-size: 12px; }
.ui-tl .lane { color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 32vw; }
.ui-tl .track { position: relative; height: 16px; background: repeating-linear-gradient(90deg, transparent 0 calc(25% - 1px), var(--line) calc(25% - 1px) 25%); border-radius: 2px; }
.ui-tl .bar { position: absolute; top: 3px; height: 10px; border-radius: 2px; background: var(--green-dim); min-width: 3px; }
.ui-tl .bar.running { background: var(--green); box-shadow: var(--glow); } .ui-tl .bar.failed { background: var(--err); }
.ui-tl .bar.waiting, .ui-tl .bar.starting { background: var(--warn); } .ui-tl .bar.done, .ui-tl .bar.idle { background: var(--green-dim); }
.ui-tl .now { position: absolute; top: -2px; bottom: -2px; width: 1px; background: var(--green); opacity: .6; }
.ui-tl .axis { grid-column: 2; display: flex; justify-content: space-between; color: var(--faint); font-size: 10.5px; }
.ui-graph { position: relative; display: grid; gap: 48px; align-items: center; padding: 6px 2px; }
.ui-graph .col { display: grid; gap: 10px; position: relative; z-index: 1; min-width: 0; }
.ui-graph svg { position: absolute; inset: 0; width: 100%; height: 100%; pointer-events: none; overflow: visible; }
.ui-gnode { border: 1px solid var(--line); background: var(--bg); border-radius: 5px; padding: 6px 9px; font-size: 12px; min-width: 0; }
.ui-gnode .n { display: flex; gap: 7px; align-items: center; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ui-gnode .s { color: var(--muted); font-size: 11px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ui-gnode.running { border-color: var(--green-dim); }
.ui-glabel { fill: var(--faint); font: 10px var(--mono); }
.ui-field { display: grid; gap: 6px; min-width: 0; }
.ui-field > label { font-size: 12px; color: var(--muted); }
.ui-choice { display: grid; gap: 2px; }
.ui-opt { display: flex; gap: 10px; align-items: baseline; text-align: left; padding: 6px 8px; border-radius: 4px; border: 1px solid transparent; }
.ui-opt:hover:not(:disabled) { background: var(--green-bg); }
.ui-opt .mk { color: var(--faint); flex: none; font-size: 12.5px; }
.ui-opt.on { border-color: var(--green-dim); background: var(--green-bg); } .ui-opt.on .mk { color: var(--green); }
.ui-opt .d { display: block; font-size: 12px; color: var(--muted); }
.ui-opt:disabled { cursor: default; }
.ui-input { border: 1px solid var(--line); border-radius: 4px; padding: 7px 10px; background: var(--bg); }
.ui-input:focus { border-color: var(--green-dim); }
.ui-input:disabled { color: var(--muted); }
textarea.ui-input { resize: vertical; line-height: 1.5; }
select.ui-input { appearance: none; -webkit-appearance: none; color: var(--text); font: inherit; }
.ui-toggle { display: flex; gap: 10px; align-items: center; text-align: left; }
.ui-toggle .sw { font-size: 12px; border: 1px solid var(--line); border-radius: 3px; padding: 0 6px; color: var(--muted); }
.ui-toggle.on .sw { color: var(--green); border-color: var(--green-dim); background: var(--green-bg); }
.ui-btn { border: 1px solid var(--green-dim); color: var(--green); border-radius: 4px; padding: 5px 14px; font-size: 13px; }
.ui-btn:hover:not(:disabled) { background: var(--green-bg); }
.ui-btn.primary { background: var(--green); color: var(--bg); font-weight: 700; box-shadow: var(--glow); }
.ui-btn.danger { border-color: var(--err); color: var(--err); }
.ui-btn:disabled { opacity: .45; box-shadow: none; cursor: default; }
.ui-btns { display: flex; flex-wrap: wrap; gap: 8px; }
.ui-code { border: 1px solid var(--line); border-radius: 5px; background: var(--bg); min-width: 0; overflow: hidden; }
.ui-code-h { display: flex; gap: 10px; align-items: center; padding: 5px 10px; border-bottom: 1px solid var(--line); font-size: 12px; color: var(--muted); }
.ui-code-h b { font-weight: 400; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); }
.ui-code-b { overflow: auto; max-height: 60vh; }
.ui-lines { font: 12px/1.55 var(--mono); border-collapse: collapse; min-width: 100%; }
.ui-lines td { padding: 0 10px; white-space: pre; vertical-align: top; }
.ui-lines td.ln { color: var(--faint); text-align: right; user-select: none; padding-right: 8px; border-right: 1px solid var(--line); width: 1%; }
.ui-lines tr.hl td { background: rgba(229, 192, 123, .08); }
.ui-lines tr.add td.c { background: rgba(62, 230, 122, .09); } .ui-lines tr.add td.sg { color: var(--green); }
.ui-lines tr.del td.c { background: rgba(255, 97, 89, .09); } .ui-lines tr.del td.sg { color: var(--err); }
.ui-lines tr.hunk td { color: #61afef; background: rgba(97, 175, 239, .06); }
.ui-lines td.sg { width: 1%; padding: 0 2px 0 6px; color: var(--faint); user-select: none; }
.ui-lines td.e { background: rgba(255,255,255,.015); }
.tk-c { color: #5c6f5d; font-style: italic; } .tk-s { color: #e5c07b; } .tk-n { color: #d19a66; } .tk-k { color: #56b6c2; } .tk-f { color: #61afef; }
.ui-diff { display: grid; grid-template-columns: minmax(0, 1fr); border: 1px solid var(--line); border-radius: 5px; background: var(--bg); overflow: hidden; min-width: 0; }
.ui-diff-h { display: flex; flex-wrap: wrap; gap: 6px 12px; align-items: center; padding: 6px 10px; border-bottom: 1px solid var(--line); font-size: 12px; color: var(--muted); }
.ui-diff-h b { font-weight: 400; color: var(--text); flex: 1; min-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ui-diff-h .add { color: var(--green); } .ui-diff-h .del { color: var(--err); }
.ui-diff-body { display: grid; grid-template-columns: minmax(0, 1fr); min-height: 0; }
.ui-diff-files { border-bottom: 1px solid var(--line); max-height: 180px; overflow: auto; padding: 4px 0; font-size: 12px; }
.ui-diff-files .dir { color: var(--faint); padding: 3px 10px 1px; }
.ui-diff-files button { display: flex; gap: 8px; width: 100%; text-align: left; padding: 3px 10px; align-items: baseline; }
.ui-diff-files button:hover { background: var(--raise); }
.ui-diff-files button.on { background: var(--green-bg); color: var(--green); }
.ui-diff-files .fn { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ui-diff-files .st { flex: none; font-size: 11px; } .ui-diff-files .st .a { color: var(--green); } .ui-diff-files .st .d { color: var(--err); }
.ui-diff-view { overflow: auto; max-height: 65vh; }
.ui-diff-fh { position: sticky; top: 0; z-index: 1; background: var(--raise); padding: 4px 10px; font-size: 12px; color: var(--text); border-bottom: 1px solid var(--line); }
.ui-diff-fh .tag { color: var(--muted); margin-left: 8px; }
.ui-diff-split { width: 100%; table-layout: fixed; } .ui-diff-split td.c { width: 50%; white-space: pre-wrap; overflow-wrap: anywhere; }
@media (min-width: 900px) {
  .ui-diff-body { grid-template-columns: 230px minmax(0, 1fr); }
  .ui-diff-files { border-bottom: 0; border-right: 1px solid var(--line); max-height: 65vh; }
}
.ui-fs { position: fixed !important; inset: 0; z-index: 45; border-radius: 0 !important; display: flex !important; flex-direction: column; padding-top: env(safe-area-inset-top); }
.ui-fs .ui-code-b, .ui-fs .ui-diff-view, .ui-fs .ui-diff-files { max-height: none !important; }
.ui-fs .ui-diff-body { flex: 1; min-height: 0; } .ui-fs .ui-diff-view, .ui-fs .ui-code-b { flex: 1; min-height: 0; }
@media (min-width: 900px) { .ui-fs .ui-diff-body { grid-template-rows: minmax(0, 1fr); } }
`;
  const style = document.createElement("style");
  style.textContent = css;
  document.head.append(style);

  const PALETTE = ["#3ee67a", "#e5c07b", "#61afef", "#c678dd", "#ff6159", "#56b6c2", "#d19a66"];
  const SVGNS = "http://www.w3.org/2000/svg";

  function h(tag, attrs = {}, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === "class") el.className = v;
      else if (k === "style") el.style.cssText = v;
      else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? "" : v);
    }
    for (const c of kids.flat()) if (c != null && c !== false) el.append(c.nodeType ? c : String(c));
    return el;
  }
  function s(tag, attrs = {}, ...kids) {
    const el = document.createElementNS(SVGNS, tag);
    for (const [k, v] of Object.entries(attrs || {})) if (v != null) el.setAttribute(k, v);
    for (const c of kids.flat()) if (c != null) el.append(c.nodeType ? c : document.createTextNode(String(c)));
    return el;
  }
  const str = (v) => v == null ? "" : typeof v === "object" ? JSON.stringify(v) : String(v);
  const num = (v, d = 0) => { const n = Number(v); return Number.isFinite(n) ? n : d; };
  const fmtNum = (n) => Math.abs(n) >= 1e6 ? (n / 1e6).toFixed(1) + "M" : Math.abs(n) >= 1e4 ? (n / 1e3).toFixed(1) + "k" : Number.isInteger(n) ? String(n) : n.toFixed(Math.abs(n) < 1 ? 2 : 1);
  const safeHref = (u) => typeof u === "string" && (/^#\//.test(u) || /^https?:\/\//.test(u) || /^\/api\/threads\//.test(u)) ? u : null;
  const toTime = (v) => typeof v === "number" ? v : Date.parse(v);
  const fmtTime = (ms, span) => { const d = new Date(ms); return span > 2 * 86400e3 ? d.toLocaleDateString([], { month: "short", day: "numeric" }) : d.toLocaleTimeString([], span < 600e3 ? { minute: "2-digit", second: "2-digit" } : { hour: "2-digit", minute: "2-digit" }); };
  function fullscreen(el, btn) {
    const on = el.classList.toggle("ui-fs");
    btn.textContent = on ? "exit ⤡" : "⤢";
    const esc = (e) => { if (e.key === "Escape") { el.classList.remove("ui-fs"); btn.textContent = "⤢"; removeEventListener("keydown", esc); } };
    if (on) addEventListener("keydown", esc); else removeEventListener("keydown", esc);
  }

  // ---- JSON pointer state ----
  function ptr(path) { return String(path || "").split("/").slice(1).map(p => p.replace(/~1/g, "/").replace(/~0/g, "~")); }
  function getP(obj, path) { let o = obj; for (const k of ptr(path)) { if (o == null) return undefined; o = o[k]; } return o; }
  function setP(obj, path, v) {
    const ks = ptr(path); if (!ks.length) return;
    let o = obj;
    for (const k of ks.slice(0, -1)) { if (o[k] == null || typeof o[k] !== "object") o[k] = {}; o = o[k]; }
    o[ks[ks.length - 1]] = v;
  }

  // ---- syntax highlighting (a small, generic tokenizer) ----
  const KW = new Set(("break case catch class const continue default defer delete do else enum export extends false finally for from func function go goto if import in instanceof interface let map new nil null package private protected public range return select static struct super switch this throw true try type typeof undefined var void while with yield async await def elif except lambda pass raise None True False self fn impl mut pub use mod match trait where loop crate as not and or is end then local require module begin rescue unless until echo fi esac done").split(" "));
  const HASH_COMMENT = /^(py|python|rb|ruby|sh|bash|zsh|shell|yaml|yml|toml|make|makefile|dockerfile|r|pl|perl|conf|ini|nix|cmake)$/i;
  function langOf(name) { const m = /\.([a-z0-9]+)$/i.exec(name || ""); return m ? m[1].toLowerCase() : (/(^|\/)(Makefile|Dockerfile)$/.test(name || "") ? "make" : ""); }
  function highlight(line, lang, state) {
    // state.block: inside a /* */ comment
    const out = [];
    let i = 0, buf = "";
    const flush = () => { if (buf) { out.push(buf); buf = ""; } };
    const push = (cls, text) => { flush(); out.push(h("span", { class: cls }, text)); };
    const hash = HASH_COMMENT.test(lang);
    const plain = /^(md|markdown|txt|text|diff|patch|csv|log)$/i.test(lang);
    if (plain) return [line];
    while (i < line.length) {
      if (state.block) { const e = line.indexOf("*/", i); const end = e < 0 ? line.length : e + 2; push("tk-c", line.slice(i, end)); i = end; if (e >= 0) state.block = false; continue; }
      const rest = line.slice(i), c = line[i];
      if (rest.startsWith("/*") && !hash) { state.block = true; continue; }
      if ((rest.startsWith("//") && !hash) || (c === "#" && hash) || (rest.startsWith("--") && /^(sql|lua|hs|haskell)$/.test(lang))) { push("tk-c", rest); break; }
      if (c === '"' || c === "'" || c === "`") {
        let j = i + 1; while (j < line.length && line[j] !== c) { if (line[j] === "\\") j++; j++; }
        push("tk-s", line.slice(i, j + 1)); i = j + 1; continue;
      }
      const m = /^[A-Za-z_$][\w$]*/.exec(rest);
      if (m) {
        const w = m[0];
        if (KW.has(w)) push("tk-k", w);
        else if (line[i + w.length] === "(") push("tk-f", w);
        else buf += w;
        i += w.length; continue;
      }
      const n = /^\d[\d_.xXa-fA-F]*/.exec(rest);
      if (n && !/[\w$]/.test(line[i - 1] || "")) { push("tk-n", n[0]); i += n[0].length; continue; }
      buf += c; i++;
    }
    flush();
    return out;
  }

  // ---- diff parsing ----
  function parseDiff(text) {
    const files = [];
    let f = null, hunk = null, oldN = 0, newN = 0;
    for (const line of text.split("\n")) {
      if (line.startsWith("diff --git ")) {
        const m = /^diff --git a\/(.*?) b\/(.*)$/.exec(line);
        f = { from: m ? m[1] : "", to: m ? m[2] : line.slice(11), hunks: [], add: 0, del: 0, status: "modified", binary: false };
        files.push(f); hunk = null; continue;
      }
      if (!f) continue;
      if (line.startsWith("new file mode")) { f.status = "added"; continue; }
      if (line.startsWith("deleted file mode")) { f.status = "deleted"; continue; }
      if (line.startsWith("rename from ")) { f.status = "renamed"; f.from = line.slice(12); continue; }
      if (line.startsWith("rename to ")) { f.to = line.slice(10); continue; }
      if (line.startsWith("Binary files")) { f.binary = true; continue; }
      if (line.startsWith("--- ") || line.startsWith("+++ ")) {
        if (!hunk) { const p = line.slice(4).replace(/^[ab]\//, ""); if (line.startsWith("+++ ") && p !== "/dev/null") f.to = p; if (line.startsWith("--- ") && p !== "/dev/null") f.from = p; continue; }
      }
      const hm = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(line);
      if (hm) { oldN = +hm[1]; newN = +hm[2]; hunk = { head: line, lines: [] }; f.hunks.push(hunk); continue; }
      if (!hunk) continue;
      const c = line[0];
      if (c === "+") { hunk.lines.push({ t: "add", n: newN++, text: line.slice(1) }); f.add++; }
      else if (c === "-") { hunk.lines.push({ t: "del", o: oldN++, text: line.slice(1) }); f.del++; }
      else if (c === " " || line === "") { if (line === "" ) continue; hunk.lines.push({ t: "ctx", o: oldN++, n: newN++, text: line.slice(1) }); }
    }
    return files;
  }

  // ---- renderer ----
  // render(spec, opts) returns an element. opts: { locked, onAction(name, params, state), openImage(src) }
  function render(spec, opts = {}) {
    const root = h("div", { class: "ui" });
    if (!spec || typeof spec !== "object" || !spec.elements) { root.append(h("div", { class: "ui-err" }, "bad spec")); return root; }
    const els = spec.elements;
    const state = JSON.parse(JSON.stringify(spec.state || {}));
    const local = new Map(); // per-element UI state (tabs, selected diff file)
    let locked = !!opts.locked;

    const resolve = (v) => {
      if (Array.isArray(v)) return v.map(resolve);
      if (!v || typeof v !== "object") return v;
      if ("$state" in v && Object.keys(v).length === 1) return getP(state, v.$state);
      if ("$bindState" in v) return getP(state, v.$bindState);
      if ("$template" in v) return String(v.$template).replace(/\$\{(\/[^}]*)\}/g, (_, p) => str(getP(state, p)));
      if ("$cond" in v) return cond(v.$cond) ? resolve(v.$then) : resolve(v.$else);
      const o = {}; for (const [k, x] of Object.entries(v)) o[k] = resolve(x); return o;
    };
    const cond = (c) => {
      if (c == null) return true;
      if (typeof c === "boolean") return c;
      if (Array.isArray(c)) return c.every(cond);
      if (typeof c !== "object") return !!c;
      if ("not" in c) return !cond(c.not);
      if ("$state" in c) {
        const v = getP(state, c.$state);
        if ("eq" in c) return JSON.stringify(v) === JSON.stringify(c.eq);
        if ("neq" in c) return JSON.stringify(v) !== JSON.stringify(c.neq);
        return Array.isArray(v) ? v.length > 0 : !!v;
      }
      return true;
    };
    function act(a) {
      if (!a) return;
      if (Array.isArray(a)) { a.forEach(act); return; }
      if (a.action === "setState") { const p = a.params || {}; setP(state, p.statePath, resolve(p.value)); rerender(); return; }
      if (locked || !opts.onAction) return;
      opts.onAction(a.action || "submit", resolve(a.params || null), JSON.parse(JSON.stringify(state)));
    }
    const bindPath = (el) => el?.props?.value && typeof el.props.value === "object" && el.props.value.$bindState;
    function setBound(el, v) {
      const p = bindPath(el); if (p) setP(state, p, v);
      if (el.on?.change) act(el.on.change);
    }

    const C = {
      Card: (p, kids) => h("div", { class: "ui-card " + (p.tone || "") },
        p.title || p.subtitle ? h("div", { class: "ui-card-h" }, p.title ? h("div", { class: "ui-card-t" }, str(p.title)) : null, p.subtitle ? h("div", { class: "ui-card-s" }, str(p.subtitle)) : null) : null, kids()),
      Stack: (p, kids) => h("div", { class: "ui-stack" + (p.direction === "horizontal" ? " row" : "") + (p.wrap ? " wrap" : ""),
        style: `gap:${Math.min(6, num(p.gap, 2)) * 4 + 2}px;${p.align ? "align-items:" + ({ start: "flex-start", end: "flex-end", center: "center", stretch: "stretch" }[p.align] || "normal") : ""}` }, kids()),
      Grid: (p, kids) => {
        const cols = Math.max(1, Math.min(6, num(p.columns, 2))), mw = num(p.minWidth, 150);
        return h("div", { class: "ui-grid", style: `gap:12px;grid-template-columns:repeat(auto-fit,minmax(max(${mw}px,calc((100% - ${(cols - 1) * 12}px)/${cols})),1fr))` }, kids());
      },
      Tabs: (p, kids, id) => {
        const tabs = (p.tabs || []).map(str), sel = local.get(id) || 0, all = kids();
        return h("div", {}, h("div", { class: "ui-tabs-bar" }, tabs.map((t, i) => h("button", { class: i === sel ? "on" : "", onclick: () => { local.set(id, i); rerender(); } }, t))), all[sel] || null);
      },
      Heading: (p) => h("div", { class: "ui-h" + Math.max(1, Math.min(3, num(p.level, 2))) }, str(p.text)),
      Text: (p) => {
        const cls = "ui-text" + (p.tone ? " tone-" + p.tone : "") + (p.mono ? " mono" : "");
        return p.mono || typeof window.md !== "function" ? h("div", { class: cls }, str(p.text)) : h("div", { class: cls + " text" }, window.md(str(p.text)));
      },
      Badge: (p) => h("span", { class: "ui-badge " + (p.tone || "") }, str(p.text)),
      Divider: () => h("hr", { class: "ui-hr" }),
      Stat: (p) => {
        const tr = p.trend != null ? num(p.trend) : null;
        return h("div", { class: "ui-stat" }, h("div", { class: "k" }, str(p.label)), h("div", { class: "v " + (p.tone || "") }, str(p.value)),
          p.sub || tr != null ? h("div", { class: "s" }, tr != null ? h("span", { class: tr >= 0 ? "up" : "down" }, (tr >= 0 ? "▲ " : "▼ ") + fmtNum(Math.abs(tr)) + " ") : null, str(p.sub || "")) : null);
      },
      Meter: (p) => {
        const max = p.max != null ? num(p.max, 1) : (num(p.value) > 1 ? 100 : 1), frac = Math.max(0, Math.min(1, num(p.value) / (max || 1)));
        return h("div", { class: "ui-meter" }, h("div", { class: "k" }, h("span", {}, str(p.label)), h("span", {}, Math.round(frac * 100) + "%")),
          h("div", { class: "bar" }, h("i", { style: `width:${(frac * 100).toFixed(1)}%` })), p.sub ? h("div", { class: "s" }, str(p.sub)) : null);
      },
      KeyValue: (p) => h("dl", { class: "ui-kv" }, (p.items || []).flatMap(it => [h("dt", {}, str(it.key ?? it.k)), h("dd", {}, str(it.value ?? it.v))])),
      Table: (p, _k, id) => {
        const cols = (p.columns || []).map(c => typeof c === "string" ? { key: c, label: c } : c);
        let rows = p.rows || [];
        const max = num(p.maxRows, 50), showAll = local.get(id);
        const more = rows.length > max && !showAll ? rows.length - max : 0;
        if (more) rows = rows.slice(0, max);
        const cell = (r, c, i) => Array.isArray(r) ? r[i] : r?.[c.key];
        return h("div", { class: "ui-table-wrap" }, h("table", { class: "ui-table" },
          h("thead", {}, h("tr", {}, cols.map(c => h("th", { class: c.align === "right" ? "r" : "" }, str(c.label ?? c.key))))),
          h("tbody", {}, rows.map(r => h("tr", {}, cols.map((c, i) => h("td", { class: (c.align === "right" ? "r " : "") + (c.mono ? "m" : "") }, str(cell(r, c, i)))))))),
          more ? h("button", { class: "ui-mini", onclick: () => { local.set(id, true); rerender(); } }, `show ${more} more rows`) : null);
      },
      List: (p) => h("div", { class: "ui-list" }, (p.items || []).map(it => {
        if (typeof it !== "object") it = { title: str(it) };
        const href = safeHref(it.href);
        return h(href ? "a" : "div", { class: "ui-li", href, target: href && /^https?:/.test(href) ? "_blank" : null, rel: "noopener" },
          it.status ? h("span", { class: "dot " + ({ done: "idle", waiting: "starting" }[it.status] || it.status) }) : null,
          h("div", { class: "t" }, h("div", {}, str(it.title)), it.sub ? h("div", { class: "s" }, str(it.sub)) : null),
          it.badge ? h("span", { class: "ui-badge" }, str(it.badge)) : null);
      })),
      Timeline: (p) => {
        const items = (p.items || []).map(it => ({ ...it, s: toTime(it.start), e: it.end != null ? toTime(it.end) : Date.now() })).filter(it => Number.isFinite(it.s));
        if (!items.length) return h("div", { class: "tone-faint" }, "no items");
        const from = p.from != null ? toTime(p.from) : Math.min(...items.map(i => i.s));
        let to = p.to != null ? toTime(p.to) : Math.max(...items.map(i => i.e));
        if (to <= from) to = from + 60e3;
        const span = to - from, pct = (t) => Math.max(0, Math.min(100, (t - from) / span * 100));
        const lanes = [...new Set(items.map(i => str(i.lane ?? i.label)))];
        const now = Date.now();
        const grid = h("div", { class: "ui-tl" });
        for (const lane of lanes) {
          const track = h("div", { class: "track" });
          for (const it of items.filter(i => str(i.lane ?? i.label) === lane)) {
            const l = pct(it.s), w = Math.max(0.6, pct(it.e) - l);
            track.append(h("div", { class: "bar " + (it.status || ""), style: `left:${l}%;width:${w}%`, title: `${str(it.label)} · ${fmtTime(it.s, span)}–${fmtTime(it.e, span)}` }));
          }
          if (now > from && now < to) track.append(h("div", { class: "now", style: `left:${pct(now)}%` }));
          grid.append(h("div", { class: "lane", title: lane }, lane), track);
        }
        grid.append(h("div", { class: "axis" }, [0, .25, .5, .75, 1].map(f => h("span", {}, fmtTime(from + span * f, span)))));
        return grid;
      },
      Chart: (p) => chart(p),
      Graph: (p) => graph(p),
      Choice: (p, _k, id, el) => {
        const multiple = !!p.multiple, val = p.value;
        const opts = (p.options || []).map(o => typeof o === "object" ? o : { value: o, label: str(o) });
        const isOn = (v) => multiple ? Array.isArray(val) && val.some(x => JSON.stringify(x) === JSON.stringify(v)) : JSON.stringify(val) === JSON.stringify(v);
        return field(p.label, h("div", { class: "ui-choice" }, opts.map(o => h("button", {
          type: "button", class: "ui-opt" + (isOn(o.value) ? " on" : ""), disabled: locked,
          onclick: () => {
            let nv = o.value;
            if (multiple) { const cur = Array.isArray(val) ? val.slice() : []; const i = cur.findIndex(x => JSON.stringify(x) === JSON.stringify(o.value)); if (i >= 0) cur.splice(i, 1); else cur.push(o.value); nv = cur; }
            setBound(el, nv); rerender();
          } },
          h("span", { class: "mk" }, multiple ? (isOn(o.value) ? "[x]" : "[ ]") : (isOn(o.value) ? "(•)" : "( )")),
          h("span", {}, str(o.label ?? o.value), o.description ? h("span", { class: "d" }, str(o.description)) : null)))));
      },
      Select: (p, _k, id, el) => {
        const opts = (p.options || []).map(o => typeof o === "object" ? o : { value: o, label: str(o) });
        const sel = h("select", { class: "ui-input", disabled: locked, "data-eid": id, onchange: () => { const o = opts[sel.selectedIndex - 1]; setBound(el, o ? o.value : null); rerender(); } },
          h("option", { value: "" }, str(p.placeholder || "choose…")), opts.map((o, i) => h("option", { value: String(i) }, str(o.label ?? o.value))));
        sel.selectedIndex = opts.findIndex(o => JSON.stringify(o.value) === JSON.stringify(p.value)) + 1;
        return field(p.label, sel);
      },
      TextInput: (p, _k, id, el) => {
        const inp = p.multiline ? h("textarea", { class: "ui-input", rows: num(p.rows, 4) }) : h("input", { class: "ui-input" });
        inp.value = str(p.value ?? "");
        inp.placeholder = str(p.placeholder || "");
        inp.disabled = locked;
        inp.dataset.eid = id;
        inp.addEventListener("input", () => setBound(el, inp.value));
        inp.addEventListener("change", () => rerender());
        return field(p.label, inp);
      },
      Toggle: (p, _k, id, el) => h("button", { type: "button", class: "ui-toggle" + (p.value ? " on" : ""), disabled: locked, onclick: () => { setBound(el, !p.value); rerender(); } },
        h("span", { class: "sw" }, p.value ? "on " : "off"), h("span", {}, str(p.label || ""))),
      Button: (p, _k, id, el) => h("button", { type: "button", class: "ui-btn " + (p.variant || ""), disabled: locked && !(el.on?.press?.action === "setState"),
        onclick: () => act(el.on?.press || (p.action ? { action: p.action, params: p.params } : { action: "submit" })) }, str(p.label || "ok")),
      Buttons: (p, kids) => h("div", { class: "ui-btns" }, kids()),
      Code: (p) => code(p),
      Diff: (p, _k, id) => diff(p, id),
      Image: (p) => {
        const src = typeof p.src === "string" && (/^\/api\/threads\//.test(p.src) || /^data:image\//.test(p.src) || /^https:\/\//.test(p.src)) ? p.src : null;
        if (!src) return h("div", { class: "ui-err" }, "image src must be a shared file URL");
        const img = h("img", { src, alt: str(p.alt || p.caption || ""), loading: "lazy", style: "max-width:100%;max-height:60vh;border:1px solid var(--line);border-radius:4px;cursor:zoom-in;display:block" });
        img.addEventListener("click", () => (opts.openImage || window.openImage)?.(src));
        return h("figure", { style: "margin:0;display:grid;gap:4px" }, img, p.caption ? h("figcaption", { class: "tone-muted", style: "font-size:12px" }, str(p.caption)) : null);
      },
    };
    const field = (label, control) => label ? h("div", { class: "ui-field" }, h("label", {}, str(label)), control) : control;

    function chart(p) {
      const kind = p.kind || "line", series = (p.series || []).map((se, i) => ({ name: str(se.name || ""), data: (se.data || []).map(d => num(typeof d === "object" ? d.y : d)), color: se.color && /^#[0-9a-f]{3,8}$/i.test(se.color) ? se.color : PALETTE[i % PALETTE.length] }));
      const n = Math.max(0, ...series.map(s => s.data.length));
      if (!n) return h("div", { class: "tone-faint" }, "no data");
      const spark = kind === "sparkline", W = 600, H = spark ? 40 : Math.max(80, Math.min(400, num(p.height, 180)));
      const pad = spark ? { l: 0, r: 0, t: 2, b: 2 } : { l: 40, r: 8, t: 8, b: 22 };
      const stacked = kind === "stackedBar";
      let max = p.max != null ? num(p.max) : 0;
      if (p.max == null) for (let i = 0; i < n; i++) max = Math.max(max, stacked ? series.reduce((a, s) => a + (s.data[i] || 0), 0) : Math.max(...series.map(s => s.data[i] || 0)));
      const min = 0; if (max <= min) max = min + 1;
      const iw = W - pad.l - pad.r, ih = H - pad.t - pad.b;
      const isBar = kind === "bar" || stacked;
      const x = (i) => pad.l + (isBar ? (i + .5) * iw / n : n === 1 ? iw / 2 : i * iw / (n - 1));
      const y = (v) => pad.t + ih - (v - min) / (max - min) * ih;
      const svg = s("svg", { class: "ui-svg", viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", style: `height:${H}px` });
      if (!spark) {
        for (let g = 0; g <= 4; g++) {
          const v = min + (max - min) * g / 4, yy = y(v);
          svg.append(s("line", { class: "grid", x1: pad.l, x2: W - pad.r, y1: yy, y2: yy }), s("text", { x: pad.l - 6, y: yy + 3, "text-anchor": "end" }, fmtNum(v) + (p.yUnit || "")));
        }
        const labels = p.labels || [];
        const step = Math.max(1, Math.ceil(n / 8));
        for (let i = 0; i < n; i += step) if (labels[i] != null) svg.append(s("text", { x: x(i), y: H - 6, "text-anchor": "middle" }, str(labels[i])));
      }
      if (kind === "bar" || stacked) {
        const bw = iw / n * (stacked ? .7 : .8) / (stacked ? 1 : series.length);
        for (let i = 0; i < n; i++) {
          let base = 0;
          series.forEach((se, k) => {
            const v = se.data[i] || 0;
            const bx = stacked ? x(i) - bw / 2 : x(i) - bw * series.length / 2 + k * bw;
            const top = y(stacked ? base + v : v), bottom = y(stacked ? base : 0);
            svg.append(s("rect", { x: bx, y: top, width: Math.max(1, bw - 1), height: Math.max(0, bottom - top), fill: se.color, opacity: .85 }, s("title", {}, `${se.name ? se.name + ": " : ""}${p.labels?.[i] != null ? str(p.labels[i]) + " · " : ""}${fmtNum(v)}`)));
            base += v;
          });
        }
      } else {
        series.forEach((se) => {
          const pts = se.data.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
          if (kind === "area" || spark) svg.append(s("polygon", { points: `${x(0)},${y(min)} ${pts} ${x(se.data.length - 1)},${y(min)}`, fill: se.color, opacity: .12 }));
          svg.append(s("polyline", { points: pts, fill: "none", stroke: se.color, "stroke-width": spark ? 1.5 : 1.8, "vector-effect": "non-scaling-stroke", "stroke-linejoin": "round" }));
          if (!spark && n <= 60) se.data.forEach((v, i) => svg.append(s("circle", { cx: x(i), cy: y(v), r: 2.2, fill: se.color, "vector-effect": "non-scaling-stroke" }, s("title", {}, `${se.name ? se.name + ": " : ""}${p.labels?.[i] != null ? str(p.labels[i]) + " · " : ""}${fmtNum(v)}`))));
        });
      }
      const wrap = h("div", {}, p.yLabel ? h("div", { class: "tone-faint", style: "font-size:11px;margin-bottom:2px" }, str(p.yLabel)) : null, svg);
      if (series.length > 1 || (series[0]?.name && !spark)) wrap.append(h("div", { class: "ui-legend" }, series.map(se => h("span", {}, h("i", { style: `background:${se.color}` }), se.name))));
      if (p.xLabel) wrap.append(h("div", { class: "tone-faint", style: "font-size:11px;text-align:center" }, str(p.xLabel)));
      return wrap;
    }

    function graph(p) {
      const nodes = (p.nodes || []).filter(n => n && n.id != null), edges = (p.edges || []).filter(e => e && e.from != null && e.to != null);
      const col = new Map();
      for (const n of nodes) if (n.col != null) col.set(str(n.id), num(n.col));
      // columns from edges: longest path from a node with no incoming edges
      const ids = nodes.map(n => str(n.id));
      for (let pass = 0; pass < ids.length; pass++) {
        let changed = false;
        for (const id of ids) if (!col.has(id) && !edges.some(e => str(e.to) === id)) { col.set(id, 0); changed = true; }
        for (const e of edges) { const f = col.get(str(e.from)); if (f != null && nodes.find(n => str(n.id) === str(e.to))?.col == null && (col.get(str(e.to)) ?? -1) < f + 1 && f + 1 < ids.length) { col.set(str(e.to), f + 1); changed = true; } }
        if (!changed) break;
      }
      for (const id of ids) if (!col.has(id)) col.set(id, 0);
      const ncols = Math.max(...[...col.values()], 0) + 1;
      const wrap = h("div", { class: "ui-graph", style: `grid-template-columns:repeat(${ncols},minmax(0,1fr))` });
      const svgEl = s("svg");
      wrap.append(svgEl);
      const boxes = new Map();
      for (let c = 0; c < ncols; c++) {
        const colEl = h("div", { class: "col" });
        for (const n of nodes.filter(n => col.get(str(n.id)) === c)) {
          const b = h("div", { class: "ui-gnode " + (n.status || "") }, h("div", { class: "n" }, n.status ? h("span", { class: "dot " + ({ done: "idle", waiting: "starting" }[n.status] || n.status) }) : null, h("span", {}, str(n.label ?? n.id))), n.sub ? h("div", { class: "s" }, str(n.sub)) : null);
          boxes.set(str(n.id), b); colEl.append(b);
        }
        wrap.append(colEl);
      }
      const draw = () => {
        if (!wrap.isConnected) return;
        const wr = wrap.getBoundingClientRect();
        svgEl.replaceChildren();
        for (const e of edges) {
          const a = boxes.get(str(e.from)), b = boxes.get(str(e.to));
          if (!a || !b) continue;
          const ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect();
          let x1 = ra.right - wr.left, y1 = ra.top + ra.height / 2 - wr.top, x2 = rb.left - wr.left, y2 = rb.top + rb.height / 2 - wr.top;
          if (rb.left < ra.right) { x1 = ra.left + ra.width / 2 - wr.left; y1 = ra.bottom - wr.top; x2 = rb.left + rb.width / 2 - wr.left; y2 = rb.top - wr.top; }
          const dx = Math.max(24, Math.abs(x2 - x1) / 2);
          const d = rb.left < ra.right ? `M${x1},${y1} C${x1},${y1 + 20} ${x2},${y2 - 20} ${x2},${y2}` : `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`;
          svgEl.append(s("path", { d, class: "ln" + (e.live ? " live" : "") + (e.dashed ? " dash" : "") }));
          if (e.label) svgEl.append(s("text", { x: (x1 + x2) / 2, y: (y1 + y2) / 2 - 4, "text-anchor": "middle", class: "ui-glabel" }, str(e.label)));
        }
      };
      requestAnimationFrame(() => requestAnimationFrame(draw));
      if (window.ResizeObserver) new ResizeObserver(draw).observe(wrap);
      return wrap;
    }

    function code(p) {
      const lang = str(p.language || langOf(p.filename)).toLowerCase(), hl = new Set((p.highlight || []).map(Number)), start = num(p.startLine, 1);
      const st = {};
      const tbl = h("table", { class: "ui-lines" }, str(p.code).replace(/\n$/, "").split("\n").map((l, i) =>
        h("tr", { class: hl.has(start + i) ? "hl" : "" }, h("td", { class: "ln" }, start + i), h("td", { class: "c" }, highlight(l, lang, st)))));
      const box = h("div", { class: "ui-code" });
      const fs = h("button", { class: "ui-mini", title: "full screen", onclick: () => fullscreen(box, fs) }, "⤢");
      const copy = h("button", { class: "ui-mini", onclick: () => { navigator.clipboard?.writeText(str(p.code)); copy.textContent = "copied"; setTimeout(() => copy.textContent = "copy", 1200); } }, "copy");
      box.append(h("div", { class: "ui-code-h" }, h("b", {}, str(p.filename || lang || "code")), copy, fs), h("div", { class: "ui-code-b" }, tbl));
      return box;
    }

    function diff(p, id) {
      const box = h("div", { class: "ui-diff" });
      const body = h("div", { class: "ui-diff-body" });
      const head = h("div", { class: "ui-diff-h" }, h("b", {}, str(p.title || "diff")), h("span", {}, "loading…"));
      box.append(head, body);
      const show = (text) => {
        const files = parseDiff(text);
        if (!files.length) { head.lastChild.textContent = "no changes"; return; }
        const st = local.get(id) || { sel: 0, view: p.view === "split" ? "split" : "unified" };
        local.set(id, st);
        const add = files.reduce((a, f) => a + f.add, 0), del = files.reduce((a, f) => a + f.del, 0);
        const viewBtn = h("button", { class: "ui-mini", onclick: () => { st.view = st.view === "split" ? "unified" : "split"; draw(); viewBtn.textContent = st.view === "split" ? "unified" : "split"; } }, st.view === "split" ? "unified" : "split");
        const fs = h("button", { class: "ui-mini", title: "full screen", onclick: () => fullscreen(box, fs) }, "⤢");
        head.replaceChildren(h("b", {}, str(p.title || "diff")), h("span", {}, `${files.length} file${files.length === 1 ? "" : "s"} `, h("span", { class: "add" }, "+" + add), " ", h("span", { class: "del" }, "−" + del)), viewBtn, fs);
        const list = h("div", { class: "ui-diff-files" }), view = h("div", { class: "ui-diff-view" });
        body.replaceChildren(list, view);
        const name = (f) => f.status === "deleted" ? f.from : f.to;
        function drawList() {
          list.replaceChildren();
          let lastDir = null;
          files.map((f, i) => ({ f, i, n: name(f) })).sort((a, b) => a.n.localeCompare(b.n)).forEach(({ f, i, n }) => {
            const dir = n.includes("/") ? n.slice(0, n.lastIndexOf("/")) : "";
            if (dir !== lastDir) { if (dir) list.append(h("div", { class: "dir" }, dir + "/")); lastDir = dir; }
            list.append(h("button", { class: i === st.sel ? "on" : "", style: dir ? "padding-left:20px" : "", onclick: () => { st.sel = i; drawList(); draw(); view.scrollTop = 0; } },
              h("span", { class: "fn", title: n }, n.slice(dir ? dir.length + 1 : 0)),
              h("span", { class: "st" }, f.status === "added" ? "new " : f.status === "deleted" ? "del " : f.status === "renamed" ? "mv " : "", h("span", { class: "a" }, "+" + f.add), " ", h("span", { class: "d" }, "−" + f.del))));
          });
        }
        function draw() {
          const f = files[st.sel], lang = langOf(name(f));
          const fh = h("div", { class: "ui-diff-fh" }, name(f), f.status !== "modified" ? h("span", { class: "tag" }, f.status === "renamed" ? "renamed from " + f.from : f.status) : null);
          if (f.binary) { view.replaceChildren(fh, h("div", { class: "tone-faint", style: "padding:10px" }, "binary file")); return; }
          const sOld = {}, sNew = {};
          const rows = [];
          let count = 0;
          const LIMIT = 4000;
          for (const hk of f.hunks) {
            if (count > LIMIT) break;
            rows.push(st.view === "split"
              ? h("tr", { class: "hunk" }, h("td", { class: "ln" }), h("td", { class: "c", colspan: 3 }, hk.head))
              : h("tr", { class: "hunk" }, h("td", { class: "ln" }), h("td", { class: "ln" }), h("td", { class: "sg" }), h("td", { class: "c" }, hk.head)));
            if (st.view === "split") {
              const L = hk.lines;
              for (let i = 0; i < L.length;) {
                if (L[i].t === "ctx") { const l = L[i++]; rows.push(h("tr", {}, h("td", { class: "ln" }, l.o), h("td", { class: "c" }, highlight(l.text, lang, sOld)), h("td", { class: "ln" }, l.n), h("td", { class: "c" }, highlight(l.text, lang, sNew)))); count++; continue; }
                const dels = [], adds = [];
                while (i < L.length && L[i].t === "del") dels.push(L[i++]);
                while (i < L.length && L[i].t === "add") adds.push(L[i++]);
                for (let k = 0; k < Math.max(dels.length, adds.length); k++) {
                  const d = dels[k], a = adds[k];
                  const tr = h("tr", {});
                  tr.append(h("td", { class: "ln" }, d ? d.o : ""), h("td", { class: "c " + (d ? "" : "e"), style: d ? "background:rgba(255,97,89,.09)" : "" }, d ? highlight(d.text, lang, sOld) : ""),
                    h("td", { class: "ln" }, a ? a.n : ""), h("td", { class: "c " + (a ? "" : "e"), style: a ? "background:rgba(62,230,122,.09)" : "" }, a ? highlight(a.text, lang, sNew) : ""));
                  rows.push(tr); count++;
                }
              }
            } else {
              for (const l of hk.lines) {
                rows.push(h("tr", { class: l.t }, h("td", { class: "ln" }, l.t === "add" ? "" : l.o), h("td", { class: "ln" }, l.t === "del" ? "" : l.n),
                  h("td", { class: "sg" }, l.t === "add" ? "+" : l.t === "del" ? "−" : " "), h("td", { class: "c" }, highlight(l.text, lang, l.t === "del" ? sOld : sNew))));
                count++;
              }
            }
          }
          view.replaceChildren(fh, h("table", { class: "ui-lines" + (st.view === "split" ? " ui-diff-split" : "") }, rows));
          if (count > LIMIT) view.append(h("div", { class: "tone-faint", style: "padding:8px 10px" }, "diff truncated; open the raw file for the rest"));
        }
        drawList(); draw();
      };
      if (typeof p.patch === "string") setTimeout(() => show(p.patch));
      else if (typeof p.src === "string" && /^\/api\/threads\//.test(p.src)) {
        fetch(p.src).then(r => r.ok ? r.text() : Promise.reject(new Error(r.statusText))).then(show)
          .catch(err => { head.lastChild.textContent = "couldn't load: " + err.message; });
      } else head.lastChild.textContent = "Diff needs patch or src";
      return box;
    }

    // Elements whose subtree holds a Diff or Graph keep their DOM across
    // re-renders (they load data and measure layout), keyed by element id.
    const keep = new Map();
    function node(id, depth = 0) {
      const el = els[id];
      if (!el || depth > 40) return h("div", { class: "ui-err" }, "missing element " + id);
      if (el.visible != null && !cond(el.visible)) return null;
      if ((el.type === "Diff" || el.type === "Graph" || el.type === "Code") && keep.has(id)) return keep.get(id);
      const comp = C[el.type];
      if (!comp) return h("div", { class: "ui-err" }, "unknown component " + el.type);
      const props = resolve(el.props || {});
      const kids = () => (el.children || []).map(k => node(k, depth + 1)).filter(Boolean);
      try {
        const out = comp(props, kids, id, el);
        if (el.type === "Diff" || el.type === "Graph" || el.type === "Code") keep.set(id, out);
        return out;
      } catch (err) { return h("div", { class: "ui-err" }, `${el.type}: ${err.message}`); }
    }
    function rerender() {
      const active = document.activeElement, eid = active?.dataset?.eid, sel = active && "selectionStart" in active ? [active.selectionStart, active.selectionEnd] : null;
      root.replaceChildren(node(spec.root) || h("div"));
      if (eid) { const again = root.querySelector(`[data-eid="${CSS.escape(eid)}"]`); if (again) { again.focus(); if (sel && again.setSelectionRange) try { again.setSelectionRange(...sel); } catch {} } }
    }
    rerender();
    root.lock = () => { locked = true; rerender(); };
    return root;
  }

  window.ruxUI = { render, parseDiff };
})();
