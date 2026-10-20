package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type TelegramUser struct {
	ID int64 `json:"id"`
}
type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	From      TelegramUser `json:"from"`
	Chat      TelegramChat `json:"chat"`
	Text      string       `json:"text"`
}
type TelegramCallback struct {
	ID      string           `json:"id"`
	From    TelegramUser     `json:"from"`
	Message *TelegramMessage `json:"message"`
	Data    string           `json:"data"`
}
type TelegramUpdate struct {
	UpdateID int64             `json:"update_id"`
	Message  *TelegramMessage  `json:"message"`
	Callback *TelegramCallback `json:"callback_query"`
}
type TelegramTransport struct {
	token  string
	base   string
	client *http.Client
}

func NewTelegramTransport(token string) *TelegramTransport {
	return &TelegramTransport{token: token, base: "https://api.telegram.org", client: &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (t *TelegramTransport) call(ctx context.Context, method string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	base, err := url.Parse(t.base)
	if err != nil {
		return errors.New("invalid Telegram API base")
	}
	base.Path = "/bot" + t.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(raw))
	if err != nil {
		return errors.New("cannot create Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return errors.New("Telegram request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errors.New("Telegram response failed")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || !envelope.OK {
		return errors.New("invalid Telegram response")
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}
func (t *TelegramTransport) GetUpdates(ctx context.Context, offset int64) ([]TelegramUpdate, error) {
	var updates []TelegramUpdate
	err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &updates)
	return updates, err
}
func (t *TelegramTransport) SendMessage(ctx context.Context, chatID int64, text string, rows [][]TelegramButton) error {
	body := map[string]any{"chat_id": chatID, "text": text}
	if len(rows) > 0 {
		body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	return t.call(ctx, "sendMessage", body, nil)
}
func (t *TelegramTransport) AnswerCallback(ctx context.Context, id, text string) error {
	return t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

type TelegramButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

func ParseAllowedUsers(raw string) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid Telegram user ID")
		}
		out[id] = true
	}
	if len(out) == 0 {
		return nil, errors.New("TELEGRAM_ALLOWED_USER_IDS is empty")
	}
	return out, nil
}
