"""
experiment.py - A/B 交互測定による改修評価と、構造化された実験履歴の管理

背景:
  同一コードでもマシン状態 (時間帯) によってスコアが 23万〜45万点と大きく揺れる。
  固定の過去ベースラインと比較すると判定がノイズに支配されるため、
  毎回 HEAD (A) と候補 (B) を A→B→A→B... と交互に測定し、同条件下の差分だけで判定する。

履歴:
  harness/experiments.json   : 全試行の構造化レコード (測定値の生データ含む)
  harness/patches/<id>.diff  : 各試行で適用したパッチの差分
"""

from __future__ import annotations

import json
import os
import shutil
import statistics
import subprocess
import time
from datetime import datetime
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
ARENA_DIR = REPO_ROOT / "arena"
HARNESS_DIR = REPO_ROOT / "harness"
WEBAPP_DIR = REPO_ROOT / "webapp-go"
EXPERIMENTS_FILE = HARNESS_DIR / "experiments.json"
PATCH_DIR = HARNESS_DIR / "patches"
TMP_DIR = REPO_ROOT / ".ab_tmp"

BASE_EXE = ARENA_DIR / "node_server_base.exe"
CAND_EXE = ARENA_DIR / "node_server.exe"

# 判定ルール: 中央値で +MIN_GAIN_PCT 以上 かつ 交互ペアの過半数で勝つこと
AB_ROUNDS = int(os.environ.get("AB_ROUNDS", "3"))
MIN_GAIN_PCT = float(os.environ.get("AB_MIN_GAIN_PCT", "2.0"))
BENCH_SEC = int(os.environ.get("AB_BENCH_SEC", "10"))
PAIR_COOLDOWN_SEC = float(os.environ.get("AB_PAIR_COOLDOWN_SEC", "3"))

PORTS = ((8001, "asia1"), (8002, "europe1"), (8003, "america1"))


def _go_bin() -> str:
    p = r"C:\Program Files\Go\bin\go.exe"
    return p if os.path.exists(p) else "go"


def _run(cmd: list[str], cwd: Path = REPO_ROOT) -> tuple[int, str, str]:
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, encoding="utf-8", errors="replace")
    return r.returncode, r.stdout, r.stderr


# ------------------------------------------------------------------------------
# ビルド
# ------------------------------------------------------------------------------
def build_head_base() -> tuple[bool, str]:
    """HEAD の arena/node_server.go を一時ファイルに取り出し、比較用 A バイナリをビルド"""
    TMP_DIR.mkdir(exist_ok=True)
    code, src, err = _run(["git", "show", "HEAD:arena/node_server.go"])
    if code != 0:
        return False, err
    tmp_src = TMP_DIR / "head_node_server.go"
    tmp_src.write_text(src, encoding="utf-8")
    code, out, err = _run([_go_bin(), "build", "-o", str(BASE_EXE), str(tmp_src)], cwd=WEBAPP_DIR)
    return code == 0, err + out


def current_diff() -> str:
    _, out, _ = _run(["git", "diff", "HEAD", "--", "arena/node_server.go", "webapp-go"])
    return out


def head_commit() -> str:
    _, out, _ = _run(["git", "rev-parse", "--short", "HEAD"])
    return out.strip()


# ------------------------------------------------------------------------------
# ベンチマーク
# ------------------------------------------------------------------------------
def bench_exe(exe: Path) -> dict:
    import sys
    sys.path.insert(0, str(ARENA_DIR))
    from topology_benchmarker import run_topology_bench
    from run_topology_battle import wait_for_cluster_ready

    procs = [subprocess.Popen([str(exe), "-port", str(p), "-region", r],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) for p, r in PORTS]
    try:
        if not wait_for_cluster_ready([p for p, _ in PORTS]):
            return {"error": "failed to start cluster"}
        res = run_topology_bench(duration_sec=BENCH_SEC, workers_per_node=8)
    finally:
        for pr in procs:
            pr.terminate()
            pr.wait()
    if "error" in res:
        return res
    return {
        "score": res["cluster_score"],
        "rps": res["cluster_rps"],
        "p50": res["latency_p50_ms"],
        "p99": res["latency_p99_ms"],
        "fail": res.get("failure_count", 0),
    }


def paired_ab(rounds: int = AB_ROUNDS, log=print) -> dict:
    """A(HEAD) と B(候補) を交互に測定。順序効果を避けるため奇数ラウンドは B→A"""
    base_runs, cand_runs = [], []
    for i in range(rounds):
        order = [("A", BASE_EXE), ("B", CAND_EXE)] if i % 2 == 0 else [("B", CAND_EXE), ("A", BASE_EXE)]
        for label, exe in order:
            r = bench_exe(exe)
            if "error" in r:
                return {"error": f"{label}: {r['error']}"}
            (base_runs if label == "A" else cand_runs).append(r)
            log(f"    round {i + 1} {label}: {r['score']:,} pts (rps {r['rps']}, p50 {r['p50']}ms, p99 {r['p99']}ms)")
            time.sleep(PAIR_COOLDOWN_SEC)
            # ソケットドレイン待ち (突発的 TIME_WAIT 滞留による測定ノイズを防止)
            try:
                from autonomous_autopilot import count_timewait_sockets
                st_w = time.time()
                while time.time() - st_w < 8.0:
                    if count_timewait_sockets() <= 120:
                        break
                    time.sleep(1.0)
            except Exception:
                pass

    b_scores = [r["score"] for r in base_runs]
    c_scores = [r["score"] for r in cand_runs]
    b_med, c_med = statistics.median(b_scores), statistics.median(c_scores)
    gain = (c_med - b_med) / b_med * 100 if b_med else 0.0
    wins = sum(1 for b, c in zip(b_scores, c_scores) if c > b)
    accepted = gain >= MIN_GAIN_PCT and wins * 2 > rounds
    noise = (max(b_scores) - min(b_scores)) / b_med * 100 if b_med else 0.0
    return {
        "base_runs": base_runs,
        "cand_runs": cand_runs,
        "base_median": int(b_med),
        "cand_median": int(c_med),
        "gain_pct": round(gain, 2),
        "wins": wins,
        "rounds": rounds,
        "base_noise_pct": round(noise, 2),
        "accepted": accepted,
        "rule": f"gain >= {MIN_GAIN_PCT}% and wins > {rounds // 2}/{rounds}",
    }


# ------------------------------------------------------------------------------
# 履歴
# ------------------------------------------------------------------------------
def load_experiments() -> list[dict]:
    if EXPERIMENTS_FILE.exists():
        try:
            return json.loads(EXPERIMENTS_FILE.read_text(encoding="utf-8"))
        except Exception:
            pass
    return []


def record_experiment(entry: dict, diff_text: str = "") -> dict:
    exps = load_experiments()
    exp_id = (max((e["id"] for e in exps), default=0) + 1)
    entry = {"id": exp_id, "timestamp": time.time(),
             "time": datetime.now().strftime("%Y-%m-%d %H:%M:%S"), **entry}
    if diff_text.strip():
        PATCH_DIR.mkdir(exist_ok=True)
        patch_path = PATCH_DIR / f"{exp_id:04d}_{entry.get('diag_id', 'manual')}.diff"
        patch_path.write_text(diff_text, encoding="utf-8")
        entry["patch_file"] = patch_path.relative_to(REPO_ROOT).as_posix()
    exps.append(entry)
    EXPERIMENTS_FILE.write_text(json.dumps(exps, indent=2, ensure_ascii=False), encoding="utf-8")
    return entry


def refresh_dashboard() -> None:
    try:
        from dashboard_gen import generate
        out = generate()
        extra = os.environ.get("DASHBOARD_COPY_TO")
        if extra:
            shutil.copyfile(out, extra)
        print(f"[*] Dashboard regenerated: {out}")
    except Exception as e:  # ダッシュボード失敗でループを止めない
        print(f"[!] Dashboard generation failed: {e}")
