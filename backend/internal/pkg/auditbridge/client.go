// Package auditbridge sends complete requests to guard-bridge through a durable
// local outbox. It is opt-in and does not change any moderation decision.
package auditbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Metadata struct {
	AuditID     string `json:"audit_id"`
	RequestID   string `json:"request_id"`
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	APIKeyID    int64  `json:"api_key_id"`
	APIKeyName  string `json:"api_key_name"`
	Stage       string `json:"stage,omitempty"`
	ClientModel string `json:"client_model,omitempty"`
}

func (m Metadata) WithID(contentHash, path string) Metadata {
	// User/key/stage/content distinguish identical prompts from different callers
	// and WebSocket turns. Retries of one audit retain the same identity.
	raw, _ := json.Marshal([]any{m.RequestID, m.UserID, m.APIKeyID, m.Stage, contentHash, path})
	sum := sha256.Sum256(raw)
	m.AuditID = hex.EncodeToString(sum[:])
	return m
}

func Hash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

type contextKey struct{}

func WithContext(ctx context.Context, m Metadata) context.Context {
	return context.WithValue(ctx, contextKey{}, m)
}
func FromContext(ctx context.Context) (Metadata, bool) {
	m, ok := ctx.Value(contextKey{}).(Metadata)
	return m, ok
}

type Upload struct {
	Context     Metadata        `json:"audit_context"`
	TS          time.Time       `json:"ts"`
	Path        string          `json:"path"`
	RequestBody json.RawMessage `json:"request_body"`
}

type Client struct {
	base   *url.URL
	token  string
	dir    string
	limit  int64
	http   *http.Client
	mu     sync.Mutex
	bytes  int64
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var active atomic.Pointer[Client]

func StartFromEnv() (func(), error) {
	base := strings.TrimSpace(os.Getenv("GUARD_BRIDGE_URL"))
	if base == "" {
		return func() {}, nil
	}
	dir := strings.TrimSpace(os.Getenv("GUARD_BRIDGE_OUTBOX_DIR"))
	if dir == "" {
		dir = "./data/guard-bridge-outbox"
	}
	limit := int64(512 << 20)
	if value := os.Getenv("GUARD_BRIDGE_OUTBOX_MAX_BYTES"); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n <= 0 {
			return nil, errors.New("invalid GUARD_BRIDGE_OUTBOX_MAX_BYTES")
		}
		limit = n
	}
	c, err := New(base, os.Getenv("GUARD_BRIDGE_TOKEN"), dir, limit)
	if err != nil {
		return nil, err
	}
	c.Start()
	active.Store(c)
	return func() { active.Store(nil); c.Close() }, nil
}

func New(base, token, dir string, limit int64) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(token) < 16 {
		return nil, errors.New("GUARD_BRIDGE_URL and a GUARD_BRIDGE_TOKEN of at least 16 characters are required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	c := &Client{base: u, token: token, dir: dir, limit: limit, http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			c.bytes += info.Size()
		}
	}
	return c, nil
}

func Enabled() bool { return active.Load() != nil }

// Capture flushes locally, then returns without waiting for a network call.
func Capture(m Metadata, body []byte, path string) {
	c := active.Load()
	if c == nil {
		return
	}
	if err := c.Capture(m, body, path); err != nil {
		slog.Error("guard_bridge.capture_failed", "audit_id", m.AuditID, "reason", err.Error())
	}
}

func (c *Client) Capture(m Metadata, body []byte, path string) error {
	if m.UserID <= 0 || m.APIKeyID <= 0 || m.RequestID == "" || len(m.AuditID) != 64 {
		return errors.New("missing audit identity")
	}
	if _, err := hex.DecodeString(m.AuditID); err != nil {
		return errors.New("invalid audit ID")
	}
	raw, err := json.Marshal(Upload{Context: m, TS: time.Now().UTC(), Path: path, RequestBody: body})
	if err != nil {
		return errors.New("invalid request JSON")
	}
	if len(raw) > 16<<20 {
		return errors.New("request exceeds bridge upload limit (16 MiB)")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dest := filepath.Join(c.dir, m.AuditID+".json")
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if c.bytes+int64(len(raw)) > c.limit {
		return errors.New("outbox capacity exceeded")
	}
	f, err := os.CreateTemp(c.dir, ".upload-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temp, dest); err != nil {
		return err
	}
	c.bytes += int64(len(raw))
	return nil
}

// Attach is deliberately scoped to the configured bridge origin and base path.
// Other moderation providers must never receive customer identity or this token.
func Attach(req *http.Request, payload map[string]any, m Metadata) {
	c := active.Load()
	if c == nil || !c.matches(req.URL) {
		return
	}
	payload["audit_context"] = m
	req.Header.Set("X-Bridge-Token", c.token)
}

func (c *Client) matches(u *url.URL) bool {
	return strings.EqualFold(u.Scheme, c.base.Scheme) && strings.EqualFold(u.Host, c.base.Host) && strings.HasPrefix(u.Path, strings.TrimRight(c.base.Path, "/")+"/")
}

// Enrich runs before marshaling an existing moderation request body.
func Enrich(ctx context.Context, req *http.Request, body []byte) ([]byte, error) {
	m, ok := FromContext(ctx)
	if !ok {
		return body, nil
	}
	c := active.Load()
	if c == nil || !c.matches(req.URL) {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	Attach(req, payload, m)
	return json.Marshal(payload)
}

func Apply(ctx context.Context, req *http.Request, body []byte) error {
	raw, err := Enrich(ctx, req, body)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.ContentLength = int64(len(raw))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	return nil
}

// Do prevents redirects from forwarding audit metadata and the shared bridge
// token to another destination. Copying preserves the caller's pooled transport
// without mutating a client shared by unrelated requests.
func Do(client *http.Client, req *http.Request) (*http.Response, error) {
	if req.Header.Get("X-Bridge-Token") != "" {
		copy := *client
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return copy.Do(req)
	}
	return client.Do(req)
}

func (c *Client) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		retry := map[string]time.Time{}
		for {
			if ctx.Err() != nil {
				return
			}
			entries, err := os.ReadDir(c.dir)
			if err == nil {
				for _, entry := range entries {
					if ctx.Err() != nil {
						return
					}
					if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || time.Now().Before(retry[entry.Name()]) {
						continue
					}
					path := filepath.Join(c.dir, entry.Name())
					if err := c.deliver(ctx, path); err != nil {
						retry[entry.Name()] = time.Now().Add(30 * time.Second)
						slog.Warn("guard_bridge.delivery_retry", "file", entry.Name(), "reason", err.Error())
					} else {
						delete(retry, entry.Name())
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
}

func (c *Client) deliver(ctx context.Context, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.base.String(), "/")+"/bridge/v1/requests", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("X-Bridge-Token", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("bridge connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bridge HTTP %d", resp.StatusCode)
	}
	var ack struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&ack) != nil || !ack.OK || ack.ID != strings.TrimSuffix(filepath.Base(path), ".json") {
		return errors.New("invalid persistence acknowledgement")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.Remove(path); err != nil {
		return err
	}
	c.bytes -= int64(len(raw))
	return nil
}

func (c *Client) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}
