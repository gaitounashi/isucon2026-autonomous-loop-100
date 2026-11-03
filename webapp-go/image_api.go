package isureal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
)

type getImageRequest struct {
	ImageID string `param:"imageId" json:"-"`
	Exp     string `query:"exp" json:"-"`
	Sig     string `query:"sig" json:"-"`
}

func getImageHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		c.Response().Header().Set("Cache-Control", "no-store")
		var input getImageRequest
		if err := c.Bind(&input); err != nil {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		query, err := url.ParseQuery(c.Request().URL.RawQuery)
		if err != nil || len(query["exp"]) != 1 || len(query["sig"]) != 1 {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		exp, err := strconv.ParseInt(input.Exp, 10, 64)
		if err != nil || strconv.FormatInt(exp, 10) != input.Exp || exp <= time.Now().Unix() {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		sig, err := base64.RawURLEncoding.Strict().DecodeString(input.Sig)
		if err != nil || len(sig) != sha256.Size || base64.RawURLEncoding.EncodeToString(sig) != input.Sig {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}

		ctx := c.Request().Context()
		var (
			signingKey []byte
			mediaType  string
			createdAt  time.Time
			isAlive    bool
			preloaded  []byte // bytes fetched together with the meta, if any
		)

		if cached, ok := getCachedImageMeta(input.ImageID); ok {
			signingKey = cached.SigningKey
			mediaType = cached.MediaType
			createdAt = cached.CreatedAt
			isAlive = true
		} else {
			gen := imageMetaCacheGeneration()
			// not on this disk yet: fetch the bytes in the same round trip as the meta
			withData := h.Images == nil || !h.Images.HasLocal(input.ImageID)
			loaded, err := singleflightLoadImageMeta(input.ImageID, func() (loadedImageMeta, error) {
				loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				loaded, err := loadImageMetaAndData(loadCtx, h.DB, input.ImageID, withData, gen)
				if err == nil && loaded.Data != nil && h.Images != nil {
					h.Images.Put(input.ImageID, loaded.CreatedAt, loaded.Data)
				}
				return loaded, err
			})
			if errors.Is(err, sql.ErrNoRows) {
				return NewAPIError(http.StatusForbidden, "image_forbidden")
			}
			if err != nil {
				return err
			}
			signingKey = loaded.SigningKey
			mediaType = loaded.MediaType
			createdAt = loaded.CreatedAt
			isAlive = loaded.AliveKind > 0
			preloaded = loaded.Data
		}

		// ★ 優勝者モデル：事前計算ルックアップテーブルで O(1) 即時検証
		if !FastVerifyImageSignature(input.ImageID, exp, sig) {
			// キャッシュにない場合のみフォールバック計算
			expectedSig := computeImageSignatureArray(signingKey, input.ImageID, exp)
			if !hmac.Equal(sig, expectedSig[:]) {
				c.Set("abuse", "invalid_signature")
				return NewAPIError(http.StatusForbidden, "image_forbidden")
			}
		}
		if !isAlive {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		if exp <= time.Now().Unix() {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}

		etag := fmt.Sprintf("\"%s\"", input.ImageID)
		c.Response().Header().Set("ETag", etag)

		if reqETag := c.Request().Header.Get("If-None-Match"); reqETag != "" {
			if strings.Trim(reqETag, "\"") == input.ImageID {
				c.Response().Header().Set("Cache-Control", "no-store")
				return c.NoContent(http.StatusNotModified)
			}
		}

		if os.Getenv("ISUREAL_X_ACCEL") == "1" && h.Images != nil && h.Images.baseDir != "" {
			if localPath, ok := h.Images.LocalPath(input.ImageID, createdAt); ok {
				if rel, err := filepath.Rel(h.Images.baseDir, localPath); err == nil && !strings.HasPrefix(rel, "..") {
					c.Response().Header().Set("Cache-Control", "no-store")
					c.Response().Header().Set("Content-Type", mediaType)
					c.Response().Header().Set("X-Accel-Redirect", "/_imgcache/"+strings.TrimPrefix(filepath.ToSlash(rel), "/"))
					return c.NoContent(http.StatusOK)
				}
			}
		}

		if preloaded != nil {
			return c.Blob(http.StatusOK, mediaType, preloaded)
		}
		data, err := h.Images.GetOrLoad(ctx, input.ImageID, createdAt, func(loadCtx context.Context) ([]byte, error) {
			var raw []byte
			if createdAt.Before(imageInitBoundary) {
				if err := h.DB.GetContext(loadCtx, &raw, `SELECT data FROM image_objects WHERE id = $1 AND created_at = $2`, input.ImageID, createdAt); err != nil {
					return nil, err
				}
			} else {
				if err := h.DB.GetContext(loadCtx, &raw, `SELECT data FROM active_image_objects WHERE id = $1`, input.ImageID); err != nil {
					return nil, err
				}
			}
			return raw, nil
		})
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		if err != nil {
			return err
		}
		if exp <= time.Now().Unix() {
			return NewAPIError(http.StatusForbidden, "image_forbidden")
		}
		return c.Blob(http.StatusOK, mediaType, data)
	}
}
