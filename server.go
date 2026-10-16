package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type approval struct {
	Method      string          `json:"method"`
	Host        string          `json:"host"`
	Params      json.RawMessage `json:"params"`
	Item        json.RawMessage `json:"item,omitempty"`
	VetoSeconds int             `json:"vetoSeconds,omitempty"`
}
type decision struct {
	Decision string `json:"decision"`
	Source   string `json:"source,omitempty"`
}
type pending struct {
	ch      chan string
	message int64
	expires time.Time
}
type backend struct {
	tg         *telegram
	token      string
	chat, user int64
	timeout    time.Duration
	mu         sync.Mutex
	pending    map[string]*pending
}

func validMethod(method string) bool {
	return method == "item/commandExecution/requestApproval" || method == "item/fileChange/requestApproval" || method == "item/permissions/requestApproval"
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func serve(ctx context.Context) error {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	secret := os.Getenv("BRIDGE_TOKEN")
	chat, e1 := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
	user, e2 := strconv.ParseInt(os.Getenv("TELEGRAM_USER_ID"), 10, 64)
	timeout, err := time.ParseDuration(env("APPROVAL_TIMEOUT", "5m"))
	if token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is missing or empty")
	}
	if len(secret) < 32 {
		return errors.New("BRIDGE_TOKEN must contain at least 32 characters")
	}
	if e1 != nil || chat == 0 {
		return errors.New("TELEGRAM_CHAT_ID must be a nonzero numeric chat ID, not a username")
	}
	if e2 != nil || user <= 0 {
		return errors.New("TELEGRAM_USER_ID must be your positive numeric Telegram user ID, not a username or bot name; use the pair command to discover it")
	}
	if err != nil || timeout < time.Second || timeout > 30*time.Minute {
		return errors.New("APPROVAL_TIMEOUT must be between 1s and 30m")
	}
	b := &backend{tg: newTelegram(token), token: secret, chat: chat, user: user, timeout: timeout, pending: make(map[string]*pending)}
	if err := b.tg.call(ctx, "getMe", map[string]any{}, nil); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /v1/approvals", b.handleApproval)
	srv := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8787"), Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	go b.poll(ctx)
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	log.Printf("approval backend listening on %s", srv.Addr)
	err = srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (b *backend) handleApproval(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var a approval
	d := json.NewDecoder(r.Body)
	if err := d.Decode(&a); err != nil {
		http.Error(w, "invalid approval", 400)
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		http.Error(w, "expected one JSON object", 400)
		return
	}
	if !validMethod(a.Method) || len(a.Params) == 0 {
		http.Error(w, "unsupported approval method", 400)
		return
	}
	if a.VetoSeconds < 0 || a.VetoSeconds > 60 {
		http.Error(w, "invalid veto window", 400)
		return
	}
	// Reject invalid/oversized context rather than offering approval for a truncated preview.
	details, err := json.MarshalIndent(a, "", "  ")
	if err != nil || len(details) > 30000 {
		http.Error(w, "approval context too large to display safely", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), b.timeout)
	defer cancel()
	id := randomID()
	p := &pending{ch: make(chan string, 1), expires: time.Now().Add(b.timeout)}
	b.mu.Lock()
	if len(b.pending) >= 32 {
		b.mu.Unlock()
		http.Error(w, "too many pending approvals", 429)
		return
	}
	b.pending[id] = p
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, id); b.mu.Unlock() }()
	// Send all context first; buttons exist only once every chunk is delivered.
	for _, text := range approvalDetails(details, id) {
		if err := b.tg.call(ctx, "sendMessage", map[string]any{"chat_id": b.chat, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}, nil); err != nil {
			http.Error(w, "Telegram delivery failed; denied", 502)
			return
		}
	}
	label := "✓ Approve once"
	if a.Method == "item/permissions/requestApproval" {
		label = "✓ Approve for turn"
	}
	keyboard := map[string]any{"inline_keyboard": [][]map[string]string{{{"text": label, "callback_data": id + ":accept"}, {"text": "✕ Deny", "callback_data": id + ":decline"}}}}
	var message tgMessage
	// Lock through send so a fast callback cannot precede message binding.
	b.mu.Lock()
	card := approvalCard(a, id, p.expires)
	err = b.tg.call(ctx, "sendMessage", map[string]any{"chat_id": b.chat, "text": card, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}, "reply_markup": keyboard}, &message)
	p.message = message.ID
	b.mu.Unlock()
	if err != nil {
		http.Error(w, "Telegram delivery failed; denied", 502)
		return
	}
	choice := "decline"
	source := "telegram"
	status := "✕ Denied"
	select {
	case choice = <-p.ch:
		if choice == "accept" {
			status = "✓ Approved"
		}
		if a.VetoSeconds > 0 {
			status = "Phone vote: denied · terminal can override"
			if choice == "accept" {
				status = "Phone vote: approved · awaiting terminal veto window"
			}
		}
	case <-ctx.Done():
		source = "timeout"
		status = "Expired · no approval granted"
		if r.Context().Err() != nil {
			source = "cancelled"
			status = "Cancelled · client disconnected"
		}
	}
	// Remove the request before doing network cleanup to prevent late approvals.
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
	cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = b.tg.call(cleanup, "editMessageText", map[string]any{"chat_id": b.chat, "message_id": message.ID, "text": "<b>" + status + "</b>\n\n" + strings.Replace(card, "Approval needed ·", "Request ·", 1), "parse_mode": "HTML", "reply_markup": map[string]any{"inline_keyboard": []any{}}}, nil)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(decision{Decision: choice, Source: source})
}
func (b *backend) resolve(c *tgCallback) string {
	if c.Message == nil || c.From.ID != b.user || c.Message.Chat.ID != b.chat {
		return "Not authorized."
	}
	id, choice, ok := strings.Cut(c.Data, ":")
	if !ok || (choice != "accept" && choice != "decline") {
		return "Invalid decision."
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pending[id]
	if p == nil || p.message != c.Message.ID || time.Now().After(p.expires) {
		return "Request expired or already resolved."
	}
	select {
	case p.ch <- choice:
		delete(b.pending, id)
		return "Decision received."
	default:
		return "Already resolved."
	}
}
func (b *backend) poll(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		updates, err := b.tg.updates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Print("Telegram polling failed; retrying (check token and ensure only one bot poller is running)")
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.ID + 1
			if u.Callback != nil {
				text := b.resolve(u.Callback)
				_ = b.tg.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": u.Callback.ID, "text": text}, nil)
			}
		}
	}
}
