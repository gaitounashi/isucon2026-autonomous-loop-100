"""
dashboard_gen.py - 実験履歴 (experiments.json / 旧 autopilot_history.json) から dashboard.html を生成

外部 CDN に依存しない単一 HTML。データは JSON として埋め込み、描画は素の JS + SVG。
    python harness/dashboard_gen.py
"""

from __future__ import annotations

import json
import subprocess
from datetime import datetime
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
HARNESS_DIR = REPO_ROOT / "harness"
EXPERIMENTS_FILE = HARNESS_DIR / "experiments.json"
LEGACY_FILE = HARNESS_DIR / "autopilot_history.json"
OUT_FILE = REPO_ROOT / "dashboard.html"


def _load(path: Path) -> list:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return []


def _repo_web_url() -> str:
    try:
        url = subprocess.run(["git", "remote", "get-url", "origin"], cwd=REPO_ROOT,
                             capture_output=True, text=True).stdout.strip()
        if url.endswith(".git"):
            url = url[:-4]
        if url.startswith("git@github.com:"):
            url = "https://github.com/" + url.split(":", 1)[1]
        return url
    except Exception:
        return ""


def _accepted_commits() -> list[dict]:
    """git log から採択コミット (perf:) を抽出 -> 改修の系譜"""
    try:
        out = subprocess.run(["git", "log", "--pretty=format:%h|%ad|%s", "--date=format:%m/%d %H:%M"],
                             cwd=REPO_ROOT, capture_output=True, text=True, encoding="utf-8").stdout
    except Exception:
        return []
    rows = []
    for line in out.splitlines():
        parts = line.split("|", 2)
        if len(parts) == 3 and parts[2].startswith("perf:"):
            rows.append({"hash": parts[0], "date": parts[1], "subject": parts[2]})
    return rows


def generate(out_path: Path = OUT_FILE) -> Path:
    data = {
        "generated_at": datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
        "repo_url": _repo_web_url(),
        "experiments": _load(EXPERIMENTS_FILE),
        "legacy": [{"score": e.get("score", 0), "accepted": bool(e.get("accepted")),
                    "desc": e.get("desc", ""), "ts": e.get("timestamp", 0)} for e in _load(LEGACY_FILE)],
        "commits": _accepted_commits(),
    }
    html = TEMPLATE.replace("/*__DATA__*/null", json.dumps(data, ensure_ascii=False).replace("</", "<\\/"))
    out_path.write_text(html, encoding="utf-8")
    return out_path


TEMPLATE = r"""<!DOCTYPE html>
<html lang="ja">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>ISUCON2026 自律改善ループ ダッシュボード</title>
<style>
  :root { --bg:#f6f8fb; --card:#fff; --ink:#1f2937; --mute:#6b7280; --line:#e5e7eb;
          --acc:#16a34a; --rej:#f59e0b; --fail:#dc2626; --base:#94a3b8; --brand:#0369a1; }
  * { box-sizing:border-box; }
  body { margin:0; background:var(--bg); color:var(--ink);
         font-family:-apple-system,"Segoe UI","Hiragino Sans","Meiryo",sans-serif; font-size:14px; }
  header { background:var(--brand); color:#fff; padding:14px 24px; display:flex; align-items:baseline; gap:16px; flex-wrap:wrap; }
  header h1 { font-size:18px; margin:0; }
  header .sub { opacity:.85; font-size:12px; }
  main { max-width:1280px; margin:0 auto; padding:20px 24px 60px; }
  .grid { display:grid; gap:14px; }
  .kpis { grid-template-columns:repeat(auto-fit,minmax(170px,1fr)); }
  .card { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:14px 16px; }
  .kpi .label { color:var(--mute); font-size:12px; }
  .kpi .value { font-size:24px; font-weight:700; margin-top:4px; }
  .kpi .note { color:var(--mute); font-size:11px; margin-top:2px; }
  h2 { font-size:15px; margin:26px 0 10px; }
  .hint { color:var(--mute); font-size:12px; margin:-4px 0 10px; line-height:1.6; }
  svg { width:100%; display:block; }
  .legend { display:flex; gap:14px; flex-wrap:wrap; font-size:12px; color:var(--mute); margin-top:6px; }
  .legend i { display:inline-block; width:10px; height:10px; border-radius:50%; margin-right:4px; vertical-align:-1px; }
  table { width:100%; border-collapse:collapse; font-size:12.5px; }
  th, td { padding:7px 8px; border-bottom:1px solid var(--line); text-align:left; vertical-align:top; }
  th { background:#f1f5f9; position:sticky; top:0; cursor:pointer; user-select:none; white-space:nowrap; }
  td.num { text-align:right; font-variant-numeric:tabular-nums; white-space:nowrap; }
  .badge { display:inline-block; padding:1px 8px; border-radius:999px; font-size:11px; font-weight:600; color:#fff; white-space:nowrap; }
  .ACCEPTED { background:var(--acc); } .REJECTED { background:var(--rej); }
  .BUILD_FAIL, .BENCH_FAIL, .PATCH_FAIL, .FUNC_FAIL { background:var(--fail); }
  .pos { color:var(--acc); font-weight:600; } .neg { color:var(--fail); }
  .controls { display:flex; gap:8px; flex-wrap:wrap; margin-bottom:10px; }
  .controls select, .controls input { padding:5px 8px; border:1px solid var(--line); border-radius:6px; font-size:12.5px; }
  .tablewrap { max-height:560px; overflow:auto; border:1px solid var(--line); border-radius:8px; }
  details summary { cursor:pointer; color:var(--brand); }
  pre { white-space:pre-wrap; font-size:11px; background:#f8fafc; padding:6px; border-radius:6px; max-height:200px; overflow:auto; margin:4px 0 0; }
  a { color:var(--brand); }
  .warn { background:#fff7ed; border:1px solid #fed7aa; color:#9a3412; padding:10px 14px; border-radius:8px; font-size:12.5px; line-height:1.7; }
  .empty { color:var(--mute); padding:24px; text-align:center; }
  .tip { position:fixed; pointer-events:none; background:rgba(15,23,42,.94); color:#fff; padding:8px 10px;
         border-radius:6px; font-size:12px; line-height:1.5; display:none; max-width:360px; z-index:10; }
</style>
</head>
<body>
<header>
  <h1>ISUCON2026 自律改善ループ ダッシュボード</h1>
  <span class="sub" id="genAt"></span>
</header>
<main>
  <div class="grid kpis" id="kpis"></div>

  <h2>A/B 交互測定の結果（新方式）</h2>
  <p class="hint">各試行で HEAD（灰色）と候補パッチ（緑＝採択／橙＝不採択）を交互に測り、中央値を表示しています。
    縦線が同じ時間帯での差分です。点の上下の揺れはマシン状態の変化で、判定には使いません。赤い×はビルド・機能テストの失敗です。</p>
  <div class="card"><svg id="abChart" height="300"></svg>
    <div class="legend"><span><i style="background:var(--base)"></i>HEAD 中央値</span>
      <span><i style="background:var(--acc)"></i>候補（採択）</span><span><i style="background:var(--rej)"></i>候補（不採択）</span>
      <span><i style="background:var(--fail)"></i>失敗</span></div></div>

  <h2>改善率（候補 ÷ HEAD − 1）</h2>
  <p class="hint">破線が採択しきい値です。採択には「中央値で しきい値 以上の改善」と「交互ペアの過半数で勝つこと」の両方が必要です。</p>
  <div class="card"><svg id="gainChart" height="220"></svg></div>

  <h2>累積改善（採択パッチの積み上げ）</h2>
  <div class="card"><svg id="cumChart" height="200"></svg></div>

  <h2>診断別の集計</h2>
  <div class="card tablewrap" style="max-height:none"><table id="diagTable"></table></div>

  <h2>全試行の履歴</h2>
  <div class="controls">
    <select id="fStatus"><option value="">全ステータス</option></select>
    <select id="fDiag"><option value="">全診断</option></select>
    <input id="fText" placeholder="キーワード検索">
  </div>
  <div class="card tablewrap" style="padding:0"><table id="expTable"></table></div>

  <h2>採択コミットの系譜（git log の perf: コミット）</h2>
  <div class="card tablewrap" style="max-height:340px"><table id="commitTable"></table></div>

  <h2>旧方式の履歴（固定ベースラインとの絶対値比較）</h2>
  <div class="warn">旧方式では過去の最高スコア（455,485点）を固定の基準にしていました。ところが<b>同じコードでも時間帯によって23万〜45万点まで変動</b>するため、
    判定はほぼノイズで決まっていました。参考として残していますが、ここに出る「不採択」はパッチの良し悪しを表しません。</div>
  <div class="card" style="margin-top:10px"><svg id="legacyChart" height="220"></svg></div>
</main>
<div class="tip" id="tip"></div>

<script>
const D = /*__DATA__*/null;
const E = D.experiments || [];
const fmt = n => (n == null || isNaN(n)) ? "-" : Math.round(n).toLocaleString("ja-JP");
const esc = s => String(s ?? "").replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]));
const tip = document.getElementById("tip");
function showTip(ev, html){ tip.innerHTML = html; tip.style.display = "block";
  tip.style.left = Math.min(ev.clientX + 14, innerWidth - 380) + "px"; tip.style.top = (ev.clientY + 14) + "px"; }
function hideTip(){ tip.style.display = "none"; }
document.getElementById("genAt").textContent = "最終更新: " + D.generated_at;

// ---------------- KPI ----------------
const measured = E.filter(e => e.base_median);
const accepted = E.filter(e => e.status === "ACCEPTED");
const fails = E.filter(e => /FAIL/.test(e.status || ""));
const cum = accepted.reduce((m, e) => m * (1 + e.gain_pct / 100), 1);
const latest = measured.length ? measured[measured.length - 1] : null;
const noiseAvg = measured.length ? measured.reduce((s, e) => s + (e.base_noise_pct || 0), 0) / measured.length : null;
const kpis = [
  ["試行数（新方式）", E.length, `測定まで到達 ${measured.length} 件`],
  ["採択", accepted.length, E.length ? `採択率 ${(accepted.length / E.length * 100).toFixed(1)}%` : ""],
  ["累積改善率", E.length ? ((cum - 1) * 100).toFixed(1) + "%" : "-", "採択パッチの改善率の積"],
  ["直近 HEAD 中央値", latest ? fmt(latest.base_median) : "-", latest ? latest.time : ""],
  ["ビルド・機能テスト失敗", fails.length, "コミット前にすべて遮断"],
  ["同条件での測定ブレ", noiseAvg == null ? "-" : noiseAvg.toFixed(1) + "%", "HEAD 反復測定の幅（平均）"],
  ["旧方式の試行数", (D.legacy || []).length, "参考値"],
];
document.getElementById("kpis").innerHTML = kpis.map(([l, v, n]) =>
  `<div class="card kpi"><div class="label">${l}</div><div class="value">${typeof v === "number" ? fmt(v) : v}</div><div class="note">${esc(n)}</div></div>`).join("");

// ---------------- SVG helpers ----------------
function frame(svg){ const w = svg.clientWidth || 1200, h = +svg.getAttribute("height");
  svg.setAttribute("viewBox", `0 0 ${w} ${h}`); svg.innerHTML = ""; return {w, h, L:64, R:16, T:12, B:28}; }
function el(svg, tag, attrs, text){ const n = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const k in attrs) n.setAttribute(k, attrs[k]); if (text != null) n.textContent = text; svg.appendChild(n); return n; }
function yAxis(svg, f, lo, hi, ticks, fmtT){ for (let i = 0; i <= ticks; i++){ const v = lo + (hi - lo) * i / ticks;
  const y = f.T + (f.h - f.T - f.B) * (1 - i / ticks);
  el(svg, "line", {x1:f.L, x2:f.w - f.R, y1:y, y2:y, stroke:"#eef2f7"});
  el(svg, "text", {x:f.L - 6, y:y + 4, "text-anchor":"end", "font-size":11, fill:"#6b7280"}, fmtT(v)); } }
function emptyMsg(svg, f, msg){ el(svg, "text", {x:f.w / 2, y:f.h / 2, "text-anchor":"middle", fill:"#9ca3af", "font-size":13}, msg); }
const color = s => s === "ACCEPTED" ? "var(--acc)" : s === "REJECTED" ? "var(--rej)" : "var(--fail)";

// ---------------- A/B chart ----------------
(function(){
  const svg = document.getElementById("abChart"), f = frame(svg);
  if (!E.length) return emptyMsg(svg, f, "まだ新方式の試行がありません");
  const vals = measured.flatMap(e => [e.base_median, e.cand_median]);
  let lo = Math.min(...vals, Infinity), hi = Math.max(...vals, -Infinity);
  if (!isFinite(lo)) { lo = 0; hi = 1; } const pad = (hi - lo) * 0.12 || hi * 0.1 || 1; lo = Math.max(0, lo - pad); hi += pad;
  yAxis(svg, f, lo, hi, 4, v => (v / 1000).toFixed(0) + "k");
  const n = E.length, x = i => f.L + (f.w - f.L - f.R) * (n === 1 ? 0.5 : i / (n - 1)), y = v => f.T + (f.h - f.T - f.B) * (1 - (v - lo) / (hi - lo));
  E.forEach((e, i) => {
    const X = x(i);
    if (!e.base_median) { const Y = f.h - f.B - 8;
      el(svg, "text", {x:X, y:Y, "text-anchor":"middle", fill:"var(--fail)", "font-size":13, "font-weight":700}, "×")
        .addEventListener("mousemove", ev => showTip(ev, `#${e.id} ${esc(e.title)}<br><b>${e.status}</b><br>${esc((e.error || "").slice(0, 200))}`));
      return; }
    el(svg, "line", {x1:X, x2:X, y1:y(e.base_median), y2:y(e.cand_median), stroke:color(e.status), "stroke-width":2, opacity:.7});
    el(svg, "circle", {cx:X, cy:y(e.base_median), r:4, fill:"var(--base)"});
    const c = el(svg, "circle", {cx:X, cy:y(e.cand_median), r:5.5, fill:color(e.status), stroke:"#fff", "stroke-width":1.5});
    const h = ev => showTip(ev, `<b>#${e.id} ${esc(e.title)}</b><br>${e.time}<br>HEAD 中央値: ${fmt(e.base_median)}<br>候補 中央値: ${fmt(e.cand_median)}<br>改善率: ${e.gain_pct}% / 勝ち ${e.wins}/${e.rounds}<br><b>${e.status}</b>`);
    c.addEventListener("mousemove", h); c.addEventListener("mouseleave", hideTip);
  });
  svg.addEventListener("mouseleave", hideTip);
})();

// ---------------- gain chart ----------------
(function(){
  const svg = document.getElementById("gainChart"), f = frame(svg);
  if (!measured.length) return emptyMsg(svg, f, "測定データなし");
  const th = measured[measured.length - 1].min_gain_pct ?? 2;
  const g = measured.map(e => e.gain_pct); let lo = Math.min(-th * 2, ...g), hi = Math.max(th * 2, ...g);
  lo = Math.floor(lo - 1); hi = Math.ceil(hi + 1);
  yAxis(svg, f, lo, hi, 4, v => v.toFixed(0) + "%");
  const n = measured.length, bw = Math.max(3, Math.min(28, (f.w - f.L - f.R) / n * 0.7));
  const x = i => f.L + (f.w - f.L - f.R) * ((i + 0.5) / n), y = v => f.T + (f.h - f.T - f.B) * (1 - (v - lo) / (hi - lo));
  el(svg, "line", {x1:f.L, x2:f.w - f.R, y1:y(0), y2:y(0), stroke:"#9ca3af"});
  el(svg, "line", {x1:f.L, x2:f.w - f.R, y1:y(th), y2:y(th), stroke:"var(--acc)", "stroke-dasharray":"5 4"});
  el(svg, "text", {x:f.w - f.R, y:y(th) - 4, "text-anchor":"end", "font-size":11, fill:"var(--acc)"}, `しきい値 +${th}%`);
  measured.forEach((e, i) => { const y0 = y(0), y1 = y(e.gain_pct);
    const r = el(svg, "rect", {x:x(i) - bw / 2, y:Math.min(y0, y1), width:bw, height:Math.max(1, Math.abs(y1 - y0)), fill:color(e.status), rx:2});
    r.addEventListener("mousemove", ev => showTip(ev, `<b>#${e.id} ${esc(e.title)}</b><br>改善率 ${e.gain_pct}%（勝ち ${e.wins}/${e.rounds}）<br>${e.status}`));
    r.addEventListener("mouseleave", hideTip); });
})();

// ---------------- cumulative chart ----------------
(function(){
  const svg = document.getElementById("cumChart"), f = frame(svg);
  if (!E.length) return emptyMsg(svg, f, "データなし");
  let m = 1; const pts = [{i:0, v:0, e:null}];
  E.forEach((e, i) => { if (e.status === "ACCEPTED") m *= 1 + e.gain_pct / 100; pts.push({i:i + 1, v:(m - 1) * 100, e}); });
  const hi = Math.max(5, ...pts.map(p => p.v)) * 1.15;
  yAxis(svg, f, 0, hi, 4, v => v.toFixed(0) + "%");
  const n = pts.length - 1 || 1, x = i => f.L + (f.w - f.L - f.R) * (i / n), y = v => f.T + (f.h - f.T - f.B) * (1 - v / hi);
  el(svg, "polyline", {points:pts.map(p => `${x(p.i)},${y(p.v)}`).join(" "), fill:"none", stroke:"var(--acc)", "stroke-width":2.5});
  pts.filter(p => p.e && p.e.status === "ACCEPTED").forEach(p => {
    const c = el(svg, "circle", {cx:x(p.i), cy:y(p.v), r:5, fill:"var(--acc)"});
    c.addEventListener("mousemove", ev => showTip(ev, `<b>#${p.e.id} ${esc(p.e.title)}</b><br>+${p.e.gain_pct}% → 累積 ${p.v.toFixed(1)}%<br>commit ${p.e.commit || "-"}`));
    c.addEventListener("mouseleave", hideTip); });
})();

// ---------------- per-diagnosis table ----------------
(function(){
  const t = document.getElementById("diagTable");
  if (!E.length) { t.innerHTML = `<tr><td class="empty">データなし</td></tr>`; return; }
  const g = {};
  E.forEach(e => { const k = e.diag_id || "manual"; (g[k] = g[k] || {title:e.title, cat:e.category, n:0, acc:0, fail:0, gains:[]});
    g[k].n++; if (e.status === "ACCEPTED") g[k].acc++; if (/FAIL/.test(e.status)) g[k].fail++; if (e.base_median) g[k].gains.push(e.gain_pct); });
  const rows = Object.entries(g).sort((a, b) => b[1].n - a[1].n);
  t.innerHTML = `<tr><th>診断 ID</th><th>カテゴリ</th><th>内容</th><th>試行</th><th>採択</th><th>失敗</th><th>平均改善率</th><th>最良</th></tr>` +
    rows.map(([k, v]) => { const avg = v.gains.length ? v.gains.reduce((a, b) => a + b, 0) / v.gains.length : null;
      const best = v.gains.length ? Math.max(...v.gains) : null;
      return `<tr><td>${esc(k)}</td><td>${esc(v.cat || "")}</td><td>${esc(v.title)}</td><td class="num">${v.n}</td><td class="num">${v.acc}</td><td class="num">${v.fail}</td>
        <td class="num ${avg > 0 ? "pos" : "neg"}">${avg == null ? "-" : avg.toFixed(2) + "%"}</td><td class="num">${best == null ? "-" : best.toFixed(2) + "%"}</td></tr>`; }).join("");
})();

// ---------------- experiments table ----------------
(function(){
  const t = document.getElementById("expTable"), fs = document.getElementById("fStatus"), fd = document.getElementById("fDiag"), ft = document.getElementById("fText");
  [...new Set(E.map(e => e.status))].forEach(s => fs.insertAdjacentHTML("beforeend", `<option>${esc(s)}</option>`));
  [...new Set(E.map(e => e.diag_id || "manual"))].forEach(s => fd.insertAdjacentHTML("beforeend", `<option>${esc(s)}</option>`));
  let sortKey = "id", sortDir = -1;
  const cols = [["id","#"],["time","日時"],["title","パッチ"],["status","判定"],["base_median","HEAD 中央値"],["cand_median","候補 中央値"],["gain_pct","改善率"],["wins","勝ち"],["p50","候補 P50"],["p99","候補 P99"],["commit","コミット / 差分"]];
  function render(){
    let rows = E.filter(e => (!fs.value || e.status === fs.value) && (!fd.value || (e.diag_id || "manual") === fd.value) &&
      (!ft.value || JSON.stringify(e).toLowerCase().includes(ft.value.toLowerCase())));
    rows.sort((a, b) => { const x = a[sortKey] ?? "", y = b[sortKey] ?? ""; return (x > y ? 1 : x < y ? -1 : 0) * sortDir; });
    if (!rows.length) { t.innerHTML = `<tr><td class="empty">該当なし</td></tr>`; return; }
    t.innerHTML = `<tr>${cols.map(([k, l]) => `<th data-k="${k}">${l}${sortKey === k ? (sortDir > 0 ? " ▲" : " ▼") : ""}</th>`).join("")}</tr>` + rows.map(e => {
      const cr = e.cand_runs && e.cand_runs.length ? e.cand_runs[Math.floor(e.cand_runs.length / 2)] : {};
      const runs = e.base_runs ? `<details><summary>生データ</summary><pre>HEAD: ${e.base_runs.map(r => fmt(r.score)).join(", ")}\n候補: ${(e.cand_runs || []).map(r => fmt(r.score)).join(", ")}\nHEAD ブレ: ${e.base_noise_pct}%\nルール: ${esc(e.rule || "")}</pre></details>` : "";
      const err = e.error ? `<details><summary>エラー</summary><pre>${esc(e.error)}</pre></details>` : "";
      const link = e.commit && D.repo_url ? `<a href="${D.repo_url}/commit/${e.commit}" target="_blank">${e.commit}</a>` : (e.commit || "");
      const diff = e.patch_file ? ` <a href="${D.repo_url ? D.repo_url + "/blob/main/" + e.patch_file : e.patch_file}" target="_blank">diff</a>` : "";
      return `<tr><td class="num">${e.id}</td><td style="white-space:nowrap">${esc(e.time)}</td><td>${esc(e.title)}<div style="color:#9ca3af;font-size:11px">${esc(e.diag_id || "")} · HEAD ${esc(e.head || "")}</div>${runs}${err}</td>
        <td><span class="badge ${e.status}">${e.status}</span></td><td class="num">${fmt(e.base_median)}</td><td class="num">${fmt(e.cand_median)}</td>
        <td class="num ${e.gain_pct > 0 ? "pos" : "neg"}">${e.gain_pct == null ? "-" : e.gain_pct + "%"}</td><td class="num">${e.wins == null ? "-" : e.wins + "/" + e.rounds}</td>
        <td class="num">${cr.p50 ?? "-"}</td><td class="num">${cr.p99 ?? "-"}</td><td>${link}${diff}</td></tr>`; }).join("");
    t.querySelectorAll("th").forEach(th => th.onclick = () => { const k = th.dataset.k; sortDir = sortKey === k ? -sortDir : -1; sortKey = k; render(); });
  }
  [fs, fd].forEach(x => x.onchange = render); ft.oninput = render; render();
})();

// ---------------- commits ----------------
(function(){
  const t = document.getElementById("commitTable"), c = D.commits || [];
  if (!c.length) { t.innerHTML = `<tr><td class="empty">perf: コミットなし</td></tr>`; return; }
  t.innerHTML = `<tr><th>日時</th><th>コミット</th><th>内容</th></tr>` + c.map(r =>
    `<tr><td style="white-space:nowrap">${esc(r.date)}</td><td>${D.repo_url ? `<a href="${D.repo_url}/commit/${r.hash}" target="_blank">${r.hash}</a>` : r.hash}</td><td>${esc(r.subject)}</td></tr>`).join("");
})();

// ---------------- legacy chart ----------------
(function(){
  const svg = document.getElementById("legacyChart"), f = frame(svg), L = D.legacy || [];
  if (!L.length) return emptyMsg(svg, f, "旧履歴なし");
  const hi = Math.max(...L.map(e => e.score)) * 1.08;
  yAxis(svg, f, 0, hi, 4, v => (v / 1000).toFixed(0) + "k");
  const n = L.length, x = i => f.L + (f.w - f.L - f.R) * (i / Math.max(1, n - 1)), y = v => f.T + (f.h - f.T - f.B) * (1 - v / hi);
  L.forEach((e, i) => { const c = el(svg, "circle", {cx:x(i), cy:y(e.score), r:e.accepted ? 4 : 2.2, fill:e.accepted ? "var(--acc)" : "#cbd5e1"});
    c.addEventListener("mousemove", ev => showTip(ev, `#${i} ${esc(e.desc)}<br>${fmt(e.score)} pts ${e.accepted ? "（採択）" : ""}`)); c.addEventListener("mouseleave", hideTip); });
})();
addEventListener("resize", () => location.reload());
</script>
</body>
</html>
"""

if __name__ == "__main__":
    print(generate())
