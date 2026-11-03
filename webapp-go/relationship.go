package isureal

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type FriendRequest struct {
	ID          string    `db:"id"`
	RequesterID string    `db:"requester_id"`
	RecipientID string    `db:"recipient_id"`
	Status      string    `db:"status"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

func getFriendRequest(ctx context.Context, db sqlx.QueryerContext, id string) (FriendRequest, error) {
	var request FriendRequest
	err := sqlx.GetContext(ctx, db, &request, `SELECT id, requester_id, recipient_id, status, created_at, updated_at FROM friend_requests WHERE id=$1`, id)
	return request, err
}

func beginRelationshipTx(ctx context.Context, db *sqlx.DB, actor, target string) (*sqlx.Tx, error) {
	tx, _, _, err := beginRelationshipTxWithUsers(ctx, db, actor, target)
	return tx, err
}

func beginRelationshipTxWithUsers(ctx context.Context, db *sqlx.DB, actor, target string) (*sqlx.Tx, User, User, error) {
	if actor == target || target == "" {
		return nil, User{}, User{}, NewAPIError(http.StatusBadRequest, "invalid_relationship")
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, User{}, User{}, err
	}
	var users []User
	if err := tx.SelectContext(ctx, &users, `
		SELECT `+strings.Join(userColumns, ", ")+`
		FROM users
		WHERE id IN ($1, $2) AND status = 'active'
		ORDER BY id
		FOR NO KEY UPDATE
	`, min(actor, target), max(actor, target)); err != nil {
		tx.Rollback()
		return nil, User{}, User{}, err
	}
	if len(users) != 2 {
		tx.Rollback()
		return nil, User{}, User{}, NewAPIError(http.StatusNotFound, "relationship_not_found")
	}
	for i := range users {
		if err := users[i].setImageKey(); err != nil {
			tx.Rollback()
			return nil, User{}, User{}, err
		}
	}
	if users[0].ID == actor {
		return tx, users[0], users[1], nil
	}
	return tx, users[1], users[0], nil
}

func isBlocked(ctx context.Context, db sqlx.QueryerContext, actor, target string) (bool, error) {
	var blocked bool
	err := sqlx.GetContext(ctx, db, &blocked, `SELECT EXISTS(SELECT 1 FROM block_relations WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1))`, actor, target)
	return blocked, err
}

func insertFriendship(ctx context.Context, tx *sqlx.Tx, request FriendRequest) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO friendships (id, user_low_id, user_high_id, source_request_id) VALUES ($1,$2,$3,$4)`, newUUID(), min(request.RequesterID, request.RecipientID), max(request.RequesterID, request.RecipientID), request.ID)
	return err
}

func requireRelationshipChange(result sql.Result, err error, status int, code string) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return NewAPIError(status, code)
	}
	return nil
}
