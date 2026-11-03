package isureal

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	imageMetaCacheMaxEntries = 200000
)

var imageMetaCacheTTL = func() time.Duration {
	if v := os.Getenv("ISUREAL_IMAGE_META_CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	// Liveness changes are pushed to every server via isureal_image_meta NOTIFY
	// (024-image-meta-notify.sql), so the TTL is only a safety net.
	return 60 * time.Second
}()

var imageMetaCacheNow = time.Now

type cachedImageMeta struct {
	OwnerUserID string
	SigningKey  []byte
	MediaType   string
	CreatedAt   time.Time
	CachedAt    time.Time
}

type loadedImageMeta struct {
	OwnerUserID string
	SigningKey  []byte
	MediaType   string
	CreatedAt   time.Time
	AliveKind   int // 2 = active post image, 1 = active profile/realmoji image, 0 = deleted/not alive (only alive ones are cached)
	Data        []byte // image bytes when loaded together with the meta (already written to the disk cache)
}

func imageMetaCacheDisabled() bool {
	return os.Getenv("ISUREAL_DISABLE_IMAGE_META_CACHE") == "1"
}

var (
	imageMetaCacheMu       sync.RWMutex
	imageMetaCacheByID     = make(map[string]cachedImageMeta)
	imageMetaCacheByUserID = make(map[string]map[string]struct{})
	imageMetaCacheGen      uint64

	imageMetaInflightMu sync.Mutex
	imageMetaInflight   = make(map[string]*imageMetaFlight)
)

func imageMetaCacheGeneration() uint64 {
	return atomic.LoadUint64(&imageMetaCacheGen)
}

func getCachedImageMeta(imageID string) (cachedImageMeta, bool) {
	if imageID == "" || imageMetaCacheDisabled() {
		return cachedImageMeta{}, false
	}
	now := imageMetaCacheNow()

	imageMetaCacheMu.RLock()
	entry, ok := imageMetaCacheByID[imageID]
	imageMetaCacheMu.RUnlock()

	if !ok {
		return cachedImageMeta{}, false
	}
	if now.Sub(entry.CachedAt) > imageMetaCacheTTL || now.Before(entry.CachedAt) {
		return cachedImageMeta{}, false
	}
	cp := entry
	if len(entry.SigningKey) > 0 {
		cp.SigningKey = append([]byte(nil), entry.SigningKey...)
	}
	return cp, true
}

func setCachedImageMeta(imageID string, entry cachedImageMeta, gen uint64) {
	if imageID == "" || entry.OwnerUserID == "" || len(entry.SigningKey) == 0 || imageMetaCacheDisabled() || imageMetaCacheGeneration() != gen {
		return
	}
	sk := append([]byte(nil), entry.SigningKey...)
	entry.SigningKey = sk
	entry.CachedAt = imageMetaCacheNow()

	imageMetaCacheMu.Lock()
	defer imageMetaCacheMu.Unlock()

	if atomic.LoadUint64(&imageMetaCacheGen) != gen {
		return
	}

	if len(imageMetaCacheByID) >= imageMetaCacheMaxEntries {
		clearAllImageMetaLocked()
	}

	if old, exists := imageMetaCacheByID[imageID]; exists && old.OwnerUserID != "" {
		if old.OwnerUserID != entry.OwnerUserID {
			if ids, ok := imageMetaCacheByUserID[old.OwnerUserID]; ok {
				delete(ids, imageID)
				if len(ids) == 0 {
					delete(imageMetaCacheByUserID, old.OwnerUserID)
				}
			}
		}
	}

	imageMetaCacheByID[imageID] = entry
	ids, ok := imageMetaCacheByUserID[entry.OwnerUserID]
	if !ok {
		ids = make(map[string]struct{})
		imageMetaCacheByUserID[entry.OwnerUserID] = ids
	}
	ids[imageID] = struct{}{}
}

func InvalidateImageMetaForUser(userID string) {
	if userID == "" {
		return
	}
	imageMetaCacheMu.Lock()
	defer imageMetaCacheMu.Unlock()
	atomic.AddUint64(&imageMetaCacheGen, 1)

	ids, ok := imageMetaCacheByUserID[userID]
	if !ok {
		return
	}
	for imageID := range ids {
		delete(imageMetaCacheByID, imageID)
	}
	delete(imageMetaCacheByUserID, userID)
}

func InvalidateImageMeta(imageID string) {
	if imageID == "" {
		return
	}
	imageMetaCacheMu.Lock()
	defer imageMetaCacheMu.Unlock()
	atomic.AddUint64(&imageMetaCacheGen, 1)

	entry, ok := imageMetaCacheByID[imageID]
	if !ok {
		return
	}
	delete(imageMetaCacheByID, imageID)
	if ids, ok := imageMetaCacheByUserID[entry.OwnerUserID]; ok {
		delete(ids, imageID)
		if len(ids) == 0 {
			delete(imageMetaCacheByUserID, entry.OwnerUserID)
		}
	}
}

// HandleImageMetaNotify applies an isureal_image_meta payload: "*" clears everything,
// otherwise it is a comma-separated list of image IDs that are no longer alive.
func HandleImageMetaNotify(payload string) {
	if payload == "" || payload == "*" {
		InvalidateAllImageMeta()
		return
	}
	for _, id := range strings.Split(payload, ",") {
		InvalidateImageMeta(id)
	}
}

func InvalidateAllImageMeta() {
	imageMetaCacheMu.Lock()
	defer imageMetaCacheMu.Unlock()
	atomic.AddUint64(&imageMetaCacheGen, 1)
	clearAllImageMetaLocked()
}

func clearAllImageMetaLocked() {
	imageMetaCacheByID = make(map[string]cachedImageMeta)
	imageMetaCacheByUserID = make(map[string]map[string]struct{})
}

type imageMetaFlight struct {
	done chan struct{}
	val  loadedImageMeta
	err  error
}

func singleflightLoadImageMeta(imageID string, fn func() (loadedImageMeta, error)) (loadedImageMeta, error) {
	imageMetaInflightMu.Lock()
	if f, ok := imageMetaInflight[imageID]; ok {
		imageMetaInflightMu.Unlock()
		<-f.done
		return f.val, f.err
	}

	f := &imageMetaFlight{
		done: make(chan struct{}),
	}
	imageMetaInflight[imageID] = f
	imageMetaInflightMu.Unlock()

	defer func() {
		imageMetaInflightMu.Lock()
		delete(imageMetaInflight, imageID)
		imageMetaInflightMu.Unlock()
		close(f.done)
	}()

	f.val, f.err = fn()
	return f.val, f.err
}
