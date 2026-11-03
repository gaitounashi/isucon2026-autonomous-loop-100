package isureal

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- unit tests (no DB) ---

func TestImageCacheHasLocal_BothTiers(t *testing.T) {
	c := NewImageCache(t.TempDir())
	if c.HasLocal("img-run") || c.HasLocal("img-init") {
		t.Fatalf("empty cache must report nothing local")
	}
	c.Put("img-run", imageInitBoundary.Add(time.Hour), []byte("r"))
	c.Put("img-init", imageInitBoundary.Add(-time.Hour), []byte("i"))
	if !c.HasLocal("img-run") || !c.HasLocal("img-init") {
		t.Fatalf("HasLocal must find files in both the run and init tiers")
	}
}

func TestImagePrefetcher_HandleNotifySkipsAndNeverBlocks(t *testing.T) {
	images := NewImageCache(t.TempDir())
	p := &ImagePrefetcher{images: images, queue: make(chan string, 1)} // no workers: inspect the queue

	p.MarkLocal("uploaded-here")
	p.HandleNotify("uploaded-here")
	images.Put("already-local", time.Now(), []byte("x"))
	p.HandleNotify("already-local")
	p.HandleNotify("")
	p.HandleNotify("../etc/passwd")
	if len(p.queue) != 0 {
		t.Fatalf("local / cached / invalid IDs must not be queued, got %d", len(p.queue))
	}

	p.UnmarkLocal("uploaded-here")
	p.HandleNotify("uploaded-here")
	p.HandleNotify("queue-full") // queue capacity 1: must drop instead of blocking the listener
	if len(p.queue) != 1 || <-p.queue != "uploaded-here" {
		t.Fatalf("expected exactly the first remote image to be queued")
	}

	var nilP *ImagePrefetcher
	nilP.HandleNotify("x") // handlers built without a prefetcher must not panic
	nilP.MarkLocal("x")
	nilP.UnmarkLocal("x")
}

// --- integration tests (ISUREAL_TEST_DB=1) ---

func prefetchTestImageID(t *testing.T, env *testEnv, postID string) string {
	t.Helper()
	var imageID string
	if err := env.DB.Get(&imageID, `SELECT image_id FROM posts WHERE id = $1`, postID); err != nil {
		t.Fatalf("image id: %v", err)
	}
	return imageID
}

func TestImagePrefetch_FetchesRemoteImageToDisk(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	author := env.registerUser("pf_author")
	postID := env.createPost(author)
	imageID := prefetchTestImageID(t, env, postID)

	// another server: empty disk, never saw the upload
	remote := NewImageCache(t.TempDir())
	p := &ImagePrefetcher{db: env.DB, images: remote, queue: make(chan string, 1)}
	p.fetch(imageID)

	got, ok := remote.Get(imageID, time.Now())
	if !ok || !bytes.Equal(got, tinyJPEG()) {
		t.Fatalf("prefetch must write the image bytes to the local disk cache (ok=%v, %d bytes)", ok, len(got))
	}

	// the combined loader: no bytes unless asked for, and none for a dead image
	loaded, err := loadImageMetaAndData(context.Background(), env.DB, imageID, false, imageMetaCacheGeneration())
	if err != nil || loaded.AliveKind != 2 || loaded.Data != nil || loaded.OwnerUserID != author.ID {
		t.Fatalf("meta-only load: %+v err=%v", loaded, err)
	}
	if rec := env.do(http.MethodDelete, "/api/posts/"+postID, author.Token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete post: %d %s", rec.Code, rec.Body.String())
	}
	loaded, err = loadImageMetaAndData(context.Background(), env.DB, imageID, true, imageMetaCacheGeneration())
	if err != nil || loaded.AliveKind != 0 || loaded.Data != nil {
		t.Fatalf("dead image must load no bytes: %+v err=%v", loaded, err)
	}
}

func TestImageGet_ColdPathServesBytesFromSingleQuery(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	author := env.registerUser("pf_cold_author")
	postID := env.createPost(author)
	imageID := prefetchTestImageID(t, env, postID)

	rec := env.do(http.MethodGet, "/api/posts/"+postID, author.Token, nil)
	post := decodeJSON[struct {
		ImageURL string `json:"imageUrl"`
	}](t, rec)
	if rec.Code != http.StatusOK || post.ImageURL == "" {
		t.Fatalf("get post: %d %s", rec.Code, rec.Body.String())
	}

	// simulate a server that does not have the file yet
	_ = os.Remove(filepath.Join(globalImageDir, "run", strings.ToLower(imageID[:2]), imageID))
	InvalidateAllImageMeta()

	rec = env.do(http.MethodGet, post.ImageURL, "", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), tinyJPEG()) {
		t.Fatalf("cold image GET: want 200 with the uploaded bytes, got %d (%d bytes)", rec.Code, rec.Body.Len())
	}
	if _, err := os.Stat(filepath.Join(globalImageDir, "run", strings.ToLower(imageID[:2]), imageID)); err != nil {
		t.Fatalf("cold image GET must also populate the disk cache: %v", err)
	}
	// warm path (from disk) returns the same bytes
	rec = env.do(http.MethodGet, post.ImageURL, "", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), tinyJPEG()) {
		t.Fatalf("warm image GET: got %d (%d bytes)", rec.Code, rec.Body.Len())
	}
}

func TestImageCachePut_RecreatesRemovedShardDir(t *testing.T) {
	dir := t.TempDir()
	c := NewImageCache(dir)
	now := time.Now()
	c.Put("ab-first", now, []byte("1"))
	if err := os.RemoveAll(filepath.Join(dir, "run")); err != nil {
		t.Fatal(err)
	}
	c.Put("ab-second", now, []byte("2")) // same shard "ab", cached as already created
	if got, ok := c.Get("ab-second", now); !ok || string(got) != "2" {
		t.Fatalf("Put must recreate a shard dir that was removed externally")
	}
}
