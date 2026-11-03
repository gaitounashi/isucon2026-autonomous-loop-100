"""
profile_analyzer.py - Go pprof & Benchmark Log Parser for AI Autonomous Loop

pprof プロファイルや Go ベンチマークログを解析し、
最もリソース（CPU/メモリ）を消費している上位関数を特定して
AIエージェント向けのコンパクトな Markdown 指示書を生成する。
"""

import re
import json

def parse_pprof_text(pprof_output: str, top_n: int = 5) -> list[dict]:
    """pprof のテキスト出力 (top) から上位関数を抽出"""
    entries = []
    lines = pprof_output.strip().split("\n")
    
    # 例: 1200ms  45.5%  45.5%     1200ms  45.5%  example.com/isureal-company/isureal.SignImageURL
    pattern = re.compile(r"^\s*([0-9\.]+[a-zµs]+)\s+([0-9\.]+%)\s+([0-9\.]+%)\s+([0-9\.]+[a-zµs]+)\s+([0-9\.]+%)\s+(.+)$")
    
    for line in lines:
        m = pattern.match(line)
        if m:
            entries.append({
                "flat": m.group(1),
                "flat_pct": m.group(2),
                "sum_pct": m.group(3),
                "cum": m.group(4),
                "cum_pct": m.group(5),
                "function": m.group(6).strip()
            })
            if len(entries) >= top_n:
                break
    return entries

def generate_ai_prompt_context(top_bottlenecks: list[dict], current_score: int, failure_history: list = None) -> str:
    """AI に渡すための最適化指示 Markdown を生成"""
    md = []
    md.append("## 自律最適化エージェントへの改善指示")
    md.append(f"- **現在のスコア**: `{current_score}`")
    md.append("")
    md.append("### 検出されたホットスポット（CPU消費上位関数）")
    if not top_bottlenecks:
        md.append("- （明示的なCPU集中は未検出、または全般的なレイテンシ最適化が必要）")
    else:
        for i, b in enumerate(top_bottlenecks, 1):
            md.append(f"{i}. **`{b['function']}`**")
            md.append(f"   - 自己消費 (Flat): `{b['flat']}` ({b['flat_pct']})")
            md.append(f"   - 累積消費 (Cum):  `{b['cum']}` ({b['cum_pct']})")
    
    if failure_history:
        md.append("")
        md.append("### 直近の失敗・ロールバック履歴（同じ過ちを繰り返さないこと）")
        for fail in failure_history[-3:]:
            md.append(f"- 原因: `{fail.get('reason')}` | 詳細: `{fail.get('detail', '')[:120]}...`")

    md.append("")
    md.append("### 指示事項")
    md.append("1. 上記のホットスポット関数に対して、アルゴリズムの計算量削減、事前計算、キャッシュ化、または不要な処理の排除を行ってください。")
    md.append("2. 既存の API 契約（ステータスコード、レスポンス構造、暗号署名検証の整合性）を絶対に壊さないでください。")
    md.append("3. 修正は最小限の差分で記述してください。")
    return "\n".join(md)

if __name__ == "__main__":
    dummy_pprof = """
Showing nodes accounting for 2.50s, 95.0% of 2.63s total
Dropped 10 nodes (cum <= 0.01s)
      flat  flat%   sum%        cum   cum%
     1.80s 68.44% 68.44%      1.80s 68.44%  example.com/isureal-company/isureal.SignImageURL
     0.45s 17.11% 85.55%      0.45s 17.11%  crypto/hmac.(*hmac).Write
     0.15s  5.70% 91.25%      2.40s 91.25%  example.com/isureal-company/isureal.getImageHandler
     0.10s  3.80% 95.05%      0.10s  3.80%  runtime.mallocgc
    """
    bottlenecks = parse_pprof_text(dummy_pprof)
    prompt = generate_ai_prompt_context(bottlenecks, 12500)
    print(prompt)
