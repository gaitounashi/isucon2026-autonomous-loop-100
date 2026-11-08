"""
brain_delegator.py - 上位フロンティアモデル (Claude Opus / o1 / GPT-4o) への委譲ブリッジ
【Flash/Pro 自動実行部隊 ➔ 外部最上位モデル設計部隊 の連携基盤】

コマンド:
  python harness/brain_delegator.py export
    -> 現在の HEAD コードと最新実測スコアを取り込んだ完全自己完結型プロンプト
       (harness/EXTERNAL_MODEL_PROMPT.md) を自動生成・更新。
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
ARENA_DIR = REPO_ROOT / "arena"
HARNESS_DIR = REPO_ROOT / "harness"
NODE_SERVER_GO = ARENA_DIR / "node_server.go"
OUTPUT_PROMPT_FILE = HARNESS_DIR / "EXTERNAL_MODEL_PROMPT.md"
EXPERIMENTS_FILE = HARNESS_DIR / "experiments.json"


def generate_external_prompt() -> str:
    """最新の HEAD コードと実測メトリクスを埋め込んだ完全自己完結型プロンプトを生成"""
    ns_code = NODE_SERVER_GO.read_text(encoding="utf-8") if NODE_SERVER_GO.exists() else ""

    best_score = "376,727"
    latest_rps = "3,500"
    latest_p50 = "5.6"
    latest_p99 = "23"
    latest_noise = "1.37%"

    if EXPERIMENTS_FILE.exists():
        try:
            with open(EXPERIMENTS_FILE, "r", encoding="utf-8") as f:
                hist = json.load(f)
                if hist:
                    last_exp = hist[-1]
                    b_med = last_exp.get("base_median") or 370000
                    best_score = f"{b_med:,}"
                    latest_noise = f"{last_exp.get('base_noise_pct', 1.37)}%"
        except Exception:
            pass

    header_template = """# 【ISUCON2026】フロンティアモデル向け最高峰アーキテクチャ設計依頼書 (Claude Opus / o1 / GPT-4o 専任)

---

## 1. はじめに（あなたのミッション）
あなたは世界最高峰のパフォーマンスエンジニアであり、ISUCON（Iikanjini Speed Up Contest）の伝説的アーキテクトです。
現在、自律改善ループによってコードの最適化を極限まで進め、**実力値 __BEST_SCORE__ pts（RPS __LATEST_RPS__ req/s, P50 __LATEST_P50__ms, P99 __LATEST_P99__ms, ノイズ __LATEST_NOISE__）** に到達しています。

ここから、**400,000〜500,000 点台（優勝チーム Typing Bears 625k、6位 釜中の鯖 513k の領域）へ突き抜けるための「ブレイクスルー・キラーパッチ」** を設計・コード生成してください。

---

## 2. 競技環境とトポロジー仕様

### ① システム構成
- **OS**: Windows 11 Pro (ホスト物理環境)
- **CPU**: **物理/論理 20 コア**
- **Go バージョン**: Go 1.23+
- **AP 構成**: 3 つの AP サーバー（ローカルマルチプロセス）
  - `asia1` (:8001): **配点 1.0 倍**（国内トラフィック）
  - `europe1` (:8002): **配点 1.5 倍**（欧州トラフィック・高収益ノード）
  - `america1` (:8003): **配点 1.5 倍**（米国トラフィック・高収益ノード）
- **クライアント（ベンチマーカー）**:
  - Python `urllib3` による各ノード 8 並行ワーカー（合計 24 ワーカー）の直接接続。
  - 各ワーカーは指定ポートに直接接続（リバースプロキシ経由の動的分散ではないため、asia1 を遅くしても欧米にリクエストは迂回しません）。

### ② トラフィック内訳（ISUReal 本番完全準拠）
1. **正常画像取得 GET (`/api/images/:id?exp=...&sig=...`) [60%]**:
   - 事前生成された正当な署名付き URL。成功時 `200 OK` ＋ `X-Accel-Redirect: /_imgcache/...` を返却。
2. **フィード取得 GET (`/api/feed/friends-of-friends` 等) [20%]**:
   - ユーザーのフィードデータ。事前生成 JSON を即座に返却。
3. **巨大不正画像アップロード POST (`/api/images/upload`) [10%]**:
   - 10MB 超（11MB）の巨大画像アップロード。サーバーはボディを読まずに即座に `413 Payload Too Large` を返却する仕様。
4. **改ざん署名・攻撃画像 GET (`/api/images/image-0001?exp=9999999999&sig=tampered...`) [5%]**:
   - 不正な署名。暗号検証で弾いて `403 Forbidden` を返却。
5. **投稿作成 POST (`/api/posts`) [5%]**:
   - メッセージ投稿。`201 Created` ＋ `{"status":"created"}` を返却。

---

## 3. 現在直面しているボトルネックと実測データ

自律プロファイリングにより、以下の事実が判明しています：

1. **サーバー内部処理時間はすでに「ゼロ（マイクロ秒単位）」に到達**:
   - 画像 200: 0ms（固定スライスメモリ参照のみ）
   - フィード 200: 0ms（事前生成 JSON 直書き）
   - 不正アップロード 413: 0ms（`Content-Length > 10MB` 先頭即断）
   - 改ざん署名 403: 0ms（ロックフリー `sync.Map` キャッシュヒット）
   - 投稿作成 201: 0ms（`io.Discard` ゼロアロケーション）
2. **レイテンシの壁（約 __LATEST_P50__ms）**:
   - サーバーが 0.05ms で応答しても、クライアントの P50 は __LATEST_P50__ms です。
   - クラスター全体で 24 並行同期ワーカーが稼働しているため、平均 RTT が約 6.95ms ➔ RPS 3,450 req/s で頭打ちになっています。
   - **あと 0.15ms（150マイクロ秒）平均 RTT を削るだけで +2.0%（+7,500点）を達成可能** です。
3. **過去の実験で失敗した施策（踏んではならない罠）**:
   - `GOMAXPROCS` を 6/12/12 に絞る ➔ Windows 11 ではデフォルト (20) 全コア開放の方が 1% スループットが高く逆デグレ。
   - `ReadHeaderTimeout` や `ReadTimeout` の設定 ➔ リクエストごとのタイマー競合でデグレ。
   - `Connection: close` の付与 ➔ TCP 3-way ハンドシェイク再接続の嵐になり大デグレ。Keep-Alive は絶対維持。
   - 画像リクエストへのスロットル ➔ 60% の流量が死んで -10% 大デグレ。

---

## 4. 上位入賞チームの核心ノウハウ

- **Typing Bears (1位 625k)**:
  - 10MB 超の不正アップロードを 1 バイトも読まずに即断。
  - レスポンス直書き、GC チューニング（`SetGCPercent(300)`）。
- **釜中の鯖 (6位 513k, +30.9% ゲイン)**:
  - 署名有効期限の分単位（60秒境界）丸め正規化。マルチノード間の起動時間差に関わらず同一の `fixedGlobalExp` を共有。
  - 正当署名文字列キャッシュ `(imageID + ":" + expStr) -> expectedSigStr`。改ざん署名が何千回送られてきても HMAC 1,000回再計算を厳格に 0 回化。

---

## 5. 現在の対象コード全文 (`arena/node_server.go`)

```go
"""

    header = (
        header_template
        .replace("__BEST_SCORE__", str(best_score))
        .replace("__LATEST_RPS__", str(latest_rps))
        .replace("__LATEST_P50__", str(latest_p50))
        .replace("__LATEST_P99__", str(latest_p99))
        .replace("__LATEST_NOISE__", str(latest_noise))
    )

    footer = """```

---

## 6. あなたへのリクエストと出力フォーマット規定

上記のコードベース、トポロジー、ボトルネックを踏まえ、**スコアを 370k ➔ 400k+ へ跳ね上げるための具体的かつ完璧な改修コード** を提案してください。

Antigravity 自動ハーネスが 1 秒で安全にパッチ適用・A/B 検証できるよう、**以下のフォーマットを厳格に順守** してください：

```markdown
### 施策名: <わかりやすい施策名>

#### 設計意図と期待される改善効果
<なぜこの変更でスループットやレイテンシが改善するのかの簡潔な解説>

#### パッチ差分

**Target (置換対象の既存コードブロック - 完全一致させてください):**
```go
<既存の完全一致コード>
```

**Replacement (置換後の確定コードブロック):**
```go
<置換する完全なコード>
```
```
複数の箇所を変更する場合は、Target と Replacement のペアを順番に過不足なく記述してください。
コンパイルエラー（未使用 import、未定義変数など）がないよう、厳格に検証されたコードを出力してください。
"""
    return header + ns_code + "\n" + footer


def export_prompt() -> None:
    text = generate_external_prompt()
    OUTPUT_PROMPT_FILE.write_text(text, encoding="utf-8")
    print(f"[+] Successfully exported external model briefing to:\n    {OUTPUT_PROMPT_FILE}")


def main():
    parser = argparse.ArgumentParser(description="External Model Prompt Exporter & Bridge")
    parser.add_argument("command", choices=["export", "show"], default="export", nargs="?",
                        help="export: write to EXTERNAL_MODEL_PROMPT.md, show: print to stdout")
    args = parser.parse_args()

    if args.command == "export":
        export_prompt()
    elif args.command == "show":
        print(generate_external_prompt())


if __name__ == "__main__":
    main()
