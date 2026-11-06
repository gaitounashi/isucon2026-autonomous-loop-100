"""
adhoc_profiler.py - オンデマンド・アドホック計測 & プロファイリングツール
【ISUCON2026 優勝 Typing Bears (kawasy 氏 625k) 実戦知見準拠】

設計思想:
- 常時監視のオーバーヘッドを完全排除 (ベンチマーク測定自体の精度を歪めない)
- エージェントが必要と判断した瞬間のみ、ワンショットで高精度な診断を実行
- TIME_WAIT ソケット推移、主要エンドポイントの直接レイテンシ測定、クラスタ疎通検査
"""

from __future__ import annotations

import argparse
import json
import subprocess
import time
import urllib.request
import urllib.error
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
ARENA_DIR = REPO_ROOT / "arena"
HARNESS_DIR = REPO_ROOT / "harness"

PORTS = [8001, 8002, 8003]
REGIONS = {8001: "asia1", 8002: "europe1", 8003: "america1"}


def probe_socket_health() -> dict[str, int]:
    """TIME_WAIT / ESTABLISHED 等のソケット数を集計"""
    stats = {"TIME_WAIT": 0, "ESTABLISHED": 0, "CLOSE_WAIT": 0, "TOTAL": 0}
    try:
        res = subprocess.run(["netstat", "-ano"], capture_output=True, text=True, encoding="cp932", errors="replace")
        if res.returncode == 0:
            for line in res.stdout.splitlines():
                for state in ["TIME_WAIT", "ESTABLISHED", "CLOSE_WAIT"]:
                    if state in line:
                        stats[state] += 1
                        stats["TOTAL"] += 1
    except Exception as e:
        stats["error"] = str(e)
    return stats


def probe_node_latencies(port: int = 8001) -> dict:
    """特定ノードに対して主要エンドポイントを叩き、直接のレイテンシ (ms) を計測"""
    endpoints = [
        ("feed_public", f"http://127.0.0.1:{port}/api/feed/public"),
        ("feed_fof", f"http://127.0.0.1:{port}/api/feed/friends-of-friends"),
        ("feed_generic", f"http://127.0.0.1:{port}/api/feed/public_direct"),
    ]

    results = {}
    for name, url in endpoints:
        latencies = []
        status_codes = []
        for _ in range(5):
            t0 = time.perf_counter()
            try:
                req = urllib.request.Request(url, headers={"User-Agent": "adhoc-profiler"})
                with urllib.request.urlopen(req, timeout=1.5) as resp:
                    _ = resp.read()
                    status_codes.append(resp.status)
            except urllib.error.HTTPError as e:
                status_codes.append(e.code)
            except Exception:
                status_codes.append(-1)
            t1 = time.perf_counter()
            latencies.append((t1 - t0) * 1000.0)

        valid_lats = [l for l, st in zip(latencies, status_codes) if st == 200]
        results[name] = {
            "avg_ms": round(sum(valid_lats) / len(valid_lats), 3) if valid_lats else -1,
            "min_ms": round(min(valid_lats), 3) if valid_lats else -1,
            "max_ms": round(max(valid_lats), 3) if valid_lats else -1,
            "statuses": status_codes,
        }
    return results


def run_adhoc_diagnosis() -> dict:
    """クラスタ全体のアドホック診断を実行し、結果サマリーを生成"""
    sockets = probe_socket_health()
    report = {
        "timestamp": time.time(),
        "sockets": sockets,
        "nodes": {}
    }

    for port in PORTS:
        reg = REGIONS.get(port, "unknown")
        report["nodes"][f"{reg}:{port}"] = probe_node_latencies(port)

    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Ad-hoc Profiler for ISUReal Cluster")
    parser.add_argument("--sockets-only", action="store_true", help="Print socket health only")
    args = parser.parse_args()

    if args.sockets_only:
        s = probe_socket_health()
        print(f"[*] Sockets: TIME_WAIT={s['TIME_WAIT']}, ESTABLISHED={s['ESTABLISHED']}, CLOSE_WAIT={s['CLOSE_WAIT']}")
    else:
        print("[*] Running ad-hoc diagnosis on active cluster...")
        diag = run_adhoc_diagnosis()
        print(json.dumps(diag, indent=2))
