package isureal

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func TestExtractClientIP_HeaderPriorityAndDirectFallback(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		wantIP     string
	}{
		{
			name: "X-Client-IP takes precedence",
			headers: map[string]string{
				"X-Client-IP":     "203.0.113.10",
				"X-Forwarded-For": "198.51.100.1",
				"X-Real-IP":       "198.51.100.2",
			},
			remoteAddr: "192.0.2.1:1234",
			wantIP:     "203.0.113.10",
		},
		{
			name: "X-Forwarded-For ignored by extractClientIP direct fallback",
			headers: map[string]string{
				"X-Forwarded-For": "198.51.100.1",
			},
			remoteAddr: "192.0.2.2:5678",
			wantIP:     "192.0.2.2",
		},
		{
			name: "X-Real-IP ignored by extractClientIP direct fallback",
			headers: map[string]string{
				"X-Real-IP": "198.51.100.2",
			},
			remoteAddr: "192.0.2.3:8080",
			wantIP:     "192.0.2.3",
		},
		{
			name:       "RemoteAddr with port returns IP only",
			headers:    nil,
			remoteAddr: "192.0.2.4:4321",
			wantIP:     "192.0.2.4",
		},
		{
			name:       "RemoteAddr without port returns host when valid IP",
			headers:    nil,
			remoteAddr: "192.0.2.5",
			wantIP:     "192.0.2.5",
		},
		{
			name:       "RemoteAddr IPv6 with port",
			headers:    nil,
			remoteAddr: "[2001:db8::1]:1234",
			wantIP:     "2001:db8::1",
		},
		{
			name:       "Malformed RemoteAddr returns empty string via ExtractIPDirect",
			headers:    nil,
			remoteAddr: "invalid-ip:port:extra",
			wantIP:     "",
		},
		{
			name: "Empty X-Client-IP falls back to RemoteAddr",
			headers: map[string]string{
				"X-Client-IP": "",
			},
			remoteAddr: "192.0.2.6:9999",
			wantIP:     "192.0.2.6",
		},
		{
			name: "Malformed X-Client-IP is returned verbatim without validation",
			headers: map[string]string{
				"X-Client-IP": "not-a-valid-ip",
			},
			remoteAddr: "192.0.2.7:1234",
			wantIP:     "not-a-valid-ip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			req.RemoteAddr = tt.remoteAddr

			got := extractClientIP(req)
			if got != tt.wantIP {
				t.Fatalf("extractClientIP() = %q, want %q", got, tt.wantIP)
			}
		})
	}
}

// Checked through IPBanMiddleware rather than IPBanList methods so it runs against every implementation.
func TestIPBanMiddleware_SnapshotTable(t *testing.T) {
	now := time.Now()
	past := now.Add(-10 * time.Minute)
	future := now.Add(10 * time.Minute)

	list := &IPBanList{}
	list.snapshot.Store(&ipBanSnapshot{
		loadedAt: now,
		bans: map[string]time.Time{
			"192.0.2.10":   future,
			"192.0.2.11":   past,
			"2001:db8::1":  future,
			"192.0.2.0/24": future, // Literal CIDR stored in map
		},
	})

	tests := []struct {
		name       string
		targetIP   string
		wantBanned bool
	}{
		{
			name:       "Active ban",
			targetIP:   "192.0.2.10",
			wantBanned: true,
		},
		{
			name:       "Expired ban",
			targetIP:   "192.0.2.11",
			wantBanned: false,
		},
		{
			name:       "Unbanned IP",
			targetIP:   "192.0.2.99",
			wantBanned: false,
		},
		{
			name:       "IPv6 active ban",
			targetIP:   "2001:db8::1",
			wantBanned: true,
		},
		{
			name:       "CIDR exact string match returns true if stored literally",
			targetIP:   "192.0.2.0/24",
			wantBanned: true,
		},
		{
			name:       "CIDR range evaluation not supported by exact map lookup",
			targetIP:   "192.0.2.5",
			wantBanned: false,
		},
		{
			name:       "Empty string check",
			targetIP:   "",
			wantBanned: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ipBanMiddlewareRejects(t, list, tt.targetIP)
			if got != tt.wantBanned {
				t.Fatalf("banned(%q) = %v, want %v", tt.targetIP, got, tt.wantBanned)
			}
		})
	}
}

func ipBanMiddlewareRejects(t *testing.T, list *IPBanList, ip string) bool {
	t.Helper()
	e := echo.New()
	e.IPExtractor = extractClientIP
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Client-IP", ip)
	c := e.NewContext(req, httptest.NewRecorder())
	err := IPBanMiddleware(&HandlerContext{IPBans: list})(func(c *echo.Context) error { return nil })(c)
	var apiErr *APIError
	if err != nil && !(errors.As(err, &apiErr) && apiErr.Code == "ip_banned") {
		t.Fatalf("unexpected error: %v", err)
	}
	return err != nil
}

func TestIPBanMiddleware_Behavior(t *testing.T) {
	bannedIP := "192.0.2.100"
	allowedIP := "192.0.2.200"

	ipBans := &IPBanList{}
	ipBans.snapshot.Store(&ipBanSnapshot{
		loadedAt: time.Now(),
		bans: map[string]time.Time{
			bannedIP: time.Now().Add(10 * time.Minute),
		},
	})

	h := &HandlerContext{
		DB:     nil,
		IPBans: ipBans,
	}

	e := echo.New()
	e.IPExtractor = extractClientIP

	mw := IPBanMiddleware(h)
	called := false
	handler := mw(func(c *echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	})

	t.Run("Allowed IP passes through", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Client-IP", allowedIP)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := handler(c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !called {
			t.Fatalf("expected next handler to be called")
		}
	})

	t.Run("Banned IP returns 403 APIError", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Client-IP", bannedIP)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := handler(c)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if called {
			t.Fatalf("next handler should not be called when banned")
		}

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("expected APIError, got %T: %v", err, err)
		}
		if apiErr.Status != http.StatusForbidden {
			t.Fatalf("expected status %d, got %d", http.StatusForbidden, apiErr.Status)
		}
		if apiErr.Code != "ip_banned" {
			t.Fatalf("expected code 'ip_banned', got %q", apiErr.Code)
		}
	})
}
