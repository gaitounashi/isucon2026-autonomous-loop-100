package isureal

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
)

type exportPostResponse struct {
	createPostResponse
	ImagePath string `json:"imagePath"`
}

func exportPostsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		session := GetSession(c)
		userID := session.UserID
		author := session.User
		var posts []struct {
			Post
			ImageCreatedAt time.Time `db:"image_created_at"`
		}
		if err := h.DB.SelectContext(ctx, &posts, `
			SELECT `+postColumns+`, m.created_at AS image_created_at
			FROM posts p
			JOIN posting_windows w ON w.id = p.posting_window_id
			JOIN image_meta m ON m.id = p.image_id
			WHERE p.author_id = $1 AND p.deleted_at IS NULL
			ORDER BY p.posted_at, p.id
		`, userID); err != nil {
			return err
		}

		imageDataByID := make(map[string][]byte, len(posts))
		type missingImage struct {
			id        string
			createdAt time.Time
		}
		// Group missing images by month to allow PostgreSQL partition pruning on image_objects
		missingByMonth := make(map[string][]missingImage)
		seenMissing := make(map[string]struct{})
		for _, post := range posts {
			if data, ok := h.Images.Get(post.ImageID, post.ImageCreatedAt); ok {
				imageDataByID[post.ImageID] = data
				continue
			}
			if _, exists := seenMissing[post.ImageID]; !exists {
				seenMissing[post.ImageID] = struct{}{}
				monthKey := post.ImageCreatedAt.UTC().Format("2006-01")
				missingByMonth[monthKey] = append(missingByMonth[monthKey], missingImage{
					id:        post.ImageID,
					createdAt: post.ImageCreatedAt,
				})
			}
		}

		for monthKey, list := range missingByMonth {
			monthStart, err := time.Parse("2006-01", monthKey)
			if err != nil {
				monthStart = list[0].createdAt.Add(-30 * 24 * time.Hour)
			}
			monthEnd := monthStart.AddDate(0, 1, 0)

			const batchSize = 100
			for start := 0; start < len(list); start += batchSize {
				end := min(start+batchSize, len(list))
				ids := make([]string, end-start)
				for i := start; i < end; i++ {
					ids[i-start] = list[i].id
				}
				var loaded []struct {
					ID        string    `db:"id"`
					CreatedAt time.Time `db:"created_at"`
					Data      []byte    `db:"data"`
				}
				// Directly query image_objects without joining image_meta, scoped to single partition;
				// images written during a run live in active_image_objects (026)
				if err := h.DB.SelectContext(ctx, &loaded, `
					SELECT id, created_at, data
					FROM active_image_objects
					WHERE id = ANY(string_to_array($1, ','))
					  AND created_at >= $2 AND created_at < $3
					UNION ALL
					SELECT id, created_at, data
					FROM image_objects
					WHERE id = ANY(string_to_array($1, ','))
					  AND created_at >= $2 AND created_at < $3
				`, strings.Join(ids, ","), monthStart, monthEnd); err != nil {
					return err
				}
				for _, img := range loaded {
					imageDataByID[img.ID] = img.Data
					h.Images.Put(img.ID, img.CreatedAt, img.Data)
				}
			}
		}

		estimatedSize := 4096
		for _, post := range posts {
			if data, ok := imageDataByID[post.ImageID]; ok {
				estimatedSize += len(data) + 512
			}
		}

		var body bytes.Buffer
		body.Grow(estimatedSize)
		writer := zip.NewWriter(&body)
		defer writer.Close()
		items := make([]exportPostResponse, 0, len(posts))
		for _, post := range posts {
			data, ok := imageDataByID[post.ImageID]
			if !ok {
				continue
			}
			imagePath := fmt.Sprintf("images/%06d.jpg", len(items)+1)
			image, err := writer.CreateHeader(&zip.FileHeader{Name: imagePath, Method: zip.Store})
			if err != nil {
				return err
			}
			if _, err := image.Write(data); err != nil {
				return err
			}
			items = append(items, exportPostResponse{createPostResponse: newPostResponse(post.Post, author), ImagePath: imagePath})
		}
		manifest, err := writer.Create("posts.json")
		if err != nil {
			return err
		}
		if err := json.NewEncoder(manifest).Encode(items); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="isureal-posts.zip"`)
		c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
		return c.Blob(http.StatusOK, "application/zip", body.Bytes())
	}
}
