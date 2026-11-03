package isureal

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"
)

type relationshipResult struct {
	Status string `db:"status"`
}

func handleRelationshipResult(status string) error {
	switch status {
	case "ok":
		return nil
	case "invalid_relationship":
		return NewAPIError(http.StatusBadRequest, "invalid_relationship")
	case "relationship_not_found":
		return NewAPIError(http.StatusNotFound, "relationship_not_found")
	case "relationship_conflict":
		return NewAPIError(http.StatusConflict, "relationship_conflict")
	case "relationship_action_forbidden":
		return NewAPIError(http.StatusForbidden, "relationship_action_forbidden")
	default:
		return fmt.Errorf("unexpected relationship status: %s", status)
	}
}

type deleteFriendshipRequest struct {
	UserID string `param:"userId" json:"-"`
}

func deleteFriendshipHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input deleteFriendshipRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		if actor == input.UserID || input.UserID == "" {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}

		var res relationshipResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status FROM isureal_delete_friendship($1, $2)
		`, actor, input.UserID); err != nil {
			return err
		}
		if err := handleRelationshipResult(res.Status); err != nil {
			return err
		}
		InvalidateRelationshipsCacheForUser(actor)
		InvalidateRelationshipsCacheForUser(input.UserID)
		return c.NoContent(http.StatusNoContent)
	}
}

type followRequest struct {
	UserID string `param:"userId" json:"-"`
}

func followHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input followRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		if actor == input.UserID || input.UserID == "" {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		followID := newUUID()

		var res relationshipResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status FROM isureal_follow($1, $2, $3)
		`, actor, input.UserID, followID); err != nil {
			return err
		}
		if err := handleRelationshipResult(res.Status); err != nil {
			return err
		}
		InvalidateRelationshipsCacheForUser(actor)
		InvalidateRelationshipsCacheForUser(input.UserID)
		return c.NoContent(http.StatusNoContent)
	}
}

type unfollowRequest struct {
	UserID string `param:"userId" json:"-"`
}

func unfollowHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input unfollowRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		if actor == input.UserID || input.UserID == "" {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}

		var res relationshipResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status FROM isureal_unfollow($1, $2)
		`, actor, input.UserID); err != nil {
			return err
		}
		if err := handleRelationshipResult(res.Status); err != nil {
			return err
		}
		InvalidateRelationshipsCacheForUser(actor)
		InvalidateRelationshipsCacheForUser(input.UserID)
		return c.NoContent(http.StatusNoContent)
	}
}

type blockRequest struct {
	UserID string `param:"userId" json:"-"`
}

func blockHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input blockRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		if actor == input.UserID || input.UserID == "" {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		blockID := newUUID()

		var res relationshipResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status FROM isureal_block($1, $2, $3)
		`, actor, input.UserID, blockID); err != nil {
			return err
		}
		if err := handleRelationshipResult(res.Status); err != nil {
			return err
		}
		InvalidateRelationshipsCacheForUser(actor)
		InvalidateRelationshipsCacheForUser(input.UserID)
		return c.NoContent(http.StatusNoContent)
	}
}

type unblockRequest struct {
	UserID string `param:"userId" json:"-"`
}

func unblockHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input unblockRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		if actor == input.UserID || input.UserID == "" {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}

		var res relationshipResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status FROM isureal_unblock($1, $2)
		`, actor, input.UserID); err != nil {
			return err
		}
		if err := handleRelationshipResult(res.Status); err != nil {
			return err
		}
		InvalidateRelationshipsCacheForUser(actor)
		InvalidateRelationshipsCacheForUser(input.UserID)
		return c.NoContent(http.StatusNoContent)
	}
}
