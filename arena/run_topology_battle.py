"""
run_topology_battle.py - 3 AP Server Topology Head-to-Head Arena

当日の本番トポロジー（AP 3台: asia1, europe1, america1）を
ローカルマルチプロセスとして一括起動し、当日コード vs 優勝者コードの
クラスタースコア・RPS・リージョン別内訳を完全直接対決する。
"""

import subprocess
import time
import urllib.request
import os
import sys
from pathlib import Path

ARENA_DIR = Path(__file__).parent.resolve()
NODE_EXE = ARENA_DIR / "node_server.exe"

def wait_for_cluster_ready(ports: list[int], max_retries: int = 25) -> bool:
    for port in ports:
        ready = False
        url = f"http://127.0.0.1:{port}/api/feed/public"
        for _ in range(max_retries):
            try:
                with urllib.request.urlopen(url, timeout=0.5) as resp:
                    if resp.status in (200, 404):
                        ready = True
                        break
            except Exception:
                time.sleep(0.15)
        if not ready:
            print(f"[-] Node on port {port} failed to respond.")
            return False
    return True

def run_cluster_battle(duration: int = 10, workers_per_node: int = 8):
    from topology_benchmarker import run_topology_bench

    print("=" * 70)
    print("      ISUCON2026 TOPOLOGY ARENA: 3 AP SERVERS HEAD-TO-HEAD")
    print(f"      Duration: {duration}s | 3 Nodes (asia1, europe1, america1) | 24 Workers")
    print("=" * 70)

    # ----------------------------------------------------
    # Round 1: 当日コード 3台体制 (Baseline Cluster)
    # ----------------------------------------------------
    print("\n>>> [ROUND 1] Starting Baseline Cluster (当日コード: AP 3台起動)...")
    p1 = subprocess.Popen([str(NODE_EXE), "-port", "8001", "-region", "asia1", "-mode", "baseline"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    p2 = subprocess.Popen([str(NODE_EXE), "-port", "8002", "-region", "europe1", "-mode", "baseline"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    p3 = subprocess.Popen([str(NODE_EXE), "-port", "8003", "-region", "america1", "-mode", "baseline"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    if not wait_for_cluster_ready([8001, 8002, 8003]):
        print("[-] Baseline cluster failed to start.")
        for p in (p1, p2, p3):
            p.kill()
        return

    print("[+] All 3 Baseline AP Nodes ready (:8001, :8002, :8003). Benchmarking...")
    baseline_res = run_topology_bench(duration_sec=duration, workers_per_node=workers_per_node)
    for p in (p1, p2, p3):
        p.terminate()
        p.wait()
    print(f"[+] Round 1 Completed! Score: {baseline_res['cluster_score']:,} pts | Total RPS: {baseline_res['cluster_rps']}")

    time.sleep(2) # クールダウン

    # ----------------------------------------------------
    # Round 2: 優勝者知見コード 3台体制 (Optimized Cluster)
    # ----------------------------------------------------
    print("\n>>> [ROUND 2] Starting Optimized Cluster (優勝者コード: AP 3台起動)...")
    p1 = subprocess.Popen([str(NODE_EXE), "-port", "8001", "-region", "asia1", "-mode", "optimized"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    p2 = subprocess.Popen([str(NODE_EXE), "-port", "8002", "-region", "europe1", "-mode", "optimized"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    p3 = subprocess.Popen([str(NODE_EXE), "-port", "8003", "-region", "america1", "-mode", "optimized"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    if not wait_for_cluster_ready([8001, 8002, 8003]):
        print("[-] Optimized cluster failed to start.")
        for p in (p1, p2, p3):
            p.kill()
        return

    print("[+] All 3 Optimized AP Nodes ready (:8001, :8002, :8003). Benchmarking...")
    optimized_res = run_topology_bench(duration_sec=duration, workers_per_node=workers_per_node)
    for p in (p1, p2, p3):
        p.terminate()
        p.wait()
    print(f"[+] Round 2 Completed! Score: {optimized_res['cluster_score']:,} pts | Total RPS: {optimized_res['cluster_rps']}")

    # ----------------------------------------------------
    # レポート出力
    # ----------------------------------------------------
    b_score = baseline_res['cluster_score']
    o_score = optimized_res['cluster_score']
    score_mult = round(o_score / b_score, 2) if b_score > 0 else "N/A"

    b_rps = baseline_res['cluster_rps']
    o_rps = optimized_res['cluster_rps']
    rps_mult = round(o_rps / b_rps, 2) if b_rps > 0 else "N/A"

    report = []
    report.append("\n" + "=" * 70)
    report.append("           3 AP SERVERS CLUSTER BATTLE REPORT (当日本番トポロジー)")
    report.append("=" * 70)
    report.append(f"| メトリクス                 | 当日コード (AP 3台)    | 優勝者コード (AP 3台)  | 改善率 / 倍率 |")
    report.append(f"|----------------------------|------------------------|------------------------|---------------|")
    report.append(f"| **クラスター総合スコア**   | **{b_score:,} pts**    | **{o_score:,} pts**    | **{score_mult}x** |")
    report.append(f"| **総スループット (RPS)**   | {b_rps:,} req/s        | {o_rps:,} req/s        | **{rps_mult}x** |")
    report.append(f"| 総リクエスト処理数         | {baseline_res['total_requests']:,}     | {optimized_res['total_requests']:,}    | - |")
    report.append(f"| クラスター P50 レイテンシ  | {baseline_res['latency_p50_ms']} ms    | {optimized_res['latency_p50_ms']} ms   | - |")
    report.append(f"| クラスター P90 レイテンシ  | {baseline_res['latency_p90_ms']} ms    | {optimized_res['latency_p90_ms']} ms   | - |")
    report.append(f"| クラスター P99 レイテンシ  | {baseline_res['latency_p99_ms']} ms    | {optimized_res['latency_p99_ms']} ms   | - |")
    report.append(f"|----------------------------|------------------------|------------------------|---------------|")
    report.append(f"| **asia1 処理数 (1.0倍)**   | {baseline_res['per_node']['asia1']['requests']:,} reqs      | {optimized_res['per_node']['asia1']['requests']:,} reqs     | - |")
    report.append(f"| **europe1 処理数 (1.5倍)** | {baseline_res['per_node']['europe1']['requests']:,} reqs      | {optimized_res['per_node']['europe1']['requests']:,} reqs     | - |")
    report.append(f"| **america1 処理数 (1.5倍)**| {baseline_res['per_node']['america1']['requests']:,} reqs      | {optimized_res['per_node']['america1']['requests']:,} reqs     | - |")
    report.append("=" * 70)

    final_text = "\n".join(report)
    print(final_text)

    # 保存
    with open(ARENA_DIR / "topology_battle_result.md", "w", encoding="utf-8") as f:
        f.write("# ISUCON2026 本番トポロジー（AP 3台）対決結果\n\n" + final_text)

if __name__ == "__main__":
    dur = int(sys.argv[1]) if len(sys.argv) > 1 else 10
    w = int(sys.argv[2]) if len(sys.argv) > 2 else 8
    run_cluster_battle(duration=dur, workers_per_node=w)
