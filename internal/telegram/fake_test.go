package telegram

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// fakeTelegram is an httptest-backed stand-in for the Telegram Bot API. It speaks
// just enough of the protocol for the transport's calls: sendMessage,
// editMessageText, getFile, answerCallbackQuery, and file downloads. Every call
// is recorded so tests can assert round-trips. No live Telegram is ever touched.
type fakeTelegram struct {
	srv   *httptest.Server
	token string

	mu       sync.Mutex
	nextMsg  int
	sent     []sentMessage // sendMessage calls, in order
	edits    []editCall    // editMessageText calls, in order
	answers  []string      // answered callback query ids
	files    map[string]fakeFile
	fileData map[string][]byte // file_path -> bytes served on download
}

type sentMessage struct {
	ChatID int64
	Text   string
	Markup *models.InlineKeyboardMarkup
}

type editCall struct {
	ChatID int64
	MsgID  int
	Text   string
}

type fakeFile struct {
	Path string
	Size int64
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{
		token:    "test:token",
		nextMsg:  1000,
		files:    make(map[string]fakeFile),
		fileData: make(map[string][]byte),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// api builds a botAPI wired to the fake server (real HTTP, skip getMe).
func (f *fakeTelegram) api(t *testing.T) api {
	t.Helper()
	b, err := bot.New(f.token,
		bot.WithServerURL(f.srv.URL),
		bot.WithSkipGetMe(),
	)
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	return &botAPI{b: b}
}

// stageFile registers a downloadable file so GetFile+Download succeed.
func (f *fakeTelegram) stageFile(fileID, path string, data []byte, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[fileID] = fakeFile{Path: path, Size: size}
	f.fileData[path] = data
}

func (f *fakeTelegram) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func (f *fakeTelegram) editCalls() []editCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]editCall(nil), f.edits...)
}

func (f *fakeTelegram) handle(w http.ResponseWriter, r *http.Request) {
	// File download path: /file/bot<token>/<path>
	if strings.HasPrefix(r.URL.Path, "/file/bot") {
		f.handleDownload(w, r)
		return
	}
	// API method path: /bot<token>/<method>
	method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
	params := parseMultipart(r)
	switch method {
	case "sendMessage":
		f.doSendMessage(w, params)
	case "editMessageText":
		f.doEditMessageText(w, params)
	case "getFile":
		f.doGetFile(w, params)
	case "answerCallbackQuery":
		f.doAnswerCallback(w, params)
	default:
		writeResult(w, json.RawMessage(`true`))
	}
}

func (f *fakeTelegram) doSendMessage(w http.ResponseWriter, p map[string]string) {
	f.mu.Lock()
	f.nextMsg++
	id := f.nextMsg
	chatID, _ := strconv.ParseInt(p["chat_id"], 10, 64)
	sm := sentMessage{ChatID: chatID, Text: p["text"]}
	if raw := p["reply_markup"]; raw != "" {
		var mk models.InlineKeyboardMarkup
		if json.Unmarshal([]byte(raw), &mk) == nil {
			sm.Markup = &mk
		}
	}
	f.sent = append(f.sent, sm)
	f.mu.Unlock()

	msg := models.Message{ID: id, Text: p["text"], Chat: models.Chat{ID: chatID}}
	b, _ := json.Marshal(msg)
	writeResult(w, b)
}

func (f *fakeTelegram) doEditMessageText(w http.ResponseWriter, p map[string]string) {
	f.mu.Lock()
	chatID, _ := strconv.ParseInt(p["chat_id"], 10, 64)
	msgID, _ := strconv.Atoi(p["message_id"])
	f.edits = append(f.edits, editCall{ChatID: chatID, MsgID: msgID, Text: p["text"]})
	f.mu.Unlock()

	msg := models.Message{ID: msgID, Text: p["text"], Chat: models.Chat{ID: chatID}}
	b, _ := json.Marshal(msg)
	writeResult(w, b)
}

func (f *fakeTelegram) doGetFile(w http.ResponseWriter, p map[string]string) {
	f.mu.Lock()
	ff, ok := f.files[p["file_id"]]
	f.mu.Unlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "file not found")
		return
	}
	file := models.File{FileID: p["file_id"], FilePath: ff.Path, FileSize: ff.Size}
	b, _ := json.Marshal(file)
	writeResult(w, b)
}

func (f *fakeTelegram) doAnswerCallback(w http.ResponseWriter, p map[string]string) {
	f.mu.Lock()
	f.answers = append(f.answers, p["callback_query_id"])
	f.mu.Unlock()
	writeResult(w, json.RawMessage(`true`))
}

func (f *fakeTelegram) handleDownload(w http.ResponseWriter, r *http.Request) {
	// Path after "/file/bot<token>/" is the file_path.
	prefix := "/file/bot" + f.token + "/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	f.mu.Lock()
	data, ok := f.fileData[path]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}

// --- helpers ---

func parseMultipart(r *http.Request) map[string]string {
	out := make(map[string]string)
	ct := r.Header.Get("Content-Type")
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		return out
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(part)
		out[part.FormName()] = string(b)
	}
	return out
}

func writeResult(w http.ResponseWriter, result json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}{OK: true, Result: result})
}

func writeError(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}{OK: false, ErrorCode: code, Description: desc})
}
