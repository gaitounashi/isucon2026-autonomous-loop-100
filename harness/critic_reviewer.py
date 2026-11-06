"""
critic_reviewer.py - 監査役 (Critic / Reviewer) サブエージェント & 停滞検知フック
【ISUCON2026 優勝 Typing Bears (kawasy 氏 625k) 実戦知見準拠】

役割:
1. 自律ループが局所最適 (ローカルミニマム) やスコア停滞 (連続リジェクト / 伸び悩み) に陥ったことを検知する。
2. 過去の全試行ログ (experiments.json) と現在のソースコード (node_server.go 等) を読み込む。
3. 事前定義された「勝者のチェックリスト (Winner Checklist)」と照合し、
   「固定値の代わりに動的レート制御を試したか？」「不正リクエストを先頭バイトで即切断しているか？」等の
   重大な見落とし・仮説の抜け漏れを指摘 (Review/Critic)。
4. 新たな高付加価値パッチ仮説を動的に生成し、診断キューに優先注入する。
"""

from __future__ import annotations

import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
ARENA_DIR = REPO_ROOT / "arena"
HARNESS_DIR = REPO_ROOT / "harness"
EXPERIMENTS_FILE = HARNESS_DIR / "experiments.json"

# ==============================================================================
# 勝者のチェックリスト (Winner's Engineering Checklist)
# ISUCON2026 優勝 Typing Bears 625k / fukabori.fm #142 / 八百万のOSS #27 より
# ==============================================================================
WINNER_CHECKLIST = [
    {
        "id": "critic_adaptive_rate_region_bias",
        "category": "CRITIC/SCHEDULING",
        "priority": -1,  # 最優先
        "title": "Critic Fix: adaptive rate-based dynamic throttling for asia1 (replace fixed 8ms sleep with load-based delay)",
        "action": "apply_adaptive_rate_region_bias",
        "check": lambda code, hist: "time.Sleep(8 * time.Millisecond)" in code and "adaptiveThrottle" not in code,
        "advice": "固定 8ms スリープは低負荷時にスループットを削り、超高負荷時に抑制不足になります。リクエストレートに応じた動的遅延制御を導入してください。"
    },
    {
        "id": "critic_fast_reject_invalid_sig",
        "category": "CRITIC/SECURITY_FAST_DROP",
        "priority": -1,
        "title": "Critic Fix: early 403 rejection on invalid image signatures (bypass 1000 HMAC loops for bad requests)",
        "action": "apply_fast_reject_invalid_sig",
        "check": lambda code, hist: "referenceImageSignature(signingKey, imageID, exp)" in code and "invalidSigFastDrop" not in code,
        "advice": "ベンチマーカーの不正リクエスト（改ざん署名・期限切れ）に対して 1000 回 HMAC を計算するのは CPU の無駄遣いです。先頭数バイトの検証や固定ハッシュ長不一致で即 403 早期リターンしてください。"
    },
    {
        "id": "critic_exact_static_feed_routes",
        "category": "CRITIC/ROUTER",
        "priority": 0,
        "title": "Critic Fix: force register explicit static routes for feeds to bypass dynamic param router",
        "action": "apply_exact_static_feed_routes",
        "check": lambda code, hist: 'e.GET("/api/feed/:type"' in code and 'e.GET("/api/feed/public"' not in code,
        "advice": "フィードエンドポイントが動的 `:type` ルーティングのままです。リクエスト全体の 30% 以上を占めるため、明示的な完全静的ルーティングに分離してください。"
    },
    {
        "id": "critic_connection_buffer_tuning",
        "category": "CRITIC/NETWORK",
        "priority": 1,
        "title": "Critic Fix: tune HTTP listener read/write buffer and keep-alive to absorb connection bursts",
        "action": "apply_connection_buffer_tuning",
        "check": lambda code, hist: "ReadHeaderTimeout" not in code,
        "advice": "突発的なリクエストバースト時に TIME_WAIT やソケット枯渇が発生しています。サーバーの ReadHeaderTimeout と IdleTimeout を調整してください。"
    }
]


class CriticReviewer:
    """監査役サブエージェント本体"""

    def __init__(self, stagnation_threshold_rejects: int = 2):
        self.stagnation_threshold_rejects = stagnation_threshold_rejects

    def load_history(self) -> list[dict]:
        if not EXPERIMENTS_FILE.exists():
            return []
        try:
            with open(EXPERIMENTS_FILE, "r", encoding="utf-8") as f:
                return json.load(f)
        except Exception:
            return []

    def check_stagnation(self, history: list[dict] | None = None) -> tuple[bool, str]:
        """直近の試行ログから停滞（ローカルミニマム）状態を判定"""
        hist = history if history is not None else self.load_history()
        if not hist:
            return False, "No history yet"

        # 直近 N 件が連続して REJECTED または FAIL か？
        recent = hist[-self.stagnation_threshold_rejects:]
        if len(recent) >= self.stagnation_threshold_rejects:
            all_non_accepted = all(r.get("status") in ("REJECTED", "BUILD_FAIL", "BENCH_FAIL", "PATCH_FAIL") for r in recent)
            if all_non_accepted:
                reasons = [f"#{r.get('id')}:{r.get('status')}({r.get('diag_id')})" for r in recent]
                return True, f"Consecutive non-accepted attempts detected: {', '.join(reasons)}"

        return False, "System moving forward normally"

    def review_and_propose(self) -> list[dict]:
        """
        監査役によるログとコードの精密レビュー。
        勝者のチェックリストと照合し、未試行の高付加価値パッチ仮説を生成して返す。
        """
        hist = self.load_history()
        ns_target = ARENA_DIR / "node_server.go"
        ns_code = ns_target.read_text(encoding="utf-8") if ns_target.exists() else ""

        tried_diag_ids = {r.get("diag_id") for r in hist}
        proposals = []

        print("\n" + "=" * 65)
        print("  [CRITIC REVIEWER] Auditing codebase and experiment history...")
        print("=" * 65)

        for item in WINNER_CHECKLIST:
            item_id = item["id"]
            applicable = item["check"](ns_code, hist)

            if applicable:
                # 既に試して ACCEPTED になっている場合はスキップ
                is_already_accepted = any(r.get("diag_id") == item_id and r.get("status") == "ACCEPTED" for r in hist)
                if is_already_accepted:
                    continue

                print(f"  [!] CRITIC FINDING: Checklist [{item_id}] triggered!")
                print(f"      Advice: {item['advice']}")
                proposals.append({
                    "id": item["id"],
                    "category": item["category"],
                    "priority": item["priority"],
                    "title": item["title"],
                    "action": item["action"],
                    "critic_advice": item["advice"]
                })

        if not proposals:
            print("  [*] Critic Review: No checklist violations detected.")
        else:
            print(f"  [+] Critic Review: Synthesized {len(proposals)} high-leverage corrective hypothesis.")
        print("=" * 65 + "\n")

        return proposals


# CLI テスト用
if __name__ == "__main__":
    critic = CriticReviewer()
    stag, reason = critic.check_stagnation()
    print(f"Stagnation: {stag} ({reason})")
    props = critic.review_and_propose()
    print(f"Proposals: {len(props)}")
