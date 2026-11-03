package main

import (
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
)

func referenceImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	for range 1000 {
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		value = mac.Sum(nil)
	}
	return value
}

func main() {
	port := flag.Int("port", 8001, "listen port")
	region := flag.String("region", "asia1", "server region: asia1, europe1, america1")
	flag.Parse()

	e := echo.New()

	// 初期化エンドポイント
	e.POST("/initialize", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"status":   "ok",
			"language": "go",
			"region":   *region,
		})
	})

	// 画像取得エンドポイント (当日初期実装: 1000回HMAC署名検証 & 64KBコピー)
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
		expected := referenceImageSignature(signingKey, imageID, exp)
		if !hmac.Equal(sig, expected) {
			return c.String(http.StatusForbidden, "image_forbidden")
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.Blob(http.StatusOK, "image/jpeg", dummyImageBytes)
	})

	// フィード取得エンドポイント (当日初期実装: 欧米101ms遅延 & 動的署名生成)
	e.GET("/api/feed/:type", func(c *echo.Context) error {
		if *region == "europe1" || *region == "america1" {
			time.Sleep(101 * time.Millisecond)
		} else {
			time.Sleep(15 * time.Millisecond)
		}
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
			"region":     *region,
		})
	})

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("[*] Baseline Node Server started on %s (region: %s)\n", addr, *region)
	if err := e.Start(addr); err != nil {
		panic(err)
	}
}
