package isureal

import "github.com/google/uuid"

// newUUID returns a time-ordered UUIDv7 string (RFC 9562).
// Sequential time-ordered keys ensure that INSERTs into B-tree primary key indexes
// are appended to the rightmost leaf pages, eliminating page splits, buffer churn,
// and random disk I/O under high concurrency.
func newUUID() string {
	u, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return u.String()
}
