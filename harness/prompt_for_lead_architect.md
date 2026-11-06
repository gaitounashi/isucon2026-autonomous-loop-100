# 上位モデル（Pro / Claude / Argon）向け設計依頼書: 不正画像アップロードの先頭数バイト即切断 (10〜32MB 受信防止)

## 1. タスク概要
- **タスク ID**: `fast_reject_oversized_upload`
- **施策名**: 不正画像アップロードの先頭数バイト即切断 (10〜32MB 受信防止)
- **参考実績**: 優勝 Typing Bears (kawasy 氏 625k)、4位 チーム54
- **目的**: ベンチマーカーが送ってくる 10〜32MB の不正画像リクエストや BAN 済み IP に対し、Content-Length > 10MB の早期判定、および先頭数バイトの JPEG マジックナンバー確認で、ボディを最後まで読まずに即座に接続を切断 (413 / 400) してルーター帯域 6 割浪費を遮断する。
- **対象ファイル**: `webapp-go/post_api.go または arena/node_server.go`

## 2. 直近のハーネス測定状況と注意点
- #49: direct zero-copy ResponseWriter and path slice for image endpoint -> REJECTED (1.61%)
- #50: zero-alloc image endpoint (path slice + inline rawQuery parsing + direct ResponseWriter) -> REJECTED (1.96%)
- #51: combo: zero-alloc image endpoint (path slice + inline rawQuery) AND tuned http.Server (IdleTimeout + ReadHeaderTimeout) -> REJECTED (-0.64%)

【Echo v5 および Go の厳格な制約事項】
- `c.Response()` 自体が `http.ResponseWriter` を実装しています（`c.Response().Writer` は未定義エラー）。
- `e.Server` フィールドは存在しません（`http.Server` を直接構成する必要があります）。
- Flash ワーカーが確実に自動適用できるよう、**「Target (完全一致する置換前のコード)」** と **「Replacement (置換後の確定コード)」** の 2 つのコードブロックを過不足なく出力してください。

## 3. 現在の HEAD コードベース (`arena/node_server.go`抜粋)
```go
package main

import (
	"strings"
	"sync/atomic"
	"runtime/debug"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
)

var (
	signingKey      = []byte("secret-key-32bytes-padding!!!!!!")
	dummyImageBytes = make([]byte, 64*1024)
	fixedGlobalExp int64
	fixedGlobalExpStr string
	precomputedSigs = make(map[string][sha256.Size]byte, 256)
	precomputedSigStrs = make(map[string]string, 4096)
	precomputedFeedJSONs = make(map[string][]byte)
	precomputedRedirects = make(map[string]string, 4096)
	precomputedSigArr [200]string
	precomputedRedirArr [200]string
	inFlightAsia int32
)

func init() {
	fixedGlobalExp = time.Now().Add(48 * time.Hour).Unix()
	fixedGlobalExpStr = strconv.FormatInt(fixedGlobalExp, 10)
}

func precomputeSignature(key []byte, imageID string) {
	value := []byte(strconv.FormatInt(fixedGlobalExp, 10) + ":" + imageID)
	for range 1000 {
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		value = mac.Sum(nil)
	}
	var sum [sha256.Size]byte
	copy(sum[:], value)
	precomputedSigs[imageID] = sum
	sigStr := base64.RawURLEncoding.EncodeToString(sum[:])
	precomputedSigStrs[imageID] = sigStr
	if idx := imageIndex(imageID); idx >= 0 {
		precomputedSigArr[idx] = sigStr
	}
}

func imageIndex(id string) int {
	if len(id) != 10 || id[:6] != "image-" {
		return -1
	}
	n := 0
	for i := 6; i < 10; i++ {
		ch := id[i]
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	if n >= 200 {
		return -1
	}
	return n
}

func fastVerifySigString(imageID string, exp int64, sigStr string) bool {
	if exp == fixedGlobalExp {
		expected, ok := precomputedSigStrs[imageID]
		if ok {
			return expected == sigStr
		}
	}
	return false
}

func referenceImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	for range 1000 {
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		value = mac.Sum(nil)
	}
	return value
}

func initPrecomputedFeeds() {
	for i := 0; i < 200; i++ {
		imgID := fmt.Sprintf("image-%04d", i)
		precomputedRedirects[imgID] = "/_imgcache/" + imgID + ".jpg"
		precomputedRedirArr[i] = "/_imgcache/" + imgID + ".jpg"
	}
	for _, reg := range []string{"asia1", "europe1", "america1"} {
		imageURLs := make([]string, 5)
		for i := 0; i < 5; i++ {
			imgID := fmt.Sprintf("image-%04d", i)
			sum := precomputedSigs[imgID]
			expStr := strconv.FormatInt(fixedGlobalExp, 10)
			sigStr := base64.RawURLEncoding.Encode
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
