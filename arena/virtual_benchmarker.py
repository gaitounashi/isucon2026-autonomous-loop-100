"""
virtual_benchmarker.py - ISUCON2026 Realistic Workload & Score Simulator

ISUCON2026（ISUReal）のワークロード特性を再現した並行負荷ベンチマーカー。
- フィード取得から画像URLを正規に解決してアクセス (実機シナリオ)
- 画像アクセス (70%): 有効な署名付きURLに対するアクセス
- フィード取得アクセス (30%): 国内(1.0倍) と 欧米(1.5倍) の混合
- レイテンシ分布 (P50, P90, P99) および ISUCON スコアリングを算出
"""

import time
import urllib.request
import urllib.error
import concurrent.futures
import statistics
import random
import sys
import json

def fetch_initial_image_urls(base_url: str, retries: int = 10) -> list[str]:
    """サーバーのフィードエンドポイントから画像URLリストを動的取得"""
    for _ in range(retries):
        try:
            req = urllib.request.Request(f"{base_url}/api/feed/public")
            with urllib.request.urlopen(req, timeout=2.0) as resp:
                data = json.loads(resp.read().decode("utf-8"))
                urls = data.get("image_urls", [])
                if urls:
                    return urls
        except Exception:
            time.sleep(0.3)
    return []

def run_bench(port: int, duration_sec: int = 10, concurrency: int = 16) -> dict:
    base_url = f"http://127.0.0.1:{port}"
    
    # サーバーから正規の画像URLを取得
    valid_image_urls = fetch_initial_image_urls(base_url)
    if not valid_image_urls:
        return {"error": "Failed to fetch image URLs from server"}

    start_time = time.perf_counter()
    end_time = start_time + duration_sec

    latencies = []
    success_count = 0
    failure_count = 0
    raw_score = 0

    def worker_loop():
        nonlocal success_count, failure_count, raw_score
        local_latencies = []
        local_success = 0
        local_failure = 0
        local_score = 0

        while time.perf_counter() < end_time:
            req_type = random.random()
            req_start = time.perf_counter()
            headers = {}
            score_weight = 1.0

            if req_type < 0.70:
                # 70% 画像アクセス (欧米 1.5倍, 国内 1.0倍)
                img_path = random.choice(valid_image_urls)
                url = f"{base_url}{img_path}"
                if random.random() < 0.5:
                    headers["X-Region"] = "europe1"
                    score_weight = 1.5
                else:
                    headers["X-Region"] = "asia1"
                    score_weight = 1.0
            else:
                # 30% フィードアクセス (欧米 3.0倍, 国内 2.0倍)
                url = f"{base_url}/api/feed/friends-of-friends"
                if random.random() < 0.5:
                    headers["X-Region"] = "europe1"
                    score_weight = 3.0
                else:
                    headers["X-Region"] = "asia1"
                    score_weight = 2.0

            req = urllib.request.Request(url, headers=headers)
            try:
                with urllib.request.urlopen(req, timeout=2.0) as resp:
                    _ = resp.read()
                    elapsed = time.perf_counter() - req_start
                    local_latencies.append(elapsed)
                    if resp.status == 200:
                        local_success += 1
                        local_score += int(score_weight * 10)
                    else:
                        local_failure += 1
                        local_score -= 10
            except Exception:
                elapsed = time.perf_counter() - req_start
                local_latencies.append(elapsed)
                local_failure += 1
                local_score -= 10

        return local_latencies, local_success, local_failure, local_score

    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as executor:
        futures = [executor.submit(worker_loop) for _ in range(concurrency)]
        for f in concurrent.futures.as_completed(futures):
            lats, sc, fc, score = f.result()
            latencies.extend(lats)
            success_count += sc
            failure_count += fc
            raw_score += score

    total_time = time.perf_counter() - start_time
    total_reqs = success_count + failure_count
    rps = total_reqs / total_time if total_time > 0 else 0

    if latencies:
        lat_ms = sorted([l * 1000.0 for l in latencies])
        p50 = lat_ms[int(len(lat_ms) * 0.50)]
        p90 = lat_ms[int(len(lat_ms) * 0.90)]
        p99 = lat_ms[int(len(lat_ms) * 0.99)] if len(lat_ms) >= 100 else lat_ms[-1]
    else:
        p50 = p90 = p99 = 0.0

    return {
        "score": max(raw_score, 0),
        "rps": round(rps, 1),
        "total_requests": total_reqs,
        "success_count": success_count,
        "failure_count": failure_count,
        "latency_p50_ms": round(p50, 2),
        "latency_p90_ms": round(p90, 2),
        "latency_p99_ms": round(p99, 2),
        "duration_sec": round(total_time, 2)
    }

if __name__ == "__main__":
    target_port = int(sys.argv[1]) if len(sys.argv) > 1 else 9001
    dur = int(sys.argv[2]) if len(sys.argv) > 2 else 10
    c = int(sys.argv[3]) if len(sys.argv) > 3 else 16
    print(f"[*] Running virtual benchmark on port {target_port} (Duration: {dur}s, Concurrency: {c})...")
    res = run_bench(target_port, dur, c)
    print(json.dumps(res, indent=2))
