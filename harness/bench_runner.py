"""
bench_runner.py - Local Performance & Load Benchmark for ISUReal

Python標準ライブラリのみで動作するローカルベンチマーカー。
並行リクエストによる負荷テストを行い、RPS、レイテンシ分布（P50/P90/P99）、
エラー率を計測して総合パフォーマンススコアを算出する。
"""

import time
import urllib.request
import urllib.error
import concurrent.futures
import statistics
import json
import sys

def worker_request(url: str, headers: dict = None) -> tuple[bool, float, int]:
    """1リクエストを送信し (成功可否, 所要時間[秒], ステータスコード) を返す"""
    start = time.perf_counter()
    req = urllib.request.Request(url, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=2.0) as resp:
            _ = resp.read()
            elapsed = time.perf_counter() - start
            return (resp.status == 200, elapsed, resp.status)
    except urllib.error.HTTPError as e:
        elapsed = time.perf_counter() - start
        return (False, elapsed, e.code)
    except Exception as e:
        elapsed = time.perf_counter() - start
        return (False, elapsed, 0)

def run_benchmark(target_url: str, duration_sec: int = 5, concurrency: int = 10) -> dict:
    """指定秒数・並行度で負荷テストを実行"""
    start_time = time.perf_counter()
    end_time = start_time + duration_sec
    latencies = []
    success_count = 0
    failure_count = 0
    status_codes = {}

    def loop_worker():
        nonlocal success_count, failure_count
        local_latencies = []
        while time.perf_counter() < end_time:
            ok, elapsed, status = worker_request(target_url)
            local_latencies.append(elapsed)
            if ok:
                success_count += 1
            else:
                failure_count += 1
            status_codes[status] = status_codes.get(status, 0) + 1
        return local_latencies

    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as executor:
        futures = [executor.submit(loop_worker) for _ in range(concurrency)]
        for f in concurrent.futures.as_completed(futures):
            latencies.extend(f.result())

    total_time = time.perf_counter() - start_time
    total_reqs = success_count + failure_count
    rps = total_reqs / total_time if total_time > 0 else 0

    if latencies:
        latencies_ms = sorted([l * 1000.0 for l in latencies])
        p50 = latencies_ms[int(len(latencies_ms) * 0.50)]
        p90 = latencies_ms[int(len(latencies_ms) * 0.90)]
        p99 = latencies_ms[int(len(latencies_ms) * 0.99)] if len(latencies_ms) >= 100 else latencies_ms[-1]
        avg_latency = statistics.mean(latencies_ms)
    else:
        p50 = p90 = p99 = avg_latency = 0.0

    # 総合スコア計算式: 成功RPS * (100 / max(p50, 1.0)) * 成功率
    success_rate = (success_count / total_reqs) if total_reqs > 0 else 0.0
    score = int(rps * (100.0 / max(p50, 0.5)) * success_rate)

    return {
        "score": score,
        "rps": round(rps, 2),
        "total_requests": total_reqs,
        "success_count": success_count,
        "failure_count": failure_count,
        "success_rate": round(success_rate * 100, 1),
        "latency_p50_ms": round(p50, 2),
        "latency_p90_ms": round(p90, 2),
        "latency_p99_ms": round(p99, 2),
        "latency_avg_ms": round(avg_latency, 2),
        "duration_sec": round(total_time, 2),
        "status_distribution": status_codes
    }

if __name__ == "__main__":
    target = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8080/api/images/test.jpg"
    duration = int(sys.argv[2]) if len(sys.argv) > 2 else 3
    conc = int(sys.argv[3]) if len(sys.argv) > 3 else 8
    
    print(f"[*] Starting local benchmark: {target} (Duration: {duration}s, Concurrency: {conc})...")
    res = run_benchmark(target, duration_sec=duration, concurrency=conc)
    print(json.dumps(res, indent=2))
