package store

import (
	"fmt"
	"time"
)

// QueuedImage is a spooled image awaiting attachment to an issue (spec: SQLite
// runtime only — image queue; Telegram images: generated names, 0700 spool).
// The store records only the path; spooling the file with a generated name and
// safe permissions is the caller's responsibility.
type QueuedImage struct {
	ID         int64
	Path       string
	Issue      int
	ReceivedAt time.Time
	Consumed   bool
}

// EnqueueImage records a spooled image for an issue and returns its id.
// ReceivedAt defaults to now when zero.
func (st *Store) EnqueueImage(img QueuedImage) (int64, error) {
	if img.ReceivedAt.IsZero() {
		img.ReceivedAt = time.Now()
	}
	res, err := st.db.Exec(
		`INSERT INTO image_queue (path, issue, received_at, consumed)
		 VALUES (?, ?, ?, ?)`,
		img.Path, img.Issue, img.ReceivedAt.Unix(), boolToInt(img.Consumed))
	if err != nil {
		return 0, fmt.Errorf("store: enqueue image: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: enqueue image id: %w", err)
	}
	return id, nil
}

// PendingImages returns the not-yet-consumed images for an issue, oldest first.
func (st *Store) PendingImages(issue int) ([]QueuedImage, error) {
	rows, err := st.db.Query(
		`SELECT id, path, issue, received_at, consumed
		 FROM image_queue WHERE issue = ? AND consumed = 0
		 ORDER BY received_at ASC, id ASC`, issue)
	if err != nil {
		return nil, fmt.Errorf("store: pending images for issue %d: %w", issue, err)
	}
	defer rows.Close()

	var out []QueuedImage
	for rows.Next() {
		var (
			img      QueuedImage
			received int64
			consumed int
		)
		if err := rows.Scan(&img.ID, &img.Path, &img.Issue, &received, &consumed); err != nil {
			return nil, fmt.Errorf("store: scan queued image: %w", err)
		}
		img.ReceivedAt = time.Unix(received, 0)
		img.Consumed = consumed != 0
		out = append(out, img)
	}
	return out, rows.Err()
}

// ConsumeImage marks a queued image consumed so it is not attached twice.
func (st *Store) ConsumeImage(id int64) error {
	return st.exec1("consume image",
		`UPDATE image_queue SET consumed = 1 WHERE id = ?`, id)
}
