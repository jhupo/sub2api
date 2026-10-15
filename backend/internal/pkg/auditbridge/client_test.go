package auditbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const testToken = "test-bridge-token-at-least-16"

func testMetadata() Metadata {
	return Metadata{RequestID: "request-123", UserID: 12, Username: "customer", APIKeyID: 34, APIKeyName: "customer-key", Stage: "http", ClientModel: "client-model"}.WithID("content-hash", "/v1/chat/completions")
}

func TestCaptureSurvivesRestartAndRequiresPersistenceAcknowledgement(t *testing.T) {
	var calls atomic.Int32
	var accept atomic.Bool
	meta := testMetadata()
	body := []byte(`{"messages":[{"role":"user","content":"请总结完整文档"}],"temperature":0.3}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/bridge/v1/requests" || r.Header.Get("X-Bridge-Token") != testToken {
			t.Error("missing upload authentication or path")
		}
		var got Upload
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got.Context != meta || !bytes.Equal(got.RequestBody, body) {
			t.Errorf("upload lost content or identity: %+v", got.Context)
		}
		id := "incorrect-ack"
		if accept.Load() {
			id = meta.AuditID
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id})
	}))
	defer server.Close()
	dir := t.TempDir()
	client, err := New(server.URL, testToken, dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Capture(meta, body, "/v1/chat/completions"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("capture performed a synchronous network call")
	}
	used := client.bytes
	if err := client.Capture(meta, body, "/v1/chat/completions"); err != nil || client.bytes != used {
		t.Fatal("duplicate capture used additional outbox space")
	}
	path := filepath.Join(dir, meta.AuditID+".json")
	if err := client.deliver(context.Background(), path); err == nil {
		t.Fatal("accepted acknowledgement for another event")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed upload was removed", err)
	}
	client, err = New(server.URL, testToken, dir, 1<<20)
	if err != nil || client.bytes != used {
		t.Fatalf("outbox not recovered: %v", err)
	}
	accept.Store(true)
	if err := client.deliver(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || client.bytes != 0 {
		t.Fatalf("acknowledged upload still queued: %v", err)
	}
}

func TestAuditMetadataIsScopedAndRedirectsDoNotLeak(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1); w.WriteHeader(200) }))
	defer destination.Close()
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer bridge.Close()
	client, err := New(bridge.URL+"/audit", testToken, t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	previous := active.Swap(client)
	t.Cleanup(func() { active.Store(previous) })
	ctx := WithContext(context.Background(), testMetadata())
	raw := []byte(`{"model":"guard","messages":[{"role":"user","content":"hello"}]}`)
	for _, tc := range []struct {
		url    string
		attach bool
	}{
		{bridge.URL + "/audit/v1/chat/completions", true},
		{bridge.URL + "/audit-other/v1/chat/completions", false},
		{destination.URL + "/audit/v1/chat/completions", false},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tc.url, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := Apply(ctx, req, raw); err != nil {
			t.Fatal(err)
		}
		actual, _ := io.ReadAll(req.Body)
		if strings.Contains(string(actual), "audit_context") != tc.attach || (req.Header.Get("X-Bridge-Token") == testToken) != tc.attach {
			t.Fatalf("wrong attachment scope for %s", tc.url)
		}
		if req.ContentLength != int64(len(actual)) {
			t.Fatal("content length not updated")
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, bridge.URL+"/audit/v1/chat/completions", bytes.NewReader(raw))
	if err := Apply(ctx, req, raw); err != nil {
		t.Fatal(err)
	}
	resp, err := Do(bridge.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || destinationCalls.Load() != 0 {
		t.Fatal("audit metadata followed a redirect")
	}
	if bridge.Client().CheckRedirect != nil {
		t.Fatal("shared HTTP client was mutated")
	}
}

func TestAuditIdentityAndOutboxCapacity(t *testing.T) {
	meta := testMetadata()
	if meta != meta.WithID("content-hash", "/v1/chat/completions") {
		t.Fatal("retry audit ID changed")
	}
	other := meta
	other.APIKeyID++
	if other.WithID("content-hash", "/v1/chat/completions").AuditID == meta.AuditID {
		t.Fatal("different customers merged")
	}
	client, err := New("http://127.0.0.1:8000", testToken, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Capture(meta, []byte(`{"input":"hello"}`), "/v1/moderations"); err == nil {
		t.Fatal("outbox limit ignored")
	}
	if err := client.Capture(meta, []byte(`not-json`), "/v1/moderations"); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	meta.UserID = 0
	if err := client.Capture(meta, []byte(`{}`), "/v1/moderations"); err == nil {
		t.Fatal("missing identity accepted")
	}
}
