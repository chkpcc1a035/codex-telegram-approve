package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// API errors intentionally omit the request URL: Telegram puts the bot token in it.
type telegram struct {
	base   string
	client *http.Client
}

func newTelegram(token string) *telegram {
	return &telegram{base: "https://api.telegram.org/bot" + token + "/", client: &http.Client{Timeout: 40 * time.Second}}
}
func (t *telegram) call(ctx context.Context, method string, params any, result any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", t.base+method, bytes.NewReader(b))
	if err != nil {
		return errors.New("invalid Telegram endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := t.client.Do(req)
	if err != nil {
		return errors.New("Telegram transport failed")
	}
	defer res.Body.Close()
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&envelope); err != nil {
		return errors.New("invalid Telegram response")
	}
	if res.StatusCode != 200 || !envelope.OK {
		return fmt.Errorf("Telegram %s failed (HTTP %d); check bot configuration", method, res.StatusCode)
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

type tgMessage struct {
	ID   int64 `json:"message_id"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
}
type tgCallback struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message *tgMessage `json:"message"`
}
type tgUpdate struct {
	ID       int64       `json:"update_id"`
	Message  *tgMessage  `json:"message"`
	Callback *tgCallback `json:"callback_query"`
}

func (t *telegram) updates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	var updates []tgUpdate
	err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &updates)
	return updates, err
}
func pair(ctx context.Context) error {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	t := newTelegram(token)
	fmt.Println("Send /start to your bot (or send a message in the target group). Only IDs are printed. Ctrl-C when done.")
	var offset int64
	for ctx.Err() == nil {
		updates, err := t.updates(ctx, offset)
		if err != nil {
			return err
		}
		for _, u := range updates {
			offset = u.ID + 1
			if u.Message != nil {
				fmt.Printf("TELEGRAM_USER_ID=%d TELEGRAM_CHAT_ID=%d\n", u.Message.From.ID, u.Message.Chat.ID)
			}
		}
	}
	return nil
}
