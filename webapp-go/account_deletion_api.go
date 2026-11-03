package isureal

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"
)

func deleteAccountHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		tx, err := h.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var lockedID string
		err = tx.GetContext(ctx, &lockedID, `SELECT id FROM users WHERE id=$1 AND status='active' FOR NO KEY UPDATE`, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "account_not_found")
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `WITH u AS (UPDATE users SET status='deleted', deleted_at=clock_timestamp(), updated_at=clock_timestamp() WHERE id=$1) UPDATE login_sessions SET active=false, invalidated_at=clock_timestamp(), reason='account_deletion' WHERE user_id=$1 AND active=true`, userID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		InvalidateSessionsForUser(userID)
		InvalidateAllRelationshipsCache()
		// realmoji images other users left on this user's posts die too.
		InvalidateAllImageMeta()
		return c.NoContent(http.StatusNoContent)
	}
}
