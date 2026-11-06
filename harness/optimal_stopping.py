"""
optimal_stopping.py - 最適停止理論 (Optimal Stopping) に基づく自律停止 & 提出確定エンジン
【ISUCON2026 優勝 Typing Bears (kawasy 氏 625k) 実戦知見準拠】

数理モデル:
- ベンチマークスコアのばらつきを確率変数 X ~ N(mu, sigma^2) とみなす。
- 残り試行回数を K としたとき、残りの試行から得られる最大スコアの期待値 E[max(X_1, ..., X_K)] を算出。
- 極値統計の漸近展開 (Fisher-Tippett-Gnedenko 定理):
    E[max_K] ≈ mu + sigma * (sqrt(2 ln K) - (ln ln K + ln(4π)) / (2 sqrt(2 ln K)))
- 現在のスコアが残り試行の最大期待値の 95%〜100% を超えている場合、
  「これ以上回してもデグレリスクが高く、今提出確定するのが数学的に最適」と判定して自律停止を勧告する。
"""

from __future__ import annotations

import math
import statistics
import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
HARNESS_DIR = REPO_ROOT / "harness"
EXPERIMENTS_FILE = HARNESS_DIR / "experiments.json"


def expected_max_score(scores: list[int | float], remaining_trials: int) -> float:
    """正規分布を仮定したときの残り K 試行における最大スコアの期待値を計算"""
    if len(scores) < 2:
        return float(scores[0]) if scores else 0.0

    if remaining_trials <= 0:
        return float(max(scores))

    mu = statistics.mean(scores)
    sigma = statistics.stdev(scores)

    if sigma <= 0.0 or remaining_trials == 1:
        return mu + 0.5 * sigma

    # 極値統計近似 (K >= 2)
    K = float(remaining_trials)
    ln_k = math.log(K)
    norm_factor = math.sqrt(2.0 * ln_k)
    correction = (math.log(ln_k) + math.log(4.0 * math.pi)) / (2.0 * norm_factor) if ln_k > 0.5 else 0.0
    expected_z = norm_factor - correction

    return mu + sigma * expected_z


def evaluate_optimal_stopping(
    current_score: int,
    current_cycle: int,
    max_cycles: int,
    elapsed_sec: float,
    timeout_sec: float,
    confidence_ratio: float = 0.96
) -> dict:
    """
    現在の状態から最適停止すべきかを判定。
    """
    scores = []
    if EXPERIMENTS_FILE.exists():
        try:
            with open(EXPERIMENTS_FILE, "r", encoding="utf-8") as f:
                hist = json.load(f)
                scores = [r["cand_median"] for r in hist if r.get("status") == "ACCEPTED" and "cand_median" in r]
        except Exception:
            pass

    if not scores:
        scores = [current_score]

    remaining_cycles = max(0, max_cycles - current_cycle)
    remaining_time = max(0.0, timeout_sec - elapsed_sec)

    # 1 試行あたりの想定時間 (A/B 交互 3 ラウンド + クールダウン = 約 100秒)
    avg_cycle_sec = 100.0
    time_based_remaining_trials = int(remaining_time / avg_cycle_sec)
    effective_remaining_trials = min(remaining_cycles, max(1, time_based_remaining_trials))

    e_max = expected_max_score(scores, effective_remaining_trials)
    threshold = e_max * confidence_ratio

    # 停止判定: 現在のスコアが閾値以上、または残り時間がわずかで現状が過去ベストの場合
    all_time_best = max(scores) if scores else current_score
    is_at_or_near_peak = current_score >= all_time_best * 0.99

    should_stop = False
    reason = "Exploration continuing"

    if current_score >= threshold and is_at_or_near_peak:
        should_stop = True
        reason = (f"Current score ({current_score:,}) exceeds optimal stopping threshold ({int(threshold):,}) "
                  f"[E[max_{effective_remaining_trials}] = {int(e_max):,}]. Statistically optimal to lock in commit now.")
    elif remaining_time < 300.0 and is_at_or_near_peak:
        should_stop = True
        reason = f"Less than 5 minutes remaining and current score ({current_score:,}) is at peak. Stopping to ensure stability."

    return {
        "should_stop": should_stop,
        "reason": reason,
        "current_score": current_score,
        "all_time_best": all_time_best,
        "effective_remaining_trials": effective_remaining_trials,
        "expected_max_future": int(e_max),
        "threshold": int(threshold),
        "confidence_ratio": confidence_ratio
    }


if __name__ == "__main__":
    test_scores = [345000, 384000, 424000, 471000]
    res = evaluate_optimal_stopping(
        current_score=471889,
        current_cycle=4,
        max_cycles=100,
        elapsed_sec=3600,
        timeout_sec=7200
    )
    print(json.dumps(res, indent=2))
