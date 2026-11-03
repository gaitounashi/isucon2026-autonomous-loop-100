package isureal

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// imageMetaAndDataQuery loads an image's meta and liveness and, when $2 is true and the image is
// alive, its bytes too, all in one round trip. $3 = imageInitBoundary: initial images live in
// image_objects, run images in active_image_objects (026/027).
const imageMetaAndDataQuery = `
	SELECT x.owner_user_id, x.secret_key_hash, x.media_type, x.created_at, x.alive_kind,
	       CASE WHEN $2::boolean AND x.alive_kind > 0 THEN
	         CASE WHEN x.created_at < $3::timestamptz
	              THEN (SELECT o.data FROM image_objects o WHERE o.id = x.id AND o.created_at = x.created_at)
	              ELSE (SELECT a.data FROM active_image_objects a WHERE a.id = x.id)
	         END
	       END AS data
	FROM (
		SELECT
			m.id,
			m.owner_user_id,
			u.secret_key_hash,
			m.media_type,
			m.created_at,
			CASE
				WHEN u.profile_image_id = m.id THEN 1
				WHEN EXISTS (
					SELECT 1 FROM posts p
					WHERE p.image_id = m.id AND p.author_id = u.id AND p.deleted_at IS NULL
				) THEN 2
				WHEN EXISTS (
					SELECT 1 FROM realmojis r
					JOIN posts p ON p.id = r.post_id AND p.deleted_at IS NULL
					JOIN users author ON author.id = p.author_id AND author.status = 'active'
					WHERE r.image_id = m.id AND r.reactor_id = u.id AND r.replaced_at IS NULL AND r.deleted_at IS NULL
				) THEN 1
				ELSE 0
			END AS alive_kind
		FROM image_meta m
		JOIN users u ON u.id = m.owner_user_id
		WHERE m.id = $1 AND u.status = 'active'
	) x
`

// loadImageMetaAndData runs imageMetaAndDataQuery and caches the meta when alive. gen must be read
// before the call so a liveness change that raced with the load is not cached. Data is nil unless
// withData is set and the image is alive.
func loadImageMetaAndData(ctx context.Context, db sqlx.QueryerContext, imageID string, withData bool, gen uint64) (loadedImageMeta, error) {
	var row struct {
		OwnerUserID   string    `db:"owner_user_id"`
		SecretKeyHash string    `db:"secret_key_hash"`
		MediaType     string    `db:"media_type"`
		CreatedAt     time.Time `db:"created_at"`
		AliveKind     int       `db:"alive_kind"`
		Data          []byte    `db:"data"`
	}
	if err := sqlx.GetContext(ctx, db, &row, imageMetaAndDataQuery, imageID, withData, imageInitBoundary); err != nil {
		return loadedImageMeta{}, err
	}
	key, err := imageSigningKey(row.SecretKeyHash)
	if err != nil {
		return loadedImageMeta{}, err
	}
	if row.AliveKind > 0 {
		setCachedImageMeta(imageID, cachedImageMeta{
			OwnerUserID: row.OwnerUserID,
			SigningKey:  key,
			MediaType:   row.MediaType,
			CreatedAt:   row.CreatedAt,
		}, gen)
	}
	return loadedImageMeta{
		OwnerUserID: row.OwnerUserID,
		SigningKey:  key,
		MediaType:   row.MediaType,
		CreatedAt:   row.CreatedAt,
		AliveKind:   row.AliveKind,
		Data:        row.Data,
	}, nil
}

// HasLocal reports whether the image bytes are already on this server's disk (either tier).
func (c *ImageCache) HasLocal(id string) bool {
	if _, ok := c.LocalPath(id, imageInitBoundary); ok {
		return true
	}
	_, ok := c.LocalPath(id, imageInitBoundary.Add(-time.Hour))
	return ok
}

// ImagePrefetcher copies images written on other servers to the local disk cache (and warms the
// meta cache) as soon as isureal_image_new arrives, so the first GET /api/images/:id here does not
// pull the bytes across regions from the DB.
type ImagePrefetcher struct {
	db     *sqlx.DB
	images *ImageCache
	queue  chan string
	local  sync.Map // image IDs being uploaded through this server
}

const (
	imagePrefetchWorkers   = 4
	imagePrefetchQueueSize = 4096
)

func imagePrefetchDisabled() bool {
	return os.Getenv("ISUREAL_DISABLE_IMAGE_PREFETCH") == "1"
}

func NewImagePrefetcher(db *sqlx.DB, images *ImageCache) *ImagePrefetcher {
	p := &ImagePrefetcher{db: db, images: images, queue: make(chan string, imagePrefetchQueueSize)}
	if db != nil && images != nil && !imagePrefetchDisabled() {
		for i := 0; i < imagePrefetchWorkers; i++ {
			go p.worker()
		}
	}
	return p
}

// MarkLocal/UnmarkLocal bracket an upload handled here: this server writes the file itself.
func (p *ImagePrefetcher) MarkLocal(id string) {
	if p != nil {
		p.local.Store(id, struct{}{})
	}
}

func (p *ImagePrefetcher) UnmarkLocal(id string) {
	if p != nil {
		p.local.Delete(id)
	}
}

func (p *ImagePrefetcher) skip(id string) bool {
	if _, ok := p.local.Load(id); ok {
		return true
	}
	return !validCacheImageID(id) || p.images.HasLocal(id)
}

// HandleNotify is the isureal_image_new listener: never blocks the listener goroutine.
func (p *ImagePrefetcher) HandleNotify(payload string) {
	if p == nil || payload == "" || imagePrefetchDisabled() || p.skip(payload) {
		return
	}
	select {
	case p.queue <- payload:
	default: // overloaded: the request path still loads it on demand
	}
}

func (p *ImagePrefetcher) worker() {
	for id := range p.queue {
		p.fetch(id)
	}
}

func (p *ImagePrefetcher) fetch(id string) {
	if p.skip(id) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gen := imageMetaCacheGeneration()
	_, _ = singleflightLoadImageMeta(id, func() (loadedImageMeta, error) {
		loaded, err := loadImageMetaAndData(ctx, p.db, id, true, gen)
		if err == nil && loaded.Data != nil {
			p.images.Put(id, loaded.CreatedAt, loaded.Data)
		}
		return loaded, err
	})
}
