package isureal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type getNotificationsResponse struct {
	ID              string    `json:"id" db:"id"`
	PostingWindowID string    `json:"postingWindowId" db:"posting_window_id"`
	Region          string    `json:"region" db:"region"`
	CreatedAt       time.Time `json:"createdAt" db:"created_at"`
}

type sseSubscriber struct {
	userID      string
	connectedAt time.Time
	notifyCh    chan getNotificationsResponse
	closeCh     chan struct{}
	closeOnce   sync.Once
}

func (s *sseSubscriber) close() {
	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
}

type NotificationHub struct {
	db              *sqlx.DB
	region          string
	mu              sync.Mutex
	subs            map[*sseSubscriber]struct{}
	trigger         chan struct{}
	lastUserCheck   time.Time
	userCheckForced bool
	clockOffset     atomic.Int64 // nanoseconds: dbTime - localTime
}

func (hub *NotificationHub) syncClock() {
	if hub == nil || hub.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	var dbTime time.Time
	if err := hub.db.GetContext(ctx, &dbTime, `SELECT clock_timestamp()`); err != nil {
		return
	}
	rtt := time.Since(start)
	estimatedDbNow := dbTime.Add(rtt / 2)
	offset := estimatedDbNow.Sub(time.Now()).Nanoseconds()
	hub.clockOffset.Store(offset)
}

func (hub *NotificationHub) Now() time.Time {
	if hub == nil {
		return time.Now()
	}
	return time.Now().Add(time.Duration(hub.clockOffset.Load()))
}

func NewNotificationHub(db *sqlx.DB, region string) *NotificationHub {
	hub := &NotificationHub{
		db:      db,
		region:  region,
		subs:    make(map[*sseSubscriber]struct{}),
		trigger: make(chan struct{}, 1),
	}
	hub.syncClock()
	go hub.loop()
	return hub
}

func (hub *NotificationHub) Trigger() {
	select {
	case hub.trigger <- struct{}{}:
	default:
	}
}

func (hub *NotificationHub) OnUserChanged(userID string) {
	if hub == nil {
		return
	}
	hub.mu.Lock()
	if len(hub.subs) == 0 {
		hub.mu.Unlock()
		return
	}
	if userID != "" {
		found := false
		for sub := range hub.subs {
			if sub.userID == userID {
				found = true
				break
			}
		}
		if !found {
			hub.mu.Unlock()
			return
		}
	}
	hub.userCheckForced = true
	hub.mu.Unlock()
	hub.Trigger()
}

func (hub *NotificationHub) subscribe(sub *sseSubscriber) {
	hub.mu.Lock()
	hub.subs[sub] = struct{}{}
	hub.mu.Unlock()
}

func (hub *NotificationHub) unsubscribe(sub *sseSubscriber) {
	hub.mu.Lock()
	delete(hub.subs, sub)
	hub.mu.Unlock()
}

func (hub *NotificationHub) loop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			hub.syncClock()
			hub.pollOnce()
		case <-hub.trigger:
			hub.pollOnce()
		}
	}
}

func (hub *NotificationHub) pollOnce() {
	hub.mu.Lock()
	if len(hub.subs) == 0 {
		hub.userCheckForced = false
		hub.mu.Unlock()
		return
	}
	hub.userCheckForced = false
	subs := make([]*sseSubscriber, 0, len(hub.subs))
	userSet := make(map[string]struct{}, len(hub.subs))
	var minConnectedAt time.Time
	for sub := range hub.subs {
		subs = append(subs, sub)
		userSet[sub.userID] = struct{}{}
		if minConnectedAt.IsZero() || sub.connectedAt.Before(minConnectedAt) {
			minConnectedAt = sub.connectedAt
		}
	}
	hub.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	userIDs := make([]string, 0, len(userSet))
	for id := range userSet {
		userIDs = append(userIDs, id)
	}
	joinedUsers := strings.Join(userIDs, ",")

	var rows []struct {
		UserID          string     `db:"user_id"`
		NotificationID  *string    `db:"notification_id"`
		PostingWindowID *string    `db:"posting_window_id"`
		Region          *string    `db:"region"`
		CreatedAt       *time.Time `db:"created_at"`
	}
	if err := hub.db.SelectContext(ctx, &rows, `
		SELECT
			u.id AS user_id,
			n.id AS notification_id,
			n.posting_window_id,
			w.region,
			n.created_at
		FROM users u
		LEFT JOIN posting_windows w
			ON w.region = $2 AND w.ended_at IS NULL
		LEFT JOIN notifications n
			ON n.posting_window_id = w.id AND n.user_id = u.id AND n.created_at > $3
		WHERE u.id = ANY(string_to_array($1, ','))
		  AND u.status = 'active'
		  AND u.region = $2
		ORDER BY n.created_at ASC NULLS LAST, n.id ASC NULLS LAST
	`, joinedUsers, hub.region, minConnectedAt); err != nil {
		return
	}
	hub.lastUserCheck = time.Now()

	activeMap := make(map[string]bool, len(userSet))
	byUser := make(map[string][]getNotificationsResponse)
	for _, r := range rows {
		activeMap[r.UserID] = true
		if r.NotificationID != nil && *r.NotificationID != "" && r.PostingWindowID != nil && r.Region != nil && r.CreatedAt != nil {
			byUser[r.UserID] = append(byUser[r.UserID], getNotificationsResponse{
				ID:              *r.NotificationID,
				PostingWindowID: *r.PostingWindowID,
				Region:          *r.Region,
				CreatedAt:       *r.CreatedAt,
			})
		}
	}

	for _, sub := range subs {
		if !activeMap[sub.userID] {
			hub.unsubscribe(sub)
			sub.close()
			continue
		}
		for _, notif := range byUser[sub.userID] {
			if notif.CreatedAt.After(sub.connectedAt) {
				hub.unsubscribe(sub)
				select {
				case sub.notifyCh <- notif:
				default:
				}
				break
			}
		}
	}
}

func getNotificationsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		session := GetSession(c)
		userID := session.UserID

		response := c.Response()
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(response)
		if err := controller.Flush(); err != nil {
			return nil
		}
		if session.User.Status != "active" {
			return nil
		}
		if session.Region != h.Config.Region {
			return nil
		}

		connectedAt := h.Notifications.Now()

		sub := &sseSubscriber{
			userID:      userID,
			connectedAt: connectedAt,
			notifyCh:    make(chan getNotificationsResponse, 1),
			closeCh:     make(chan struct{}),
		}
		h.Notifications.subscribe(sub)
		defer h.Notifications.unsubscribe(sub)

		select {
		case <-ctx.Done():
			return nil
		case <-sub.closeCh:
			return nil
		case notification := <-sub.notifyCh:
			data, err := json.Marshal(notification)
			if err != nil {
				c.Logger().Error("failed to encode notification", "error", err)
				return nil
			}
			if _, err := fmt.Fprintf(response, "data: %s\n\n", data); err != nil {
				return nil
			}
			_ = controller.Flush()
			return nil
		}
	}
}
