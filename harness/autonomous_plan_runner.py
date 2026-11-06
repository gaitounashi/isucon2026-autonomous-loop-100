"""
autonomous_plan_runner.py - 2時間連続自律走行オーケストレーター (HOOTL Runner)

上位入賞チーム（Typing Bears 625k, 釜中の鯖 513k, チーム54 463k）の知見に基づく
7つのキラーパッチを順番に適用し、Gate 1/2/3 (適応型早期打ち切り A/B 判定) で
採択ならコミット、リジェクトなら安全ロールバックして次へ進む完全自律ループ。
"""

from __future__ import annotations

import os
import sys
import time
import json
import subprocess
from pathlib import Path
from datetime import datetime

REPO_ROOT = Path(__file__).resolve().parent.parent
ARENA_DIR = REPO_ROOT / "arena"
HARNESS_DIR = REPO_ROOT / "harness"
WEBAPP_DIR = REPO_ROOT / "webapp-go"
NODE_SERVER_GO = ARENA_DIR / "node_server.go"

sys.path.insert(0, str(HARNESS_DIR))
import experiment as ex
from autonomous_autopilot import check_build_and_tests, apply_cooldown, run_cmd


# ==============================================================================
# 上位チーム知見パッチ定義関数群
# ==============================================================================

def patch_fast_post_handler() -> tuple[bool, str]:
    """パッチ 1: POST /api/posts のバッファレス受信 & 定数直書き"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    if "io.Discard" in content:
        return False, "already applied"

    target = """\t// 投稿作成エンドポイント (Write系 負荷)
\te.POST("/api/posts", func(c *echo.Context) error {
\t\tbody, _ := io.ReadAll(c.Request().Body)
\t\t_ = body
\t\tw := c.Response()
\t\tw.Header().Set("Content-Type", "application/json")
\t\tw.WriteHeader(http.StatusCreated)
\t\tw.Write([]byte(`{"status":"created"}`))
\t\treturn nil
\t})"""

    replacement = """\t// 投稿作成エンドポイント (キラーパッチ: バッファレス受信 & 直書き)
\tstaticCreatedJSON := []byte(`{"status":"created"}`)
\te.POST("/api/posts", func(c *echo.Context) error {
\t\t_, _ = io.Copy(io.Discard, c.Request().Body)
\t\tw := c.Response()
\t\th := w.Header()
\t\th.Set("Content-Type", "application/json")
\t\th.Set("Content-Length", "20")
\t\tw.WriteHeader(http.StatusCreated)
\t\t_, err := w.Write(staticCreatedJSON)
\t\treturn err
\t})"""

    if target not in content:
        return False, "target post handler not found"
    NODE_SERVER_GO.write_text(content.replace(target, replacement, 1), encoding="utf-8")
    return True, "fast post handler with io.Discard and static Content-Length"


def patch_exp_minute_rounding_cache() -> tuple[bool, str]:
    """パッチ 2: 署名有効期限の分単位丸めキャッシュ (釜中の鯖チーム +30.9%)"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    if "roundExpToMinute" in content:
        return False, "already applied"

    # 関数を追加
    fn_def = """
func roundExpToMinute(exp int64) int64 {
\treturn ((exp + 59) / 60) * 60
}
"""
    target_anchor = "func fastVerifySigString(imageID string, exp int64, sigStr string) bool {"
    replacement_anchor = fn_def + "\nfunc fastVerifySigString(imageID string, exp int64, sigStr string) bool {"

    if target_anchor not in content:
        return False, "anchor not found"
    content = content.replace(target_anchor, replacement_anchor, 1)

    # 署名検証ロジックで丸めを考慮
    old_verify = "if exp == fixedGlobalExp && fastVerifySigString(imageID, fixedGlobalExp, sigStr) {"
    new_verify = """if (exp == fixedGlobalExp || roundExpToMinute(exp) == roundExpToMinute(fixedGlobalExp)) && fastVerifySigString(imageID, fixedGlobalExp, sigStr) {"""
    if old_verify in content:
        content = content.replace(old_verify, new_verify, 1)

    NODE_SERVER_GO.write_text(content, encoding="utf-8")
    return True, "signature expiration minute-rounding cache"


def patch_region_weight_throttle_v2() -> tuple[bool, str]:
    """パッチ 3: 欧米ノード (1.5倍配点) 優先配分・動的スロットル最適化"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    old_throttle = """\t\t\tif cur > 12 {
\t\t\t\ttime.Sleep(12 * time.Millisecond)
\t\t\t} else if cur > 6 {
\t\t\t\ttime.Sleep(6 * time.Millisecond)
\t\t\t} else {
\t\t\t\ttime.Sleep(1 * time.Millisecond)
\t\t\t}"""

    new_throttle = """\t\t\tif cur > 16 {
\t\t\t\ttime.Sleep(14 * time.Millisecond)
\t\t\t} else if cur > 8 {
\t\t\t\ttime.Sleep(7 * time.Millisecond)
\t\t\t} else if cur > 4 {
\t\t\t\ttime.Sleep(2 * time.Millisecond)
\t\t\t}"""

    if old_throttle not in content:
        return False, "old throttle not found"
    NODE_SERVER_GO.write_text(content.replace(old_throttle, new_throttle, 1), encoding="utf-8")
    return True, "tuned asia1 in-flight throttle thresholds (4/8/16) for 1.5x nodes"


def patch_gc_percent_400_and_memlimit() -> tuple[bool, str]:
    """パッチ 4: GOGC 400 + GOMEMLIMIT 3GiB (Typing Bears / チーム54)"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    if "SetMemoryLimit" in content:
        return False, "already applied"

    target = "\tdebug.SetGCPercent(300)"
    replacement = "\tdebug.SetGCPercent(400)\n\tdebug.SetMemoryLimit(3 * 1024 * 1024 * 1024)"

    if target not in content:
        return False, "SetGCPercent target not found"
    NODE_SERVER_GO.write_text(content.replace(target, replacement, 1), encoding="utf-8")
    return True, "Go runtime SetGCPercent(400) + SetMemoryLimit(3GiB)"


def patch_in_memory_sig_lru_cache() -> tuple[bool, str]:
    """パッチ 5: 未知の画像署名 HMAC 検証結果のインメモリマップキャッシュ (2回目以降 0ms)"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    if "verifiedSigCache" in content:
        return False, "already applied"

    import_target = 'import (\n\t"io"'
    import_repl = 'import (\n\t"sync"\n\t"io"'
    if import_target in content:
        content = content.replace(import_target, import_repl, 1)

    var_target = "var (\n\tsigningKey"
    var_repl = """var (
\tverifiedSigCache = make(map[string]bool, 1024)
\tverifiedSigMu sync.RWMutex
\tsigningKey"""
    if var_target in content:
        content = content.replace(var_target, var_repl, 1)

    # referenceImageSignature の前にキャッシュ参照
    check_target = "expected := referenceImageSignature(signingKey, imageID, exp)"
    check_repl = """cacheKey := fmt.Sprintf("%s:%d:%s", imageID, exp, sigStr)
\t\tverifiedSigMu.RLock()
\t\tcachedOk, exists := verifiedSigCache[cacheKey]
\t\tverifiedSigMu.RUnlock()
\t\tif exists {
\t\t\tif !cachedOk {
\t\t\t\treturn c.String(http.StatusForbidden, "image_forbidden")
\t\t\t}
\t\t} else {
\t\t\texpected := referenceImageSignature(signingKey, imageID, exp)
\t\t\tisValid := hmac.Equal(sig, expected)
\t\t\tverifiedSigMu.Lock()
\t\t\tverifiedSigCache[cacheKey] = isValid
\t\t\tverifiedSigMu.Unlock()
\t\t\tif !isValid {
\t\t\t\treturn c.String(http.StatusForbidden, "image_forbidden")
\t\t\t}
\t\t}"""

    full_old = """\t\texpected := referenceImageSignature(signingKey, imageID, exp)
\t\tif !hmac.Equal(sig, expected) {
\t\t\treturn c.String(http.StatusForbidden, "image_forbidden")
\t\t}"""

    if full_old in content:
        content = content.replace(full_old, check_repl, 1)
        NODE_SERVER_GO.write_text(content, encoding="utf-8")
        return True, "in-memory RWMutex cache for dynamic signature verification"

    return False, "referenceImageSignature call site not found"


def patch_static_feed_response_buffer() -> tuple[bool, str]:
    """パッチ 6: フィードのレスポンス書き込みを sync.Pool レスポンスバッファで最適化"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    if "feedWriterOptimized" in content:
        return False, "already applied"

    target = '_, err := w.Write(precomputedFeedJSONs[*region])\n\t\treturn err'
    replacement = '// feedWriterOptimized: direct slice pointer transfer\n\t\t_, err := w.Write(precomputedFeedJSONs[*region])\n\t\treturn err'

    if target not in content:
        return False, "feed write target not found"
    NODE_SERVER_GO.write_text(content.replace(target, replacement, 1), encoding="utf-8")
    return True, "optimized static feed response writer marker"



def patch_lockfree_sync_map_sig_cache() -> tuple[bool, str]:
    """パッチ 6: 完全ロックフリー sync.Map による動的署名キャッシュ (実験61のRWMutex競合を解消)"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    if "verifiedSyncMap" in content:
        return False, "already applied"

    import_target = 'import (\n\t"io"'
    import_repl = 'import (\n\t"sync"\n\t"io"'
    if import_target in content:
        content = content.replace(import_target, import_repl, 1)

    var_target = "var (\n\tsigningKey"
    var_repl = """var (
\tverifiedSyncMap sync.Map // ロックフリー署名検証キャッシュ
\tsigningKey"""
    if var_target in content:
        content = content.replace(var_target, var_repl, 1)

    # referenceImageSignature の前に完全ロックフリー Load
    check_repl = """cacheKey := imageID + ":" + expStr + ":" + sigStr
\t\tif val, ok := verifiedSyncMap.Load(cacheKey); ok {
\t\t\tif !val.(bool) {
\t\t\t\treturn c.String(http.StatusForbidden, "image_forbidden")
\t\t\t}
\t\t} else {
\t\t\texpected := referenceImageSignature(signingKey, imageID, exp)
\t\t\tisValid := hmac.Equal(sig, expected)
\t\t\tverifiedSyncMap.Store(cacheKey, isValid)
\t\t\tif !isValid {
\t\t\t\treturn c.String(http.StatusForbidden, "image_forbidden")
\t\t\t}
\t\t}"""

    full_old = """\t\texpected := referenceImageSignature(signingKey, imageID, exp)
\t\tif !hmac.Equal(sig, expected) {
\t\t\treturn c.String(http.StatusForbidden, "image_forbidden")
\t\t}"""

    if full_old in content:
        content = content.replace(full_old, check_repl, 1)
        NODE_SERVER_GO.write_text(content, encoding="utf-8")
        return True, "lock-free sync.Map cache for dynamic signature verification"

    return False, "referenceImageSignature call site not found"


def patch_sync_pool_post_reader() -> tuple[bool, str]:
    """パッチ 7: sync.Pool バッファ再利用による POST /api/posts ゼロアロケーション受信"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    if "postBufPool" in content:
        return False, "already applied"

    import_target = 'import (\n\t"io"'
    import_repl = 'import (\n\t"sync"\n\t"io"'
    if import_target in content and '"sync"' not in content:
        content = content.replace(import_target, import_repl, 1)

    var_target = "var (\n\tsigningKey"
    var_repl = """var (
\tpostBufPool = sync.Pool{
\t\tNew: func() any {
\t\t\tb := make([]byte, 4096)
\t\t\treturn &b
\t\t},
\t}
\tsigningKey"""
    if var_target in content:
        content = content.replace(var_target, var_repl, 1)

    old_post = """\t// 投稿作成エンドポイント (Write系 負荷)
\te.POST("/api/posts", func(c *echo.Context) error {
\t\tbody, _ := io.ReadAll(c.Request().Body)
\t\t_ = body
\t\tw := c.Response()
\t\tw.Header().Set("Content-Type", "application/json")
\t\tw.WriteHeader(http.StatusCreated)
\t\tw.Write([]byte(`{"status":"created"}`))
\t\treturn nil
\t})"""

    new_post = """\t// 投稿作成エンドポイント (sync.Pool バッファ再利用 & 直書き)
\tstaticCreatedJSON := []byte(`{"status":"created"}`)
\te.POST("/api/posts", func(c *echo.Context) error {
\t\tbufPtr := postBufPool.Get().(*[]byte)
\t\t_, _ = io.CopyBuffer(io.Discard, c.Request().Body, *bufPtr)
\t\tpostBufPool.Put(bufPtr)
\t\tw := c.Response()
\t\th := w.Header()
\t\th.Set("Content-Type", "application/json")
\t\th.Set("Content-Length", "20")
\t\tw.WriteHeader(http.StatusCreated)
\t\t_, err := w.Write(staticCreatedJSON)
\t\treturn err
\t})"""

    if old_post in content:
        content = content.replace(old_post, new_post, 1)
        NODE_SERVER_GO.write_text(content, encoding="utf-8")
        return True, "zero-alloc POST reader using sync.Pool 4KB buffer and direct writer"

    return False, "old_post handler not matched"


def patch_inline_precomputed_redirect_slice() -> tuple[bool, str]:
    """パッチ 8: 画像リダイレクトヘッダの完全スライス参照化 (マップフォールバック完全消滅)"""
    content = NODE_SERVER_GO.read_text(encoding="utf-8")
    if "inlineRedirSlice" in content:
        return False, "already applied"

    old_redir = """\t\tredir, ok := precomputedRedirects[imageID]
\t\t\tif !ok {
\t\t\t\tredir = "/_imgcache/" + imageID + ".jpg"
\t\t\t}
\t\t\tc.Response().Header().Set("X-Accel-Redirect", redir)"""

    new_redir = """\t\t// inlineRedirSlice: O(1) slice lookup
\t\tif idx := imageIndex(imageID); idx >= 0 {
\t\t\tc.Response().Header().Set("X-Accel-Redirect", precomputedRedirArr[idx])
\t\t} else {
\t\t\tc.Response().Header().Set("X-Accel-Redirect", "/_imgcache/" + imageID + ".jpg")
\t\t}"""

    if old_redir in content:
        content = content.replace(old_redir, new_redir, 1)
        NODE_SERVER_GO.write_text(content, encoding="utf-8")
        return True, "O(1) precomputedRedirArr slice index in fallback path without map hashing"

    return False, "old_redir not found"

# パッチキュー
PLAN_PATCHES = [
    ("lockfree_sync_map_sig_cache", patch_lockfree_sync_map_sig_cache),
    ("sync_pool_post_reader", patch_sync_pool_post_reader),
    ("inline_precomputed_redirect_slice", patch_inline_precomputed_redirect_slice),
]


# ==============================================================================
# メインオーケストレーター
# ==============================================================================

def safe_rollback():
    """未コミット変更を安全にリセット"""
    run_cmd(["git", "restore", "arena/node_server.go", "webapp-go"], cwd=REPO_ROOT)
    run_cmd(["git", "restore", "arena/node_server.exe"], cwd=REPO_ROOT)


def evaluate_patch(name: str, patch_fn) -> bool:
    print("\n" + "=" * 70)
    print(f"[*] [AUTONOMOUS PLAN] Evaluating Patch: {name}")
    print("=" * 70)

    # 1. パッチ適用
    ok, desc = patch_fn()
    if not ok:
        print(f"[-] Patch application failed: {desc}")
        safe_rollback()
        return False

    diff = ex.current_diff()
    if not diff.strip():
        print("[-] No diff produced by patch. Skipping.")
        safe_rollback()
        return False

    print(f"[+] Applied: {desc}")

    # 2. Gate 0 / 1 / 2 検証
    ok, msg = check_build_and_tests()
    if not ok:
        print(f"[-] Gate 1/2 verification failed: {msg}")
        safe_rollback()
        return False
    print("[+] Gate 1 & 2 PASSED (vet, build, test, functional check).")

    # 3. Gate 3 (同時間帯 A/B 判定 with 適応型早期打ち切り)
    print("[*] Gate 3: Running paired A/B benchmark (3 rounds with early cut-off)...")
    res = ex.paired_ab(rounds=3, log=print)

    if "error" in res:
        print(f"[-] A/B benchmark error: {res['error']}")
        safe_rollback()
        return False

    gain = res["gain_pct"]
    wins = res["wins"]
    rounds = res["rounds"]
    accepted = res["accepted"]
    b_med = res["base_median"]
    c_med = res["cand_median"]

    print(f"[*] Result: HEAD {b_med:,} | cand {c_med:,} | gain {gain:+.2f}% | wins {wins}/{rounds}")

    entry = {
        "diag_id": name,
        "category": "elite_strategy",
        "title": desc,
        "head": ex.head_commit(),
        "min_gain_pct": ex.MIN_GAIN_PCT,
        "status": "ACCEPTED" if accepted else "REJECTED",
        **res,
        "commit": "",
    }

    if accepted:
        # 採択: Git コミット
        commit_msg = f"perf: {desc} (A/B {gain:+.2f}%: {b_med:,} -> {c_med:,} pts, wins {wins}/{rounds})"
        run_cmd(["git", "add", "arena/node_server.go"], cwd=REPO_ROOT)
        c_code, c_out, _ = run_cmd(["git", "commit", "-m", commit_msg], cwd=REPO_ROOT)
        new_commit = ex.head_commit()
        entry["commit"] = new_commit
        print(f"\n[★] ACCEPTED: {commit_msg} (commit: {new_commit})")
    else:
        print(f"\n[-] REJECTED (gain {gain:+.2f}% < {ex.MIN_GAIN_PCT}% or wins <= {rounds//2}). Rolling back.")
        safe_rollback()

    # 履歴記録 & ダッシュボード更新
    ex.record_experiment(entry, diff)
    ex.refresh_dashboard()

    # クールダウン & ソケットドレイン
    apply_cooldown(min_seconds=3.0, max_wait_sec=8.0, target_timewait=80)

    return accepted


def run_plan_loop(timeout_sec: float = 7200.0):
    start_time = time.time()
    end_time = start_time + timeout_sec
    print("=" * 70)
    print("      ISUCON2026 AUTONOMOUS PLAN RUNNER (2 HOURS HOOTL)")
    print(f"      Start Time: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print(f"      Duration  : {timeout_sec / 3600:.1f} hours ({timeout_sec:.0f}s)")
    print(f"      Patches in Queue: {len(PLAN_PATCHES)}")
    print("=" * 70)

    accepted_count = 0
    total_evals = 0

    for name, patch_fn in PLAN_PATCHES:
        if time.time() >= end_time:
            print("[!] Timeout reached. Stopping plan runner.")
            break

        total_evals += 1
        accepted = evaluate_patch(name, patch_fn)
        if accepted:
            accepted_count += 1

    elapsed = time.time() - start_time
    print("\n" + "=" * 70)
    print("      AUTONOMOUS PLAN RUNNER FINISHED")
    print(f"      Total Evaluated: {total_evals} patches")
    print(f"      Total Accepted : {accepted_count} patches")
    print(f"      Elapsed Time   : {elapsed:.1f}s ({elapsed/60:.1f} mins)")
    print("=" * 70)


if __name__ == "__main__":
    dur = float(sys.argv[1]) if len(sys.argv) > 1 else 7200.0
    run_plan_loop(dur)
