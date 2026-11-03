package isureal

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

const ipBanReloadInterval = 30 * time.Second

var ipBanNow = time.Now

type ipFailureDetector struct {
	threshold int
	window    time.Duration
	failures  map[string][]time.Time
	lastSweep time.Time
}

func newDetector(threshold int, window time.Duration) *ipFailureDetector {
	return &ipFailureDetector{
		threshold: threshold,
		window:    window,
		failures:  make(map[string][]time.Time),
	}
}

func (d *ipFailureDetector) observe(ip string, at time.Time) bool {
	if d.failures == nil {
		d.failures = make(map[string][]time.Time)
	}
	d.sweep(at)
	kept := d.failures[ip][:0]
	for _, failedAt := range d.failures[ip] {
		if at.Sub(failedAt) < d.window {
			kept = append(kept, failedAt)
		}
	}
	kept = append(kept, at)
	if len(kept) >= d.threshold {
		delete(d.failures, ip)
		return true
	}
	d.failures[ip] = kept
	return false
}

func (d *ipFailureDetector) sweep(now time.Time) {
	if now.Sub(d.lastSweep) < d.window {
		return
	}
	d.lastSweep = now
	for ip, times := range d.failures {
		if len(times) == 0 || now.Sub(times[len(times)-1]) >= d.window {
			delete(d.failures, ip)
		}
	}
}

func loadInprocGuardConfig() (loginThreshold int, loginWindow time.Duration, banDuration time.Duration) {
	loginThreshold = 10
	loginWindow = 10 * time.Second
	banDuration = 2 * time.Minute

	if v := os.Getenv("GUARD_FAIL_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			loginThreshold = n
		}
	}
	if v := os.Getenv("GUARD_FAIL_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			loginWindow = d
		}
	}
	if v := os.Getenv("GUARD_BAN_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			banDuration = d
		}
	}
	return loginThreshold, loginWindow, banDuration
}

type IPBanList struct {
	snapshot atomic.Pointer[ipBanSnapshot]
	trigger  chan struct{}

	db              *sqlx.DB
	localBans       atomic.Pointer[map[string]time.Time]
	guardMu         sync.Mutex
	loginDet        *ipFailureDetector
	invalidImageDet *ipFailureDetector
	invalidSigDet   *ipFailureDetector
	banDuration     time.Duration
	resetEpoch      uint64
	banCtx          context.Context
	banCancel       context.CancelFunc
	banSem          chan struct{}
	files           *banFileSyncer
}

type ipBanSnapshot struct {
	loadedAt time.Time
	bans     map[string]time.Time
}

type ipBan struct {
	IP          string    `db:"ip"`
	BannedUntil time.Time `db:"banned_until"`
}

func (l *IPBanList) ensureDetectorsLocked() {
	if l.loginDet == nil {
		loginThresh, loginWin, banDur := loadInprocGuardConfig()
		l.loginDet = newDetector(loginThresh, loginWin)
		l.invalidImageDet = newDetector(2, 5*time.Second)
		l.invalidSigDet = newDetector(3, 5*time.Second)
		l.banDuration = banDur
		l.banCtx, l.banCancel = context.WithCancel(context.Background())
	}
	if l.banSem == nil {
		l.banSem = make(chan struct{}, 16)
	}
}

func NewIPBanList(db *sqlx.DB) *IPBanList {
	l := &IPBanList{
		db:      db,
		trigger: make(chan struct{}, 1),
		files:   newBanFileSyncer(os.Getenv("ISUREAL_IPBAN_DIR")),
	}
	l.guardMu.Lock()
	l.ensureDetectorsLocked()
	l.guardMu.Unlock()
	if db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = l.Reload(ctx, db)
		cancel()
		go l.reloadLoop(db)
	}
	return l
}

func (l *IPBanList) TriggerReload() {
	if l == nil || l.trigger == nil {
		return
	}
	select {
	case l.trigger <- struct{}{}:
	default:
	}
}

func (l *IPBanList) reloadLoop(db *sqlx.DB) {
	ticker := time.NewTicker(ipBanReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-l.trigger:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = l.Reload(ctx, db)
		cancel()
	}
}

func (l *IPBanList) Reload(ctx context.Context, db *sqlx.DB) error {
	if db == nil {
		return nil
	}
	loaded, err := loadIPBans(ctx, db)
	if err != nil {
		return err
	}
	l.snapshot.Store(loaded)

	if l.localBans.Load() != nil {
		l.guardMu.Lock()
		if curLocal := l.localBans.Load(); curLocal != nil {
			now := ipBanNow()
			pruned := make(map[string]time.Time)
			for ip, localUntil := range *curLocal {
				if now.Before(localUntil) {
					if dbUntil, ok := loaded.bans[ip]; !ok || dbUntil.Before(localUntil) {
						pruned[ip] = localUntil
					}
				}
			}
			if len(pruned) == 0 {
				l.localBans.Store(nil)
			} else {
				l.localBans.Store(&pruned)
			}
		}
		l.guardMu.Unlock()
	}
	l.syncBanFiles()
	return nil
}

func (l *IPBanList) syncBanFiles() {
	if l == nil || l.files == nil {
		return
	}
	l.files.sync(func() []map[string]time.Time {
		var maps []map[string]time.Time
		if snapshot := l.snapshot.Load(); snapshot != nil && snapshot.bans != nil {
			maps = append(maps, snapshot.bans)
		}
		if local := l.localBans.Load(); local != nil && *local != nil {
			maps = append(maps, *local)
		}
		return maps
	}, l.syncBanFiles)
}

func (l *IPBanList) IsBanned(ip string) bool {
	if l == nil || ip == "" {
		return false
	}
	now := ipBanNow()
	if snapshot := l.snapshot.Load(); snapshot != nil {
		if until, ok := snapshot.bans[ip]; ok && now.Before(until) {
			return true
		}
	}
	if local := l.localBans.Load(); local != nil {
		if until, ok := (*local)[ip]; ok && now.Before(until) {
			return true
		}
	}
	return false
}

func (l *IPBanList) ObserveActionFailure(action string, status int, ip string, abuse string) {
	if l == nil || ip == "" || os.Getenv("ISUREAL_DISABLE_INPROC_BAN") == "1" {
		return
	}

	var kind string
	switch {
	case action == "login" && status == http.StatusUnauthorized:
		kind = "login"
	case action == "put_profile_image" && status == http.StatusBadRequest && abuse == "invalid_image":
		kind = "invalid_image"
	case action == "get_image" && status == http.StatusForbidden && abuse == "invalid_signature":
		kind = "invalid_signature"
	default:
		return
	}

	l.guardMu.Lock()
	l.ensureDetectorsLocked()
	now := ipBanNow()

	var det *ipFailureDetector
	switch kind {
	case "login":
		det = l.loginDet
	case "invalid_image":
		det = l.invalidImageDet
	case "invalid_signature":
		det = l.invalidSigDet
	}

	if det == nil || !det.observe(ip, now) {
		l.guardMu.Unlock()
		return
	}

	nextMap := make(map[string]time.Time)
	if oldLocal := l.localBans.Load(); oldLocal != nil {
		for k, u := range *oldLocal {
			if now.Before(u) {
				nextMap[k] = u
			}
		}
	}
	until := now.Add(l.banDuration)
	if prev, ok := nextMap[ip]; ok && prev.After(until) {
		until = prev
	}
	nextMap[ip] = until
	l.localBans.Store(&nextMap)

	db := l.db
	banDurSecs := l.banDuration.Seconds()
	epoch := l.resetEpoch
	parentCtx := l.banCtx
	sem := l.banSem
	l.guardMu.Unlock()
	l.syncBanFiles()

	if db != nil {
		select {
		case sem <- struct{}{}:
			go func() {
				defer func() { <-sem }()
				l.guardMu.Lock()
				if l.resetEpoch != epoch {
					l.guardMu.Unlock()
					return
				}
				l.guardMu.Unlock()

				ctx, cancel := context.WithTimeout(parentCtx, 3*time.Second)
				defer cancel()

				_, _ = db.ExecContext(ctx, `
					INSERT INTO ip_bans (ip, banned_until)
					VALUES ($1, clock_timestamp() + make_interval(secs => $2))
					ON CONFLICT (ip) DO UPDATE SET banned_until = GREATEST(ip_bans.banned_until, EXCLUDED.banned_until)
				`, ip, banDurSecs)
			}()
		default:
		}
	}
}

func (l *IPBanList) ResetLocalState() {
	if l == nil {
		return
	}
	l.guardMu.Lock()
	defer l.guardMu.Unlock()

	l.resetEpoch++
	if l.banCancel != nil {
		l.banCancel()
	}
	l.banCtx, l.banCancel = context.WithCancel(context.Background())
	loginThresh, loginWin, banDur := loadInprocGuardConfig()
	l.loginDet = newDetector(loginThresh, loginWin)
	l.invalidImageDet = newDetector(2, 5*time.Second)
	l.invalidSigDet = newDetector(3, 5*time.Second)
	l.banDuration = banDur
	if l.banSem == nil {
		l.banSem = make(chan struct{}, 16)
	}
	l.localBans.Store(nil)
	l.syncBanFiles()
}

func loadIPBans(ctx context.Context, db *sqlx.DB) (*ipBanSnapshot, error) {
	var rows []ipBan
	if err := db.SelectContext(ctx, &rows, `SELECT ip, banned_until FROM ip_bans WHERE banned_until > clock_timestamp()`); err != nil {
		return nil, err
	}
	bans := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		bans[row.IP] = row.BannedUntil
	}
	return &ipBanSnapshot{loadedAt: ipBanNow(), bans: bans}, nil
}

func extractClientIP(req *http.Request) string {
	if ip := req.Header.Get("X-Client-IP"); ip != "" {
		return ip
	}
	return echo.ExtractIPDirect()(req)
}

func IPBanMiddleware(h *HandlerContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if h.IPBans.IsBanned(c.RealIP()) {
				return NewAPIError(http.StatusForbidden, "ip_banned")
			}
			return next(c)
		}
	}
}
