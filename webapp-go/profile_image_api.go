package isureal

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

type profileImageResult struct {
	Status       string     `db:"status"`
	OldImageID   *string    `db:"old_image_id"`
	OldCreatedAt *time.Time `db:"old_created_at"`
}

func putProfileImageHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if c.Request().Header.Get("Content-Type") != "image/jpeg" {
			return NewAPIError(http.StatusUnsupportedMediaType, "unsupported_media_type")
		}
		data, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			c.Set("abuse", "invalid_image")
			return NewAPIError(http.StatusBadRequest, "invalid_profile_image")
		}
		if img.Bounds().Dx() > 512 || img.Bounds().Dy() > 512 {
			return NewAPIError(http.StatusBadRequest, "invalid_profile_image")
		}

		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		imageID := newUUID()
		now := time.Now().UTC()
		h.Prefetch.MarkLocal(imageID)
		defer h.Prefetch.UnmarkLocal(imageID)

		var res profileImageResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, old_image_id, old_created_at
			FROM isureal_put_profile_image($1, $2, $3, $4, $5)
		`, userID, imageID, data, now, imageInitBoundary); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			// proceed
		case "account_not_found":
			return NewAPIError(http.StatusNotFound, "account_not_found")
		default:
			return fmt.Errorf("unexpected profile image status: %s", res.Status)
		}

		InvalidateSessionsForUser(userID) // also drops this user's image meta (old profile image)
		h.Images.Put(imageID, now, data)
		if key := GetSession(c).User.ImageKey; len(key) > 0 {
			setCachedImageMeta(imageID, cachedImageMeta{OwnerUserID: userID, SigningKey: key, MediaType: "image/jpeg", CreatedAt: now}, imageMetaCacheGeneration())
		}
		if res.OldImageID != nil && res.OldCreatedAt != nil && !res.OldCreatedAt.Before(imageInitBoundary) {
			h.Images.Delete(*res.OldImageID, *res.OldCreatedAt)
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func deleteProfileImageHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID

		var res profileImageResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, old_image_id, old_created_at
			FROM isureal_delete_profile_image($1, $2)
		`, userID, imageInitBoundary); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			// proceed
		case "account_not_found":
			return NewAPIError(http.StatusNotFound, "account_not_found")
		default:
			return fmt.Errorf("unexpected delete profile image status: %s", res.Status)
		}

		InvalidateSessionsForUser(userID)
		if res.OldImageID != nil && res.OldCreatedAt != nil && !res.OldCreatedAt.Before(imageInitBoundary) {
			h.Images.Delete(*res.OldImageID, *res.OldCreatedAt)
		}
		return c.NoContent(http.StatusNoContent)
	}
}
