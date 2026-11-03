package isureal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func (env *testEnv) openPostingWindowInRegion(region string) string {
	token := env.adminToken()

	var maxDate *time.Time
	_ = env.DB.Get(&maxDate, `SELECT MAX(local_date) FROM posting_windows WHERE region = $1`, region)
	targetDate := time.Now().UTC()
	if maxDate != nil && !targetDate.After(*maxDate) {
		targetDate = maxDate.AddDate(0, 0, 1)
	}
	dateStr := targetDate.Format(time.DateOnly)

	rec := env.do(http.MethodPost, "/api/admin/posting-window", token, map[string]any{
		"region":    region,
		"localDate": dateStr,
	})
	if rec.Code < 200 || rec.Code >= 300 {
		env.T.Fatalf("openPostingWindowInRegion (%s) failed with status %d: %s", region, rec.Code, rec.Body.String())
	}
	res := decodeJSON[openPostingWindowResponse](env.T, rec)
	time.Sleep(10 * time.Millisecond)
	return res.ID
}

// 1. Subscriber in the window's region receives the notification
func TestNotificationPoll_SubscriberInWindowRegionReceivesNotification(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUserInRegion("poll_sse_asia_user", "asia")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.RemoteAddr = "192.0.2.1:12345"

	rec := newMediaSyncRecorder()
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		env.E.ServeHTTP(rec, req)
	}()

	select {
	case <-rec.flushedCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for SSE connection to establish")
	}

	time.Sleep(30 * time.Millisecond)

	winID := env.openPostingWindowInRegion("asia")

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for notification event delivery")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for notifications SSE, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %s", ct)
	}

	body := rec.Body.String()
	notif, err := mediaParseSSENotification(body)
	if err != nil {
		t.Fatalf("failed to parse SSE notification: %v, raw body: %q", err, body)
	}

	if notif.ID == "" {
		t.Fatalf("expected non-empty notification ID")
	}
	if notif.PostingWindowID != winID {
		t.Fatalf("expected postingWindowId %s, got %s", winID, notif.PostingWindowID)
	}
	if notif.Region != "asia" {
		t.Fatalf("expected notification region 'asia', got '%s'", notif.Region)
	}
	if notif.CreatedAt.IsZero() || time.Since(notif.CreatedAt) > 10*time.Second {
		t.Fatalf("expected reasonable createdAt timestamp, got %v", notif.CreatedAt)
	}
}

// 2. Subscriber in another region does not receive notification
func TestNotificationPoll_SubscriberInAnotherRegionDoesNotReceive(t *testing.T) {
	env := newTestEnv(t)
	userAsia := env.registerUserInRegion("poll_user_asia_diff", "asia")

	hubAsia := NewNotificationHub(env.DB, "asia")
	subAsia := &sseSubscriber{
		userID:      userAsia.ID,
		connectedAt: time.Now().Add(-1 * time.Second),
		notifyCh:    make(chan getNotificationsResponse, 1),
		closeCh:     make(chan struct{}),
	}
	hubAsia.subscribe(subAsia)
	defer hubAsia.unsubscribe(subAsia)

	// Open a window in "america", NOT "asia"
	env.openPostingWindowInRegion("america")
	hubAsia.pollOnce()

	select {
	case notif := <-subAsia.notifyCh:
		t.Fatalf("subscriber in asia should not receive notification for america window: %+v", notif)
	default:
		// expected: no notification delivered
	}

	// Verify subscriber is still connected and not closed
	select {
	case <-subAsia.closeCh:
		t.Fatalf("subscriber in asia should not be disconnected")
	default:
	}
}

// 3. A user who became inactive/deleted does not receive notification
func TestNotificationPoll_InactiveOrDeletedUserDoesNotReceive(t *testing.T) {
	env := newTestEnv(t)
	userInactive := env.registerUserInRegion("poll_user_inactive", "asia")
	userDeleted := env.registerUserInRegion("poll_user_deleted", "asia")

	hub := NewNotificationHub(env.DB, "asia")

	subInactive := &sseSubscriber{
		userID:      userInactive.ID,
		connectedAt: time.Now().Add(-1 * time.Second),
		notifyCh:    make(chan getNotificationsResponse, 1),
		closeCh:     make(chan struct{}),
	}
	subDeleted := &sseSubscriber{
		userID:      userDeleted.ID,
		connectedAt: time.Now().Add(-1 * time.Second),
		notifyCh:    make(chan getNotificationsResponse, 1),
		closeCh:     make(chan struct{}),
	}
	hub.subscribe(subInactive)
	defer hub.unsubscribe(subInactive)
	hub.subscribe(subDeleted)
	defer hub.unsubscribe(subDeleted)

	// Mark userInactive as inactive, and userDeleted as deleted
	if _, err := env.DB.Exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, userInactive.ID); err != nil {
		t.Fatalf("failed to update user status to inactive: %v", err)
	}
	if _, err := env.DB.Exec(`UPDATE users SET status = 'deleted' WHERE id = $1`, userDeleted.ID); err != nil {
		t.Fatalf("failed to update user status to deleted: %v", err)
	}

	env.openPostingWindowInRegion("asia")
	hub.pollOnce()

	// Both should NOT receive notification
	select {
	case notif := <-subInactive.notifyCh:
		t.Fatalf("inactive user should not receive notification: %+v", notif)
	default:
	}
	select {
	case notif := <-subDeleted.notifyCh:
		t.Fatalf("deleted user should not receive notification: %+v", notif)
	default:
	}

	// Both should have their closeCh closed (disconnected)
	select {
	case <-subInactive.closeCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for inactive subscriber closeCh")
	}
	select {
	case <-subDeleted.closeCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for deleted subscriber closeCh")
	}
}

// 4. Notification created before the subscriber connected is not delivered
func TestNotificationPoll_NotificationCreatedBeforeSubscriberConnectedNotDelivered(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUserInRegion("poll_preexist_user", "asia")

	// Open posting window BEFORE subscriber connects
	env.openPostingWindowInRegion("asia")

	// Connect via HTTP SSE with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	rec := mediaDoWithContext(env, ctx, http.MethodGet, "/api/notifications", user.Token, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("expected pre-existing posting window notification NOT to be delivered, got: %s", rec.Body.String())
	}

	// Also test direct hub polling with a subscriber whose connectedAt is after the window
	hub := NewNotificationHub(env.DB, "asia")
	sub := &sseSubscriber{
		userID:      user.ID,
		connectedAt: time.Now(),
		notifyCh:    make(chan getNotificationsResponse, 1),
		closeCh:     make(chan struct{}),
	}
	hub.subscribe(sub)
	defer hub.unsubscribe(sub)

	hub.pollOnce()

	select {
	case notif := <-sub.notifyCh:
		t.Fatalf("expected pre-existing notification NOT to be delivered to direct subscriber, got: %+v", notif)
	default:
	}
	select {
	case <-sub.closeCh:
		t.Fatalf("subscriber should not be closed")
	default:
	}
}

// 5. Two open windows / two subscribers each get the right one
func TestNotificationPoll_TwoOpenWindowsTwoSubscribersGetRightOne(t *testing.T) {
	env := newTestEnv(t)
	userAsia := env.registerUserInRegion("poll_two_win_asia", "asia")
	userAmerica := env.registerUserInRegion("poll_two_win_america", "america")

	hubAsia := NewNotificationHub(env.DB, "asia")
	hubAmerica := NewNotificationHub(env.DB, "america")

	subAsia := &sseSubscriber{
		userID:      userAsia.ID,
		connectedAt: time.Now().Add(-1 * time.Second),
		notifyCh:    make(chan getNotificationsResponse, 1),
		closeCh:     make(chan struct{}),
	}
	subAmerica := &sseSubscriber{
		userID:      userAmerica.ID,
		connectedAt: time.Now().Add(-1 * time.Second),
		notifyCh:    make(chan getNotificationsResponse, 1),
		closeCh:     make(chan struct{}),
	}
	hubAsia.subscribe(subAsia)
	defer hubAsia.unsubscribe(subAsia)
	hubAmerica.subscribe(subAmerica)
	defer hubAmerica.unsubscribe(subAmerica)

	// Open two windows: one in asia, one in america
	winAsiaID := env.openPostingWindowInRegion("asia")
	winAmericaID := env.openPostingWindowInRegion("america")

	// Poll both hubs
	hubAsia.pollOnce()
	hubAmerica.pollOnce()

	// Asia subscriber gets asia notification
	select {
	case notif := <-subAsia.notifyCh:
		if notif.PostingWindowID != winAsiaID {
			t.Fatalf("asia subscriber expected window %s, got %s", winAsiaID, notif.PostingWindowID)
		}
		if notif.Region != "asia" {
			t.Fatalf("asia subscriber expected region 'asia', got %s", notif.Region)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for asia notification")
	}

	// America subscriber gets america notification
	select {
	case notif := <-subAmerica.notifyCh:
		if notif.PostingWindowID != winAmericaID {
			t.Fatalf("america subscriber expected window %s, got %s", winAmericaID, notif.PostingWindowID)
		}
		if notif.Region != "america" {
			t.Fatalf("america subscriber expected region 'america', got %s", notif.Region)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for america notification")
	}

	// Neither subscriber receives unexpected additional notifications
	select {
	case notif := <-subAsia.notifyCh:
		t.Fatalf("asia subscriber received unexpected extra notification: %+v", notif)
	default:
	}
	select {
	case notif := <-subAmerica.notifyCh:
		t.Fatalf("america subscriber received unexpected extra notification: %+v", notif)
	default:
	}
}
