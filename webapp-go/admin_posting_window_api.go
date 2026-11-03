package isureal

import (
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

type openPostingWindowRequest struct {
	Region        string `json:"region"`
	LocalDate     string `json:"localDate"`
	OnTimeSeconds *int64 `json:"onTimeSeconds"`
}

type openPostingWindowResponse struct {
	ID             string    `json:"id"`
	Region         string    `json:"region"`
	LocalDate      string    `json:"localDate"`
	StartsAt       time.Time `json:"startsAt"`
	OnTimeDeadline time.Time `json:"onTimeDeadline"`
}

func openPostingWindowHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input openPostingWindowRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		region := input.Region
		localDate, err := time.Parse(time.DateOnly, input.LocalDate)
		if !validRegion(region) || err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_posting_window")
		}
		seconds := int64(24 * 60 * 60)
		if input.OnTimeSeconds != nil {
			seconds = *input.OnTimeSeconds
		}
		ctx := c.Request().Context()
		windowID := newUUID()

		var res struct {
			Status         string     `db:"status"`
			ID             *string    `db:"out_id"`
			Region         *string    `db:"out_region"`
			LocalDate      *time.Time `db:"out_local_date"`
			StartsAt       *time.Time `db:"out_starts_at"`
			OnTimeDeadline *time.Time `db:"out_on_time_deadline"`
			EndedAt        *time.Time `db:"out_ended_at"`
		}

		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, out_id, out_region, out_local_date, out_starts_at, out_on_time_deadline, out_ended_at
			FROM isureal_open_posting_window($1, $2, $3, $4)
		`, windowID, region, localDate, seconds); err != nil {
			return err
		}

		if res.Status == "posting_window_conflict" {
			return NewAPIError(http.StatusConflict, "posting_window_conflict")
		}
		if res.Status != "ok" || res.ID == nil {
			return fmt.Errorf("unexpected status: %s", res.Status)
		}

		InvalidateAllPostingWindowCache()
		InvalidateAllMyCurrentPostCache()
		InvalidateAllFeedCache()
		if region == h.Config.Region && h.Notifications != nil {
			h.Notifications.Trigger()
		}
		return c.JSON(http.StatusCreated, openPostingWindowResponse{
			ID: *res.ID, Region: *res.Region, LocalDate: res.LocalDate.Format(time.DateOnly),
			StartsAt: *res.StartsAt, OnTimeDeadline: *res.OnTimeDeadline,
		})
	}
}
