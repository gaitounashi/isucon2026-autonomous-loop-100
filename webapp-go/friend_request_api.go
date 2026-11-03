package isureal

import (
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

type createFriendRequestRequest struct {
	RecipientID string `json:"recipientId"`
}

type createFriendRequestResponse struct {
	RelationID string    `json:"relationId"`
	Status     string    `json:"status"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type friendRequestResult struct {
	Status         string     `db:"status"`
	RelationID     *string    `db:"out_relation_id"`
	RelationStatus *string    `db:"out_relation_status"`
	UpdatedAt      *time.Time `db:"out_updated_at"`
}

func createFriendRequestHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createFriendRequestRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		actor := GetSession(c).UserID
		if actor == input.RecipientID || input.RecipientID == "" {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		ctx := c.Request().Context()
		requestID := newUUID()
		friendshipID := newUUID()

		var res friendRequestResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, out_relation_id, out_relation_status, out_updated_at
			FROM isureal_create_friend_request($1, $2, $3, $4)
		`, actor, input.RecipientID, requestID, friendshipID); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			InvalidateRelationshipsCacheForUser(actor)
			InvalidateRelationshipsCacheForUser(input.RecipientID)
		case "invalid_relationship":
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		case "relationship_not_found":
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		case "relationship_conflict":
			return NewAPIError(http.StatusConflict, "relationship_conflict")
		default:
			return fmt.Errorf("unexpected create friend request status: %s", res.Status)
		}

		return c.JSON(http.StatusCreated, createFriendRequestResponse{
			RelationID: *res.RelationID,
			Status:     *res.RelationStatus,
			UpdatedAt:  *res.UpdatedAt,
		})
	}
}

type transitionFriendRequestRequest struct {
	RequestID string `param:"requestId" json:"-"`
	Action    string `param:"action" json:"-"`
}

type transitionFriendRequestResponse createFriendRequestResponse

func transitionFriendRequestHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input transitionFriendRequestRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		var actionStatus string
		switch input.Action {
		case "accept":
			actionStatus = "accepted"
		case "reject":
			actionStatus = "rejected"
		case "cancel":
			actionStatus = "cancelled"
		default:
			return NewAPIError(http.StatusForbidden, "relationship_action_forbidden")
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		friendshipID := newUUID()

		var res struct {
			friendRequestResult
			RequesterID *string `db:"out_requester_id"`
			RecipientID *string `db:"out_recipient_id"`
		}
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, out_relation_id, out_relation_status, out_updated_at, out_requester_id, out_recipient_id
			FROM isureal_transition_friend_request($1, $2, $3, $4)
		`, input.RequestID, actor, actionStatus, friendshipID); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			if res.RequesterID != nil {
				InvalidateRelationshipsCacheForUser(*res.RequesterID)
			} else {
				InvalidateRelationshipsCacheForUser(actor)
			}
			if res.RecipientID != nil {
				InvalidateRelationshipsCacheForUser(*res.RecipientID)
			}
		case "relationship_not_found":
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		case "relationship_action_forbidden":
			return NewAPIError(http.StatusForbidden, "relationship_action_forbidden")
		case "relationship_conflict":
			return NewAPIError(http.StatusConflict, "relationship_conflict")
		case "invalid_relationship":
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		default:
			return fmt.Errorf("unexpected friend request transition status: %s", res.Status)
		}

		return c.JSON(http.StatusOK, transitionFriendRequestResponse{
			RelationID: *res.RelationID,
			Status:     *res.RelationStatus,
			UpdatedAt:  *res.UpdatedAt,
		})
	}
}
