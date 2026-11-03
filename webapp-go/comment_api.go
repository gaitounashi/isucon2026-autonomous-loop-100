package isureal

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

type createCommentRequest struct {
	PostID string `param:"postId" json:"-"`
	Body   string `json:"body"`
}

type createCommentResponse struct {
	ID        string                   `json:"id"`
	Author    relationshipUserResponse `json:"author"`
	Body      string                   `json:"body"`
	CreatedAt time.Time                `json:"createdAt"`
}

func InvalidateCommentsCacheForPost(postID string) {
}

func InvalidateAllCommentsCache() {
}

func newCommentResponse(comment Comment, user User) createCommentResponse {
	return createCommentResponse{ID: comment.ID, Author: newRelationshipUserResponse(user), Body: comment.Body, CreatedAt: comment.CreatedAt}
}

type createCommentResult struct {
	Status               string     `db:"status"`
	CommentID            *string    `db:"comment_id"`
	CommentCreatedAt     *time.Time `db:"comment_created_at"`
	AuthorID             *string    `db:"author_id"`
	AuthorUsername       *string    `db:"author_username"`
	AuthorSecretKeyHash  *string    `db:"author_secret_key_hash"`
	AuthorDisplayName    *string    `db:"author_display_name"`
	AuthorBio            *string    `db:"author_bio"`
	AuthorProfileImageID *string    `db:"author_profile_image_id"`
	AuthorRegion         *string    `db:"author_region"`
	AuthorAccountType    *string    `db:"author_account_type"`
	AuthorStatus         *string    `db:"author_status"`
}

func createCommentHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createCommentRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if !validUserText(input.Body, 1, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_interaction")
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		commentID := newUUID()

		var res createCommentResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, comment_id, comment_created_at, author_id, author_username,
			       author_secret_key_hash, author_display_name, author_bio, author_profile_image_id,
			       author_region, author_account_type, author_status
			FROM isureal_create_comment($1, $2, $3, $4)
		`, input.PostID, actor, input.Body, commentID); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			// proceed
		case "interaction_forbidden":
			return NewAPIError(http.StatusForbidden, "interaction_forbidden")
		case "user_not_found":
			return sql.ErrNoRows
		default:
			return fmt.Errorf("unexpected comment status: %s", res.Status)
		}

		actorUser := User{
			ID:             *res.AuthorID,
			Username:       *res.AuthorUsername,
			SecretKeyHash:  *res.AuthorSecretKeyHash,
			DisplayName:    *res.AuthorDisplayName,
			Bio:            *res.AuthorBio,
			ProfileImageID: res.AuthorProfileImageID,
			Region:         *res.AuthorRegion,
			AccountType:    *res.AuthorAccountType,
			Status:         *res.AuthorStatus,
		}
		if err := actorUser.setImageKey(); err != nil {
			return err
		}

		comment := Comment{
			ID:        *res.CommentID,
			PostID:    input.PostID,
			AuthorID:  actor,
			Body:      input.Body,
			CreatedAt: *res.CommentCreatedAt,
		}
		InvalidateCommentsCacheForPost(input.PostID)
		InvalidateAllMemoriesCache()
		return c.JSON(http.StatusCreated, newCommentResponse(comment, actorUser))
	}
}

type getCommentsRequest struct {
	PostID         string     `param:"postId"`
	AfterCreatedAt *time.Time `query:"afterCreatedAt"`
	AfterID        string     `query:"afterId"`
}

type getCommentsResponse struct {
	Items   []createCommentResponse `json:"items"`
	HasMore bool                    `json:"hasMore"`
}

func getCommentsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getCommentsRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if (input.AfterCreatedAt == nil) != (input.AfterID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		ctx := c.Request().Context()
		viewerID := GetSession(c).UserID

		allowed, comments, err := selectCommentsIfAllowed(ctx, h.DB, viewerID, input.PostID, input.AfterCreatedAt, input.AfterID)
		if err != nil {
			return err
		}
		if !allowed {
			return NewAPIError(http.StatusForbidden, "interaction_forbidden")
		}
		items := make([]createCommentResponse, 0, len(comments))
		for _, comment := range comments {
			items = append(items, newCommentResponse(comment.Comment, comment.Author))
		}
		hasMore := len(items) > 50
		if hasMore {
			items = items[:50]
		}

		return c.JSON(http.StatusOK, getCommentsResponse{Items: items, HasMore: hasMore})
	}
}

type deleteCommentRequest struct {
	CommentID string `param:"commentId" json:"-"`
}

type deleteCommentResult struct {
	Status string `db:"status"`
}

func deleteCommentHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input deleteCommentRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID

		var res deleteCommentResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status
			FROM isureal_delete_comment($1, $2)
		`, input.CommentID, actor); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			InvalidateAllCommentsCache()
			InvalidateAllMemoriesCache()
		case "user_not_found":
			return sql.ErrNoRows
		case "interaction_not_found":
			return NewAPIError(http.StatusNotFound, "interaction_not_found")
		case "interaction_forbidden":
			return NewAPIError(http.StatusForbidden, "interaction_forbidden")
		default:
			return fmt.Errorf("unexpected delete comment status: %s", res.Status)
		}
		return c.NoContent(http.StatusNoContent)
	}
}
