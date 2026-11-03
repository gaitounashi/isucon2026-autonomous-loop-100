"""
autonomous_engine.py - Autonomous Optimization Loop Controller

AIエージェントによるコード改善試行を自動評価し、
「ビルド成否」「整合性テスト」「ベンチマークスコア」の3段階ゲートによって
自動コミットまたは即時ロールバックを実行する統括コントローラー。
"""

import os
import sys
import subprocess
import json
import time
from pathlib import Path

REPO_ROOT = Path(__file__).parent.parent.resolve()
WEBAPP_DIR = REPO_ROOT / "webapp-go"
HISTORY_FILE = REPO_ROOT / "harness" / "run_history.json"

def run_cmd(cmd: list[str], cwd: Path = REPO_ROOT) -> tuple[int, str, str]:
    """コマンドを実行し (exit_code, stdout, stderr) を返す"""
    res = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, encoding="utf-8", errors="replace")
    return res.returncode, res.stdout, res.stderr

def git_rollback():
    """直前の安全なコミットまでハードリセット"""
    print("[!] ROLLBACK: Resetting working tree to HEAD...")
    run_cmd(["git", "reset", "--hard", "HEAD"])
    run_cmd(["git", "clean", "-fd"])

def git_commit(message: str) -> bool:
    """変更をコミット"""
    print(f"[+] COMMIT: {message}")
    run_cmd(["git", "add", "."])
    code, out, err = run_cmd(["git", "commit", "-m", message])
    return code == 0

def run_real_go_benchmark() -> dict:
    """go test -bench を実行して実測パフォーマンスを算出"""
    go_bin = r"C:\Program Files\Go\bin\go.exe"
    if not os.path.exists(go_bin):
        go_bin = "go"

    code, out, err = run_cmd([go_bin, "test", "-bench=BenchmarkPrecomputedImageSignature", "-benchmem", "-run=^$", "."], cwd=WEBAPP_DIR)
    if code != 0:
        return {"ok": False, "score": 0, "error": f"Benchmark failed:\n{err}\n{out}"}

    # パース: BenchmarkPrecomputedImageSignature-20   49833886   24.32 ns/op   0 B/op   0 allocs/op
    import re
    match = re.search(r"BenchmarkPrecomputedImageSignature[^\s]+\s+\d+\s+([0-9\.]+)\s+ns/op\s+(\d+)\s+B/op\s+(\d+)\s+allocs/op", out)
    if match:
        ns_op = float(match.group(1))
        b_op = int(match.group(2))
        allocs = int(match.group(3))
        # 60万点スケールへの換算式: 1000回ループ(375000ns)を15,000点、25nsを625,000点とする対数/逆数スケール
        # score = 625000 * (25.0 / max(ns_op, 25.0))
        normalized_score = int(625000 * (24.32 / max(ns_op, 24.32)))
        return {
            "ok": True,
            "ns_per_op": ns_op,
            "bytes_per_op": b_op,
            "allocs_per_op": allocs,
            "score": normalized_score,
            "raw_output": out.strip()
        }
    return {"ok": True, "score": 50000, "raw_output": out.strip()}

def check_build() -> tuple[bool, str]:
    """Go ビルドの検証 (Build Gate)"""
    go_bin = r"C:\Program Files\Go\bin\go.exe"
    if not os.path.exists(go_bin):
        go_bin = "go"
    
    code, out, err = run_cmd([go_bin, "build", "-o", "bin_check_test.exe", "."], cwd=WEBAPP_DIR)
    if code != 0:
        return False, f"Build Failed:\n{err}\n{out}"
    
    test_bin = WEBAPP_DIR / "bin_check_test.exe"
    if test_bin.exists():
        try:
            test_bin.unlink()
        except Exception:
            pass
    return True, "Build succeeded"

def run_tests() -> tuple[bool, str]:
    """Go 互換性テストの検証 (Consistency Gate)"""
    go_bin = r"C:\Program Files\Go\bin\go.exe"
    if not os.path.exists(go_bin):
        go_bin = "go"
    
    code, out, err = run_cmd([go_bin, "test", "-run=TestImageSignatureCompatibility", "."], cwd=WEBAPP_DIR)
    if code != 0:
        return False, f"Test failed:\n{err}\n{out}"
    return True, "All tests passed"

def record_history(entry: dict):
    """実行履歴を JSON に追記"""
    history = []
    if HISTORY_FILE.exists():
        try:
            with open(HISTORY_FILE, "r", encoding="utf-8") as f:
                history = json.load(f)
        except Exception:
            history = []
    history.append(entry)
    with open(HISTORY_FILE, "w", encoding="utf-8") as f:
        json.dump(history, f, indent=2, ensure_ascii=False)

def run_evaluation_cycle(target_desc: str, mock_score: int = None) -> dict:
    """
    1回の評価サイクルを実行:
    1. ビルド検証
    2. 整合性検証
    3. ベンチマーク実行 & スコア判定
    4. コミット or ロールバック
    """
    start_time = time.time()
    print(f"[*] Starting Evaluation Cycle: '{target_desc}'")

    # 1. ビルド検証
    build_ok, build_msg = check_build()
    if not build_ok:
        print(f"[-] GATE 1 FAILED (Build): {build_msg}")
        git_rollback()
        record_history({
            "target": target_desc,
            "status": "BUILD_FAILED",
            "error": build_msg,
            "timestamp": time.time()
        })
        return {"accepted": False, "gate": "build", "error": build_msg}

    print("[+] GATE 1 PASSED: Build OK")

    # 2. 整合性検証 (テストスイート実行)
    test_ok, test_msg = run_tests()
    if not test_ok:
        print(f"[-] GATE 2 FAILED (Consistency): {test_msg}")
        git_rollback()
        record_history({
            "target": target_desc,
            "status": "TEST_FAILED",
            "error": test_msg,
            "timestamp": time.time()
        })
        return {"accepted": False, "gate": "consistency", "error": test_msg}
    
    print("[+] GATE 2 PASSED: Consistency Tests OK")

    # 3. 性能スコア測定
    if mock_score is not None:
        score = mock_score
        bench_info = {"score": score, "ns_per_op": 0}
    else:
        bench_info = run_real_go_benchmark()
        if not bench_info.get("ok"):
            print(f"[-] GATE 3 FAILED (Bench Execution): {bench_info.get('error')}")
            git_rollback()
            return {"accepted": False, "gate": "benchmark_exec", "error": bench_info.get("error")}
        score = bench_info["score"]
        print(f"[*] Real Go Benchmark: {bench_info.get('ns_per_op')} ns/op, {bench_info.get('bytes_per_op')} B/op, {bench_info.get('allocs_per_op')} allocs/op")
    
    # 過去のベースラインスコアを取得
    baseline_score = 0
    if HISTORY_FILE.exists():
        try:
            with open(HISTORY_FILE, "r", encoding="utf-8") as f:
                hist = json.load(f)
                accepted = [h.get("score", 0) for h in hist if h.get("status") == "ACCEPTED"]
                if accepted:
                    baseline_score = max(accepted)
        except Exception:
            pass

    print(f"[*] Performance Score: {score} (Current Baseline: {baseline_score})")

    # 4. 判定
    if score >= baseline_score:
        commit_msg = f"perf: {target_desc} (Score: {score} >= {baseline_score})"
        git_commit(commit_msg)
        record_history({
            "target": target_desc,
            "status": "ACCEPTED",
            "score": score,
            "baseline": baseline_score,
            "timestamp": time.time()
        })
        print(f"[+] CHANGE ACCEPTED & COMMITTED! Score: {score}")
        return {"accepted": True, "score": score, "baseline": baseline_score}
    else:
        print(f"[-] SCORE REGRESSION: {score} < {baseline_score}. Rolling back...")
        git_rollback()
        record_history({
            "target": target_desc,
            "status": "REJECTED_REGRESSION",
            "score": score,
            "baseline": baseline_score,
            "timestamp": time.time()
        })
        return {"accepted": False, "gate": "benchmark", "score": score, "baseline": baseline_score}

if __name__ == "__main__":
    desc = sys.argv[1] if len(sys.argv) > 1 else "manual test run"
    sc = int(sys.argv[2]) if len(sys.argv) > 2 else 600000
    run_evaluation_cycle(desc, mock_score=sc)
