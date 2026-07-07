package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"
)

// DefaultAlbumWindow is how long the transport waits for the rest of an album's
// photos to arrive before flushing them as one batch. Telegram delivers album
// members as separate updates in quick succession.
const DefaultAlbumWindow = 750 * time.Millisecond

// handlePhoto spools the photo(s) in a message. A standalone photo is spooled
// and reported immediately; a photo that is part of a media group (album) is
// buffered so OnImages fires once with every member's path. Image handling never
// blocks the caller path in a way that could stall the update loop beyond the
// bounded download, and never interrupts running work (spec: images queue).
func (t *Transport) handlePhoto(ctx context.Context, m *models.Message) {
	if t.spoolDir == "" {
		return // image handling disabled
	}
	best := largestPhoto(m.Photo)
	if best == nil {
		return
	}
	path, err := t.spoolFile(ctx, best.FileID)
	if err != nil {
		// A bad/oversize image is dropped with a one-line note, not fatal.
		_, _ = t.SendLine(ctx, "image skipped: "+err.Error())
		return
	}
	if m.MediaGroupID != "" {
		t.albums.add(m.MediaGroupID, path, replyToID(m))
		return
	}
	t.fireImages([]string{path}, replyToID(m))
}

// flushAlbum is the albumBuffer callback: it fires OnImages once for a completed
// media group.
func (t *Transport) flushAlbum(files []string, replyToMsgID int) {
	t.fireImages(files, replyToMsgID)
}

// fireImages invokes the registered OnImages callback, if any, off the caller's
// goroutine so a slow consumer can never stall update processing.
func (t *Transport) fireImages(files []string, replyToMsgID int) {
	t.mu.Lock()
	fn := t.onImages
	t.mu.Unlock()
	if fn == nil {
		return
	}
	go fn(files, replyToMsgID)
}

// spoolFile downloads the file identified by fileID into the spool dir under a
// freshly GENERATED name (never a Telegram-supplied path — that would let a
// sender influence local filesystem paths) and enforces the size limit. It
// returns the absolute path written.
func (t *Transport) spoolFile(ctx context.Context, fileID string) (string, error) {
	if err := os.MkdirAll(t.spoolDir, 0o700); err != nil {
		return "", fmt.Errorf("spool dir: %w", err)
	}
	f, err := t.api.GetFile(ctx, fileID)
	if err != nil {
		return "", fmt.Errorf("get file: %w", err)
	}
	// Pre-check the advertised size when Telegram provides it, so we can reject
	// oversize files before downloading their bytes.
	if f.FileSize > 0 && f.FileSize > t.maxImgBytes {
		return "", fmt.Errorf("image too large: %d > %d bytes", f.FileSize, t.maxImgBytes)
	}
	data, err := t.api.Download(ctx, f)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	// Enforce the limit again on the actual bytes: FileSize may be absent or
	// under-reported.
	if int64(len(data)) > t.maxImgBytes {
		return "", fmt.Errorf("image too large: %d > %d bytes", len(data), t.maxImgBytes)
	}
	name := generatedImageName(f.FilePath)
	dst := filepath.Join(t.spoolDir, name)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", fmt.Errorf("write image: %w", err)
	}
	return dst, nil
}

// generatedImageName produces a random, collision-resistant filename. It borrows
// only the extension from the Telegram path (never the path itself) so spooled
// files keep a useful suffix without trusting sender-controlled path components.
func generatedImageName(telegramPath string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ext := filepath.Ext(filepath.Base(telegramPath))
	if !safeExt(ext) {
		ext = ".bin"
	}
	return "img_" + hex.EncodeToString(b[:]) + ext
}

// safeExt reports whether ext is a short, purely alphanumeric extension safe to
// append to a generated name. Anything else (path separators, dots, oversize)
// is rejected in favor of ".bin".
func safeExt(ext string) bool {
	if len(ext) < 2 || len(ext) > 6 || ext[0] != '.' {
		return false
	}
	for _, r := range ext[1:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// largestPhoto returns the highest-resolution PhotoSize in a photo set (Telegram
// orders them ascending, so the last is largest), or nil if empty.
func largestPhoto(sizes []models.PhotoSize) *models.PhotoSize {
	if len(sizes) == 0 {
		return nil
	}
	return &sizes[len(sizes)-1]
}

// albumBuffer aggregates the photos of a media group (album), which Telegram
// delivers as separate updates, and flushes them as one batch after a quiet
// window so OnImages fires exactly once per album.
type albumBuffer struct {
	window time.Duration
	flush  func(files []string, replyToMsgID int)

	mu      sync.Mutex
	groups  map[string]*albumGroup
	newTime func(time.Duration, func()) *time.Timer // injectable for tests
}

type albumGroup struct {
	files        []string
	replyToMsgID int
	timer        *time.Timer
}

func newAlbumBuffer(window time.Duration, flush func(files []string, replyToMsgID int)) *albumBuffer {
	return &albumBuffer{
		window:  window,
		flush:   flush,
		groups:  make(map[string]*albumGroup),
		newTime: time.AfterFunc,
	}
}

// add records one photo of an album and (re)arms the flush timer. When the
// window elapses without a new member, the whole group is handed to flush once.
func (ab *albumBuffer) add(groupID, file string, replyToMsgID int) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	g, ok := ab.groups[groupID]
	if !ok {
		g = &albumGroup{replyToMsgID: replyToMsgID}
		ab.groups[groupID] = g
	}
	g.files = append(g.files, file)
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = ab.newTime(ab.window, func() { ab.fire(groupID) })
}

// fire flushes and forgets a completed album group.
func (ab *albumBuffer) fire(groupID string) {
	ab.mu.Lock()
	g, ok := ab.groups[groupID]
	if !ok {
		ab.mu.Unlock()
		return
	}
	delete(ab.groups, groupID)
	files := g.files
	reply := g.replyToMsgID
	ab.mu.Unlock()
	ab.flush(files, reply)
}
