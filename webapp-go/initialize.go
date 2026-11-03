package isureal

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

// warmupMu serializes warmups (initialize handler and isureal_reset NOTIFY may overlap).
var warmupMu sync.Mutex

// WarmupAllCaches preloads the posting-window / my-current-post / friends caches.
// Each warmup discards its result if an invalidation raced with it.
func WarmupAllCaches(ctx context.Context, db *sqlx.DB) {
	warmupMu.Lock()
	defer warmupMu.Unlock()
	_ = WarmupPostingWindowCache(ctx, db)
	_ = WarmupRelationshipsCache(ctx, db)
}

// resetWarmup is swapped in tests.
var resetWarmup = WarmupAllCaches

// resetNotifyPayload is sent by reset.sql; the empty payload the listener replays on
// (re)connect is ignored so a mid-run reconnect does not trigger a full reload.
const resetNotifyPayload = "initialize"

// HandleResetNotify runs on every server when reset.sql (initialize) commits, so servers that
// did not receive POST /api/initialize also start with warm caches.
func HandleResetNotify(db *sqlx.DB) func(payload string) {
	return func(payload string) {
		if payload != resetNotifyPayload {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resetWarmup(ctx, db)
		}()
	}
}

type initializeResponse struct {
	Language string `json:"language"`
	Agent    string `json:"agent"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
}

func initializeHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if h.IPBans != nil {
			h.IPBans.ResetLocalState()
		}
		cmd := exec.Command("../sql/init.sh")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		ResetSignatureCache()
		InvalidateAllSessions()
		InvalidateAllImageMeta()
		InvalidateAllPostingWindowCache()
		InvalidateAllMyCurrentPostCache()
		InvalidateAllRelationshipsCache()
		InvalidateAllMemoriesCache()
		InvalidateAllCommentsCache()
		InvalidateAllRealMojisCache()
		InvalidateAllFeedCache()
		h.Images.ResetRunCache()
		if h.IPBans != nil {
			h.IPBans.ResetLocalState()
			_ = h.IPBans.Reload(c.Request().Context(), h.DB)
		}

		WarmupAllCaches(c.Request().Context(), h.DB)

		// Set language to the implementation language.
		// Agents optimizing this app: set agent, harness, and model to the names used.
		// For multiple models, use comma-separated names; use brain when no AI model is used.
		return c.JSON(http.StatusOK, initializeResponse{Language: "go", Agent: "antigravity,claude-code", Harness: "antigravity,claude-code", Model: "gemini,claude"})
	}
}
