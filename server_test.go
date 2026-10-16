package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "01234567890123456789012345678901"

func fakeBackend(t *testing.T, choose string) (*backend, *httptest.Server) {
	t.Helper()
	b := &backend{token: testToken, chat: -100123, user: 42, timeout: time.Second, pending: make(map[string]*pending)}
	var mu sync.Mutex
	var next int64
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		next++
		message := next
		mu.Unlock()
		if r.URL.Path == "/sendMessage" && body["reply_markup"] != nil && choose != "" {
			var keyboard struct {
				Rows [][]struct {
					Data string `json:"callback_data"`
				} `json:"inline_keyboard"`
			}
			json.Unmarshal(body["reply_markup"], &keyboard)
			data := keyboard.Rows[0][0].Data
			if choose == "decline" {
				data = strings.TrimSuffix(data, "accept") + "decline"
			}
			go func() {
				c := &tgCallback{Data: data, Message: &tgMessage{ID: message}}
				c.From.ID = b.user
				c.Message.Chat.ID = b.chat
				b.resolve(c)
			}()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": message}})
	}))
	t.Cleanup(tg.Close)
	b.tg = &telegram{base: tg.URL + "/", client: tg.Client()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/approvals", b.handleApproval)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return b, srv
}
func sampleApproval() approval {
	return approval{Method: "item/commandExecution/requestApproval", Host: "test-host", Params: json.RawMessage(`{"command":"npm install","threadId":"t","turnId":"u","itemId":"i"}`)}
}
func TestApprovalRoundTrip(t *testing.T) {
	for _, choice := range []string{"accept", "decline"} {
		t.Run(choice, func(t *testing.T) {
			b, srv := fakeBackend(t, choice)
			got, err := requestDecision(context.Background(), srv.URL, testToken, sampleApproval())
			if err != nil || got != choice {
				t.Fatalf("got %s, %v", got, err)
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.pending) != 0 {
				t.Fatal("pending request leaked")
			}
		})
	}
}

func TestDecisionSourceDistinguishesPhoneVoteFromTimeout(t *testing.T) {
	for _, choice := range []string{"accept", "decline", ""} {
		t.Run(choice, func(t *testing.T) {
			b, srv := fakeBackend(t, choice)
			b.timeout = 20 * time.Millisecond
			body, _ := json.Marshal(sampleApproval())
			req, _ := http.NewRequest("POST", srv.URL+"/v1/approvals", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			response, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var result decision
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			want := "telegram"
			if choice == "" {
				want = "timeout"
			}
			if result.Source != want {
				t.Fatalf("got source %q; want %q", result.Source, want)
			}
		})
	}
}
func TestCallbackAuthorizationAndReplay(t *testing.T) {
	b := &backend{chat: -1, user: 42, pending: make(map[string]*pending)}
	p := &pending{ch: make(chan string, 1), message: 10, expires: time.Now().Add(time.Minute)}
	b.pending["abc"] = p
	c := &tgCallback{Data: "abc:accept", Message: &tgMessage{ID: 10}}
	c.From.ID = 43
	c.Message.Chat.ID = -1
	if b.resolve(c) != "Not authorized." {
		t.Fatal("wrong user accepted")
	}
	c.From.ID = 42
	c.Message.Chat.ID = -2
	if b.resolve(c) != "Not authorized." {
		t.Fatal("wrong chat accepted")
	}
	c.Message.Chat.ID = -1
	c.Message.ID = 11
	if b.resolve(c) != "Request expired or already resolved." {
		t.Fatal("wrong message accepted")
	}
	c.Message.ID = 10
	if b.resolve(c) != "Decision received." {
		t.Fatal("valid callback rejected")
	}
	if b.resolve(c) != "Request expired or already resolved." {
		t.Fatal("replay accepted")
	}
	if <-p.ch != "accept" {
		t.Fatal("wrong decision")
	}
	b.pending["abc"] = &pending{ch: make(chan string, 1), message: 10, expires: time.Now().Add(-time.Second)}
	if b.resolve(c) != "Request expired or already resolved." {
		t.Fatal("expired callback accepted")
	}
}
func TestTimeoutAndDisconnect(t *testing.T) {
	b, srv := fakeBackend(t, "")
	b.timeout = 30 * time.Millisecond
	got, err := requestDecision(context.Background(), srv.URL, testToken, sampleApproval())
	if err != nil || got != "decline" {
		t.Fatalf("timeout: %s %v", got, err)
	}
	b.timeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := requestDecision(ctx, srv.URL, testToken, sampleApproval()); err == nil {
		t.Fatal("disconnect should fail")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(b.pending)
		b.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("disconnected request leaked")
}
func TestRejectUnauthorizedAndOversize(t *testing.T) {
	_, srv := fakeBackend(t, "accept")
	if choice, err := requestDecision(context.Background(), srv.URL, "wrong", sampleApproval()); err == nil || choice != "decline" {
		t.Fatal("bad token accepted")
	}
	a := sampleApproval()
	p, _ := json.Marshal(map[string]string{"command": strings.Repeat("x", 31000)})
	a.Params = p
	if choice, err := requestDecision(context.Background(), srv.URL, testToken, a); err == nil || choice != "decline" {
		t.Fatal("truncated context accepted")
	}
	a = sampleApproval()
	a.Method = "unknown"
	if _, err := requestDecision(context.Background(), srv.URL, testToken, a); err == nil {
		t.Fatal("unknown method accepted")
	}
}
func TestUnavailableBridgeFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"decision":"acceptForSession"}`) }))
	defer srv.Close()
	got, err := requestDecision(context.Background(), srv.URL, testToken, sampleApproval())
	if err == nil || got != "decline" {
		t.Fatal("unexpected approval accepted")
	}
}

func TestTelegramDeliveryFailureDoesNotOfferButtons(t *testing.T) {
	b, _ := fakeBackend(t, "")
	buttons := false
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		if body["reply_markup"] != nil {
			buttons = true
		}
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"ok":false}`)
	}))
	defer tg.Close()
	b.tg = &telegram{base: tg.URL + "/", client: tg.Client()}
	srv := httptest.NewServer(http.HandlerFunc(b.handleApproval))
	defer srv.Close()
	got, err := requestDecision(context.Background(), srv.URL, testToken, sampleApproval())
	if err == nil || got != "decline" {
		t.Fatal("Telegram failure approved a request")
	}
	if buttons {
		t.Fatal("buttons sent after context delivery failed")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) != 0 {
		t.Fatal("failed delivery leaked a request")
	}
}
func TestPermissionMapping(t *testing.T) {
	p := json.RawMessage(`{"permissions":{"network":{"enabled":true}}}`)
	yes, _ := json.Marshal(permissionResult(p, true))
	no, _ := json.Marshal(permissionResult(p, false))
	if !strings.Contains(string(yes), `"enabled":true`) || !strings.Contains(string(yes), `"scope":"turn"`) {
		t.Fatal(string(yes))
	}
	if strings.Contains(string(no), "network") {
		t.Fatal("denial granted network access")
	}
}

// This subprocess acts as Codex, exercising actual pipes and request IDs without model usage.
func TestFakeCodexProcess(t *testing.T) {
	if os.Getenv("TEST_FAKE_CODEX") != "1" {
		return
	}
	defer os.Exit(0)
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			os.Exit(3)
		}
	}
	for {
		var m rpcMessage
		if dec.Decode(&m) != nil {
			return
		}
		switch m.Method {
		case "initialize":
			send(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "thread/start":
			var p map[string]any
			json.Unmarshal(m.Params, &p)
			if p["approvalPolicy"] != "untrusted" || p["sandbox"] != "workspace-write" || p["approvalsReviewer"] != "user" {
				os.Exit(4)
			}
			if os.Getenv("BRIDGE_TOKEN") != "" || os.Getenv("TELEGRAM_BOT_TOKEN") != "" {
				os.Exit(5)
			}
			send(map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]string{"id": "thread-test"}}})
		case "turn/start":
			send(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]string{"id": "turn-test"}}})
			send(map[string]any{"method": "item/started", "params": map[string]any{"item": map[string]any{"id": "item-test", "type": "commandExecution", "command": "npm install"}}})
			send(map[string]any{"id": "server-request-1", "method": "item/commandExecution/requestApproval", "params": map[string]any{"itemId": "item-test", "threadId": "thread-test", "turnId": "turn-test", "command": "npm install"}})
		case "":
			var result decision
			json.Unmarshal(m.Result, &result)
			if string(m.ID) != `"server-request-1"` || result.Decision != "accept" {
				os.Exit(6)
			}
			send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"delta": "approved and completed"}})
			send(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]string{"status": "completed"}}})
		}
	}
}
func TestTerminalToTelegramEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("shell required for fake Codex launcher")
	}
	_, srv := fakeBackend(t, "accept")
	t.Setenv("BRIDGE_TOKEN", testToken)
	t.Setenv("TELEGRAM_BOT_TOKEN", "must-not-reach-codex")
	t.Setenv("TEST_FAKE_CODEX", "1")
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec '"+strings.ReplaceAll(testBinary, "'", "'\\''")+"' -test.run=^TestFakeCodexProcess$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	err = run(ctx, []string{"--codex", script, "--bridge", srv.URL, "--approval", "untrusted", "--prompt", "install dependencies"}, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "approved and completed") {
		t.Fatalf("missing output: %s", out.String())
	}
}
