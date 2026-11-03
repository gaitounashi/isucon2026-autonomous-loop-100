package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
)

const imageSignatureIterations = 1000

func referenceImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	for range imageSignatureIterations {
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		value = mac.Sum(nil)
	}
	return value
}

func main() {
	e := echo.New()
	signingKey := []byte("secret-key-32bytes-padding!!!!!!")
	dummyImageBytes := make([]byte, 64*1024) // 64KB dummy image

	// 画像取得エンドポイント (当日コード: 1000回HMAC検証 + Goでの画像バイナリ転送)
	e.GET("/api/images/:imageId", func(c *echo.Context) error {
		imageID := c.Param("imageId")
		expStr := c.QueryParam("exp")
		sigStr := c.QueryParam("sig")

		exp, err := strconv.ParseInt(expStr, 10, 64)
		if err != nil || exp <= time.Now().Unix() {
			return c.String(http.StatusForbidden, "image_forbidden")
		}

		sig, err := base64.RawURLEncoding.Strict().DecodeString(sigStr)
		if err != nil || len(sig) != sha256.Size {
			return c.String(http.StatusForbidden, "image_forbidden")
		}

		// 1000回HMACループ検証 (CPU激重)
		expected := referenceImageSignature(signingKey, imageID, exp)
		if !hmac.Equal(sig, expected) {
			return c.String(http.StatusForbidden, "image_forbidden")
		}

		// Go が直接画像バイナリをレスポンス
		c.Response().Header().Set("Cache-Control", "no-store")
		c.Response().Header().Set("ETag", fmt.Sprintf("\"%s\"", imageID))
		return c.Blob(http.StatusOK, "image/jpeg", dummyImageBytes)
	})

	// フィード取得エンドポイント (当日コード: 欧米からのアクセス時にアジアDBへの101ms RTT遅延が直撃)
	e.GET("/api/feed/:type", func(c *echo.Context) error {
		region := c.Request().Header.Get("X-Region")
		if region == "europe1" || region == "america1" {
			time.Sleep(101 * time.Millisecond)
		} else {
			time.Sleep(15 * time.Millisecond)
		}
		// フィード内の各投稿に紐づく画像URLを当日コードの方式で生成
		exp := time.Now().Unix() + 3600
		imageURLs := make([]string, 5)
		for i := 0; i < 5; i++ {
			imgID := fmt.Sprintf("image-%04d", i)
			sig := referenceImageSignature(signingKey, imgID, exp)
			sigStr := base64.RawURLEncoding.EncodeToString(sig)
			imageURLs[i] = fmt.Sprintf("/api/images/%s?exp=%d&sig=%s", imgID, exp, sigStr)
		}
		return c.JSON(http.StatusOK, map[string]any{
			"items":      []string{"post-1", "post-2", "post-3"},
			"image_urls": imageURLs,
			"hasMore":    false,
		})
	})

	fmt.Println("[*] Baseline Server (当日コード) listening on :9001")
	if err := e.Start(":9001"); err != nil {
		panic(err)
	}
}
