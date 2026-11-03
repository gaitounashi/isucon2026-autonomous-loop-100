"""
agent_prompter.py - AI Optimization Prompt Generator

ハーネスの状態（ベンチマークスコア、失敗履歴、ボトルネック関数）を集約し、
AIエージェント（Antigravity / Claude Code）に渡す最適化指示プロンプトを自動生成する。
"""

import os
import json
from pathlib import Path

REPO_ROOT = Path(__file__).parent.parent.resolve()
HISTORY_FILE = REPO_ROOT / "harness" / "run_history.json"

def get_latest_status() -> dict:
    if not HISTORY_FILE.exists():
        return {"baseline": 0, "history": []}
    try:
        with open(HISTORY_FILE, "r", encoding="utf-8") as f:
            hist = json.load(f)
            accepted = [h.get("score", 0) for h in hist if h.get("status") == "ACCEPTED"]
            baseline = max(accepted) if accepted else 0
            return {"baseline": baseline, "history": hist}
    except Exception:
        return {"baseline": 0, "history": []}

def generate_prompt_for_agent(target_file: str, focus_func: str) -> str:
    status = get_latest_status()
    baseline = status["baseline"]
    history = status["history"]

    prompt = []
    prompt.append("# 【ISUCON2026 自律改善ミッション】")
    prompt.append(f"現在ベースラインスコア: **{baseline:,} 点**")
    prompt.append("")
    prompt.append("## ターゲット対象")
    prompt.append(f"- ファイル: `webapp-go/{target_file}`")
    prompt.append(f"- 最適化対象関数: `{focus_func}`")
    prompt.append("")
    prompt.append("## 達成すべき要件")
    prompt.append("1. **ボトルネックの解消**: 無駄なループ・ハッシュ計算・冗長な DB 呼び出し・シリアライズ処理を削減すること。")
    prompt.append("2. **API 整合性の維持**: ステータスコード、エラーレスポンス、署名検証の仕様を絶対に壊さないこと。")
    prompt.append("3. **最小差分**: 変更は指定された関数・ブロックのみに留め、無関係な箇所を変更しないこと。")
    prompt.append("")
    if history:
        prompt.append("## 直近の試行履歴")
        for h in history[-3:]:
            st = h.get("status")
            sc = h.get("score", "-")
            tg = h.get("target")
            prompt.append(f"- `[{st}]` {tg} (Score: {sc})")
    
    return "\n".join(prompt)

if __name__ == "__main__":
    import sys
    t_file = sys.argv[1] if len(sys.argv) > 1 else "image_signature.go"
    t_func = sys.argv[2] if len(sys.argv) > 2 else "SignImageURL / VerifyImageSignature"
    print(generate_prompt_for_agent(t_file, t_func))
