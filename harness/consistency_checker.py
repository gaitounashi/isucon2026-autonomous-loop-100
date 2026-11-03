"""
consistency_checker.py - ISUReal API Consistency & Correctness Validator

AIによるコード改修が仕様を破壊していないか（失格条件に抵触していないか）を
高速に検証する整合性ゲート。
"""

import urllib.request
import urllib.error
import json
import time
import sys

def check_endpoint(url: str, expected_status: int = 200, headers: dict = None, method: str = "GET", data: bytes = None):
    req = urllib.request.Request(url, data=data, headers=headers or {}, method=method)
    try:
        with urllib.request.urlopen(req, timeout=3.0) as resp:
            body = resp.read()
            return {
                "ok": resp.status == expected_status,
                "status": resp.status,
                "headers": dict(resp.headers),
                "body_len": len(body),
                "error": None
            }
    except urllib.error.HTTPError as e:
        return {
            "ok": e.code == expected_status,
            "status": e.code,
            "headers": dict(e.headers),
            "body_len": 0,
            "error": f"HTTP {e.code}"
        }
    except Exception as e:
        return {
            "ok": False,
            "status": 0,
            "headers": {},
            "body_len": 0,
            "error": str(e)
        }

def run_consistency_suite(base_url: str = "http://localhost:8080") -> dict:
    """主要エンドポイントの整合性を一括検証"""
    results = []
    
    # 1. ヘルスチェック / ルート
    res = check_endpoint(f"{base_url}/")
    results.append(("Root / Healthcheck", res))
    
    # 2. 不正な署名での画像取得 (403 Forbidden になるべき)
    res_invalid_sig = check_endpoint(f"{base_url}/api/images/test.jpg?sig=invalid_signature&exp=9999999999", expected_status=403)
    results.append(("Image API Invalid Signature Check", res_invalid_sig))
    
    # 3. 期限切れ署名での画像取得 (403 Forbidden になるべき)
    res_expired = check_endpoint(f"{base_url}/api/images/test.jpg?sig=dummy&exp=1000", expected_status=403)
    results.append(("Image API Expired Signature Check", res_expired))
    
    # 判定
    all_passed = all(r[1]["ok"] for r in results)
    failures = [name for name, r in results if not r[1]["ok"]]
    
    return {
        "passed": all_passed,
        "total_checks": len(results),
        "failures": failures,
        "details": results
    }

if __name__ == "__main__":
    base = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8080"
    print(f"[*] Running consistency suite against {base}...")
    suite_res = run_consistency_suite(base)
    if suite_res["passed"]:
        print(f"[+] All {suite_res['total_checks']} consistency checks PASSED.")
        sys.exit(0)
    else:
        print(f"[-] Consistency checks FAILED! Failures: {suite_res['failures']}")
        sys.exit(1)
