"""
brain_delegator.py - 上位モデル (Argon / Claude / Pro) への難タスク切り出し & 委譲ブリッジ
【Flash 実行部隊 ➔ 上位モデル設計部隊 の連携基盤】

役割:
1. Flash では型エラーやバグを踏みやすい高難度タスク (JPEG バイナリパース、巨大アップロード先頭切断等) を特定。
2. 現在の HEAD コード、過去の experiments.json 失敗ログ、上位陣ブログの仕様を自動収集。
3. 上位モデル (isucon-lead-architect) 向けの「完全自己完結ブリーフィング (prompt_for_pro.md)」を生成。
4. 上位モデルが生成したコードブロックをチェックリスト (critic_reviewer.py) に自動登録。
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
ARENA_DIR = REPO_ROOT / "arena"
HARNESS_DIR = REPO_ROOT / "harness"
BRIEFING_FILE = HARNESS_DIR / "prompt_for_lead_architect.md"

# 明日のリセット後に上位モデルへ依頼する 3 大キラータスクの定義
HARD_TASKS = {
    "jpeg_sof0_header_parser": {
        "title": "JPEG ピクセルデコード完全省略 (SOF0マーカー高速バイナリパース)",
        "source_teams": "4位 チーム54、6位 チーム釜中の鯖 (+17.6%)",
        "description": (
            "jpeg.Decode による画像ピクセル全体のデコードを全廃し、"
            "JPEG バイナリから SOI (0xFF 0xD8) を確認後、SOF0 マーカー (0xFF 0xC0) を探して"
            "画像の幅と高さを直接バイナリパースする極小 Go 関数を生成する。"
        ),
        "target_file": "webapp-go/image_util.go または arena/node_server.go",
    },
    "fast_reject_oversized_upload": {
        "title": "不正画像アップロードの先頭数バイト即切断 (10〜32MB 受信防止)",
        "source_teams": "優勝 Typing Bears (kawasy 氏 625k)、4位 チーム54",
        "description": (
            "ベンチマーカーが送ってくる 10〜32MB の不正画像リクエストや BAN 済み IP に対し、"
            "Content-Length > 10MB の早期判定、および先頭数バイトの JPEG マジックナンバー確認で、"
            "ボディを最後まで読まずに即座に接続を切断 (413 / 400) してルーター帯域 6 割浪費を遮断する。"
        ),
        "target_file": "webapp-go/post_api.go または arena/node_server.go",
    },
    "exp_minute_rounding_cache": {
        "title": "画像署名有効期限の分単位・時間単位丸めキャッシュ",
        "source_teams": "6位 チーム釜中の鯖 (+30.9% ゲイン)",
        "description": (
            "画像の署名有効期限 (exp) を「今から 24 時間後の秒単位」ではなく、"
            "1 分単位 (または 1 時間単位) に切り上げて署名を生成・検証することで、"
            "同一画像の署名が 1 分間完全に固定され、インメモリキャッシュが 100% ヒットし続けるロジックを設計する。"
        ),
        "target_file": "webapp-go/feed_api.go",
    }
}


def prepare_briefing(task_id: str) -> str:
    """指定タスクの上位モデル向け高精度ブリーフィング Markdown を生成"""
    if task_id not in HARD_TASKS:
        raise ValueError(f"Unknown task_id: {task_id}. Available: {list(HARD_TASKS.keys())}")

    task = HARD_TASKS[task_id]
    ns_target = ARENA_DIR / "node_server.go"
    ns_code = ns_target.read_text(encoding="utf-8") if ns_target.exists() else ""

    # 直近の実験失敗ログを抽出
    recent_errors = []
    exp_file = HARNESS_DIR / "experiments.json"
    if exp_file.exists():
        try:
            with open(exp_file, "r", encoding="utf-8") as f:
                hist = json.load(f)
                fails = [r for r in hist if r.get("status") in ("BUILD_FAIL", "BENCH_FAIL", "REJECTED")][-3:]
                for fa in fails:
                    recent_errors.append(f"- #{fa.get('id')}: {fa.get('title')} -> {fa.get('status')} ({fa.get('gain_pct', 0)}%)")
        except Exception:
            pass

    briefing = f"""# 上位モデル（Pro / Claude / Argon）向け設計依頼書: {task['title']}

## 1. タスク概要
- **タスク ID**: `{task_id}`
- **施策名**: {task['title']}
- **参考実績**: {task['source_teams']}
- **目的**: {task['description']}
- **対象ファイル**: `{task['target_file']}`

## 2. 直近のハーネス測定状況と注意点
{chr(10).join(recent_errors) if recent_errors else "直近の重大エラーはありません。"}

【Echo v5 および Go の厳格な制約事項】
- `c.Response()` 自体が `http.ResponseWriter` を実装しています（`c.Response().Writer` は未定義エラー）。
- `e.Server` フィールドは存在しません（`http.Server` を直接構成する必要があります）。
- Flash ワーカーが確実に自動適用できるよう、**「Target (完全一致する置換前のコード)」** と **「Replacement (置換後の確定コード)」** の 2 つのコードブロックを過不足なく出力してください。

## 3. 現在の HEAD コードベース (`arena/node_server.go`抜粋)
```go
{ns_code[:2500]}
// ... (中略) ...
```

## 4. 期待する出力フォーマット
上位モデルは以下のフォーマットで Flash 向けに最終確定コードを出力してください：

```markdown
### 設計根拠
(数行でアルゴリズムや型安全性の根拠を説明)

### パッチ適用仕様
**Target:**
```go
// 置換対象の正確なコード
```

**Replacement:**
```go
// 置換後の確定コード (アトミック・自己完結)
```
```
"""
    BRIEFING_FILE.write_text(briefing, encoding="utf-8")
    print(f"[+] Briefing generated: {BRIEFING_FILE}")
    return briefing


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Brain Delegator for High-Tier Models")
    parser.add_argument("task", choices=list(HARD_TASKS.keys()) + ["all"], help="Task ID to prepare")
    args = parser.parse_args()

    if args.task == "all":
        for tid in HARD_TASKS:
            prepare_briefing(tid)
    else:
        prepare_briefing(args.task)
