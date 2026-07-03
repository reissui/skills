package store

import (
	"fmt"
)

// TelegramLink maps a Telegram message id to the issue or epic it represents,
// so clex can edit that message in place and target replies (spec: SQLite
// runtime only — Telegram message ↔ issue mapping).
type TelegramLink struct {
	MsgID  int64
	Issue  int
	IsEpic bool
}

// PutTelegramMap upserts the mapping for a Telegram message id. Re-linking an
// existing message id overwrites its target (the bot edits one message per
// issue over the pipeline's life).
func (st *Store) PutTelegramMap(l TelegramLink) error {
	_, err := st.db.Exec(
		`INSERT INTO telegram_map (msg_id, issue, is_epic) VALUES (?, ?, ?)
		 ON CONFLICT(msg_id) DO UPDATE SET issue = excluded.issue, is_epic = excluded.is_epic`,
		l.MsgID, l.Issue, boolToInt(l.IsEpic))
	if err != nil {
		return fmt.Errorf("store: put telegram map: %w", err)
	}
	return nil
}

// TelegramByMsg returns the mapping for a Telegram message id, or ErrNotFound.
func (st *Store) TelegramByMsg(msgID int64) (TelegramLink, error) {
	var (
		l      TelegramLink
		isEpic int
	)
	err := st.db.QueryRow(
		`SELECT msg_id, issue, is_epic FROM telegram_map WHERE msg_id = ?`, msgID).
		Scan(&l.MsgID, &l.Issue, &isEpic)
	if err != nil {
		return TelegramLink{}, wrapLookup("telegram by msg", err)
	}
	l.IsEpic = isEpic != 0
	return l, nil
}

// TelegramMsgForIssue returns the most recently mapped Telegram message id for
// an issue, or ErrNotFound. Used to find which message to edit in place.
func (st *Store) TelegramMsgForIssue(issue int) (int64, error) {
	var msgID int64
	err := st.db.QueryRow(
		`SELECT msg_id FROM telegram_map WHERE issue = ? ORDER BY msg_id DESC LIMIT 1`, issue).
		Scan(&msgID)
	if err != nil {
		return 0, wrapLookup("telegram msg for issue", err)
	}
	return msgID, nil
}

// DeleteTelegramMap removes the mapping for a Telegram message id.
func (st *Store) DeleteTelegramMap(msgID int64) error {
	return st.exec1("delete telegram map",
		`DELETE FROM telegram_map WHERE msg_id = ?`, msgID)
}
