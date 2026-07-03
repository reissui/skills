package telegram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

func photoMessage(chatID, userID int64, groupID string, fileID string) *models.Message {
	return &models.Message{
		ID:           7,
		From:         &models.User{ID: userID},
		Chat:         models.Chat{ID: chatID},
		MediaGroupID: groupID,
		Photo: []models.PhotoSize{
			{FileID: fileID + "_small", Width: 90, Height: 90},
			{FileID: fileID, Width: 1280, Height: 1280}, // largest last
		},
	}
}

// --- AC: generated filenames (no Telegram-supplied paths); size limit; single
// photo spooled and callback invoked ---

func TestSingleImageSpooledGeneratedName(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	// Telegram's advertised path includes a sender-influenced component we must
	// NOT reuse for the local filename.
	f.stageFile("pic", "photos/evil/../secret.jpg", []byte("JPEGBYTES"), 9)

	gotCh := make(chan []string, 1)
	tr.OnImages(func(files []string, _ int) { gotCh <- files })

	tr.processUpdate(context.Background(), &models.Update{
		Message: photoMessage(testChat, testUser, "", "pic"),
	})

	files := waitFiles(t, gotCh)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	base := filepath.Base(files[0])
	if !strings.HasPrefix(base, "img_") || !strings.HasSuffix(base, ".jpg") {
		t.Fatalf("filename %q is not a generated img_*.jpg name", base)
	}
	if strings.Contains(base, "secret") || strings.Contains(base, "..") {
		t.Fatalf("filename %q leaked the Telegram-supplied path", base)
	}
	// File is under the spool dir and holds the downloaded bytes.
	if filepath.Dir(files[0]) != tr.spoolDir {
		t.Fatalf("file %q not in spool dir %q", files[0], tr.spoolDir)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || string(data) != "JPEGBYTES" {
		t.Fatalf("spooled data = %q, err=%v", data, err)
	}
}

func TestImageSizeLimitEnforced(t *testing.T) {
	f := newFakeTelegram(t)
	tr, err := newWithAPI(f.api(t), Config{
		ChatID:         testChat,
		AllowedUserIDs: []int64{testUser},
		SpoolDir:       filepath.Join(t.TempDir(), "spool"),
		MaxImageBytes:  4, // tiny
	})
	if err != nil {
		t.Fatal(err)
	}
	f.stageFile("big", "photos/big.png", []byte("way too many bytes"), 18)

	_, err = tr.spoolFile(context.Background(), "big")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("spoolFile err = %v, want 'too large'", err)
	}
	// Nothing written.
	entries, _ := os.ReadDir(tr.spoolDir)
	if len(entries) != 0 {
		t.Fatalf("oversize image left %d files in spool", len(entries))
	}
}

// --- AC: album of 2 images → both spooled, callback invoked once with both ---

func TestAlbumTwoImagesOneCallback(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	f.stageFile("a", "photos/a.jpg", []byte("AAA"), 3)
	f.stageFile("b", "photos/b.jpg", []byte("BBB"), 3)

	calls := make(chan []string, 4)
	tr.OnImages(func(files []string, _ int) { calls <- files })

	ctx := context.Background()
	tr.processUpdate(ctx, &models.Update{Message: photoMessage(testChat, testUser, "grp1", "a")})
	tr.processUpdate(ctx, &models.Update{Message: photoMessage(testChat, testUser, "grp1", "b")})

	// One aggregated callback for the whole album.
	files := waitFiles(t, calls)
	if len(files) != 2 {
		t.Fatalf("album callback got %d files, want 2", len(files))
	}
	// No second callback.
	select {
	case extra := <-calls:
		t.Fatalf("album fired a second callback: %v", extra)
	case <-time.After(80 * time.Millisecond):
	}
	// Both spooled files exist and are distinct generated names.
	if files[0] == files[1] {
		t.Fatalf("album files not distinct: %v", files)
	}
	for _, p := range files {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("album file %q missing: %v", p, err)
		}
	}
}

func waitFiles(t *testing.T, ch <-chan []string) []string {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnImages callback")
		return nil
	}
}
