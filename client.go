package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}
type rpcClient struct {
	workers               sync.WaitGroup
	ctx                   context.Context
	writeMu               sync.Mutex
	stdin                 io.WriteCloser
	mu                    sync.Mutex
	next                  int
	calls                 map[string]chan rpcMessage
	requests              map[string]context.CancelFunc
	items                 map[string]json.RawMessage
	events                chan rpcMessage
	done                  chan struct{}
	endpoint, token, host string
	errOut                io.Writer
}

func (r *rpcClient) send(v any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return json.NewEncoder(r.stdin).Encode(v)
}
func (r *rpcClient) call(ctx context.Context, method string, params any, result any) error {
	r.mu.Lock()
	r.next++
	id := r.next
	key := strconv.Itoa(id)
	ch := make(chan rpcMessage, 1)
	r.calls[key] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.calls, key); r.mu.Unlock() }()
	if err := r.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return errors.New("Codex app-server disconnected")
	case m := <-ch:
		if len(m.Error) > 0 {
			return fmt.Errorf("Codex %s: %s", method, m.Error)
		}
		if result != nil {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	}
}
func (r *rpcClient) read(stdout io.Reader) {
	defer close(r.done)
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, cancel := range r.requests {
			cancel()
		}
	}()
	s := bufio.NewScanner(stdout)
	s.Buffer(make([]byte, 4096), 4<<20)
	for s.Scan() {
		var m rpcMessage
		if err := json.Unmarshal(s.Bytes(), &m); err != nil {
			fmt.Fprintln(r.errOut, "Invalid app-server JSON; closing session.")
			return
		}
		if len(m.ID) > 0 && m.Method == "" {
			r.mu.Lock()
			ch := r.calls[string(m.ID)]
			r.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if len(m.ID) > 0 {
			if !validMethod(m.Method) {
				fmt.Fprintf(r.errOut, "Unsupported Codex request %s; returning an error.\n", m.Method)
				_ = r.send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "This client supports command, file-change, and permission approvals only"}})
				continue
			}
			var p struct {
				ItemID string `json:"itemId"`
			}
			json.Unmarshal(m.Params, &p)
			ctx, cancel := context.WithCancel(r.ctx)
			r.mu.Lock()
			r.requests[string(m.ID)] = cancel
			item := r.items[p.ItemID]
			r.mu.Unlock()
			fmt.Fprintf(r.errOut, "\nApproval requested: %s. Waiting for Telegram.\n", m.Method)
			r.workers.Add(1)
			go func() { defer r.workers.Done(); r.answer(ctx, m, item) }()
			continue
		}
		// Capture the full item (including file diffs) before an approval arrives.
		if m.Method == "item/started" || m.Method == "item/completed" {
			var p struct {
				Item json.RawMessage `json:"item"`
			}
			json.Unmarshal(m.Params, &p)
			var item struct {
				ID string `json:"id"`
			}
			json.Unmarshal(p.Item, &item)
			r.mu.Lock()
			if m.Method == "item/started" {
				r.items[item.ID] = p.Item
			} else {
				delete(r.items, item.ID)
			}
			r.mu.Unlock()
		}
		if m.Method == "serverRequest/resolved" {
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			json.Unmarshal(m.Params, &p)
			r.mu.Lock()
			if cancel := r.requests[string(p.RequestID)]; cancel != nil {
				cancel()
			}
			r.mu.Unlock()
		}
		select {
		case r.events <- m:
		case <-r.ctx.Done():
			return
		}
	}
	if err := s.Err(); err != nil {
		fmt.Fprintln(r.errOut, "Reading Codex app-server failed:", err)
	}
}
func permissionResult(params json.RawMessage, accept bool) any {
	if !accept {
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	}
	var p struct {
		Permissions json.RawMessage `json:"permissions"`
	}
	json.Unmarshal(params, &p)
	if len(p.Permissions) == 0 || string(p.Permissions) == "null" {
		p.Permissions = json.RawMessage(`{}`)
	}
	return map[string]any{"permissions": p.Permissions, "scope": "turn"}
}
func (r *rpcClient) answer(ctx context.Context, m rpcMessage, item json.RawMessage) {
	defer func() {
		r.mu.Lock()
		if cancel := r.requests[string(m.ID)]; cancel != nil {
			cancel()
		}
		delete(r.requests, string(m.ID))
		r.mu.Unlock()
	}()
	choice, err := requestDecision(ctx, r.endpoint, r.token, approval{Method: m.Method, Host: r.host, Params: m.Params, Item: item})
	if ctx.Err() != nil {
		return
	} // Codex already resolved it or the session ended.
	if err != nil {
		fmt.Fprintln(r.errOut, "Telegram approval unavailable; denying:", err)
		choice = "decline"
	}
	result := any(decision{Decision: choice})
	if m.Method == "item/permissions/requestApproval" {
		result = permissionResult(m.Params, choice == "accept")
	}
	_ = r.send(map[string]any{"id": m.ID, "result": result})
	fmt.Fprintf(r.errOut, "Approval decision: %s\n", choice)
}
func requestDecision(ctx context.Context, endpoint, token string, a approval) (string, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return "decline", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(endpoint, "/")+"/v1/approvals", bytes.NewReader(b))
	if err != nil {
		return "decline", errors.New("invalid bridge URL")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 31 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		// Do not print the URL (which may contain private path information) or
		// headers, but retain the transport error needed to diagnose failures.
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return "decline", fmt.Errorf("bridge connection failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "decline", fmt.Errorf("bridge returned HTTP %d", res.StatusCode)
	}
	var d decision
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&d); err != nil {
		return "decline", errors.New("invalid bridge response")
	}
	if d.Decision != "accept" && d.Decision != "decline" {
		return "decline", errors.New("bridge returned an unsupported decision")
	}
	return d.Decision, nil
}

type lockedWriter struct {
	mu  sync.Mutex
	dst io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dst.Write(p)
}

func run(ctx context.Context, args []string, input io.Reader, output, errOut io.Writer) error {
	errOut = &lockedWriter{dst: errOut}
	f := flag.NewFlagSet("run", flag.ContinueOnError)
	f.SetOutput(errOut)
	cwd := f.String("cwd", "", "Working directory (default: current directory)")
	prompt := f.String("prompt", "", "Run one prompt and exit; omit for a line-based terminal chat")
	resume := f.String("resume", "", "Resume a thread ID (do not run it concurrently elsewhere)")
	policy := f.String("approval", "on-request", "Approval policy: on-request or untrusted")
	model := f.String("model", "", "Optional model override; default uses your Codex configuration")
	binary := f.String("codex", "codex", "Local Codex executable")
	endpoint := f.String("bridge", env("BRIDGE_URL", "http://127.0.0.1:8787"), "Approval backend URL")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() > 0 {
		return errors.New("unexpected arguments; use --prompt 'your task'")
	}
	if *policy != "on-request" && *policy != "untrusted" {
		return errors.New("approval must be on-request or untrusted")
	}
	token := os.Getenv("BRIDGE_TOKEN")
	if len(token) < 32 {
		return errors.New("BRIDGE_TOKEN must contain at least 32 characters")
	}
	u, err := url.Parse(*endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid bridge URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return errors.New("remote bridge connections require HTTPS")
	}
	if *cwd == "" {
		*cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	*cwd, err = filepath.Abs(*cwd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, *binary, "app-server", "--listen", "stdio://")
	cmd.Dir = *cwd
	// Codex needs the bridge credential to be kept outside its child environment.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "TELEGRAM_BOT_TOKEN=") && !strings.HasPrefix(entry, "BRIDGE_TOKEN=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Stderr = errOut
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	host, _ := os.Hostname()
	r := &rpcClient{ctx: ctx, stdin: stdin, calls: make(map[string]chan rpcMessage), requests: make(map[string]context.CancelFunc), items: make(map[string]json.RawMessage), events: make(chan rpcMessage, 1024), done: make(chan struct{}), endpoint: *endpoint, token: token, host: fmt.Sprintf("%s pid=%d", host, os.Getpid()), errOut: errOut}
	go r.read(stdout)
	defer func() { cancel(); stdin.Close(); cmd.Wait(); <-r.done; r.workers.Wait() }()
	initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
	defer initCancel()
	if err := r.call(initCtx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "codex_telegram_approve", "title": "Codex Telegram Approve", "version": "0.1.0"}}, nil); err != nil {
		return err
	}
	if err := r.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	params := map[string]any{"cwd": *cwd, "approvalPolicy": *policy, "approvalsReviewer": "user", "sandbox": "workspace-write"}
	if *model != "" {
		params["model"] = *model
	}
	method := "thread/start"
	if *resume != "" {
		method = "thread/resume"
		params["threadId"] = *resume
	}
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := r.call(initCtx, method, params, &result); err != nil {
		return err
	}
	if result.Thread.ID == "" {
		return errors.New("Codex returned an empty thread ID")
	}
	fmt.Fprintf(errOut, "Thread: %s\nWorkspace: %s\nTelegram approvals enabled; Ctrl-C exits.\n", result.Thread.ID, *cwd)
	lines := make(chan string)
	go func() {
		defer close(lines)
		s := bufio.NewScanner(input)
		s.Buffer(make([]byte, 4096), 1<<20)
		for s.Scan() {
			select {
			case lines <- s.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	single := *prompt != ""
	for {
		text := *prompt
		if !single {
			fmt.Fprint(errOut, "\ncodex> ")
			select {
			case <-ctx.Done():
				return nil
			case <-r.done:
				return errors.New("Codex disconnected")
			case line, ok := <-lines:
				if !ok {
					return nil
				}
				text = line
			}
			if text == "/quit" || text == "/exit" {
				return nil
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
		}
		if err := r.call(ctx, "turn/start", map[string]any{"threadId": result.Thread.ID, "input": []map[string]string{{"type": "text", "text": text}}}, nil); err != nil {
			return err
		}
		finished := false
		for !finished {
			select {
			case <-ctx.Done():
				return nil
			case <-r.done:
				return errors.New("Codex disconnected")
			case m := <-r.events:
				switch m.Method {
				case "item/agentMessage/delta":
					var p struct {
						Delta string `json:"delta"`
					}
					json.Unmarshal(m.Params, &p)
					fmt.Fprint(output, p.Delta)
				case "item/commandExecution/outputDelta":
					var p struct {
						Delta string `json:"delta"`
					}
					json.Unmarshal(m.Params, &p)
					fmt.Fprint(output, p.Delta)
				case "item/completed":
					var p struct {
						Item struct {
							Type string `json:"type"`
						} `json:"item"`
					}
					json.Unmarshal(m.Params, &p)
					if p.Item.Type == "agentMessage" {
						fmt.Fprintln(output)
					}
				case "error":
					fmt.Fprintf(errOut, "\nCodex error: %s\n", m.Params)
				case "turn/completed":
					var p struct {
						Turn struct {
							Status string          `json:"status"`
							Error  json.RawMessage `json:"error"`
						} `json:"turn"`
					}
					json.Unmarshal(m.Params, &p)
					fmt.Fprintf(errOut, "\nTurn %s\n", p.Turn.Status)
					if p.Turn.Status != "completed" {
						return fmt.Errorf("Codex turn %s: %s", p.Turn.Status, p.Turn.Error)
					}
					finished = true
				}
			}
		}
		if single {
			return nil
		}
	}
}
