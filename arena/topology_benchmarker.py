"""
topology_benchmarker.py - 3 AP Server Topology Benchmarker for ISUReal

当日の本番トポロジー（AP 3台: asia1, europe1, america1）に対して
世界各地からのアクセスを均等分散して負荷をかけ、リージョン配点に準拠してスコアを算出。
- asia1 (8001): 国内 (配点 1.0倍)
- europe1 (8002): 欧州 (配点 1.5倍)
- america1 (8003): 米国 (配点 1.5倍)
"""

import time
import urllib.request
import urllib.error
import concurrent.futures
import random
import sys
import json
import urllib3
urllib3.disable_warnings()

NODES = [
    {"name": "asia1", "port": 8001, "multiplier": 1.0},
    {"name": "europe1", "port": 8002, "multiplier": 1.5},
    {"name": "america1", "port": 8003, "multiplier": 1.5}
]

# 本番シナリオ対応用ダミーペイロード (事前アロケーションで GIL 競合ゼロ化)
DUMMY_OVERSIZED_PAYLOAD = b"\xff\xd8\xff\xe0" + b"\x00" * 1024  # 1KB ストリーム先頭部 (Content-Length: 11MB で即413検証)
POST_PAYLOAD = b'{"content":"ISUCON2026 test post message"}'


def fetch_urls(base_url: str, retries: int = 15) -> list[str]:
    for _ in range(retries):
        try:
            req = urllib.request.Request(f"{base_url}/api/feed/public")
            with urllib.request.urlopen(req, timeout=1.0) as resp:
                data = json.loads(resp.read().decode("utf-8"))
                urls = data.get("image_urls", [])
                if urls:
                    return urls
        except Exception:
            time.sleep(0.2)
    return []

def run_topology_bench(duration_sec: int = 10, workers_per_node: int = 8) -> dict:
    # 各ノードの画像URLを初期ロード
    node_image_urls = {}
    for node in NODES:
        base = f"http://127.0.0.1:{node['port']}"
        urls = fetch_urls(base)
        if not urls:
            return {"error": f"Failed to connect to node {node['name']} on port {node['port']}"}
        node_image_urls[node["name"]] = urls

    start_time = time.perf_counter()
    end_time = start_time + duration_sec

    node_stats = {node["name"]: {"success": 0, "failure": 0, "latencies": [], "score": 0} for node in NODES}
    total_workers = workers_per_node * len(NODES)

    def worker_loop(node_info: dict):
        base_url = f"http://127.0.0.1:{node_info['port']}"
        n_name = node_info["name"]
        mult = node_info["multiplier"]
        urls = node_image_urls[n_name]

        local_latencies = []
        local_success = 0
        local_failure = 0
        local_score = 0
        http_pool = urllib3.PoolManager(maxsize=10, timeout=2.0, retries=False)

        # 事前ビルドリクエストパターン (本番 ISUReal 準拠 v2: 60% 正常画像, 20% フィード, 10% 不正アップロード, 5% 改ざん署名, 5% 投稿)
        prebuilt_requests = []
        for i in range(100):
            if i < 60:
                # 正常画像 GET (60%)
                img_path = urls[i % len(urls)]
                pts = 1.5 if mult > 1.0 else 1.0
                prebuilt_requests.append(("GET", f"{base_url}{img_path}", {}, None, 200, pts))
            elif i < 80:
                # フィード GET (20%)
                pts = 3.0 if mult > 1.0 else 2.0
                prebuilt_requests.append(("GET", f"{base_url}/api/feed/friends-of-friends", {}, None, 200, pts))
            elif i < 90:
                # 巨大不正画像アップロード POST (10%) - 上位陣の 10〜32MB 即切断ターゲット
                pts = 2.0 * (1.5 if mult > 1.0 else 1.0)
                headers = {"Content-Type": "image/jpeg", "Content-Length": "11534336"}
                prebuilt_requests.append(("POST", f"{base_url}/api/images/upload", headers, DUMMY_OVERSIZED_PAYLOAD, 413, pts))
            elif i < 95:
                # 改ざん署名・攻撃画像 GET (5%) - HMAC 1000回ループ回避 / 即403ターゲット
                pts = 1.0 * (1.5 if mult > 1.0 else 1.0)
                prebuilt_requests.append(("GET", f"{base_url}/api/images/image-0001?exp=9999999999&sig=tampered_attack_sig", {}, None, 403, pts))
            else:
                # 投稿作成 POST (5%) - Write系 負荷
                pts = 3.0 if mult > 1.0 else 2.0
                headers = {"Content-Type": "application/json"}
                prebuilt_requests.append(("POST", f"{base_url}/api/posts", headers, POST_PAYLOAD, 201, pts))

        num_prebuilt = len(prebuilt_requests)
        req_idx = 0

        while time.perf_counter() < end_time:
            method, target_url, headers, body, expected_status, score_pts = prebuilt_requests[req_idx % num_prebuilt]
            req_idx += 1
            req_start = time.perf_counter()

            try:
                if method == "GET":
                    resp = http_pool.request("GET", target_url, headers=headers)
                else:
                    resp = http_pool.request("POST", target_url, headers=headers, body=body)
                _ = resp.data
                elapsed = time.perf_counter() - req_start
                local_latencies.append(elapsed)
                if resp.status == expected_status:
                    local_success += 1
                    local_score += score_pts
                else:
                    local_failure += 1
                    local_score -= 2.0
            except Exception:
                elapsed = time.perf_counter() - req_start
                local_latencies.append(elapsed)
                local_failure += 1
                local_score -= 2.0

        return n_name, local_latencies, local_success, local_failure, local_score

    # 各ノードに並行ワーカーを均等投入
    futures = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=total_workers) as executor:
        for node in NODES:
            for _ in range(workers_per_node):
                futures.append(executor.submit(worker_loop, node))

        for f in concurrent.futures.as_completed(futures):
            n_name, lats, succ, fail, sc = f.result()
            node_stats[n_name]["success"] += succ
            node_stats[n_name]["failure"] += fail
            node_stats[n_name]["latencies"].extend(lats)
            node_stats[n_name]["score"] += sc

    total_time = time.perf_counter() - start_time
    total_reqs = sum(st["success"] + st["failure"] for st in node_stats.values())
    raw_score = sum(st["score"] for st in node_stats.values())
    # 本番公式シナリオ（60秒間）相当のスコアへ正規化換算
    time_factor = 60.0 / total_time if total_time > 0 else 6.0
    official_normalized_score = int(raw_score * time_factor)

    all_latencies = []
    for st in node_stats.values():
        all_latencies.extend(st["latencies"])

    cluster_rps = total_reqs / total_time if total_time > 0 else 0

    if all_latencies:
        lat_ms = sorted([l * 1000.0 for l in all_latencies])
        p50 = lat_ms[int(len(lat_ms) * 0.50)]
        p90 = lat_ms[int(len(lat_ms) * 0.90)]
        p99 = lat_ms[int(len(lat_ms) * 0.99)] if len(lat_ms) >= 100 else lat_ms[-1]
    else:
        p50 = p90 = p99 = 0.0

    # ノード別サマリー
    per_node_summary = {}
    for n_name, st in node_stats.items():
        n_reqs = st["success"] + st["failure"]
        n_lats = sorted([l * 1000.0 for l in st["latencies"]]) if st["latencies"] else [0]
        per_node_summary[n_name] = {
            "requests": n_reqs,
            "success": st["success"],
            "rps": round(n_reqs / total_time, 1),
            "p50_ms": round(n_lats[int(len(n_lats) * 0.50)], 2),
            "p90_ms": round(n_lats[int(len(n_lats) * 0.90)], 2),
            "score": int(st["score"] * time_factor)
        }

    return {
        "cluster_score": max(official_normalized_score, 0),
        "cluster_rps": round(cluster_rps, 1),
        "total_requests": total_reqs,
        "success_count": sum(st["success"] for st in node_stats.values()),
        "failure_count": sum(st["failure"] for st in node_stats.values()),
        "latency_p50_ms": round(p50, 2),
        "latency_p90_ms": round(p90, 2),
        "latency_p99_ms": round(p99, 2),
        "duration_sec": round(total_time, 2),
        "per_node": per_node_summary
    }

if __name__ == "__main__":
    dur = int(sys.argv[1]) if len(sys.argv) > 1 else 10
    w = int(sys.argv[2]) if len(sys.argv) > 2 else 8
    res = run_topology_bench(duration_sec=dur, workers_per_node=w)
    print(json.dumps(res, indent=2))
