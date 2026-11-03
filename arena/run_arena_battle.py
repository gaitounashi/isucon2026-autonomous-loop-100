"""
run_arena_battle.py - Head-to-Head Benchmark Arena Runner

当日コード (baseline_server) と 優勝者知見コード (optimized_server) を
ローカル上で順番に起動し、同一ワークロード負荷でベンチマークを実施して
スコア・RPS・レイテンシを直接対決・比較する。
"""

import subprocess
import time
import urllib.request
import os
import sys
from pathlib import Path

ARENA_DIR = Path(__file__).parent.resolve()
BASELINE_EXE = ARENA_DIR / "baseline_server.exe"
OPTIMIZED_EXE = ARENA_DIR / "optimized_server.exe"

def wait_for_ready(port: int, max_retries: int = 20) -> bool:
    url = f"http://127.0.0.1:{port}/api/feed/public"
    for _ in range(max_retries):
        try:
            with urllib.request.urlopen(url, timeout=1.0) as resp:
                if resp.status in (200, 404):
                    return True
        except Exception:
            time.sleep(0.2)
    return False

def run_battle(duration: int = 10, concurrency: int = 16):
    from virtual_benchmarker import run_bench

    print("=" * 65)
    print("       ISUCON2026 VIRTUAL ARENA: HEAD-TO-HEAD BATTLE")
    print(f"       Duration: {duration}s | Concurrency: {concurrency} workers")
    print("=" * 65)

    # ----------------------------------------------------
    # Round 1: 当日コード (Baseline)
    # ----------------------------------------------------
    print("\n>>> [ROUND 1] Starting Baseline Server (当日コード)...")
    proc_baseline = subprocess.Popen([str(BASELINE_EXE)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if not wait_for_ready(9001):
        print("[-] Error: Baseline server failed to start.")
        proc_baseline.kill()
        return

    print("[+] Baseline Server ready on :9001. Running benchmark...")
    baseline_result = run_bench(9001, duration_sec=duration, concurrency=concurrency)
    proc_baseline.terminate()
    proc_baseline.wait()
    print(f"[+] Round 1 Completed! Score: {baseline_result['score']:,} pts | RPS: {baseline_result['rps']}")

    time.sleep(2) # クールダウン

    # ----------------------------------------------------
    # Round 2: 優勝者知見コード (Optimized)
    # ----------------------------------------------------
    print("\n>>> [ROUND 2] Starting Optimized Server (優勝者知見コード)...")
    proc_optimized = subprocess.Popen([str(OPTIMIZED_EXE)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if not wait_for_ready(9002):
        print("[-] Error: Optimized server failed to start.")
        proc_optimized.kill()
        return

    print("[+] Optimized Server ready on :9002. Running benchmark...")
    optimized_result = run_bench(9002, duration_sec=duration, concurrency=concurrency)
    proc_optimized.terminate()
    proc_optimized.wait()
    print(f"[+] Round 2 Completed! Score: {optimized_result['score']:,} pts | RPS: {optimized_result['rps']}")

    # ----------------------------------------------------
    # 結果レポート生成
    # ----------------------------------------------------
    b_score = baseline_result['score']
    o_score = optimized_result['score']
    score_multiplier = round(o_score / b_score, 2) if b_score > 0 else "N/A"
    
    b_rps = baseline_result['rps']
    o_rps = optimized_result['rps']
    rps_multiplier = round(o_rps / b_rps, 2) if b_rps > 0 else "N/A"

    report = []
    report.append("\n" + "=" * 65)
    report.append("                   FINAL ARENA BATTLE REPORT")
    report.append("=" * 65)
    report.append(f"| メトリクス               | 当日コード (Baseline) | 優勝者コード (Optimized) | 改善率 / 倍率 |")
    report.append(f"|--------------------------|-----------------------|--------------------------|---------------|")
    report.append(f"| **総合スコア**           | **{b_score:,} pts**   | **{o_score:,} pts**      | **{score_multiplier}x** |")
    report.append(f"| **秒間処理数 (RPS)**     | {b_rps:,} req/s       | {o_rps:,} req/s          | **{rps_multiplier}x** |")
    report.append(f"| 総リクエスト数           | {baseline_result['total_requests']:,}  | {optimized_result['total_requests']:,}   | - |")
    report.append(f"| 成功リクエスト数         | {baseline_result['success_count']:,}  | {optimized_result['success_count']:,}   | - |")
    report.append(f"| エラー・失敗数           | {baseline_result['failure_count']}    | {optimized_result['failure_count']}     | - |")
    report.append(f"| レイテンシ (P50)         | {baseline_result['latency_p50_ms']} ms | {optimized_result['latency_p50_ms']} ms | - |")
    report.append(f"| レイテンシ (P90)         | {baseline_result['latency_p90_ms']} ms | {optimized_result['latency_p90_ms']} ms | - |")
    report.append(f"| レイテンシ (P99)         | {baseline_result['latency_p99_ms']} ms | {optimized_result['latency_p99_ms']} ms | - |")
    report.append("=" * 65)

    final_text = "\n".join(report)
    print(final_text)

    # 結果をMarkdownファイルとしても保存
    report_file = ARENA_DIR / "arena_battle_result.md"
    with open(report_file, "w", encoding="utf-8") as f:
        f.write("# ISUCON2026 ローカル仮想ベンチマーク対決結果\n\n" + final_text)

if __name__ == "__main__":
    dur = int(sys.argv[1]) if len(sys.argv) > 1 else 10
    con = int(sys.argv[2]) if len(sys.argv) > 2 else 16
    run_battle(duration=dur, concurrency=con)
