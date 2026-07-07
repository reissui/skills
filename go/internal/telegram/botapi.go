package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// botAPI is the production api implementation. It wraps *bot.Bot and translates
// the transport's small interface into the library's parameter structs. It is
// the only file that depends on the concrete bot type for I/O; everything else
// works against the api interface, which is what makes the transport testable
// against an httptest fake.
type botAPI struct {
	b *bot.Bot
}

func (a *botAPI) SendMessage(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) (int, error) {
	m, err := a.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})
	if err != nil {
		return 0, err
	}
	return m.ID, nil
}

func (a *botAPI) EditMessageText(ctx context.Context, chatID int64, msgID int, text string, markup models.ReplyMarkup) error {
	_, err := a.b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   msgID,
		Text:        text,
		ReplyMarkup: markup,
	})
	return err
}

func (a *botAPI) AnswerCallbackQuery(ctx context.Context, callbackID string) error {
	_, err := a.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
	})
	return err
}

func (a *botAPI) GetFile(ctx context.Context, fileID string) (*models.File, error) {
	return a.b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
}

func (a *botAPI) Download(ctx context.Context, f *models.File) ([]byte, error) {
	link := a.b.FileDownloadLink(f)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download file: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
