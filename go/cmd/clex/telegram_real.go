package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// telegramAPIBase is the Telegram Bot API root. The bot token is appended as a
// path segment per the Bot API convention.
const telegramAPIBase = "https://api.telegram.org"

// getMeTimeout bounds a single getMe request. It is short because getMe is a
// trivial round-trip (healthy responses land in tens of milliseconds); a longer
// wait just means the endpoint is wedged. Verify retries once, so the worst-case
// verification wait is ~2×this (issue #40: give getMe its own short per-request
// deadline plus one retry so a transient blip doesn't kill setup).
const getMeTimeout = 10 * time.Second

// Verify calls getMe to confirm the token authenticates and returns the bot's
// @username. It never sends a message. A network or auth failure yields
// telegramResult{Valid:false} with a human Detail — the wizard renders that and
// lets the user re-enter the token.
//
// getMe runs under its own short per-request deadline (getMeTimeout) and is
// retried once on a transport error, so a single transient blip doesn't fail
// first-time setup. The retry is skipped on auth failures (body.OK == false),
// which are deterministic.
func (realTelegram) Verify(ctx context.Context, token string) telegramResult {
	var body struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
		Description string `json:"description"`
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, getMeTimeout)
		err := telegramGet(reqCtx, token, "getMe", nil, &body)
		cancel()
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		// Don't burn the retry if the parent context is already done (cancelled or
		// its own deadline hit) — retrying can't succeed.
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		return telegramResult{Detail: lastErr.Error()}
	}
	if !body.OK {
		return telegramResult{Detail: telegramErr(body.Description)}
	}
	return telegramResult{Valid: true, BotUsername: body.Result.Username}
}

// Bind sends a test message and long-polls getUpdates until the owner replies or
// taps, capturing the chat id (spec: "sends a test message and waits for the user
// to tap it"). It is best-effort and bounded by the context deadline.
func (realTelegram) Bind(ctx context.Context, token string) telegramResult {
	// Send the test message to whoever most recently messaged the bot is not
	// possible before we know the chat; instead we poll for the first inbound
	// update and treat its chat as the owner (the user is told to message the bot
	// now). This keeps the handshake to stdlib HTTP with no prior chat id.
	var upd struct {
		OK     bool `json:"ok"`
		Result []struct {
			Message struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
		Description string `json:"description"`
	}
	deadline := time.Now().Add(90 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for time.Now().Before(deadline) {
		q := url.Values{"timeout": {"20"}, "allowed_updates": {`["message"]`}}
		if err := telegramGet(ctx, token, "getUpdates", q, &upd); err != nil {
			return telegramResult{Detail: err.Error()}
		}
		for _, u := range upd.Result {
			if id := u.Message.Chat.ID; id != 0 {
				return telegramResult{Valid: true, ChatID: id}
			}
		}
		select {
		case <-ctx.Done():
			return telegramResult{Detail: "cancelled while waiting for the tap-to-bind message"}
		case <-time.After(time.Second):
		}
	}
	return telegramResult{Detail: "timed out waiting for you to message the bot"}
}

// telegramGet performs a GET against the Bot API and decodes JSON into out.
func telegramGet(ctx context.Context, token, method string, q url.Values, out any) error {
	u := fmt.Sprintf("%s/bot%s/%s", telegramAPIBase, token, method)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("telegram %s: decode response: %w", method, err)
	}
	return nil
}

// telegramErr renders a Bot API description, defaulting when empty.
func telegramErr(desc string) string {
	if desc == "" {
		return "invalid bot token"
	}
	return desc
}
