package isureal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type testEnv struct {
	T  *testing.T
	E  *echo.Echo
	DB *sqlx.DB
}

type testUser struct {
	ID          string
	Username    string
	DisplayName string
	Region      string
	SecretKey   string
	Token       string
}

var (
	globalOnce        sync.Once
	globalEcho        *echo.Echo
	globalDB          *sqlx.DB
	globalErr         error
	globalImageDir    string
	globalTruncateSQL string
	cachedTinyJPEG    []byte
	cachedTinyPNG     []byte
)

func init() {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var jBuf bytes.Buffer
	if err := jpeg.Encode(&jBuf, img, nil); err == nil {
		cachedTinyJPEG = jBuf.Bytes()
	}
	var pBuf bytes.Buffer
	if err := png.Encode(&pBuf, img); err == nil {
		cachedTinyPNG = pBuf.Bytes()
	}
}

func readSchemaFile(path string) ([]byte, error) {
	candidates := []string{
		path,
		strings.TrimPrefix(path, "../"),
		filepath.Join("webapp", strings.TrimPrefix(path, "../")),
	}
	for _, c := range candidates {
		if data, err := os.ReadFile(c); err == nil {
			return data, nil
		}
	}
	return os.ReadFile(path)
}

// testEnvResetHooks are called after every table truncation in newTestEnv.
// Implementation-specific caches register themselves from non-integration test files.
var testEnvResetHooks []func()

func newTestEnv(t *testing.T) *testEnv {
	if os.Getenv("ISUREAL_TEST_DB") != "1" {
		t.Skip("set ISUREAL_TEST_DB=1 to run DB integration tests")
	}
	t.Setenv("ISUREAL_DISABLE_IMAGE_META_CACHE", "1")
	t.Setenv("ISUREAL_DISABLE_INPROC_BAN", "1")

	globalOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "isureal-image-cache-*")
		if err != nil {
			globalErr = fmt.Errorf("failed to create temp dir for image cache: %w", err)
			return
		}
		globalImageDir = tempDir
		// Passed via env so the harness also compiles against versions without Config.ImageCacheDir.
		os.Setenv("ISUREAL_IMAGE_CACHE_DIR", tempDir)

		cfg := ConfigFromEnv()
		if region := os.Getenv("ISUREAL_REGION"); region != "" {
			cfg.Region = region
		} else {
			cfg.Region = "asia"
		}

		db, err := sqlx.Open("pgx", cfg.DatabaseDSN())
		if err != nil {
			globalErr = fmt.Errorf("failed to open test db: %w", err)
			return
		}
		if err := db.Ping(); err != nil {
			globalErr = fmt.Errorf("failed to ping test db: %w", err)
			return
		}
		globalDB = db
		globalDB.SetMaxOpenConns(50)
		globalDB.SetMaxIdleConns(50)
		globalDB.SetConnMaxLifetime(10 * time.Minute)

		// Disable durability overhead for integration tests
		_, _ = globalDB.Exec(`
			ALTER SYSTEM SET synchronous_commit = 'off';
			ALTER SYSTEM SET fsync = 'off';
			ALTER SYSTEM SET full_page_writes = 'off';
			ALTER DATABASE isureal SET synchronous_commit = 'off';
			ALTER ROLE isureal SET synchronous_commit = 'off';
			SET synchronous_commit = 'off';
		`)

		// Apply every numbered SQL file in order so new migrations are picked up automatically.
		schemaFiles, err := filepath.Glob("../sql/[0-9][0-9][0-9]-*.sql")
		if err != nil || len(schemaFiles) == 0 {
			globalErr = fmt.Errorf("failed to find schema files: %v", err)
			return
		}
		sort.Strings(schemaFiles)
		for _, f := range schemaFiles {
			content, err := readSchemaFile(f)
			if err != nil {
				globalErr = fmt.Errorf("failed to read schema file %s: %w", f, err)
				return
			}
			if _, err := globalDB.Exec(string(content)); err != nil {
				globalErr = fmt.Errorf("failed to apply schema %s: %w", f, err)
				return
			}
		}

		globalEcho = NewServer(cfg)

		// Build the cached TRUNCATE SQL once
		var tables string
		_ = globalDB.Get(&tables, `
			SELECT string_agg(format('%I', tablename), ', ')
			FROM pg_tables
			WHERE schemaname = 'public' AND tablename NOT LIKE 'initial\_%'
			  AND tablename NOT IN ('administrators')
			  AND tablename NOT IN (SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid)`)
		if tables != "" {
			globalTruncateSQL = "TRUNCATE " + tables + " CASCADE;"
		}

		// Ensure default administrator exists
		_, _ = globalDB.Exec(`
			INSERT INTO administrators (id, username, password_hash, created_at)
			VALUES ('00000000-0000-0000-0000-000000000001', 'admin', 'b9c950640e1b3740e98acb93e669c65766f6670dd1609ba91ff41052ba48c6f3', '2024-10-28 00:00:00+00')
			ON CONFLICT (id) DO UPDATE SET username = EXCLUDED.username, password_hash = EXCLUDED.password_hash;
		`)
	})

	if globalErr != nil {
		t.Fatalf("failed to initialize test environment: %v", globalErr)
	}

	// Truncate tables using cached direct TRUNCATE (fallback to dynamic DO $$ block)
	if globalTruncateSQL != "" {
		if _, err := globalDB.Exec(globalTruncateSQL); err != nil {
			t.Fatalf("failed to truncate tables: %v", err)
		}
	} else {
		truncateSQL := `DO $$
DECLARE tables text;
BEGIN
  SELECT string_agg(format('%I', tablename), ', ') INTO tables
  FROM pg_tables
  WHERE schemaname = 'public' AND tablename NOT LIKE 'initial\_%'
    AND tablename NOT IN ('administrators')
    AND tablename NOT IN (SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid);
  IF tables IS NOT NULL THEN
    EXECUTE 'TRUNCATE ' || tables || ' CASCADE';
  END IF;
  INSERT INTO administrators (id, username, password_hash, created_at)
  VALUES ('00000000-0000-0000-0000-000000000001', 'admin', 'b9c950640e1b3740e98acb93e669c65766f6670dd1609ba91ff41052ba48c6f3', '2024-10-28 00:00:00+00')
  ON CONFLICT (id) DO UPDATE SET username = EXCLUDED.username, password_hash = EXCLUDED.password_hash;
END $$;`
		if _, err := globalDB.Exec(truncateSQL); err != nil {
			t.Fatalf("failed to truncate tables: %v", err)
		}
	}

	// Reset in-process caches registered by implementation-specific test files
	// (kept as hooks so this file still compiles against the initial implementation in the baseline CI job).
	for _, reset := range testEnvResetHooks {
		reset()
	}
	if globalImageDir != "" {
		_ = os.RemoveAll(filepath.Join(globalImageDir, "run"))
	}

	return &testEnv{
		T:  t,
		E:  globalEcho,
		DB: globalDB,
	}
}

func (env *testEnv) do(method, path, token string, body any) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	var contentType string

	if body != nil {
		switch v := body.(type) {
		case []byte:
			bodyReader = bytes.NewReader(v)
		case string:
			bodyReader = strings.NewReader(v)
		case io.Reader:
			bodyReader = v
		default:
			data, err := json.Marshal(body)
			if err != nil {
				env.T.Fatalf("failed to marshal request body: %v", err)
			}
			bodyReader = bytes.NewReader(data)
			contentType = "application/json"
		}
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decodeJSON failed: %v, body: %s", err, rec.Body.String())
	}
	return v
}

func (env *testEnv) registerUserInRegion(username, region string) testUser {
	regBody := map[string]string{
		"username":    username,
		"displayName": username,
		"region":      region,
	}
	regRec := env.do(http.MethodPost, "/api/auth/register", "", regBody)
	if regRec.Code < 200 || regRec.Code >= 300 {
		env.T.Fatalf("register failed for %s with status %d: %s", username, regRec.Code, regRec.Body.String())
	}

	type regResponse struct {
		SecretKey string `json:"secretKey"`
		User      struct {
			ID          string `json:"id"`
			Username    string `json:"username"`
			DisplayName string `json:"displayName"`
			Region      string `json:"region"`
		} `json:"user"`
	}
	reg := decodeJSON[regResponse](env.T, regRec)

	loginBody := map[string]string{
		"username":  username,
		"secretKey": reg.SecretKey,
	}
	loginRec := env.do(http.MethodPost, "/api/auth/login", "", loginBody)
	if loginRec.Code < 200 || loginRec.Code >= 300 {
		env.T.Fatalf("login failed for %s with status %d: %s", username, loginRec.Code, loginRec.Body.String())
	}

	type logResponse struct {
		Token string `json:"token"`
	}
	login := decodeJSON[logResponse](env.T, loginRec)

	return testUser{
		ID:          reg.User.ID,
		Username:    username,
		DisplayName: reg.User.DisplayName,
		Region:      reg.User.Region,
		SecretKey:   reg.SecretKey,
		Token:       login.Token,
	}
}

func (env *testEnv) registerUser(username string) testUser {
	return env.registerUserInRegion(username, "asia")
}

func (env *testEnv) adminToken() string {
	adminID := uuid.NewString()
	adminUsername := "admin_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	adminPassword := "admin_password"
	hash := sha256.Sum256([]byte(adminPassword))
	passwordHash := hex.EncodeToString(hash[:])

	_, err := env.DB.Exec(`INSERT INTO administrators (id, username, password_hash) VALUES ($1, $2, $3)`, adminID, adminUsername, passwordHash)
	if err != nil {
		env.T.Fatalf("failed to insert administrator: %v", err)
	}

	rec := env.do(http.MethodPost, "/api/admin/auth/login", "", map[string]string{
		"username": adminUsername,
		"password": adminPassword,
	})
	if rec.Code < 200 || rec.Code >= 300 {
		env.T.Fatalf("admin login failed with status %d: %s", rec.Code, rec.Body.String())
	}

	type adminLoginResp struct {
		Token string `json:"token"`
	}
	resp := decodeJSON[adminLoginResp](env.T, rec)
	return resp.Token
}

func (env *testEnv) openPostingWindow() {
	token := env.adminToken()

	var maxDate *time.Time
	_ = env.DB.Get(&maxDate, `SELECT MAX(local_date) FROM posting_windows WHERE region = 'asia'`)
	targetDate := time.Now().UTC()
	if maxDate != nil && !targetDate.After(*maxDate) {
		targetDate = maxDate.AddDate(0, 0, 1)
	}
	dateStr := targetDate.Format(time.DateOnly)

	rec := env.do(http.MethodPost, "/api/admin/posting-window", token, map[string]any{
		"region":    "asia",
		"localDate": dateStr,
	})
	if rec.Code < 200 || rec.Code >= 300 {
		env.T.Fatalf("openPostingWindow failed with status %d: %s", rec.Code, rec.Body.String())
	}

	time.Sleep(10 * time.Millisecond)
}

func (env *testEnv) createPost(u testUser) string {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("caption", "minimal post"); err != nil {
		env.T.Fatalf("failed to write caption: %v", err)
	}
	if err := writer.WriteField("visibility", "public"); err != nil {
		env.T.Fatalf("failed to write visibility: %v", err)
	}
	if err := writer.WriteField("location_kind", "none"); err != nil {
		env.T.Fatalf("failed to write location_kind: %v", err)
	}
	part, err := writer.CreateFormFile("image", "image.jpg")
	if err != nil {
		env.T.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write(tinyJPEG()); err != nil {
		env.T.Fatalf("failed to write image data: %v", err)
	}
	if err := writer.Close(); err != nil {
		env.T.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/posts", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)

	if rec.Code < 200 || rec.Code >= 300 {
		env.T.Fatalf("createPost failed with status %d: %s", rec.Code, rec.Body.String())
	}

	var res struct {
		ID string `json:"id"`
	}
	res = decodeJSON[struct {
		ID string `json:"id"`
	}](env.T, rec)
	if res.ID == "" {
		env.T.Fatalf("createPost returned empty post ID: %s", rec.Body.String())
	}
	return res.ID
}

func tinyJPEG() []byte {
	if len(cachedTinyJPEG) > 0 {
		return append([]byte(nil), cachedTinyJPEG...)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func tinyPNG() []byte {
	if len(cachedTinyPNG) > 0 {
		return append([]byte(nil), cachedTinyPNG...)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
