package isureal

import (
	"bytes"
	"database/sql"
	"fmt"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

type replaceRealMojiRequest struct {
	PostID       string                `param:"postId" json:"-"`
	Image        *multipart.FileHeader `form:"image"`
	ReactionType string                `form:"reactionType"`
}

type replaceRealMojiResponse struct {
	ID           string                   `json:"id"`
	Reactor      relationshipUserResponse `json:"reactor"`
	ReactionType string                   `json:"reactionType"`
	ImageURL     string                   `json:"imageUrl"`
	CreatedAt    time.Time                `json:"createdAt"`
}

func InvalidateRealMojisCacheForPost(postID string) {
}

func InvalidateAllRealMojisCache() {
}

func newRealMojiResponse(reaction RealMoji, user User) replaceRealMojiResponse {
	return replaceRealMojiResponse{ID: reaction.ID, Reactor: newRelationshipUserResponse(user), ReactionType: reaction.ReactionType, ImageURL: signedImageURL(user, reaction.ImageID), CreatedAt: reaction.CreatedAt}
}

type replaceRealMojiResult struct {
	Status                string     `db:"status"`
	ReactionID            *string    `db:"reaction_id"`
	ReactionCreatedAt     *time.Time `db:"reaction_created_at"`
	ReactorID             *string    `db:"reactor_id"`
	ReactorUsername       *string    `db:"reactor_username"`
	ReactorSecretKeyHash  *string    `db:"reactor_secret_key_hash"`
	ReactorDisplayName    *string    `db:"reactor_display_name"`
	ReactorBio            *string    `db:"reactor_bio"`
	ReactorProfileImageID *string    `db:"reactor_profile_image_id"`
	ReactorRegion         *string    `db:"reactor_region"`
	ReactorAccountType    *string    `db:"reactor_account_type"`
	ReactorStatus         *string    `db:"reactor_status"`
}

func replaceRealMojiHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input replaceRealMojiRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if input.Image == nil {
			return NewAPIError(http.StatusBadRequest, "invalid_realmoji")
		}
		switch input.ReactionType {
		case "like", "smile", "surprise", "heart", "laugh", "instant":
		default:
			return NewAPIError(http.StatusBadRequest, "invalid_realmoji")
		}
		file, err := input.Image.Open()
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_realmoji")
		}
		if cfg.Width > 512 || cfg.Height > 512 {
			return NewAPIError(http.StatusBadRequest, "invalid_realmoji")
		}

		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		imageID := newUUID()
		now := time.Now().UTC()
		h.Prefetch.MarkLocal(imageID)
		defer h.Prefetch.UnmarkLocal(imageID)
		reactionID := newUUID()

		var res replaceRealMojiResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, reaction_id, reaction_created_at, reactor_id, reactor_username,
			       reactor_secret_key_hash, reactor_display_name, reactor_bio, reactor_profile_image_id,
			       reactor_region, reactor_account_type, reactor_status
			FROM isureal_replace_realmoji($1, $2, $3, $4, $5, $6, $7)
		`, input.PostID, actor, input.ReactionType, reactionID, imageID, data, now); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			// proceed
		case "interaction_forbidden":
			return NewAPIError(http.StatusForbidden, "interaction_forbidden")
		case "relationship_not_found":
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		default:
			return fmt.Errorf("unexpected realmoji status: %s", res.Status)
		}

		actorUser := User{
			ID:             *res.ReactorID,
			Username:       *res.ReactorUsername,
			SecretKeyHash:  *res.ReactorSecretKeyHash,
			DisplayName:    *res.ReactorDisplayName,
			Bio:            *res.ReactorBio,
			ProfileImageID: res.ReactorProfileImageID,
			Region:         *res.ReactorRegion,
			AccountType:    *res.ReactorAccountType,
			Status:         *res.ReactorStatus,
		}
		if err := actorUser.setImageKey(); err != nil {
			return err
		}

		reaction := RealMoji{
			ID:           *res.ReactionID,
			PostID:       input.PostID,
			ReactorID:    actor,
			ImageID:      imageID,
			ReactionType: input.ReactionType,
			CreatedAt:    *res.ReactionCreatedAt,
		}
		h.Images.Put(imageID, now, data)
		InvalidateImageMetaForUser(actor) // the replaced realmoji's image died
		setCachedImageMeta(imageID, cachedImageMeta{OwnerUserID: actor, SigningKey: actorUser.ImageKey, MediaType: "image/jpeg", CreatedAt: now}, imageMetaCacheGeneration())
		InvalidateRealMojisCacheForPost(input.PostID)
		InvalidateAllMemoriesCache()
		return c.JSON(http.StatusCreated, newRealMojiResponse(reaction, actorUser))
	}
}

type getRealMojisRequest struct {
	PostID          string     `param:"postId"`
	BeforeCreatedAt *time.Time `query:"beforeCreatedAt"`
	BeforeID        string     `query:"beforeId"`
}

type getRealMojisResponse struct {
	Items   []replaceRealMojiResponse `json:"items"`
	HasMore bool                      `json:"hasMore"`
}

func getRealMojisHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getRealMojisRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if (input.BeforeCreatedAt == nil) != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		ctx := c.Request().Context()
		viewerID := GetSession(c).UserID

		allowed, reactions, err := selectRealMojisIfAllowed(ctx, h.DB, viewerID, input.PostID, input.BeforeCreatedAt, input.BeforeID)
		if err != nil {
			return err
		}
		if !allowed {
			return NewAPIError(http.StatusForbidden, "interaction_forbidden")
		}
		items := make([]replaceRealMojiResponse, 0, len(reactions))
		for _, reaction := range reactions {
			items = append(items, newRealMojiResponse(reaction.RealMoji, reaction.Reactor))
		}
		hasMore := len(items) > 50
		if hasMore {
			items = items[:50]
		}

		return c.JSON(http.StatusOK, getRealMojisResponse{Items: items, HasMore: hasMore})
	}
}

type deleteRealMojiRequest struct {
	RealMojiID string `param:"realMojiId" json:"-"`
}

type deleteRealMojiResult struct {
	Status string `db:"status"`
}

func deleteRealMojiHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input deleteRealMojiRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID

		var res deleteRealMojiResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status
			FROM isureal_delete_realmoji($1, $2)
		`, input.RealMojiID, actor); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			InvalidateImageMetaForUser(actor)
			InvalidateAllRealMojisCache()
			InvalidateAllMemoriesCache()
		case "user_not_found":
			return sql.ErrNoRows
		case "interaction_not_found":
			return NewAPIError(http.StatusNotFound, "interaction_not_found")
		case "interaction_forbidden":
			return NewAPIError(http.StatusForbidden, "interaction_forbidden")
		default:
			return fmt.Errorf("unexpected delete realmoji status: %s", res.Status)
		}
		return c.NoContent(http.StatusNoContent)
	}
}
