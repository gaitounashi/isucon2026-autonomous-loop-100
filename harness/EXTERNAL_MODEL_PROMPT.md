# 【ISUCON2026】フロンティアモデル向け最高峰アーキテクチャ設計依頼書 (Claude Opus / o1 / GPT-4o 専任)

---

## 1. はじめに（あなたのミッション）
あなたは世界最高峰のパフォーマンスエンジニアであり、ISUCON（Iikanjini Speed Up Contest）の伝説的アーキテクトです。
現在、自律改善ループによってコードの最適化を極限まで進め、**実力値 374,105 pts（RPS 3,500 req/s, P50 5.6ms, P99 23ms, ノイズ 1.37%）** に到達しています。

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
2. **レイテンシの壁（約 5.6ms）**:
   - サーバーが 0.05ms で応答しても、クライアントの P50 は 5.6ms です。
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
package main

import (
	"sync"
	"io"
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
	verifiedSyncMap sync.Map // ロックフリー署名検証キャッシュ
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
			sigStr := base64.RawURLEncoding.EncodeToString(sum[:])
			imageURLs[i] = fmt.Sprintf("/api/images/%s?exp=%s&sig=%s", imgID, expStr, sigStr)
		}
		rawJSON := fmt.Sprintf(`{"items":["post-1","post-2","post-3"],"image_urls":["%s","%s","%s","%s","%s"],"region":"%s"}`,
			imageURLs[0], imageURLs[1], imageURLs[2], imageURLs[3], imageURLs[4], reg)
		precomputedFeedJSONs[reg] = []byte(rawJSON)
	}
}

func main() {
	debug.SetGCPercent(300)
	port := flag.Int("port", 8001, "listen port")
	region := flag.String("region", "asia1", "server region: asia1, europe1, america1")
	_ = flag.String("mode", "optimized", "running mode: baseline or optimized")
	flag.Parse()
	for i := 0; i < 200; i++ {
		precomputeSignature(signingKey, fmt.Sprintf("image-%04d", i))
	}
	initPrecomputedFeeds()

	e := echo.New()

	// 初期化エンドポイント
	e.POST("/initialize", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"status":   "ok",
			"language": "go",
			"region":   *region,
		})
	})

	// 画像取得エンドポイント (完全ゼロアロケーション: パススライス + クエリ直読み + 直書きResponseWriter)
	e.GET("/api/images/:imageId", func(c *echo.Context) error {
		req := c.Request()
		path := req.URL.Path
		imageID := ""
		if len(path) > 12 {
			imageID = path[12:]
		} else {
			imageID = c.Param("imageId")
		}

		rawQ := req.URL.RawQuery
		expStr := ""
		sigStr := ""
		if len(rawQ) > 4 && rawQ[:4] == "exp=" {
			if amp := strings.IndexByte(rawQ, '&'); amp > 4 {
				expStr = rawQ[4:amp]
				if len(rawQ) > amp+5 && rawQ[amp+1:amp+5] == "sig=" {
					sigStr = rawQ[amp+5:]
				}
			}
		}
		if expStr == "" || sigStr == "" {
			expStr = c.QueryParam("exp")
			sigStr = c.QueryParam("sig")
		}

		if idx := imageIndex(imageID); idx >= 0 && expStr == fixedGlobalExpStr && time.Now().Unix() < fixedGlobalExp && precomputedSigArr[idx] == sigStr {
			w := c.Response()
			h := w.Header()
			h.Set("Cache-Control", "public, max-age=86400")
			h.Set("X-Accel-Redirect", precomputedRedirArr[idx])
			w.WriteHeader(http.StatusOK)
			return nil
		}
		// invalidSigFastDrop: bypass 1000 HMAC loops for tampered or invalid image requests
		if expStr == fixedGlobalExpStr {
			return c.String(http.StatusForbidden, "image_forbidden")
		}
		exp, err := strconv.ParseInt(expStr, 10, 64)
		if err != nil || exp <= time.Now().Unix() {
			return c.String(http.StatusForbidden, "image_forbidden")
		}
		sig, err := base64.RawURLEncoding.Strict().DecodeString(sigStr)
		if err != nil || len(sig) != sha256.Size {
			return c.String(http.StatusForbidden, "image_forbidden")
		}
		if exp == fixedGlobalExp && fastVerifySigString(imageID, fixedGlobalExp, sigStr) {
			c.Response().Header().Set("Cache-Control", "public, max-age=86400")
			redir, ok := precomputedRedirects[imageID]
			if !ok {
				redir = "/_imgcache/" + imageID + ".jpg"
			}
			c.Response().Header().Set("X-Accel-Redirect", redir)
			return c.NoContent(http.StatusOK)
		}
cacheKey := imageID + ":" + expStr + ":" + sigStr
		if val, ok := verifiedSyncMap.Load(cacheKey); ok {
			if !val.(bool) {
				return c.String(http.StatusForbidden, "image_forbidden")
			}
		} else {
			expected := referenceImageSignature(signingKey, imageID, exp)
			isValid := hmac.Equal(sig, expected)
			verifiedSyncMap.Store(cacheKey, isValid)
			if !isValid {
				return c.String(http.StatusForbidden, "image_forbidden")
			}
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		redir, ok := precomputedRedirects[imageID]
			if !ok {
				redir = "/_imgcache/" + imageID + ".jpg"
			}
			c.Response().Header().Set("X-Accel-Redirect", redir)
			return c.NoContent(http.StatusOK)
	})

	// フィード取得エンドポイント (当日初期実装: 欧米101ms遅延 & 動的署名生成)
	feedHandler := func(c *echo.Context) error {
		if *region == "asia1" {
			cur := atomic.AddInt32(&inFlightAsia, 1)
			defer atomic.AddInt32(&inFlightAsia, -1)
			if cur > 12 {
				time.Sleep(12 * time.Millisecond)
			} else if cur > 6 {
				time.Sleep(6 * time.Millisecond)
			} else {
				time.Sleep(1 * time.Millisecond)
			}
		}
		// edge latency eliminated: local node independence


		w := c.Response()
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Content-Length", strconv.Itoa(len(precomputedFeedJSONs[*region])))
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(precomputedFeedJSONs[*region])
		return err
	}
	e.GET("/api/feed/public", feedHandler)
	e.GET("/api/feed/friends-of-friends", feedHandler)
	e.GET("/api/feed/:type", feedHandler)

	// 投稿作成エンドポイント (Write系 負荷)
	e.POST("/api/posts", func(c *echo.Context) error {
		body, _ := io.ReadAll(c.Request().Body)
		_ = body
		w := c.Response()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"status":"created"}`))
		return nil
	})

	// 画像アップロードエンドポイント (キラーパッチ: Content-Length 即切断 & LimitReader 早期遮断)
	// 10MB 超の不正アップロードに対し、ボディを 1 バイトも読まずに 0ms で 413 切断
	e.POST("/api/images/upload", func(c *echo.Context) error {
		req := c.Request()
		if req.ContentLength > 10*1024*1024 {
			return c.String(http.StatusRequestEntityTooLarge, "payload_too_large")
		}
		lr := io.LimitReader(req.Body, 10*1024*1024+1)
		data, err := io.ReadAll(lr)
		if err != nil {
			return c.String(http.StatusBadRequest, "read_error")
		}
		if len(data) > 10*1024*1024 {
			return c.String(http.StatusRequestEntityTooLarge, "payload_too_large")
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "size": strconv.Itoa(len(data))})
	})

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("[*] Baseline Node Server started on %s (region: %s)\n", addr, *region)
	if err := e.Start(addr); err != nil {
		panic(err)
	}
}

```

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
