package isureal

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

func TestLoginIPBan(t *testing.T) {
	origNow := ipBanNow
	defer func() { ipBanNow = origNow }()

	baseTime := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	ipBanNow = func() time.Time { return currTime }

	list := NewIPBanList(nil)
	testIP := "192.0.2.1"

	// 9 failures within 10s -> not banned
	for i := 0; i < 9; i++ {
		list.ObserveActionFailure("login", http.StatusUnauthorized, testIP, "")
		if list.IsBanned(testIP) {
			t.Fatalf("expected IP not banned after %d failures, but was banned", i+1)
		}
	}

	// 10th failure -> immediately IsBanned(ip) == true
	list.ObserveActionFailure("login", http.StatusUnauthorized, testIP, "")
	if !list.IsBanned(testIP) {
		t.Fatalf("expected IP banned after 10th failure, but was not")
	}

	// Still banned at 1 minute 59 seconds
	currTime = baseTime.Add(2*time.Minute - time.Second)
	if !list.IsBanned(testIP) {
		t.Fatalf("expected IP still banned before 2m elapsed")
	}

	// Not banned after 2 minutes
	currTime = baseTime.Add(2*time.Minute + time.Nanosecond)
	if list.IsBanned(testIP) {
		t.Fatalf("expected IP unbanned after 2m elapsed")
	}
}

func TestLoginSlidingWindowExpiry(t *testing.T) {
	origNow := ipBanNow
	defer func() { ipBanNow = origNow }()

	baseTime := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	ipBanNow = func() time.Time { return currTime }

	list := NewIPBanList(nil)
	testIP := "192.0.2.2"

	// 9 failures at t=0
	for i := 0; i < 9; i++ {
		list.ObserveActionFailure("login", http.StatusUnauthorized, testIP, "")
	}
	if list.IsBanned(testIP) {
		t.Fatalf("expected not banned after 9 failures")
	}

	// Advance clock to t=10s (boundary at.Sub(failedAt) == window is NOT < window)
	currTime = baseTime.Add(10 * time.Second)

	// 1 failure at t=10s
	list.ObserveActionFailure("login", http.StatusUnauthorized, testIP, "")
	if list.IsBanned(testIP) {
		t.Fatalf("expected IP not banned because previous 9 failures expired at 10s window boundary")
	}
}

func TestPutProfileImageAbuse(t *testing.T) {
	origNow := ipBanNow
	defer func() { ipBanNow = origNow }()

	baseTime := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	ipBanNow = func() time.Time { return currTime }

	list := NewIPBanList(nil)
	testIP := "192.0.2.3"

	// put_profile_image 400 WITHOUT abuse="invalid_image" does NOT count
	for i := 0; i < 5; i++ {
		list.ObserveActionFailure("put_profile_image", http.StatusBadRequest, testIP, "")
		list.ObserveActionFailure("put_profile_image", http.StatusBadRequest, testIP, "other_error")
	}
	if list.IsBanned(testIP) {
		t.Fatalf("expected IP not banned for non-abuse 400s")
	}

	// 1st failure with abuse="invalid_image" -> not banned (threshold=2)
	list.ObserveActionFailure("put_profile_image", http.StatusBadRequest, testIP, "invalid_image")
	if list.IsBanned(testIP) {
		t.Fatalf("expected IP not banned after 1st invalid_image failure")
	}

	// 2nd failure within 5s -> immediately banned!
	currTime = baseTime.Add(2 * time.Second)
	list.ObserveActionFailure("put_profile_image", http.StatusBadRequest, testIP, "invalid_image")
	if !list.IsBanned(testIP) {
		t.Fatalf("expected IP banned after 2nd invalid_image failure within 5s")
	}
}

func TestGetImageAbuse(t *testing.T) {
	origNow := ipBanNow
	defer func() { ipBanNow = origNow }()

	baseTime := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	ipBanNow = func() time.Time { return currTime }

	list := NewIPBanList(nil)
	testIP := "192.0.2.4"

	// get_image 403 WITHOUT abuse (e.g. deleted image) does NOT count
	for i := 0; i < 10; i++ {
		list.ObserveActionFailure("get_image", http.StatusForbidden, testIP, "")
		list.ObserveActionFailure("get_image", http.StatusForbidden, testIP, "image_deleted")
	}
	if list.IsBanned(testIP) {
		t.Fatalf("expected IP not banned without invalid_signature abuse")
	}

	// 1st & 2nd invalid_signature -> not banned (threshold=3)
	list.ObserveActionFailure("get_image", http.StatusForbidden, testIP, "invalid_signature")
	list.ObserveActionFailure("get_image", http.StatusForbidden, testIP, "invalid_signature")
	if list.IsBanned(testIP) {
		t.Fatalf("expected IP not banned after 2 invalid_signature failures")
	}

	// 3rd failure within 5s -> immediately banned!
	currTime = baseTime.Add(3 * time.Second)
	list.ObserveActionFailure("get_image", http.StatusForbidden, testIP, "invalid_signature")
	if !list.IsBanned(testIP) {
		t.Fatalf("expected IP banned after 3rd invalid_signature failure within 5s")
	}
}

func TestEndToEndMiddleware(t *testing.T) {
	origNow := ipBanNow
	defer func() { ipBanNow = origNow }()

	baseTime := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	currTime := baseTime
	ipBanNow = func() time.Time { return currTime }

	ipBans := NewIPBanList(nil)
	hCtx := &HandlerContext{IPBans: ipBans}

	e := echo.New()
	e.IPExtractor = extractClientIP
	e.HTTPErrorHandler = APIErrorHandler

	var logBuf bytes.Buffer
	e.Use(ActionLogMiddlewareWithBans("info", &logBuf, ipBans))
	e.Use(middleware.Recover())
	e.Use(IPBanMiddleware(hCtx))

	var handlerCalls int
	e.GET("/api/images/:imageId", func(c *echo.Context) error {
		handlerCalls++
		c.Set("abuse", "invalid_signature")
		return NewAPIError(http.StatusForbidden, "invalid_signature")
	})

	testIP := "198.51.100.5"

	// 3 invalid signature GET /api/images/:imageId requests
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/images/img123", nil)
		req.Header.Set("X-Client-IP", testIP)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("request %d: expected 403, got %d", i+1, rec.Code)
		}
	}

	if handlerCalls != 3 {
		t.Fatalf("expected handler called 3 times, got %d", handlerCalls)
	}

	// Verify 3 JSON action log lines were written to logBuf
	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 action log lines, got %d: %q", len(lines), logBuf.String())
	}
	for idx, l := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(l), &entry); err != nil {
			t.Fatalf("failed to unmarshal log line %d: %v", idx, err)
		}
		if entry["action"] != "get_image" {
			t.Fatalf("line %d: expected action get_image, got %v", idx, entry["action"])
		}
		if entry["abuse"] != "invalid_signature" {
			t.Fatalf("line %d: expected abuse invalid_signature, got %v", idx, entry["abuse"])
		}
		if entry["ip"] != testIP {
			t.Fatalf("line %d: expected ip %s, got %v", idx, testIP, entry["ip"])
		}
	}

	// Check that IP is now banned
	if !ipBans.IsBanned(testIP) {
		t.Fatalf("expected IP %s to be banned after 3 failures", testIP)
	}

	// 4th request from that IP is immediately blocked by IPBanMiddleware with 403 ip_banned
	// without reaching the handler!
	req := httptest.NewRequest(http.MethodGet, "/api/images/img123", nil)
	req.Header.Set("X-Client-IP", testIP)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("4th request: expected 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ip_banned") {
		t.Fatalf("4th request: expected error code 'ip_banned', got %s", rec.Body.String())
	}
	if handlerCalls != 3 {
		t.Fatalf("expected handler NOT reached on 4th request (calls=%d)", handlerCalls)
	}
}

func TestResetLocalState(t *testing.T) {
	list := NewIPBanList(nil)
	ip1 := "192.0.2.10"
	ip2 := "192.0.2.11"

	// Accumulate partial failures for ip1
	for i := 0; i < 5; i++ {
		list.ObserveActionFailure("login", http.StatusUnauthorized, ip1, "")
	}

	// Ban ip2
	for i := 0; i < 2; i++ {
		list.ObserveActionFailure("put_profile_image", http.StatusBadRequest, ip2, "invalid_image")
	}
	if !list.IsBanned(ip2) {
		t.Fatalf("expected ip2 to be banned")
	}

	// Reset local state
	list.ResetLocalState()

	// ip2 should no longer be banned
	if list.IsBanned(ip2) {
		t.Fatalf("expected ip2 unbanned after ResetLocalState")
	}

	// ip1 should require full 10 failures to be banned, not just 5 more
	for i := 0; i < 5; i++ {
		list.ObserveActionFailure("login", http.StatusUnauthorized, ip1, "")
	}
	if list.IsBanned(ip1) {
		t.Fatalf("expected ip1 not banned because previous 5 partial failures were cleared")
	}

	// 5 more -> total 10 fresh failures -> banned
	for i := 0; i < 5; i++ {
		list.ObserveActionFailure("login", http.StatusUnauthorized, ip1, "")
	}
	if !list.IsBanned(ip1) {
		t.Fatalf("expected ip1 banned after 10 fresh failures")
	}
}

func TestKillSwitchDisabled(t *testing.T) {
	t.Setenv("ISUREAL_DISABLE_INPROC_BAN", "1")

	ipBans := NewIPBanList(nil)
	hCtx := &HandlerContext{IPBans: ipBans}

	e := echo.New()
	e.IPExtractor = extractClientIP
	e.HTTPErrorHandler = APIErrorHandler

	var logBuf bytes.Buffer
	e.Use(ActionLogMiddlewareWithBans("info", &logBuf, ipBans))
	e.Use(IPBanMiddleware(hCtx))

	e.POST("/api/auth/login", func(c *echo.Context) error {
		return NewAPIError(http.StatusUnauthorized, "invalid_credentials")
	})

	testIP := "198.51.100.20"

	// Send 15 login failure requests
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.Header.Set("X-Client-IP", testIP)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	}

	// Action logs SHOULD still be emitted
	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(lines) != 15 {
		t.Fatalf("expected 15 action log lines even with kill-switch, got %d", len(lines))
	}

	// But in-process ban MUST NOT be applied
	if ipBans.IsBanned(testIP) {
		t.Fatalf("expected IP not banned when ISUREAL_DISABLE_INPROC_BAN=1")
	}
}
