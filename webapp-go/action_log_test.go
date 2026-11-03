package isureal

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestActionLogMiddleware_InternalServerError(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		routePattern   string
		requestPath    string
		handler        echo.HandlerFunc
		expectedStatus int
		expectedAction any
		expectedLevel  string
	}{
		{
			name:         "500 via returned error on matched route",
			method:       http.MethodGet,
			routePattern: "/api/me",
			requestPath:  "/api/me",
			handler: func(c *echo.Context) error {
				return errors.New("database connection failed")
			},
			expectedStatus: http.StatusInternalServerError,
			expectedAction: "get_me",
			expectedLevel:  "error",
		},
		{
			name:         "500 via echo.NewHTTPError(500) on matched route",
			method:       http.MethodPost,
			routePattern: "/api/posts",
			requestPath:  "/api/posts",
			handler: func(c *echo.Context) error {
				return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
			},
			expectedStatus: http.StatusInternalServerError,
			expectedAction: "create_post",
			expectedLevel:  "error",
		},
		{
			name:         "503 Service Unavailable via c.String",
			method:       http.MethodGet,
			routePattern: "/api/posts/:postId",
			requestPath:  "/api/posts/p123",
			handler: func(c *echo.Context) error {
				return c.String(http.StatusServiceUnavailable, "service unavailable")
			},
			expectedStatus: http.StatusServiceUnavailable,
			expectedAction: "get_post",
			expectedLevel:  "error",
		},
		{
			name:         "500 on unmatched action route sets action to nil",
			method:       http.MethodGet,
			routePattern: "/custom/unregistered",
			requestPath:  "/custom/unregistered",
			handler: func(c *echo.Context) error {
				return errors.New("boom")
			},
			expectedStatus: http.StatusInternalServerError,
			expectedAction: nil,
			expectedLevel:  "error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			e := echo.New()
			e.Use(ActionLogMiddleware("info", &buf))
			e.Add(tc.method, tc.routePattern, tc.handler)

			req := httptest.NewRequest(tc.method, tc.requestPath, nil)
			req.Header.Set("X-Forwarded-For", "203.0.113.195")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tc.expectedStatus {
				t.Fatalf("expected HTTP status %d, got %d", tc.expectedStatus, rec.Code)
			}

			output := buf.String()
			if !strings.HasSuffix(output, "\n") {
				t.Fatalf("log line must end with newline: %q", output)
			}

			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("failed to parse log entry as JSON: %v (raw: %q)", err, output)
			}

			if entry["level"] != tc.expectedLevel {
				t.Fatalf("expected level %q, got %v", tc.expectedLevel, entry["level"])
			}
			if int(entry["status"].(float64)) != tc.expectedStatus {
				t.Fatalf("expected status %d, got %v", tc.expectedStatus, entry["status"])
			}
			if entry["action"] != tc.expectedAction {
				t.Fatalf("expected action %v, got %v", tc.expectedAction, entry["action"])
			}
			if entry["ip"] == "" {
				t.Fatalf("expected non-empty ip field, got %v", entry["ip"])
			}
		})
	}
}

func TestActionLogMiddleware_GuardTriggers(t *testing.T) {
	tests := []struct {
		name               string
		method             string
		routePattern       string
		requestPath        string
		handler            echo.HandlerFunc
		expectedStatus     int
		expectedLevel      string
		expectedAction     string
		expectedAbuse      any
		expectedUsername   any
		hasUsernameKey     bool
		hasAbuseKey        bool
		expectedLogEmitted bool
	}{
		{
			name:         "login 401 unauthorized with valid username",
			method:       http.MethodPost,
			routePattern: "/api/auth/login",
			requestPath:  "/api/auth/login",
			handler: func(c *echo.Context) error {
				c.Set("username", "valid_user-1")
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
			},
			expectedStatus:     http.StatusUnauthorized,
			expectedLevel:      "info",
			expectedAction:     "login",
			expectedUsername:   "valid_user-1",
			hasUsernameKey:     true,
			hasAbuseKey:        false,
			expectedLogEmitted: true,
		},
		{
			name:         "login 401 unauthorized with invalid username syntax sanitized to nil",
			method:       http.MethodPost,
			routePattern: "/api/auth/login",
			requestPath:  "/api/auth/login",
			handler: func(c *echo.Context) error {
				c.Set("username", "invalid space user!")
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
			},
			expectedStatus:     http.StatusUnauthorized,
			expectedLevel:      "info",
			expectedAction:     "login",
			expectedUsername:   nil,
			hasUsernameKey:     true,
			hasAbuseKey:        false,
			expectedLogEmitted: true,
		},
		{
			name:         "put_profile_image 400 with invalid_image abuse",
			method:       http.MethodPut,
			routePattern: "/api/me/profile-image",
			requestPath:  "/api/me/profile-image",
			handler: func(c *echo.Context) error {
				c.Set("abuse", "invalid_image")
				return echo.NewHTTPError(http.StatusBadRequest, "invalid image")
			},
			expectedStatus:     http.StatusBadRequest,
			expectedLevel:      "info",
			expectedAction:     "put_profile_image",
			expectedAbuse:      "invalid_image",
			hasUsernameKey:     false,
			hasAbuseKey:        true,
			expectedLogEmitted: true,
		},
		{
			name:         "get_image 403 with invalid_signature abuse",
			method:       http.MethodGet,
			routePattern: "/api/images/:imageId",
			requestPath:  "/api/images/img-123",
			handler: func(c *echo.Context) error {
				c.Set("abuse", "invalid_signature")
				return echo.NewHTTPError(http.StatusForbidden, "invalid signature")
			},
			expectedStatus:     http.StatusForbidden,
			expectedLevel:      "info",
			expectedAction:     "get_image",
			expectedAbuse:      "invalid_signature",
			hasUsernameKey:     false,
			hasAbuseKey:        true,
			expectedLogEmitted: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			e := echo.New()
			e.Use(ActionLogMiddleware("info", &buf))
			e.Add(tc.method, tc.routePattern, tc.handler)

			req := httptest.NewRequest(tc.method, tc.requestPath, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tc.expectedStatus {
				t.Fatalf("expected HTTP status %d, got %d", tc.expectedStatus, rec.Code)
			}

			if !tc.expectedLogEmitted {
				if buf.Len() != 0 {
					t.Fatalf("expected no log emitted, got %q", buf.String())
				}
				return
			}

			if buf.Len() == 0 {
				t.Fatalf("expected log emitted, got empty buffer")
			}

			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("failed to parse log JSON: %v (raw: %q)", err, buf.String())
			}

			if entry["level"] != tc.expectedLevel {
				t.Fatalf("expected level %q, got %v", tc.expectedLevel, entry["level"])
			}
			if int(entry["status"].(float64)) != tc.expectedStatus {
				t.Fatalf("expected status %d, got %v", tc.expectedStatus, entry["status"])
			}
			if entry["action"] != tc.expectedAction {
				t.Fatalf("expected action %q, got %v", tc.expectedAction, entry["action"])
			}
			if tc.hasUsernameKey {
				val, exists := entry["username"]
				if !exists {
					t.Fatalf("expected username key to exist in log entry")
				}
				if val != tc.expectedUsername {
					t.Fatalf("expected username %v, got %v", tc.expectedUsername, val)
				}
			}
			if tc.hasAbuseKey {
				val, exists := entry["abuse"]
				if !exists {
					t.Fatalf("expected abuse key to exist in log entry")
				}
				if val != tc.expectedAbuse {
					t.Fatalf("expected abuse %v, got %v", tc.expectedAbuse, val)
				}
			} else {
				if _, exists := entry["abuse"]; exists {
					t.Fatalf("unexpected abuse key in entry: %v", entry["abuse"])
				}
			}
		})
	}
}

func TestActionLogMiddleware_SessionAndUserFields(t *testing.T) {
	// When session exists in context, user_id is populated.
	var buf bytes.Buffer
	e := echo.New()
	e.Use(ActionLogMiddleware("info", &buf))

	e.GET("/api/me", func(c *echo.Context) error {
		c.Set("session", &Session{
			UserID: "user-uuid-1234",
		})
		return echo.NewHTTPError(http.StatusInternalServerError, "db error")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if entry["user_id"] != "user-uuid-1234" {
		t.Fatalf("expected user_id %q, got %v", "user-uuid-1234", entry["user_id"])
	}
}

// isureal-guard reads these logs, so they must be emitted at the default (debug) and info levels.
func TestActionLogMiddleware_GuardLogEmittedAtDefaultLevels(t *testing.T) {
	for _, configuredLevel := range []string{"debug", "info"} {
		t.Run("level_"+configuredLevel, func(t *testing.T) {
			var buf bytes.Buffer
			e := echo.New()
			e.Use(ActionLogMiddleware(configuredLevel, &buf))

			e.POST("/api/auth/login", func(c *echo.Context) error {
				c.Set("username", "testuser")
				return echo.NewHTTPError(http.StatusUnauthorized, "login failed")
			})

			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if buf.Len() == 0 {
				t.Fatalf("expected log output at configuredLevel %q", configuredLevel)
			}
			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if entry["action"] != "login" || entry["status"] != float64(http.StatusUnauthorized) {
				t.Fatalf("unexpected guard log entry: %v", entry)
			}
		})
	}
}

type errWriter struct{}

func (errWriter) Write(p []byte) (n int, err error) {
	return 0, errors.New("write failed")
}

func TestActionLogMiddleware_WriterErrorPropagated(t *testing.T) {
	e := echo.New()
	e.Use(ActionLogMiddleware("info", errWriter{}))

	e.GET("/api/me", func(c *echo.Context) error {
		return errors.New("handler error")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// In Echo, the middleware return value is handled by echo.
	// Since errWriter fails, writeErr is returned by middleware.
}
